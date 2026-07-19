package webfetch

import (
	"net/netip"
	"net/url"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

var publicIPv6Prefix = netip.MustParsePrefix("2000::/3")

var deniedAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func parseTargetURL(raw string, maxURLBytes int) (*url.URL, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > maxURLBytes || strings.ContainsAny(raw, "\x00\r\n\t") {
		return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeRequestInvalid, false, errInvalidRequest)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Scheme == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" ||
		parsed.Hostname() == "" || parsed.Scheme != strings.ToLower(parsed.Scheme) ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || !validPort(parsed.Port()) || !validURLHost(parsed) {
		return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeRequestInvalid, false, errInvalidRequest)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed, nil
}

func validURLHost(value *url.URL) bool {
	host := value.Host
	if len(host) > 320 || strings.ContainsAny(host, " \t\r\n\\/@?#") {
		return false
	}
	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing < 0 || closing == 1 {
			return false
		}
		remainder := host[closing+1:]
		if remainder != "" && (len(remainder) < 2 || remainder[0] != ':' || remainder[1:] == "") {
			return false
		}
		address, err := netip.ParseAddr(value.Hostname())
		return err == nil && address.Zone() == ""
	}
	if strings.Count(host, ":") > 1 || (strings.Count(host, ":") == 1 && value.Port() == "") {
		return false
	}
	hostname := value.Hostname()
	if len(hostname) > 253 || hostname == "" {
		return false
	}
	if address, err := netip.ParseAddr(hostname); err == nil {
		return address.Zone() == ""
	}
	return validDNSName(hostname)
}

func validDNSName(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func allowedPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || address.Zone() != "" || !address.IsGlobalUnicast() || address.IsUnspecified() || address.IsLoopback() ||
		address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
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
