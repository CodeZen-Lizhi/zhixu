package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type terminalResultRequest struct {
	Result        organizingdomain.RunResult
	DefinitionKey string
}

type terminalResultWriter interface {
	BindSucceededResult(context.Context, any, terminalResultRequest) error
}

// TerminalHook 在 Workflow 成功终态事务中追加不可变 Organizing RunResult。
type TerminalHook struct {
	results terminalResultWriter
}

var _ workflowapp.WorkflowTerminalHook = (*TerminalHook)(nil)

// NewTerminalHook 创建使用 Runtime PostgreSQL 事务写入结果的终态 Hook。
func NewTerminalHook() *TerminalHook {
	return &TerminalHook{results: postgresTerminalResultWriter{}}
}

func newTerminalHook(results terminalResultWriter) *TerminalHook {
	return &TerminalHook{results: results}
}

// OnWorkflowNodeTerminal 只处理四个 Organizing 最终节点；其他节点保持 no-op。
func (hook *TerminalHook) OnWorkflowNodeTerminal(ctx context.Context, transaction any, event workflowapp.WorkflowNodeTerminalEvent) error {
	definitionKey, expectedKind, final := terminalContract(event.NodeKind)
	if !final || event.Outcome != workflowapp.WorkflowTerminalOutcomeSucceeded {
		return nil
	}
	if hook == nil || hook.results == nil {
		return terminalUnavailable(errors.New("organizing terminal result writer is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !validID(event.WorkspaceID) || !validID(event.WorkflowRunID) || !validID(event.NodeRunID) ||
		!validID(event.NodeAttemptID) || event.WorkflowRunID == event.NodeRunID || event.TerminalAt.IsZero() {
		return terminalInvalid(errors.New("organizing terminal event identity is invalid"))
	}
	receipt, err := decodeFinalReceipt([]byte(event.TerminalOutput))
	if err != nil {
		return err
	}
	if receipt.Kind != expectedKind {
		return terminalInvalid(errors.New("organizing terminal receipt kind does not match the final node"))
	}
	result := organizingdomain.RunResult{
		ID: receipt.ResultID, WorkspaceID: event.WorkspaceID, RunBindingID: receipt.RunBindingID,
		SnapshotID: receipt.SnapshotID, WorkflowRunID: event.WorkflowRunID, NodeRunID: event.NodeRunID,
		Kind: receipt.Kind, ResultRef: receipt.ResultRef, ResultHash: receipt.ResultHash,
		CreatedAt: canonicalTime(event.TerminalAt),
	}
	if err := result.Validate(); err != nil {
		return terminalInvalid(fmt.Errorf("validate organizing terminal result: %w", err))
	}
	return hook.results.BindSucceededResult(ctx, transaction, terminalResultRequest{Result: result, DefinitionKey: definitionKey})
}

func decodeFinalReceipt(raw []byte) (finalReceipt, error) {
	receipt, err := decodeObject(raw, func(value finalReceipt) error {
		if value.SchemaVersion != receiptSchemaV1 || !validID(value.ResultID) || !validID(value.RunBindingID) ||
			!validID(value.SnapshotID) || (value.Kind != organizingdomain.ResultArtifact && value.Kind != organizingdomain.ResultMergeProposal) ||
			!validID(value.ResultRef) || !validHash(value.ResultHash) {
			return terminalInvalid(errors.New("organizing terminal receipt is invalid"))
		}
		return nil
	})
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Code == organizingapp.ErrorCodeResultInvalid {
			return finalReceipt{}, err
		}
		return finalReceipt{}, terminalInvalid(errors.New("organizing terminal output does not match the result receipt schema"))
	}
	return receipt, nil
}

func terminalContract(nodeKind string) (string, organizingdomain.ResultKind, bool) {
	switch nodeKind {
	case TopicArtifactNodeKind:
		return TopicArticleDefinitionKey, organizingdomain.ResultArtifact, true
	case MergeProposalNodeKind:
		return MergeDocumentsDefinitionKey, organizingdomain.ResultMergeProposal, true
	case KnowledgeReportNodeKind:
		return KnowledgeReportDefinitionKey, organizingdomain.ResultArtifact, true
	case InterviewReviewNodeKind:
		return InterviewReviewDefinitionKey, organizingdomain.ResultArtifact, true
	default:
		return "", "", false
	}
}

type postgresTerminalResultWriter struct{}

func (postgresTerminalResultWriter) BindSucceededResult(ctx context.Context, transaction any, request terminalResultRequest) error {
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil {
		return terminalInvalid(errors.New("organizing terminal transaction is invalid"))
	}
	result := request.Result
	if err := result.Validate(); err != nil || request.DefinitionKey == "" {
		return terminalInvalid(errors.New("organizing terminal result request is invalid"))
	}

	existing, found, err := loadTerminalResult(ctx, tx, result.WorkspaceID, result.WorkflowRunID)
	if err != nil {
		return err
	}
	if found {
		if !sameTerminalResult(existing, result) {
			return foundation.NewError(foundation.ErrorVersionConflict, organizingapp.ErrorCodeIdempotencyConflict, false, errors.New("organizing workflow run is bound to a different result"))
		}
		return nil
	}

	var snapshotID, workflowRunID foundation.ID
	var definitionKey string
	var definitionVersion int64
	err = tx.QueryRow(ctx, `SELECT snapshot_id::text,workflow_run_id::text,definition_key,definition_version
		FROM organizing.run_binding WHERE id=$1 AND workspace_id=$2 FOR SHARE`,
		string(result.RunBindingID), string(result.WorkspaceID)).
		Scan(&snapshotID, &workflowRunID, &definitionKey, &definitionVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return terminalInvalid(errors.New("organizing terminal run binding was not found"))
	}
	if err != nil {
		return classifyTerminalPostgres(err)
	}
	if snapshotID != result.SnapshotID || workflowRunID != result.WorkflowRunID ||
		definitionKey != request.DefinitionKey || definitionVersion != DefinitionVersion {
		return terminalInvalid(errors.New("organizing terminal result does not match its run binding"))
	}
	_, err = tx.Exec(ctx, `INSERT INTO organizing.run_result(
		id,workspace_id,run_binding_id,snapshot_id,workflow_run_id,node_run_id,
		result_kind,result_ref,result_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		string(result.ID), string(result.WorkspaceID), string(result.RunBindingID), string(result.SnapshotID),
		string(result.WorkflowRunID), string(result.NodeRunID), string(result.Kind), string(result.ResultRef),
		result.ResultHash, result.CreatedAt.UTC())
	if err != nil {
		return classifyTerminalPostgres(err)
	}
	return nil
}

func loadTerminalResult(ctx context.Context, tx pgx.Tx, workspaceID, workflowRunID foundation.ID) (organizingdomain.RunResult, bool, error) {
	var result organizingdomain.RunResult
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,run_binding_id::text,snapshot_id::text,
		workflow_run_id::text,node_run_id::text,result_kind,result_ref::text,result_hash,created_at
		FROM organizing.run_result WHERE workspace_id=$1 AND workflow_run_id=$2 FOR UPDATE`,
		string(workspaceID), string(workflowRunID)).
		Scan(&result.ID, &result.WorkspaceID, &result.RunBindingID, &result.SnapshotID, &result.WorkflowRunID,
			&result.NodeRunID, &result.Kind, &result.ResultRef, &result.ResultHash, &result.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingdomain.RunResult{}, false, nil
	}
	if err != nil {
		return organizingdomain.RunResult{}, false, classifyTerminalPostgres(err)
	}
	result.CreatedAt = canonicalTime(result.CreatedAt)
	if err := result.Validate(); err != nil {
		return organizingdomain.RunResult{}, false, terminalInvalid(fmt.Errorf("validate stored organizing terminal result: %w", err))
	}
	return result, true, nil
}

func sameTerminalResult(left, right organizingdomain.RunResult) bool {
	return left.WorkspaceID == right.WorkspaceID && left.RunBindingID == right.RunBindingID &&
		left.SnapshotID == right.SnapshotID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.Kind == right.Kind && left.ResultRef == right.ResultRef &&
		left.ResultHash == right.ResultHash
}

func terminalInvalid(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeResultInvalid, false, err)
}

func terminalUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, organizingapp.ErrorCodeRepositoryUnavailable, true, err)
}

func classifyTerminalPostgres(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeRepositoryUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return terminalUnavailable(err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23502", "23503", "23505", "23514", "22P02", "55000":
			return terminalInvalid(err)
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01", "57014":
			return foundation.NewError(foundation.ErrorRetryableFailure, organizingapp.ErrorCodeRepositoryUnavailable, true, err)
		}
	}
	return terminalUnavailable(err)
}
