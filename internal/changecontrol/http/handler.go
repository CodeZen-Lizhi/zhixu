// Package changecontrolhttp exposes Proposal、Approval 与 Apply 前置校验 HTTP 契约。
package changecontrolhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/gin-gonic/gin"
)

// Service 定义 Change Control HTTP 所需的最小应用层契约。
type Service interface {
	CreateProposal(context.Context, application.CreateCommand) (application.CreateResult, error)
	CreateDownstreamUpdateProposal(context.Context, application.CreateDownstreamUpdateCommand) (application.CreateResult, error)
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
type Handler struct {
	service                 Service
	downstreamUpdateTimeout time.Duration
}

const defaultDownstreamUpdateTimeout = 2 * time.Second

// NewHandler 创建 Change Control HTTP Handler。
func NewHandler(service Service) *Handler {
	return NewHandlerWithTimeout(service, defaultDownstreamUpdateTimeout)
}

// NewHandlerWithTimeout 创建带 downstream update 命令超时的 Change Control HTTP Handler。
func NewHandlerWithTimeout(service Service, downstreamUpdateTimeout time.Duration) *Handler {
	if downstreamUpdateTimeout <= 0 {
		downstreamUpdateTimeout = defaultDownstreamUpdateTimeout
	}
	return &Handler{service: service, downstreamUpdateTimeout: downstreamUpdateTimeout}
}

// Routes 注册 Proposal、Approval 与 Apply preflight 路由。
func (h *Handler) Routes(router gin.IRouter) {
	router.POST("/workspaces/:workspace_id/proposals", httpapi.GinHandler(h.createProposal))
	router.GET("/workspaces/:workspace_id/proposals", httpapi.GinHandler(h.listProposals))
	router.POST("/workspaces/:workspace_id/impact-reports/:report_id/proposals", httpapi.GinHandler(h.createDownstreamUpdateProposal))
	router.GET("/proposals/:proposal_id", httpapi.GinHandler(h.getProposal))
	router.GET("/proposals/:proposal_id/current-content", httpapi.GinHandler(h.getProposalCurrentContent))
	router.POST("/proposals/:proposal_id/revision-merge-previews", httpapi.GinHandler(h.previewProposalRevision))
	router.GET("/proposals/:proposal_id/revisions", httpapi.GinHandler(h.listProposalRevisions))
	router.POST("/proposals/:proposal_id/revisions", httpapi.GinHandler(h.appendProposalRevision))
	router.GET("/proposals/:proposal_id/revisions/:revision_id", httpapi.GinHandler(h.getProposalRevision))
	router.POST("/proposals/:proposal_id/approvals", httpapi.GinHandler(h.decideProposal))
	router.POST("/proposals/:proposal_id/apply-preflight", httpapi.GinHandler(h.applyPreflight))
}

type proposalCurrentContentResponse struct {
	ProposalID    string `json:"proposal_id"`
	WorkspaceID   string `json:"workspace_id"`
	TargetPath    string `json:"target_path"`
	TargetMode    string `json:"target_mode"`
	Content       string `json:"content"`
	CurrentHash   string `json:"current_hash"`
	BaseHash      string `json:"base_hash"`
	BaseHashMatch bool   `json:"base_hash_match"`
}

func (h *Handler) getProposalCurrentContent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	proposalID, err := foundation.ParseID(r.PathValue("proposal_id"))
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
		TargetMode: string(result.TargetMode), CurrentHash: result.CurrentHash, BaseHash: result.BaseHash, BaseHashMatch: result.BaseHashMatch,
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
	ID                 string                     `json:"id"`
	WorkspaceID        string                     `json:"workspace_id"`
	ProposalType       string                     `json:"proposal_type"`
	Status             string                     `json:"status"`
	Target             string                     `json:"target"`
	RiskLevel          string                     `json:"risk_level"`
	Risk               string                     `json:"risk"`
	RevisionID         string                     `json:"revision_id"`
	ChangeHash         string                     `json:"change_hash"`
	Version            int64                      `json:"version"`
	RevisionCapability revisionCapabilityResponse `json:"revision_capability"`
	Approval           *approvalSnapshotResponse  `json:"approval,omitempty"`
	CreatedAt          string                     `json:"created_at"`
	UpdatedAt          string                     `json:"updated_at"`
}

func (h *Handler) listProposals(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(listService)
	if !ok {
		writeUnavailable(w)
		return
	}
	workspaceID, err := foundation.ParseID(r.PathValue("workspace_id"))
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
	if status != "" && !validProposalListStatus(status) || proposalType != "" && proposalType != domain.ProposalTypeFilePatch && proposalType != domain.ProposalTypeRestoreDocument && proposalType != domain.ProposalTypeKnowledgeChange && proposalType != domain.ProposalTypePublishArtifact && proposalType != domain.ProposalTypeDownstreamUpdate || len(risk) > 64 || strings.ContainsAny(risk, "\r\n\t") {
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
		mapped, mapErr := toProposalListResponse(item, h.revisionMergeAvailable())
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

const (
	maxDownstreamUpdateProposalBodyBytes = 16 * 1024
	maxDownstreamUpdateIdempotencyKey    = 128
)

type createDownstreamUpdateProposalRequest struct {
	TargetType knowledge.ImpactObjectType `json:"target_type"`
	TargetID   string                     `json:"target_id"`
	Action     knowledge.ImpactAction     `json:"action"`
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
	ProposalType       string                     `json:"proposal_type"`
	ID                 string                     `json:"id"`
	WorkspaceID        string                     `json:"workspace_id"`
	TargetPath         string                     `json:"target_path,omitempty"`
	Status             string                     `json:"status"`
	RiskLevel          string                     `json:"risk_level"`
	Version            int64                      `json:"version"`
	RevisionCapability revisionCapabilityResponse `json:"revision_capability"`
	Revision           any                        `json:"revision"`
	Approval           *approvalSnapshotResponse  `json:"approval"`
	CreatedAt          string                     `json:"created_at"`
	UpdatedAt          string                     `json:"updated_at"`
}

type downstreamUpdateProposalCreateResponse struct {
	proposalResponse
	Replayed bool `json:"replayed"`
}

type revisionResponse struct {
	ID              string                   `json:"id"`
	RevisionNo      int                      `json:"revision_no"`
	TargetMode      string                   `json:"target_mode"`
	BaseHash        string                   `json:"base_hash"`
	Content         string                   `json:"content"`
	EvidenceSummary string                   `json:"evidence_summary"`
	Risk            string                   `json:"risk"`
	RollbackPlan    string                   `json:"rollback_plan"`
	ChangeHash      string                   `json:"change_hash"`
	CreatedAt       string                   `json:"created_at"`
	Restore         *restoreDocumentResponse `json:"restore,omitempty"`
}

type restoreDocumentResponse struct {
	WorkspaceID             string `json:"workspace_id"`
	DocumentID              string `json:"document_id"`
	TargetCommit            string `json:"target_commit"`
	ExpectedHead            string `json:"expected_head"`
	ExpectedDocumentVersion int64  `json:"expected_document_version"`
	PreviewHash             string `json:"preview_hash"`
	CurrentContentHash      string `json:"current_content_hash"`
	TargetContentHash       string `json:"target_content_hash"`
	SchemaVersion           string `json:"schema_version"`
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

type publishArtifactGapResponse struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type publishArtifactCoverageResponse struct {
	SectionKey string                       `json:"section_key"`
	Status     string                       `json:"status"`
	Gaps       []publishArtifactGapResponse `json:"gaps"`
}

type publishArtifactBindingResponse struct {
	WorkspaceID     string                            `json:"workspace_id"`
	ArtifactID      string                            `json:"artifact_id"`
	RevisionID      string                            `json:"revision_id"`
	RevisionNo      int64                             `json:"revision_no"`
	ArtifactVersion int64                             `json:"artifact_version"`
	ContentHash     string                            `json:"content_hash"`
	SourceCoverage  []publishArtifactCoverageResponse `json:"source_coverage"`
	SchemaVersion   string                            `json:"schema_version"`
}

type publishArtifactRevisionResponse struct {
	ID           string                         `json:"id"`
	RevisionNo   int                            `json:"revision_no"`
	Publication  publishArtifactBindingResponse `json:"publication"`
	Risk         string                         `json:"risk"`
	RollbackPlan string                         `json:"rollback_plan"`
	ChangeHash   string                         `json:"change_hash"`
	CreatedAt    string                         `json:"created_at"`
}

type downstreamUpdateSourceReportResponse struct {
	ID              string `json:"id"`
	AnalysisVersion string `json:"analysis_version"`
	Fingerprint     string `json:"fingerprint"`
}

type downstreamUpdateSourceEventResponse struct {
	ID           string `json:"id"`
	EventVersion int64  `json:"event_version"`
}

type artifactImpactBindingResponse struct {
	ArtifactID      string `json:"artifact_id"`
	ArtifactVersion int64  `json:"artifact_version"`
	RevisionID      string `json:"revision_id"`
	RevisionNo      int64  `json:"revision_no"`
	ContentHash     string `json:"content_hash"`
}

type reviewCardImpactBindingResponse struct {
	CardID                     string `json:"card_id"`
	CardVersion                int64  `json:"card_version"`
	Status                     string `json:"status"`
	Fingerprint                string `json:"fingerprint"`
	ClaimID                    string `json:"claim_id"`
	EvidenceBindingFingerprint string `json:"evidence_binding_fingerprint"`
}

type downstreamUpdateResponse struct {
	WorkspaceID       string                               `json:"workspace_id"`
	SourceReport      downstreamUpdateSourceReportResponse `json:"source_report"`
	SourceEvent       downstreamUpdateSourceEventResponse  `json:"source_event"`
	TargetType        string                               `json:"target_type"`
	TargetID          string                               `json:"target_id"`
	BaseVersion       int64                                `json:"base_version"`
	Action            string                               `json:"action"`
	ArtifactBinding   *artifactImpactBindingResponse       `json:"artifact_binding,omitempty"`
	ReviewCardBinding *reviewCardImpactBindingResponse     `json:"review_card_binding,omitempty"`
	Reason            string                               `json:"reason"`
	SchemaVersion     string                               `json:"schema_version"`
}

type downstreamUpdateRevisionResponse struct {
	ID           string                   `json:"id"`
	RevisionNo   int                      `json:"revision_no"`
	Update       downstreamUpdateResponse `json:"update"`
	Risk         string                   `json:"risk"`
	RollbackPlan string                   `json:"rollback_plan"`
	ChangeHash   string                   `json:"change_hash"`
	CreatedAt    string                   `json:"created_at"`
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
	TargetMode      string `json:"target_mode"`
	BaseHash        string `json:"base_hash"`
	PreflightPassed bool   `json:"preflight_passed"`
	Mode            string `json:"mode"`
	WritePerformed  bool   `json:"write_performed"`
}

func (h *Handler) createProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	workspaceID, err := foundation.ParseID(r.PathValue("workspace_id"))
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
	response, mapErr := toProposalResponse(result.Proposal, h.revisionMergeAvailable())
	if mapErr != nil {
		writeError(w, mapErr)
		return
	}
	httpapi.WriteJSON(w, status, response)
}

func (h *Handler) createDownstreamUpdateProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.RawQuery != "" {
		writeError(w, downstreamUpdateProposalInvalid("query parameters are not supported"))
		return
	}
	workspaceID, err := parseDownstreamUpdateID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	reportID, err := parseDownstreamUpdateID(r.PathValue("report_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	request, err := decodeDownstreamUpdateProposalRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	targetID, err := parseDownstreamUpdateID(request.TargetID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !validDownstreamUpdateSelection(request.TargetType, request.Action) {
		writeError(w, downstreamUpdateProposalInvalid("target type and action are not compatible"))
		return
	}
	idempotencyKey, err := parseDownstreamUpdateIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeUnavailable(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.downstreamUpdateTimeout)
	defer cancel()
	result, err := h.service.CreateDownstreamUpdateProposal(ctx, application.CreateDownstreamUpdateCommand{
		WorkspaceID: workspaceID, ReportID: reportID, TargetType: request.TargetType,
		TargetID: targetID, Action: request.Action, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			httpapi.WriteProblem(w, http.StatusServiceUnavailable, knowledge.ErrorCodeImpactUnavailable,
				"影响分析依赖暂不可用", errors.Is(err, context.DeadlineExceeded), nil)
			return
		}
		writeError(w, err)
		return
	}
	response, err := toProposalResponse(result.Proposal, h.revisionMergeAvailable())
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, downstreamUpdateProposalCreateResponse{proposalResponse: response, Replayed: result.Replayed})
}

func decodeDownstreamUpdateProposalRequest(r *http.Request) (createDownstreamUpdateProposalRequest, error) {
	var zero createDownstreamUpdateProposalRequest
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDownstreamUpdateProposalBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxDownstreamUpdateProposalBodyBytes {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxDownstreamUpdateProposalBodyBytes
	request, err := strictjson.DecodeObject[createDownstreamUpdateProposalRequest](body, limits, nil)
	if err != nil {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return request, nil
}

func parseDownstreamUpdateID(raw string) (foundation.ID, error) {
	id, err := foundation.ParseID(raw)
	if err != nil || raw != string(id) {
		return "", downstreamUpdateProposalInvalid("proposal command ID is invalid")
	}
	return id, nil
}

func parseDownstreamUpdateIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) || values[0] == "" {
		return "", foundation.NewError(foundation.ErrorInvalidInput, "IDEMPOTENCY_KEY_REQUIRED", false, errors.New("exactly one Idempotency-Key header is required"))
	}
	key := values[0]
	if len(key) > maxDownstreamUpdateIdempotencyKey || strings.TrimSpace(key) != key {
		return "", downstreamUpdateProposalInvalid("Idempotency-Key is invalid")
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", downstreamUpdateProposalInvalid("Idempotency-Key is invalid")
		}
	}
	return key, nil
}

func validDownstreamUpdateSelection(targetType knowledge.ImpactObjectType, action knowledge.ImpactAction) bool {
	return targetType == knowledge.ImpactObjectArtifact && action == knowledge.ImpactActionRegenerateArtifact ||
		targetType == knowledge.ImpactObjectReviewCard && action == knowledge.ImpactActionRevalidateReviewCard
}

func downstreamUpdateProposalInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_PROPOSAL_INVALID", false, errors.New(message))
}

func (h *Handler) getProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	proposalID, err := foundation.ParseID(r.PathValue("proposal_id"))
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
	response, mapErr := toProposalResponse(proposal, h.revisionMergeAvailable())
	if mapErr != nil {
		writeError(w, mapErr)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) decideProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, err := foundation.ParseID(r.PathValue("proposal_id"))
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
	proposalID, err := foundation.ParseID(r.PathValue("proposal_id"))
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
		TargetMode: string(result.TargetMode), BaseHash: result.BaseHash,
		PreflightPassed: true, Mode: "preflight_only", WritePerformed: false,
	})
}

func toProposalResponse(proposal domain.Proposal, mergeAvailable bool) (proposalResponse, error) {
	proposalType := domain.NormalizeProposalType(proposal.Type)
	riskLevel, err := responseRiskLevel(proposalType, proposal.RiskLevel)
	if err != nil {
		return proposalResponse{}, err
	}
	response := proposalResponse{
		ProposalType: string(proposalType), ID: string(proposal.ID), WorkspaceID: string(proposal.WorkspaceID), TargetPath: proposal.TargetPath,
		Status: string(proposal.Status), RiskLevel: string(riskLevel), Version: proposal.Version, RevisionCapability: revisionCapability(proposal, mergeAvailable),
		CreatedAt: proposal.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: proposal.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	switch proposalType {
	case domain.ProposalTypeKnowledgeChange:
		response.TargetPath = ""
		response.Revision = toKnowledgeRevisionResponse(proposal.Revision)
	case domain.ProposalTypePublishArtifact:
		response.TargetPath = ""
		revision, mapErr := toPublishArtifactRevisionResponse(proposal.Revision)
		if mapErr != nil {
			return proposalResponse{}, mapErr
		}
		response.Revision = revision
	case domain.ProposalTypeDownstreamUpdate:
		response.TargetPath = ""
		revision, mapErr := toDownstreamUpdateRevisionResponse(proposal.WorkspaceID, proposal.Revision)
		if mapErr != nil {
			return proposalResponse{}, mapErr
		}
		response.Revision = revision
	default:
		revision := revisionResponse{
			ID: string(proposal.Revision.ID), RevisionNo: proposal.Revision.RevisionNo,
			TargetMode: string(domain.NormalizeTargetMode(proposal.Revision.TargetMode)), BaseHash: proposal.Revision.BaseHash,
			Content: proposal.Revision.Content, EvidenceSummary: proposal.Revision.EvidenceSummary, Risk: proposal.Revision.Risk,
			RollbackPlan: proposal.Revision.RollbackPlan, ChangeHash: proposal.Revision.ChangeHash,
			CreatedAt: proposal.Revision.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if proposalType == domain.ProposalTypeRestoreDocument {
			if err := domain.ValidateProposalRevisionForType(proposalType, proposal.Revision); err != nil || proposal.Revision.RestoreDocument == nil {
				return proposalResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_INVALID", false, err)
			}
			restore := proposal.Revision.RestoreDocument
			revision.Restore = &restoreDocumentResponse{
				WorkspaceID: string(restore.WorkspaceID), DocumentID: string(restore.DocumentID),
				TargetCommit: restore.TargetCommit, ExpectedHead: restore.ExpectedHead,
				ExpectedDocumentVersion: restore.ExpectedDocumentVersion, PreviewHash: restore.PreviewHash,
				CurrentContentHash: restore.CurrentContentHash, TargetContentHash: restore.TargetContentHash,
				SchemaVersion: restore.SchemaVersion,
			}
		}
		response.Revision = revision
	}
	if proposal.Approval != nil {
		approval := toApprovalSnapshotResponse(*proposal.Approval, proposal.Type, proposal.WorkflowRunID)
		response.Approval = &approval
	}
	return response, nil
}

func toPublishArtifactRevisionResponse(revision domain.Revision) (publishArtifactRevisionResponse, error) {
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypePublishArtifact, revision); err != nil {
		return publishArtifactRevisionResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_INVALID", false, err)
	}
	publication, err := domain.ValidatePublishArtifact(*revision.PublishArtifact)
	if err != nil {
		return publishArtifactRevisionResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_INVALID", false, err)
	}
	coverage := make([]publishArtifactCoverageResponse, len(publication.SourceCoverage))
	for index, item := range publication.SourceCoverage {
		gaps := make([]publishArtifactGapResponse, len(item.Gaps))
		for gapIndex, gap := range item.Gaps {
			gaps[gapIndex] = publishArtifactGapResponse{Code: gap.Code, Description: gap.Description}
		}
		coverage[index] = publishArtifactCoverageResponse{SectionKey: item.SectionKey, Status: string(item.Status), Gaps: gaps}
	}
	return publishArtifactRevisionResponse{
		ID: string(revision.ID), RevisionNo: revision.RevisionNo,
		Publication: publishArtifactBindingResponse{
			WorkspaceID: string(publication.WorkspaceID), ArtifactID: string(publication.ArtifactID), RevisionID: string(publication.RevisionID),
			RevisionNo: publication.RevisionNo, ArtifactVersion: publication.ArtifactVersion, ContentHash: publication.ContentHash,
			SourceCoverage: coverage, SchemaVersion: publication.SchemaVersion,
		},
		Risk: revision.Risk, RollbackPlan: revision.RollbackPlan, ChangeHash: revision.ChangeHash,
		CreatedAt: revision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func toDownstreamUpdateRevisionResponse(workspaceID foundation.ID, revision domain.Revision) (downstreamUpdateRevisionResponse, error) {
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeDownstreamUpdate, revision); err != nil {
		return downstreamUpdateRevisionResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_INVALID", false, err)
	}
	update, err := domain.ValidateDownstreamUpdate(*revision.DownstreamUpdate)
	if err != nil || update.WorkspaceID != workspaceID {
		if err == nil {
			err = errors.New("downstream update workspace differs from proposal workspace")
		}
		return downstreamUpdateRevisionResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_INVALID", false, err)
	}
	response := downstreamUpdateResponse{
		WorkspaceID: string(update.WorkspaceID),
		SourceReport: downstreamUpdateSourceReportResponse{
			ID: string(update.ReportID), AnalysisVersion: string(update.AnalysisVersion), Fingerprint: update.ReportFingerprint,
		},
		SourceEvent: downstreamUpdateSourceEventResponse{ID: string(update.SourceEventID), EventVersion: update.SourceEventVersion},
		TargetType:  string(update.TargetType), TargetID: string(update.TargetID), BaseVersion: update.BaseVersion,
		Action: string(update.Action), Reason: update.Reason, SchemaVersion: update.SchemaVersion,
	}
	if update.OwnerBinding.Artifact != nil {
		binding := update.OwnerBinding.Artifact
		response.ArtifactBinding = &artifactImpactBindingResponse{
			ArtifactID: string(binding.ArtifactID), ArtifactVersion: binding.ArtifactVersion,
			RevisionID: string(binding.RevisionID), RevisionNo: binding.RevisionNo, ContentHash: binding.ContentHash,
		}
	}
	if update.OwnerBinding.ReviewCard != nil {
		binding := update.OwnerBinding.ReviewCard
		response.ReviewCardBinding = &reviewCardImpactBindingResponse{
			CardID: string(binding.CardID), CardVersion: binding.CardVersion, Status: binding.Status,
			Fingerprint: binding.Fingerprint, ClaimID: string(binding.ClaimID),
			EvidenceBindingFingerprint: binding.EvidenceBindingFingerprint,
		}
	}
	return downstreamUpdateRevisionResponse{
		ID: string(revision.ID), RevisionNo: revision.RevisionNo, Update: response,
		Risk: revision.Risk, RollbackPlan: revision.RollbackPlan, ChangeHash: revision.ChangeHash,
		CreatedAt: revision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
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

func toProposalListResponse(item domain.ProposalListItem, mergeAvailable bool) (proposalListResponse, error) {
	proposalType := domain.NormalizeProposalType(item.Type)
	riskLevel, err := responseRiskLevel(proposalType, item.RiskLevel)
	if err != nil {
		return proposalListResponse{}, err
	}
	target := item.Target
	if proposalType == domain.ProposalTypePublishArtifact {
		target = "Artifact 发布"
	}
	capability, err := toRevisionCapabilityResponse(domain.GateProposalRevisionMergeEngine(item.RevisionCapability, mergeAvailable))
	if err != nil || item.Version <= 0 {
		return proposalListResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_CAPABILITY_INVALID", false, errors.New("proposal list revision capability is invalid"))
	}
	response := proposalListResponse{
		ID: string(item.ProposalID), WorkspaceID: string(item.WorkspaceID), ProposalType: string(proposalType),
		Status: string(item.Status), Target: target, RiskLevel: string(riskLevel), Risk: item.Risk, RevisionID: string(item.RevisionID),
		ChangeHash: item.ChangeHash, Version: item.Version, RevisionCapability: capability, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
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
	if approval.Decision != domain.DecisionApproved || !domain.ProposalSupportsFileWriteback(proposalType) {
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
	if classified.Code == "UNSUPPORTED_MEDIA_TYPE" {
		status = http.StatusUnsupportedMediaType
	}
	switch classified.Code {
	case "PROPOSAL_REVISION_INPUT_TOO_LARGE":
		status = http.StatusRequestEntityTooLarge
	case "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED", "PROPOSAL_MERGE_RESULT_TOO_LARGE":
		status = http.StatusUnprocessableEntity
	case "PROPOSAL_MERGE_BUSY", "PROPOSAL_MERGE_TIMEOUT", "PROPOSAL_MERGE_TEMPORARY_STORAGE_UNAVAILABLE",
		"PROPOSAL_MERGE_ENGINE_UNAVAILABLE", "PROPOSAL_MERGE_ENGINE_UNSUPPORTED", "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID":
		status = http.StatusServiceUnavailable
	case "PROPOSAL_REVISION_RESULT_INVALID", "PROPOSAL_REVISION_HISTORY_BINDING_INVALID", "PROPOSAL_REVISION_SOURCE_INVALID",
		"PROPOSAL_BASE_SNAPSHOT_INVALID", "PROPOSAL_CURRENT_CONTENT_HASH_INVALID", "PROPOSAL_REVISION_CAPABILITY_INVALID":
		status = http.StatusInternalServerError
	}
	details := map[string]any(nil)
	var conflict *application.HashConflict
	if errors.As(err, &conflict) {
		if classified.Code == "PROPOSAL_REVISION_STALE" {
			details = map[string]any{
				"expected_current_hash": conflict.Expected,
				"current_hash":          conflict.Current,
				"conflict_type":         "current_content_changed",
				"resolution_actions":    []string{"refresh_proposal", "restart_merge"},
			}
		} else {
			details = map[string]any{
				"expected_version":   conflict.Expected,
				"current_version":    conflict.Current,
				"conflict_type":      "target_base_hash_changed",
				"resolution_actions": []string{"create_new_proposal_revision"},
			}
		}
	}
	switch classified.Code {
	case "PROPOSAL_REVISION_INPUT_TOO_LARGE":
		details = map[string]any{
			"max_preview_request_bytes": revisionPreviewRequestMaxBytes, "max_append_request_bytes": revisionAppendRequestMaxBytes,
			"max_content_bytes":  int(domain.ProposalRevisionMaxBytes),
			"max_metadata_bytes": revisionMetadataMaxBytes, "max_conflict_ids": revisionConflictIDMaxItems,
		}
	case "PROPOSAL_MERGE_RESULT_TOO_LARGE":
		details = map[string]any{
			"max_candidate_bytes": int(domain.ProposalRevisionMaxBytes), "max_response_bytes": revisionPreviewResponseMaxBytes,
			"max_conflicts": revisionConflictIDMaxItems,
		}
	case "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED":
		details = map[string]any{"max_conflicts": revisionConflictIDMaxItems, "resolution_actions": []string{"resolve_all_conflicts", "recalculate_preview"}}
		if conflictIDs := unresolvedRevisionConflictIDs(err); len(conflictIDs) > 0 {
			details["conflict_ids"] = conflictIDs
		}
	case "PROPOSAL_REVISION_NOT_EDITABLE":
		details = map[string]any{"resolution_actions": []string{"refresh_proposal"}}
	case "PROPOSAL_REVISION_STALE":
		if details == nil {
			details = map[string]any{"resolution_actions": []string{"refresh_proposal", "restart_merge"}}
		}
	}
	message := publicMessage(classified.Code)
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, details)
}

func unresolvedRevisionConflictIDs(err error) []string {
	var unresolved *application.UnresolvedRevisionConflicts
	if !errors.As(err, &unresolved) || unresolved == nil || len(unresolved.ConflictIDs) == 0 || len(unresolved.ConflictIDs) > revisionConflictIDMaxItems {
		return nil
	}
	result := make([]string, len(unresolved.ConflictIDs))
	seen := make(map[string]struct{}, len(unresolved.ConflictIDs))
	for index, conflictID := range unresolved.ConflictIDs {
		if !validLowerHash(conflictID) {
			return nil
		}
		if _, duplicate := seen[conflictID]; duplicate {
			return nil
		}
		seen[conflictID] = struct{}{}
		result[index] = conflictID
	}
	return result
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
		return "写入请求必须提供且仅提供一个 Idempotency-Key"
	case "IDEMPOTENCY_KEY_REUSED":
		return "Idempotency-Key 已绑定到不同的 Proposal 请求"
	case "DOWNSTREAM_UPDATE_PROPOSAL_INVALID":
		return "下游更新 Proposal 请求无效"
	case "DOWNSTREAM_UPDATE_TARGET_INVALID":
		return "影响报告中的目标不支持该 Proposal 操作"
	case "KNOWLEDGE_IMPACT_NOT_FOUND":
		return "影响报告不存在"
	case "KNOWLEDGE_IMPACT_CONFLICT":
		return "影响报告或目标绑定已变化，请重新分析后再创建 Proposal"
	case "KNOWLEDGE_IMPACT_UNAVAILABLE":
		return "影响分析依赖暂不可用"
	case "DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE":
		return "下游更新意图已记录，但尚未提供执行能力"
	case "PROPOSAL_REVISION_CONFLICT":
		return "审批未绑定到请求的 Proposal Revision"
	case "PROPOSAL_NOT_FOUND", "PROPOSAL_REVISION_NOT_FOUND":
		return "请求的 Proposal 或 Revision 不存在"
	case "PROPOSAL_REVISION_NOT_EDITABLE":
		return "当前 Proposal 状态或类型不允许创建新 Revision"
	case "PROPOSAL_BASE_SNAPSHOT_UNAVAILABLE":
		return "当前 Revision 没有可信的基线快照"
	case "PROPOSAL_REVISION_STALE":
		return "Proposal 或 Workspace 内容已变化，请重新合并"
	case "PROPOSAL_REVISION_SIDE_EFFECT_STARTED":
		return "当前 Revision 已进入不可安全替换的执行阶段"
	case "PROPOSAL_REVISION_WORKFLOW_ACTIVE":
		return "当前 Revision 的 Workflow 尚未结束"
	case "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE":
		return "当前 Revision 的 Workflow 取消服务暂不可用"
	case "PROPOSAL_REVISION_AUTHORIZATION_CONFLICT":
		return "当前 Revision 的写入授权无法安全撤销"
	case "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED":
		return "仍有合并冲突尚未确认"
	case "PROPOSAL_REVISION_INPUT_TOO_LARGE":
		return "Revision 请求或正文超过固定大小限制"
	case "PROPOSAL_MERGE_CONTENT_INVALID":
		return "Revision 合并请求内容无效"
	case "PROPOSAL_MERGE_RESULT_TOO_LARGE":
		return "合并结果超过固定大小限制"
	case "PROPOSAL_MERGE_BUSY":
		return "合并服务繁忙，请稍后重试"
	case "PROPOSAL_MERGE_TIMEOUT":
		return "合并计算超时，请稍后重试"
	case "PROPOSAL_MERGE_TEMPORARY_STORAGE_UNAVAILABLE":
		return "合并临时存储暂不可用"
	case "PROPOSAL_MERGE_ENGINE_UNAVAILABLE", "PROPOSAL_MERGE_ENGINE_UNSUPPORTED", "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID":
		return "三方合并能力暂不可用"
	case "PROPOSAL_REVISION_HISTORY_QUERY_INVALID":
		return "Revision 历史查询参数无效"
	default:
		return "请求未完成：" + code
	}
}

var _ Service = (*application.Service)(nil)
