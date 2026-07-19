package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// MaxRetrievalCandidates 是 Agent 单次检索允许请求的最大 Chunk 数。
	MaxRetrievalCandidates int32 = 100
	// MaxRetrievedEvidence 是进入单批 Knowledge Eligibility 的最大具体 Provenance 数。
	MaxRetrievedEvidence     = 500
	maxRetrievalQueryBytes   = 8 * 1024
	maxRetrievedExcerptBytes = 32 * 1024
)

const (
	errorCodeRetrievalPortMissing    = "AGENT_RETRIEVAL_PORT_MISSING"
	errorCodeRetrievalRequestInvalid = "AGENT_RETRIEVAL_REQUEST_INVALID"
	errorCodeRetrievalResultInvalid  = "AGENT_RETRIEVAL_RESULT_INVALID"
	errorCodeEligibilityPortMissing  = "AGENT_EVIDENCE_ELIGIBILITY_PORT_MISSING"
)

// RetrievalRequest 是 Agent 发起批准知识检索所需的最小有界输入。
type RetrievalRequest struct {
	WorkspaceID foundation.ID
	Query       string
	Limit       int32
}

// RetrievedEvidence 是一次 Search 结果中已经选择具体 Provenance 的候选证据。
type RetrievedEvidence struct {
	Citation      domain.Citation
	SearchExcerpt string
	CapturedAt    time.Time
}

// RetrievalBatch 保存实际 Index 版本及最多 500 个具体 Provenance 候选。
type RetrievalBatch struct {
	WorkspaceID        foundation.ID
	IndexVersionID     foundation.ID
	EmbeddingVersionID *foundation.ID
	Items              []RetrievedEvidence
	Truncated          bool
}

// OpenedEvidence 是通过 Retrieval EvidenceReference seam 复核后的不可变 Span。
type OpenedEvidence struct {
	Citation domain.Citation
	Excerpt  string
}

// RetrievalPort 隐藏 Search、RRF、Artifact 与 Source Span 的 Retrieval 实现细节。
type RetrievalPort interface {
	// Retrieve 使用 Retrieval 自有有界 Search 返回具体 Provenance，不接受路径。
	Retrieve(context.Context, RetrievalRequest) (RetrievalBatch, error)
	// Open 通过 Citation 身份复核不可变 Source Span，不接受路径或模型生成 URL。
	Open(context.Context, domain.Citation) (OpenedEvidence, error)
	// OpenBatch 单批复核最多 500 个 Citation，并避免逐条数据库与 Artifact 读取。
	OpenBatch(context.Context, []domain.Citation) ([]OpenedEvidence, error)
}

// EvidenceEligibilityPort 复用 Knowledge Application 的单批资格查询契约。
type EvidenceEligibilityPort interface {
	// CheckEvidenceEligibility 按 Workspace 一次判断最多 500 个 Provenance。
	CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error)
}

// ValidateRetrievalRequest 校验 Agent Retrieval Port 的 Workspace、查询和有界数量。
func ValidateRetrievalRequest(request RetrievalRequest) error {
	if !canonicalApplicationID(request.WorkspaceID) || request.Limit <= 0 || request.Limit > MaxRetrievalCandidates {
		return applicationError(foundation.ErrorInvalidInput, errorCodeRetrievalRequestInvalid, false, errors.New("retrieval workspace or limit is invalid"))
	}
	if request.Query == "" || strings.TrimSpace(request.Query) != request.Query || len(request.Query) > maxRetrievalQueryBytes ||
		!utf8.ValidString(request.Query) || strings.ContainsRune(request.Query, '\x00') {
		return applicationError(foundation.ErrorInvalidInput, errorCodeRetrievalRequestInvalid, false, errors.New("retrieval query is empty, oversized, or invalid utf-8"))
	}
	return nil
}

func validateRetrievalBatchScope(workspaceID foundation.ID, batch RetrievalBatch) error {
	if !canonicalApplicationID(workspaceID) || batch.WorkspaceID != workspaceID ||
		!canonicalApplicationID(batch.IndexVersionID) || len(batch.Items) > MaxRetrievedEvidence {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeRetrievalResultInvalid, false, errors.New("retrieval result scope or count is invalid"))
	}
	if batch.EmbeddingVersionID != nil && !canonicalApplicationID(*batch.EmbeddingVersionID) {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeRetrievalResultInvalid, false, errors.New("retrieval embedding version is invalid"))
	}
	seen := make(map[string]struct{}, len(batch.Items))
	for _, item := range batch.Items {
		if err := item.Citation.Validate(); err != nil || item.Citation.WorkspaceID != batch.WorkspaceID ||
			item.Citation.IndexVersionID != batch.IndexVersionID || item.SearchExcerpt == "" ||
			len(item.SearchExcerpt) > maxRetrievedExcerptBytes || !utf8.ValidString(item.SearchExcerpt) || item.CapturedAt.IsZero() {
			return applicationError(foundation.ErrorConsistencyViolation, errorCodeRetrievalResultInvalid, false, errors.New("retrieval evidence binding is invalid"))
		}
		if _, duplicate := seen[item.Citation.ID]; duplicate {
			return applicationError(foundation.ErrorConsistencyViolation, errorCodeRetrievalResultInvalid, false, errors.New("retrieval evidence contains duplicate citation identities"))
		}
		seen[item.Citation.ID] = struct{}{}
	}
	return nil
}

func canonicalApplicationID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
