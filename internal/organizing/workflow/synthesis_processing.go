package workflow

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type SynthesisProcessingQueries interface {
	GetSynthesisProcessing(context.Context, foundation.ID, foundation.ID) (organizingapp.SynthesisProcessing, error)
	ListSynthesisProcessing(context.Context, organizingapp.SynthesisListQuery) (organizingapp.SynthesisProcessingPage, error)
}

type SynthesisProcessingServiceDependencies struct {
	UnitOfWork  foundation.UnitOfWork
	Queries     SynthesisProcessingQueries
	Retries     SynthesisRetryStore
	Applied     SynthesisAppliedResultReader
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

type SynthesisProcessingService struct {
	dependencies SynthesisProcessingServiceDependencies
}

func NewSynthesisProcessingService(dependencies SynthesisProcessingServiceDependencies) (*SynthesisProcessingService, error) {
	if nilScopedDependency(dependencies.UnitOfWork) || nilScopedDependency(dependencies.Queries) || nilScopedDependency(dependencies.Retries) || nilScopedDependency(dependencies.Applied) ||
		nilScopedDependency(dependencies.Starter) || dependencies.Definitions == nil || nilScopedDependency(dependencies.IDs) || nilScopedDependency(dependencies.Clock) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisExecutionUnavailable, false, "synthesis processing service dependencies are incomplete")
	}
	return &SynthesisProcessingService{dependencies: dependencies}, nil
}

func (service *SynthesisProcessingService) ListProcessing(ctx context.Context, query organizingapp.SynthesisListQuery) (organizingapp.SynthesisProcessingPage, error) {
	if service == nil || ctx == nil {
		return organizingapp.SynthesisProcessingPage{}, synthesisInvalid("synthesis processing service is unavailable")
	}
	if query.Limit == 0 {
		query.Limit = organizingapp.DefaultSynthesisListLimit
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > organizingapp.MaxSynthesisListLimit || (query.BeforeTime == nil) != (query.BeforeID == "") || (query.BeforeID != "" && !validID(query.BeforeID)) {
		return organizingapp.SynthesisProcessingPage{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeSynthesisExecutionInvalid, false, "synthesis processing query is invalid")
	}
	return service.dependencies.Queries.ListSynthesisProcessing(ctx, query)
}

func (service *SynthesisProcessingService) GetProcessing(ctx context.Context, workspaceID, processingID foundation.ID) (organizingapp.SynthesisProcessing, error) {
	if service == nil || ctx == nil {
		return organizingapp.SynthesisProcessing{}, synthesisInvalid("synthesis processing service is unavailable")
	}
	if !validID(workspaceID) || !validID(processingID) {
		return organizingapp.SynthesisProcessing{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeSynthesisExecutionInvalid, false, "synthesis processing identity is invalid")
	}
	return service.dependencies.Queries.GetSynthesisProcessing(ctx, workspaceID, processingID)
}

// RetryProcessing starts a new bounded Workflow only for an explicit, versioned
// user command. The original SourceReady event and all failed calls remain.
func (service *SynthesisProcessingService) RetryProcessing(ctx context.Context, command organizingapp.RetrySynthesisCommand) (organizingapp.RetrySynthesisResult, error) {
	if service == nil || ctx == nil {
		return organizingapp.RetrySynthesisResult{}, synthesisInvalid("synthesis processing service is unavailable")
	}
	if !validID(command.WorkspaceID) || !validID(command.ProcessingID) || command.ExpectedVersion < 1 || command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 || strings.TrimSpace(command.IdempotencyKey) != command.IdempotencyKey || strings.ContainsAny(command.IdempotencyKey, "\r\n\x00") {
		return organizingapp.RetrySynthesisResult{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeSynthesisExecutionInvalid, false, "synthesis retry command is invalid")
	}
	d := service.dependencies
	encoded, err := json.Marshal(command)
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, err
	}
	requestHash := hashBytes(encoded)
	var result organizingapp.RetrySynthesisResult
	err = d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		existing, found, err := d.Retries.FindSynthesisRetryScoped(ctx, scope, command, requestHash)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		processing, executionNo, err := d.Retries.LockSynthesisProcessingForRetryScoped(ctx, scope, command)
		if err != nil {
			return err
		}
		_, recoverApply, err := d.Applied.LookupSynthesisApplyResultScoped(ctx, scope, command.WorkspaceID, command.ProcessingID)
		if err != nil {
			return err
		}
		definition, err := d.Definitions.Resolve(SynthesisDefinitionKey, SynthesisDefinitionVersion)
		if err != nil {
			return err
		}
		input, err := json.Marshal(SynthesisStartInput{ProcessingID: processing.ID, ExecutionNo: executionNo, ApplyRecovery: recoverApply})
		if err != nil {
			return err
		}
		request, err := workflowapp.BuildRuntimeStartRequest(d.IDs, d.Clock, command.WorkspaceID, SynthesisStartIdempotencyKey(processing.ID, executionNo), input, definition)
		if err != nil {
			return err
		}
		started, err := d.Starter.StartScoped(ctx, scope, request)
		if err != nil {
			return err
		}
		result, err = d.Retries.RecordSynthesisRetryScoped(ctx, scope, SynthesisRetryRecord{Command: command, RequestHash: requestHash, Start: started, ApplyRecovery: recoverApply, CreatedAt: canonicalTime(d.Clock.Now())})
		return err
	})
	if err != nil {
		return organizingapp.RetrySynthesisResult{}, err
	}
	return result, nil
}
