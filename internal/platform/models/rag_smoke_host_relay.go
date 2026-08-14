package models

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	ragSmokeRelayChatPath        = "/v1/chat/completions"
	ragSmokeRelayHealthPath      = "/healthz"
	ragSmokeRelayMaxRequestBytes = int64(4 << 20)
	ragSmokeRelayRequestTimeout  = 6 * time.Minute
)

// RAGSmokeChatRelay is a loopback test-harness handler for one real Chat provider.
// It is intentionally not a production model Adapter or a generic HTTP proxy.
type RAGSmokeChatRelay struct {
	endpoint       string
	client         *http.Client
	forwardedCount atomic.Uint64
}

// NewRAGSmokeChatRelay constructs the smoke-only handler with the production Chat URL and transport guards.
func NewRAGSmokeChatRelay(chatBaseURL string) (*RAGSmokeChatRelay, error) {
	return newRAGSmokeChatRelay(chatBaseURL, nil)
}

func newRAGSmokeChatRelay(chatBaseURL string, suppliedClient *http.Client) (*RAGSmokeChatRelay, error) {
	baseURL, err := parseChatBaseURL(chatBaseURL)
	if err != nil || baseURL.Scheme != "https" {
		return nil, errChatConfigInvalid
	}
	endpoint, err := url.Parse(appendChatPath(baseURL))
	if err != nil {
		return nil, errChatConfigInvalid
	}
	client, err := newModelHTTPClient(endpoint, suppliedClient)
	if err != nil {
		return nil, errChatConfigInvalid
	}
	return &RAGSmokeChatRelay{endpoint: endpoint.String(), client: client}, nil
}

// ServeHTTP permits exactly the smoke Chat endpoint and a non-sensitive health response.
func (relay *RAGSmokeChatRelay) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if relay == nil || relay.client == nil {
		http.Error(writer, "relay unavailable", http.StatusServiceUnavailable)
		return
	}
	switch request.URL.Path {
	case ragSmokeRelayHealthPath:
		if request.Method != http.MethodGet || request.URL.RawQuery != "" {
			methodOrPathRejected(writer, http.MethodGet)
			return
		}
		relay.health(writer)
	case ragSmokeRelayChatPath:
		if request.Method != http.MethodPost || request.URL.RawQuery != "" {
			methodOrPathRejected(writer, http.MethodPost)
			return
		}
		relay.forwardChat(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (relay *RAGSmokeChatRelay) health(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"forwarded_count":` + strconv.FormatUint(relay.forwardedCount.Load(), 10) + `}`))
}

func (relay *RAGSmokeChatRelay) forwardChat(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") == "" {
		http.Error(writer, "authorization required", http.StatusUnauthorized)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(writer, "application/json content type required", http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, ragSmokeRelayMaxRequestBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	defer request.Body.Close()

	requestContext, cancel := context.WithTimeout(request.Context(), ragSmokeRelayRequestTimeout)
	defer cancel()
	upstreamRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, relay.endpoint, bytes.NewReader(body))
	if err != nil {
		badRelayGateway(writer)
		return
	}
	copyRelayRequestHeaders(upstreamRequest.Header, request.Header)
	relay.forwardedCount.Add(1)
	response, err := relay.client.Do(upstreamRequest)
	if err != nil {
		badRelayGateway(writer)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		badRelayGateway(writer)
		return
	}
	copyRelayResponseHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.StatusCode)
	flushRelayWriter(writer)
	_, _ = copyRelayStream(writer, response.Body)
}

func copyRelayRequestHeaders(destination, source http.Header) {
	for _, name := range []string{"Accept", "Authorization", "Content-Type"} {
		if value := source.Get(name); value != "" {
			destination.Set(name, value)
		}
	}
}

func copyRelayResponseHeaders(destination, source http.Header) {
	connectionHeaders := make(map[string]struct{})
	for _, name := range relayConnectionHeaders(source.Values("Connection")) {
		connectionHeaders[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name, values := range source {
		if _, blocked := connectionHeaders[http.CanonicalHeaderKey(name)]; blocked || relayHopByHopHeader(name) || strings.EqualFold(name, "Set-Cookie") {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func relayConnectionHeaders(values []string) []string {
	var names []string
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func relayHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func copyRelayStream(writer http.ResponseWriter, source io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var written int64
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := writer.Write(buffer[:read])
			written += int64(count)
			flushRelayWriter(writer)
			if writeErr != nil {
				return written, writeErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

func flushRelayWriter(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func methodOrPathRejected(writer http.ResponseWriter, allowedMethod string) {
	writer.Header().Set("Allow", allowedMethod)
	http.Error(writer, "method or path not allowed", http.StatusMethodNotAllowed)
}

func badRelayGateway(writer http.ResponseWriter) {
	http.Error(writer, "upstream request failed", http.StatusBadGateway)
}
