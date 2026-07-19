package workflow

import (
	"context"
	"errors"
	"reflect"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const errorCodeEvidenceOpenInvalid = "AGENT_WORKFLOW_EVIDENCE_OPEN_INVALID"

type evidenceReference interface {
	OpenCitationEvidenceBatch(context.Context, []retrievaldomain.CitationReferenceQuery) ([]retrievalapplication.OpenedCitationEvidence, error)
}

// ReferenceOpener 通过 Retrieval EvidenceReferenceService 打开不可变 Source Span。
type ReferenceOpener struct{ reference evidenceReference }

// NewReferenceOpener 创建不接受路径或调用方 excerpt 的 Evidence opener。
func NewReferenceOpener(reference evidenceReference) (*ReferenceOpener, error) {
	if nilReference(reference) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("retrieval evidence reference is unavailable"))
	}
	return &ReferenceOpener{reference: reference}, nil
}

// Open 复核 Citation 身份并只返回服务端不可变 excerpt。
func (opener *ReferenceOpener) Open(ctx context.Context, citation agentdomain.Citation) (agentapplication.OpenedEvidence, error) {
	opened, err := opener.OpenBatch(ctx, []agentdomain.Citation{citation})
	if err != nil {
		return agentapplication.OpenedEvidence{}, err
	}
	if len(opened) != 1 || opened[0].Citation != citation {
		return agentapplication.OpenedEvidence{}, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened citation batch returned an invalid single result"))
	}
	return opened[0], nil
}

// OpenBatch 单批复核 Citation 身份并返回与输入顺序一致的服务端 excerpt。
func (opener *ReferenceOpener) OpenBatch(ctx context.Context, citations []agentdomain.Citation) ([]agentapplication.OpenedEvidence, error) {
	if opener == nil || nilReference(opener.reference) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("retrieval evidence opener is unavailable"))
	}
	if len(citations) == 0 || len(citations) > agentapplication.MaxRelationEvidence {
		return nil, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("relation citation batch count is invalid"))
	}
	queries := make([]retrievaldomain.CitationReferenceQuery, len(citations))
	byQuery := make(map[retrievaldomain.CitationReferenceQuery]agentdomain.Citation, len(citations))
	for index, citation := range citations {
		if err := citation.Validate(); err != nil {
			return nil, err
		}
		query := retrievaldomain.CitationReferenceQuery{
			WorkspaceID: citation.WorkspaceID, IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
			SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		}
		if _, duplicate := byQuery[query]; duplicate {
			return nil, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("relation citation batch contains duplicates"))
		}
		queries[index], byQuery[query] = query, citation
	}
	views, err := opener.reference.OpenCitationEvidenceBatch(ctx, queries)
	if err != nil {
		return nil, err
	}
	if len(views) != len(citations) {
		return nil, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened relation citation batch is incomplete"))
	}
	openedByQuery := make(map[retrievaldomain.CitationReferenceQuery]agentapplication.OpenedEvidence, len(views))
	for _, item := range views {
		citation, exists := byQuery[item.Query]
		if !exists || item.View.Reference.SourceVersion.WorkspaceID != citation.WorkspaceID ||
			item.View.Reference.SourceVersion.SourceVersionID != citation.SourceVersionID || item.View.Reference.Span.ID != citation.SourceSpanID ||
			item.View.Excerpt == "" || !utf8.ValidString(item.View.Excerpt) {
			return nil, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened source span differs from citation identity"))
		}
		openedByQuery[item.Query] = agentapplication.OpenedEvidence{Citation: citation, Excerpt: item.View.Excerpt}
	}
	result := make([]agentapplication.OpenedEvidence, len(citations))
	for index, query := range queries {
		opened, exists := openedByQuery[query]
		if !exists {
			return nil, workflowError(foundation.ErrorConsistencyViolation, errorCodeEvidenceOpenInvalid, false, errors.New("opened relation citation batch is incomplete"))
		}
		result[index] = opened
	}
	return result, nil
}

func nilReference(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
