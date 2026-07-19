// Package webfetch implements the SSRF-safe FetchWebPage network boundary.
package webfetch

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeConfigInvalid 表示 Web Fetch Adapter 配置不满足安全边界。
	ErrorCodeConfigInvalid = "WEB_FETCH_CONFIG_INVALID"
	// ErrorCodeRequestInvalid 表示 URL 形态或协议不合法。
	ErrorCodeRequestInvalid = "WEB_FETCH_REQUEST_INVALID"
	// ErrorCodeAddressDenied 表示目标解析到了禁止访问的地址。
	ErrorCodeAddressDenied = "WEB_FETCH_ADDRESS_DENIED"
	// ErrorCodeResolveFailed 表示目标域名无法安全解析。
	ErrorCodeResolveFailed = "WEB_FETCH_RESOLVE_FAILED"
	// ErrorCodeConnectFailed 表示已固定地址的连接失败。
	ErrorCodeConnectFailed = "WEB_FETCH_CONNECT_FAILED"
	// ErrorCodeTimeout 表示请求超过调用方或工具的最早截止时间。
	ErrorCodeTimeout = "WEB_FETCH_TIMEOUT"
	// ErrorCodeCancelled 表示调用方取消了请求。
	ErrorCodeCancelled = "WEB_FETCH_CANCELLED"
	// ErrorCodeRedirectDenied 表示重定向缺失、非法、循环或超过上限。
	ErrorCodeRedirectDenied = "WEB_FETCH_REDIRECT_DENIED"
	// ErrorCodeResponseStatus 表示远端返回了不接受的状态码。
	ErrorCodeResponseStatus = "WEB_FETCH_RESPONSE_STATUS"
	// ErrorCodeContentTypeDenied 表示响应内容类型不在白名单内。
	ErrorCodeContentTypeDenied = "WEB_FETCH_CONTENT_TYPE_DENIED"
	// ErrorCodeContentEncodingDenied 表示响应使用了不受控的内容编码。
	ErrorCodeContentEncodingDenied = "WEB_FETCH_CONTENT_ENCODING_DENIED"
	// ErrorCodeBodyTooLarge 表示解压后的响应正文超过上限。
	ErrorCodeBodyTooLarge = "WEB_FETCH_BODY_TOO_LARGE"
	// ErrorCodeBodyInvalid 表示响应正文无法安全解码为受控 UTF-8 文本。
	ErrorCodeBodyInvalid = "WEB_FETCH_BODY_INVALID"
	// ErrorCodeOutputTooLarge 表示清理后的文本超过输出上限。
	ErrorCodeOutputTooLarge = "WEB_FETCH_OUTPUT_TOO_LARGE"
)

const (
	maxConfigurationURLBytes    = 64 * 1024
	maxConfigurationHeaderBytes = 2 * 1024 * 1024
	maxConfigurationBodyBytes   = 32 * 1024 * 1024
	maxConfigurationRedirects   = 20
	maxConfigurationResolvedIPs = 64
)

var (
	errInvalidConfiguration  = errors.New("web fetch configuration is invalid")
	errInvalidRequest        = errors.New("web fetch request is invalid")
	errAddressDenied         = errors.New("web fetch address is denied")
	errResolveFailed         = errors.New("web fetch resolution failed")
	errConnectFailed         = errors.New("web fetch connection failed")
	errRedirectDenied        = errors.New("web fetch redirect is denied")
	errResponseStatus        = errors.New("web fetch response status is denied")
	errContentTypeDenied     = errors.New("web fetch content type is denied")
	errContentEncodingDenied = errors.New("web fetch content encoding is denied")
	errBodyTooLarge          = errors.New("web fetch body is too large")
	errBodyInvalid           = errors.New("web fetch body is invalid")
	errOutputTooLarge        = errors.New("web fetch output is too large")
)

// Resolver 为单个重定向跳解析全部 A 与 AAAA 地址。
type Resolver interface {
	// LookupNetIP 返回指定主机在当前跳的完整地址快照。
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer 只连接已经校验通过的数字地址。
type Dialer interface {
	// DialContext 在调用方上下文内连接指定数字地址。
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Config 定义 Web Fetch 的全部资源与传输上限。
type Config struct {
	// Timeout 限制整个调用的最长时间。
	Timeout time.Duration
	// ResponseHeaderTimeout 限制单跳等待响应头的时间。
	ResponseHeaderTimeout time.Duration
	// TLSHandshakeTimeout 限制单跳 TLS 握手时间。
	TLSHandshakeTimeout time.Duration
	// MaxRedirects 限制手动重定向次数。
	MaxRedirects int
	// MaxURLBytes 限制原始 URL 与 Location 的字节数。
	MaxURLBytes int
	// MaxResponseHeaderBytes 限制单跳响应头字节数。
	MaxResponseHeaderBytes int64
	// MaxBodyBytes 限制解压后响应正文字节数。
	MaxBodyBytes int64
	// MaxTextBytes 限制清理后 UTF-8 文本字节数。
	MaxTextBytes int
	// MaxResolvedIPs 限制单跳 DNS 地址数量。
	MaxResolvedIPs int
	// AllowedContentTypes 是受控 MIME 白名单。
	AllowedContentTypes []string
	// RootCAs 提供 HTTPS 服务端证书信任根。
	RootCAs *x509.CertPool
}

// Request 是 FetchWebPage 的强类型输入。
type Request struct {
	// URL 是待抓取的绝对 HTTP(S) 地址。
	URL string `json:"url"`
}

// String 返回不含 URL 的请求摘要。
func (request Request) String() string { return "web fetch request(redacted)" }

// GoString 防止诊断格式展开含敏感查询参数的 URL。
func (request Request) GoString() string { return request.String() }

// Result 是有界且明确不受信任的 FetchWebPage 输出。
type Result struct {
	// FinalURL 是移除查询参数和片段后的最终地址。
	FinalURL string `json:"final_url"`
	// FetchedAt 是完成抓取的 UTC 时间。
	FetchedAt time.Time `json:"fetched_at"`
	// ContentType 是规范化后的受控 MIME 类型。
	ContentType string `json:"content_type"`
	// ByteCount 是解压后原始正文的字节数。
	ByteCount int64 `json:"byte_count"`
	// ContentHash 是解压后原始正文的 SHA-256。
	ContentHash string `json:"content_hash"`
	// Text 是清理并限制后的 UTF-8 纯文本。
	Text string `json:"text"`
	// UntrustedData 恒为 true，禁止调用方将正文提升为策略输入。
	UntrustedData bool `json:"untrusted_data"`
}

// String 返回不含最终 URL 和正文的结果摘要。
func (result Result) String() string {
	return fmt.Sprintf("web fetch result(content_type=%s,byte_count=%d,untrusted=%t)", result.ContentType, result.ByteCount, result.UntrustedData)
}

// GoString 防止诊断格式展开不受信任的正文。
func (result Result) GoString() string { return result.String() }

// Fetcher 执行一次不含隐藏重试的有界公网抓取。
type Fetcher interface {
	// Fetch 对每个 DNS 与重定向跳重新授权后抓取页面。
	Fetch(ctx context.Context, request Request) (Result, error)
}

type fetcher struct {
	config       Config
	resolver     Resolver
	dialer       Dialer
	clock        foundation.Clock
	contentTypes map[string]struct{}
}

// New 使用显式配置与传输依赖构造 Fetcher。
func New(config Config, resolver Resolver, dialer Dialer, clock foundation.Clock) (Fetcher, error) {
	contentTypes, err := validateConfig(config, resolver, dialer, clock)
	if err != nil {
		return nil, err
	}
	config.AllowedContentTypes = append([]string(nil), config.AllowedContentTypes...)
	if config.RootCAs != nil {
		config.RootCAs = config.RootCAs.Clone()
	}
	return &fetcher{config: config, resolver: resolver, dialer: dialer, clock: clock, contentTypes: contentTypes}, nil
}

// Fetch 在校验每个 DNS 与重定向跳后抓取一个 HTTP(S) 页面。
func (fetcher *fetcher) Fetch(ctx context.Context, request Request) (Result, error) {
	if fetcher == nil {
		return Result{}, fetchError(foundation.ErrorDependencyUnavailable, ErrorCodeConfigInvalid, false, errInvalidConfiguration)
	}
	if ctx == nil {
		return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeRequestInvalid, false, errInvalidRequest)
	}
	requestContext, cancel := context.WithTimeout(ctx, fetcher.config.Timeout)
	defer cancel()

	currentURL, err := parseTargetURL(request.URL, fetcher.config.MaxURLBytes)
	if err != nil {
		return Result{}, err
	}
	visited := map[string]struct{}{currentURL.String(): {}}

	for redirectCount := 0; ; redirectCount++ {
		addresses, err := fetcher.resolveHop(requestContext, currentURL)
		if err != nil {
			return Result{}, err
		}
		response, transport, err := fetcher.doHop(requestContext, currentURL, addresses)
		if err != nil {
			return Result{}, err
		}
		transport.CloseIdleConnections()

		if isRedirectStatus(response.StatusCode) {
			_ = response.Body.Close()
			if redirectCount >= fetcher.config.MaxRedirects {
				return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeRedirectDenied, false, errRedirectDenied)
			}
			nextURL, err := redirectTarget(response, currentURL, fetcher.config.MaxURLBytes)
			if err != nil {
				return Result{}, err
			}
			key := nextURL.String()
			if _, duplicate := visited[key]; duplicate {
				return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeRedirectDenied, false, errRedirectDenied)
			}
			visited[key] = struct{}{}
			currentURL = nextURL
			continue
		}

		result, consumeErr := fetcher.consumeResponse(requestContext, currentURL, response)
		_ = response.Body.Close()
		if consumeErr != nil {
			return Result{}, consumeErr
		}
		return result, nil
	}
}

func validateConfig(config Config, resolver Resolver, dialer Dialer, clock foundation.Clock) (map[string]struct{}, error) {
	if resolver == nil || dialer == nil || clock == nil || config.Timeout <= 0 || config.ResponseHeaderTimeout <= 0 ||
		config.TLSHandshakeTimeout <= 0 || config.MaxRedirects < 0 || config.MaxRedirects > maxConfigurationRedirects ||
		config.MaxURLBytes < 1 || config.MaxURLBytes > maxConfigurationURLBytes || config.MaxResponseHeaderBytes < 1 ||
		config.MaxResponseHeaderBytes > maxConfigurationHeaderBytes || config.MaxBodyBytes < 1 || config.MaxBodyBytes > maxConfigurationBodyBytes ||
		config.MaxTextBytes < 1 || int64(config.MaxTextBytes) > config.MaxBodyBytes || config.MaxResolvedIPs < 1 ||
		config.MaxResolvedIPs > maxConfigurationResolvedIPs || len(config.AllowedContentTypes) == 0 {
		return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeConfigInvalid, false, errInvalidConfiguration)
	}
	contentTypes := make(map[string]struct{}, len(config.AllowedContentTypes))
	for _, value := range config.AllowedContentTypes {
		if value != strings.TrimSpace(value) || (value != "text/plain" && value != "text/html") {
			return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeConfigInvalid, false, errInvalidConfiguration)
		}
		if _, duplicate := contentTypes[value]; duplicate {
			return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeConfigInvalid, false, errInvalidConfiguration)
		}
		contentTypes[value] = struct{}{}
	}
	return contentTypes, nil
}

func (fetcher *fetcher) resolveHop(ctx context.Context, target *url.URL) ([]netip.Addr, error) {
	host := target.Hostname()
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if literal.Zone() != "" || !allowedPublicAddress(literal) {
			return nil, fetchError(foundation.ErrorPermissionDenied, ErrorCodeAddressDenied, false, errAddressDenied)
		}
		return []netip.Addr{literal}, nil
	}

	addresses, err := fetcher.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		if contextErr := classifyContextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		retryable := true
		var dnsError *net.DNSError
		if errors.As(err, &dnsError) && dnsError.IsNotFound {
			retryable = false
		}
		kind := foundation.ErrorRetryableFailure
		if !retryable {
			kind = foundation.ErrorNonRetryableFailure
		}
		return nil, fetchError(kind, ErrorCodeResolveFailed, retryable, errResolveFailed)
	}
	if len(addresses) == 0 || len(addresses) > fetcher.config.MaxResolvedIPs {
		return nil, fetchError(foundation.ErrorNonRetryableFailure, ErrorCodeResolveFailed, false, errResolveFailed)
	}
	validated := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || address.Zone() != "" || !allowedPublicAddress(address) {
			return nil, fetchError(foundation.ErrorPermissionDenied, ErrorCodeAddressDenied, false, errAddressDenied)
		}
		if _, duplicate := seen[address]; duplicate {
			continue
		}
		seen[address] = struct{}{}
		validated = append(validated, address)
	}
	if len(validated) == 0 {
		return nil, fetchError(foundation.ErrorNonRetryableFailure, ErrorCodeResolveFailed, false, errResolveFailed)
	}
	return validated, nil
}

func (fetcher *fetcher) doHop(ctx context.Context, target *url.URL, addresses []netip.Addr) (*http.Response, *http.Transport, error) {
	hostname := target.Hostname()
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname}
	if fetcher.config.RootCAs != nil {
		tlsConfig.RootCAs = fetcher.config.RootCAs.Clone()
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            fetcher.pinnedDialContext(ctx, hostname, port, addresses),
		ForceAttemptHTTP2:      false,
		DisableKeepAlives:      true,
		MaxConnsPerHost:        1,
		ResponseHeaderTimeout:  fetcher.config.ResponseHeaderTimeout,
		TLSHandshakeTimeout:    fetcher.config.TLSHandshakeTimeout,
		MaxResponseHeaderBytes: fetcher.config.MaxResponseHeaderBytes,
		TLSClientConfig:        tlsConfig,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, transport, fetchError(foundation.ErrorInvalidInput, ErrorCodeRequestInvalid, false, errInvalidRequest)
	}
	request.Header.Set("Accept", "text/plain, text/html")
	response, err := client.Do(request)
	if err != nil {
		transport.CloseIdleConnections()
		if contextErr := classifyContextError(ctx); contextErr != nil {
			return nil, transport, contextErr
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return nil, transport, fetchError(foundation.ErrorRetryableFailure, ErrorCodeTimeout, true, context.DeadlineExceeded)
		}
		return nil, transport, fetchError(foundation.ErrorRetryableFailure, ErrorCodeConnectFailed, true, errConnectFailed)
	}
	return response, transport, nil
}

func (fetcher *fetcher) pinnedDialContext(hopContext context.Context, expectedHost, expectedPort string, addresses []netip.Addr) func(context.Context, string, string) (net.Conn, error) {
	return func(transportContext context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(expectedHost, ".")) || port != expectedPort ||
			(network != "tcp" && network != "tcp4" && network != "tcp6") {
			return nil, errConnectFailed
		}
		dialContext, cancelDial := context.WithCancel(hopContext)
		stopTransportCancel := context.AfterFunc(transportContext, cancelDial)
		defer func() {
			stopTransportCancel()
			cancelDial()
		}()
		for index, target := range addresses {
			attemptContext, cancel := boundedDialAttempt(dialContext, len(addresses)-index)
			connection, dialErr := fetcher.dialer.DialContext(attemptContext, networkForAddress(target), net.JoinHostPort(target.String(), expectedPort))
			attemptTimedOut := errors.Is(attemptContext.Err(), context.DeadlineExceeded)
			var networkError net.Error
			if errors.As(dialErr, &networkError) && networkError.Timeout() {
				attemptTimedOut = true
			}
			cancel()
			if dialErr == nil {
				return connection, nil
			}
			if dialContext.Err() != nil {
				break
			}
			if attemptTimedOut && index == len(addresses)-1 {
				return nil, context.DeadlineExceeded
			}
		}
		if contextErr := classifyContextError(dialContext); contextErr != nil {
			return nil, contextErr
		}
		return nil, errConnectFailed
	}
}

func (fetcher *fetcher) consumeResponse(ctx context.Context, target *url.URL, response *http.Response) (Result, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		kind := foundation.ErrorNonRetryableFailure
		if retryable {
			kind = foundation.ErrorRetryableFailure
		}
		return Result{}, fetchError(kind, ErrorCodeResponseStatus, retryable, errResponseStatus)
	}

	rawContentType, ok := singleHeaderValue(response.Header, "Content-Type")
	if !ok {
		return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeContentTypeDenied, false, errContentTypeDenied)
	}
	contentType, err := fetcher.validateContentType(rawContentType)
	if err != nil {
		return Result{}, err
	}
	contentEncoding, ok := optionalSingleHeaderValue(response.Header, "Content-Encoding")
	if !ok {
		return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeContentEncodingDenied, false, errContentEncodingDenied)
	}
	contentEncoding = strings.TrimSpace(contentEncoding)
	if contentEncoding != "" && !strings.EqualFold(contentEncoding, "identity") {
		return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeContentEncodingDenied, false, errContentEncodingDenied)
	}
	if response.ContentLength > fetcher.config.MaxBodyBytes {
		return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeBodyTooLarge, false, errBodyTooLarge)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, fetcher.config.MaxBodyBytes+1))
	if err != nil {
		if contextErr := classifyContextError(ctx); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, fetchError(foundation.ErrorRetryableFailure, ErrorCodeBodyInvalid, true, errBodyInvalid)
	}
	if contextErr := classifyContextError(ctx); contextErr != nil {
		return Result{}, contextErr
	}
	if int64(len(body)) > fetcher.config.MaxBodyBytes {
		return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeBodyTooLarge, false, errBodyTooLarge)
	}
	if !utf8.Valid(body) {
		return Result{}, fetchError(foundation.ErrorInvalidInput, ErrorCodeBodyInvalid, false, errBodyInvalid)
	}

	text, err := extractText(ctx, contentType, body, fetcher.config.MaxTextBytes)
	if err != nil {
		return Result{}, err
	}
	if contextErr := classifyContextError(ctx); contextErr != nil {
		return Result{}, contextErr
	}
	fetchedAt := fetcher.clock.Now().UTC()
	if fetchedAt.IsZero() {
		return Result{}, fetchError(foundation.ErrorDependencyUnavailable, ErrorCodeConfigInvalid, false, errInvalidConfiguration)
	}
	if contextErr := classifyContextError(ctx); contextErr != nil {
		return Result{}, contextErr
	}
	digest := sha256.Sum256(body)
	result := Result{
		FinalURL:      redactedURL(target),
		FetchedAt:     fetchedAt,
		ContentType:   contentType,
		ByteCount:     int64(len(body)),
		ContentHash:   hex.EncodeToString(digest[:]),
		Text:          text,
		UntrustedData: true,
	}
	if contextErr := classifyContextError(ctx); contextErr != nil {
		return Result{}, contextErr
	}
	return result, nil
}

func (fetcher *fetcher) validateContentType(raw string) (string, error) {
	mediaType, parameters, err := mime.ParseMediaType(raw)
	mediaType = strings.ToLower(mediaType)
	if err != nil {
		return "", fetchError(foundation.ErrorInvalidInput, ErrorCodeContentTypeDenied, false, errContentTypeDenied)
	}
	if _, allowed := fetcher.contentTypes[mediaType]; !allowed {
		return "", fetchError(foundation.ErrorPermissionDenied, ErrorCodeContentTypeDenied, false, errContentTypeDenied)
	}
	charset := strings.ToLower(strings.TrimSpace(parameters["charset"]))
	if charset != "" && charset != "utf-8" && charset != "us-ascii" {
		return "", fetchError(foundation.ErrorInvalidInput, ErrorCodeContentTypeDenied, false, errContentTypeDenied)
	}
	return mediaType, nil
}

func redirectTarget(response *http.Response, current *url.URL, maxURLBytes int) (*url.URL, error) {
	location, ok := singleHeaderValue(response.Header, "Location")
	if !ok || location == "" || location != strings.TrimSpace(location) || len(location) > maxURLBytes || strings.ContainsAny(location, "\x00\r\n\t") {
		return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeRedirectDenied, false, errRedirectDenied)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeRedirectDenied, false, errRedirectDenied)
	}
	next, err := parseTargetURL(current.ResolveReference(parsed).String(), maxURLBytes)
	if err != nil {
		return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeRedirectDenied, false, errRedirectDenied)
	}
	return next, nil
}

func singleHeaderValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	returnValue := ""
	if len(values) == 1 {
		returnValue = values[0]
	}
	return returnValue, len(values) == 1
}

func optionalSingleHeaderValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func classifyContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	contextCause := ctx.Err()
	switch {
	case errors.Is(contextCause, context.Canceled):
		return fetchError(foundation.ErrorNonRetryableFailure, ErrorCodeCancelled, false, context.Canceled)
	case errors.Is(contextCause, context.DeadlineExceeded):
		return fetchError(foundation.ErrorRetryableFailure, ErrorCodeTimeout, true, context.DeadlineExceeded)
	default:
		return nil
	}
}

func fetchError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}

func networkForAddress(address netip.Addr) string {
	if address.Is4() {
		return "tcp4"
	}
	return "tcp6"
}

func boundedDialAttempt(ctx context.Context, addressCount int) (context.Context, context.CancelFunc) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || addressCount <= 1 {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, remaining/time.Duration(addressCount))
}

func redactedURL(value *url.URL) string {
	redacted := *value
	redacted.RawQuery = ""
	redacted.ForceQuery = false
	redacted.Fragment = ""
	return redacted.String()
}

// String 返回不含配置细节的 Adapter 摘要。
func (fetcher *fetcher) String() string {
	if fetcher == nil {
		return "web fetch adapter(unavailable)"
	}
	return "web fetch adapter(SSRF-safe)"
}

// GoString 防止诊断格式展开传输配置。
func (fetcher *fetcher) GoString() string { return fetcher.String() }

var _ Fetcher = (*fetcher)(nil)

// String 返回不含信任根和目标信息的配置摘要。
func (config Config) String() string {
	return fmt.Sprintf("web fetch config(timeout=%s,max_redirects=%d,max_body_bytes=%d)", config.Timeout, config.MaxRedirects, config.MaxBodyBytes)
}

// GoString 防止诊断格式展开 TLS 信任配置。
func (config Config) GoString() string { return config.String() }

func validPort(port string) bool {
	if port == "" {
		return true
	}
	parsed, err := strconv.Atoi(port)
	return err == nil && parsed >= 1 && parsed <= 65535
}
