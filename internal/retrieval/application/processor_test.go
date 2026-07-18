package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestProcessorBuildsReadySnapshotAndReturnsLatestCompletionFence(t *testing.T) {
	fixture := newProcessorFixture(t, processorCheckpointNone, domain.IndexStatusBuilding)
	processor := fixture.processor(t)

	result, err := processor.Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != ProcessorReady || result.WorkspaceID != fixture.context.Request.WorkspaceID ||
		result.OutboxEventID != fixture.context.Delivery.OutboxEventID || result.IndexVersionID != fixture.index.ID ||
		result.Fence.DeliveryVersion != fixture.initialFence.DeliveryVersion+4 {
		t.Fatalf("result = %#v", result)
	}
	if fixture.capture.calls != 1 || fixture.ingestion.calls != 1 || fixture.retrieval.snapshotCalls != 1 ||
		fixture.retrieval.lexicalCalls != 1 || fixture.regression.calls != 1 || fixture.retrieval.readyCalls != 1 {
		t.Fatalf("calls capture=%d ingestion=%d snapshot=%d lexical=%d regression=%d ready=%d",
			fixture.capture.calls, fixture.ingestion.calls, fixture.retrieval.snapshotCalls,
			fixture.retrieval.lexicalCalls, fixture.regression.calls, fixture.retrieval.readyCalls)
	}
	if got := fixture.ingestion.request; got.SourceVersionID != fixture.sourceVersionID || got.WorkflowRunID != nil ||
		got.AttemptNumber != int32(fixture.context.Delivery.DispatchNo) ||
		got.IdempotencyKey != "reindex-v1:"+string(fixture.context.Delivery.OutboxEventID)+":ingestion:1" {
		t.Fatalf("ingestion request = %#v", got)
	}
	snapshot := fixture.retrieval.snapshotRequest
	if snapshot.WorkspaceID != fixture.context.Request.WorkspaceID || snapshot.TargetSourceID != fixture.sourceID ||
		snapshot.TargetSourceVersionID != fixture.sourceVersionID || snapshot.TargetParseProjectionID != fixture.projectionID ||
		snapshot.SourceSnapshotRef != "reindex-v1:"+string(fixture.context.Delivery.OutboxEventID) ||
		snapshot.IdempotencyKey != "reindex-v1:"+string(fixture.context.Delivery.OutboxEventID)+":snapshot" ||
		snapshot.ProcessingContract.ParserID != fixture.ingestionAttempt.ParserID ||
		snapshot.ProcessingContract.ChunkStrategyVersion != fixture.ingestionAttempt.ChunkStrategyVersion {
		t.Fatalf("snapshot request = %#v", snapshot)
	}
	wantStages := []domain.DeliveryCheckpointStage{
		domain.DeliveryCheckpointSourceCaptured,
		domain.DeliveryCheckpointIngested,
		domain.DeliveryCheckpointIndexBuilding,
		domain.DeliveryCheckpointRegressionPassed,
	}
	if len(fixture.runtime.checkpoints) != len(wantStages) {
		t.Fatalf("checkpoints = %#v", fixture.runtime.checkpoints)
	}
	for index, stage := range wantStages {
		if fixture.runtime.checkpoints[index].Stage != stage {
			t.Fatalf("checkpoint[%d] = %#v", index, fixture.runtime.checkpoints[index])
		}
	}
}

func TestDefaultFTSOnlyProcessorOptionsMatchLexicalBuilderContract(t *testing.T) {
	options := DefaultFTSOnlyProcessorOptions(3 * time.Second)
	if options.TokenizerID != "postgres-simple" || options.TokenizerVersion != "v1" ||
		options.TokenizerConfigHash != "7c1191591f7bcaac74555b66c0243ef93f8668a60bd373ac7b7078d9b643fad7" ||
		string(options.FusionConfig) != `{"method":"fts_only"}` || options.RetryDelay != 3*time.Second ||
		options.PageSize != 0 || options.MaxSources != 0 || options.MaxChunks != 0 {
		t.Fatalf("default options = %#v", options)
	}
}

func TestProcessorResumesFromEveryPersistedCheckpoint(t *testing.T) {
	for _, test := range []struct {
		name            string
		checkpoint      processorCheckpoint
		indexStatus     domain.IndexStatus
		captureCalls    int
		ingestionCalls  int
		snapshotCalls   int
		lexicalCalls    int
		regressionCalls int
		readyCalls      int
		newCheckpoints  int
	}{
		{name: "source captured", checkpoint: processorCheckpointSource, indexStatus: domain.IndexStatusBuilding,
			captureCalls: 0, ingestionCalls: 1, snapshotCalls: 1, lexicalCalls: 1, regressionCalls: 1, readyCalls: 1, newCheckpoints: 3},
		{name: "ingested", checkpoint: processorCheckpointIngested, indexStatus: domain.IndexStatusBuilding,
			captureCalls: 0, ingestionCalls: 0, snapshotCalls: 1, lexicalCalls: 1, regressionCalls: 1, readyCalls: 1, newCheckpoints: 2},
		{name: "index building", checkpoint: processorCheckpointIndex, indexStatus: domain.IndexStatusBuilding,
			captureCalls: 0, ingestionCalls: 0, snapshotCalls: 0, lexicalCalls: 1, regressionCalls: 1, readyCalls: 1, newCheckpoints: 1},
		{name: "regression passed", checkpoint: processorCheckpointRegression, indexStatus: domain.IndexStatusBuilding,
			captureCalls: 0, ingestionCalls: 0, snapshotCalls: 0, lexicalCalls: 0, regressionCalls: 0, readyCalls: 1, newCheckpoints: 0},
		{name: "ready response loss", checkpoint: processorCheckpointRegression, indexStatus: domain.IndexStatusReady,
			captureCalls: 0, ingestionCalls: 0, snapshotCalls: 0, lexicalCalls: 0, regressionCalls: 0, readyCalls: 0, newCheckpoints: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProcessorFixture(t, test.checkpoint, test.indexStatus)
			result, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
			if err != nil {
				t.Fatal(err)
			}
			if result.Disposition != ProcessorReady || result.IndexVersionID != fixture.index.ID {
				t.Fatalf("result = %#v", result)
			}
			if fixture.capture.calls != test.captureCalls || fixture.ingestion.calls != test.ingestionCalls ||
				fixture.retrieval.snapshotCalls != test.snapshotCalls || fixture.retrieval.lexicalCalls != test.lexicalCalls ||
				fixture.regression.calls != test.regressionCalls || fixture.retrieval.readyCalls != test.readyCalls ||
				len(fixture.runtime.checkpoints) != test.newCheckpoints {
				t.Fatalf("calls capture=%d ingestion=%d snapshot=%d lexical=%d regression=%d ready=%d checkpoints=%d",
					fixture.capture.calls, fixture.ingestion.calls, fixture.retrieval.snapshotCalls,
					fixture.retrieval.lexicalCalls, fixture.regression.calls, fixture.retrieval.readyCalls,
					len(fixture.runtime.checkpoints))
			}
		})
	}
}

func TestProcessorStopsAfterLeaseBecomesStale(t *testing.T) {
	fixture := newProcessorFixture(t, processorCheckpointNone, domain.IndexStatusBuilding)
	fixture.runtime.staleAt = 1
	result, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != ProcessorStale || fixture.capture.calls != 1 || fixture.ingestion.calls != 0 {
		t.Fatalf("result=%#v capture=%d ingestion=%d", result, fixture.capture.calls, fixture.ingestion.calls)
	}
}

func TestProcessorReturnsLoaderStaleAndCommittedWithoutSideEffects(t *testing.T) {
	for _, disposition := range []ProcessorContextDisposition{ProcessorContextStale, ProcessorContextCommitted} {
		t.Run(string(disposition), func(t *testing.T) {
			fixture := newProcessorFixture(t, processorCheckpointNone, domain.IndexStatusBuilding)
			fixture.loader.result = ProcessorContextLoadResult{Disposition: disposition}
			result, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
			if err != nil {
				t.Fatal(err)
			}
			want := ProcessorStale
			if disposition == ProcessorContextCommitted {
				want = ProcessorCommitted
			}
			if result.Disposition != want || fixture.capture.calls != 0 || fixture.ingestion.calls != 0 {
				t.Fatalf("result=%#v capture=%d ingestion=%d", result, fixture.capture.calls, fixture.ingestion.calls)
			}
		})
	}
}

func TestProcessorRejectsCrossBoundContextBeforeSideEffects(t *testing.T) {
	fixture := newProcessorFixture(t, processorCheckpointNone, domain.IndexStatusBuilding)
	fixture.context.Request.WritebackExecutionID = processorID(99)
	fixture.loader.result.Context = fixture.context
	_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
	if processorErrorCode(err) != "REINDEX_PROCESSOR_CONTEXT_INVALID" || fixture.capture.calls != 0 {
		t.Fatalf("error=%v capture=%d", err, fixture.capture.calls)
	}
}

func TestProcessorRejectsReadyIndexWithoutRegressionCheckpoint(t *testing.T) {
	fixture := newProcessorFixture(t, processorCheckpointIndex, domain.IndexStatusReady)
	_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
	if processorErrorCode(err) != "REINDEX_PROCESSOR_INDEX_STATE_INVALID" || fixture.retrieval.readyCalls != 0 {
		t.Fatalf("error=%v ready=%d", err, fixture.retrieval.readyCalls)
	}
}

func TestProcessorReturnsTypedFailureOnlyForProvenBusinessReduction(t *testing.T) {
	t.Run("persisted retryable ingestion", func(t *testing.T) {
		fixture := newProcessorFixture(t, processorCheckpointSource, domain.IndexStatusBuilding)
		failed := fixture.ingestionAttempt
		failed.Status = ingestiondomain.AttemptParseFailed
		failed.ParseProjectionID = nil
		failed.ErrorCode = "PARSER_TEMPORARY_FAILURE"
		failed.Retryable = true
		fixture.ingestion.result = ingestionapplication.ProcessResult{Attempt: failed}
		fixture.ingestion.err = foundation.NewError(foundation.ErrorRetryableFailure, failed.ErrorCode, true, errors.New("parser unavailable"))
		_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
		var failure *DeliveryFailureError
		if !errors.As(err, &failure) || failure.Failure.Class != domain.DeliveryFailureRetryable ||
			failure.Failure.Code != failed.ErrorCode || failure.RetryDelay != time.Second || fixture.retrieval.snapshotCalls != 0 {
			t.Fatalf("failure=%#v err=%v snapshot=%d", failure, err, fixture.retrieval.snapshotCalls)
		}
	})

	t.Run("committed regression failure", func(t *testing.T) {
		fixture := newProcessorFixture(t, processorCheckpointIndex, domain.IndexStatusBuilding)
		fixture.regression.err = domain.SnapshotRegressionFailure("manifest closure failed")
		_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
		var failure *DeliveryFailureError
		if !errors.As(err, &failure) || failure.Failure.Class != domain.DeliveryFailureNonRetryable ||
			failure.Failure.Code != domain.ErrorCodeSnapshotStructureRegressionFailed || failure.RetryDelay != 0 {
			t.Fatalf("failure=%#v err=%v", failure, err)
		}
	})

	t.Run("snapshot transaction unknown", func(t *testing.T) {
		fixture := newProcessorFixture(t, processorCheckpointIngested, domain.IndexStatusBuilding)
		fixture.retrieval.snapshotErr = foundation.NewError(foundation.ErrorDependencyUnavailable, "RETRIEVAL_SNAPSHOT_COMMIT_FAILED", true, errors.New("connection lost"))
		_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
		var failure *DeliveryFailureError
		if errors.As(err, &failure) || processorErrorCode(err) != "RETRIEVAL_SNAPSHOT_COMMIT_FAILED" {
			t.Fatalf("failure=%#v err=%v", failure, err)
		}
	})

	t.Run("snapshot deterministic capacity failure", func(t *testing.T) {
		fixture := newProcessorFixture(t, processorCheckpointIngested, domain.IndexStatusBuilding)
		fixture.retrieval.snapshotErr = foundation.NewError(foundation.ErrorNonRetryableFailure, "REINDEX_SNAPSHOT_CAPACITY_EXCEEDED", false, errors.New("source limit exceeded"))
		_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
		var failure *DeliveryFailureError
		if !errors.As(err, &failure) || failure.Failure.Class != domain.DeliveryFailureNonRetryable ||
			failure.Failure.Code != "REINDEX_SNAPSHOT_CAPACITY_EXCEEDED" || failure.RetryDelay != 0 {
			t.Fatalf("failure=%#v err=%v", failure, err)
		}
	})

	t.Run("lexical deterministic consistency failure", func(t *testing.T) {
		fixture := newProcessorFixture(t, processorCheckpointIndex, domain.IndexStatusBuilding)
		fixture.retrieval.lexicalErr = foundation.NewError(foundation.ErrorConsistencyViolation, "RETRIEVAL_LEXICAL_INCOMPLETE", false, errors.New("manifest closure mismatch"))
		_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
		var failure *DeliveryFailureError
		if !errors.As(err, &failure) || failure.Failure.Class != domain.DeliveryFailureManualRecovery ||
			failure.Failure.Code != "RETRIEVAL_LEXICAL_INCOMPLETE" || failure.RetryDelay != 0 {
			t.Fatalf("failure=%#v err=%v", failure, err)
		}
	})

	t.Run("ready deterministic incomplete failure", func(t *testing.T) {
		fixture := newProcessorFixture(t, processorCheckpointRegression, domain.IndexStatusBuilding)
		fixture.retrieval.readyErr = foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeBuildIncomplete, false, errors.New("build is incomplete"))
		_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
		var failure *DeliveryFailureError
		if !errors.As(err, &failure) || failure.Failure.Class != domain.DeliveryFailureNonRetryable ||
			failure.Failure.Code != domain.ErrorCodeBuildIncomplete || failure.RetryDelay != 0 {
			t.Fatalf("failure=%#v err=%v", failure, err)
		}
	})

	t.Run("checkpoint transaction unknown", func(t *testing.T) {
		fixture := newProcessorFixture(t, processorCheckpointNone, domain.IndexStatusBuilding)
		fixture.runtime.errAt = 1
		fixture.runtime.err = foundation.NewError(foundation.ErrorDependencyUnavailable, "REINDEX_DELIVERY_MUTATION_COMMIT_FAILED", true, errors.New("commit response lost"))
		_, err := fixture.processor(t).Process(context.Background(), ProcessorRequest{Lease: fixture.lease})
		var failure *DeliveryFailureError
		if errors.As(err, &failure) || processorErrorCode(err) != "REINDEX_DELIVERY_MUTATION_COMMIT_FAILED" {
			t.Fatalf("failure=%#v err=%v", failure, err)
		}
	})
}

type processorCheckpoint int

const (
	processorCheckpointNone processorCheckpoint = iota
	processorCheckpointSource
	processorCheckpointIngested
	processorCheckpointIndex
	processorCheckpointRegression
)

type processorFixture struct {
	context          ProcessorContext
	initialFence     domain.DeliveryFence
	sourceID         foundation.ID
	sourceVersionID  foundation.ID
	ingestionAttempt ingestiondomain.AttemptRecord
	projectionID     foundation.ID
	index            domain.IndexVersion
	loader           *processorLoaderFake
	capture          *processorCaptureFake
	ingestion        *processorIngestionFake
	retrieval        *processorRetrievalFake
	regression       *processorRegressionFake
	runtime          *processorLeaseRuntime
	lease            *DeliveryLeaseSession
}

func newProcessorFixture(t *testing.T, checkpoint processorCheckpoint, indexStatus domain.IndexStatus) *processorFixture {
	t.Helper()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	workspaceID, eventID, executionID := processorID(1), processorID(2), processorID(3)
	deliveryID, deliveryAttemptID := processorID(4), processorID(5)
	sourceID, sourceVersionID, projectionID, ingestionAttemptID, indexID :=
		processorID(6), processorID(7), processorID(8), processorID(9), processorID(10)
	request := reindexcontract.RequestV1{
		SchemaVersion: reindexcontract.SchemaVersionV1, WorkspaceID: workspaceID,
		WorkflowRunID: processorID(11), NodeRunID: processorID(12), ProposalID: processorID(13),
		RevisionID: processorID(14), ApprovalID: processorID(15), WritebackExecutionID: executionID,
		TargetPath: "docs/processor.md", ResultHash: processorTestHash("result"), GitCommit: strings.Repeat("a", 40),
	}
	delivery := domain.Delivery{
		ID: deliveryID, ConsumerName: "retrieval-reindex-v1", OutboxEventID: eventID,
		WorkspaceID: workspaceID, WritebackExecutionID: executionID, Status: domain.DeliveryStatusProcessing,
		DispatchNo: 1, AttemptNo: 1, Version: 4, CreatedAt: now, UpdatedAt: now,
	}
	delivery.CurrentAttemptID = processorIDPointer(deliveryAttemptID)
	attempt := domain.DeliveryAttempt{
		ID: deliveryAttemptID, DeliveryID: deliveryID, AttemptNo: 1, DispatchNo: 1,
		RiverJobID: 42, RiverAttempt: 1, DeliveryKey: "processor-delivery", LeaseOwner: "processor-owner",
		LeaseUntil: now.Add(time.Hour), Status: domain.DeliveryAttemptProcessing, StartedAt: now,
		HeartbeatAt: now,
	}
	ingestionAttempt := ingestiondomain.AttemptRecord{
		Attempt: ingestiondomain.Attempt{
			ID: ingestionAttemptID, WorkspaceID: workspaceID, SourceVersionID: sourceVersionID,
			Status: ingestiondomain.AttemptChunked, SecurityStatus: ingestiondomain.SecurityPassed,
			ParserID: "goldmark", ParserVersion: "goldmark-1.8.4", ParserConfigHash: processorTestHash("parser"),
			ChunkStrategyVersion: "structure-v1", SchemaVersion: "parse-v1",
			IdempotencyKey: "reindex-v1:" + string(eventID) + ":ingestion:1", AttemptNumber: 1,
		},
		ParseProjectionID: processorIDPointer(projectionID), StartedAt: now, CompletedAt: processorTimePointer(now), Version: 5,
	}
	expectedSources := int64(1)
	index := domain.IndexVersion{
		ID: indexID, WorkspaceID: workspaceID, TokenizerID: "postgres-simple", TokenizerVersion: "v1",
		TokenizerConfigHash: processorTestHash("tokenizer"), FusionConfig: json.RawMessage(`{"method":"fts_only"}`),
		SourceSnapshotRef: "reindex-v1:" + string(eventID), ManifestHash: processorTestHash("manifest"), ExpectedChunkCount: 1,
		SourceManifestHash: processorTestHash("sources"), ExpectedSourceCount: &expectedSources,
		ProcessingContract: &domain.ProcessingContract{
			ParserID: ingestionAttempt.ParserID, ParserVersion: ingestionAttempt.ParserVersion,
			ParserConfigHash: ingestionAttempt.ParserConfigHash, ChunkStrategyVersion: ingestionAttempt.ChunkStrategyVersion,
			SchemaVersion: ingestionAttempt.SchemaVersion,
		},
		IdempotencyKey: "reindex-v1:" + string(eventID) + ":snapshot", Status: indexStatus,
		DegradedCapabilities: []domain.DegradedCapability{domain.DegradedVector}, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if indexStatus == domain.IndexStatusReady {
		index.Version = 2
		index.UpdatedAt = now.Add(time.Minute)
	}
	contextValue := ProcessorContext{
		Delivery: delivery, Attempt: attempt, Request: request,
		Binding: reindexcontract.Binding{
			WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
			ProposalID: request.ProposalID, RevisionID: request.RevisionID, ApprovalID: request.ApprovalID,
			WritebackExecutionID: request.WritebackExecutionID, TargetPath: request.TargetPath,
			ResultHash: request.ResultHash, GitCommit: request.GitCommit,
		},
	}
	switch {
	case checkpoint >= processorCheckpointRegression:
		contextValue.Delivery.Regression = &domain.DeliveryRegression{
			Code: domain.RegressionCodeSnapshotStructureV1, Hash: processorTestHash("regression"), PassedAt: now.Add(30 * time.Second),
		}
		fallthrough
	case checkpoint >= processorCheckpointIndex:
		contextValue.Delivery.IndexVersionID = processorIDPointer(indexID)
		contextValue.Delivery.ExcludedSourceCount = processorInt64Pointer(0)
		contextValue.IndexVersion = &index
		fallthrough
	case checkpoint >= processorCheckpointIngested:
		contextValue.Delivery.ParseProjectionID = processorIDPointer(projectionID)
		contextValue.Attempt.IngestionAttemptID = processorIDPointer(ingestionAttemptID)
		contextValue.IngestionAttempt = &ingestionAttempt
		fallthrough
	case checkpoint >= processorCheckpointSource:
		contextValue.Delivery.SourceVersionID = processorIDPointer(sourceVersionID)
		contextValue.TargetSourceID = sourceID
	}
	fence := domain.DeliveryFence{
		DeliveryID: deliveryID, DispatchNo: 1, AttemptID: deliveryAttemptID,
		AttemptNo: 1, Owner: attempt.LeaseOwner, DeliveryVersion: delivery.Version,
	}
	runtime := &processorLeaseRuntime{}
	lease, err := NewDeliveryLeaseSession(runtime, fence)
	if err != nil {
		t.Fatal(err)
	}
	captureResult := workspacedomain.SourceRegistrationResult{
		Source: workspacedomain.Source{ID: sourceID, WorkspaceID: workspaceID, OriginalLocation: request.TargetPath, CreatedAt: now},
		Version: workspacedomain.SourceVersion{ID: sourceVersionID, SourceID: sourceID, ContentHash: request.ResultHash,
			OriginalContentLocation: request.TargetPath, CapturedAt: now},
	}
	fixture := &processorFixture{
		context: contextValue, initialFence: fence, sourceID: sourceID, sourceVersionID: sourceVersionID,
		ingestionAttempt: ingestionAttempt, projectionID: projectionID, index: index,
		loader:    &processorLoaderFake{result: ProcessorContextLoadResult{Disposition: ProcessorContextCurrent, Context: contextValue}},
		capture:   &processorCaptureFake{result: captureResult},
		ingestion: &processorIngestionFake{result: ingestionapplication.ProcessResult{Attempt: ingestionAttempt, Projection: ingestiondomain.ProjectionResult{Projection: ingestiondomain.ParseProjection{ID: projectionID}}}},
		retrieval: &processorRetrievalFake{
			snapshot: domain.WorkspaceSnapshotResult{IndexVersion: index, SourceCount: 1, ChunkCount: 1, ExcludedSourceCount: 0, Created: true},
			lexical:  domain.ProjectionBatchResult{IndexVersionID: index.ID, AttemptedCount: 1, InsertedCount: 1},
			ready:    processorReadyIndex(index),
		},
		regression: &processorRegressionFake{result: domain.SnapshotRegressionResult{Code: domain.SnapshotStructureRegressionV1, Hash: processorTestHash("regression"), PassedAt: now.Add(30 * time.Second)}},
		runtime:    runtime, lease: lease,
	}
	return fixture
}

func (fixture *processorFixture) processor(t *testing.T) *Processor {
	t.Helper()
	processor, err := NewProcessor(ProcessorDependencies{
		Contexts: fixture.loader, Capture: fixture.capture, Ingestion: fixture.ingestion,
		Retrieval: fixture.retrieval, Regression: fixture.regression,
	}, ProcessorOptions{
		TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: processorTestHash("tokenizer"),
		FusionConfig: json.RawMessage(`{"method":"fts_only"}`), PageSize: 1000, MaxSources: 10_000, MaxChunks: 500_000,
		RetryDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

type processorLoaderFake struct {
	result ProcessorContextLoadResult
	err    error
	calls  int
}

func (fake *processorLoaderFake) LoadProcessorContext(_ context.Context, _ domain.DeliveryFence) (ProcessorContextLoadResult, error) {
	fake.calls++
	return fake.result, fake.err
}

type processorCaptureFake struct {
	result  workspacedomain.SourceRegistrationResult
	err     error
	request workspaceapplication.CaptureCommittedSourceRequest
	calls   int
}

func (fake *processorCaptureFake) CaptureCommittedSourceVersion(_ context.Context, request workspaceapplication.CaptureCommittedSourceRequest) (workspacedomain.SourceRegistrationResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

type processorIngestionFake struct {
	result  ingestionapplication.ProcessResult
	err     error
	request ingestionapplication.ProcessRequest
	calls   int
}

func (fake *processorIngestionFake) Process(_ context.Context, request ingestionapplication.ProcessRequest) (ingestionapplication.ProcessResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

type processorRetrievalFake struct {
	snapshot        domain.WorkspaceSnapshotResult
	snapshotErr     error
	snapshotRequest BeginWorkspaceSnapshotRequest
	snapshotCalls   int
	lexical         domain.ProjectionBatchResult
	lexicalErr      error
	lexicalRequest  TransitionRequest
	lexicalCalls    int
	ready           domain.IndexVersion
	readyErr        error
	readyRequest    TransitionRequest
	readyCalls      int
}

func (fake *processorRetrievalFake) BeginWorkspaceSnapshot(_ context.Context, request BeginWorkspaceSnapshotRequest) (domain.WorkspaceSnapshotResult, error) {
	fake.snapshotCalls++
	fake.snapshotRequest = request
	return fake.snapshot, fake.snapshotErr
}

func (fake *processorRetrievalFake) BuildLexical(_ context.Context, request TransitionRequest) (domain.ProjectionBatchResult, error) {
	fake.lexicalCalls++
	fake.lexicalRequest = request
	return fake.lexical, fake.lexicalErr
}

func (fake *processorRetrievalFake) Ready(_ context.Context, request TransitionRequest) (domain.IndexVersion, error) {
	fake.readyCalls++
	fake.readyRequest = request
	return fake.ready, fake.readyErr
}

type processorRegressionFake struct {
	result  domain.SnapshotRegressionResult
	err     error
	command domain.SnapshotRegressionCommand
	calls   int
}

func (fake *processorRegressionFake) RunSnapshotStructureV1(_ context.Context, command domain.SnapshotRegressionCommand) (domain.SnapshotRegressionResult, error) {
	fake.calls++
	fake.command = command
	return fake.result, fake.err
}

type processorLeaseRuntime struct {
	checkpoints []domain.DeliveryCheckpoint
	calls       int
	staleAt     int
	errAt       int
	err         error
}

func (*processorLeaseRuntime) Heartbeat(_ context.Context, _ DeliveryHeartbeatCommand) (DeliveryMutationResult, error) {
	return DeliveryMutationResult{}, errors.New("unexpected heartbeat")
}

func (*processorLeaseRuntime) Fail(_ context.Context, _ DeliveryFailureCommand) (DeliveryMutationResult, error) {
	return DeliveryMutationResult{}, errors.New("unexpected failure reduction")
}

func (runtime *processorLeaseRuntime) Checkpoint(_ context.Context, command DeliveryCheckpointCommand) (DeliveryMutationResult, error) {
	runtime.calls++
	runtime.checkpoints = append(runtime.checkpoints, command.Checkpoint)
	if runtime.errAt == runtime.calls {
		return DeliveryMutationResult{}, runtime.err
	}
	if runtime.staleAt == runtime.calls {
		return DeliveryMutationResult{Disposition: DeliveryMutationStale}, nil
	}
	next := command.Fence
	next.DeliveryVersion++
	return DeliveryMutationResult{Disposition: DeliveryMutationApplied, Fence: next}, nil
}

func processorReadyIndex(index domain.IndexVersion) domain.IndexVersion {
	index.Status = domain.IndexStatusReady
	index.Version++
	index.UpdatedAt = index.UpdatedAt.Add(time.Minute)
	return index
}

func processorID(ordinal int) foundation.ID {
	return foundation.ID(fmt.Sprintf("d1000000-0000-4000-8000-%012d", ordinal))
}

func processorTestHash(label string) string {
	value := strings.Repeat("0", 64)
	if label == "" {
		return value
	}
	digit := byte('1' + byte(len(label)%8))
	return strings.Repeat(string(digit), 64)
}

func processorIDPointer(value foundation.ID) *foundation.ID { return &value }
func processorInt64Pointer(value int64) *int64              { return &value }
func processorTimePointer(value time.Time) *time.Time       { return &value }

func processorErrorCode(err error) string {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return ""
	}
	return classified.Code
}
