package models

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

var (
	errModelEndpointBlocked  = errors.New("model endpoint address is not allowed")
	errModelConnectionFailed = errors.New("model endpoint connection failed")
)

var modelLoopbackRelayAddress = netip.MustParseAddr("127.0.0.1")
var modelPublicIPv6Prefix = netip.MustParsePrefix("2000::/3")

type modelHostResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type modelContextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type modelEndpointAuthority struct {
	host               string
	port               string
	allowLoopbackRelay bool
}

type idleConnectionCloser interface {
	CloseIdleConnections()
}

// modelHTTPClient records transport ownership at construction. A supplied
// non-nil RoundTripper remains caller-owned even though the client is copied.
type modelHTTPClient struct {
	client    *http.Client
	owned     idleConnectionCloser
	closeOnce sync.Once
}

func (client *modelHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return client.client.Do(request)
}

func (client *modelHTTPClient) Close() error {
	if client == nil {
		return nil
	}
	client.closeOnce.Do(func() {
		if client.owned != nil {
			client.owned.CloseIdleConnections()
		}
	})
	return nil
}

// newModelHTTPClient creates the production model transport. Explicit custom
// transports remain an adapter test seam; production factories pass nil.
func newModelHTTPClient(endpoint *url.URL, supplied *http.Client) (*modelHTTPClient, error) {
	return newModelHTTPClientWithNetwork(endpoint, supplied, net.DefaultResolver, &net.Dialer{})
}

func newModelHTTPClientWithNetwork(endpoint *url.URL, supplied *http.Client, resolver modelHostResolver, dialer modelContextDialer) (*modelHTTPClient, error) {
	authority, err := newModelEndpointAuthority(endpoint)
	if err != nil {
		return nil, err
	}
	client := &http.Client{}
	if supplied != nil {
		*client = *supplied
	}
	var owned idleConnectionCloser
	if client.Transport == nil {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errModelEndpointBlocked
		}
		transport := base.Clone()
		transport.Proxy = nil
		transport.DialContext = modelDialContext(authority, resolver, dialer)
		transport.TLSClientConfig = modelTLSConfig(transport.TLSClientConfig, authority.host)
		client.Transport = transport
		owned = transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &modelHTTPClient{client: client, owned: owned}, nil
}

func modelTLSConfig(base *tls.Config, serverName string) *tls.Config {
	configured := &tls.Config{MinVersion: tls.VersionTLS12}
	if base != nil {
		configured = base.Clone()
		if configured.MinVersion < tls.VersionTLS12 {
			configured.MinVersion = tls.VersionTLS12
		}
	}
	configured.ServerName = serverName
	return configured
}

func newModelEndpointAuthority(endpoint *url.URL) (modelEndpointAuthority, error) {
	if endpoint == nil || endpoint.Hostname() == "" {
		return modelEndpointAuthority{}, errModelEndpointBlocked
	}
	host := normalizeModelHostname(endpoint.Hostname())
	if host == "" {
		return modelEndpointAuthority{}, errModelEndpointBlocked
	}
	port := endpoint.Port()
	if port == "" {
		switch endpoint.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return modelEndpointAuthority{}, errModelEndpointBlocked
		}
	}
	port, err := normalizeModelPort(port)
	if err != nil {
		return modelEndpointAuthority{}, errModelEndpointBlocked
	}
	return modelEndpointAuthority{
		host:               host,
		port:               port,
		allowLoopbackRelay: endpoint.Scheme == "http" && endpoint.Hostname() == modelLoopbackRelayAddress.String(),
	}, nil
}

func modelDialContext(authority modelEndpointAuthority, resolver modelHostResolver, dialer modelContextDialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, errModelEndpointBlocked
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil || normalizeModelHostname(host) != authority.host {
			return nil, errModelEndpointBlocked
		}
		port, err = normalizeModelPort(port)
		if err != nil || port != authority.port || resolver == nil || dialer == nil {
			return nil, errModelEndpointBlocked
		}
		addresses, err := resolveModelHost(ctx, resolver, authority.host)
		if err != nil || len(addresses) == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, errModelEndpointBlocked
		}
		for _, candidate := range addresses {
			if !modelAddressAllowed(candidate, authority.allowLoopbackRelay) {
				return nil, errModelEndpointBlocked
			}
		}
		for _, candidate := range addresses {
			connection, dialErr := dialer.DialContext(ctx, modelNetworkForAddress(candidate), net.JoinHostPort(candidate.String(), port))
			if dialErr == nil && connection != nil {
				return connection, nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
		}
		return nil, errModelConnectionFailed
	}
}

func modelNetworkForAddress(address netip.Addr) string {
	if address.Unmap().Is4() {
		return "tcp4"
	}
	return "tcp6"
}

func normalizeModelHostname(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func normalizeModelPort(port string) (string, error) {
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return "", errModelEndpointBlocked
	}
	return strconv.FormatUint(value, 10), nil
}

func resolveModelHost(ctx context.Context, resolver modelHostResolver, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{literal.Unmap()}, nil
	}
	resolved, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	result := make([]netip.Addr, 0, len(resolved))
	for _, address := range resolved {
		result = append(result, address.Unmap())
	}
	return result, nil
}

func modelAddressAllowed(address netip.Addr, allowLoopback bool) bool {
	address = address.Unmap()
	if allowLoopback {
		return address == modelLoopbackRelayAddress
	}
	if !address.IsValid() || address.Zone() != "" || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is6() && !modelPublicIPv6Prefix.Contains(address) {
		return false
	}
	for _, prefix := range modelReservedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var modelReservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}
