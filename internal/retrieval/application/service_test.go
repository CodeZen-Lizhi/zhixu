package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestRegisterEmbeddingNormalizesAndOwnsIdentity(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	service := newTestService(t, store)

	_, err := service.RegisterEmbedding(context.Background(), RegisterEmbeddingRequest{
		Provider: " openai ", AdapterName: " compatible ", AdapterVersion: " v1 ", Model: " text-embedding ",
		Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		ConfigHash: " ABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCD ",
	})
	if err != nil {
		t.Fatalf("RegisterEmbedding() error = %v", err)
	}
	if store.registered.ID != testIDs[0] || store.registered.Provider != "openai" || store.registered.ConfigHash != "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Fatalf("registered = %#v", store.registered)
	}
	if !store.registered.CreatedAt.Equal(testNow) {
		t.Fatalf("CreatedAt = %v", store.registered.CreatedAt)
	}
}

func TestRegisterEmbeddingRejectsInvalidBeforeStore(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	service := newTestService(t, store)
	_, err := service.RegisterEmbedding(context.Background(), RegisterEmbeddingRequest{Dimensions: -1})
	if err == nil || store.registerCalls != 0 {
		t.Fatalf("error = %v, calls = %d", err, store.registerCalls)
	}
}

func TestBeginIndexFreezesCanonicalManifest(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	service := newTestService(t, store)

	result, err := service.BeginIndex(context.Background(), BeginIndexRequest{
		WorkspaceID: testWorkspaceID, TokenizerID: " simple ", TokenizerVersion: " v1 ",
		TokenizerConfigHash: hashA, FusionConfig: json.RawMessage(` { "rrf_k": 60 } `),
		SourceSnapshotRef: " source-version:1 ", IdempotencyKey: " build-1 ",
		Manifest: []ManifestChunkInput{{ChunkID: testChunkID, ContentHash: hashB, Sequence: 0, ParserVersion: " p1 ", ChunkStrategyVersion: " c1 ", SchemaVersion: " s1 "}},
	})
	if err != nil {
		t.Fatalf("BeginIndex() error = %v", err)
	}
	build := store.begun
	if build.IndexVersion.ID != testIDs[0] || build.IndexVersion.ManifestHash == "" || build.IndexVersion.ExpectedChunkCount != 1 {
		t.Fatalf("index = %#v", build.IndexVersion)
	}
	if string(build.IndexVersion.FusionConfig) != `{"rrf_k":60}` || len(build.Manifest) != 1 || build.Manifest[0].IndexVersionID != testIDs[0] {
		t.Fatalf("build = %#v", build)
	}
	if len(result.Manifest) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestBeginIndexRequiresOneCanonicalJSONObject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value json.RawMessage
	}{
		{name: "trailing token", value: json.RawMessage(`{}junk`)},
		{name: "second value", value: json.RawMessage(`{} {}`)},
		{name: "array", value: json.RawMessage(`[]`)},
		{name: "scalar", value: json.RawMessage(`42`)},
		{name: "null", value: json.RawMessage(`null`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			service := newTestService(t, store)
			_, err := service.BeginIndex(context.Background(), BeginIndexRequest{
				WorkspaceID: testWorkspaceID, TokenizerID: "simple", TokenizerVersion: "v1", TokenizerConfigHash: hashA,
				FusionConfig: test.value, SourceSnapshotRef: "source:1", IdempotencyKey: "build-1",
			})
			if err == nil || store.begun.IndexVersion.ID != "" {
				t.Fatalf("BeginIndex() error = %v, store build = %#v", err, store.begun)
			}
		})
	}
}

func TestBeginIndexCanonicalizesFusionObjectKeyOrder(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	service := newTestService(t, store)
	_, err := service.BeginIndex(context.Background(), BeginIndexRequest{
		WorkspaceID: testWorkspaceID, TokenizerID: "simple", TokenizerVersion: "v1", TokenizerConfigHash: hashA,
		FusionConfig: json.RawMessage(`{"z":1,"a":{"y":2,"x":3}}`), SourceSnapshotRef: "source:1", IdempotencyKey: "build-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(store.begun.IndexVersion.FusionConfig); got != `{"a":{"x":3,"y":2},"z":1}` {
		t.Fatalf("FusionConfig = %s", got)
	}
}

func TestSaveVectorBatchLoadsBindingAndValidatesBeforeWrite(t *testing.T) {
	t.Parallel()
	embeddingID := foundation.ID("30000000-0000-4000-8000-000000000001")
	index := validIndex(&embeddingID)
	store := &fakeStore{index: index, embedding: validEmbedding(embeddingID)}
	service := newTestService(t, store)

	_, err := service.SaveVectorBatch(context.Background(), SaveVectorBatchRequest{
		WorkspaceID: testWorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: 1,
		Projections: []VectorProjectionInput{{ChunkID: testChunkID, Embedding: []float32{1, 0, 0}, TokenCount: 2, VectorStatus: domain.VectorStatusReady}},
	})
	if err != nil {
		t.Fatalf("SaveVectorBatch() error = %v", err)
	}
	if store.saved.EmbeddingVersionID != embeddingID || !store.saved.At.Equal(testNow) {
		t.Fatalf("batch = %#v", store.saved)
	}

	store.saved = domain.VectorProjectionBatch{}
	_, err = service.SaveVectorBatch(context.Background(), SaveVectorBatchRequest{
		WorkspaceID: testWorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: 1,
		Projections: []VectorProjectionInput{{ChunkID: testChunkID, Embedding: []float32{0, 0, 0}, TokenCount: 2, VectorStatus: domain.VectorStatusReady}},
	})
	if err == nil || len(store.saved.Projections) != 0 {
		t.Fatalf("invalid vector error = %v, saved = %#v", err, store.saved)
	}
}

func TestReadyProvesBuildBeforeTransition(t *testing.T) {
	t.Parallel()
	index := validIndex(nil)
	store := &fakeStore{index: index, status: readyStatus(index)}
	service := newTestService(t, store)

	_, err := service.Ready(context.Background(), TransitionRequest{WorkspaceID: testWorkspaceID, IndexVersionID: index.ID, ExpectedVersion: 1})
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if store.transition.Status != domain.IndexStatusReady || !store.transition.At.Equal(testNow) {
		t.Fatalf("transition = %#v", store.transition)
	}

	store.status.LexicalReadyCount = 0
	store.transition = domain.IndexTransition{}
	_, err = service.Ready(context.Background(), TransitionRequest{WorkspaceID: testWorkspaceID, IndexVersionID: index.ID, ExpectedVersion: 1})
	if err == nil || store.transition.IndexVersionID != "" {
		t.Fatalf("incomplete error = %v, transition = %#v", err, store.transition)
	}
}

func TestBuildLexicalAndFailValidateCurrentIndex(t *testing.T) {
	t.Parallel()
	index := validIndex(nil)
	store := &fakeStore{index: index}
	service := newTestService(t, store)
	request := TransitionRequest{WorkspaceID: testWorkspaceID, IndexVersionID: index.ID, ExpectedVersion: 1}

	if _, err := service.BuildLexical(context.Background(), request); err != nil {
		t.Fatalf("BuildLexical() error = %v", err)
	}
	if store.lexical.IndexVersionID != index.ID || !store.lexical.At.Equal(testNow) {
		t.Fatalf("lexical = %#v", store.lexical)
	}
	if _, err := service.Fail(context.Background(), FailRequest{TransitionRequest: request, FailureCode: " VECTOR_PROVIDER_FAILED "}); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if store.transition.Status != domain.IndexStatusFailed || store.transition.FailureCode != "VECTOR_PROVIDER_FAILED" {
		t.Fatalf("transition = %#v", store.transition)
	}
}

func TestReadyDerivesVectorDegradationFromBuildFacts(t *testing.T) {
	t.Parallel()
	embeddingID := foundation.ID("30000000-0000-4000-8000-000000000001")
	tests := []struct {
		name         string
		initial      []domain.DegradedCapability
		status       domain.BuildStatus
		wantDegraded bool
		wantError    bool
	}{
		{
			name:         "failed vector adds degradation",
			status:       domain.BuildStatus{ProjectionCount: 1, LexicalReadyCount: 1, VectorFailedCount: 1},
			wantDegraded: true,
		},
		{
			name:    "all vectors ready clear predeclared degradation",
			initial: []domain.DegradedCapability{domain.DegradedVector},
			status:  domain.BuildStatus{ProjectionCount: 1, LexicalReadyCount: 1, VectorReadyCount: 1},
		},
		{
			name:      "pending vector is rejected",
			status:    domain.BuildStatus{ProjectionCount: 1, LexicalReadyCount: 1, VectorPendingCount: 1},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := validIndex(&embeddingID)
			index.DegradedCapabilities = test.initial
			status := test.status
			status.WorkspaceID = index.WorkspaceID
			status.IndexVersionID = index.ID
			status.IndexStatus = index.Status
			status.IndexVersion = index.Version
			status.ExpectedChunkCount = 1
			status.ManifestChunkCount = 1
			store := &fakeStore{index: index, status: status}
			service := newTestService(t, store)

			_, err := service.Ready(context.Background(), TransitionRequest{WorkspaceID: testWorkspaceID, IndexVersionID: index.ID, ExpectedVersion: 1})
			if (err != nil) != test.wantError {
				t.Fatalf("Ready() error = %v, wantError = %t", err, test.wantError)
			}
			if test.wantError {
				if store.transition.IndexVersionID != "" {
					t.Fatalf("transition = %#v", store.transition)
				}
				return
			}
			gotDegraded := domain.HasDegradedCapability(store.transition.DegradedCapabilities, domain.DegradedVector)
			if gotDegraded != test.wantDegraded {
				t.Fatalf("degraded = %#v", store.transition.DegradedCapabilities)
			}
		})
	}
}

func TestActivateAndRollbackOwnActivationIdentity(t *testing.T) {
	t.Parallel()
	current := validIndex(nil)
	current.ID = "20000000-0000-4000-8000-000000000009"
	current.Status = domain.IndexStatusActive
	target := validIndex(nil)
	target.Status = domain.IndexStatusReady
	store := &fakeStore{index: target, active: current, hasActive: true}
	service := newTestService(t, store)

	_, err := service.Activate(context.Background(), ActivateRequest{
		WorkspaceID: testWorkspaceID, TargetIndexVersionID: target.ID, ExpectedTargetVersion: 1,
		ExpectedCurrentIndexVersionID: &current.ID, ExpectedCurrentVersion: ptrInt64(1), IdempotencyKey: " activate-1 ", ReasonCode: " initial_build ",
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if store.activation.ActivationID != testIDs[0] || store.activation.IdempotencyKey != "activate-1" {
		t.Fatalf("activation = %#v", store.activation)
	}

	store.index = current
	store.index.Status = domain.IndexStatusRetiring
	store.active = target
	store.active.Status = domain.IndexStatusActive
	_, err = service.RollbackActivate(context.Background(), RollbackActivateRequest{
		WorkspaceID: testWorkspaceID, TargetIndexVersionID: current.ID, ExpectedTargetVersion: 1,
		ExpectedCurrentIndexVersionID: target.ID, ExpectedCurrentVersion: 1, IdempotencyKey: " rollback-1 ", ReasonCode: " regression ",
	})
	if err != nil {
		t.Fatalf("RollbackActivate() error = %v", err)
	}
	if store.rollback.ActivationID != testIDs[1] {
		t.Fatalf("rollback = %#v", store.rollback)
	}
}

func TestActivateAllowsExplicitInitialActivationOnlyWhenNoActiveExists(t *testing.T) {
	t.Parallel()
	target := validIndex(nil)
	target.Status = domain.IndexStatusReady
	store := &fakeStore{index: target}
	service := newTestService(t, store)

	_, err := service.Activate(context.Background(), ActivateRequest{
		WorkspaceID:           testWorkspaceID,
		TargetIndexVersionID:  target.ID,
		ExpectedTargetVersion: 1,
		IdempotencyKey:        "initial-activation",
		ReasonCode:            "first_ready_index",
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if store.activation.ExpectedCurrentIndexVersionID != nil || store.activation.ExpectedCurrentVersion != nil {
		t.Fatalf("activation = %#v", store.activation)
	}
}

func TestQueryMethodsDelegateExactScope(t *testing.T) {
	t.Parallel()
	index := validIndex(nil)
	store := &fakeStore{index: index, status: readyStatus(index), active: index, hasActive: true}
	service := newTestService(t, store)
	ctx := context.Background()
	if _, err := service.GetIndex(ctx, testWorkspaceID, index.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetIndexByIdempotencyKey(ctx, testWorkspaceID, " build-1 "); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetBuildStatus(ctx, testWorkspaceID, index.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetActive(ctx, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if store.lookupKey != "build-1" {
		t.Fatalf("lookup key = %q", store.lookupKey)
	}
}

type fakeStore struct {
	registered    domain.EmbeddingVersion
	registerCalls int
	begun         domain.IndexBuild
	index         domain.IndexVersion
	embedding     domain.EmbeddingVersion
	saved         domain.VectorProjectionBatch
	lexical       domain.LexicalBuildCommand
	transition    domain.IndexTransition
	status        domain.BuildStatus
	active        domain.IndexVersion
	hasActive     bool
	activation    domain.ActivationCommand
	rollback      domain.RollbackActivationCommand
	lookupKey     string
}

func (f *fakeStore) RegisterEmbeddingVersion(_ context.Context, value domain.EmbeddingVersion) (domain.EmbeddingVersionResult, error) {
	f.registerCalls++
	f.registered = value
	return domain.EmbeddingVersionResult{EmbeddingVersion: value, Created: true}, nil
}
func (f *fakeStore) BeginIndex(_ context.Context, value domain.IndexBuild) (domain.IndexVersionResult, error) {
	f.begun = value
	return domain.IndexVersionResult{IndexVersion: value.IndexVersion, Manifest: value.Manifest, Created: true}, nil
}
func (f *fakeStore) BuildLexical(_ context.Context, value domain.LexicalBuildCommand) (domain.ProjectionBatchResult, error) {
	f.lexical = value
	return domain.ProjectionBatchResult{IndexVersionID: value.IndexVersionID}, nil
}
func (f *fakeStore) SaveVectorBatch(_ context.Context, value domain.VectorProjectionBatch) (domain.ProjectionBatchResult, error) {
	f.saved = value
	return domain.ProjectionBatchResult{IndexVersionID: value.IndexVersionID}, nil
}
func (f *fakeStore) TransitionIndex(_ context.Context, value domain.IndexTransition) (domain.IndexVersion, error) {
	f.transition = value
	return f.index, nil
}
func (f *fakeStore) Activate(_ context.Context, value domain.ActivationCommand) (domain.ActivationResult, error) {
	f.activation = value
	return domain.ActivationResult{}, nil
}
func (f *fakeStore) RollbackActivate(_ context.Context, value domain.RollbackActivationCommand) (domain.ActivationResult, error) {
	f.rollback = value
	return domain.ActivationResult{}, nil
}
func (f *fakeStore) GetEmbeddingVersion(context.Context, foundation.ID) (domain.EmbeddingVersion, error) {
	if f.embedding.ID == "" {
		return domain.EmbeddingVersion{}, errors.New("missing")
	}
	return f.embedding, nil
}
func (f *fakeStore) GetIndex(_ context.Context, _, _ foundation.ID) (domain.IndexVersion, error) {
	return f.index, nil
}
func (f *fakeStore) GetIndexByIdempotencyKey(_ context.Context, _ foundation.ID, key string) (domain.IndexVersion, error) {
	f.lookupKey = key
	return f.index, nil
}
func (f *fakeStore) GetBuildStatus(context.Context, foundation.ID, foundation.ID) (domain.BuildStatus, error) {
	return f.status, nil
}
func (f *fakeStore) GetActive(context.Context, foundation.ID) (domain.IndexVersion, error) {
	if !f.hasActive {
		return domain.IndexVersion{}, foundation.NewError(foundation.ErrorNotFound, "RETRIEVAL_ACTIVE_NOT_FOUND", false, errors.New("missing"))
	}
	return f.active, nil
}

type sequenceIDs struct {
	values []foundation.ID
	next   int
}

func (s *sequenceIDs) New() (foundation.ID, error) {
	value := s.values[s.next]
	s.next++
	return value, nil
}

var (
	testNow                       = time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	testWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000001"
	testChunkID     foundation.ID = "40000000-0000-4000-8000-000000000001"
	testIDs                       = []foundation.ID{"20000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"}
	hashA                         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB                         = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newTestService(t *testing.T, store Store) *Service {
	t.Helper()
	service, err := NewService(Dependencies{Store: store, IDs: &sequenceIDs{values: testIDs}, Clock: foundation.FixedClock{Value: testNow}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func validEmbedding(id foundation.ID) domain.EmbeddingVersion {
	return domain.EmbeddingVersion{ID: id, Provider: "openai", AdapterName: "compatible", AdapterVersion: "v1", Model: "embed", Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine, ConfigHash: hashA, CreatedAt: testNow}
}
func validIndex(embeddingID *foundation.ID) domain.IndexVersion {
	degraded := []domain.DegradedCapability(nil)
	if embeddingID == nil {
		degraded = []domain.DegradedCapability{domain.DegradedVector}
	}
	return domain.IndexVersion{ID: testIDs[0], WorkspaceID: testWorkspaceID, EmbeddingVersionID: embeddingID, TokenizerID: "simple", TokenizerVersion: "v1", TokenizerConfigHash: hashA, FusionConfig: json.RawMessage(`{"rrf_k":60}`), SourceSnapshotRef: "source:1", ManifestHash: hashB, ExpectedChunkCount: 1, IdempotencyKey: "build-1", Status: domain.IndexStatusBuilding, DegradedCapabilities: degraded, Version: 1, CreatedAt: testNow, UpdatedAt: testNow}
}
func readyStatus(index domain.IndexVersion) domain.BuildStatus {
	return domain.BuildStatus{WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, IndexStatus: index.Status, IndexVersion: index.Version, ExpectedChunkCount: 1, ManifestChunkCount: 1, ProjectionCount: 1, LexicalReadyCount: 1, VectorDisabledCount: 1, DegradedCapabilities: index.DegradedCapabilities}
}
func ptrInt64(value int64) *int64 { return &value }
