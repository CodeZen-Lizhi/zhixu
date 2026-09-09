package application

import (
	"context"
	"errors"
	"log/slog"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisJournalQuery scopes recovery to the durable analysis run.
type WorkspaceAnalysisJournalQuery struct {
	WorkspaceID   foundation.ID
	AnalysisRunID foundation.ID
	WorkflowRunID foundation.ID
}

func (query WorkspaceAnalysisJournalQuery) Validate() error {
	return validateWorkspaceAnalysisModelQueryIDs(query.WorkspaceID, query.AnalysisRunID, query.WorkflowRunID)
}

// WorkspaceAnalysisJournalEntry is one append-only sequence slot. Tool output
// stays in its exact receipt; model output stays in its typed decision receipt.
type WorkspaceAnalysisJournalEntry struct {
	Sequence   int64
	Operation  domain.WorkspaceAnalysisOperation
	DecisionID *foundation.ID
	ModelRun   *domain.ModelRun
	ModelCall  *domain.ModelCall
	Decision   *domain.WorkspaceAnalysisDecisionReceipt
}

// WorkspaceAnalysisJournalSnapshot contains no source body or provider transcript.
type WorkspaceAnalysisJournalSnapshot struct {
	Run     domain.WorkspaceAnalysisRun
	Entries []WorkspaceAnalysisJournalEntry
}

// FinalizeWorkspaceAnalysisDecisionCommand completes a single AGENT call. The
// repository derives timestamps, model-run terminal state and budget settlement.
// UNKNOWN has no decision or usage; it is charged the complete reservation.
type FinalizeWorkspaceAnalysisDecisionCommand struct {
	Identity            WorkspaceAnalysisModelExecutionIdentity
	OperationKey        domain.WorkspaceAnalysisOperationKey
	OperationID         foundation.ID
	ExpectedCallVersion int64
	ReceiptID           foundation.ID
	Status              domain.ModelCallStatus
	Decision            *domain.WorkspaceAnalysisDecision
	Usage               domain.TokenUsage
	LatencyMillis       int64
	ErrorCode           string
}

func (command FinalizeWorkspaceAnalysisDecisionCommand) Validate() error {
	if err := command.Identity.Validate(); err != nil {
		return err
	}
	if command.Identity.DefinitionVersion != 2 || command.Identity.NodeKey != domain.WorkspaceAnalysisOperationNodeDecideNext ||
		command.OperationKey.NodeKey != command.Identity.NodeKey || command.OperationKey.Kind != domain.WorkspaceAnalysisOperationDecision ||
		command.OperationKey.Validate() != nil || !canonicalApplicationID(command.OperationID) || command.ExpectedCallVersion < 1 ||
		command.Usage.Validate() != nil || command.LatencyMillis < 0 {
		return workspaceAnalysisModelError(errors.New("workspace analysis decision completion identity is invalid"))
	}
	switch command.Status {
	case domain.ModelCallSucceeded:
		if command.Decision == nil || command.Decision.Validate() != nil || !canonicalApplicationID(command.ReceiptID) || command.ErrorCode != "" {
			return workspaceAnalysisModelError(errors.New("workspace analysis decision completion is incomplete"))
		}
	case domain.ModelCallFailed, domain.ModelCallUnknown:
		if command.Decision != nil || command.ReceiptID != "" || command.ErrorCode == "" ||
			command.Status == domain.ModelCallUnknown && command.Usage != (domain.TokenUsage{}) {
			return workspaceAnalysisModelError(errors.New("workspace analysis failed decision claims an output"))
		}
	default:
		return workspaceAnalysisModelError(errors.New("workspace analysis decision completion status is invalid"))
	}
	return nil
}

type WorkspaceAnalysisDecisionMutationResult struct {
	Run       domain.ModelRun
	Call      domain.ModelCall
	Operation domain.WorkspaceAnalysisOperation
	Decision  *domain.WorkspaceAnalysisDecisionReceipt
	Replayed  bool
}

// WorkspaceAnalysisDecisionRepository extends the existing atomic model-call
// authority. AuthorizeWorkspaceAnalysisModelCall accepts DECISION only at v2
// decide_next; its ModelRun is shared by all calls of the same live attempt.
type WorkspaceAnalysisDecisionRepository interface {
	WorkspaceAnalysisModelOperationRepository
	LoadWorkspaceAnalysisJournal(context.Context, WorkspaceAnalysisJournalQuery) (WorkspaceAnalysisJournalSnapshot, error)
	FinalizeWorkspaceAnalysisDecision(context.Context, FinalizeWorkspaceAnalysisDecisionCommand) (WorkspaceAnalysisDecisionMutationResult, error)
}

// WorkspaceAnalysisAdmissionDenial is evidence of a committed PENDING slot for
// which no external call was authorized. It never implies a failed transaction.
type WorkspaceAnalysisAdmissionDenial struct {
	OperationID foundation.ID `json:"-"`
	Reason      domain.WorkspaceAnalysisRunTerminationReason
	Requested   domain.WorkspaceAnalysisBudgetAmount `json:"-"`
}

func (denial *WorkspaceAnalysisAdmissionDenial) Error() string { return string(denial.Reason) }

func (denial WorkspaceAnalysisAdmissionDenial) String() string   { return string(denial.Reason) }
func (denial WorkspaceAnalysisAdmissionDenial) GoString() string { return denial.String() }
func (denial WorkspaceAnalysisAdmissionDenial) LogValue() slog.Value {
	return slog.StringValue(denial.String())
}

// WorkspaceAnalysisAdmissionDenialFromError extracts a durable pre-call denial.
func WorkspaceAnalysisAdmissionDenialFromError(err error) (WorkspaceAnalysisAdmissionDenial, bool) {
	var denial *WorkspaceAnalysisAdmissionDenial
	if !errors.As(err, &denial) || denial == nil || !canonicalApplicationID(denial.OperationID) ||
		(denial.Reason != domain.WorkspaceAnalysisRunBudgetExhausted && denial.Reason != domain.WorkspaceAnalysisRunDeadlineExceeded) {
		return WorkspaceAnalysisAdmissionDenial{}, false
	}
	return *denial, true
}
