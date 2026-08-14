package localmodelruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// childOllamaOrigin is intentionally different from the supervisor's
	// network-facing proxy port. It is never configurable by settings or DB
	// input.
	childOllamaOrigin = "http://127.0.0.1:11435"

	defaultOllamaRequestTimeout = 30 * time.Second
	defaultOllamaPullTimeout    = 6 * time.Hour
	defaultPullNoProgress       = 5 * time.Minute
	defaultOllamaMaxResponse    = 8 << 20
	defaultOllamaMaxPullEvent   = 16 << 10
)

var (
	ErrOllamaUnavailable   = errors.New("ollama service is unavailable")
	ErrOllamaResponse      = errors.New("ollama response is invalid")
	ErrOllamaModelMissing  = errors.New("ollama model is missing")
	ErrOllamaPullStalled   = errors.New("ollama model pull made no progress")
	ErrOllamaRequestLimit  = errors.New("ollama request or response exceeded its limit")
	ErrOllamaStatusFailure = errors.New("ollama returned an unsuccessful status")
)

// OllamaClientOptions configures the fixed child API adapter. BaseURL may be
// omitted, but any supplied value must be the compile-time child origin. A
// custom RoundTripper is useful for tests and remains caller-owned.
type OllamaClientOptions struct {
	BaseURL           string
	HTTPClient        *http.Client
	Transport         http.RoundTripper
	RequestTimeout    time.Duration
	PullTimeout       time.Duration
	NoProgressTimeout time.Duration
	MaxResponseBytes  int64
	MaxPullEventBytes int64
}

// PullProgress is the bounded, provider-independent progress projection. A
// zero Total means the upstream has not reported a total yet.
type PullProgress struct {
	Model     ModelRef
	Status    string
	Completed int64
	Total     *int64
	Digest    string
}

// OllamaModel is the small manifest projection needed by lifecycle code. Raw
// provider JSON is deliberately never retained or returned.
type OllamaModel struct {
	Name      ModelRef
	Digest    string
	Bytes     int64
	Modified  string
	Family    string
	Parameter string
}

// OllamaControlClient is the reconciler's fixed management API seam.
type OllamaControlClient interface {
	Health(context.Context) error
	Tags(context.Context) ([]OllamaModel, error)
	Show(context.Context, ModelRef) (OllamaModel, error)
	Pull(context.Context, ModelRef, func(PullProgress) error) (OllamaModel, error)
}

var _ OllamaControlClient = (*OllamaClient)(nil)

// OllamaClient talks only to the child loopback API. It does not expose the
// lifecycle endpoints through the supervisor proxy.
type OllamaClient struct {
	baseURL           *url.URL
	client            *http.Client
	requestTimeout    time.Duration
	pullTimeout       time.Duration
	noProgressTimeout time.Duration
	maxResponseBytes  int64
	maxPullEventBytes int64
}

// NewOllamaClient creates a fixed-address client with bounded response reads.
func NewOllamaClient(options OllamaClientOptions) (*OllamaClient, error) {
	base := options.BaseURL
	if base == "" {
		base = childOllamaOrigin
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "http" || parsed.Host != "127.0.0.1:11435" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, errors.New("ollama child endpoint must be the fixed loopback origin")
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = defaultOllamaRequestTimeout
	}
	if options.PullTimeout == 0 {
		options.PullTimeout = defaultOllamaPullTimeout
	}
	if options.NoProgressTimeout == 0 {
		options.NoProgressTimeout = defaultPullNoProgress
	}
	if options.MaxResponseBytes == 0 {
		options.MaxResponseBytes = defaultOllamaMaxResponse
	}
	if options.MaxPullEventBytes == 0 {
		options.MaxPullEventBytes = defaultOllamaMaxPullEvent
	}
	if options.RequestTimeout <= 0 || options.RequestTimeout > 5*time.Minute || options.PullTimeout <= 0 || options.PullTimeout > 24*time.Hour || options.NoProgressTimeout <= 0 || options.NoProgressTimeout > 30*time.Minute || options.MaxResponseBytes <= 0 || options.MaxResponseBytes > 64<<20 || options.MaxPullEventBytes <= 0 || options.MaxPullEventBytes > 1<<20 {
		return nil, errors.New("ollama client limits are invalid")
	}

	httpClient := &http.Client{}
	if options.HTTPClient != nil {
		clone := cloneHTTPClient(*options.HTTPClient)
		httpClient = &clone
	}
	if options.Transport != nil {
		httpClient.Transport = options.Transport
	} else if httpClient.Transport == nil {
		transport := (&http.Transport{
			Proxy:                 nil,
			DialContext:           fixedLoopbackDialer,
			DisableCompression:    true,
			MaxIdleConns:          2,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: options.RequestTimeout,
		}).Clone()
		transport.Proxy = nil
		transport.DialContext = fixedLoopbackDialer
		httpClient.Transport = transport
	}
	// The child must never redirect a management request to another address.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OllamaClient{baseURL: parsed, client: httpClient, requestTimeout: options.RequestTimeout, pullTimeout: options.PullTimeout, noProgressTimeout: options.NoProgressTimeout, maxResponseBytes: options.MaxResponseBytes, maxPullEventBytes: options.MaxPullEventBytes}, nil
}

// NewFixedOllamaClient is a convenience constructor for production composition.
func NewFixedOllamaClient() (*OllamaClient, error) { return NewOllamaClient(OllamaClientOptions{}) }

func cloneHTTPClient(client http.Client) http.Client {
	clone := client
	return clone
}

func fixedLoopbackDialer(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, ErrOllamaUnavailable
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port != "11435" {
		return nil, ErrOllamaUnavailable
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp4", "127.0.0.1:11435")
}

// Health checks the child without loading a model.
func (client *OllamaClient) Health(ctx context.Context) error {
	if client == nil {
		return ErrOllamaUnavailable
	}
	requestCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), client.requestTimeout)
	defer cancel()
	response, err := client.do(requestCtx, http.MethodGet, "/api/version", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d", ErrOllamaStatusFailure, response.StatusCode)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := decodeBoundedJSON(response.Body, client.maxResponseBytes, &payload); err != nil || strings.TrimSpace(payload.Version) == "" {
		return ErrOllamaResponse
	}
	return nil
}

// Tags returns installed model manifests without loading them.
func (client *OllamaClient) Tags(ctx context.Context) ([]OllamaModel, error) {
	if client == nil {
		return nil, ErrOllamaUnavailable
	}
	requestCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), client.requestTimeout)
	defer cancel()
	response, err := client.do(requestCtx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrOllamaStatusFailure, response.StatusCode)
	}
	var payload struct {
		Models []struct {
			Name     string `json:"name"`
			Digest   string `json:"digest"`
			Size     int64  `json:"size"`
			Modified string `json:"modified_at"`
			Details  struct {
				Family        string `json:"family"`
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := decodeBoundedJSON(response.Body, client.maxResponseBytes, &payload); err != nil {
		return nil, ErrOllamaResponse
	}
	models := make([]OllamaModel, 0, len(payload.Models))
	for _, model := range payload.Models {
		name := ModelRef(model.Name)
		if validateModelRef(name) != nil || model.Size < 0 || !safeDigest(model.Digest) {
			return nil, ErrOllamaResponse
		}
		models = append(models, OllamaModel{Name: name, Digest: model.Digest, Bytes: model.Size, Modified: boundedText(model.Modified, 128), Family: boundedText(model.Details.Family, 128), Parameter: boundedText(model.Details.ParameterSize, 64)})
	}
	return models, nil
}

// Show verifies one exact model and returns its bounded manifest projection.
func (client *OllamaClient) Show(ctx context.Context, model ModelRef) (OllamaModel, error) {
	if client == nil {
		return OllamaModel{}, ErrOllamaUnavailable
	}
	if err := validateModelRef(model); err != nil {
		return OllamaModel{}, err
	}
	body, _ := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: string(model)})
	requestCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), client.requestTimeout)
	defer cancel()
	response, err := client.do(requestCtx, http.MethodPost, "/api/show", bytes.NewReader(body))
	if err != nil {
		return OllamaModel{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return OllamaModel{}, ErrOllamaModelMissing
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return OllamaModel{}, fmt.Errorf("%w: status %d", ErrOllamaStatusFailure, response.StatusCode)
	}
	var payload struct {
		Model   string `json:"model"`
		Digest  string `json:"digest"`
		Size    int64  `json:"size"`
		Details struct {
			Family        string `json:"family"`
			ParameterSize string `json:"parameter_size"`
		} `json:"details"`
	}
	if err := decodeBoundedJSON(response.Body, client.maxResponseBytes, &payload); err != nil {
		return OllamaModel{}, ErrOllamaResponse
	}
	name := ModelRef(payload.Model)
	if name == "" {
		name = model
	}
	if !sameModelRef(name, model) || payload.Size < 0 || !safeDigest(payload.Digest) {
		return OllamaModel{}, ErrOllamaResponse
	}
	return OllamaModel{Name: model, Digest: payload.Digest, Bytes: payload.Size, Family: boundedText(payload.Details.Family, 128), Parameter: boundedText(payload.Details.ParameterSize, 64)}, nil
}

// Pull downloads one exact model and reports bounded, monotonic stream events.
func (client *OllamaClient) Pull(ctx context.Context, model ModelRef, callback func(PullProgress) error) (OllamaModel, error) {
	if client == nil {
		return OllamaModel{}, ErrOllamaUnavailable
	}
	if err := validateModelRef(model); err != nil {
		return OllamaModel{}, err
	}
	body, _ := json.Marshal(struct {
		Name     string `json:"name"`
		Insecure bool   `json:"insecure"`
	}{Name: string(model), Insecure: false})
	pullCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), client.pullTimeout)
	defer cancel()
	var stalled atomic.Bool
	stallTimer := time.AfterFunc(client.noProgressTimeout, func() {
		stalled.Store(true)
		cancel()
	})
	defer stallTimer.Stop()
	response, err := client.do(pullCtx, http.MethodPost, "/api/pull", bytes.NewReader(body))
	if err != nil {
		if stalled.Load() {
			return OllamaModel{}, ErrOllamaPullStalled
		}
		return OllamaModel{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return OllamaModel{}, fmt.Errorf("%w: status %d", ErrOllamaStatusFailure, response.StatusCode)
	}
	reader := bufio.NewReaderSize(io.LimitReader(response.Body, client.maxResponseBytes+1), 32<<10)
	var streamBytes int64
	var last int64
	var digest string
	var total *int64
	var lastStatus string
	var sawSuccess bool
	for {
		var event struct {
			Status    string `json:"status"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
			Digest    string `json:"digest"`
			Error     string `json:"error"`
		}
		line, readErr := reader.ReadBytes('\n')
		if stalled.Load() {
			return OllamaModel{}, ErrOllamaPullStalled
		}
		streamBytes += int64(len(line))
		if streamBytes > client.maxResponseBytes {
			return OllamaModel{}, ErrOllamaRequestLimit
		}
		if len(line) == 0 && readErr != nil {
			if readErr == io.EOF {
				break
			}
			return OllamaModel{}, fmt.Errorf("%w: pull stream read failed", ErrOllamaUnavailable)
		}
		if int64(len(line)) > client.maxPullEventBytes {
			return OllamaModel{}, ErrOllamaRequestLimit
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if readErr == io.EOF {
				break
			}
			continue
		}
		err := json.Unmarshal(line, &event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return OllamaModel{}, ErrOllamaResponse
		}
		if event.Error != "" {
			return OllamaModel{}, ErrOllamaStatusFailure
		}
		if event.Completed < 0 || event.Total < 0 || event.Total > 0 && event.Completed > event.Total {
			return OllamaModel{}, ErrOllamaResponse
		}
		madeProgress := false
		if event.Total > 0 {
			value := event.Total
			if total != nil && *total > value {
				value = *total
			}
			if total == nil || value > *total {
				madeProgress = true
			}
			total = &value
		}
		if event.Completed > last {
			last = event.Completed
			madeProgress = true
		}
		if event.Digest != "" {
			if !safeDigest(event.Digest) {
				return OllamaModel{}, ErrOllamaResponse
			}
			if digest != event.Digest {
				madeProgress = true
				digest = event.Digest
			}
		}
			status := boundedText(event.Status, 128)
			if status == "" && event.Status != "" {
				return OllamaModel{}, ErrOllamaResponse
			}
			if status != "" && status != lastStatus {
				lastStatus = status
			}
		if strings.EqualFold(strings.TrimSpace(status), "success") {
			sawSuccess = true
		}
		if callback != nil {
			progress := PullProgress{Model: model, Status: status, Completed: last, Total: cloneInt64(total), Digest: digest}
			if err := callback(progress); err != nil {
				return OllamaModel{}, err
			}
		}
		if madeProgress {
			stallTimer.Stop()
			stallTimer.Reset(client.noProgressTimeout)
		}
		if readErr != nil && readErr != io.EOF {
			return OllamaModel{}, fmt.Errorf("%w: pull stream read failed", ErrOllamaUnavailable)
		}
		if readErr == io.EOF {
			break
		}
	}
	if !sawSuccess {
		return OllamaModel{}, ErrOllamaResponse
	}
	result, err := client.Show(pullCtx, model)
	if err != nil {
		return OllamaModel{}, err
	}
	if digest != "" && result.Digest != "" && digest != result.Digest {
		return OllamaModel{}, ErrOllamaResponse
	}
	return result, nil
}

func (client *OllamaClient) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if client == nil || client.baseURL == nil || client.client == nil {
		return nil, ErrOllamaUnavailable
	}
	request, err := http.NewRequestWithContext(ctxOrBackground(ctx), method, client.baseURL.String()+path, body)
	if err != nil {
		return nil, ErrOllamaUnavailable
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrOllamaUnavailable, safeTransportError(err))
	}
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "http" || response.Request.URL.Host != "127.0.0.1:11435" {
		_ = response.Body.Close()
		return nil, ErrOllamaUnavailable
	}
	return response, nil
}

func decodeBoundedJSON(reader io.Reader, limit int64, target any) error {
	if limit <= 0 {
		return ErrOllamaRequestLimit
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(encoded)) > limit {
		return ErrOllamaRequestLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrOllamaResponse
	}
	return nil
}

func safeTransportError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "request failed"
}

func safeDigest(value string) bool {
	if value == "" {
		return true
	}
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func sameModelRef(left, right ModelRef) bool {
	if left == right {
		return true
	}
	return withDefaultTag(left) == withDefaultTag(right)
}

func withDefaultTag(model ModelRef) ModelRef {
	value := string(model)
	lastSlash := strings.LastIndexByte(value, '/')
	name := value[lastSlash+1:]
	if strings.ContainsAny(name, ":@") {
		return model
	}
	return ModelRef(value + ":latest")
}

func boundedText(value string, max int) string {
	if len(value) > max {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
