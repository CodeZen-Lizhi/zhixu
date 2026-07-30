package models

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"
)

type modelResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (resolve modelResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return resolve(ctx, network, host)
}

type modelDialerFunc func(context.Context, string, string) (net.Conn, error)

func (dial modelDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return dial(ctx, network, address)
}

func TestModelAddressAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		address       string
		allowLoopback bool
		want          bool
	}{
		{name: "public ipv4", address: "8.8.8.8", want: true},
		{name: "public ipv6", address: "2606:4700:4700::1111", want: true},
		{name: "fixed loopback relay", address: "127.0.0.1", allowLoopback: true, want: true},
		{name: "other ipv4 loopback relay", address: "127.0.0.2", allowLoopback: true, want: false},
		{name: "ipv6 loopback relay", address: "::1", allowLoopback: true, want: false},
		{name: "loopback remote", address: "127.0.0.1", want: false},
		{name: "private", address: "10.0.0.1", want: false},
		{name: "link local", address: "169.254.169.254", want: false},
		{name: "carrier nat", address: "100.64.0.1", want: false},
		{name: "documentation", address: "203.0.113.8", want: false},
		{name: "deprecated relay", address: "192.88.99.1", want: false},
		{name: "ipv6 private", address: "fd00::1", want: false},
		{name: "ipv6 translation", address: "64:ff9b::1", want: false},
		{name: "ipv6 discard only", address: "100::1", want: false},
		{name: "ipv6 documentation", address: "2001:db8::1", want: false},
		{name: "ipv6 six to four", address: "2002::1", want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			address := netip.MustParseAddr(test.address)
			if got := modelAddressAllowed(address, test.allowLoopback); got != test.want {
				t.Fatalf("modelAddressAllowed(%s, %t) = %t, want %t", address, test.allowLoopback, got, test.want)
			}
		})
	}
}

func TestModelDialContextBindsOriginalHostnameAndPort(t *testing.T) {
	t.Parallel()
	dialCalls := 0
	dial := newTestModelDialContext(t, "https://Models.Example.Test:8443", modelResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("resolver must not run for a mismatched authority")
		return nil, nil
	}), modelDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("unexpected dial")
	}))

	for _, address := range []string{"other.example.test:8443", "models.example.test:443"} {
		if _, err := dial(context.Background(), "tcp", address); !errors.Is(err, errModelEndpointBlocked) {
			t.Fatalf("dial(%q) error = %v, want endpoint blocked", address, err)
		}
	}
	if _, err := dial(context.Background(), "udp", "models.example.test:8443"); !errors.Is(err, errModelEndpointBlocked) {
		t.Fatalf("non-TCP dial error = %v, want endpoint blocked", err)
	}
	if dialCalls != 0 {
		t.Fatalf("dial calls = %d, want 0", dialCalls)
	}
}

func TestModelDialContextRejectsMixedDNSBeforeDial(t *testing.T) {
	t.Parallel()
	dialCalls := 0
	dial := newTestModelDialContext(t, "https://models.example.test", staticModelResolver("8.8.8.8", "10.0.0.8"), modelDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("unexpected dial")
	}))

	if _, err := dial(context.Background(), "tcp", "models.example.test:443"); !errors.Is(err, errModelEndpointBlocked) {
		t.Fatalf("dial error = %v, want endpoint blocked", err)
	}
	if dialCalls != 0 {
		t.Fatalf("dial calls = %d, want 0", dialCalls)
	}
}

func TestModelDialContextFallsBackAcrossIPv4AndIPv6(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		addresses []string
	}{
		{name: "ipv6 then ipv4", addresses: []string{"2606:4700:4700::1111", "8.8.8.8"}},
		{name: "ipv4 then ipv6", addresses: []string{"8.8.8.8", "2606:4700:4700::1111"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var dialed []string
			var deadlines []time.Time
			dial := newTestModelDialContext(t, "https://models.example.test", staticModelResolver(test.addresses...), modelDialerFunc(func(ctx context.Context, _ string, address string) (net.Conn, error) {
				dialed = append(dialed, address)
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("dial context has no deadline")
				}
				deadlines = append(deadlines, deadline)
				if len(dialed) == 1 {
					return nil, errors.New("first address unavailable")
				}
				client, peer := net.Pipe()
				t.Cleanup(func() { peer.Close() })
				return client, nil
			}))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connection, err := dial(ctx, "tcp", "models.example.test:443")
			if err != nil {
				t.Fatal(err)
			}
			connection.Close()

			wantDialed := []string{
				net.JoinHostPort(test.addresses[0], "443"),
				net.JoinHostPort(test.addresses[1], "443"),
			}
			if len(dialed) != len(wantDialed) || dialed[0] != wantDialed[0] || dialed[1] != wantDialed[1] {
				t.Fatalf("dialed = %v, want %v", dialed, wantDialed)
			}
			if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
				t.Fatalf("dial deadlines = %v, want one shared deadline", deadlines)
			}
		})
	}
}

func TestModelDialContextResolvesEveryNewConnectionAndRejectsRebind(t *testing.T) {
	t.Parallel()
	resolveCalls := 0
	dialCalls := 0
	resolver := modelResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		resolveCalls++
		if resolveCalls == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil
	})
	dial := newTestModelDialContext(t, "https://models.example.test", resolver, modelDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		client, peer := net.Pipe()
		t.Cleanup(func() { peer.Close() })
		return client, nil
	}))

	connection, err := dial(context.Background(), "tcp", "models.example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	if _, err := dial(context.Background(), "tcp", "models.example.test:443"); !errors.Is(err, errModelEndpointBlocked) {
		t.Fatalf("second connection error = %v, want endpoint blocked", err)
	}
	if resolveCalls != 2 {
		t.Fatalf("resolve calls = %d, want 2", resolveCalls)
	}
	if dialCalls != 1 {
		t.Fatalf("dial calls = %d, want 1", dialCalls)
	}
}

func TestModelDialContextAllowsOnlyFixedIPv4LoopbackRelay(t *testing.T) {
	t.Parallel()
	t.Run("fixed relay", func(t *testing.T) {
		dial := newTestModelDialContext(t, "http://127.0.0.1:11434", modelResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			t.Fatal("literal address must not use the resolver")
			return nil, nil
		}), modelDialerFunc(func(_ context.Context, _ string, address string) (net.Conn, error) {
			if address != "127.0.0.1:11434" {
				t.Fatalf("dial address = %q, want fixed relay", address)
			}
			client, peer := net.Pipe()
			t.Cleanup(func() { peer.Close() })
			return client, nil
		}))
		connection, err := dial(context.Background(), "tcp", "127.0.0.1:11434")
		if err != nil {
			t.Fatal(err)
		}
		connection.Close()
	})

	for _, test := range []struct {
		name     string
		endpoint string
		address  string
		resolver modelHostResolver
	}{
		{name: "localhost alias", endpoint: "http://localhost:11434", address: "localhost:11434", resolver: staticModelResolver("127.0.0.1")},
		{name: "other ipv4 loopback", endpoint: "http://127.0.0.2:11434", address: "127.0.0.2:11434", resolver: staticModelResolver("8.8.8.8")},
		{name: "ipv6 loopback", endpoint: "http://[::1]:11434", address: "[::1]:11434", resolver: staticModelResolver("8.8.8.8")},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			dial := newTestModelDialContext(t, test.endpoint, test.resolver, modelDialerFunc(func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("blocked loopback endpoint must not be dialed")
				return nil, nil
			}))
			if _, err := dial(context.Background(), "tcp", test.address); !errors.Is(err, errModelEndpointBlocked) {
				t.Fatalf("dial error = %v, want endpoint blocked", err)
			}
		})
	}
}

func TestModelHTTPClientKeepsOriginalTLSHostname(t *testing.T) {
	observed := make(chan struct {
		host string
		sni  string
	}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		observed <- struct {
			host string
			sni  string
		}{host: request.Host, sni: request.TLS.ServerName}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse("https://example.com:" + serverURL.Port() + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	var dialed string
	client, err := newModelHTTPClientWithNetwork(endpoint, nil, staticModelResolver("8.8.8.8"), modelDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = address
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}))
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.ServerName != endpoint.Hostname() || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("TLS config = %#v, want fixed hostname and TLS 1.2+", transport.TLSClientConfig)
	}
	trustedTransport := server.Client().Transport.(*http.Transport)
	transport.TLSClientConfig.RootCAs = trustedTransport.TLSClientConfig.RootCAs
	transport.TLSClientConfig.InsecureSkipVerify = trustedTransport.TLSClientConfig.InsecureSkipVerify // test server trust only

	response, err := client.Get(endpoint.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if dialed != net.JoinHostPort("8.8.8.8", serverURL.Port()) {
		t.Fatalf("dialed = %q, want validated resolved address", dialed)
	}
	got := <-observed
	if got.host != endpoint.Host || got.sni != endpoint.Hostname() {
		t.Fatalf("observed host/SNI = %q/%q, want %q/%q", got.host, got.sni, endpoint.Host, endpoint.Hostname())
	}
}

func TestModelHTTPClientDisablesProxyAndRedirects(t *testing.T) {
	t.Parallel()
	endpoint, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	client, err := newModelHTTPClient(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.DialContext == nil || transport.TLSClientConfig == nil ||
		transport.TLSClientConfig.ServerName != endpoint.Hostname() || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("model transport is not hardened: %#v", client.Transport)
	}
	request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); err != http.ErrUseLastResponse {
		t.Fatalf("redirect error = %v, want %v", err, http.ErrUseLastResponse)
	}
}

func newTestModelDialContext(t *testing.T, rawEndpoint string, resolver modelHostResolver, dialer modelContextDialer) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	endpoint, err := url.Parse(rawEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := newModelEndpointAuthority(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return modelDialContext(authority, resolver, dialer)
}

func staticModelResolver(addresses ...string) modelHostResolver {
	return modelResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		result := make([]netip.Addr, 0, len(addresses))
		for _, address := range addresses {
			result = append(result, netip.MustParseAddr(address))
		}
		return result, nil
	})
}
