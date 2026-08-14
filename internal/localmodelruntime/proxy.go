package localmodelruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

const (
	defaultMaxRequestBytes = int64(16 << 20)
	defaultProxyTimeout    = 5 * time.Minute
	maxProxyErrorBytes     = int64(8 << 10)
)

// ProxyOptions configures the bounded inference proxy.
type ProxyOptions struct {
	MaxRequestBytes int64
	RequestTimeout  time.Duration
	Transport       http.RoundTripper
}

// NewHandler constructs the manager health and inference HTTP surface.
func NewHandler(manager *Manager, options ProxyOptions) (http.Handler, error) {
	if manager == nil {
		return nil, errors.New("local model runtime manager is required")
	}
	if options.MaxRequestBytes == 0 {
		options.MaxRequestBytes = defaultMaxRequestBytes
	}
	if options.MaxRequestBytes <= 0 || options.MaxRequestBytes > defaultMaxRequestBytes {
		return nil, errors.New("local model runtime proxy request limit is invalid")
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = defaultProxyTimeout
	}
	if options.RequestTimeout <= 0 || options.RequestTimeout > defaultProxyTimeout {
		return nil, errors.New("local model runtime proxy timeout is invalid")
	}

	target, err := url.Parse(childOllamaOrigin)
	if err != nil {
		return nil, fmt.Errorf("parse local model runtime child origin: %w", err)
	}
	transport := options.Transport
	if transport == nil {
		transport = &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true}
	}

	handler := &proxyHandler{manager: manager, maxRequestBytes: options.MaxRequestBytes, requestTimeout: options.RequestTimeout}
	handler.proxy = &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			proxyRequest.SetURL(target)
			proxyRequest.Out.Host = target.Host
		},
		Transport:     availabilityTransport{manager: manager, next: transport},
		FlushInterval: -1,
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, _ error) {
			writeUnavailable(writer)
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Server")
			response.Header.Del("Set-Cookie")
			boundProxyErrorResponse(response)
			return nil
		},
	}
	return handler, nil
}

func boundProxyErrorResponse(response *http.Response) {
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return
	}
	_, _ = io.ReadAll(io.LimitReader(response.Body, maxProxyErrorBytes+1))
	_ = response.Body.Close()
	// Provider response bodies are not part of the supervisor's stable error
	// contract. Preserve only the bounded status class and a fixed code.
	response.StatusCode = http.StatusBadGateway
	response.Status = fmt.Sprintf("%d %s", http.StatusBadGateway, http.StatusText(http.StatusBadGateway))
	body := []byte("{\"code\":\"LOCAL_MODEL_RUNTIME_RESPONSE_INVALID\"}\n")
	response.Header.Set("Content-Type", "application/json")
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	response.Header.Del("Content-Encoding")
	response.TransferEncoding = nil
}

type proxyHandler struct {
	manager         *Manager
	proxy           *httputil.ReverseProxy
	maxRequestBytes int64
	requestTimeout  time.Duration
}

func (handler *proxyHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/healthz" {
		handler.serveHealth(writer, request)
		return
	}
	if !isAllowedInferencePath(request.URL.Path) || request.URL.RawPath != "" || request.URL.RawQuery != "" {
		writeJSONError(writer, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSONError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.requestTimeout)
	defer cancel()
	inferenceContext, generation, releaseInference, err := handler.manager.acquireInference(ctx)
	if err != nil {
		writeUnavailable(writer)
		return
	}
	defer releaseInference()
	if !isJSONContentType(request.Header.Get("Content-Type")) {
		writeJSONError(writer, http.StatusUnsupportedMediaType, "CONTENT_TYPE_UNSUPPORTED")
		return
	}
	body, err := readBoundedRequest(inferenceContext, request, handler.maxRequestBytes)
	if err != nil {
		switch {
		case errors.Is(err, errRequestTooLarge):
			writeJSONError(writer, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE")
		case errors.Is(err, ErrChildUnavailable):
			writeUnavailable(writer)
		case errors.Is(err, context.DeadlineExceeded):
			writeJSONError(writer, http.StatusGatewayTimeout, "REQUEST_TIMEOUT")
		case errors.Is(err, context.Canceled):
			writeJSONError(writer, http.StatusRequestTimeout, "REQUEST_CANCELLED")
		default:
			writeJSONError(writer, http.StatusBadRequest, "REQUEST_BODY_INVALID")
		}
		return
	}
	proxied := request.Clone(withInferenceGeneration(inferenceContext, generation))
	proxied.Body = io.NopCloser(bytes.NewReader(body))
	proxied.ContentLength = int64(len(body))
	proxied.GetBody = nil
	proxied.Header = make(http.Header)
	proxied.Header.Set("Content-Type", "application/json")
	proxied.Header.Set("Accept", "application/json")
	proxied.Trailer = nil
	proxied.TransferEncoding = nil
	handler.proxy.ServeHTTP(writer, proxied)
}

func (handler *proxyHandler) serveHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSONError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if request.URL.RawQuery != "" || request.ContentLength != 0 || (request.Body != nil && request.Body != http.NoBody) {
		writeJSONError(writer, http.StatusBadRequest, "HEALTH_REQUEST_INVALID")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

type availabilityTransport struct {
	manager *Manager
	next    http.RoundTripper
}

func (transport availabilityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	generation, ok := inferenceGenerationFromContext(request.Context())
	if !ok || !transport.manager.generationAvailable(generation) {
		return nil, ErrChildUnavailable
	}
	return transport.next.RoundTrip(request)
}

var errRequestTooLarge = errors.New("local model runtime inference body is too large")

func readBoundedRequest(ctx context.Context, request *http.Request, limit int64) ([]byte, error) {
	defer request.Body.Close()
	stopClose := context.AfterFunc(ctx, func() {
		_ = request.Body.Close()
	})
	defer stopClose()
	if request.ContentLength > limit {
		return nil, errRequestTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: request.Body}, limit+1))
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errRequestTooLarge
	}
	if len(body) == 0 {
		return nil, errors.New("local model runtime inference body is empty")
	}
	return body, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-reader.ctx.Done():
		return 0, context.Cause(reader.ctx)
	default:
		return reader.reader.Read(buffer)
	}
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "application/json")
}

func isAllowedInferencePath(path string) bool {
	switch path {
	case "/api/embed", "/v1/chat/completions":
		return true
	default:
		return false
	}
}

type inferenceGenerationKey struct{}

func withInferenceGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, inferenceGenerationKey{}, generation)
}

func inferenceGenerationFromContext(ctx context.Context) (uint64, bool) {
	generation, ok := ctx.Value(inferenceGenerationKey{}).(uint64)
	return generation, ok && generation > 0
}

func writeUnavailable(writer http.ResponseWriter) {
	writer.Header().Set("Retry-After", "1")
	writeJSONError(writer, http.StatusServiceUnavailable, "LOCAL_MODEL_RUNTIME_UNAVAILABLE")
}

func writeJSONError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintf(writer, "{\"code\":%q}\n", code)
}
