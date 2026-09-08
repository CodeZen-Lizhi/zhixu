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
	errorsChannel, stopped := startAPIModelRuntime(ctx, controller)
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
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := waitAPIModelRuntime(shutdownContext, stopped); err != nil {
		t.Fatal(err)
	}
}

func TestStartAPIModelRuntimeReportsFailureBeforeActivation(t *testing.T) {
	want := errors.New("registration failed")
	controller := &fakeAPIModelRuntimeController{active: make(chan struct{}), runErr: want}
	errorsChannel, stopped := startAPIModelRuntime(context.Background(), controller)
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := waitAPIModelRuntime(shutdownContext, stopped); err != nil {
		t.Fatal(err)
	}
	if err := <-errorsChannel; !errors.Is(err, want) {
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
	errorsChannel, stopped := startAPIModelRuntime(ctx, controller)
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
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := waitAPIModelRuntime(shutdownContext, stopped); err != nil {
		t.Fatal(err)
	}
}

func TestStartAPIActivationCoordinatorReportsFatalAndUnexpectedStop(t *testing.T) {
	want := errors.New("activation ownership lost")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errorsChannel, stopped := startAPIActivationCoordinator(ctx, fakeAPIActivationCoordinator{runErr: want})
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := waitAPIModelRuntime(shutdownContext, stopped); err != nil {
		t.Fatal(err)
	}
	if err := <-errorsChannel; !errors.Is(err, want) {
		t.Fatalf("fatal error=%v", err)
	}

	errorsChannel, stopped = startAPIActivationCoordinator(context.Background(), fakeAPIActivationCoordinator{})
	if err := waitAPIModelRuntime(shutdownContext, stopped); err != nil {
		t.Fatal(err)
	}
	if err := <-errorsChannel; err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("unexpected stop error=%v", err)
	}
}

func TestStartAPIActivationCoordinatorIgnoresNormalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanupStarted := make(chan struct{})
	cleanupRelease := make(chan struct{})
	cleanupReleased := false
	defer func() {
		if !cleanupReleased {
			close(cleanupRelease)
		}
	}()
	coordinator := fakeAPIActivationCoordinator{waitForCancellation: true, onReturn: func() {
		close(cleanupStarted)
		<-cleanupRelease
	}}
	coordinatorErrors, coordinatorStopped := startAPIActivationCoordinator(ctx, coordinator)
	controllerErrors, controllerStopped := startAPIModelRuntime(ctx, &fakeAPIModelRuntimeController{active: make(chan struct{})})
	cancel()
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not begin cleanup after cancellation")
	}
	expiredContext, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	if err := waitAPIModelRuntime(expiredContext, nil, controllerStopped, coordinatorStopped); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked cleanup did not preserve the shutdown deadline: %v", err)
	}
	select {
	case <-coordinatorStopped:
		t.Fatal("coordinator completion was reported before cleanup finished")
	default:
	}
	close(cleanupRelease)
	cleanupReleased = true
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := waitAPIModelRuntime(shutdownContext, controllerStopped, coordinatorStopped); err != nil {
		t.Fatal(err)
	}
	if err := waitAPIModelRuntime(expiredContext, nil, controllerStopped, coordinatorStopped); err != nil {
		t.Fatalf("completed runtimes should not wait for another shutdown budget: %v", err)
	}
	for _, failures := range []<-chan error{controllerErrors, coordinatorErrors} {
		select {
		case err := <-failures:
			t.Fatalf("normal cancellation reported as fatal: %v", err)
		default:
		}
	}
}

func TestAPIRunWaitsForManagedRuntimeBeforeListenAndServeAndReturnsOnRuntimeErrors(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	runBody := findFunctionBody(file, "runAPI")
	if runBody == nil {
		t.Fatal("runAPI function was not found")
	}

	var startPosition token.Pos
	var coordinatorStartPosition token.Pos
	var activeWaitPosition token.Pos
	var listenAndServePosition token.Pos
	var shutdownPosition token.Pos
	var runtimeErrorClauses []*ast.CommClause
	var managedGuard *ast.IfStmt
	ast.Inspect(runBody, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.DeferStmt:
			// Cleanup drains late errors after consumers stop; it is not a startup branch.
			return false
		case *ast.CallExpr:
			if identifier, ok := typed.Fun.(*ast.Ident); ok && identifier.Name == "startAPIModelRuntime" {
				startPosition = typed.Pos()
			}
			if identifier, ok := typed.Fun.(*ast.Ident); ok && identifier.Name == "startAPIActivationCoordinator" {
				coordinatorStartPosition = typed.Pos()
			}
			if selector, ok := typed.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "ListenAndServe" {
				listenAndServePosition = typed.Pos()
			}
			if selector, ok := typed.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Shutdown" {
				shutdownPosition = typed.Pos()
			}
		case *ast.UnaryExpr:
			if typed.Op == token.ARROW && receivesControllerActive(typed) {
				activeWaitPosition = typed.Pos()
			}
		case *ast.SelectStmt:
			for _, statement := range typed.Body.List {
				clause, ok := statement.(*ast.CommClause)
				if ok && (receivesIdentifier(clause.Comm, "modelRuntimeErr") ||
					receivesIdentifier(clause.Comm, "activationCoordinatorErr") || receivesIdentifier(clause.Comm, "workspaceRuntimeErr")) {
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

	if startPosition == token.NoPos || coordinatorStartPosition == token.NoPos || activeWaitPosition == token.NoPos || listenAndServePosition == token.NoPos || shutdownPosition == token.NoPos {
		t.Fatalf("startup positions start=%d active=%d coordinator=%d serve=%d shutdown=%d", startPosition, activeWaitPosition, coordinatorStartPosition, listenAndServePosition, shutdownPosition)
	}
	if !(startPosition < activeWaitPosition && activeWaitPosition < coordinatorStartPosition && coordinatorStartPosition < listenAndServePosition) {
		t.Fatalf("managed runtime gate order start=%d active=%d coordinator=%d serve=%d", startPosition, activeWaitPosition, coordinatorStartPosition, listenAndServePosition)
	}
	if managedGuard == nil || !(managedGuard.Body.Pos() < startPosition && activeWaitPosition < managedGuard.Body.End()) ||
		coordinatorStartPosition >= managedGuard.Body.End() || managedGuard.End() >= listenAndServePosition {
		t.Fatal("managed runtime activation wait does not guard the real HTTP serve startup path")
	}

	var beforeServeExit bool
	var afterServeExit bool
	var activationCoordinatorExit bool
	for _, clause := range runtimeErrorClauses {
		if clause.Pos() < listenAndServePosition {
			if !containsReturnOne(clause) {
				t.Fatalf("pre-serve runtime error branch at %d does not return a failing API exit code", clause.Pos())
			}
			beforeServeExit = true
		} else {
			if !containsAssignmentOne(clause, "exitCode") {
				t.Fatalf("served runtime error branch at %d does not preserve a failing API exit code", clause.Pos())
			}
			if clause.Pos() >= shutdownPosition {
				t.Fatalf("served runtime error branch at %d bypasses shutdown at %d", clause.Pos(), shutdownPosition)
			}
			if receivesIdentifier(clause.Comm, "activationCoordinatorErr") {
				activationCoordinatorExit = true
			}
			afterServeExit = true
		}
	}
	if !beforeServeExit || !afterServeExit || !activationCoordinatorExit {
		t.Fatalf(
			"runtime error exits before_serve=%t after_serve=%t activation_coordinator=%t clauses=%d",
			beforeServeExit,
			afterServeExit,
			activationCoordinatorExit,
			len(runtimeErrorClauses),
		)
	}
}

func TestAPIRunComposesHotModelsRuntimeAndActivationStarter(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	runBody := findFunctionBody(file, "runAPI")
	if runBody == nil {
		t.Fatal("runAPI function was not found")
	}

	calls := map[string]bool{}
	var activationStarterWired bool
	var initialPhaseWired bool
	ast.Inspect(runBody, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			if selector, ok := typed.Fun.(*ast.SelectorExpr); ok {
				calls[selector.Sel.Name] = true
			}
		case *ast.KeyValueExpr:
			key, ok := typed.Key.(*ast.Ident)
			if !ok {
				return true
			}
			value, valueOK := typed.Value.(*ast.Ident)
			if key.Name == "ActivationStarter" && valueOK && value.Name == "modelActivationStarter" {
				activationStarterWired = true
			}
			if key.Name == "InitialPhase" && mentionsIdentifier(typed.Value, "bootstrap") {
				initialPhaseWired = true
			}
		}
		return true
	})

	for _, constructor := range []string{"NewManagedModelsHost", "NewHotRuntimeController", "NewActivationCoordinator"} {
		if !calls[constructor] {
			t.Fatalf("runAPI does not call %s", constructor)
		}
	}
	if !activationStarterWired {
		t.Fatal("runAPI does not pass the durable activation starter to the model settings handler")
	}
	if !initialPhaseWired {
		t.Fatal("runAPI does not preserve the managed bootstrap runtime phase")
	}
}

func containsAssignmentOne(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		assignment, ok := current.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return true
		}
		identifier, identifierOK := assignment.Lhs[0].(*ast.Ident)
		value, valueOK := assignment.Rhs[0].(*ast.BasicLit)
		if identifierOK && identifier.Name == name && valueOK && value.Kind == token.INT && value.Value == "1" {
			found = true
			return false
		}
		return true
	})
	return found
}

type fakeAPIModelRuntimeController struct {
	active     chan struct{}
	release    chan struct{}
	runStarted chan struct{}
	runResult  <-chan error
	runErr     error
}

type fakeAPIActivationCoordinator struct {
	runErr              error
	waitForCancellation bool
	onReturn            func()
}

func (coordinator fakeAPIActivationCoordinator) Run(ctx context.Context) error {
	if coordinator.onReturn != nil {
		defer coordinator.onReturn()
	}
	if coordinator.runErr != nil {
		return coordinator.runErr
	}
	if coordinator.waitForCancellation {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
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
			return ctx.Err()
		}
	}
	if controller.release != nil {
		select {
		case <-controller.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	<-ctx.Done()
	return ctx.Err()
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

func containsReturnOne(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		statement, ok := current.(*ast.ReturnStmt)
		if !ok || len(statement.Results) != 1 {
			return true
		}
		status, statusOK := statement.Results[0].(*ast.BasicLit)
		if statusOK && status.Kind == token.INT && status.Value == "1" {
			found = true
			return false
		}
		return true
	})
	return found
}
