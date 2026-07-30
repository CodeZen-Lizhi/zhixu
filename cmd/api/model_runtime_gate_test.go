package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

func TestAPIProducerGateRejectsMutationsDuringDrainAndResumes(t *testing.T) {
	gate := newAPIProducerGate()
	served := 0
	handler := gate.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		served++
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := func(method string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/workflows", nil))
		return response
	}
	if response := request(http.MethodPost); response.Code != http.StatusNoContent {
		t.Fatalf("initial status=%d", response.Code)
	}
	hooks := gate.Hooks()
	if err := hooks.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if quiesced, err := hooks.IsQuiesced(context.Background()); err != nil || !quiesced {
		t.Fatalf("quiesced=%t err=%v", quiesced, err)
	}
	if response := request(http.MethodGet); response.Code != http.StatusNoContent {
		t.Fatalf("read status=%d", response.Code)
	}
	response := request(http.MethodPost)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(response.Body.String(), "MODEL_SETTINGS_ENQUEUE_PAUSED") {
		t.Fatalf("blocked status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if served != 2 {
		t.Fatalf("downstream calls=%d", served)
	}
	if err := hooks.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodDelete); response.Code != http.StatusNoContent {
		t.Fatalf("resumed status=%d", response.Code)
	}
}

func TestStartAPIModelRuntimeDoesNotActivateBeforeControllerSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := make(chan struct{})
	release := make(chan struct{})
	runStarted := make(chan struct{})
	controller := &fakeAPIModelRuntimeController{active: active, release: release, runStarted: runStarted}
	errorsChannel := startAPIModelRuntime(ctx, controller)
	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("runtime watcher did not start")
	}
	select {
	case <-active:
		t.Fatal("runtime activated before controller registration")
	default:
	}
	select {
	case err := <-errorsChannel:
		t.Fatalf("runtime watcher stopped before activation: %v", err)
	default:
	}
	close(active)
	select {
	case <-active:
	case err := <-errorsChannel:
		t.Fatalf("activation failed: %v", err)
	}
	close(release)
}

func TestStartAPIModelRuntimeReportsFailureBeforeActivation(t *testing.T) {
	want := errors.New("registration failed")
	controller := &fakeAPIModelRuntimeController{active: make(chan struct{}), runErr: want}
	if err := <-startAPIModelRuntime(context.Background(), controller); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

func TestStartAPIModelRuntimeReportsOwnershipLossAfterActivation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := make(chan struct{})
	close(active)
	runResult := make(chan error, 1)
	controller := &fakeAPIModelRuntimeController{active: active, runResult: runResult}
	errorsChannel := startAPIModelRuntime(ctx, controller)
	ownershipLost := foundation.NewError(
		foundation.ErrorVersionConflict,
		modelsettingsdomain.ErrorCodeRuntimeConflict,
		false,
		errors.New("runtime ownership was replaced"),
	)
	runResult <- ownershipLost
	select {
	case err := <-errorsChannel:
		if !errors.Is(err, ownershipLost) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime ownership loss did not terminate the API watcher path")
	}
}

func TestAPIMainWaitsForManagedRuntimeBeforeListenAndServeAndExitsOnRuntimeErrors(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	mainBody := findFunctionBody(file, "main")
	if mainBody == nil {
		t.Fatal("main function was not found")
	}

	var startPosition token.Pos
	var activeWaitPosition token.Pos
	var listenAndServePosition token.Pos
	var runtimeErrorClauses []*ast.CommClause
	var managedGuard *ast.IfStmt
	ast.Inspect(mainBody, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			if identifier, ok := typed.Fun.(*ast.Ident); ok && identifier.Name == "startAPIModelRuntime" {
				startPosition = typed.Pos()
			}
			if selector, ok := typed.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "ListenAndServe" {
				listenAndServePosition = typed.Pos()
			}
		case *ast.UnaryExpr:
			if typed.Op == token.ARROW && receivesControllerActive(typed) {
				activeWaitPosition = typed.Pos()
			}
		case *ast.SelectStmt:
			for _, statement := range typed.Body.List {
				clause, ok := statement.(*ast.CommClause)
				if ok && receivesIdentifier(clause.Comm, "modelRuntimeErr") {
					runtimeErrorClauses = append(runtimeErrorClauses, clause)
				}
			}
		case *ast.IfStmt:
			if mentionsIdentifier(typed.Cond, "modelRuntimeController") {
				managedGuard = typed
			}
		}
		return true
	})

	if startPosition == token.NoPos || activeWaitPosition == token.NoPos || listenAndServePosition == token.NoPos {
		t.Fatalf("startup positions start=%d active=%d serve=%d", startPosition, activeWaitPosition, listenAndServePosition)
	}
	if !(startPosition < activeWaitPosition && activeWaitPosition < listenAndServePosition) {
		t.Fatalf("managed runtime gate order start=%d active=%d serve=%d", startPosition, activeWaitPosition, listenAndServePosition)
	}
	if managedGuard == nil || !(managedGuard.Body.Pos() < startPosition && activeWaitPosition < managedGuard.Body.End()) ||
		managedGuard.End() >= listenAndServePosition {
		t.Fatal("managed runtime activation wait does not guard the real HTTP serve startup path")
	}

	var beforeServeExit bool
	var afterServeExit bool
	for _, clause := range runtimeErrorClauses {
		if !containsExitOne(clause) {
			t.Fatalf("runtime error branch at %d does not terminate the API process", clause.Pos())
		}
		if clause.Pos() < listenAndServePosition {
			beforeServeExit = true
		} else {
			afterServeExit = true
		}
	}
	if !beforeServeExit || !afterServeExit {
		t.Fatalf("runtime error exits before_serve=%t after_serve=%t clauses=%d", beforeServeExit, afterServeExit, len(runtimeErrorClauses))
	}
}

type fakeAPIModelRuntimeController struct {
	active     chan struct{}
	release    chan struct{}
	runStarted chan struct{}
	runResult  <-chan error
	runErr     error
}

func (controller *fakeAPIModelRuntimeController) Active() <-chan struct{} { return controller.active }

func (controller *fakeAPIModelRuntimeController) Run(ctx context.Context) error {
	if controller.runStarted != nil {
		close(controller.runStarted)
	}
	if controller.runErr != nil {
		return controller.runErr
	}
	if controller.runResult != nil {
		select {
		case err := <-controller.runResult:
			return err
		case <-ctx.Done():
			return nil
		}
	}
	if controller.release != nil {
		select {
		case <-controller.release:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

func findFunctionBody(file *ast.File, name string) *ast.BlockStmt {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function.Body
		}
	}
	return nil
}

func receivesControllerActive(expression *ast.UnaryExpr) bool {
	call, ok := expression.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Active" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == "modelRuntimeController"
}

func receivesIdentifier(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		expression, ok := current.(*ast.UnaryExpr)
		if !ok || expression.Op != token.ARROW {
			return true
		}
		identifier, ok := expression.X.(*ast.Ident)
		if ok && identifier.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func mentionsIdentifier(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		identifier, ok := current.(*ast.Ident)
		if ok && identifier.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func containsExitOne(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		call, ok := current.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Exit" {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		status, statusOK := call.Args[0].(*ast.BasicLit)
		if ok && packageName.Name == "os" && statusOK && status.Kind == token.INT && status.Value == "1" {
			found = true
			return false
		}
		return true
	})
	return found
}
