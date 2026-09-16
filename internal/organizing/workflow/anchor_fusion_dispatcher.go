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

// AnchorFusionDispatcher 消费已审核的关联请求。
// 它不会重新发布摄取事件，因此不能重新打开其旧回执。
type AnchorFusionDispatcher struct {
	Manuscripts bool
	UnitOfWork  foundation.UnitOfWork
	Requests    organizingapp.AnchorFusionRequestStore
	Processing  SynthesisProcessingStore
	Sources     SynthesisProvenanceGate
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

func (d *AnchorFusionDispatcher) DispatchBatch(ctx context.Context, limit int) (SynthesisDispatchBatchResult, error) {
	if d == nil || ctx == nil || limit < 1 || limit > 100 || nilScopedDependency(d.UnitOfWork) || d.Requests == nil || d.Processing == nil || d.Sources == nil || d.Starter == nil || d.Definitions == nil || d.IDs == nil || d.Clock == nil {
		return SynthesisDispatchBatchResult{}, synthesisInvalid("anchor fusion dispatcher is unavailable")
	}
	var out SynthesisDispatchBatchResult
	var excluded []foundation.ID
	for out.Claimed+out.Waiting < limit {
		var one SynthesisDispatchBatchResult
		var workspace foundation.ID
		var found bool
		err := d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			r, ok, err := d.Requests.ClaimAnchorFusionRequestScoped(ctx, scope, excluded)
			if err != nil || !ok {
				return err
			}
			found = true
			workspace = r.WorkspaceID
			event := r.SourceEvent
			event.Fusion = &organizingdomain.SynthesisFusionTrigger{RequestID: r.ID, AnchorID: r.AnchorID, NoteID: r.NoteID, ProposalID: r.ProposalID, ScopeVersion: r.ScopeVersion, AllowedSources: r.AllowedSources}
			if err = event.Validate(); err != nil {
				return err
			}
			if active, e := d.Processing.HasActiveSynthesisProcessingScoped(ctx, scope, r.WorkspaceID); e != nil {
				return e
			} else if active {
				one.Waiting = 1
				return nil
			}
			if generated, e := d.Sources.IsGeneratedSynthesisSourceScoped(ctx, scope, event.Source); e != nil {
				return e
			} else if generated {
				return d.Requests.MarkAnchorFusionRequestStaleScoped(ctx, scope, r.ID)
			}
			key, _ := event.ProcessingKey()
			id, e := d.IDs.New()
			if e != nil {
				return e
			}
			now := canonicalTime(d.Clock.Now())
			definition, e := d.Definitions.Resolve(SynthesisDefinitionKey, synthesisDispatchVersion(d.Manuscripts))
			if e != nil {
				return e
			}
			input, e := json.Marshal(SynthesisStartInput{ProcessingID: id, ExecutionNo: 1})
			if e != nil {
				return e
			}
			start, e := workflowapp.BuildRuntimeStartRequest(d.IDs, d.Clock, r.WorkspaceID, SynthesisStartIdempotencyKey(id, 1), input, definition)
			if e != nil {
				return e
			}
			started, e := d.Starter.StartScoped(ctx, scope, start)
			if e != nil {
				return e
			}
			p := organizingapp.SynthesisProcessing{ID: id, SourceEvent: event, WorkflowRunID: started.Run.ID, RequestHash: strings.TrimPrefix(key, "synthesis-fusion:"), Status: organizingapp.SynthesisProcessingPending, RevisionIDs: []foundation.ID{}, Version: 1, CreatedAt: now, UpdatedAt: now}
			if e = d.Processing.CreateSynthesisProcessingScoped(ctx, scope, SynthesisCreateProcessing{Processing: p, Start: started}); e != nil {
				return e
			}
			if e = d.Requests.MarkAnchorFusionRequestStartedScoped(ctx, scope, r.ID, id); e != nil {
				return e
			}
			one.Claimed, one.Started = 1, 1
			return nil
		})
		if err != nil {
			return out, err
		}
		if !found {
			break
		}
		out.Claimed += one.Claimed
		out.Started += one.Started
		out.Waiting += one.Waiting
		if one.Waiting > 0 {
			excluded = append(excluded, workspace)
		}
	}
	return out, nil
}
