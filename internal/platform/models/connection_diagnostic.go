package models

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const maxProviderDiagnosticBodyBytes = int64(8 << 10)

// ConnectionStage identifies the bounded model connection phase that failed.
type ConnectionStage string

const (
	ConnectionStageRequest            ConnectionStage = "request"
	ConnectionStageDNS                ConnectionStage = "dns"
	ConnectionStageConnect            ConnectionStage = "connect"
	ConnectionStageTLS                ConnectionStage = "tls"
	ConnectionStageProviderResponse   ConnectionStage = "provider_response"
	ConnectionStageResponseRead       ConnectionStage = "response_read"
	ConnectionStageResponseValidation ConnectionStage = "response_validation"
	ConnectionStageCancelled          ConnectionStage = "cancelled"
	ConnectionStageTimeout            ConnectionStage = "timeout"
)

// ConnectionValidationReason identifies a fixed response-contract failure without retaining provider output.
type ConnectionValidationReason string

const (
	ConnectionValidationInvalidResponse     ConnectionValidationReason = "invalid_response"
	ConnectionValidationModelMismatch       ConnectionValidationReason = "model_mismatch"
	ConnectionValidationFinishReasonLength  ConnectionValidationReason = "finish_reason_length"
	ConnectionValidationFinishReasonInvalid ConnectionValidationReason = "finish_reason_invalid"
	ConnectionValidationEmptyContent        ConnectionValidationReason = "empty_content"
	ConnectionValidationRefusal             ConnectionValidationReason = "refusal"
	ConnectionValidationToolCalls           ConnectionValidationReason = "tool_calls"
	ConnectionValidationMissingUsage        ConnectionValidationReason = "missing_usage"
	ConnectionValidationInvalidUsage        ConnectionValidationReason = "invalid_usage"
	ConnectionValidationContractInvalid     ConnectionValidationReason = "response_contract_invalid"
)

// IsKnown reports whether the validation reason belongs to the fixed public diagnostic vocabulary.
func (reason ConnectionValidationReason) IsKnown() bool {
	switch reason {
	case ConnectionValidationInvalidResponse, ConnectionValidationModelMismatch, ConnectionValidationFinishReasonLength,
		ConnectionValidationFinishReasonInvalid, ConnectionValidationEmptyContent, ConnectionValidationRefusal,
		ConnectionValidationToolCalls, ConnectionValidationMissingUsage, ConnectionValidationInvalidUsage,
		ConnectionValidationContractInvalid:
		return true
	default:
		return false
	}
}

// ConnectionDiagnostic contains only bounded fields that are safe for the model settings test API.
type ConnectionDiagnostic struct {
	Stage              ConnectionStage
	ProviderHTTPStatus int
	ProviderErrorCode  string
	ProviderErrorType  string
	ProviderMessage    string
	ProviderRequestID  string
	TransportError     string
	ValidationReason   ConnectionValidationReason
	cause              error
}

// Error intentionally excludes provider text so generic error formatting cannot expose diagnostics.
func (diagnostic *ConnectionDiagnostic) Error() string {
	if diagnostic == nil {
		return "<nil>"
	}
	return fmt.Sprintf("model connection diagnostic(stage=%s)", diagnostic.Stage)
}

// GoString prevents `%#v` from expanding the retained internal cause.
func (diagnostic *ConnectionDiagnostic) GoString() string {
	return diagnostic.Error()
}

// Unwrap preserves cancellation and deadline checks without exposing the raw error string.
func (diagnostic *ConnectionDiagnostic) Unwrap() error {
	if diagnostic == nil {
		return nil
	}
	return diagnostic.cause
}

func providerResponseDiagnostic(response *http.Response, authorization, endpoint string) *ConnectionDiagnostic {
	return providerResponseDiagnosticBounded(response, authorization, endpoint, maxProviderDiagnosticBodyBytes)
}

func providerResponseDiagnosticBounded(response *http.Response, authorization, endpoint string, maxBodyBytes int64) *ConnectionDiagnostic {
	diagnostic := &ConnectionDiagnostic{Stage: ConnectionStageProviderResponse, ProviderHTTPStatus: response.StatusCode}
	sensitive := diagnosticSensitiveValues(authorization, endpoint)
	diagnostic.ProviderRequestID = safeDiagnosticToken(firstNonEmpty(
		response.Header.Get("x-request-id"),
		response.Header.Get("request-id"),
		response.Header.Get("x-dashscope-request-id"),
	), 256, sensitive)
	if maxBodyBytes <= 0 || maxBodyBytes > maxProviderDiagnosticBodyBytes {
		maxBodyBytes = maxProviderDiagnosticBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		diagnostic.Stage = ConnectionStageResponseRead
		diagnostic.TransportError = "response read failed"
		diagnostic.cause = err
		return diagnostic
	}
	// Reaching the read ceiling is treated as truncated even when the body may
	// be exactly that size. Connection diagnostics favor a generic message over
	// a second read that would exceed the adapter's strict error-body budget.
	if int64(len(body)) >= maxBodyBytes || !utf8.Valid(body) || !isJSONMediaType(response.Header.Get("Content-Type")) {
		diagnostic.ProviderMessage = "响应详情无法安全解析"
		return diagnostic
	}
	code, errorType, message, requestID, ok := decodeProviderDiagnostic(body)
	if !ok {
		diagnostic.ProviderMessage = "响应详情无法安全解析"
		return diagnostic
	}
	diagnostic.ProviderErrorCode = safeDiagnosticToken(code, 128, sensitive)
	diagnostic.ProviderErrorType = safeDiagnosticToken(errorType, 128, sensitive)
	diagnostic.ProviderMessage = safeDiagnosticText(message, 1024, sensitive)
	if diagnostic.ProviderRequestID == "" {
		diagnostic.ProviderRequestID = safeDiagnosticToken(requestID, 256, sensitive)
	}
	if diagnostic.ProviderErrorCode == "" && diagnostic.ProviderErrorType == "" && diagnostic.ProviderMessage == "" && diagnostic.ProviderRequestID == "" {
		diagnostic.ProviderMessage = "响应详情无法安全解析"
	}
	return diagnostic
}

func responseValidationDiagnostic(cause ...error) *ConnectionDiagnostic {
	return responseValidationDiagnosticWithReason(ConnectionValidationInvalidResponse, cause...)
}

func responseValidationDiagnosticWithReason(reason ConnectionValidationReason, cause ...error) *ConnectionDiagnostic {
	diagnostic := &ConnectionDiagnostic{Stage: ConnectionStageResponseValidation, ValidationReason: reason}
	if len(cause) > 0 {
		diagnostic.cause = cause[0]
	}
	return diagnostic
}

func withResponseValidationDiagnostic(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return foundation.NewError(classified.Kind, classified.Code, classified.Retryable, responseValidationDiagnosticWithReason(ConnectionValidationContractInvalid, err))
	}
	return err
}

func transportDiagnostic(ctx context.Context, cause error, fallbackStage ConnectionStage) *ConnectionDiagnostic {
	diagnostic := &ConnectionDiagnostic{Stage: fallbackStage, cause: cause}
	switch {
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		diagnostic.Stage = ConnectionStageCancelled
		diagnostic.TransportError = "request cancelled"
		diagnostic.cause = context.Canceled
		return diagnostic
	case ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		diagnostic.Stage = ConnectionStageTimeout
		diagnostic.TransportError = "deadline exceeded"
		diagnostic.cause = context.DeadlineExceeded
		return diagnostic
	}

	root := unwrapURLError(cause)
	var dnsError *net.DNSError
	var networkError net.Error
	var operationError *net.OpError
	var recordHeaderError tls.RecordHeaderError
	var unknownAuthorityError x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateError x509.CertificateInvalidError
	switch {
	case errors.As(root, &dnsError):
		diagnostic.Stage = ConnectionStageDNS
		diagnostic.TransportError = "DNS lookup failed"
	case fallbackStage != ConnectionStageResponseRead && errors.Is(root, io.EOF):
		diagnostic.Stage = ConnectionStageTLS
		diagnostic.TransportError = "EOF"
	case fallbackStage != ConnectionStageResponseRead && errors.Is(root, io.ErrUnexpectedEOF):
		diagnostic.Stage = ConnectionStageTLS
		diagnostic.TransportError = "unexpected EOF"
	case errors.As(root, &recordHeaderError), errors.As(root, &unknownAuthorityError), errors.As(root, &hostnameError), errors.As(root, &certificateError):
		diagnostic.Stage = ConnectionStageTLS
		diagnostic.TransportError = "TLS validation failed"
	case errors.Is(root, syscall.ECONNREFUSED):
		diagnostic.Stage = ConnectionStageConnect
		diagnostic.TransportError = "connection refused"
	case errors.Is(root, syscall.ECONNRESET):
		diagnostic.Stage = ConnectionStageConnect
		diagnostic.TransportError = "connection reset"
	case errors.As(root, &networkError) && networkError.Timeout():
		diagnostic.Stage = ConnectionStageTimeout
		diagnostic.TransportError = "network timeout"
	case errors.As(root, &operationError):
		if strings.EqualFold(operationError.Op, "read") {
			diagnostic.Stage = ConnectionStageResponseRead
			diagnostic.TransportError = "response read failed"
		} else {
			diagnostic.Stage = ConnectionStageConnect
			diagnostic.TransportError = "connection failed"
		}
	default:
		if diagnostic.Stage == "" {
			diagnostic.Stage = ConnectionStageConnect
		}
		if diagnostic.Stage == ConnectionStageResponseRead {
			diagnostic.TransportError = "response read failed"
		} else {
			diagnostic.TransportError = "connection failed"
		}
	}
	return diagnostic
}

func unwrapURLError(err error) error {
	for {
		var urlError *url.Error
		if !errors.As(err, &urlError) || urlError.Err == nil || urlError.Err == err {
			return err
		}
		err = urlError.Err
	}
}

func isJSONMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"))
}

func decodeProviderDiagnostic(body []byte) (string, string, string, string, bool) {
	type openAIError struct {
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Param   json.RawMessage `json:"param"`
	}
	type openAIEnvelope struct {
		Error     openAIError `json:"error"`
		RequestID string      `json:"request_id"`
	}
	var openAI openAIEnvelope
	if decodeStrictDiagnostic(body, &openAI) == nil && (len(openAI.Error.Code) > 0 || openAI.Error.Type != "" || openAI.Error.Message != "") {
		return diagnosticCode(openAI.Error.Code), openAI.Error.Type, openAI.Error.Message, openAI.RequestID, true
	}
	type dashScopeEnvelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	var dashScope dashScopeEnvelope
	if decodeStrictDiagnostic(body, &dashScope) == nil && (dashScope.Code != "" || dashScope.Message != "" || dashScope.RequestID != "") {
		return dashScope.Code, "", dashScope.Message, dashScope.RequestID, true
	}
	return "", "", "", "", false
}

func decodeStrictDiagnostic(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("provider diagnostic contains trailing data")
	}
	return nil
}

func diagnosticCode(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil {
		return number.String()
	}
	return ""
}

func diagnosticSensitiveValues(authorization, endpoint string) []string {
	values := make([]string, 0, 5)
	for _, value := range []string{authorization, strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")), endpoint} {
		if value != "" {
			values = append(values, value)
		}
	}
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		values = append(values, parsed.Host)
	}
	return values
}

func safeDiagnosticToken(value string, maxBytes int, sensitive []string) string {
	value = safeDiagnosticText(value, maxBytes, sensitive)
	if value == "" {
		return ""
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return ""
	}
	for _, character := range value {
		if character > 0x7f || !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._:/-", character)) {
			return ""
		}
	}
	return value
}

func safeDiagnosticText(value string, maxBytes int, sensitive []string) string {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || len([]byte(value)) > maxBytes {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "authorization") || strings.Contains(lower, "bearer") || strings.Contains(lower, "credential") {
		return ""
	}
	for _, secret := range sensitive {
		if secret != "" && strings.Contains(lower, strings.ToLower(secret)) {
			return ""
		}
	}
	for _, character := range value {
		if character == utf8.RuneError || unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return ""
		}
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
