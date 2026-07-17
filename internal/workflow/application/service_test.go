package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type fakeRepository struct {
	start                domain.StartRequest
	claimNow, claimUntil time.Time
	submittedVersion     int64
	err                  error
}

func (f *fakeRepository) Start(_ context.Context, r domain.StartRequest) (domain.Run, error) {
	f.start = r
	return r.Run, f.err
}
func (f *fakeRepository) GetRun(context.Context, foundation.ID) (domain.Run, error) {
	return domain.Run{}, f.err
}
func (f *fakeRepository) ClaimNode(_ context.Context, _ foundation.ID, _ string, n, u time.Time) (domain.NodeRun, error) {
	f.claimNow = n
	f.claimUntil = u
	return domain.NodeRun{}, f.err
}
func (f *fakeRepository) HeartbeatNode(context.Context, foundation.ID, string, time.Time, time.Time) (domain.NodeRun, error) {
	return domain.NodeRun{}, f.err
}
func (f *fakeRepository) CompleteNode(context.Context, domain.Completion) (domain.NodeRun, error) {
	return domain.NodeRun{}, f.err
}
func (f *fakeRepository) CreateHumanTask(_ context.Context, t domain.HumanTask, _ domain.OutboxEvent, _ time.Time) (domain.HumanTask, error) {
	return t, f.err
}
func (f *fakeRepository) SubmitHumanTask(_ context.Context, _ foundation.ID, v int64, _ json.RawMessage, _ time.Time, _ domain.OutboxEvent) (domain.HumanTask, error) {
	f.submittedVersion = v
	return domain.HumanTask{}, f.err
}

type fakeRuntimeStarter struct {
	request             RuntimeStartRequest
	result              RuntimeStartResult
	err                 error
	calls               int
	duplicateWithoutRun bool
}

func (f *fakeRuntimeStarter) Start(_ context.Context, request RuntimeStartRequest) (RuntimeStartResult, error) {
	f.calls++
	f.request = request
	if f.result.Run.ID == "" {
		f.result = RuntimeStartResult{Run: request.Run, FirstNode: request.FirstNode, Job: JobReceipt{JobID: 42, Duplicate: f.duplicateWithoutRun}}
	}
	return f.result, f.err
}

type sequenceIDs struct {
	values []foundation.ID
	index  int
}

func (s *sequenceIDs) New() (foundation.ID, error) {
	if s.index >= len(s.values) {
		return "", errors.New("exhausted")
	}
	v := s.values[s.index]
	s.index++
	return v, nil
}
func id(n byte) foundation.ID {
	return foundation.ID("00000000-0000-4000-8000-00000000000" + string([]byte{'0' + n}))
}

func TestStartResolvesRegisteredDefinitionAndBuildsRuntimeIdentity(t *testing.T) {
	service, runtime, definition := newRuntimeStartService(t, definitionFixture("ingest", []domain.NodeDefinition{
		{Key: "finish", Kind: "deterministic.test", Dependencies: []string{"scan"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
		{Key: "scan", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
	}))

	run, err := service.Start(context.Background(), StartCommand{
		WorkspaceID: id(5), DefinitionKey: " ingest ", DefinitionVersion: 1,
		Input: json.RawMessage(`{"source":"a"}`), IdempotencyKey: " request-1 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := runtime.request
	if runtime.calls != 1 || run.ID != request.Run.ID || request.Definition.Key != "ingest" || request.DefinitionGraphHash != definition.GraphHash || request.DefinitionInputSchemaVersion != 1 {
		t.Fatalf("run=%#v request=%#v calls=%d", run, request, runtime.calls)
	}
	if request.Run.IdempotencyKey != "request-1" || request.Run.RequestHash == "" || request.Run.DefinitionID != request.Definition.ID {
		t.Fatalf("run identity=%#v", request.Run)
	}
	if request.FirstNode.NodeKey != "scan" || request.FirstNode.NodeType != "deterministic.test" || request.FirstNode.InputSchemaVersion != 1 || request.FirstNode.OutputSchemaVersion != 1 || request.FirstNode.DispatchNo != 1 || request.FirstNode.IdempotencyKey == "" {
		t.Fatalf("node identity=%#v", request.FirstNode)
	}
	if request.Event.EventKey == "" || request.Event.EventKey == "workflow.run.started:"+string(request.Run.ID) || request.Event.SchemaVersion != 1 || request.Event.EventVersion != 1 || request.Event.RunID == nil || *request.Event.RunID != request.Run.ID {
		t.Fatalf("event identity=%#v", request.Event)
	}
}

func TestStartCanonicalizesInputBeforeHashing(t *testing.T) {
	definition := definitionFixture("ingest", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}})
	left, leftRuntime, _ := newRuntimeStartService(t, definition)
	right, rightRuntime, _ := newRuntimeStartService(t, definition)
	right.ids = &sequenceIDs{values: []foundation.ID{id(6), id(7), id(8), id(9)}}

	if _, err := left.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Input: json.RawMessage("{\n  \"b\": 2, \"a\": 1\n}"), IdempotencyKey: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Input: json.RawMessage(`{"a":1,"b":2}`), IdempotencyKey: "one"}); err != nil {
		t.Fatal(err)
	}
	if string(leftRuntime.request.Run.Input) != `{"a":1,"b":2}` || leftRuntime.request.Run.RequestHash != rightRuntime.request.Run.RequestHash {
		t.Fatalf("left input/hash=%s/%s right=%s/%s", leftRuntime.request.Run.Input, leftRuntime.request.Run.RequestHash, rightRuntime.request.Run.Input, rightRuntime.request.Run.RequestHash)
	}
	if leftRuntime.request.Run.RequestHash != "4f713095c552912de03da30d483499e0f09fa9ee751821e187669f69e5403752" {
		t.Fatalf("request hash=%s", leftRuntime.request.Run.RequestHash)
	}
	if leftRuntime.request.Run.ID == rightRuntime.request.Run.ID || leftRuntime.request.Event.EventKey != rightRuntime.request.Event.EventKey {
		t.Fatalf("left run/event=%s/%s right=%s/%s", leftRuntime.request.Run.ID, leftRuntime.request.Event.EventKey, rightRuntime.request.Run.ID, rightRuntime.request.Event.EventKey)
	}
}

func TestComputeRuntimeStartRequestHashCanonicalizesInput(t *testing.T) {
	service, runtime, definition := newRuntimeStartService(t, definitionFixture("ingest", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}}))
	formatted := json.RawMessage("{\n  \"b\": 2, \"a\": 1\n}")
	hash, err := ComputeRuntimeStartRequestHash(id(5), definition, formatted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: definition.Key, DefinitionVersion: definition.Version, Input: formatted, IdempotencyKey: "request-1"}); err != nil {
		t.Fatal(err)
	}
	if hash != runtime.request.Run.RequestHash {
		t.Fatalf("hash=%s start hash=%s", hash, runtime.request.Run.RequestHash)
	}
	equivalent, err := ComputeRuntimeStartRequestHash(id(5), definition, json.RawMessage(`{"a":1,"b":2}`))
	if err != nil || equivalent != hash {
		t.Fatalf("equivalent hash=%s err=%v, want %s", equivalent, err, hash)
	}
}

func TestBuildRuntimeStartRequestOwnsCanonicalIdentity(t *testing.T) {
	_, _, definition := newRuntimeStartService(t, definitionFixture("ingest", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}}))
	ids := &sequenceIDs{values: []foundation.ID{id(1), id(2), id(3), id(4)}}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	request, err := BuildRuntimeStartRequest(ids, foundation.FixedClock{Value: now}, id(5), " request-1 ", json.RawMessage(`{"b":2,"a":1}`), definition)
	if err != nil {
		t.Fatal(err)
	}
	if request.Definition.ID != id(1) || request.Run.ID != id(2) || request.FirstNode.ID != id(3) || request.Event.ID != id(4) || request.Run.IdempotencyKey != "request-1" {
		t.Fatalf("request identity=%#v", request)
	}
	if string(request.Run.Input) != `{"a":1,"b":2}` || string(request.FirstNode.Input) != string(request.Run.Input) || request.Run.RequestHash == "" || request.RequestHash != request.Run.RequestHash {
		t.Fatalf("request input/hash=%#v", request)
	}
	wantHash, err := ComputeRuntimeStartRequestHash(id(5), definition, json.RawMessage(`{"a":1,"b":2}`))
	if err != nil || request.RequestHash != wantHash {
		t.Fatalf("request hash=%s want=%s err=%v", request.RequestHash, wantHash, err)
	}
	if request.FirstNode.NodeKey != "root" || request.FirstNode.RunID != request.Run.ID || request.Event.RunID == nil || *request.Event.RunID != request.Run.ID || request.Event.EventKey == "" {
		t.Fatalf("request root/event=%#v", request)
	}
}

func TestStartRejectsClientGraphOrFirstNodeThatDoesNotMatchRegistry(t *testing.T) {
	definition := definitionFixture("ingest", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}})
	tests := []struct {
		name    string
		command StartCommand
		code    string
	}{
		{name: "graph", command: StartCommand{Graph: json.RawMessage(`{"nodes":[{"key":"other","kind":"deterministic.test","input_schema_version":1,"output_schema_version":1,"retry_policy":{"max_retries":2,"base_delay":1000000000,"max_delay":60000000000}}]}`)}, code: "WORKFLOW_START_GRAPH_MISMATCH"},
		{name: "first node key", command: StartCommand{FirstNodeKey: "other", FirstNodeType: "deterministic.test"}, code: "WORKFLOW_START_ROOT_MISMATCH"},
		{name: "first node kind", command: StartCommand{FirstNodeKey: "root", FirstNodeType: "side.effect"}, code: "WORKFLOW_START_ROOT_MISMATCH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, runtime, _ := newRuntimeStartService(t, definition)
			command := tc.command
			command.WorkspaceID = id(5)
			command.DefinitionKey = "ingest"
			command.DefinitionVersion = 1
			command.Input = json.RawMessage(`{}`)
			command.IdempotencyKey = "request-1"
			_, err := service.Start(context.Background(), command)
			assertWorkflowErrorCode(t, err, tc.code)
			if runtime.calls != 0 {
				t.Fatalf("runtime starter called %d times", runtime.calls)
			}
		})
	}
}

func TestStartAcceptsCanonicalEquivalentDeprecatedGraphAndRoot(t *testing.T) {
	definition := definitionFixture("ingest", []domain.NodeDefinition{
		{Key: "finish", Kind: "deterministic.test", Dependencies: []string{"root"}, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
		{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
	})
	service, runtime, _ := newRuntimeStartService(t, definition)
	graph := json.RawMessage(`{"nodes":[{"key":"root","kind":"deterministic.test","input_schema_version":1,"output_schema_version":1,"retry_policy":{"max_retries":2,"base_delay":1000000000,"max_delay":60000000000}},{"key":"finish","kind":"deterministic.test","dependencies":["root"],"input_schema_version":1,"output_schema_version":1,"retry_policy":{"max_retries":2,"base_delay":1000000000,"max_delay":60000000000}}]}`)
	_, err := service.Start(context.Background(), StartCommand{
		WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Input: json.RawMessage(`{}`), IdempotencyKey: "request-1",
		Graph: graph, FirstNodeKey: "root", FirstNodeType: "deterministic.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 || runtime.request.FirstNode.NodeKey != "root" {
		t.Fatalf("request=%#v calls=%d", runtime.request, runtime.calls)
	}
}

func TestStartRequiresExactlyOneRegisteredRoot(t *testing.T) {
	service, runtime, _ := newRuntimeStartService(t, definitionFixture("two-roots", []domain.NodeDefinition{
		{Key: "left", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
		{Key: "right", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()},
	}))
	_, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "two-roots", DefinitionVersion: 1, Input: json.RawMessage(`{}`), IdempotencyKey: "request-1"})
	assertWorkflowErrorCode(t, err, "WORKFLOW_DEFINITION_ROOT_NOT_UNIQUE")
	if runtime.calls != 0 {
		t.Fatalf("runtime starter called %d times", runtime.calls)
	}
}

func TestStartPropagatesLegacyActiveRuntimeContract(t *testing.T) {
	service, runtime, _ := newRuntimeStartService(t, definitionFixture("ingest", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}}))
	runtime.err = foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED", false, errors.New("active legacy run has no runtime identity"))
	_, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Input: json.RawMessage(`{}`), IdempotencyKey: "request-1"})
	assertWorkflowErrorCode(t, err, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED")
}

func TestStartRejectsIncompleteRuntimeResult(t *testing.T) {
	service, runtime, _ := newRuntimeStartService(t, definitionFixture("ingest", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}}))
	runtime.result = RuntimeStartResult{Run: domain.Run{ID: id(9), WorkspaceID: id(5)}}
	_, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Input: json.RawMessage(`{}`), IdempotencyKey: "request-1"})
	assertWorkflowErrorCode(t, err, "WORKFLOW_START_RESULT_INVALID")
}

func TestStartRejectsConflictingRuntimeReplayResult(t *testing.T) {
	service, runtime, _ := newRuntimeStartService(t, definitionFixture("ingest", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}}))
	runtime.duplicateWithoutRun = true
	_, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Input: json.RawMessage(`{}`), IdempotencyKey: "request-1"})
	assertWorkflowErrorCode(t, err, "WORKFLOW_START_RESULT_INVALID")
}

func TestNewRuntimeServiceRejectsTypedNilStarter(t *testing.T) {
	var starter *fakeRuntimeStarter
	_, err := NewRuntimeService(&fakeRepository{}, &sequenceIDs{}, foundation.FixedClock{}, RuntimeDependencies{Definitions: &DefinitionRegistry{}, Starter: starter})
	assertWorkflowErrorCode(t, err, "WORKFLOW_RUNTIME_DEPENDENCY_MISSING")
}

func TestStartWithoutRuntimeDependenciesReturnsLegacyUnsupported(t *testing.T) {
	service, err := NewService(&fakeRepository{}, &sequenceIDs{}, foundation.FixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Input: json.RawMessage(`{}`), IdempotencyKey: "request-1"})
	assertWorkflowErrorCode(t, err, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED")
}

func TestStartReturnsRegistryResolutionError(t *testing.T) {
	service, runtime, _ := newRuntimeStartService(t, definitionFixture("registered", []domain.NodeDefinition{{Key: "root", Kind: "deterministic.test", InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: testRetryPolicy()}}))
	_, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "missing", DefinitionVersion: 1, Input: json.RawMessage(`{}`), IdempotencyKey: "request-1"})
	assertWorkflowErrorCode(t, err, "WORKFLOW_DEFINITION_NOT_REGISTERED")
	if runtime.calls != 0 {
		t.Fatalf("runtime starter called %d times", runtime.calls)
	}
}

func TestClaimUsesClockAndPositiveLease(t *testing.T) {
	repo := &fakeRepository{}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	service, _ := NewService(repo, &sequenceIDs{}, foundation.FixedClock{Value: now})
	_, err := service.Claim(context.Background(), id(1), "worker-a", 30*time.Second)
	if err != nil || !repo.claimNow.Equal(now) || !repo.claimUntil.Equal(now.Add(30*time.Second)) {
		t.Fatalf("now=%v until=%v err=%v", repo.claimNow, repo.claimUntil, err)
	}
	if _, err = service.Claim(context.Background(), id(1), "", time.Second); err == nil {
		t.Fatal("empty owner accepted")
	}
}

func TestSubmitRejectsInvalidVersion(t *testing.T) {
	service, _ := NewService(&fakeRepository{}, &sequenceIDs{}, foundation.FixedClock{})
	if _, err := service.SubmitHumanDecision(context.Background(), id(1), 0, json.RawMessage(`{}`), id(2), id(3)); err == nil {
		t.Fatal("invalid version accepted")
	}
}

func newRuntimeStartService(t *testing.T, definition domain.RegisteredDefinition) (*Service, *fakeRuntimeStarter, domain.RegisteredDefinition) {
	t.Helper()
	catalog := testValidationCatalog(t)
	executors := testFrozenExecutors(t, catalog, "deterministic.test")
	definitions, err := NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	resolved, err := definitions.Resolve(definition.Key, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntimeStarter{}
	service, err := NewRuntimeService(
		&fakeRepository{},
		&sequenceIDs{values: []foundation.ID{id(1), id(2), id(3), id(4)}},
		foundation.FixedClock{Value: time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)},
		RuntimeDependencies{Definitions: definitions, Starter: runtime},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, runtime, resolved
}
