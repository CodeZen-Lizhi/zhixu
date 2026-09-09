package workflow

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type SynthesisDispatcherDependencies struct {
	UnitOfWork  foundation.UnitOfWork
	Outbox      SynthesisSourceReadyOutbox
	Sources     SynthesisProvenanceGate
	Processing  SynthesisProcessingStore
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

type SynthesisSourceReadyOutbox interface {
	workflowapp.ScopedSourceReadyOutbox
	ClaimSourceReadyExcludingScoped(context.Context, foundation.TransactionScope, []foundation.ID) (workflowapp.SourceReadyOutboxFact, bool, error)
}

type SynthesisDispatchBatchResult struct {
	Claimed  int
	Started  int
	Replayed int
	Skipped  int
	Waiting  int
}

// SynthesisDispatcher atomically consumes an existing source-ready notification
// and starts the fixed Workflow. It performs no model or filesystem operation.
type SynthesisDispatcher struct {
	dependencies SynthesisDispatcherDependencies
}

func NewSynthesisDispatcher(dependencies SynthesisDispatcherDependencies) (*SynthesisDispatcher, error) {
	if nilScopedDependency(dependencies.UnitOfWork) || nilScopedDependency(dependencies.Outbox) ||
		nilScopedDependency(dependencies.Sources) || nilScopedDependency(dependencies.Processing) ||
		nilScopedDependency(dependencies.Starter) || dependencies.Definitions == nil ||
		nilScopedDependency(dependencies.IDs) || nilScopedDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisExecutionUnavailable, false, "synthesis source dispatcher dependencies are incomplete")
	}
	if _, err := dependencies.Definitions.Resolve(SynthesisDefinitionKey, SynthesisDefinitionVersion); err != nil {
		return nil, err
	}
	return &SynthesisDispatcher{dependencies: dependencies}, nil
}

func (dispatcher *SynthesisDispatcher) DispatchBatch(ctx context.Context, limit int) (SynthesisDispatchBatchResult, error) {
	if dispatcher == nil || ctx == nil || limit < 1 || limit > 100 {
		return SynthesisDispatchBatchResult{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeSynthesisExecutionInvalid, false, "synthesis dispatch batch is invalid")
	}
	var result SynthesisDispatchBatchResult
	var excluded []foundation.ID
	for result.Claimed+result.Waiting < limit {
		one, workspaceID, found, err := dispatcher.dispatchOne(ctx, excluded)
		if err != nil {
			return result, err
		}
		if !found {
			break
		}
		result.Claimed += one.Claimed
		result.Started += one.Started
		result.Replayed += one.Replayed
		result.Skipped += one.Skipped
		result.Waiting += one.Waiting
		if one.Waiting != 0 {
			excluded = append(excluded, workspaceID)
		}
	}
	return result, nil
}

func (dispatcher *SynthesisDispatcher) dispatchOne(ctx context.Context, excluded []foundation.ID) (SynthesisDispatchBatchResult, foundation.ID, bool, error) {
	d := dispatcher.dependencies
	var result SynthesisDispatchBatchResult
	var found bool
	var workspaceID foundation.ID
	err := d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		fact, exists, err := d.Outbox.ClaimSourceReadyExcludingScoped(ctx, scope, excluded)
		if err != nil || !exists {
			return err
		}
		found = true
		workspaceID = fact.Ready.WorkspaceID
		event := synthesisSourceEvent(fact)
		if err := event.Validate(); err != nil {
			return err
		}
		existing, exists, err := d.Processing.FindSynthesisProcessingScoped(ctx, scope, event)
		if err != nil {
			return err
		}
		if exists {
			if existing.SourceEvent.Source != event.Source || existing.SourceEvent.ProcessorVersion != event.ProcessorVersion {
				return synthesisInvalid("source-ready replay changed its complete source binding")
			}
			if err := d.Outbox.PublishSourceReadyScoped(ctx, scope, fact); err != nil {
				return err
			}
			result.Claimed, result.Replayed = 1, 1
			return nil
		}
		generated, err := d.Sources.IsGeneratedSynthesisSourceScoped(ctx, scope, event.Source)
		if err != nil {
			return err
		}
		key, _ := event.ProcessingKey()
		id, err := d.IDs.New()
		if err != nil {
			return err
		}
		now := canonicalTime(d.Clock.Now())
		processing := organizingapp.SynthesisProcessing{ID: id, SourceEvent: event, RequestHash: strings.TrimPrefix(key, "synthesis:"),
			Status: organizingapp.SynthesisProcessingPending, RevisionIDs: []foundation.ID{}, Version: 1, CreatedAt: now, UpdatedAt: now}
		if generated {
			processing.Status, processing.CompletedAt = organizingapp.SynthesisProcessingSkipped, &now
			if err := d.Processing.RecordSkippedSynthesisSourceScoped(ctx, scope, processing); err != nil {
				return err
			}
			if err := d.Outbox.PublishSourceReadyScoped(ctx, scope, fact); err != nil {
				return err
			}
			result.Claimed, result.Skipped = 1, 1
			return nil
		}
		active, err := d.Processing.HasActiveSynthesisProcessingScoped(ctx, scope, event.Source.WorkspaceID)
		if err != nil {
			return err
		}
		if active {
			// Keep the original notification unpublished. A later bounded pass
			// runs it after the previous source has committed its candidate.
			result.Waiting = 1
			return nil
		}
		definition, err := d.Definitions.Resolve(SynthesisDefinitionKey, SynthesisDefinitionVersion)
		if err != nil {
			return err
		}
		input, err := json.Marshal(SynthesisStartInput{ProcessingID: id, ExecutionNo: 1})
		if err != nil {
			return err
		}
		request, err := workflowapp.BuildRuntimeStartRequest(d.IDs, d.Clock, event.Source.WorkspaceID,
			SynthesisStartIdempotencyKey(id, 1), input, definition)
		if err != nil {
			return err
		}
		started, err := d.Starter.StartScoped(ctx, scope, request)
		if err != nil {
			return err
		}
		if started.Run.WorkspaceID != event.Source.WorkspaceID || !validID(started.Run.ID) ||
			started.Run.RequestHash != request.RequestHash || started.Run.IdempotencyKey != request.Run.IdempotencyKey || started.Job.JobID < 1 {
			return synthesisInvalid("synthesis Workflow start returned a conflicting receipt")
		}
		processing.WorkflowRunID = started.Run.ID
		if err := d.Processing.CreateSynthesisProcessingScoped(ctx, scope, SynthesisCreateProcessing{Processing: processing, Start: started}); err != nil {
			return err
		}
		if err := d.Outbox.PublishSourceReadyScoped(ctx, scope, fact); err != nil {
			return err
		}
		result.Claimed, result.Started = 1, 1
		return nil
	})
	if err != nil {
		return SynthesisDispatchBatchResult{}, "", false, err
	}
	return result, workspaceID, found, nil
}

func synthesisSourceEvent(fact workflowapp.SourceReadyOutboxFact) organizingdomain.SynthesisSourceReady {
	ready := fact.Ready
	return organizingdomain.SynthesisSourceReady{ID: fact.EventID, Source: organizingdomain.SynthesisSourceVersion{
		WorkspaceID: ready.WorkspaceID, SourceID: ready.SourceID, SourceVersionID: ready.SourceVersionID,
		ContentArtifactID: ready.ContentArtifactID, ParseProjectionID: ready.ParseProjectionID, ContentHash: ready.ContentHash},
		IngestionAttemptID: ready.IngestionAttemptID, ProcessorVersion: organizingdomain.SynthesisProcessorVersion, CreatedAt: canonicalTime(ready.OccurredAt)}
}
