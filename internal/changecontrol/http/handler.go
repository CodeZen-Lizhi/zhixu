// Package changecontrolhttp exposes Proposal、Approval 与 Apply 前置校验 HTTP 契约。
package changecontrolhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

// Service 定义 Change Control HTTP 所需的最小应用层契约。
type Service interface {
	CreateProposal(context.Context, application.CreateCommand) (application.CreateResult, error)
	GetProposal(context.Context, foundation.ID) (domain.Proposal, error)
	DecideProposalWithDispatch(context.Context, foundation.ID, foundation.ID, string, domain.Decision) (application.ApprovalDecisionResult, error)
	CheckApplyPreflight(context.Context, foundation.ID, foundation.ID, string) (application.ApplyPreflightResult, error)
}

type listService interface {
	ListProposals(context.Context, domain.ProposalListQuery) ([]domain.ProposalListItem, bool, error)
}

type currentContentService interface {
	GetProposalCurrentContent(context.Context, foundation.ID) (application.ProposalCurrentContent, error)
}

// Handler 只负责协议解析和响应映射，不执行文件或 Git 写回。
type Handler struct{ service Service }

// NewHandler 创建 Change Control HTTP Handler。
func NewHandler(service Service) *Handler { return &Handler{service: service} }

// Routes 注册 Proposal、Approval 与 Apply preflight 路由。
func (h *Handler) Routes(router chi.Router) {
	router.Post("/workspaces/{workspaceID}/proposals", h.createProposal)
	router.Get("/workspaces/{workspaceID}/proposals", h.listProposals)
	router.Get("/proposals/{proposalID}", h.getProposal)
	router.Get("/proposals/{proposalID}/current-content", h.getProposalCurrentContent)
	router.Post("/proposals/{proposalID}/approvals", h.decideProposal)
	router.Post("/proposals/{proposalID}/apply-preflight", h.applyPreflight)
}

type proposalCurrentContentResponse struct {
	ProposalID    string `json:"proposal_id"`
	WorkspaceID   string `json:"workspace_id"`
	TargetPath    string `json:"target_path"`
	Content       string `json:"content"`
	CurrentHash   string `json:"current_hash"`
	BaseHash      string `json:"base_hash"`
	BaseHashMatch bool   `json:"base_hash_match"`
}

func (h *Handler) getProposalCurrentContent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	proposalID, err := foundation.ParseID(chi.URLParam(r, "proposalID"))
	if err != nil {
		writeError(w, err)
		return
	}
	service, ok := h.service.(currentContentService)
	if !ok {
		writeUnavailable(w)
		return
	}
	result, err := service.GetProposalCurrentContent(r.Context(), proposalID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, proposalCurrentContentResponse{
		ProposalID: string(result.ProposalID), WorkspaceID: string(result.WorkspaceID), TargetPath: result.TargetPath, Content: result.Content,
		CurrentHash: result.CurrentHash, BaseHash: result.BaseHash, BaseHashMatch: result.BaseHashMatch,
	})
}

type proposalPageResponse struct {
	Items      []proposalListResponse `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

const (
	proposalCursorVersion = 1
	proposalCursorKind    = "proposal_list"
)

type proposalListCursor struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	At          string `json:"at"`
	ID          string `json:"id"`
	Filter      string `json:"filter"`
}

type proposalListResponse struct {
	ID           string                    `json:"id"`
	WorkspaceID  string                    `json:"workspace_id"`
	ProposalType string                    `json:"proposal_type"`
	Status       string                    `json:"status"`
	Target       string                    `json:"target"`
	RiskLevel    string                    `json:"risk_level"`
	Risk         string                    `json:"risk"`
	RevisionID   string                    `json:"revision_id"`
	ChangeHash   string                    `json:"change_hash"`
	Approval     *approvalSnapshotResponse `json:"approval,omitempty"`
	CreatedAt    string                    `json:"created_at"`
	UpdatedAt    string                    `json:"updated_at"`
}

func (h *Handler) listProposals(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(listService)
	if !ok {
		writeUnavailable(w)
		return
	}
	workspaceID, err := foundation.ParseID(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeError(w, err)
		return
	}
	allowed := map[string]struct{}{"cursor": {}, "limit": {}, "status": {}, "proposal_type": {}, "risk": {}, "created_after": {}}
	for key, values := range r.URL.Query() {
		if _, ok := allowed[key]; !ok {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_FILTER_INVALID", false, fmt.Errorf("unknown query parameter %q", key)))
			return
		}
		if len(values) != 1 {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_FILTER_INVALID", false, fmt.Errorf("query parameter %q must appear once", key)))
			return
		}
	}
	status := domain.ProposalStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	proposalType := domain.ProposalType(strings.TrimSpace(r.URL.Query().Get("proposal_type")))
	risk := r.URL.Query().Get("risk")
	if status != "" && !validProposalListStatus(status) || proposalType != "" && proposalType != domain.ProposalTypeFilePatch && proposalType != domain.ProposalTypeKnowledgeChange || len(risk) > 64 || strings.ContainsAny(risk, "\r\n\t") {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_FILTER_INVALID", false, errors.New("proposal list filter is invalid")))
		return
	}
	riskLevel := domain.ProposalRiskLevel("")
	if risk != "" {
		resolved, resolveErr := domain.ParseProposalRiskLevel(domain.ProposalRiskLevel(risk))
		if resolveErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_FILTER_INVALID", false, resolveErr))
			return
		}
		riskLevel = resolved
	}
	var createdAfter *time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("created_after")); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_FILTER_INVALID", false, parseErr))
			return
		}
		parsed = parsed.UTC()
		createdAfter = &parsed
	}
	filterJSON, _ := json.Marshal(struct {
		Status       domain.ProposalStatus `json:"status,omitempty"`
		ProposalType domain.ProposalType   `json:"proposal_type,omitempty"`
		Risk         string                `json:"risk,omitempty"`
		CreatedAfter string                `json:"created_after,omitempty"`
	}{Status: status, ProposalType: proposalType, Risk: risk, CreatedAfter: r.URL.Query().Get("created_after")})
	limit := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, scanErr := strconv.Atoi(raw)
		if scanErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_LIMIT_INVALID", false, scanErr))
			return
		}
		limit = parsed
	}
	if limit < 1 || limit > 100 {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_LIMIT_INVALID", false, errors.New("limit must be between 1 and 100")))
		return
	}
	var cursorTime *time.Time
	var cursorID foundation.ID
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		if len(raw) > 2048 {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURSOR_INVALID", false, errors.New("cursor is too long")))
			return
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(raw)
		if decodeErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURSOR_INVALID", false, decodeErr))
			return
		}
		var cursor proposalListCursor
		decoder := json.NewDecoder(strings.NewReader(string(decoded)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Version != proposalCursorVersion || cursor.Kind != proposalCursorKind || cursor.At == "" || cursor.WorkspaceID != string(workspaceID) || cursor.Filter != string(filterJSON) {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURSOR_INVALID", false, errors.New("cursor payload is invalid")))
			return
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, cursor.At)
		if parseErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURSOR_INVALID", false, parseErr))
			return
		}
		id, idErr := foundation.ParseID(cursor.ID)
		if idErr != nil {
			writeError(w, idErr)
			return
		}
		cursorTime, cursorID = &parsed, id
	}
	items, hasMore, err := service.ListProposals(r.Context(), domain.ProposalListQuery{WorkspaceID: workspaceID, Status: status, Type: proposalType, RiskLevel: riskLevel, CreatedAfter: createdAfter, CursorTime: cursorTime, CursorID: cursorID, Limit: limit})
	if err != nil {
		writeError(w, err)
		return
	}
	response := proposalPageResponse{Items: make([]proposalListResponse, len(items))}
	for index, item := range items {
		mapped, mapErr := toProposalListResponse(item)
		if mapErr != nil {
			writeError(w, mapErr)
			return
		}
		response.Items[index] = mapped
	}
	if hasMore && len(items) > 0 {
		tail := items[len(items)-1]
		payload, _ := json.Marshal(proposalListCursor{Version: proposalCursorVersion, Kind: proposalCursorKind, WorkspaceID: string(workspaceID), At: tail.UpdatedAt.UTC().Format(time.RFC3339Nano), ID: string(tail.ProposalID), Filter: string(filterJSON)})
		response.NextCursor = base64.RawURLEncoding.EncodeToString(payload)
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

func validProposalListStatus(status domain.ProposalStatus) bool {
	switch status {
	case domain.StatusDraft, domain.StatusValidating, domain.StatusReady, domain.StatusApproved, domain.StatusApplying, domain.StatusApplied, domain.StatusVerifying, domain.StatusCompleted, domain.StatusRejected, domain.StatusNeedsRevision, domain.StatusDeferred, domain.StatusApplyFailed, domain.StatusVerifyFailed, domain.StatusRolledBack, domain.StatusCancelled:
		return true
	default:
		return false
	}
}

type createProposalRequest struct {
	TargetPath      string                   `json:"target_path"`
	BaseHash        string                   `json:"base_hash"`
	Content         string                   `json:"content"`
	EvidenceSummary string                   `json:"evidence_summary"`
	RiskLevel       domain.ProposalRiskLevel `json:"risk_level"`
	Risk            string                   `json:"risk"`
	RollbackPlan    string                   `json:"rollback_plan"`
}

type decisionRequest struct {
	RevisionID string          `json:"revision_id"`
	ChangeHash string          `json:"change_hash"`
	Decision   domain.Decision `json:"decision"`
}

type applyPreflightRequest struct {
	RevisionID         string `json:"revision_id"`
	ApprovedChangeHash string `json:"approved_change_hash"`
}

type proposalResponse struct {
	ProposalType string                    `json:"proposal_type"`
	ID           string                    `json:"id"`
	WorkspaceID  string                    `json:"workspace_id"`
	TargetPath   string                    `json:"target_path,omitempty"`
	Status       string                    `json:"status"`
	RiskLevel    string                    `json:"risk_level"`
	Revision     any                       `json:"revision"`
	Approval     *approvalSnapshotResponse `json:"approval"`
	CreatedAt    string                    `json:"created_at"`
	UpdatedAt    string                    `json:"updated_at"`
}

type revisionResponse struct {
	ID              string `json:"id"`
	RevisionNo      int    `json:"revision_no"`
	BaseHash        string `json:"base_hash"`
	Content         string `json:"content"`
	EvidenceSummary string `json:"evidence_summary"`
	Risk            string `json:"risk"`
	RollbackPlan    string `json:"rollback_plan"`
	ChangeHash      string `json:"change_hash"`
	CreatedAt       string `json:"created_at"`
}

type knowledgeTargetRefResponse struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

type knowledgeBaseVersionResponse struct {
	NodeType string `json:"node_type"`
	NodeID   string `json:"node_id"`
	Version  int64  `json:"version"`
}

type knowledgeChangeEndpointResponse struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

type knowledgeChangeSetResponse struct {
	Operation    string                          `json:"operation"`
	Source       knowledgeChangeEndpointResponse `json:"source"`
	Target       knowledgeChangeEndpointResponse `json:"target"`
	RelationType string                          `json:"relation_type"`
}

type knowledgeEvidenceRefResponse struct {
	CandidateEvidenceID string `json:"candidate_evidence_id"`
	SemanticHash        string `json:"semantic_hash"`
}

type knowledgeRevisionResponse struct {
	ID            string                         `json:"id"`
	RevisionNo    int                            `json:"revision_no"`
	SchemaVersion string                         `json:"schema_version"`
	TargetRefs    []knowledgeTargetRefResponse   `json:"target_refs"`
	BaseVersions  []knowledgeBaseVersionResponse `json:"base_versions"`
	ChangeSet     knowledgeChangeSetResponse     `json:"change_set"`
	EvidenceRefs  []knowledgeEvidenceRefResponse `json:"evidence_refs"`
	Risk          string                         `json:"risk"`
	RollbackPlan  string                         `json:"rollback_plan"`
	ChangeHash    string                         `json:"change_hash"`
	CreatedAt     string                         `json:"created_at"`
}

type approvalSnapshotResponse struct {
	ID                string  `json:"id"`
	ProposalID        string  `json:"proposal_id"`
	RevisionID        string  `json:"revision_id"`
	ChangeHash        string  `json:"change_hash"`
	Decision          string  `json:"decision"`
	ApprovedGitHead   *string `json:"approved_git_head,omitempty"`
	WorkflowRunID     string  `json:"workflow_run_id,omitempty"`
	WorkflowStatusURL string  `json:"workflow_status_url,omitempty"`
	DecidedAt         string  `json:"decided_at"`
}

type approvalDecisionResponse struct {
	approvalSnapshotResponse
	DispatchStatus string `json:"dispatch_status,omitempty"`
}

type applyPreflightResponse struct {
	ProposalID      string `json:"proposal_id"`
	RevisionID      string `json:"revision_id"`
	ChangeHash      string `json:"change_hash"`
	BaseHash        string `json:"base_hash"`
	PreflightPassed bool   `json:"preflight_passed"`
	Mode            string `json:"mode"`
	WritePerformed  bool   `json:"write_performed"`
}

func (h *Handler) createProposal(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := foundation.ParseID(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeError(w, err)
		return
	}
	var request createProposalRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeUnavailable(w)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "IDEMPOTENCY_KEY_REQUIRED", false, errors.New("proposal creation requires an Idempotency-Key header")))
		return
	}
	riskLevel, err := domain.ParseProposalRiskLevel(request.RiskLevel)
	if err != nil {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_RISK_LEVEL_INVALID", false, err))
		return
	}
	result, err := h.service.CreateProposal(r.Context(), application.CreateCommand{
		WorkspaceID: workspaceID, TargetPath: request.TargetPath, BaseHash: request.BaseHash, Content: request.Content,
		IdempotencyKey: idempotencyKey, RiskLevel: riskLevel,
		EvidenceSummary: request.EvidenceSummary, Risk: request.Risk, RollbackPlan: request.RollbackPlan,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	response, mapErr := toProposalResponse(result.Proposal)
	if mapErr != nil {
		writeError(w, mapErr)
		return
	}
	httpapi.WriteJSON(w, status, response)
}

func (h *Handler) getProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, err := foundation.ParseID(chi.URLParam(r, "proposalID"))
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeUnavailable(w)
		return
	}
	proposal, err := h.service.GetProposal(r.Context(), proposalID)
	if err != nil {
		writeError(w, err)
		return
	}
	response, mapErr := toProposalResponse(proposal)
	if mapErr != nil {
		writeError(w, mapErr)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) decideProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, err := foundation.ParseID(chi.URLParam(r, "proposalID"))
	if err != nil {
		writeError(w, err)
		return
	}
	var request decisionRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	revisionID, err := foundation.ParseID(request.RevisionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeUnavailable(w)
		return
	}
	result, err := h.service.DecideProposalWithDispatch(r.Context(), proposalID, revisionID, request.ChangeHash, request.Decision)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	response := approvalDecisionResponse{approvalSnapshotResponse: toApprovalDecisionSnapshotResponse(result.Approval)}
	if result.Approval.Decision == domain.DecisionApproved && result.Workflow != nil {
		response.WorkflowRunID = string(result.Workflow.RunID)
		response.WorkflowStatusURL = "/api/v1/workflows/" + string(result.Workflow.RunID)
		response.DispatchStatus = string(result.Workflow.Status)
	}
	httpapi.WriteJSON(w, status, response)
}

func (h *Handler) applyPreflight(w http.ResponseWriter, r *http.Request) {
	proposalID, err := foundation.ParseID(chi.URLParam(r, "proposalID"))
	if err != nil {
		writeError(w, err)
		return
	}
	var request applyPreflightRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	revisionID, err := foundation.ParseID(request.RevisionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeUnavailable(w)
		return
	}
	result, err := h.service.CheckApplyPreflight(r.Context(), proposalID, revisionID, request.ApprovedChangeHash)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, applyPreflightResponse{
		ProposalID: string(result.ProposalID), RevisionID: string(result.RevisionID), ChangeHash: result.ChangeHash,
		BaseHash: result.BaseHash, PreflightPassed: true, Mode: "preflight_only", WritePerformed: false,
	})
}

func toProposalResponse(proposal domain.Proposal) (proposalResponse, error) {
	proposalType := domain.NormalizeProposalType(proposal.Type)
	riskLevel, err := responseRiskLevel(proposalType, proposal.RiskLevel)
	if err != nil {
		return proposalResponse{}, err
	}
	response := proposalResponse{
		ProposalType: string(proposalType), ID: string(proposal.ID), WorkspaceID: string(proposal.WorkspaceID), TargetPath: proposal.TargetPath,
		Status: string(proposal.Status), RiskLevel: string(riskLevel), CreatedAt: proposal.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: proposal.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if proposalType == domain.ProposalTypeKnowledgeChange {
		response.TargetPath = ""
		response.Revision = toKnowledgeRevisionResponse(proposal.Revision)
	} else {
		response.Revision = revisionResponse{
			ID: string(proposal.Revision.ID), RevisionNo: proposal.Revision.RevisionNo, BaseHash: proposal.Revision.BaseHash,
			Content: proposal.Revision.Content, EvidenceSummary: proposal.Revision.EvidenceSummary, Risk: proposal.Revision.Risk,
			RollbackPlan: proposal.Revision.RollbackPlan, ChangeHash: proposal.Revision.ChangeHash,
			CreatedAt: proposal.Revision.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	if proposal.Approval != nil {
		approval := toApprovalSnapshotResponse(*proposal.Approval, proposal.Type, proposal.WorkflowRunID)
		response.Approval = &approval
	}
	return response, nil
}

func toKnowledgeRevisionResponse(revision domain.Revision) knowledgeRevisionResponse {
	response := knowledgeRevisionResponse{
		ID: string(revision.ID), RevisionNo: revision.RevisionNo, Risk: revision.Risk,
		RollbackPlan: revision.RollbackPlan, ChangeHash: revision.ChangeHash,
		CreatedAt: revision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if revision.KnowledgeChange == nil {
		return response
	}
	change := revision.KnowledgeChange
	response.SchemaVersion = change.SchemaVersion
	response.TargetRefs = make([]knowledgeTargetRefResponse, len(change.TargetRefs))
	for index, item := range change.TargetRefs {
		response.TargetRefs[index] = knowledgeTargetRefResponse{Type: string(item.Type), ID: string(item.ID), Fingerprint: item.Fingerprint}
	}
	response.BaseVersions = make([]knowledgeBaseVersionResponse, len(change.BaseVersions))
	versions := make(map[string]int64, len(change.BaseVersions))
	for index, item := range change.BaseVersions {
		response.BaseVersions[index] = knowledgeBaseVersionResponse{NodeType: string(item.NodeType), NodeID: string(item.NodeID), Version: item.Version}
		versions[string(item.NodeType)+":"+string(item.NodeID)] = item.Version
	}
	response.ChangeSet = knowledgeChangeSetResponse{
		Operation: string(change.ChangeSet.Operation),
		Source: knowledgeChangeEndpointResponse{
			Type: string(change.ChangeSet.Source.Type), ID: string(change.ChangeSet.Source.ID),
			Version: versions[string(change.ChangeSet.Source.Type)+":"+string(change.ChangeSet.Source.ID)],
		},
		Target: knowledgeChangeEndpointResponse{
			Type: string(change.ChangeSet.Target.Type), ID: string(change.ChangeSet.Target.ID),
			Version: versions[string(change.ChangeSet.Target.Type)+":"+string(change.ChangeSet.Target.ID)],
		},
		RelationType: string(change.ChangeSet.RelationType),
	}
	response.EvidenceRefs = make([]knowledgeEvidenceRefResponse, len(change.EvidenceRefs))
	for index, item := range change.EvidenceRefs {
		response.EvidenceRefs[index] = knowledgeEvidenceRefResponse{CandidateEvidenceID: string(item.CandidateEvidenceID), SemanticHash: item.SemanticHash}
	}
	return response
}

func toProposalListResponse(item domain.ProposalListItem) (proposalListResponse, error) {
	riskLevel, err := responseRiskLevel(item.Type, item.RiskLevel)
	if err != nil {
		return proposalListResponse{}, err
	}
	response := proposalListResponse{
		ID: string(item.ProposalID), WorkspaceID: string(item.WorkspaceID), ProposalType: string(item.Type),
		Status: string(item.Status), Target: item.Target, RiskLevel: string(riskLevel), Risk: item.Risk, RevisionID: string(item.RevisionID),
		ChangeHash: item.ChangeHash, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if item.Approval != nil {
		approval := toApprovalSnapshotResponse(*item.Approval, item.Type, item.WorkflowRunID)
		response.Approval = &approval
	}
	return response, nil
}

// responseRiskLevel 按 Proposal 类型严格校验持久风险等级，损坏数据不得进入公共 API。
func responseRiskLevel(proposalType domain.ProposalType, level domain.ProposalRiskLevel) (domain.ProposalRiskLevel, error) {
	resolved, err := domain.ValidateProposalRiskLevelForType(proposalType, level)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_RISK_LEVEL_INVALID", false, err)
	}
	return resolved, nil
}

func toApprovalDecisionSnapshotResponse(approval domain.Approval) approvalSnapshotResponse {
	response := approvalSnapshotResponse{ID: string(approval.ID), ProposalID: string(approval.ProposalID), RevisionID: string(approval.RevisionID), ChangeHash: approval.ChangeHash, Decision: string(approval.Decision), DecidedAt: approval.DecidedAt.UTC().Format(time.RFC3339Nano)}
	if approval.Decision == domain.DecisionApproved && approval.ApprovedGitHead != nil {
		head := strings.ToLower(*approval.ApprovedGitHead)
		response.ApprovedGitHead = &head
	}
	return response
}

func toApprovalSnapshotResponse(approval domain.Approval, proposalType domain.ProposalType, workflowRunID *foundation.ID) approvalSnapshotResponse {
	response := toApprovalDecisionSnapshotResponse(approval)
	if approval.Decision != domain.DecisionApproved || domain.NormalizeProposalType(proposalType) != domain.ProposalTypeFilePatch {
		response.ApprovedGitHead = nil
		return response
	}
	if workflowRunID != nil {
		response.WorkflowRunID = string(*workflowRunID)
		response.WorkflowStatusURL = "/api/v1/workflows/" + string(*workflowRunID)
	}
	return response
}

func decodeJSON(r *http.Request, target any) error {
	if err := httpapi.DecodeJSON(r, target); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return nil
}

func writeUnavailable(w http.ResponseWriter) {
	httpapi.WriteProblem(w, http.StatusServiceUnavailable, "CHANGE_CONTROL_SERVICE_UNAVAILABLE", "Change Control 服务暂不可用", true, nil)
}

func writeError(w http.ResponseWriter, err error) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务处理失败", false, nil)
		return
	}
	status := http.StatusInternalServerError
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		status = http.StatusBadRequest
	case foundation.ErrorNotFound:
		status = http.StatusNotFound
	case foundation.ErrorVersionConflict, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		status = http.StatusConflict
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		status = http.StatusServiceUnavailable
	case foundation.ErrorPermissionDenied:
		status = http.StatusForbidden
	}
	details := map[string]any(nil)
	var conflict *application.HashConflict
	if errors.As(err, &conflict) {
		details = map[string]any{
			"expected_version":   conflict.Expected,
			"current_version":    conflict.Current,
			"conflict_type":      "target_base_hash_changed",
			"resolution_actions": []string{"create_new_proposal_revision"},
		}
	}
	message := publicMessage(classified.Code)
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, details)
}

func publicMessage(code string) string {
	switch strings.TrimSpace(code) {
	case "PROPOSAL_NOT_APPROVED":
		return "Proposal 尚未获得有效批准"
	case "CHANGE_HASH_MISMATCH":
		return "审批绑定的 Change Hash 与 Revision 不一致"
	case "TARGET_BASE_HASH_CONFLICT":
		return "目标内容在 Proposal 创建后发生变化"
	case "TARGET_BASE_UNAVAILABLE":
		return "Proposal 目标文件已不存在或不再安全可读"
	case "IDEMPOTENCY_KEY_REQUIRED":
		return "创建 Proposal 必须提供 Idempotency-Key"
	case "IDEMPOTENCY_KEY_REUSED":
		return "Idempotency-Key 已绑定到不同的 Proposal 请求"
	case "PROPOSAL_REVISION_CONFLICT":
		return "审批未绑定到请求的 Proposal Revision"
	case "PROPOSAL_NOT_FOUND", "PROPOSAL_REVISION_NOT_FOUND":
		return "请求的 Proposal 或 Revision 不存在"
	default:
		return "请求未完成：" + code
	}
}

var _ Service = (*application.Service)(nil)
