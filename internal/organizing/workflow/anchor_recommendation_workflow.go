package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	AnchorRecommendationDefinitionKey     = "organizing.anchor-recommendation"
	AnchorRecommendationDefinitionVersion = int64(1)
	AnchorRecommendationNodeKind          = "organizing.anchor-recommendation.run"
	AnchorRecommendationInputSchema       = 1
)

func AnchorRecommendationDefinitions() []workflowdomain.RegisteredDefinition {
	node := workflowdomain.NodeDefinition{Key: AnchorRecommendationNodeKind, Kind: AnchorRecommendationNodeKind,
		InputSchemaVersion: AnchorRecommendationInputSchema, OutputSchemaVersion: 1,
		RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal}, RetryPolicy: workflowdomain.RetryPolicy{}}
	return []workflowdomain.RegisteredDefinition{{Key: AnchorRecommendationDefinitionKey, Version: AnchorRecommendationDefinitionVersion,
		InputSchemaVersion: AnchorRecommendationInputSchema, Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{node}}}}
}

func AnchorRecommendationStartKey(requestID foundation.ID, version int64) string {
	return "anchor-recommendation-start:" + string(requestID) + ":" + strconv.FormatInt(version, 10)
}

type AnchorRecommendationDispatcher struct {
	UnitOfWork  foundation.UnitOfWork
	Requests    organizingapp.AnchorRecommendationDispatchStore
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

func (d *AnchorRecommendationDispatcher) DispatchBatch(ctx context.Context, limit int) (int, error) {
	if d == nil || ctx == nil || limit < 1 || limit > 100 || nilScopedDependency(d.UnitOfWork) || d.Requests == nil || d.Starter == nil || d.Definitions == nil || d.IDs == nil || d.Clock == nil {
		return 0, workflowError(foundation.ErrorDependencyUnavailable, "ANCHOR_RECOMMENDATION_DISPATCH_UNAVAILABLE", false, "anchor recommendation dispatcher is unavailable")
	}
	if _, err := d.Definitions.Resolve(AnchorRecommendationDefinitionKey, AnchorRecommendationDefinitionVersion); err != nil {
		return 0, err
	}
	if _, err := d.Requests.ReconcileAnchorRecommendationWorkflows(ctx, limit); err != nil {
		return 0, err
	}
	started := 0
	for started < limit {
		found := false
		err := d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			request, ok, err := d.Requests.ClaimPendingAnchorRecommendationScoped(ctx, scope)
			if err != nil || !ok {
				return err
			}
			found = true
			definition, err := d.Definitions.Resolve(AnchorRecommendationDefinitionKey, AnchorRecommendationDefinitionVersion)
			if err != nil {
				return err
			}
			input, err := json.Marshal(organizingapp.AnchorRecommendationStartInput{RequestID: request.ID, ExpectedVersion: request.Version})
			if err != nil {
				return err
			}
			start, err := workflowapp.BuildRuntimeStartRequest(d.IDs, d.Clock, request.WorkspaceID, AnchorRecommendationStartKey(request.ID, request.Version), input, definition)
			if err != nil {
				return err
			}
			result, err := d.Starter.StartScoped(ctx, scope, start)
			if err != nil {
				return err
			}
			if result.Run.WorkspaceID != request.WorkspaceID || !validID(result.Run.ID) || result.Run.IdempotencyKey != AnchorRecommendationStartKey(request.ID, request.Version) || result.Job.JobID < 1 {
				return organizingapp.AnchorConflict()
			}
			if err := d.Requests.BindAnchorRecommendationWorkflowScoped(ctx, scope, request.ID, result.Run.ID); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return started, err
		}
		if !found {
			break
		}
		started++
	}
	return started, nil
}

type AnchorRecommendationModel interface {
	Recommend(context.Context, workflowapp.ExecutionContext, organizingapp.AnchorRecommendationRequest, organizingdomain.SynthesisRevision, *organizingdomain.Anchor, []organizingapp.SynthesisSourceView) (organizingapp.AnchorRecommendationRequest, error)
}

type unavailableAnchorRecommendationModel struct{}

func NewUnavailableAnchorRecommendationModel(_ organizingapp.AnchorRecommendationExecutionStore) AnchorRecommendationModel {
	return unavailableAnchorRecommendationModel{}
}
func (unavailableAnchorRecommendationModel) Recommend(_ context.Context, _ workflowapp.ExecutionContext, request organizingapp.AnchorRecommendationRequest, _ organizingdomain.SynthesisRevision, _ *organizingdomain.Anchor, _ []organizingapp.SynthesisSourceView) (organizingapp.AnchorRecommendationRequest, error) {
	return request, workflowError(foundation.ErrorDependencyUnavailable, "ANCHOR_MODEL_UNAVAILABLE", true, "anchor recommendation model is unavailable")
}

type AnchorRecommendationExecutorDependencies struct {
	Runs     WorkflowRunReader
	Requests organizingapp.AnchorRecommendationDispatchStore
	Notes    interface {
		GetSynthesisRevision(context.Context, foundation.ID, foundation.ID, foundation.ID) (organizingdomain.SynthesisRevision, error)
	}
	Sources organizingapp.SynthesisSourceReader
	Anchors interface {
		GetAnchor(context.Context, foundation.ID, foundation.ID) (organizingdomain.Anchor, error)
	}
	Model AnchorRecommendationModel
}

type AnchorRecommendationExecutor struct {
	dependencies AnchorRecommendationExecutorDependencies
}

func NewAnchorRecommendationExecutor(dependencies AnchorRecommendationExecutorDependencies) (*AnchorRecommendationExecutor, error) {
	if nilScopedDependency(dependencies.Runs) || dependencies.Requests == nil || dependencies.Notes == nil || dependencies.Sources == nil || dependencies.Anchors == nil || dependencies.Model == nil {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ANCHOR_RECOMMENDATION_EXECUTION_UNAVAILABLE", false, "anchor recommendation executor dependencies are incomplete")
	}
	return &AnchorRecommendationExecutor{dependencies: dependencies}, nil
}

func (e *AnchorRecommendationExecutor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	if e == nil || ctx == nil || execution.NodeKind != AnchorRecommendationNodeKind || execution.NodeKey != AnchorRecommendationNodeKind || execution.DefinitionVersion != AnchorRecommendationDefinitionVersion || execution.InputSchemaVersion != AnchorRecommendationInputSchema || !validID(execution.WorkspaceID) || !validID(execution.RunID) || !validID(execution.NodeRunID) || !validID(execution.NodeAttemptID) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ANCHOR_RECOMMENDATION_EXECUTION_INVALID", false, "anchor recommendation execution binding is invalid")
	}
	run, err := e.dependencies.Runs.GetRun(ctx, execution.RunID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	input, err := organizingapp.DecodeAnchorRecommendationStartInput(run.Input)
	if err != nil || run.WorkspaceID != execution.WorkspaceID || run.ID != execution.RunID || run.IdempotencyKey != AnchorRecommendationStartKey(input.RequestID, input.ExpectedVersion) {
		return workflowapp.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, "ANCHOR_RECOMMENDATION_EXECUTION_INVALID", false, "anchor recommendation start binding changed")
	}
	scheduled, err := e.dependencies.Requests.AnchorRecommendationScheduledWorkflow(ctx, execution.WorkspaceID, input.RequestID)
	if err != nil || scheduled != execution.RunID {
		return workflowapp.ExecutionResult{}, organizingapp.AnchorConflict()
	}
	request, err := e.loadRequest(ctx, execution.WorkspaceID, input.RequestID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if request.Status == organizingdomain.AnchorRecommendationSucceeded || request.Status == organizingdomain.AnchorRecommendationNoRecommendation {
		return anchorRecommendationReceipt(request), nil
	}
	if request.Status != organizingdomain.AnchorRecommendationPending || request.Version != input.ExpectedVersion {
		return workflowapp.ExecutionResult{}, organizingapp.AnchorConflict()
	}
	revision, err := e.dependencies.Notes.GetSynthesisRevision(ctx, request.WorkspaceID, request.NoteID, request.BasisRevisionID)
	if err != nil {
		return workflowapp.ExecutionResult{}, errors.Join(err, e.persistPendingFailure(ctx, request, execution.RunID, "ANCHOR_RECOMMENDATION_BASIS_UNAVAILABLE", false))
	}
	var anchor *organizingdomain.Anchor
	if request.Kind != organizingapp.AnchorInitialScopeRecommendation {
		loaded, err := e.dependencies.Anchors.GetAnchor(ctx, request.WorkspaceID, request.AnchorID)
		if err != nil {
			return workflowapp.ExecutionResult{}, errors.Join(err, e.persistPendingFailure(ctx, request, execution.RunID, "ANCHOR_RECOMMENDATION_ANCHOR_UNAVAILABLE", false))
		}
		if loaded.NoteID != request.NoteID || loaded.ScopeVersion != request.ExpectedScopeVersion {
			return workflowapp.ExecutionResult{}, errors.Join(organizingapp.AnchorConflict(), e.persistPendingFailure(ctx, request, execution.RunID, "ANCHOR_RECOMMENDATION_SCOPE_STALE", false))
		}
		anchor = &loaded
	}
	opened := make([]organizingapp.SynthesisSourceView, 0, len(request.Evidence))
	for _, reference := range request.Evidence {
		view, err := e.dependencies.Sources.OpenSynthesisSource(ctx, reference)
		if err != nil {
			return workflowapp.ExecutionResult{}, errors.Join(err, e.persistPendingFailure(ctx, request, execution.RunID, "ANCHOR_RECOMMENDATION_SOURCE_UNAVAILABLE", false))
		}
		if view.Reference != reference || view.Validate(request.WorkspaceID) != nil || view.Availability != organizingdomain.MaterialAvailable {
			return workflowapp.ExecutionResult{}, errors.Join(organizingapp.AnchorConflict(), e.persistPendingFailure(ctx, request, execution.RunID, "ANCHOR_RECOMMENDATION_SOURCE_STALE", false))
		}
		opened = append(opened, view)
	}
	if _, err := organizingapp.BuildAnchorRecommendationInput(request, revision, anchor, opened); err != nil {
		return workflowapp.ExecutionResult{}, errors.Join(err, e.persistPendingFailure(ctx, request, execution.RunID, "ANCHOR_RECOMMENDATION_INPUT_INVALID", false))
	}
	completed, err := e.dependencies.Model.Recommend(ctx, execution, request, revision, anchor, opened)
	if err != nil {
		code, retryable := "ANCHOR_RECOMMENDATION_EXECUTION_FAILED", false
		var classified *foundation.Error
		if errors.As(err, &classified) {
			code, retryable = classified.Code, classified.Retryable
		}
		return workflowapp.ExecutionResult{}, errors.Join(err, e.persistPendingFailure(ctx, request, execution.RunID, code, retryable))
	}
	if completed.ID != request.ID || completed.WorkspaceID != request.WorkspaceID || (completed.Status != organizingdomain.AnchorRecommendationSucceeded && completed.Status != organizingdomain.AnchorRecommendationNoRecommendation) {
		return workflowapp.ExecutionResult{}, organizingapp.AnchorConflict()
	}
	return anchorRecommendationReceipt(completed), nil
}

func (e *AnchorRecommendationExecutor) persistPendingFailure(ctx context.Context, request organizingapp.AnchorRecommendationRequest, workflowRunID foundation.ID, code string, retryable bool) error {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := e.dependencies.Requests.FailScheduledAnchorRecommendation(finalCtx, request.WorkspaceID, request.ID, workflowRunID, code, retryable)
	return err
}

func (e *AnchorRecommendationExecutor) loadRequest(ctx context.Context, workspaceID, requestID foundation.ID) (organizingapp.AnchorRecommendationRequest, error) {
	reader, ok := e.dependencies.Requests.(interface {
		GetAnchorRecommendation(context.Context, foundation.ID, foundation.ID) (organizingapp.AnchorRecommendationRequest, error)
	})
	if !ok {
		return organizingapp.AnchorRecommendationRequest{}, workflowError(foundation.ErrorDependencyUnavailable, "ANCHOR_RECOMMENDATION_EXECUTION_UNAVAILABLE", false, "anchor recommendation reader is unavailable")
	}
	return reader.GetAnchorRecommendation(ctx, workspaceID, requestID)
}

func anchorRecommendationReceipt(request organizingapp.AnchorRecommendationRequest) workflowapp.ExecutionResult {
	output, _ := json.Marshal(struct {
		RequestID foundation.ID                               `json:"request_id"`
		Status    organizingdomain.AnchorRecommendationStatus `json:"status"`
	}{request.ID, request.Status})
	return workflowapp.ExecutionResult{Output: output}
}
