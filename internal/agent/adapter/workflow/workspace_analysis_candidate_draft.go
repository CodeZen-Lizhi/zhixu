package workflow

import (
	"bytes"
	"context"
	"errors"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisCandidateDraftCoordinatorDependencies 是候选 Draft 创建和精确恢复边界。
type WorkspaceAnalysisCandidateDraftCoordinatorDependencies struct {
	Store  agentapplication.DraftStreamStore
	Loader agentapplication.WorkspaceAnalysisCandidateDraftSessionLoader
}

type workspaceAnalysisCandidateDraftCoordinator struct {
	store  agentapplication.DraftStreamStore
	loader agentapplication.WorkspaceAnalysisCandidateDraftSessionLoader
}

// NewWorkspaceAnalysisCandidateDraftCoordinator 创建只投影候选 Markdown 的 Draft 协调器。
func NewWorkspaceAnalysisCandidateDraftCoordinator(
	dependencies WorkspaceAnalysisCandidateDraftCoordinatorDependencies,
) (agentapplication.WorkspaceAnalysisCandidateDraftCoordinator, error) {
	if nilDependency(dependencies.Store) || nilDependency(dependencies.Loader) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis candidate draft dependencies are unavailable"),
		)
	}
	return &workspaceAnalysisCandidateDraftCoordinator{store: dependencies.Store, loader: dependencies.Loader}, nil
}

func (coordinator *workspaceAnalysisCandidateDraftCoordinator) Begin(
	ctx context.Context,
	binding agentapplication.DraftStreamBinding,
) (agentapplication.WorkspaceAnalysisCandidateDraft, error) {
	if coordinator == nil || nilDependency(coordinator.store) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis candidate draft coordinator is unavailable"),
		)
	}
	sink, err := beginDraftStream(
		ctx,
		coordinator.store,
		binding,
		agentapplication.WorkspaceAnalysisV1MaxRunDuration,
	)
	if err != nil {
		return nil, err
	}
	return &workspaceAnalysisCandidateDraft{sink: sink}, nil
}

func (coordinator *workspaceAnalysisCandidateDraftCoordinator) Load(
	ctx context.Context,
	query agentapplication.WorkspaceAnalysisCandidateDraftQuery,
) (agentapplication.DraftStreamSession, error) {
	if coordinator == nil || nilDependency(coordinator.loader) {
		return agentapplication.DraftStreamSession{}, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis candidate draft loader is unavailable"),
		)
	}
	return coordinator.loader.LoadWorkspaceAnalysisCandidateDraftSession(ctx, query)
}

type workspaceAnalysisCandidateDraft struct {
	sink *ragDraftStreamSink
	raw  bytes.Buffer
}

func (draft *workspaceAnalysisCandidateDraft) Append(
	_ context.Context,
	chunk agentapplication.WorkspaceAnalysisCandidateStreamChunk,
) error {
	if draft == nil || draft.sink == nil || chunk.Content == "" || !utf8.ValidString(chunk.Content) ||
		draft.raw.Len() > int(agentdomain.MaxWorkspaceAnalysisCandidateBytes)-len(chunk.Content) {
		return workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
			errors.New("workspace analysis candidate draft received an invalid bounded chunk"),
		)
	}
	draft.raw.WriteString(chunk.Content)
	return nil
}

func (draft *workspaceAnalysisCandidateDraft) Session() agentapplication.DraftStreamSession {
	if draft == nil || draft.sink == nil {
		return agentapplication.DraftStreamSession{}
	}
	return draft.sink.session
}

func (draft *workspaceAnalysisCandidateDraft) Complete(ctx context.Context) (agentapplication.DraftStreamSession, error) {
	if draft == nil || draft.sink == nil {
		return agentapplication.DraftStreamSession{}, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis candidate draft is unavailable"),
		)
	}
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(agentdomain.MaxWorkspaceAnalysisCandidateBytes)
	candidate, err := agentdomain.DecodeWorkspaceAnalysisCandidateProvider(draft.raw.Bytes(), limits)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if err := appendWorkspaceAnalysisDraftMarkdown(ctx, draft.sink, candidate.Payload.AnswerMarkdown); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	return draft.sink.Complete(ctx)
}

func (draft *workspaceAnalysisCandidateDraft) Abort(ctx context.Context) error {
	if draft == nil || draft.sink == nil {
		return nil
	}
	return draft.sink.Abort(ctx)
}

func appendWorkspaceAnalysisDraftMarkdown(ctx context.Context, sink *ragDraftStreamSink, markdown string) error {
	for len(markdown) > 0 {
		chunkBytes := min(len(markdown), agentapplication.MaxAnswerStreamChunkBytes)
		for chunkBytes > 0 && !utf8.ValidString(markdown[:chunkBytes]) {
			chunkBytes--
		}
		if chunkBytes == 0 {
			return workflowError(
				foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false,
				errors.New("workspace analysis candidate markdown cannot be split safely"),
			)
		}
		if err := sink.Append(ctx, agentapplication.AnswerStreamChunk{Content: markdown[:chunkBytes]}); err != nil {
			return err
		}
		markdown = markdown[chunkBytes:]
	}
	return nil
}

var _ agentapplication.WorkspaceAnalysisCandidateDraftCoordinator = (*workspaceAnalysisCandidateDraftCoordinator)(nil)
var _ agentapplication.WorkspaceAnalysisCandidateDraft = (*workspaceAnalysisCandidateDraft)(nil)
