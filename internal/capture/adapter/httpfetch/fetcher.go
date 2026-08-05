// Package httpfetch implements a bounded SSRF-resistant public HTML fetcher.
package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	defaultMaxBytes     int64 = 10 * 1024 * 1024
	defaultMaxRedirects       = 5
	responseSniffBytes        = 512
)

// Resolver is the DNS seam used to reject non-public answers before dialing.
type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// Dialer opens an already-resolved public endpoint.
type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Options bound URL fetch network and response resources.
type Options struct {
	MaxBytes     int64
	MaxRedirects int
	Timeout      time.Duration
	Resolver     Resolver
	Dialer       Dialer
}

// Fetcher retrieves public HTML without environment proxies, credentials, or private network access.
type Fetcher struct {
	client   *http.Client
	maxBytes int64
}

// New constructs a hardened URL fetcher.
func New(options Options) (*Fetcher, error) {
	if options.MaxBytes == 0 {
		options.MaxBytes = defaultMaxBytes
	}
	if options.MaxRedirects == 0 {
		options.MaxRedirects = defaultMaxRedirects
	}
	if options.Timeout == 0 {
		options.Timeout = 15 * time.Second
	}
	if options.Resolver == nil {
		options.Resolver = net.DefaultResolver
	}
	if options.Dialer == nil {
		options.Dialer = &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	}
	if options.MaxBytes < 1 || options.MaxBytes > defaultMaxBytes || options.MaxRedirects < 1 || options.MaxRedirects > 10 ||
		options.Timeout < time.Second || options.Timeout > 60*time.Second {
		return nil, fetchError(foundation.ErrorInvalidInput, "CAPTURE_URL_FETCH_POLICY_INVALID", false, errors.New("URL fetch policy is invalid"))
	}
	secureDial := secureDialer{resolver: options.Resolver, dialer: options.Dialer}
	transport := &http.Transport{
		Proxy: nil, DialContext: secureDial.DialContext, ForceAttemptHTTP2: true,
		DisableKeepAlives: true, MaxIdleConns: 0, MaxConnsPerHost: 2,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second,
		ExpectContinueTimeout: time.Second, MaxResponseHeaderBytes: 64 * 1024,
	}
	client := &http.Client{Transport: transport, Timeout: options.Timeout}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= options.MaxRedirects {
			return fetchError(foundation.ErrorNonRetryableFailure, "CAPTURE_URL_REDIRECT_LIMIT", false, errors.New("URL redirect limit exceeded"))
		}
		if err := validateURL(request.URL); err != nil {
			return err
		}
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		return nil
	}
	return &Fetcher{client: client, maxBytes: options.MaxBytes}, nil
}

// Fetch retrieves one HTML response and rejects oversized or unsupported content.
func (fetcher *Fetcher) Fetch(ctx context.Context, rawURL string) (captureapp.URLFetchResult, error) {
	if fetcher == nil || fetcher.client == nil || fetcher.maxBytes < 1 {
		return captureapp.URLFetchResult{}, fetchError(foundation.ErrorDependencyUnavailable, "CAPTURE_URL_FETCH_UNAVAILABLE", true, errors.New("URL fetcher is unavailable"))
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return captureapp.URLFetchResult{}, fetchError(foundation.ErrorInvalidInput, "CAPTURE_URL_INVALID", false, err)
	}
	if err := validateURL(parsed); err != nil {
		return captureapp.URLFetchResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return captureapp.URLFetchResult{}, fetchError(foundation.ErrorInvalidInput, "CAPTURE_URL_INVALID", false, err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	request.Header.Set("User-Agent", "zhixu-capture/1")
	response, err := fetcher.client.Do(request)
	if err != nil {
		return captureapp.URLFetchResult{}, classifyFetchError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		retryable := retryableHTTPStatus(response.StatusCode)
		kind := foundation.ErrorNonRetryableFailure
		if retryable {
			kind = foundation.ErrorRetryableFailure
		}
		return captureapp.URLFetchResult{}, fetchError(kind, "CAPTURE_URL_HTTP_STATUS", retryable, fmt.Errorf("unexpected HTTP status %d", response.StatusCode))
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, fetcher.maxBytes+1))
	if err != nil {
		return captureapp.URLFetchResult{}, classifyFetchError(err)
	}
	if int64(len(content)) > fetcher.maxBytes {
		return captureapp.URLFetchResult{}, fetchError(foundation.ErrorNonRetryableFailure, "CAPTURE_URL_RESPONSE_TOO_LARGE", false, errors.New("URL response exceeds the configured limit"))
	}
	if len(content) == 0 {
		return captureapp.URLFetchResult{}, fetchError(foundation.ErrorNonRetryableFailure, "CAPTURE_URL_RESPONSE_EMPTY", false, errors.New("URL response is empty"))
	}
	mediaType, err := responseMediaType(response.Header.Get("Content-Type"), content)
	if err != nil {
		return captureapp.URLFetchResult{}, err
	}
	return captureapp.URLFetchResult{
		Content: content, MediaType: mediaType, FinalURL: response.Request.URL.String(),
	}, nil
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

type secureDialer struct {
	resolver Resolver
	dialer   Dialer
}

func (dialer secureDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fetchError(foundation.ErrorInvalidInput, "CAPTURE_URL_HOST_INVALID", false, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil, blockedAddressError()
	}
	if literal := net.ParseIP(host); literal != nil {
		if !publicIP(literal) {
			return nil, blockedAddressError()
		}
		return dialer.dialer.DialContext(ctx, network, net.JoinHostPort(literal.String(), port))
	}
	addresses, err := dialer.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fetchError(foundation.ErrorRetryableFailure, "CAPTURE_URL_DNS_FAILED", true, err)
	}
	if len(addresses) == 0 {
		return nil, fetchError(foundation.ErrorRetryableFailure, "CAPTURE_URL_DNS_EMPTY", true, errors.New("URL host has no DNS address"))
	}
	for _, address := range addresses {
		if !publicIP(address.IP) {
			return nil, blockedAddressError()
		}
	}
	selected := addresses[0].IP.String()
	return dialer.dialer.DialContext(ctx, network, net.JoinHostPort(selected, port))
}

func validateURL(value *url.URL) error {
	if value == nil || (value.Scheme != "http" && value.Scheme != "https") || value.Host == "" ||
		value.User != nil || value.Opaque != "" || len(value.String()) > 8192 || strings.ContainsAny(value.String(), "\r\n\x00") {
		return fetchError(foundation.ErrorInvalidInput, "CAPTURE_URL_INVALID", false, errors.New("URL must be a bounded HTTP(S) URL without credentials"))
	}
	if strings.EqualFold(value.Hostname(), "localhost") {
		return blockedAddressError()
	}
	if literal := net.ParseIP(value.Hostname()); literal != nil && !publicIP(literal) {
		return blockedAddressError()
	}
	return nil
}

func publicIP(value net.IP) bool {
	address, ok := netip.AddrFromSlice(value)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var blockedPrefixes = []netip.Prefix{
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
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fec0::/10"),
}

func responseMediaType(header string, content []byte) (string, error) {
	sniffed := content
	if len(sniffed) > responseSniffBytes {
		sniffed = sniffed[:responseSniffBytes]
	}
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(sniffed))
	detected = strings.ToLower(detected)

	if header == "" {
		if detected != "text/html" && detected != "application/xhtml+xml" {
			return "", unsupportedMediaTypeError()
		}
		return "text/html", nil
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return "", fetchError(foundation.ErrorNonRetryableFailure, "CAPTURE_URL_MEDIA_TYPE_INVALID", false, err)
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return "", unsupportedMediaTypeError()
	}
	if detected != "text/html" && detected != "text/plain" && detected != "text/xml" && detected != "application/xhtml+xml" {
		return "", unsupportedMediaTypeError()
	}
	return "text/html", nil
}

func unsupportedMediaTypeError() error {
	return fetchError(foundation.ErrorNonRetryableFailure, "CAPTURE_URL_MEDIA_TYPE_UNSUPPORTED", false, errors.New("URL response is not HTML"))
}

func blockedAddressError() error {
	return fetchError(foundation.ErrorPermissionDenied, "CAPTURE_URL_PRIVATE_ADDRESS_BLOCKED", false, errors.New("URL resolves to a non-public address"))
}

func classifyFetchError(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fetchError(foundation.ErrorDependencyUnavailable, "CAPTURE_URL_FETCH_CANCELLED", false, err)
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return fetchError(foundation.ErrorRetryableFailure, "CAPTURE_URL_NETWORK_FAILED", true, err)
	}
	return fetchError(foundation.ErrorRetryableFailure, "CAPTURE_URL_FETCH_FAILED", true, err)
}

func fetchError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}

var _ captureapp.URLFetcher = (*Fetcher)(nil)
