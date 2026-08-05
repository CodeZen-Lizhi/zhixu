package httpfetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestFetcherRejectsLiteralAndDNSPrivateAddresses(t *testing.T) {
	fetcher := newTestFetcher(t, staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}, rejectingDialer{})
	for _, target := range []string{"http://127.0.0.1/private", "http://example.test/private"} {
		_, err := fetcher.Fetch(context.Background(), target)
		if fetchErrorCode(err) != "CAPTURE_URL_PRIVATE_ADDRESS_BLOCKED" {
			t.Fatalf("Fetch(%q) error = %#v", target, err)
		}
	}
}

func TestPublicIPRejectsSpecialUseRanges(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.0.0.1", "192.0.2.1", "192.168.0.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "240.0.0.1", "::1", "::ffff:127.0.0.1",
		"64:ff9b::1", "100::1", "2001:db8::1", "2002::1", "fc00::1", "fe80::1",
	} {
		if publicIP(net.ParseIP(raw)) {
			t.Fatalf("publicIP(%q) = true", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(raw)) {
			t.Fatalf("publicIP(%q) = false", raw)
		}
	}
}

func TestFetcherAllowsBoundedPublicHTMLThroughResolvedEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = response.Write([]byte("<html><body>Java AI</body></html>"))
	}))
	defer server.Close()
	serverAddress := strings.TrimPrefix(server.URL, "http://")
	fetcher := newTestFetcher(t,
		staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}},
		redirectDialer{address: serverAddress},
	)
	result, err := fetcher.Fetch(context.Background(), "http://public.example/java-ai")
	if err != nil {
		t.Fatal(err)
	}
	if result.MediaType != "text/html" || !strings.Contains(string(result.Content), "Java AI") || result.FinalURL != "http://public.example/java-ai" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFetcherRejectsBinaryBodiesSpoofingHTML(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
	}{
		{name: "pdf", content: []byte("%PDF-1.7\n1 0 obj\n")},
		{name: "zip", content: []byte{'P', 'K', 0x03, 0x04, 0x14, 0x00}},
		{name: "binary", content: []byte{0x00, 0x01, 0x02, 0x03}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = response.Write(test.content)
			}))
			defer server.Close()
			fetcher := newTestFetcher(t,
				staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}},
				redirectDialer{address: strings.TrimPrefix(server.URL, "http://")},
			)

			_, err := fetcher.Fetch(context.Background(), "http://public.example/spoofed")
			if fetchErrorCode(err) != "CAPTURE_URL_MEDIA_TYPE_UNSUPPORTED" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestResponseMediaTypeAcceptsHTMLCompatibleBodies(t *testing.T) {
	for _, test := range []struct {
		name    string
		header  string
		content []byte
	}{
		{name: "declared html", header: "text/html; charset=utf-8", content: []byte("<html><body>Java AI</body></html>")},
		{name: "declared xhtml", header: "application/xhtml+xml", content: []byte("<?xml version=\"1.0\"?><html xmlns=\"http://www.w3.org/1999/xhtml\"></html>")},
		{name: "html without header", content: []byte("<!doctype html><html><body>Java AI</body></html>")},
	} {
		t.Run(test.name, func(t *testing.T) {
			mediaType, err := responseMediaType(test.header, test.content)
			if err != nil {
				t.Fatal(err)
			}
			if mediaType != "text/html" {
				t.Fatalf("media type = %q", mediaType)
			}
		})
	}
}

func TestFetcherRejectsRedirectToPrivateAddressAndOversizedBody(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "http://127.0.0.1/private", http.StatusFound)
	}))
	defer redirect.Close()
	redirectAddress := strings.TrimPrefix(redirect.URL, "http://")
	fetcher := newTestFetcher(t,
		staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}},
		redirectDialer{address: redirectAddress},
	)
	if _, err := fetcher.Fetch(context.Background(), "http://public.example/redirect"); fetchErrorCode(err) != "CAPTURE_URL_PRIVATE_ADDRESS_BLOCKED" {
		t.Fatalf("redirect error = %#v", err)
	}

	large := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte("<html>" + strings.Repeat("x", 128) + "</html>"))
	}))
	defer large.Close()
	largeAddress := strings.TrimPrefix(large.URL, "http://")
	limited, err := New(Options{
		MaxBytes: 32, MaxRedirects: 2, Timeout: 2 * time.Second,
		Resolver: staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}},
		Dialer:   redirectDialer{address: largeAddress},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Fetch(context.Background(), "http://public.example/large"); fetchErrorCode(err) != "CAPTURE_URL_RESPONSE_TOO_LARGE" {
		t.Fatalf("large response error = %#v", err)
	}
}

func TestFetcherClassifiesTransientHTTPStatusAsRetryable(t *testing.T) {
	for _, test := range []struct {
		status    int
		retryable bool
	}{
		{status: http.StatusNotFound, retryable: false},
		{status: http.StatusTooManyRequests, retryable: true},
		{status: http.StatusServiceUnavailable, retryable: true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(test.status)
		}))
		serverAddress := strings.TrimPrefix(server.URL, "http://")
		fetcher := newTestFetcher(t,
			staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}},
			redirectDialer{address: serverAddress},
		)
		_, err := fetcher.Fetch(context.Background(), "http://public.example/status")
		server.Close()
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != "CAPTURE_URL_HTTP_STATUS" || classified.Retryable != test.retryable {
			t.Fatalf("status %d error = %#v", test.status, err)
		}
	}
}

type staticResolver struct {
	addresses []net.IPAddr
	err       error
}

func (resolver staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return resolver.addresses, resolver.err
}

type redirectDialer struct{ address string }

func (dialer redirectDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, dialer.address)
}

type rejectingDialer struct{}

func (rejectingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("dial should not be called")
}

func newTestFetcher(t *testing.T, resolver Resolver, dialer Dialer) *Fetcher {
	t.Helper()
	fetcher, err := New(Options{MaxBytes: 1024, MaxRedirects: 2, Timeout: 2 * time.Second, Resolver: resolver, Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	return fetcher
}

func fetchErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
