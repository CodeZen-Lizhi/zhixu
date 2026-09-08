package postgres

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"time"
)

const (
	ErrorCodeWorkspaceAnalysisFinalizeConflict    = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_CONFLICT"
	ErrorCodeWorkspaceAnalysisFinalizeCorrupt     = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_CORRUPT"
	ErrorCodeWorkspaceAnalysisFinalizeUnavailable = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_UNAVAILABLE"

	workspaceAnalysisCommitRecoveryTimeout = 5 * time.Second
)

type workspaceAnalysisFinalizationFence struct {
	RunID                     foundation.ID
	WorkflowStatus            string
	WorkflowCancelRequestedAt *time.Time
	NodeStatus                string
	NodeKey                   string
	NodeAttemptNo             int
	NodeLeaseOwner            *string
	NodeLeaseUntil            *time.Time
	AttemptStatus             string
	AttemptNo                 int
	AttemptLeaseOwner         *string
	AttemptLeaseUntil         *time.Time
	RunStatus                 agentdomain.WorkspaceAnalysisRunStatus
	RunReason                 *agentdomain.WorkspaceAnalysisRunTerminationReason
	RunVersion                int64
	DeadlineAt                time.Time
	MaxModelCalls             int
	MaxToolCalls              int
	MaxSourceReads            int
	MaxInputTokens            int64
	MaxOutputTokens           int64
	ReservedModelCalls        int
	ReservedToolCalls         int
	ReservedSourceReads       int
	ReservedInputTokens       int64
	ReservedOutputTokens      int64
	SettledModelCalls         int
	SettledToolCalls          int
	SettledSourceReads        int
	SettledInputTokens        int64
	SettledOutputTokens       int64
	SettledCostMicrounits     *int64
	RunCreatedAt              time.Time
	RunUpdatedAt              time.Time
	DatabaseNow               time.Time
}

func validateActiveSuccessFence(
	fence workspaceAnalysisFinalizationFence,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) error {
	if err := validateActiveWorkspaceAnalysisFence(fence, lookup); err != nil {
		return err
	}
	if fence.NodeKey != conversationworkflow.WorkspaceAnalysisNodeReviewPublish || fence.WorkflowCancelRequestedAt != nil ||
		fence.DatabaseNow.After(fence.DeadlineAt) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis success fence is no longer publishable"))
	}
	return nil
}

func validateActiveTerminationFence(
	fence workspaceAnalysisFinalizationFence,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) error {
	if err := validateCurrentWorkspaceAnalysisLease(fence, command.WorkspaceAnalysisPublicationLookup); err != nil {
		return err
	}
	queuedPreoperationDeadline := command.Reason == agentdomain.WorkspaceAnalysisRunDeadlineExceeded &&
		command.OperationID == nil && fence.RunStatus == agentdomain.WorkspaceAnalysisRunQueued
	if fence.RunReason != nil || (fence.RunStatus != agentdomain.WorkspaceAnalysisRunRunning && !queuedPreoperationDeadline) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis termination fence is not current"))
	}
	if command.Reason == agentdomain.WorkspaceAnalysisRunCancellation {
		if fence.WorkflowCancelRequestedAt == nil || fence.WorkflowCancelRequestedAt.After(fence.DatabaseNow) {
			return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis cancellation is not requested at this checkpoint"))
		}
	} else if fence.WorkflowCancelRequestedAt != nil {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis non-cancellation terminalization raced cancellation"))
	}
	if command.Reason != agentdomain.WorkspaceAnalysisRunDeadlineExceeded && fence.DatabaseNow.After(fence.DeadlineAt) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis terminalization missed its run deadline"))
	}
	return nil
}

func validateActiveWorkspaceAnalysisFence(
	fence workspaceAnalysisFinalizationFence,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) error {
	if err := validateCurrentWorkspaceAnalysisLease(fence, lookup); err != nil {
		return err
	}
	if fence.RunStatus != agentdomain.WorkspaceAnalysisRunRunning || fence.RunReason != nil {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis finalization fence is not current"))
	}
	return nil
}

func validateCurrentWorkspaceAnalysisLease(
	fence workspaceAnalysisFinalizationFence,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) error {
	if fence.WorkflowStatus != "running" || fence.NodeStatus != "running" || fence.AttemptStatus != "running" ||
		fence.NodeAttemptNo != fence.AttemptNo || int64(fence.NodeAttemptNo) != lookup.ExpectedLeaseFence ||
		fence.NodeLeaseOwner == nil || *fence.NodeLeaseOwner != lookup.ExpectedLeaseOwner ||
		fence.AttemptLeaseOwner == nil || *fence.AttemptLeaseOwner != lookup.ExpectedLeaseOwner ||
		fence.NodeLeaseUntil == nil || fence.AttemptLeaseUntil == nil || !fence.NodeLeaseUntil.Equal(*fence.AttemptLeaseUntil) ||
		!fence.DatabaseNow.Before(*fence.AttemptLeaseUntil) {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis finalization lease does not match the caller fence"))
	}
	return nil
}

type workspaceAnalysisOperationRecord struct {
	ID          foundation.ID
	NodeRunID   foundation.ID
	Kind        agentdomain.WorkspaceAnalysisOperationKind
	CallKind    agentdomain.WorkspaceAnalysisOperationCallKind
	Status      agentdomain.WorkspaceAnalysisOperationStatus
	ModelCallID *foundation.ID
	ToolCallID  *foundation.ID
	ResultKind  *agentdomain.WorkspaceAnalysisOperationResultKind
	ResultID    *foundation.ID
	ResultHash  *string
	ErrorCode   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

type workspaceAnalysisReservationRecord struct {
	Found     bool
	Status    agentdomain.WorkspaceAnalysisBudgetReservationStatus
	SettledAt *time.Time
}

type workspaceAnalysisCallAuthority struct {
	Reservation        workspaceAnalysisReservationRecord
	ModelRunID         *foundation.ID
	ModelRunStatus     *agentdomain.ModelRunStatus
	ModelFinalResult   *string
	ModelRunCompleted  *time.Time
	ModelCallStatus    *agentdomain.ModelCallStatus
	ModelCallCompleted *time.Time
	ToolCallStatus     *toolsdomain.CallStatus
	ToolCallCompleted  *time.Time
	LatestFactAt       time.Time
}

type workspaceAnalysisTerminationAuthority struct {
	Operation *workspaceAnalysisOperationRecord
	Call      workspaceAnalysisCallAuthority
}

func scanWorkspaceAnalysisOperation(row scanner) (workspaceAnalysisOperationRecord, error) {
	var (
		record                            workspaceAnalysisOperationRecord
		id, nodeRunID                     string
		modelCallID, toolCallID, resultID *string
		resultKind, resultHash, errorCode *string
	)
	if err := row.Scan(
		&id, &nodeRunID, &record.Kind, &record.CallKind, &record.Status, &modelCallID, &toolCallID,
		&resultKind, &resultID, &resultHash, &errorCode, &record.CreatedAt, &record.UpdatedAt, &record.CompletedAt,
	); err != nil {
		return workspaceAnalysisOperationRecord{}, err
	}
	parsedID, err := parseCanonicalID(id)
	if err != nil {
		return workspaceAnalysisOperationRecord{}, err
	}
	parsedNodeRunID, err := parseCanonicalID(nodeRunID)
	if err != nil {
		return workspaceAnalysisOperationRecord{}, err
	}
	record.ID, record.NodeRunID = parsedID, parsedNodeRunID
	if modelCallID != nil {
		value, parseErr := parseCanonicalID(*modelCallID)
		if parseErr != nil {
			return workspaceAnalysisOperationRecord{}, parseErr
		}
		record.ModelCallID = &value
	}
	if toolCallID != nil {
		value, parseErr := parseCanonicalID(*toolCallID)
		if parseErr != nil {
			return workspaceAnalysisOperationRecord{}, parseErr
		}
		record.ToolCallID = &value
	}
	if resultID != nil {
		value, parseErr := parseCanonicalID(*resultID)
		if parseErr != nil {
			return workspaceAnalysisOperationRecord{}, parseErr
		}
		record.ResultID = &value
	}
	if resultKind != nil {
		value := agentdomain.WorkspaceAnalysisOperationResultKind(*resultKind)
		record.ResultKind = &value
	}
	record.ResultHash, record.ErrorCode = resultHash, errorCode
	return record, nil
}

func validateWorkspaceAnalysisTerminationAuthority(
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	fence workspaceAnalysisFinalizationFence,
	operation workspaceAnalysisOperationRecord,
	authority workspaceAnalysisCallAuthority,
) error {
	artifactMatches := func(kind conversationapplication.WorkspaceAnalysisTerminationArtifactKind) bool {
		return command.Artifact != nil && command.Artifact.Kind == kind && operation.ResultID != nil && operation.ResultHash != nil &&
			*operation.ResultID == command.Artifact.ID && *operation.ResultHash == command.Artifact.Hash
	}
	modelSucceeded := authority.ModelRunStatus != nil && *authority.ModelRunStatus == agentdomain.ModelRunSucceeded &&
		authority.ModelCallStatus != nil && *authority.ModelCallStatus == agentdomain.ModelCallSucceeded
	toolSucceeded := authority.ToolCallStatus != nil && *authority.ToolCallStatus == toolsdomain.CallSucceeded
	settled := authority.Reservation.Found && authority.Reservation.Status == agentdomain.WorkspaceAnalysisBudgetSettled
	switch command.Reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationKnowledgeSearch || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt) ||
			!toolSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("evidence-insufficient authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationCitationValidation || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt) ||
			!toolSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("citation-invalid authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunFaithfulnessRejected:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationFaithfulnessReview || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult) ||
			!modelSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("faithfulness rejection authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunModelRefused:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			operation.ErrorCode == nil || *operation.ErrorCode != string(agentdomain.WorkspaceAnalysisRunModelRefused) ||
			authority.ModelRunStatus == nil || *authority.ModelRunStatus != agentdomain.ModelRunRefused ||
			authority.ModelCallStatus == nil || *authority.ModelCallStatus != agentdomain.ModelCallSucceeded ||
			authority.ModelFinalResult == nil || *authority.ModelFinalResult != agentdomain.ResultTypeRefusal || !settled {
			return workspaceAnalysisTerminationAuthorityError("model refusal authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunNeedsClarification:
		if operation.Kind != agentdomain.WorkspaceAnalysisOperationRetrievalPlan || operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded ||
			operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || !artifactMatches(conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult) ||
			!modelSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("clarification authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted:
		if operation.Status != agentdomain.WorkspaceAnalysisOperationPending || authority.Reservation.Found || operation.ModelCallID != nil || operation.ToolCallID != nil ||
			command.BudgetRequest == nil || !workspaceAnalysisBudgetRequestMatchesOperation(*command.BudgetRequest, operation.Kind, fence) {
			return workspaceAnalysisTerminationAuthorityError("budget exhaustion authority is incomplete")
		}
		request := *command.BudgetRequest
		if request.ModelCalls <= fence.MaxModelCalls-fence.ReservedModelCalls-fence.SettledModelCalls &&
			request.ToolCalls <= fence.MaxToolCalls-fence.ReservedToolCalls-fence.SettledToolCalls &&
			request.SourceReads <= fence.MaxSourceReads-fence.ReservedSourceReads-fence.SettledSourceReads &&
			request.InputTokens <= fence.MaxInputTokens-fence.ReservedInputTokens-fence.SettledInputTokens &&
			request.OutputTokens <= fence.MaxOutputTokens-fence.ReservedOutputTokens-fence.SettledOutputTokens {
			return workspaceAnalysisTerminationAuthorityError("budget request still fits the authoritative remaining budget")
		}
	case agentdomain.WorkspaceAnalysisRunReceiptInvalid:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			operation.ErrorCode == nil || *operation.ErrorCode != string(agentdomain.WorkspaceAnalysisRunReceiptInvalid) || !toolSucceeded || !settled {
			return workspaceAnalysisTerminationAuthorityError("receipt-invalid authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunResultUnknown:
		callUnknown := operation.CallKind == agentdomain.WorkspaceAnalysisOperationCallModel && authority.ModelCallStatus != nil &&
			*authority.ModelCallStatus == agentdomain.ModelCallUnknown && authority.ModelRunStatus != nil && *authority.ModelRunStatus == agentdomain.ModelRunUnknown
		callUnknown = callUnknown || (operation.CallKind == agentdomain.WorkspaceAnalysisOperationCallTool && authority.ToolCallStatus != nil && *authority.ToolCallStatus == toolsdomain.CallUnknown)
		if operation.Status != agentdomain.WorkspaceAnalysisOperationUnknown || !authority.Reservation.Found ||
			authority.Reservation.Status != agentdomain.WorkspaceAnalysisBudgetUnknownCharged || !callUnknown {
			return workspaceAnalysisTerminationAuthorityError("unknown result authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunDeadlineExceeded:
		if operation.Status != agentdomain.WorkspaceAnalysisOperationPending && operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded {
			return workspaceAnalysisTerminationAuthorityError("deadline authority is not pending or successful")
		}
		if operation.Status == agentdomain.WorkspaceAnalysisOperationPending && authority.Reservation.Found {
			return workspaceAnalysisTerminationAuthorityError("pending deadline authority unexpectedly owns a reservation")
		}
	case agentdomain.WorkspaceAnalysisRunModelFailed:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallModel || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			authority.ModelCallStatus == nil || *authority.ModelCallStatus != agentdomain.ModelCallFailed ||
			authority.ModelRunStatus == nil || *authority.ModelRunStatus != agentdomain.ModelRunFailed || !settled {
			return workspaceAnalysisTerminationAuthorityError("model failure authority is incomplete")
		}
	case agentdomain.WorkspaceAnalysisRunToolFailed:
		if operation.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			authority.ToolCallStatus == nil || *authority.ToolCallStatus != toolsdomain.CallFailed || !settled {
			return workspaceAnalysisTerminationAuthorityError("tool failure authority is incomplete")
		}
	default:
		return workspaceAnalysisTerminationAuthorityError("termination reason has no operation authority matrix")
	}
	return nil
}

func workspaceAnalysisBudgetRequestMatchesOperation(
	request conversationapplication.WorkspaceAnalysisBudgetRequest,
	kind agentdomain.WorkspaceAnalysisOperationKind,
	fence workspaceAnalysisFinalizationFence,
) bool {
	want := conversationapplication.WorkspaceAnalysisBudgetRequest{}
	switch kind {
	case agentdomain.WorkspaceAnalysisOperationRetrievalPlan:
		want.ModelCalls, want.InputTokens, want.OutputTokens = 1, agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall, agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationAnswerSynthesis:
		want.ModelCalls, want.InputTokens = 1, agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall
		want.OutputTokens = fence.MaxOutputTokens - agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens - agentdomain.WorkspaceAnalysisV1ReviewMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationFaithfulnessReview:
		want.ModelCalls, want.InputTokens, want.OutputTokens = 1, agentdomain.WorkspaceAnalysisV1MaxInputTokensPerModelCall, agentdomain.WorkspaceAnalysisV1ReviewMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		want.ToolCalls, want.SourceReads = 1, 1
	case agentdomain.WorkspaceAnalysisOperationGitStatus, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch,
		agentdomain.WorkspaceAnalysisOperationCitationValidation:
		want.ToolCalls = 1
	default:
		return false
	}
	return request.ModelCalls == want.ModelCalls && request.ToolCalls == want.ToolCalls && request.SourceReads == want.SourceReads &&
		request.InputTokens == want.InputTokens && request.OutputTokens == want.OutputTokens && request.RequestedCostMicrounits == nil
}

func workspaceAnalysisTerminationAuthorityError(message string) error {
	return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New(message))
}

type workspaceAnalysisSuccessFacts struct {
	Candidate         agentdomain.WorkspaceAnalysisCandidate
	GitReceipt        toolsdomain.ResultReceipt
	ValidationReceipt toolsdomain.ResultReceipt
	ReviewResult      agentdomain.WorkspaceAnalysisModelResult
	Review            agentdomain.FaithfulnessReviewResult
	LatestFactAt      time.Time
}

func validateWorkspaceAnalysisLoadedReceipt(receipt toolsdomain.ResultReceipt) error {
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(receipt.Tool)
	if !found || receipt.OutputSchema != contract.OutputSchema || receipt.PersistencePolicy != toolsdomain.ResultPersistenceCanonical ||
		receipt.MaxOutputBytes != contract.MaxOutputBytes || receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes ||
		contract.RequiresPrivateBinding != (receipt.PrivateBinding != nil) && contract.RequiresPrivateBinding ||
		receipt.OutputBytes != int64(len(receipt.Output)) || receipt.OutputBytes < 1 || receipt.OutputBytes > contract.MaxOutputBytes ||
		workspaceAnalysisDocumentHash(receipt.Output) != receipt.OutputHash || receipt.CreatedAt.IsZero() {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis receipt envelope is invalid"))
	}
	if receipt.PrivateBinding != nil {
		if receipt.PrivateBinding.Schema != contract.PrivateBindingSchema || receipt.PrivateBinding.Bytes != int64(len(receipt.PrivateBinding.Document)) ||
			receipt.PrivateBinding.Bytes < 1 || receipt.PrivateBinding.Bytes > contract.MaxPrivateBindingBytes ||
			workspaceAnalysisDocumentHash(receipt.PrivateBinding.Document) != receipt.PrivateBinding.Hash {
			return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis receipt private binding is invalid"))
		}
	}
	return nil
}

type workspaceAnalysisPublication struct {
	Status        conversationdomain.AnswerPublicationStatus
	ResultType    conversationdomain.AnswerResultType
	ModelRunID    *foundation.ID
	Document      json.RawMessage
	ResultHash    string
	RunStatus     agentdomain.WorkspaceAnalysisRunStatus
	Reason        agentdomain.WorkspaceAnalysisRunTerminationReason
	CitationCount int64
}

func buildWorkspaceAnalysisSuccessPublication(
	workspaceID foundation.ID,
	fence workspaceAnalysisFinalizationFence,
	facts workspaceAnalysisSuccessFacts,
) (workspaceAnalysisPublication, error) {
	candidate, err := agentdomain.DecodeWorkspaceAnalysisCandidate(facts.Candidate.Document, agentdomain.DefaultDecodeLimits())
	if err != nil || candidate.ModelRunRef != facts.Candidate.SynthesisModelRunID {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis candidate document is invalid"))
	}
	git, err := conversationworkflow.DecodeWorkspaceAnalysisGitStatusSummary(facts.GitReceipt.Output)
	if err != nil || facts.GitReceipt.Tool != (toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}) {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis Git receipt is invalid"))
	}
	validationResults, err := toolsdomain.ValidateCitationV3ReceiptResults(
		facts.ValidationReceipt, facts.Candidate.ID, facts.Candidate.DocumentHash, candidate.Payload.CitationRefs,
	)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	for _, result := range validationResults {
		if !result.Valid || result.ReasonCode != "OK" {
			return workspaceAnalysisPublication{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis citations are not all valid"))
		}
	}
	trustedCitations, err := toolsdomain.ValidateCitationV3ReceiptCitations(
		facts.ValidationReceipt, facts.Candidate.ID, facts.Candidate.DocumentHash, candidate.Payload.CitationRefs,
	)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	citations := make([]agentdomain.Citation, len(trustedCitations))
	citationIDByRef := make(map[string]string, len(trustedCitations))
	for index, trusted := range trustedCitations {
		citation := agentdomain.Citation{
			ID: trusted.CitationID, WorkspaceID: workspaceID, IndexVersionID: trusted.IndexVersionID,
			ChunkID: trusted.ChunkID, SourceVersionID: trusted.SourceVersionID, SourceSpanID: trusted.SourceSpanID,
		}
		if err := citation.Validate(); err != nil {
			return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
		}
		citations[index] = citation
		citationIDByRef[trusted.EvidenceRef] = trusted.CitationID
	}
	var proposal *conversationdomain.WorkspaceAnalysisProposalSuggestion
	if source := candidate.Payload.ProposalSuggestion; source != nil {
		citationIDs := make([]string, len(source.CitationRefs))
		for index, reference := range source.CitationRefs {
			citationID, found := citationIDByRef[reference]
			if !found {
				return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis proposal references an unpublished citation"))
			}
			citationIDs[index] = citationID
		}
		proposal = &conversationdomain.WorkspaceAnalysisProposalSuggestion{
			Summary: source.Summary, CitationIDs: citationIDs, Href: conversationdomain.WorkspaceAnalysisProposalHref,
		}
	}
	var settledCost *int64
	if fence.SettledCostMicrounits != nil {
		value := *fence.SettledCostMicrounits
		settledCost = &value
	}
	result := conversationdomain.WorkspaceAnalysisAnswerResult{
		ResultType:    conversationdomain.AnswerResultWorkspaceAnalysis,
		SchemaID:      conversationdomain.WorkspaceAnalysisAnswerSchemaID,
		SchemaVersion: conversationdomain.WorkspaceAnalysisResultSchemaVersionV1,
		ModelRunRef:   facts.Candidate.SynthesisModelRunID,
		Payload: conversationdomain.WorkspaceAnalysisAnswerPayload{
			AnswerMarkdown: candidate.Payload.AnswerMarkdown,
			Citations:      citations,
			GitStatus: conversationdomain.WorkspaceAnalysisGitStatus{
				Branch: git.Branch, Head: git.Head, Clean: git.Clean, StagedCount: git.StagedCount,
				UnstagedCount: git.UnstagedCount, UntrackedCount: git.UntrackedCount, ConflictCount: git.ConflictCount,
			},
			Budget: conversationdomain.WorkspaceAnalysisBudgetSummary{
				ModelCalls: fence.SettledModelCalls, ToolCalls: fence.SettledToolCalls,
				InputTokens: fence.SettledInputTokens, OutputTokens: fence.SettledOutputTokens,
				EstimatedCostMicrounits: settledCost,
			},
			ProposalSuggestion: proposal,
			TerminationReason:  conversationdomain.WorkspaceAnalysisCompleted,
		},
	}
	document, err := json.Marshal(result)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(
		conversationdomain.AnswerPublicationCompleted, conversationdomain.AnswerResultWorkspaceAnalysis, document,
	)
	if err != nil || published.ModelRunID == nil || *published.ModelRunID != facts.Candidate.SynthesisModelRunID {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return workspaceAnalysisPublication{
		Status: conversationdomain.AnswerPublicationCompleted, ResultType: published.Type,
		ModelRunID: copyWorkspaceAnalysisFinalizerID(published.ModelRunID), Document: published.Document, ResultHash: published.Hash,
		RunStatus: agentdomain.WorkspaceAnalysisRunSucceeded, Reason: agentdomain.WorkspaceAnalysisRunCompleted,
		CitationCount: int64(len(citations)),
	}, nil
}

type workspaceAnalysisReceiptFailureRecord struct {
	ID           foundation.ID
	OperationID  foundation.ID
	Code         conversationapplication.WorkspaceAnalysisReceiptFailureCode
	ExpectedHash string
	ActualHash   *string
	CreatedAt    time.Time
}

func canonicalWorkspaceAnalysisClarificationPublication(
	plan agentdomain.WorkspaceAnalysisPlanResult,
) (workspaceAnalysisPublication, error) {
	result := conversationdomain.ClarificationResult{
		ResultType:    agentdomain.ResultTypeClarification,
		SchemaID:      conversationdomain.ClarificationSchemaID,
		SchemaVersion: conversationdomain.ClarificationSchemaVersionV1,
		ModelRunRef:   plan.ModelRunRef,
		Payload: conversationdomain.ClarificationPayload{
			Reason: plan.Payload.ClarificationReason, Question: plan.Payload.ClarificationQuestion,
			SuggestedScopes: append([]string(nil), plan.Payload.SuggestedScopes...),
		},
	}
	document, err := json.Marshal(result)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(
		conversationdomain.AnswerPublicationClarificationRequired, conversationdomain.AnswerResultClarification, document,
	)
	if err != nil || published.ModelRunID == nil || *published.ModelRunID != plan.ModelRunRef {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return workspaceAnalysisPublication{
		Status: conversationdomain.AnswerPublicationClarificationRequired, ResultType: published.Type,
		ModelRunID: copyWorkspaceAnalysisFinalizerID(published.ModelRunID), Document: published.Document, ResultHash: published.Hash,
		RunStatus: agentdomain.WorkspaceAnalysisRunClarificationRequired, Reason: agentdomain.WorkspaceAnalysisRunNeedsClarification,
	}, nil
}

func canonicalWorkspaceAnalysisTerminationPublication(
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
	modelRunID *foundation.ID,
) (workspaceAnalysisPublication, error) {
	status, resultType, runStatus, err := workspaceAnalysisTerminationMatrix(reason)
	if err != nil {
		return workspaceAnalysisPublication{}, err
	}
	var document json.RawMessage
	if status == conversationdomain.AnswerPublicationRefused {
		result := conversationdomain.WorkspaceAnalysisRefusalResult{
			ResultType:    conversationdomain.AnswerResultWorkspaceAnalysisRefusal,
			SchemaID:      conversationdomain.WorkspaceAnalysisRefusalSchemaID,
			SchemaVersion: conversationdomain.WorkspaceAnalysisResultSchemaVersionV1,
			ModelRunRef:   copyWorkspaceAnalysisFinalizerID(modelRunID),
			Payload: conversationdomain.WorkspaceAnalysisRefusalPayload{
				ReasonCode: conversationdomain.WorkspaceAnalysisRefusalReason(reason),
				Summary:    workspaceAnalysisTerminationSummary(reason),
			},
		}
		document, err = json.Marshal(result)
	} else {
		result := conversationdomain.WorkspaceAnalysisTerminationResult{
			ResultType:    conversationdomain.AnswerResultWorkspaceAnalysisTermination,
			SchemaID:      conversationdomain.WorkspaceAnalysisTerminationSchemaID,
			SchemaVersion: conversationdomain.WorkspaceAnalysisResultSchemaVersionV1,
			ModelRunRef:   copyWorkspaceAnalysisFinalizerID(modelRunID),
			Payload: conversationdomain.WorkspaceAnalysisTerminationPayload{
				TerminationReason: conversationdomain.WorkspaceAnalysisTerminationReason(reason),
				Summary:           workspaceAnalysisTerminationSummary(reason),
			},
		}
		document, err = json.Marshal(result)
	}
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(status, resultType, document)
	if err != nil || !equalOptionalWorkspaceAnalysisID(published.ModelRunID, modelRunID) {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return workspaceAnalysisPublication{
		Status: status, ResultType: published.Type, ModelRunID: copyWorkspaceAnalysisFinalizerID(published.ModelRunID),
		Document: published.Document, ResultHash: published.Hash, RunStatus: runStatus, Reason: reason,
	}, nil
}

func workspaceAnalysisTerminationMatrix(
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
) (conversationdomain.AnswerPublicationStatus, conversationdomain.AnswerResultType, agentdomain.WorkspaceAnalysisRunStatus, error) {
	switch reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, agentdomain.WorkspaceAnalysisRunCitationInvalid,
		agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, agentdomain.WorkspaceAnalysisRunModelRefused:
		return conversationdomain.AnswerPublicationRefused, conversationdomain.AnswerResultWorkspaceAnalysisRefusal,
			agentdomain.WorkspaceAnalysisRunRefused, nil
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted, agentdomain.WorkspaceAnalysisRunReceiptInvalid,
		agentdomain.WorkspaceAnalysisRunResultUnknown, agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
		agentdomain.WorkspaceAnalysisRunModelFailed, agentdomain.WorkspaceAnalysisRunToolFailed,
		agentdomain.WorkspaceAnalysisRunRuntimeFailed:
		return conversationdomain.WorkspaceAnalysisPublicationFailed, conversationdomain.AnswerResultWorkspaceAnalysisTermination,
			agentdomain.WorkspaceAnalysisRunFailed, nil
	case agentdomain.WorkspaceAnalysisRunCancellation:
		return conversationdomain.WorkspaceAnalysisPublicationCancelled, conversationdomain.AnswerResultWorkspaceAnalysisTermination,
			agentdomain.WorkspaceAnalysisRunCancelled, nil
	default:
		return "", "", "", invalid(conversationapplication.ErrorCodeWorkspaceAnalysisFinalizerInvalid, errors.New("workspace analysis termination reason is unsupported"))
	}
}

func workspaceAnalysisTerminationSummary(reason agentdomain.WorkspaceAnalysisRunTerminationReason) string {
	switch reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		return "Available workspace evidence was insufficient to produce a grounded answer."
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		return "The generated answer was not published because its citations could not be validated."
	case agentdomain.WorkspaceAnalysisRunFaithfulnessRejected:
		return "The generated answer was not published because its evidence review did not pass."
	case agentdomain.WorkspaceAnalysisRunModelRefused:
		return "The analysis could not produce a supported answer."
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted:
		return "The workspace analysis stopped because its execution budget was exhausted."
	case agentdomain.WorkspaceAnalysisRunReceiptInvalid:
		return "The workspace analysis stopped because a required result could not be verified."
	case agentdomain.WorkspaceAnalysisRunResultUnknown:
		return "The workspace analysis stopped because an external call result could not be determined safely."
	case agentdomain.WorkspaceAnalysisRunDeadlineExceeded:
		return "The workspace analysis stopped because its execution deadline was reached."
	case agentdomain.WorkspaceAnalysisRunModelFailed:
		return "The workspace analysis stopped because a model call failed."
	case agentdomain.WorkspaceAnalysisRunToolFailed:
		return "The workspace analysis stopped because a required tool call failed."
	case agentdomain.WorkspaceAnalysisRunRuntimeFailed:
		return "The workspace analysis stopped because its runtime could not complete safely."
	case agentdomain.WorkspaceAnalysisRunCancellation:
		return "The workspace analysis was cancelled."
	default:
		return "The workspace analysis stopped."
	}
}

type workspaceAnalysisDraftRecord struct {
	ID            foundation.ID
	NodeAttemptID foundation.ID
	Status        string
}

type workspaceAnalysisPublicationSlot struct {
	Answer                conversationdomain.Answer
	ConversationVersion   int64
	ConversationCreatedAt time.Time
	ConversationUpdatedAt time.Time
	LastActivityAt        time.Time
	Draft                 *workspaceAnalysisDraftRecord
}

func validateSuccessDraft(draft *workspaceAnalysisDraftRecord, candidate agentdomain.WorkspaceAnalysisCandidate) error {
	if draft == nil {
		return nil
	}
	if draft.NodeAttemptID != candidate.NodeAttemptID || (draft.Status != "COMPLETED" && draft.Status != "DEGRADED") {
		return conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("workspace analysis candidate draft binding differs"))
	}
	return nil
}

func workspaceAnalysisTerminalEventRequests(
	answer conversationdomain.Answer,
	analysisRunID foundation.ID,
	runStatus agentdomain.WorkspaceAnalysisRunStatus,
	citationCount int64,
	now time.Time,
) []eventsdomain.AppendRequest {
	answerID, conversationID, workflowRunID := answer.ID, answer.ConversationID, answer.WorkflowRunID
	questionID := answer.QuestionID
	eventType := "answer." + string(answer.PublicationStatus)
	summary := eventsdomain.PayloadSummary{
		ConversationID: &conversationID, WorkflowRunID: &workflowRunID, QuestionID: &questionID, AnswerID: &answerID,
		PublicationStatus: string(answer.PublicationStatus), ResultType: string(answer.ResultType), CitationCount: &citationCount,
	}
	if answer.ModelRunID != nil {
		modelRunID := *answer.ModelRunID
		summary.ModelRunID = &modelRunID
	}
	answerEvent := eventsdomain.AppendRequest{
		WorkspaceID: answer.WorkspaceID, ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
		Type: eventType, ResourceRef: "answer:" + string(answer.ID), ResourceVersion: answer.Version,
		PayloadSummary: summary, SchemaVersion: 1,
		SourceEventRef: eventType + ":" + string(answer.ID) + ":v2", OccurredAt: now,
	}
	analysisSummary := eventsdomain.PayloadSummary{
		ConversationID: &conversationID, WorkflowRunID: &workflowRunID, QuestionID: &questionID, AnswerID: &answerID,
		Status: string(runStatus), PublicationStatus: string(answer.PublicationStatus),
		ResultType: string(answer.ResultType), CitationCount: &citationCount,
	}
	analysisEvent := eventsdomain.AppendRequest{
		WorkspaceID: answer.WorkspaceID, ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
		Type: "workspace_analysis.terminated", ResourceRef: "workspace_analysis:" + string(analysisRunID),
		ResourceVersion: answer.Version, PayloadSummary: analysisSummary, SchemaVersion: 1,
		SourceEventRef: "workspace_analysis.terminated:" + string(analysisRunID) + ":v1", OccurredAt: now,
	}
	return []eventsdomain.AppendRequest{answerEvent, analysisEvent}
}

func workspaceAnalysisPublicationOutput(
	answer conversationdomain.Answer,
	proofID foundation.ID,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, error) {
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	output := conversationworkflow.WorkspaceAnalysisPublicationOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		AnswerID:      answer.ID, PublicationStatus: answer.PublicationStatus, ResultType: answer.ResultType,
		ModelRunID: copyWorkspaceAnalysisFinalizerID(answer.ModelRunID), ResultHash: answer.ResultHash, ProofID: proofID,
	}
	if _, err := conversationworkflow.EncodeWorkspaceAnalysisPublicationOutput(output); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return output, nil
}

type workspaceAnalysisSuccessProofRecord struct {
	ID                    foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	CandidateID           foundation.ID
	CandidateHash         string
	GitReceiptID          foundation.ID
	GitReceiptHash        string
	ValidationReceiptID   foundation.ID
	ValidationReceiptHash string
	ReviewResultID        foundation.ID
	ReviewResultHash      string
	PublishedDocument     json.RawMessage
	PublishedResultHash   string
}

type workspaceAnalysisTerminationProofRecord struct {
	ID                    foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	Reason                agentdomain.WorkspaceAnalysisRunTerminationReason
	OperationID           *foundation.ID
	ArtifactKind          *conversationapplication.WorkspaceAnalysisTerminationArtifactKind
	ArtifactID            *foundation.ID
	ArtifactHash          *string
	RequestedModelCalls   *int
	RequestedToolCalls    *int
	RequestedSourceReads  *int
	RequestedInputTokens  *int64
	RequestedOutputTokens *int64
	RequestedCost         *int64
	FailureCode           *conversationapplication.WorkspaceAnalysisReceiptFailureCode
	FailureID             *foundation.ID
	ExpectedHash          *string
	ActualHash            *string
	PublishedModelRunID   *foundation.ID
	PublishedDocument     json.RawMessage
	PublishedResultHash   string
}

type workspaceAnalysisProofState struct {
	Answer      conversationdomain.Answer
	RunStatus   agentdomain.WorkspaceAnalysisRunStatus
	RunReason   *agentdomain.WorkspaceAnalysisRunTerminationReason
	Success     *workspaceAnalysisSuccessProofRecord
	Termination *workspaceAnalysisTerminationProofRecord
}

func workspaceAnalysisOutputFromState(
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
	state workspaceAnalysisProofState,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	answer := state.Answer
	if answer.PublicationStatus == conversationdomain.AnswerPublicationPending {
		if state.Success != nil || state.Termination != nil || !state.RunStatus.Active() || state.RunReason != nil {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
				consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("pending workspace analysis publication is split"))
		}
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, nil
	}
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	if state.Success != nil && state.Termination != nil {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis answer has two publication proofs"))
	}
	var proofID foundation.ID
	if state.Success != nil {
		proof := state.Success
		if proof.NodeRunID != lookup.NodeRunID || answer.PublicationStatus != conversationdomain.AnswerPublicationCompleted ||
			answer.ResultType != conversationdomain.AnswerResultWorkspaceAnalysis || state.RunStatus != agentdomain.WorkspaceAnalysisRunSucceeded ||
			state.RunReason == nil || *state.RunReason != agentdomain.WorkspaceAnalysisRunCompleted ||
			answer.ResultHash != proof.PublishedResultHash || !bytes.Equal(answer.Result, proof.PublishedDocument) {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
				consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis success proof is split"))
		}
		proofID = proof.ID
	} else if state.Termination != nil {
		proof := state.Termination
		status, resultType, runStatus, matrixErr := workspaceAnalysisTerminationMatrix(proof.Reason)
		if proof.Reason == agentdomain.WorkspaceAnalysisRunNeedsClarification {
			status, resultType, runStatus = conversationdomain.AnswerPublicationClarificationRequired,
				conversationdomain.AnswerResultClarification, agentdomain.WorkspaceAnalysisRunClarificationRequired
			matrixErr = nil
		}
		if matrixErr != nil || proof.NodeRunID != lookup.NodeRunID || answer.PublicationStatus != status || answer.ResultType != resultType ||
			state.RunStatus != runStatus || state.RunReason == nil || *state.RunReason != proof.Reason ||
			answer.ResultHash != proof.PublishedResultHash || !bytes.Equal(answer.Result, proof.PublishedDocument) ||
			!equalOptionalWorkspaceAnalysisID(answer.ModelRunID, proof.PublishedModelRunID) {
			return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
				consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis termination proof is split"))
		}
		proofID = proof.ID
	} else {
		return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false,
			consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("terminal workspace analysis answer has no publication proof"))
	}
	output, err := workspaceAnalysisPublicationOutput(answer, proofID)
	return output, err == nil, err
}

func workspaceAnalysisTerminalCitationCount(answer conversationdomain.Answer) (int64, error) {
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(
		answer.PublicationStatus, answer.ResultType, answer.Result,
	)
	if err != nil {
		return 0, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	if published.Type != conversationdomain.AnswerResultWorkspaceAnalysis {
		return 0, nil
	}
	var result conversationdomain.WorkspaceAnalysisAnswerResult
	if err := json.Unmarshal(published.Document, &result); err != nil || result.Validate() != nil {
		return 0, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, errors.New("workspace analysis replay citation projection is invalid"))
	}
	return int64(len(result.Payload.Citations)), nil
}

func workspaceAnalysisTerminationArtifactMatchesProof(
	artifact *conversationapplication.WorkspaceAnalysisTerminationArtifact,
	proof *workspaceAnalysisTerminationProofRecord,
) bool {
	if artifact == nil {
		return proof.ArtifactKind == nil && proof.ArtifactID == nil && proof.ArtifactHash == nil
	}
	return proof.ArtifactKind != nil && *proof.ArtifactKind == artifact.Kind && proof.ArtifactID != nil && *proof.ArtifactID == artifact.ID &&
		proof.ArtifactHash != nil && *proof.ArtifactHash == artifact.Hash
}

func workspaceAnalysisBudgetRequestMatchesProof(
	request *conversationapplication.WorkspaceAnalysisBudgetRequest,
	proof *workspaceAnalysisTerminationProofRecord,
) bool {
	if request == nil {
		return proof.RequestedModelCalls == nil && proof.RequestedToolCalls == nil && proof.RequestedSourceReads == nil &&
			proof.RequestedInputTokens == nil && proof.RequestedOutputTokens == nil && proof.RequestedCost == nil
	}
	return proof.RequestedModelCalls != nil && *proof.RequestedModelCalls == request.ModelCalls &&
		proof.RequestedToolCalls != nil && *proof.RequestedToolCalls == request.ToolCalls &&
		proof.RequestedSourceReads != nil && *proof.RequestedSourceReads == request.SourceReads &&
		proof.RequestedInputTokens != nil && *proof.RequestedInputTokens == request.InputTokens &&
		proof.RequestedOutputTokens != nil && *proof.RequestedOutputTokens == request.OutputTokens &&
		request.RequestedCostMicrounits == nil && proof.RequestedCost == nil
}

func workspaceAnalysisReceiptFailureMatchesProof(
	failure *conversationapplication.WorkspaceAnalysisReceiptFailure,
	proof *workspaceAnalysisTerminationProofRecord,
) bool {
	if failure == nil {
		return proof.FailureCode == nil && proof.FailureID == nil && proof.ExpectedHash == nil && proof.ActualHash == nil
	}
	return proof.FailureCode != nil && *proof.FailureCode == failure.Code && proof.FailureID != nil && *proof.FailureID == failure.ID &&
		proof.ExpectedHash != nil && *proof.ExpectedHash == failure.ExpectedHash && equalOptionalWorkspaceAnalysisString(proof.ActualHash, failure.ActualHash)
}

func cloneWorkspaceAnalysisTerminationCommand(
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand {
	if command.OperationID != nil {
		value := *command.OperationID
		command.OperationID = &value
	}
	if command.Artifact != nil {
		value := *command.Artifact
		command.Artifact = &value
	}
	if command.BudgetRequest != nil {
		value := *command.BudgetRequest
		if value.RequestedCostMicrounits != nil {
			cost := *value.RequestedCostMicrounits
			value.RequestedCostMicrounits = &cost
		}
		command.BudgetRequest = &value
	}
	if command.ReceiptFailure != nil {
		value := *command.ReceiptFailure
		if value.ActualHash != nil {
			hash := *value.ActualHash
			value.ActualHash = &hash
		}
		command.ReceiptFailure = &value
	}
	return command
}

func parseWorkspaceAnalysisIDs(values ...string) ([]foundation.ID, error) {
	parsed := make([]foundation.ID, len(values))
	seen := make(map[foundation.ID]struct{}, len(values))
	for index, value := range values {
		id, err := parseCanonicalID(value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errors.New("workspace analysis persisted identity is reused")
		}
		seen[id] = struct{}{}
		parsed[index] = id
	}
	return parsed, nil
}

func workspaceAnalysisDocumentHash(document []byte) string {
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}

func laterWorkspaceAnalysisTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func copyWorkspaceAnalysisFinalizerID(source *foundation.ID) *foundation.ID {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func equalOptionalWorkspaceAnalysisID(left, right *foundation.ID) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func equalOptionalWorkspaceAnalysisString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func workspaceAnalysisFinalizerQueryError(err error, fact string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, fmt.Errorf("%s is missing", fact))
	}
	return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
}
