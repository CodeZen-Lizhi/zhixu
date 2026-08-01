package hostcontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/go-chi/chi/v5"
)

const maxControlBodyBytes = 64 << 10

const (
	runtimeStatusHeader      = "X-Zhixu-Runtime-Status"
	runtimeStatusUnavailable = "unavailable"
)

// HandlerOptions fixes the browser origin, host, credential authority, and runtime seams.
type HandlerOptions struct {
	Authority     *SessionAuthority
	Store         Store
	Backend       BackendLocator
	Static        http.Handler
	ExpectedHost  string
	Origin        string
	SecureCookie  bool
	PathValidator PathValidator
}

// Handler serves the static workbench, isolated control API, and ready-only business proxy.
type Handler struct {
	authority     *SessionAuthority
	store         Store
	backend       BackendLocator
	static        http.Handler
	expectedHost  string
	origin        string
	secureCookie  bool
	pathValidator PathValidator
	control       http.Handler
	host          http.Handler
	idempotency   *idempotencyRegistry
}

// NewHandler creates the stable host entrypoint without exposing a generic Docker surface.
func NewHandler(options HandlerOptions) (*Handler, error) {
	if options.Authority == nil || options.Store == nil || options.Backend == nil || options.ExpectedHost == "" || options.Origin == "" {
		return nil, errors.New("host controller HTTP options are incomplete")
	}
	parsedOrigin, err := url.Parse(options.Origin)
	if err != nil || parsedOrigin.Scheme != "http" || parsedOrigin.Host != options.ExpectedHost || parsedOrigin.Path != "" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		return nil, errors.New("host controller origin is invalid")
	}
	if options.Static == nil {
		options.Static = http.NotFoundHandler()
	}
	handler := &Handler{
		authority: options.Authority, store: options.Store, backend: options.Backend, static: options.Static,
		expectedHost: options.ExpectedHost, origin: options.Origin, secureCookie: options.SecureCookie,
		pathValidator: options.PathValidator, idempotency: newIdempotencyRegistry(512),
	}
	handler.control = handler.controlRouter()
	handler.host = handler.hostRouter()
	return handler, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	switch {
	case path == "/control" || strings.HasPrefix(path, "/control/"):
		handler.control.ServeHTTP(writer, request)
	case path == "/host" || strings.HasPrefix(path, "/host/"):
		handler.host.ServeHTTP(writer, request)
	case path == "/api/v1" || strings.HasPrefix(path, "/api/v1/") || path == "/livez" || path == "/readyz":
		handler.proxyBusiness(writer, request)
	case path == "/api" || strings.HasPrefix(path, "/api/"):
		writeProblem(writer, responseProblem(http.StatusNotFound, "CONTROL_ROUTE_NOT_FOUND", "请求的接口不存在", false, nil, ""))
	default:
		handler.static.ServeHTTP(writer, request)
	}
}

func (handler *Handler) hostRouter() http.Handler {
	router := chi.NewRouter()
	router.Get("/host/v1/runtime", handler.currentRuntimeAccess)
	router.Head("/host/v1/runtime", handler.currentRuntimeAccess)
	router.NotFound(func(writer http.ResponseWriter, request *http.Request) {
		writeHostResponse(writer, request, responseProblem(http.StatusNotFound, "HOST_ROUTE_NOT_FOUND", "请求的本机接口不存在", false, nil, ""))
	})
	router.MethodNotAllowed(func(writer http.ResponseWriter, request *http.Request) {
		writeHostResponse(writer, request, responseProblem(http.StatusMethodNotAllowed, "HOST_METHOD_NOT_ALLOWED", "本机接口不支持该方法", false, nil, ""))
	})
	return router
}

type runtimeAccessResponse struct {
	Status            RuntimeAccessStatus `json:"status"`
	ActiveWorkspaceID *string             `json:"active_workspace_id"`
	PollAfterMS       int                 `json:"poll_after_ms"`
}

func (handler *Handler) currentRuntimeAccess(writer http.ResponseWriter, request *http.Request) {
	if request.Host != handler.expectedHost {
		writeHostResponse(writer, request, responseProblem(http.StatusForbidden, "HOST_REQUEST_INVALID", "本机请求来源无效", false, nil, ""))
		return
	}
	access, err := handler.backend.LocateRuntime(request.Context())
	if err != nil || !validRuntimeAccess(access) {
		writeHostResponse(writer, request, responseProblem(http.StatusServiceUnavailable, "RUNTIME_ACCESS_UNAVAILABLE", "业务运行时状态暂不可用", true, nil, ""))
		return
	}
	var workspaceID *string
	if access.Status == RuntimeAccessReady {
		value := access.WorkspaceID
		workspaceID = &value
	}
	response := runtimeAccessResponse{Status: access.Status, ActiveWorkspaceID: workspaceID, PollAfterMS: access.PollAfterMS}
	writeHostResponse(writer, request, jsonResponse(http.StatusOK, response))
}

func writeHostResponse(writer http.ResponseWriter, request *http.Request, response cachedResponse) {
	if request.Method == http.MethodHead {
		response.Body = nil
	}
	writeCachedResponse(writer, response)
}

func (handler *Handler) controlRouter() http.Handler {
	router := chi.NewRouter()
	router.Get("/control/v1/livez", handler.livez)
	router.Post("/control/v1/sessions", handler.exchangeSession)
	router.Get("/control/v1/session", handler.currentSession)
	router.Get("/control/v1/state", handler.currentState)
	router.Post("/control/v1/workspace-switches", handler.beginSwitch)
	router.Get("/control/v1/workspace-switches/{operationID}", handler.getOperation)
	router.Post("/control/v1/workspaces/{workspaceID}/availability-checks", handler.checkAvailability)
	router.Delete("/control/v1/workspaces/{workspaceID}", handler.removeWorkspace)
	router.NotFound(func(writer http.ResponseWriter, _ *http.Request) {
		writeProblem(writer, responseProblem(http.StatusNotFound, "CONTROL_ROUTE_NOT_FOUND", "请求的控制接口不存在", false, nil, ""))
	})
	router.MethodNotAllowed(func(writer http.ResponseWriter, _ *http.Request) {
		writeProblem(writer, responseProblem(http.StatusMethodNotAllowed, "CONTROL_METHOD_NOT_ALLOWED", "控制接口不支持该方法", false, nil, ""))
	})
	return router
}

func (handler *Handler) livez(writer http.ResponseWriter, request *http.Request) {
	if request.Host != handler.expectedHost {
		writeProblem(writer, responseProblem(http.StatusForbidden, "CONTROL_HOST_INVALID", "控制请求来源无效", false, nil, ""))
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) exchangeSession(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if err := handler.requireHostAndOrigin(request); err != nil {
		writeControllerError(writer, err)
		return
	}
	if !bodyIsEmpty(request) {
		writeProblem(writer, responseProblem(http.StatusBadRequest, "CONTROL_BODY_INVALID", "会话交换不接受请求体", false, nil, ""))
		return
	}
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		writeControllerError(writer, unauthorizedFault())
		return
	}
	token, ok := bearerCredential(values[0])
	if !ok {
		writeControllerError(writer, unauthorizedFault())
		return
	}
	credential, err := handler.authority.Exchange(token)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: ControlSessionCookie, Value: credential.CookieToken, Path: "/control/", HttpOnly: true,
		Secure: handler.secureCookie, SameSite: http.SameSiteStrictMode, Expires: credential.ExpiresAt,
		MaxAge: max(1, int(time.Until(credential.ExpiresAt).Seconds())),
	})
	writeJSON(writer, http.StatusCreated, credential)
}

func (handler *Handler) currentSession(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	credential, err := handler.authenticate(request)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, credential)
}

func (handler *Handler) currentState(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if _, err := handler.authenticate(request); err != nil {
		writeControllerError(writer, err)
		return
	}
	state, err := handler.store.State(request.Context())
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	state.ControllerInstanceID = handler.authority.InstanceID()
	if state.RecentWorkspaces == nil {
		state.RecentWorkspaces = []Workspace{}
	}
	writer.Header().Set("ETag", fmt.Sprintf("\"%d\"", state.StateVersion))
	writeJSON(writer, http.StatusOK, state)
}

type switchRequest struct {
	TargetKind    string `json:"target_kind"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	Name          string `json:"name,omitempty"`
	RootPath      string `json:"root_path,omitempty"`
	InitializeGit *bool  `json:"initialize_git,omitempty"`
}

func (handler *Handler) beginSwitch(writer http.ResponseWriter, request *http.Request) {
	credential, expectedVersion, idempotencyKey, err := handler.authorizeUnsafe(request)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	body, err := readStrictJSON(request)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	handler.executeIdempotent(writer, credential.SessionID, idempotencyKey, request, expectedVersion, body, func(requestHash string) cachedResponse {
		var input switchRequest
		if err := decodeStrictJSON(body, &input); err != nil {
			return problemResponse(http.StatusBadRequest, "CONTROL_BODY_INVALID", "控制请求格式无效", false, nil, "")
		}
		command := SwitchCommand{
			ExpectedStateVersion: expectedVersion, ControllerInstanceID: handler.authority.InstanceID(),
			IdempotencyKey: idempotencyKey, RequestHash: requestHash, TargetKind: input.TargetKind,
		}
		switch input.TargetKind {
		case "registered":
			if !grantIdentityPattern.MatchString(input.WorkspaceID) || input.Name != "" || input.RootPath != "" || input.InitializeGit != nil {
				return problemResponse(http.StatusBadRequest, "WORKSPACE_SWITCH_TARGET_INVALID", "Workspace 切换目标无效", false, map[string]string{"workspace_id": "invalid"}, "")
			}
			command.WorkspaceID = input.WorkspaceID
		case "new":
			if input.WorkspaceID != "" || strings.TrimSpace(input.Name) == "" || input.Name != strings.TrimSpace(input.Name) || len(input.Name) > 256 || input.InitializeGit == nil {
				return problemResponse(http.StatusBadRequest, "WORKSPACE_SWITCH_TARGET_INVALID", "Workspace 切换目标无效", false, map[string]string{"name": "invalid"}, "")
			}
			validated, validationErr := handler.pathValidator.Validate(input.RootPath)
			if validationErr != nil {
				code := "WORKSPACE_PATH_INVALID"
				var pathError *PathError
				if errors.As(validationErr, &pathError) {
					code = pathError.Code
				}
				return problemResponse(http.StatusBadRequest, code, "Workspace 路径无效", false, map[string]string{"root_path": code}, "")
			}
			command.Name, command.RootPath, command.InitializeGit = input.Name, validated.CanonicalPath, *input.InitializeGit
			command.RootFingerprint, command.BindingVersion = validated.Fingerprint.Digest(), validated.Fingerprint.BindingVersion
		default:
			return problemResponse(http.StatusBadRequest, "WORKSPACE_SWITCH_TARGET_INVALID", "Workspace 切换目标无效", false, map[string]string{"target_kind": "invalid"}, "")
		}
		operation, storeErr := handler.store.BeginSwitch(request.Context(), command)
		if storeErr != nil {
			return cachedControllerError(storeErr)
		}
		return jsonResponse(http.StatusAccepted, map[string]any{"operation": operation})
	})
}

func (handler *Handler) getOperation(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if _, err := handler.authenticate(request); err != nil {
		writeControllerError(writer, err)
		return
	}
	operationID := chi.URLParam(request, "operationID")
	if !grantIdentityPattern.MatchString(operationID) {
		writeProblem(writer, responseProblem(http.StatusNotFound, "WORKSPACE_SWITCH_NOT_FOUND", "切换操作不存在", false, nil, ""))
		return
	}
	operation, err := handler.store.Operation(request.Context(), operationID)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"operation": operation})
}

func (handler *Handler) checkAvailability(writer http.ResponseWriter, request *http.Request) {
	credential, expectedVersion, idempotencyKey, err := handler.authorizeUnsafe(request)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	body, err := readStrictJSON(request)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	workspaceID := chi.URLParam(request, "workspaceID")
	handler.executeIdempotent(writer, credential.SessionID, idempotencyKey, request, expectedVersion, body, func(string) cachedResponse {
		var empty struct{}
		if !grantIdentityPattern.MatchString(workspaceID) || decodeStrictJSON(body, &empty) != nil {
			return problemResponse(http.StatusBadRequest, "CONTROL_BODY_INVALID", "可用性检查请求无效", false, nil, "")
		}
		workspace, storeErr := handler.store.CheckAvailability(request.Context(), workspaceID, expectedVersion)
		if storeErr != nil {
			return cachedControllerError(storeErr)
		}
		return jsonResponse(http.StatusOK, map[string]any{"workspace": workspace})
	})
}

func (handler *Handler) removeWorkspace(writer http.ResponseWriter, request *http.Request) {
	credential, expectedVersion, idempotencyKey, err := handler.authorizeUnsafe(request)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	if !bodyIsEmpty(request) {
		writeProblem(writer, responseProblem(http.StatusBadRequest, "CONTROL_BODY_INVALID", "移除请求不接受请求体", false, nil, ""))
		return
	}
	workspaceID := chi.URLParam(request, "workspaceID")
	handler.executeIdempotent(writer, credential.SessionID, idempotencyKey, request, expectedVersion, nil, func(string) cachedResponse {
		if !grantIdentityPattern.MatchString(workspaceID) {
			return problemResponse(http.StatusNotFound, "WORKSPACE_NOT_FOUND", "Workspace 不存在", false, nil, "")
		}
		if storeErr := handler.store.RemoveWorkspace(request.Context(), workspaceID, expectedVersion); storeErr != nil {
			return cachedControllerError(storeErr)
		}
		return cachedResponse{Status: http.StatusNoContent, Header: make(http.Header)}
	})
}

func (handler *Handler) authenticate(request *http.Request) (SessionCredential, error) {
	if request.Host != handler.expectedHost {
		return SessionCredential{}, &Fault{Code: "CONTROL_HOST_INVALID", Message: "控制请求来源无效", Status: 403}
	}
	cookies := request.Cookies()
	value := ""
	count := 0
	for _, cookie := range cookies {
		if cookie.Name == ControlSessionCookie {
			count++
			value = cookie.Value
		}
	}
	if count != 1 || value == "" {
		return SessionCredential{}, unauthorizedFault()
	}
	return handler.authority.Authenticate(value)
}

func (handler *Handler) authorizeUnsafe(request *http.Request) (SessionCredential, int64, string, error) {
	credential, err := handler.authenticate(request)
	if err != nil {
		return SessionCredential{}, 0, "", err
	}
	if err := handler.requireHostAndOrigin(request); err != nil {
		return SessionCredential{}, 0, "", err
	}
	csrfValues := request.Header.Values(ControlCSRFHeader)
	if len(csrfValues) != 1 || subtle.ConstantTimeCompare([]byte(csrfValues[0]), []byte(credential.CSRFToken)) != 1 {
		return SessionCredential{}, 0, "", &Fault{Code: "CONTROL_CSRF_INVALID", Message: "控制请求校验失败", Status: 403}
	}
	idempotencyValues := request.Header.Values("Idempotency-Key")
	if len(idempotencyValues) != 1 || !validIdempotencyKey(idempotencyValues[0]) {
		return SessionCredential{}, 0, "", &Fault{Code: "IDEMPOTENCY_KEY_INVALID", Message: "幂等键无效", Status: 400}
	}
	matchValues := request.Header.Values("If-Match")
	if len(matchValues) != 1 {
		return SessionCredential{}, 0, "", &Fault{Code: "STATE_VERSION_REQUIRED", Message: "需要当前状态版本", Status: 409}
	}
	expectedVersion, err := parseStateVersion(matchValues[0])
	if err != nil {
		return SessionCredential{}, 0, "", &Fault{Code: "STATE_VERSION_INVALID", Message: "状态版本无效", Status: 409}
	}
	return credential, expectedVersion, idempotencyValues[0], nil
}

func (handler *Handler) requireHostAndOrigin(request *http.Request) error {
	if request.Host != handler.expectedHost {
		return &Fault{Code: "CONTROL_HOST_INVALID", Message: "控制请求来源无效", Status: 403}
	}
	origins := request.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != handler.origin {
		return &Fault{Code: "CONTROL_ORIGIN_INVALID", Message: "控制请求来源无效", Status: 403}
	}
	return nil
}

func (handler *Handler) executeIdempotent(
	writer http.ResponseWriter,
	sessionID, key string,
	request *http.Request,
	expectedVersion int64,
	body []byte,
	action func(string) cachedResponse,
) {
	digest := sha256.New()
	_, _ = digest.Write([]byte(request.Method))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(request.URL.Path))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(strconv.FormatInt(expectedVersion, 10)))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(body)
	requestHash := fmt.Sprintf("%x", digest.Sum(nil))
	response := handler.idempotency.execute(sessionID, key, requestHash, func() cachedResponse { return action(requestHash) })
	writeCachedResponse(writer, response)
}

func (handler *Handler) proxyBusiness(writer http.ResponseWriter, request *http.Request) {
	access, err := handler.backend.LocateRuntime(request.Context())
	if err != nil || access.Status != RuntimeAccessReady {
		writer.Header().Set(runtimeStatusHeader, runtimeStatusUnavailable)
		writeProblem(writer, responseProblem(http.StatusServiceUnavailable, "RUNTIME_NOT_READY", "业务运行时尚未就绪", true, nil, ""))
		return
	}
	if !validRuntimeAccess(access) {
		writer.Header().Set(runtimeStatusHeader, runtimeStatusUnavailable)
		writeProblem(writer, responseProblem(http.StatusServiceUnavailable, "RUNTIME_BACKEND_INVALID", "业务运行时尚未就绪", true, nil, ""))
		return
	}
	backend := access.Backend
	originalHost := request.Host
	proxy := httputil.NewSingleHostReverseProxy(backend)
	originalDirector := proxy.Director
	proxy.Director = func(outbound *http.Request) {
		originalDirector(outbound)
		outbound.Host = originalHost
		stripControlHeaders(outbound, handler.authority)
	}
	proxy.FlushInterval = -1
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del(runtimeStatusHeader)
		filterControlSetCookie(response.Header)
		return nil
	}
	proxy.ErrorHandler = func(responseWriter http.ResponseWriter, _ *http.Request, _ error) {
		responseWriter.Header().Set(runtimeStatusHeader, runtimeStatusUnavailable)
		writeProblem(responseWriter, responseProblem(http.StatusBadGateway, "RUNTIME_PROXY_FAILED", "业务运行时连接失败", true, nil, ""))
	}
	proxy.ServeHTTP(writer, request)
}

type problem struct {
	Code        string            `json:"code"`
	Message     string            `json:"message"`
	Retryable   bool              `json:"retryable"`
	OperationID string            `json:"operation_id,omitempty"`
	FieldErrors map[string]string `json:"field_errors,omitempty"`
}

type cachedResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func responseProblem(status int, code, message string, retryable bool, fieldErrors map[string]string, operationID string) cachedResponse {
	return problemResponse(status, code, message, retryable, fieldErrors, operationID)
}

func problemResponse(status int, code, message string, retryable bool, fieldErrors map[string]string, operationID string) cachedResponse {
	body, _ := json.Marshal(problem{Code: code, Message: message, Retryable: retryable, FieldErrors: fieldErrors, OperationID: operationID})
	return cachedResponse{Status: status, Header: http.Header{"Content-Type": []string{"application/problem+json"}, "Cache-Control": []string{"no-store"}}, Body: body}
}

func cachedControllerError(err error) cachedResponse {
	if fault, ok := AsFault(err); ok {
		status := fault.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return problemResponse(status, fault.Code, fault.Message, fault.Retryable, fault.FieldErrors, fault.OperationID)
	}
	return problemResponse(http.StatusInternalServerError, "CONTROL_INTERNAL_ERROR", "控制服务暂不可用", false, nil, "")
}

func writeControllerError(writer http.ResponseWriter, err error) {
	writeCachedResponse(writer, cachedControllerError(err))
}

func writeProblem(writer http.ResponseWriter, response cachedResponse) {
	writeCachedResponse(writer, response)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writeCachedResponse(writer, jsonResponse(status, value))
}

func jsonResponse(status int, value any) cachedResponse {
	body, _ := json.Marshal(value)
	return cachedResponse{Status: status, Header: http.Header{"Content-Type": []string{"application/json"}, "Cache-Control": []string{"no-store"}}, Body: body}
}

func writeCachedResponse(writer http.ResponseWriter, response cachedResponse) {
	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.Status)
	if len(response.Body) > 0 {
		_, _ = writer.Write(response.Body)
	}
}

func noStore(writer http.ResponseWriter) { writer.Header().Set("Cache-Control", "no-store") }

func bodyIsEmpty(request *http.Request) bool {
	return (request.Body == nil || request.ContentLength == 0) && request.ContentLength >= 0 && len(request.TransferEncoding) == 0
}

func readStrictJSON(request *http.Request) ([]byte, error) {
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return nil, &Fault{Code: "CONTROL_CONTENT_TYPE_INVALID", Message: "控制请求必须使用 JSON", Status: 415}
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" || len(parameters) != 0 {
		return nil, &Fault{Code: "CONTROL_CONTENT_TYPE_INVALID", Message: "控制请求必须使用 JSON", Status: 415}
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxControlBodyBytes+1))
	if err != nil || len(body) > maxControlBodyBytes {
		return nil, &Fault{Code: "CONTROL_BODY_TOO_LARGE", Message: "控制请求体过大", Status: 413}
	}
	if len(body) == 0 || !utf8.Valid(body) || !strictjson.ValidUnicode(body) {
		return nil, &Fault{Code: "CONTROL_BODY_INVALID", Message: "控制请求格式无效", Status: 400}
	}
	return body, nil
}

func decodeStrictJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func parseStateVersion(value string) (int64, error) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("state version is not a strong ETag")
	}
	parsed, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || parsed < 1 {
		return 0, errors.New("state version is invalid")
	}
	return parsed, nil
}

func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validBackendURL(value *url.URL) bool {
	if value == nil || value.Scheme != "http" || value.User != nil || value.Path != "" || value.RawQuery != "" || value.Fragment != "" {
		return false
	}
	host := value.Hostname()
	port := value.Port()
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() || port == "" {
		return false
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	return err == nil && parsedPort > 0
}

func stripControlHeaders(request *http.Request, authority *SessionAuthority) {
	for key := range request.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-zhixu-control-") {
			request.Header.Del(key)
		}
	}
	for _, value := range request.Header.Values("Authorization") {
		if authority.IsBootstrapAuthorization(value) {
			request.Header.Del("Authorization")
			break
		}
	}
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		if cookie.Name != ControlSessionCookie {
			request.AddCookie(cookie)
		}
	}
}

func filterControlSetCookie(header http.Header) {
	values := header.Values("Set-Cookie")
	header.Del("Set-Cookie")
	for _, value := range values {
		name, _, found := strings.Cut(value, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), ControlSessionCookie) {
			header.Add("Set-Cookie", value)
		}
	}
}

type idempotencyRegistry struct {
	mu      sync.Mutex
	limit   int
	entries map[string]idempotencyEntry
	order   []string
}

type idempotencyEntry struct {
	Digest   string
	Response cachedResponse
}

func newIdempotencyRegistry(limit int) *idempotencyRegistry {
	return &idempotencyRegistry{limit: limit, entries: make(map[string]idempotencyEntry)}
}

func (registry *idempotencyRegistry) execute(sessionID, key, digest string, action func() cachedResponse) cachedResponse {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	compound := sessionID + "\x00" + key
	if entry, found := registry.entries[compound]; found {
		if entry.Digest != digest {
			return problemResponse(http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "幂等键已用于不同请求", false, nil, "")
		}
		return cloneResponse(entry.Response)
	}
	response := action()
	registry.entries[compound] = idempotencyEntry{Digest: digest, Response: cloneResponse(response)}
	registry.order = append(registry.order, compound)
	if len(registry.order) > registry.limit {
		oldest := registry.order[0]
		registry.order = registry.order[1:]
		delete(registry.entries, oldest)
	}
	return response
}

func cloneResponse(response cachedResponse) cachedResponse {
	return cachedResponse{Status: response.Status, Header: response.Header.Clone(), Body: append([]byte(nil), response.Body...)}
}

// UnavailableBackend is the explicit zero-grant proxy state.
type UnavailableBackend struct{}

func (UnavailableBackend) LocateRuntime(context.Context) (RuntimeAccess, error) {
	return RuntimeAccess{Status: RuntimeAccessUnavailable, PollAfterMS: 2000}, nil
}
