package graphhttp

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

type candidateEndpointResponse struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Summary string `json:"summary"`
	Excerpt string `json:"excerpt"`
}

type candidateEvidenceResponse struct {
	ID                string `json:"id"`
	SemanticHash      string `json:"semantic_hash"`
	SourceVersionID   string `json:"source_version_id"`
	SourceSpanID      string `json:"source_span_id"`
	SourceVersionHref string `json:"source_version_href"`
	SourceSpanHref    string `json:"source_span_href"`
	Excerpt           string `json:"excerpt"`
	Reason            string `json:"reason"`
}

// candidateGenerationResponse 使用 Domain 的版本字段作为唯一事实源。
// Rule 与 Model 通过字段组合区分，不在 HTTP 层伪造 Provider 名称。
type candidateGenerationResponse struct {
	IndexVersionID     *string `json:"index_version_id"`
	EmbeddingVersionID *string `json:"embedding_version_id"`
	RerankVersionID    *string `json:"rerank_version_id"`

	ModelVersion        string  `json:"model_version,omitempty"`
	ModelProfileVersion string  `json:"model_profile_version,omitempty"`
	PromptVersion       string  `json:"prompt_version,omitempty"`
	SchemaVersion       string  `json:"schema_version,omitempty"`
	ModelRunID          *string `json:"model_run_id,omitempty"`

	RuleID      *string `json:"rule_id,omitempty"`
	RuleVersion string  `json:"rule_version,omitempty"`
}

type candidateResponse struct {
	ID                      string                      `json:"id"`
	WorkspaceID             string                      `json:"workspace_id"`
	Fingerprint             string                      `json:"fingerprint"`
	Status                  string                      `json:"status"`
	Version                 int64                       `json:"version"`
	Source                  candidateEndpointResponse   `json:"source"`
	Target                  candidateEndpointResponse   `json:"target"`
	ProposedRelationType    string                      `json:"proposed_relation_type"`
	Confidence              float64                     `json:"confidence"`
	Reason                  string                      `json:"reason"`
	DiscoveryMethods        []string                    `json:"discovery_methods"`
	Evidence                []candidateEvidenceResponse `json:"evidence"`
	Generation              candidateGenerationResponse `json:"generation"`
	ReopenedReason          *string                     `json:"reopened_reason"`
	ReopenedFromCandidateID *string                     `json:"reopened_from_candidate_id"`
	ProposalID              *string                     `json:"proposal_id"`
	DeferredUntil           *string                     `json:"deferred_until"`
	CreatedAt               string                      `json:"created_at"`
	UpdatedAt               string                      `json:"updated_at"`
}

type candidatePageResponse struct {
	WorkspaceID string              `json:"workspace_id"`
	Items       []candidateResponse `json:"items"`
	NextCursor  string              `json:"next_cursor,omitempty"`
}

type candidateDecisionReceiptResponse struct {
	ID          string  `json:"id"`
	CandidateID string  `json:"candidate_id"`
	WorkspaceID string  `json:"workspace_id"`
	Action      string  `json:"action"`
	Status      string  `json:"status"`
	Version     int64   `json:"version"`
	ProposalID  *string `json:"proposal_id"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type candidateScanAcceptanceResponse struct {
	ScanID        string `json:"scan_id"`
	WorkflowRunID string `json:"workflow_run_id"`
	Status        string `json:"status"`
	Version       int64  `json:"version"`
	StatusURL     string `json:"status_url"`
}

type candidateScanScopeResponse struct {
	Kind              string `json:"kind"`
	TopicID           string `json:"topic_id,omitempty"`
	CollectionID      string `json:"collection_id,omitempty"`
	CollectionVersion int64  `json:"collection_version,omitempty"`
	QueryHash         string `json:"query_hash,omitempty"`
	ReadModelRevision string `json:"read_model_revision,omitempty"`
}

type candidateScanErrorResponse struct {
	Stage     string `json:"stage"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

type candidateScanResponse struct {
	ID             string                      `json:"id"`
	WorkspaceID    string                      `json:"workspace_id"`
	Scope          candidateScanScopeResponse  `json:"scope"`
	Status         string                      `json:"status"`
	WorkflowRunID  string                      `json:"workflow_run_id"`
	Version        int64                       `json:"version"`
	StatusURL      string                      `json:"status_url"`
	TotalCount     int64                       `json:"total_count"`
	ProcessedCount int64                       `json:"processed_count"`
	CandidateCount int64                       `json:"candidate_count"`
	IgnoredCount   int64                       `json:"ignored_count"`
	FailedCount    int64                       `json:"failed_count"`
	LastError      *candidateScanErrorResponse `json:"last_error"`
	CreatedAt      string                      `json:"created_at"`
	UpdatedAt      string                      `json:"updated_at"`
	CompletedAt    *string                     `json:"completed_at"`
}

func toCandidatePageResponse(page graphdomain.SemanticLinkCandidatePage) candidatePageResponse {
	items := make([]candidateResponse, len(page.Items))
	for index, candidate := range page.Items {
		items[index] = toCandidateResponse(candidate)
	}
	return candidatePageResponse{WorkspaceID: string(page.WorkspaceID), Items: items, NextCursor: page.Meta.NextCursor}
}

func toCandidateResponse(candidate graphdomain.SemanticLinkCandidate) candidateResponse {
	evidence := make([]candidateEvidenceResponse, len(candidate.Evidence))
	for index, item := range candidate.Evidence {
		sourceVersionID := string(item.Provenance.SourceVersionID)
		sourceSpanID := string(item.Provenance.SourceSpanID)
		workspaceID := string(candidate.WorkspaceID)
		sourceVersionHref := "/api/v1/workspaces/" + workspaceID + "/source-versions/" + sourceVersionID
		evidence[index] = candidateEvidenceResponse{
			ID: string(item.ID), SemanticHash: item.SemanticHash,
			SourceVersionID: sourceVersionID, SourceSpanID: sourceSpanID,
			SourceVersionHref: sourceVersionHref,
			SourceSpanHref:    sourceVersionHref + "/spans/" + sourceSpanID,
			Excerpt:           item.Excerpt, Reason: item.Reason,
		}
	}
	discoveryMethods := make([]string, len(candidate.DiscoveryMethods))
	for index, method := range candidate.DiscoveryMethods {
		discoveryMethods[index] = string(method)
	}
	return candidateResponse{
		ID: string(candidate.ID), WorkspaceID: string(candidate.WorkspaceID), Fingerprint: candidate.Fingerprint,
		Status: string(candidate.Status), Version: candidate.Version,
		Source: toCandidateEndpointResponse(candidate.Source), Target: toCandidateEndpointResponse(candidate.Target),
		ProposedRelationType: string(candidate.SuggestedRelationType), Confidence: candidate.Confidence,
		Reason: candidate.Reason, DiscoveryMethods: discoveryMethods, Evidence: evidence,
		Generation:              toCandidateGenerationResponse(candidate.Generation),
		ReopenedReason:          optionalCandidateString(string(candidate.ReopenedReason)),
		ReopenedFromCandidateID: optionalCandidateID(candidate.ReopenedFromCandidateID),
		ProposalID:              optionalCandidateID(candidate.ProposalID), DeferredUntil: optionalCandidateTime(candidate.ResumeAfter),
		CreatedAt: formatCandidateTime(candidate.CreatedAt), UpdatedAt: formatCandidateTime(candidate.UpdatedAt),
	}
}

func toCandidateEndpointResponse(endpoint graphdomain.SemanticLinkCandidateEndpoint) candidateEndpointResponse {
	return candidateEndpointResponse{
		Type: string(endpoint.Ref.Type), ID: string(endpoint.Ref.ID), Version: endpoint.Version,
		Summary: endpoint.Summary, Excerpt: endpoint.Excerpt,
	}
}

func toCandidateGenerationResponse(generation graphdomain.SemanticLinkCandidateGeneration) candidateGenerationResponse {
	return candidateGenerationResponse{
		IndexVersionID:     optionalCandidateID(generation.IndexVersionID),
		EmbeddingVersionID: optionalCandidateID(generation.EmbeddingVersionID),
		RerankVersionID:    optionalCandidateID(generation.RerankVersionID),
		ModelVersion:       generation.ModelVersion, ModelProfileVersion: generation.ModelProfileVersion,
		PromptVersion: generation.PromptVersion, SchemaVersion: generation.SchemaVersion,
		ModelRunID: optionalCandidateID(generation.ModelRunID), RuleID: optionalCandidateID(generation.RuleID),
		RuleVersion: generation.RuleVersion,
	}
}

func toCandidateDecisionReceiptResponse(receipt graphapp.CandidateDecisionReceipt) candidateDecisionReceiptResponse {
	return candidateDecisionReceiptResponse{
		ID: string(receipt.ID), CandidateID: string(receipt.CandidateID), WorkspaceID: string(receipt.WorkspaceID),
		Action: string(receipt.Action), Status: string(receipt.Status), Version: receipt.Version,
		ProposalID: optionalCandidateID(receipt.ProposalID), CreatedAt: formatCandidateTime(receipt.CreatedAt),
		UpdatedAt: formatCandidateTime(receipt.UpdatedAt),
	}
}

func toCandidateScanAcceptanceResponse(result graphapp.SemanticLinkScanStartResult) candidateScanAcceptanceResponse {
	return candidateScanAcceptanceResponse{
		ScanID: string(result.Scan.ID), WorkflowRunID: string(result.Scan.WorkflowRunID),
		Status: string(result.Scan.Status), Version: result.Scan.Version, StatusURL: result.StatusURL,
	}
}

func toCandidateScanResponse(scan graphdomain.SemanticLinkScan) candidateScanResponse {
	var lastError *candidateScanErrorResponse
	if scan.LastError != nil {
		lastError = &candidateScanErrorResponse{Stage: scan.LastError.Stage, Code: scan.LastError.Code, Retryable: scan.LastError.Retryable}
	}
	scope := candidateScanScopeResponse{Kind: string(scan.Scope.Type)}
	if scan.Scope.Type == graphdomain.SemanticLinkScanScopeSmartCollection {
		scope.CollectionID = scan.Scope.Ref
		scope.CollectionVersion = scan.Scope.Version
		scope.QueryHash = scan.Scope.QueryHash
		scope.ReadModelRevision = scan.Scope.ReadModelRevision
	} else {
		scope.TopicID = scan.Scope.Ref
	}
	return candidateScanResponse{
		ID: string(scan.ID), WorkspaceID: string(scan.WorkspaceID),
		Scope:  scope,
		Status: string(scan.Status), WorkflowRunID: string(scan.WorkflowRunID), Version: scan.Version,
		StatusURL:  graphapp.SemanticLinkScanStatusURL(scan.WorkspaceID, scan.ID),
		TotalCount: scan.TotalNodes, ProcessedCount: scan.ProcessedNodes, CandidateCount: scan.CandidateCount,
		IgnoredCount: scan.SuppressedCount, FailedCount: scan.FailedCount,
		LastError: lastError,
		CreatedAt: formatCandidateTime(scan.CreatedAt), UpdatedAt: formatCandidateTime(scan.UpdatedAt),
		CompletedAt: optionalCandidateTime(scan.CompletedAt),
	}
}

func optionalCandidateID(value *foundation.ID) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func optionalCandidateString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalCandidateTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	result := formatCandidateTime(*value)
	return &result
}

func formatCandidateTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
