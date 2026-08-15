package changecontrolhttp

import (
	"context"
	"encoding/json"
	"errors"
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
)

const (
	revisionPreviewRequestMaxBytes  = 4 << 10
	revisionAppendRequestMaxBytes   = 8 << 20
	revisionPreviewResponseMaxBytes = 16 << 20
	revisionHistoryResponseMaxBytes = 1 << 20
	revisionMetadataMaxBytes        = 64 << 10
	revisionConflictIDMaxItems      = 1024
	revisionPreviewSchemaVersion    = "proposal-text-merge-preview/v1"
)

type revisionService interface {
	PreviewProposalRevision(context.Context, domain.RevisionMergeInput) (application.RevisionPreviewResult, error)
	AppendProposalRevision(context.Context, application.AppendRevisionCommand) (application.AppendRevisionResult, error)
}

type revisionHistoryService interface {
	ListProposalRevisions(context.Context, domain.ProposalRevisionHistoryQuery) ([]domain.ProposalRevisionHistoryItem, bool, error)
	GetProposalRevision(context.Context, foundation.ID, foundation.ID) (domain.ProposalRevisionHistoryDetail, error)
}

type revisionCapabilityResponse struct {
	Editable bool   `json:"editable"`
	Reason   string `json:"reason"`
}

type revisionMergePreviewRequest struct {
	SourceRevisionID        string `json:"source_revision_id"`
	SourceChangeHash        string `json:"source_change_hash"`
	ExpectedProposalVersion int64  `json:"expected_proposal_version"`
}

type appendRevisionRequest struct {
	ExpectedProposalVersion int64     `json:"expected_proposal_version"`
	SourceRevisionID        string    `json:"source_revision_id"`
	SourceChangeHash        string    `json:"source_change_hash"`
	ExpectedCurrentHash     string    `json:"expected_current_hash"`
	MergeFingerprint        string    `json:"merge_fingerprint"`
	MergeAlgorithm          string    `json:"merge_algorithm"`
	MergeAlgorithmVersion   string    `json:"merge_algorithm_version"`
	Content                 string    `json:"content"`
	EvidenceSummary         string    `json:"evidence_summary"`
	Risk                    string    `json:"risk"`
	RollbackPlan            string    `json:"rollback_plan"`
	ResolvedConflictIDs     *[]string `json:"resolved_conflict_ids"`
}

type revisionTextSnapshotResponse struct {
	Content  string `json:"content"`
	Hash     string `json:"hash"`
	ByteSize int    `json:"byte_size"`
}

// The application currently exposes exact conflict fragments, but not stable
// source/result ranges. Keeping this DTO explicit prevents marker parsing in
// the HTTP layer while preserving a strict, bounded wire contract.
type revisionMergeConflictResponse struct {
	ID       string `json:"id"`
	Ordinal  int    `json:"ordinal"`
	Base     string `json:"base"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
}

type revisionMergePreviewResponse struct {
	SchemaVersion         string                          `json:"schema_version"`
	MergeAlgorithm        string                          `json:"merge_algorithm"`
	MergeAlgorithmVersion string                          `json:"merge_algorithm_version"`
	MergeFingerprint      string                          `json:"merge_fingerprint"`
	ProposalID            string                          `json:"proposal_id"`
	WorkspaceID           string                          `json:"workspace_id"`
	ProposalVersion       int64                           `json:"proposal_version"`
	SourceRevisionID      string                          `json:"source_revision_id"`
	SourceRevisionNo      int                             `json:"source_revision_no"`
	SourceChangeHash      string                          `json:"source_change_hash"`
	TargetPath            string                          `json:"target_path"`
	TargetMode            string                          `json:"target_mode"`
	Base                  revisionTextSnapshotResponse    `json:"base"`
	Current               revisionTextSnapshotResponse    `json:"current"`
	Proposed              revisionTextSnapshotResponse    `json:"proposed"`
	Candidate             revisionTextSnapshotResponse    `json:"candidate"`
	ConflictCount         int                             `json:"conflict_count"`
	Conflicts             []revisionMergeConflictResponse `json:"conflicts"`
}

type proposalRevisionAppendResponse struct {
	proposalResponse
	Replayed bool `json:"replayed"`
}

type proposalRevisionHistoryResponse struct {
	Items                []proposalRevisionHistoryItemResponse `json:"items"`
	NextBeforeRevisionNo *int                                  `json:"next_before_revision_no,omitempty"`
}

type proposalRevisionHistoryItemResponse struct {
	ProposalID    string                    `json:"proposal_id"`
	RevisionID    string                    `json:"revision_id"`
	RevisionNo    int                       `json:"revision_no"`
	TargetPath    string                    `json:"target_path"`
	TargetMode    string                    `json:"target_mode"`
	BaseHash      string                    `json:"base_hash"`
	ChangeHash    string                    `json:"change_hash"`
	BaseAvailable bool                      `json:"base_available"`
	Current       bool                      `json:"current"`
	Approval      *approvalSnapshotResponse `json:"approval"`
	Workflow      *revisionWorkflowResponse `json:"workflow"`
	CreatedAt     string                    `json:"created_at"`
}

type revisionWorkflowResponse struct {
	ID        string `json:"id"`
	StatusURL string `json:"status_url"`
}

type revisionBaseSnapshotDetailResponse struct {
	Hash          string `json:"hash"`
	Content       string `json:"content"`
	ByteSize      int    `json:"byte_size"`
	SchemaVersion string `json:"schema_version"`
	CreatedAt     string `json:"created_at"`
}

type revisionLineageResponse struct {
	SourceRevisionID string `json:"source_revision_id"`
	SourceChangeHash string `json:"source_change_hash"`
	Kind             string `json:"kind"`
	MergeAlgorithm   string `json:"merge_algorithm"`
	MergeVersion     string `json:"merge_algorithm_version"`
	MergeFingerprint string `json:"merge_fingerprint"`
	CreatedAt        string `json:"created_at"`
}

type proposalRevisionHistoryDetailResponse struct {
	WorkspaceID       string                              `json:"workspace_id"`
	ProposalID        string                              `json:"proposal_id"`
	CurrentRevisionID string                              `json:"current_revision_id"`
	Current           bool                                `json:"current"`
	Revision          any                                 `json:"revision"`
	BaseAvailable     bool                                `json:"base_available"`
	BaseSnapshot      *revisionBaseSnapshotDetailResponse `json:"base_snapshot"`
	Lineage           *revisionLineageResponse            `json:"lineage"`
	Approval          *approvalSnapshotResponse           `json:"approval"`
	Workflow          *revisionWorkflowResponse           `json:"workflow"`
}

func (h *Handler) previewProposalRevision(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	proposalID, err := parseRevisionID(r.PathValue("proposal_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	service, ok := h.revisionService()
	if !ok {
		writeUnavailable(w)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, revisionInvalid(errors.New("query parameters are not supported")))
		return
	}
	request, err := decodeRevisionJSON[revisionMergePreviewRequest](r, revisionPreviewRequestMaxBytes, 256, 0)
	if err != nil {
		writeError(w, err)
		return
	}
	sourceRevisionID, err := parseRevisionID(request.SourceRevisionID)
	if err != nil || request.ExpectedProposalVersion <= 0 || !validLowerHash(request.SourceChangeHash) {
		writeError(w, revisionInvalid(errors.New("merge preview binding is invalid")))
		return
	}
	proposal, err := h.service.GetProposal(r.Context(), proposalID)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := service.PreviewProposalRevision(r.Context(), domain.RevisionMergeInput{
		WorkspaceID: proposal.WorkspaceID, ProposalID: proposalID, SourceRevisionID: sourceRevisionID,
		SourceChangeHash: request.SourceChangeHash, ExpectedProposalVersion: request.ExpectedProposalVersion,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := toRevisionMergePreviewResponse(proposalID, sourceRevisionID, request, result)
	if err != nil {
		writeError(w, err)
		return
	}
	writePrivateBoundedJSON(w, http.StatusOK, response, revisionPreviewResponseMaxBytes)
}

func (h *Handler) appendProposalRevision(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	proposalID, err := parseRevisionID(r.PathValue("proposal_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	service, ok := h.revisionService()
	if !ok {
		writeUnavailable(w)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, revisionInvalid(errors.New("query parameters are not supported")))
		return
	}
	key, err := parseRevisionIdempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	request, err := decodeRevisionJSON[appendRevisionRequest](r, revisionAppendRequestMaxBytes, int(domain.ProposalRevisionMaxBytes), revisionConflictIDMaxItems)
	if err != nil {
		writeError(w, err)
		return
	}
	sourceRevisionID, err := parseRevisionID(request.SourceRevisionID)
	if err != nil {
		writeError(w, revisionInvalid(errors.New("source revision identity is invalid")))
		return
	}
	if err := validateAppendRevisionRequest(request); err != nil {
		writeError(w, err)
		return
	}
	proposal, err := h.service.GetProposal(r.Context(), proposalID)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := service.AppendProposalRevision(r.Context(), application.AppendRevisionCommand{
		WorkspaceID: proposal.WorkspaceID, ProposalID: proposalID, IdempotencyKey: key,
		ExpectedProposalVersion: request.ExpectedProposalVersion, SourceRevisionID: sourceRevisionID,
		SourceChangeHash: request.SourceChangeHash, ExpectedCurrentHash: request.ExpectedCurrentHash,
		PreviewFingerprint: request.MergeFingerprint, MergeAlgorithm: request.MergeAlgorithm,
		MergeAlgorithmVersion: request.MergeAlgorithmVersion, FinalContent: request.Content,
		EvidenceSummary: request.EvidenceSummary, Risk: request.Risk, RollbackPlan: request.RollbackPlan,
		ResolvedConflictIDs: append([]string(nil), (*request.ResolvedConflictIDs)...),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validateAppendRevisionResult(proposalID, proposal.WorkspaceID, sourceRevisionID, request, result); err != nil {
		writeError(w, err)
		return
	}
	mapped, err := toProposalResponse(result.Proposal, h.revisionMergeAvailable())
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writePrivateBoundedJSON(w, status, proposalRevisionAppendResponse{proposalResponse: mapped, Replayed: result.Replayed}, revisionPreviewResponseMaxBytes)
}

func (h *Handler) listProposalRevisions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	proposalID, err := parseRevisionHistoryID(r.PathValue("proposal_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	service, ok := h.revisionHistoryService()
	if !ok {
		writeUnavailable(w)
		return
	}
	query, err := parseRevisionHistoryQuery(r, proposalID)
	if err != nil {
		writeError(w, err)
		return
	}
	items, hasMore, err := service.ListProposalRevisions(r.Context(), query)
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := toProposalRevisionHistoryResponse(proposalID, items, hasMore, query.Limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writePrivateBoundedJSON(w, http.StatusOK, response, revisionHistoryResponseMaxBytes)
}

func (h *Handler) getProposalRevision(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	proposalID, err := parseRevisionHistoryID(r.PathValue("proposal_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	revisionID, err := parseRevisionHistoryID(r.PathValue("revision_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, revisionHistoryInvalid(errors.New("query parameters are not supported")))
		return
	}
	service, ok := h.revisionHistoryService()
	if !ok {
		writeUnavailable(w)
		return
	}
	detail, err := service.GetProposalRevision(r.Context(), proposalID, revisionID)
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := toProposalRevisionHistoryDetailResponse(proposalID, revisionID, detail)
	if err != nil {
		writeError(w, err)
		return
	}
	writePrivateBoundedJSON(w, http.StatusOK, response, revisionPreviewResponseMaxBytes)
}

func (h *Handler) revisionService() (revisionService, bool) {
	if h == nil || h.service == nil {
		return nil, false
	}
	service, ok := h.service.(revisionService)
	return service, ok
}

func (h *Handler) revisionHistoryService() (revisionHistoryService, bool) {
	if h == nil || h.service == nil {
		return nil, false
	}
	service, ok := h.service.(revisionHistoryService)
	return service, ok
}

func decodeRevisionJSON[T any](r *http.Request, maxDocumentBytes, maxStringBytes, maxArrayItems int) (T, error) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	if r.ContentLength > int64(maxDocumentBytes) {
		return zero, revisionTooLarge(errors.New("request body exceeds the revision limit"))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxDocumentBytes)+1))
	if err != nil {
		return zero, revisionInvalid(errors.New("request body could not be read"))
	}
	if len(body) == 0 {
		return zero, revisionInvalid(errors.New("request body is empty"))
	}
	if len(body) > maxDocumentBytes {
		return zero, revisionTooLarge(errors.New("request body exceeds the revision limit"))
	}
	limits := strictjson.Limits{
		MaxDocumentBytes: maxDocumentBytes, MaxDepth: 4, MaxStringBytes: maxStringBytes,
		MaxArrayItems: maxArrayItems, MaxObjectFields: 32,
	}
	decoded, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		if kind, ok := strictjson.KindOf(err); ok && kind == strictjson.ErrorKindLimitExceeded {
			return zero, revisionTooLarge(err)
		}
		return zero, revisionInvalid(err)
	}
	return decoded, nil
}

func validateAppendRevisionRequest(request appendRevisionRequest) error {
	if request.ExpectedProposalVersion <= 0 || !validLowerHash(request.SourceChangeHash) || !validLowerHash(request.ExpectedCurrentHash) ||
		!validLowerHash(request.MergeFingerprint) || request.MergeAlgorithm != domain.ProposalRevisionMergeAlgorithm ||
		request.MergeAlgorithmVersion != domain.ProposalRevisionMergeAlgorithmVersion || request.ResolvedConflictIDs == nil {
		return revisionInvalid(errors.New("append revision identity is invalid"))
	}
	for _, value := range []string{request.Content, request.EvidenceSummary, request.Risk, request.RollbackPlan} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return revisionInvalid(errors.New("append revision content is invalid"))
		}
	}
	if len([]byte(request.Content)) > int(domain.ProposalRevisionMaxBytes) {
		return revisionTooLarge(errors.New("append revision content exceeds the limit"))
	}
	if strings.TrimSpace(request.Content) == "" {
		return revisionInvalid(errors.New("append revision content is empty"))
	}
	for _, value := range []string{request.EvidenceSummary, request.Risk, request.RollbackPlan} {
		if len([]byte(value)) > revisionMetadataMaxBytes {
			return revisionTooLarge(errors.New("append revision metadata exceeds the limit"))
		}
		if strings.TrimSpace(value) == "" {
			return revisionInvalid(errors.New("append revision metadata is empty"))
		}
	}
	if len(*request.ResolvedConflictIDs) > revisionConflictIDMaxItems {
		return revisionTooLarge(errors.New("too many resolved conflicts"))
	}
	seen := make(map[string]struct{}, len(*request.ResolvedConflictIDs))
	for _, conflictID := range *request.ResolvedConflictIDs {
		if !validLowerHash(conflictID) {
			return revisionInvalid(errors.New("resolved conflict identity is invalid"))
		}
		if _, duplicate := seen[conflictID]; duplicate {
			return revisionInvalid(errors.New("resolved conflict identity is duplicated"))
		}
		seen[conflictID] = struct{}{}
	}
	return nil
}

func validateAppendRevisionResult(proposalID, workspaceID, sourceRevisionID foundation.ID, request appendRevisionRequest, result application.AppendRevisionResult) error {
	proposal := result.Proposal
	_, riskLevelErr := domain.ValidateProposalRiskLevelForType(domain.ProposalTypeFilePatch, proposal.RiskLevel)
	if proposal.ID != proposalID || proposal.WorkspaceID != workspaceID || !validRevisionID(proposal.WorkspaceID) || !validRevisionID(result.Revision.ID) ||
		result.Revision.ID == sourceRevisionID || result.Revision.RevisionNo <= 1 || result.Revision.CreatedAt.IsZero() ||
		result.Revision.ProposalID != proposalID || proposal.Revision.ID != result.Revision.ID ||
		proposal.CurrentRevisionID != result.Revision.ID || proposal.Approval != nil || proposal.WorkflowRunID != nil ||
		domain.NormalizeProposalType(proposal.Type) != domain.ProposalTypeFilePatch ||
		domain.NormalizeTargetMode(result.Revision.TargetMode) != domain.TargetModeReplace ||
		proposal.TargetPath != result.Revision.TargetPath || proposal.Status != domain.StatusReady ||
		request.ExpectedProposalVersion <= 0 || proposal.Version != request.ExpectedProposalVersion+1 ||
		result.Revision.BaseHash != request.ExpectedCurrentHash || result.Revision.Content != request.Content ||
		result.Revision.EvidenceSummary != request.EvidenceSummary || result.Revision.Risk != request.Risk ||
		result.Revision.RollbackPlan != request.RollbackPlan ||
		riskLevelErr != nil ||
		domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, result.Revision) != nil ||
		domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, proposal.Revision) != nil ||
		!sameRevisionReceipt(proposal.Revision, result.Revision) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_RESULT_INVALID", false, errors.New("append revision result binding is invalid"))
	}
	return nil
}

func sameRevisionReceipt(left, right domain.Revision) bool {
	return left.ID == right.ID && left.ProposalID == right.ProposalID && left.RevisionNo == right.RevisionNo &&
		left.TargetPath == right.TargetPath && domain.NormalizeTargetMode(left.TargetMode) == domain.NormalizeTargetMode(right.TargetMode) &&
		left.BaseHash == right.BaseHash && left.Content == right.Content && left.EvidenceSummary == right.EvidenceSummary &&
		left.Risk == right.Risk && left.RollbackPlan == right.RollbackPlan && left.ChangeHash == right.ChangeHash &&
		left.CreatedAt.Equal(right.CreatedAt)
}

func parseRevisionIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || values[0] == "" {
		return "", foundation.NewError(foundation.ErrorInvalidInput, "IDEMPOTENCY_KEY_REQUIRED", false, errors.New("exactly one Idempotency-Key header is required"))
	}
	key := values[0]
	if !utf8.ValidString(key) || len(key) > 128 || strings.TrimSpace(key) != key {
		return "", revisionInvalid(errors.New("Idempotency-Key is invalid"))
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", revisionInvalid(errors.New("Idempotency-Key is invalid"))
		}
	}
	return key, nil
}

func parseRevisionHistoryQuery(r *http.Request, proposalID foundation.ID) (domain.ProposalRevisionHistoryQuery, error) {
	allowed := map[string]struct{}{"limit": {}, "before_revision_no": {}}
	for key, values := range r.URL.Query() {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return domain.ProposalRevisionHistoryQuery{}, revisionHistoryInvalid(errors.New("revision history query is invalid"))
		}
	}
	query := domain.ProposalRevisionHistoryQuery{ProposalID: proposalID, Limit: 30}
	if values, exists := r.URL.Query()["limit"]; exists {
		raw := values[0]
		if raw == "" {
			return domain.ProposalRevisionHistoryQuery{}, revisionHistoryInvalid(errors.New("revision history limit is empty"))
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return domain.ProposalRevisionHistoryQuery{}, revisionHistoryInvalid(err)
		}
		query.Limit = value
	}
	if values, exists := r.URL.Query()["before_revision_no"]; exists {
		raw := values[0]
		if raw == "" {
			return domain.ProposalRevisionHistoryQuery{}, revisionHistoryInvalid(errors.New("revision history cursor is empty"))
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return domain.ProposalRevisionHistoryQuery{}, revisionHistoryInvalid(err)
		}
		query.BeforeRevisionNo = &value
	}
	if err := domain.ValidateProposalRevisionHistoryQuery(query); err != nil {
		return domain.ProposalRevisionHistoryQuery{}, revisionHistoryInvalid(err)
	}
	return query, nil
}

func toRevisionMergePreviewResponse(proposalID, sourceRevisionID foundation.ID, request revisionMergePreviewRequest, result application.RevisionPreviewResult) (revisionMergePreviewResponse, error) {
	preview := result.Preview
	if preview.ProposalID != proposalID || preview.SourceRevisionID != sourceRevisionID || preview.ProposalVersion != request.ExpectedProposalVersion ||
		!validRevisionID(result.WorkspaceID) || result.SourceRevisionNo <= 0 || result.SourceChangeHash != request.SourceChangeHash ||
		!validRevisionTargetPath(result.TargetPath) || result.TargetMode != domain.TargetModeReplace ||
		!validLowerHash(preview.BaseHash) || !validLowerHash(preview.CurrentHash) || !validLowerHash(preview.ProposedHash) ||
		!validLowerHash(preview.Fingerprint) || preview.Algorithm != domain.ProposalRevisionMergeAlgorithm ||
		preview.AlgorithmVersion != domain.ProposalRevisionMergeAlgorithmVersion || len(preview.Conflicts) > revisionConflictIDMaxItems {
		return revisionMergePreviewResponse{}, mergeOutputInvalid(errors.New("merge preview binding is invalid"))
	}
	if domain.RawContentHash(result.BaseContent) != preview.BaseHash || domain.RawContentHash(result.CurrentContent) != preview.CurrentHash ||
		domain.RawContentHash(result.ProposedContent) != preview.ProposedHash ||
		!validRevisionText(result.BaseContent) || !validRevisionText(result.CurrentContent) ||
		!validRevisionText(result.ProposedContent) || !validRevisionText(preview.Candidate) {
		return revisionMergePreviewResponse{}, mergeOutputInvalid(errors.New("merge preview text binding is invalid"))
	}
	conflicts := make([]revisionMergeConflictResponse, len(preview.Conflicts))
	seen := make(map[string]struct{}, len(preview.Conflicts))
	for index, conflict := range preview.Conflicts {
		if !validLowerHash(conflict.ID) || conflict.Ordinal != index+1 ||
			!validRevisionText(conflict.Base) || !validRevisionText(conflict.Current) || !validRevisionText(conflict.Proposed) {
			return revisionMergePreviewResponse{}, mergeOutputInvalid(errors.New("merge conflict identity is invalid"))
		}
		if _, duplicate := seen[conflict.ID]; duplicate {
			return revisionMergePreviewResponse{}, mergeOutputInvalid(errors.New("merge conflict identity is duplicated"))
		}
		seen[conflict.ID] = struct{}{}
		conflicts[index] = revisionMergeConflictResponse{
			ID: conflict.ID, Ordinal: conflict.Ordinal, Base: conflict.Base, Current: conflict.Current, Proposed: conflict.Proposed,
		}
	}
	return revisionMergePreviewResponse{
		SchemaVersion: revisionPreviewSchemaVersion, MergeAlgorithm: preview.Algorithm,
		MergeAlgorithmVersion: preview.AlgorithmVersion, MergeFingerprint: preview.Fingerprint,
		ProposalID: string(proposalID), WorkspaceID: string(result.WorkspaceID), ProposalVersion: preview.ProposalVersion,
		SourceRevisionID: string(sourceRevisionID), SourceRevisionNo: result.SourceRevisionNo, SourceChangeHash: result.SourceChangeHash,
		TargetPath: result.TargetPath, TargetMode: string(result.TargetMode),
		Base: textSnapshot(result.BaseContent, preview.BaseHash), Current: textSnapshot(result.CurrentContent, preview.CurrentHash),
		Proposed: textSnapshot(result.ProposedContent, preview.ProposedHash), Candidate: textSnapshot(preview.Candidate, domain.RawContentHash(preview.Candidate)),
		ConflictCount: len(conflicts), Conflicts: conflicts,
	}, nil
}

func toProposalRevisionHistoryResponse(proposalID foundation.ID, items []domain.ProposalRevisionHistoryItem, hasMore bool, limit int) (proposalRevisionHistoryResponse, error) {
	if limit < 1 || limit > domain.MaxProposalRevisionHistoryLimit || len(items) > limit {
		return proposalRevisionHistoryResponse{}, revisionHistoryOutputInvalid(errors.New("revision history page exceeds the requested limit"))
	}
	response := proposalRevisionHistoryResponse{Items: make([]proposalRevisionHistoryItemResponse, len(items))}
	previousRevisionNo := int(^uint(0) >> 1)
	for index, item := range items {
		mode, modeErr := domain.ValidateTargetMode(item.TargetMode)
		if item.ProposalID != proposalID || !validRevisionID(item.RevisionID) || item.RevisionNo <= 0 || item.RevisionNo >= previousRevisionNo ||
			modeErr != nil || mode != domain.TargetModeReplace || !validRevisionTargetPath(item.TargetPath) ||
			!validLowerHash(item.BaseHash) || !validLowerHash(item.ChangeHash) ||
			item.CreatedAt.IsZero() {
			return proposalRevisionHistoryResponse{}, revisionHistoryOutputInvalid(errors.New("revision history binding is invalid"))
		}
		previousRevisionNo = item.RevisionNo
		approval, workflow, err := toRevisionHistoryDecision(proposalID, item.RevisionID, item.ChangeHash, item.Approval, item.WorkflowRunID)
		if err != nil {
			return proposalRevisionHistoryResponse{}, err
		}
		response.Items[index] = proposalRevisionHistoryItemResponse{
			ProposalID: string(proposalID), RevisionID: string(item.RevisionID), RevisionNo: item.RevisionNo,
			TargetPath: item.TargetPath, TargetMode: string(mode),
			BaseHash: item.BaseHash, ChangeHash: item.ChangeHash, BaseAvailable: item.BaseAvailable,
			Current: item.Current, Approval: approval, Workflow: workflow, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	if hasMore {
		if len(items) == 0 || items[len(items)-1].RevisionNo <= 1 {
			return proposalRevisionHistoryResponse{}, revisionHistoryOutputInvalid(errors.New("revision history pagination is inconsistent"))
		}
		next := items[len(items)-1].RevisionNo
		response.NextBeforeRevisionNo = &next
	}
	return response, nil
}

func toProposalRevisionHistoryDetailResponse(proposalID, revisionID foundation.ID, detail domain.ProposalRevisionHistoryDetail) (proposalRevisionHistoryDetailResponse, error) {
	if detail.ProposalID != proposalID || !validRevisionID(detail.WorkspaceID) || !validRevisionID(detail.CurrentRevisionID) ||
		detail.Revision.ID != revisionID || detail.Revision.ProposalID != proposalID || detail.Revision.RevisionNo <= 0 ||
		detail.Revision.CreatedAt.IsZero() {
		return proposalRevisionHistoryDetailResponse{}, revisionHistoryOutputInvalid(errors.New("revision detail binding is invalid"))
	}
	revision, err := toHistoricalRevisionResponse(detail.Revision)
	if err != nil {
		return proposalRevisionHistoryDetailResponse{}, err
	}
	approval, workflow, err := toRevisionHistoryDecision(proposalID, revisionID, detail.Revision.ChangeHash, detail.Approval, detail.WorkflowRunID)
	if err != nil {
		return proposalRevisionHistoryDetailResponse{}, err
	}
	response := proposalRevisionHistoryDetailResponse{
		WorkspaceID: string(detail.WorkspaceID), ProposalID: string(proposalID), CurrentRevisionID: string(detail.CurrentRevisionID),
		Current: detail.CurrentRevisionID == revisionID, Revision: revision, Approval: approval, Workflow: workflow,
	}
	if snapshot := detail.Revision.BaseSnapshot; snapshot != nil {
		if snapshot.ProposalID != proposalID || snapshot.RevisionID != revisionID || domain.ValidateRevisionBaseSnapshot(*snapshot) != nil {
			return proposalRevisionHistoryDetailResponse{}, revisionHistoryOutputInvalid(errors.New("revision base snapshot binding is invalid"))
		}
		response.BaseAvailable = true
		response.BaseSnapshot = &revisionBaseSnapshotDetailResponse{
			Hash: snapshot.BaseHash, Content: snapshot.Content, ByteSize: snapshot.ByteSize,
			SchemaVersion: snapshot.SchemaVersion, CreatedAt: snapshot.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	if lineage := detail.Revision.Lineage; lineage != nil {
		if lineage.ProposalID != proposalID || lineage.RevisionID != revisionID || !validRevisionID(lineage.SourceRevisionID) ||
			lineage.SourceRevisionID == revisionID || !validLowerHash(lineage.SourceChangeHash) || !validLowerHash(lineage.MergeFingerprint) ||
			(lineage.Kind != domain.RevisionLineageDirectEdit && lineage.Kind != domain.RevisionLineageThreeWayMerge) ||
			lineage.MergeAlgorithm != domain.ProposalRevisionMergeAlgorithm ||
			lineage.MergeVersion != domain.ProposalRevisionMergeAlgorithmVersion ||
			lineage.CreatedAt.IsZero() {
			return proposalRevisionHistoryDetailResponse{}, revisionHistoryOutputInvalid(errors.New("revision lineage binding is invalid"))
		}
		response.Lineage = &revisionLineageResponse{
			SourceRevisionID: string(lineage.SourceRevisionID), SourceChangeHash: lineage.SourceChangeHash, Kind: string(lineage.Kind),
			MergeAlgorithm: lineage.MergeAlgorithm, MergeVersion: lineage.MergeVersion,
			MergeFingerprint: lineage.MergeFingerprint, CreatedAt: lineage.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	} else if detail.Revision.RevisionNo > 1 {
		return proposalRevisionHistoryDetailResponse{}, revisionHistoryOutputInvalid(errors.New("appended revision lineage is unavailable"))
	}
	return response, nil
}

func toHistoricalRevisionResponse(revision domain.Revision) (any, error) {
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, revision); err != nil ||
		domain.NormalizeTargetMode(revision.TargetMode) != domain.TargetModeReplace {
		return nil, revisionHistoryOutputInvalid(errors.New("historical revision content is invalid"))
	}
	if !validRevisionText(revision.Content) || !validRevisionMetadata(revision.EvidenceSummary) ||
		!validRevisionMetadata(revision.Risk) || !validRevisionMetadata(revision.RollbackPlan) {
		return nil, revisionTooLarge(errors.New("historical revision content exceeds the public limit"))
	}
	return revisionResponse{
		ID: string(revision.ID), RevisionNo: revision.RevisionNo, TargetMode: string(domain.TargetModeReplace),
		BaseHash: revision.BaseHash, Content: revision.Content, EvidenceSummary: revision.EvidenceSummary,
		Risk: revision.Risk, RollbackPlan: revision.RollbackPlan, ChangeHash: revision.ChangeHash,
		CreatedAt: revision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func toRevisionHistoryDecision(proposalID, revisionID foundation.ID, changeHash string, approval *domain.Approval, workflowID *foundation.ID) (*approvalSnapshotResponse, *revisionWorkflowResponse, error) {
	var approvalResponse *approvalSnapshotResponse
	if approval != nil {
		if !validRevisionID(approval.ID) || approval.ProposalID != proposalID || approval.RevisionID != revisionID ||
			approval.ChangeHash != changeHash || !validLowerHash(approval.ChangeHash) || approval.DecidedAt.IsZero() ||
			(approval.Decision != domain.DecisionApproved && approval.Decision != domain.DecisionRejected) ||
			(approval.Decision == domain.DecisionRejected && approval.ApprovedGitHead != nil) ||
			(approval.ApprovedGitHead != nil && !domain.ValidGitHead(*approval.ApprovedGitHead)) {
			return nil, nil, revisionHistoryOutputInvalid(errors.New("historical approval binding is invalid"))
		}
		mapped := toApprovalDecisionSnapshotResponse(*approval)
		approvalResponse = &mapped
	}
	var workflow *revisionWorkflowResponse
	if workflowID != nil {
		if approval == nil || approval.Decision != domain.DecisionApproved || approval.ApprovedGitHead == nil || !validRevisionID(*workflowID) {
			return nil, nil, revisionHistoryOutputInvalid(errors.New("historical workflow binding is invalid"))
		}
		workflow = &revisionWorkflowResponse{ID: string(*workflowID), StatusURL: "/api/v1/workflows/" + string(*workflowID)}
	}
	return approvalResponse, workflow, nil
}

func revisionCapability(proposal domain.Proposal, mergeAvailable bool) revisionCapabilityResponse {
	capability := domain.EvaluateProposalRevisionCapability(domain.ProposalRevisionCapabilityFacts{
		ProposalType: proposal.Type, ProposalStatus: proposal.Status, TargetMode: proposal.Revision.TargetMode,
		CurrentRevisionID: proposal.CurrentRevisionID, RevisionID: proposal.Revision.ID,
		RevisionContentSize: len([]byte(proposal.Revision.Content)), WorkflowRunID: proposal.WorkflowRunID,
		WorkflowRunStatus: proposal.WorkflowRunStatus,
	})
	capability = domain.GateProposalRevisionMergeEngine(capability, mergeAvailable)
	return revisionCapabilityResponse{Editable: capability.Editable, Reason: string(capability.Reason)}
}

func toRevisionCapabilityResponse(capability domain.ProposalRevisionCapability) (revisionCapabilityResponse, error) {
	switch capability.Reason {
	case domain.ProposalRevisionAvailable, domain.ProposalRevisionUnsupportedType, domain.ProposalRevisionUnsupportedMode,
		domain.ProposalRevisionStatusNotEditable, domain.ProposalRevisionCapabilityStale, domain.ProposalRevisionWorkflowActive,
		domain.ProposalRevisionSideEffectStarted, domain.ProposalRevisionCapabilityTooLarge, domain.ProposalRevisionEngineUnavailable:
	default:
		return revisionCapabilityResponse{}, errors.New("proposal revision capability reason is invalid")
	}
	if capability.Editable != (capability.Reason == domain.ProposalRevisionAvailable) {
		return revisionCapabilityResponse{}, errors.New("proposal revision capability state is invalid")
	}
	return revisionCapabilityResponse{Editable: capability.Editable, Reason: string(capability.Reason)}, nil
}

type revisionMergeAvailability interface {
	RevisionMergeAvailable() bool
}

func (h *Handler) revisionMergeAvailable() bool {
	if h == nil || h.service == nil {
		return false
	}
	provider, ok := h.service.(revisionMergeAvailability)
	return ok && provider.RevisionMergeAvailable()
}

func textSnapshot(content, hash string) revisionTextSnapshotResponse {
	return revisionTextSnapshotResponse{Content: content, Hash: hash, ByteSize: len([]byte(content))}
}

func writePrivateBoundedJSON(w http.ResponseWriter, status int, value any, maxBytes int) {
	payload, err := json.Marshal(value)
	if err != nil {
		writeError(w, mergeOutputInvalid(err))
		return
	}
	if len(payload)+1 > maxBytes {
		writeError(w, foundation.NewError(foundation.ErrorNonRetryableFailure, "PROPOSAL_MERGE_RESULT_TOO_LARGE", false, errors.New("serialized revision response exceeds the limit")))
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func parseRevisionID(raw string) (foundation.ID, error) {
	id, err := foundation.ParseID(raw)
	if err != nil || raw != string(id) {
		return "", revisionInvalid(errors.New("revision route identity is invalid"))
	}
	return id, nil
}

func parseRevisionHistoryID(raw string) (foundation.ID, error) {
	id, err := foundation.ParseID(raw)
	if err != nil || raw != string(id) {
		return "", revisionHistoryInvalid(errors.New("revision history route identity is invalid"))
	}
	return id, nil
}

func revisionInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_MERGE_CONTENT_INVALID", false, cause)
}

func revisionTooLarge(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, cause)
}

func revisionHistoryInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_HISTORY_QUERY_INVALID", false, cause)
}

func revisionHistoryOutputInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_HISTORY_BINDING_INVALID", false, cause)
}

func mergeOutputInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID", false, cause)
}

func validLowerHash(value string) bool {
	return value == strings.ToLower(value) && domain.ValidHash(value)
}

func validRevisionID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && value == parsed && string(value) == string(parsed)
}

func validRevisionTargetPath(value string) bool {
	canonical, err := domain.ValidateTargetPath(value)
	return err == nil && canonical == value
}

func validRevisionText(value string) bool {
	return len([]byte(value)) <= int(domain.ProposalRevisionMaxBytes) && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validRevisionMetadata(value string) bool {
	return strings.TrimSpace(value) != "" && len([]byte(value)) <= revisionMetadataMaxBytes &&
		utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
