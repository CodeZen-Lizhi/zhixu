// Package changecontrolhttp exposes Proposal、Approval 与 Apply 前置校验 HTTP 契约。
package changecontrolhttp

import (
	"context"
	"errors"
	"net/http"
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
	CreateProposal(context.Context, application.CreateCommand) (domain.Proposal, error)
	GetProposal(context.Context, foundation.ID) (domain.Proposal, error)
	DecideProposal(context.Context, foundation.ID, foundation.ID, string, domain.Decision) (domain.Approval, error)
	CheckApplyPreflight(context.Context, foundation.ID, foundation.ID, string) (application.ApplyPreflightResult, error)
}

// Handler 只负责协议解析和响应映射，不执行文件或 Git 写回。
type Handler struct{ service Service }

// NewHandler 创建 Change Control HTTP Handler。
func NewHandler(service Service) *Handler { return &Handler{service: service} }

// Routes 注册 Proposal、Approval 与 Apply preflight 路由。
func (h *Handler) Routes(router chi.Router) {
	router.Post("/workspaces/{workspaceID}/proposals", h.createProposal)
	router.Get("/proposals/{proposalID}", h.getProposal)
	router.Post("/proposals/{proposalID}/approvals", h.decideProposal)
	router.Post("/proposals/{proposalID}/apply-preflight", h.applyPreflight)
}

type createProposalRequest struct {
	TargetPath      string `json:"target_path"`
	BaseHash        string `json:"base_hash"`
	Content         string `json:"content"`
	EvidenceSummary string `json:"evidence_summary"`
	Risk            string `json:"risk"`
	RollbackPlan    string `json:"rollback_plan"`
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
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspace_id"`
	TargetPath  string            `json:"target_path"`
	Status      string            `json:"status"`
	Revision    revisionResponse  `json:"revision"`
	Approval    *approvalResponse `json:"approval,omitempty"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
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

type approvalResponse struct {
	ID         string `json:"id"`
	ProposalID string `json:"proposal_id"`
	RevisionID string `json:"revision_id"`
	ChangeHash string `json:"change_hash"`
	Decision   string `json:"decision"`
	DecidedAt  string `json:"decided_at"`
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
	proposal, err := h.service.CreateProposal(r.Context(), application.CreateCommand{
		WorkspaceID: workspaceID, TargetPath: request.TargetPath, BaseHash: request.BaseHash, Content: request.Content,
		EvidenceSummary: request.EvidenceSummary, Risk: request.Risk, RollbackPlan: request.RollbackPlan,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, toProposalResponse(proposal))
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
	httpapi.WriteJSON(w, http.StatusOK, toProposalResponse(proposal))
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
	approval, err := h.service.DecideProposal(r.Context(), proposalID, revisionID, request.ChangeHash, request.Decision)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, toApprovalResponse(approval))
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

func toProposalResponse(proposal domain.Proposal) proposalResponse {
	response := proposalResponse{
		ID: string(proposal.ID), WorkspaceID: string(proposal.WorkspaceID), TargetPath: proposal.TargetPath,
		Status: string(proposal.Status), Revision: revisionResponse{
			ID: string(proposal.Revision.ID), RevisionNo: proposal.Revision.RevisionNo, BaseHash: proposal.Revision.BaseHash,
			Content: proposal.Revision.Content, EvidenceSummary: proposal.Revision.EvidenceSummary, Risk: proposal.Revision.Risk,
			RollbackPlan: proposal.Revision.RollbackPlan, ChangeHash: proposal.Revision.ChangeHash,
			CreatedAt: proposal.Revision.CreatedAt.UTC().Format(time.RFC3339Nano),
		}, CreatedAt: proposal.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: proposal.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if proposal.Approval != nil {
		approval := toApprovalResponse(*proposal.Approval)
		response.Approval = &approval
	}
	return response
}

func toApprovalResponse(approval domain.Approval) approvalResponse {
	return approvalResponse{ID: string(approval.ID), ProposalID: string(approval.ProposalID), RevisionID: string(approval.RevisionID), ChangeHash: approval.ChangeHash, Decision: string(approval.Decision), DecidedAt: approval.DecidedAt.UTC().Format(time.RFC3339Nano)}
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
	case "PROPOSAL_REVISION_CONFLICT":
		return "审批未绑定到请求的 Proposal Revision"
	case "PROPOSAL_NOT_FOUND", "PROPOSAL_REVISION_NOT_FOUND":
		return "请求的 Proposal 或 Revision 不存在"
	default:
		return "请求未完成：" + code
	}
}

var _ Service = (*application.Service)(nil)
