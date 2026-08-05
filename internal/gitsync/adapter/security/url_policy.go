// Package security implements Git remote URL and credential security boundaries.
package security

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/remoteurl"
)

const (
	maxRemoteURLBytes    = 2048
	maxResolvedAddresses = 16
)

// Resolver is the DNS seam used to reject private and special-use destinations.
type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// URLPolicy normalizes public HTTPS Git remote URLs.
type URLPolicy struct{ resolver Resolver }

// NewURLPolicy constructs a policy backed by the supplied resolver.
func NewURLPolicy(resolver Resolver) *URLPolicy {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &URLPolicy{resolver: resolver}
}

// NormalizeHTTPSRemote returns one canonical public HTTPS URL without credentials,
// query parameters, or fragments.
func (policy *URLPolicy) NormalizeHTTPSRemote(ctx context.Context, raw string) (string, error) {
	endpoint, err := policy.ResolveHTTPSRemote(ctx, raw)
	if err != nil {
		return "", err
	}
	return endpoint.URL, nil
}

// ResolveHTTPSRemote normalizes and freezes the public addresses allowed for
// one immediate remote command, preventing DNS rebinding between validation and I/O.
func (policy *URLPolicy) ResolveHTTPSRemote(ctx context.Context, raw string) (remoteurl.Endpoint, error) {
	if policy == nil || policy.resolver == nil {
		return remoteurl.Endpoint{}, policyError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New("Git remote URL policy is unavailable"))
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxRemoteURLBytes || strings.ContainsAny(raw, "\x00\r\n\t") {
		return remoteurl.Endpoint{}, invalidURL("Git remote URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return remoteurl.Endpoint{}, invalidURL("Git remote must be an HTTPS URL without credentials, query parameters, or fragments")
	}
	if parsed.Path == "" || parsed.Path == "/" || strings.Contains(parsed.EscapedPath(), "\\") || strings.Contains(parsed.Path, "//") {
		return remoteurl.Endpoint{}, invalidURL("Git remote URL must identify a repository path")
	}
	if parsed.RawPath != "" && parsed.EscapedPath() != parsed.RawPath {
		return remoteurl.Endpoint{}, invalidURL("Git remote URL path encoding is invalid")
	}
	if err := validatePort(parsed.Port()); err != nil {
		return remoteurl.Endpoint{}, err
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" || hostname == "localhost" || !validHost(hostname) {
		return remoteurl.Endpoint{}, invalidURL("Git remote host is invalid")
	}
	addresses, err := policy.resolvePublicHost(ctx, hostname)
	if err != nil {
		return remoteurl.Endpoint{}, err
	}

	parsed.Scheme = "https"
	parsed.Host = canonicalHost(hostname, parsed.Port())
	parsed.ForceQuery = false
	canonical := parsed.String()
	if len(canonical) > maxRemoteURLBytes {
		return remoteurl.Endpoint{}, invalidURL("Git remote URL is too long")
	}
	port := uint16(443)
	if parsed.Port() != "" {
		parsedPort, _ := strconv.ParseUint(parsed.Port(), 10, 16)
		port = uint16(parsedPort)
	}
	return remoteurl.Endpoint{URL: canonical, Hostname: hostname, Port: port, Addresses: addresses}, nil
}

func (policy *URLPolicy) resolvePublicHost(ctx context.Context, hostname string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(hostname); err == nil {
		if !allowedPublicAddress(literal) {
			return nil, invalidURL("Git remote host is not a public address")
		}
		return []netip.Addr{literal.Unmap()}, nil
	}
	addresses, err := policy.resolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, policyError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, errors.New("Git remote DNS lookup failed"))
	}
	if len(addresses) == 0 || len(addresses) > maxResolvedAddresses {
		return nil, policyError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, errors.New("Git remote host has an invalid DNS answer count"))
	}
	resolved := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, answer := range addresses {
		address, ok := netip.AddrFromSlice(answer.IP)
		if !ok || answer.Zone != "" || !allowedPublicAddress(address) {
			return nil, invalidURL("Git remote DNS resolved to a non-public address")
		}
		address = address.Unmap()
		if _, exists := seen[address]; !exists {
			seen[address] = struct{}{}
			resolved = append(resolved, address)
		}
	}
	sort.Slice(resolved, func(left, right int) bool { return resolved[left].Less(resolved[right]) })
	return resolved, nil
}

func validatePort(port string) error {
	if port == "" {
		return nil
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return invalidURL("Git remote port is invalid")
	}
	return nil
}

func canonicalHost(hostname, port string) string {
	host := hostname
	if address, err := netip.ParseAddr(hostname); err == nil && address.Is6() {
		host = "[" + address.String() + "]"
	}
	if port == "" || port == "443" {
		return host
	}
	return net.JoinHostPort(hostname, port)
}

func validHost(host string) bool {
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Zone() == ""
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

var publicIPv6Prefix = netip.MustParsePrefix("2000::/3")

var deniedAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func allowedPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || address.Zone() != "" || !address.IsGlobalUnicast() || address.IsUnspecified() ||
		address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	if address.Is6() && !publicIPv6Prefix.Contains(address) {
		return false
	}
	for _, prefix := range deniedAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func invalidURL(message string) error {
	return policyError(foundation.ErrorInvalidInput, domain.ErrorCodeURLInvalid, false, errors.New(message))
}

func policyError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}

var _ remoteurl.Policy = (*URLPolicy)(nil)
