package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	evidenceReferenceServiceUnavailableCode = "RETRIEVAL_EVIDENCE_REFERENCE_SERVICE_UNAVAILABLE"
	evidenceArtifactInvalidCode             = "RETRIEVAL_EVIDENCE_ARTIFACT_INVALID"
)

// EvidenceReferenceStore 读取 Source Version 与 Source Span 的不可变数据库绑定。
type EvidenceReferenceStore interface {
	// LoadSourceVersionReference 只返回指定 Workspace 内的 Source Version 引用。
	LoadSourceVersionReference(context.Context, foundation.ID, foundation.ID) (domain.SourceVersionReference, error)
	// LoadSourceSpanReference 必须证明 Source Version、Parse Projection、Content Artifact 与 Span 全绑定。
	LoadSourceSpanReference(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SourceSpanReference, error)
}

// CitationEvidenceStore 按完整 frozen Index Citation tuple 加载 Source Span 绑定。
type CitationEvidenceStore interface {
	// LoadCitationSourceSpanReferences 必须单批证明 Index、Chunk、Source Version、Projection 与 Span。
	LoadCitationSourceSpanReferences(context.Context, []domain.CitationReferenceQuery) ([]CitationSourceSpanBinding, error)
}

// CitationSourceSpanBinding 保留完整 Citation 查询与不可变 Source Span 的精确对应。
type CitationSourceSpanBinding struct {
	Query     domain.CitationReferenceQuery
	Reference domain.SourceSpanReference
}

// OpenedCitationEvidence 返回完整 Citation 查询与经 Artifact 复核的 Source Span。
type OpenedCitationEvidence struct {
	Query domain.CitationReferenceQuery
	View  SourceSpanView
}

// EvidenceArtifactReader 通过安全 Workspace/Content Artifact 边界读取不可变原始字节。
type EvidenceArtifactReader interface {
	// ReadEvidenceArtifact 不接受路径；调用方只能使用 Workspace 与 Source Version 身份读取。
	ReadEvidenceArtifact(context.Context, foundation.ID, foundation.ID) (EvidenceArtifact, error)
}

// EvidenceArtifact 是 Application 用于复核引用的最小不可变内容快照。
type EvidenceArtifact struct {
	WorkspaceID       foundation.ID
	SourceVersionID   foundation.ID
	ContentArtifactID foundation.ID
	ContentHash       string
	ByteSize          int64
	Bytes             []byte
}

// SourceSpanView 返回可打开 Source Span 的不可变元数据与有界 excerpt。
type SourceSpanView struct {
	Reference        domain.SourceSpanReference
	Excerpt          string
	ExcerptTruncated bool
}

// EvidenceReferenceService 编排数据库引用与不可变 Content Artifact 复核。
type EvidenceReferenceService struct {
	store  EvidenceReferenceStore
	reader EvidenceArtifactReader
}

// NewEvidenceReferenceService 创建 Evidence 引用查询服务；依赖缺失时拒绝启动。
func NewEvidenceReferenceService(store EvidenceReferenceStore, reader EvidenceArtifactReader) (*EvidenceReferenceService, error) {
	if nilDispatcherDependency(store) || nilDispatcherDependency(reader) {
		return nil, evidenceReferenceDependencyError("evidence reference store and artifact reader are required")
	}
	return &EvidenceReferenceService{store: store, reader: reader}, nil
}

// GetSourceVersion 返回指定 Workspace 内的 Source Version 公开引用元数据。
func (service *EvidenceReferenceService) GetSourceVersion(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
) (domain.SourceVersionReference, error) {
	if service == nil || nilDispatcherDependency(service.store) {
		return domain.SourceVersionReference{}, evidenceReferenceDependencyError("evidence reference service is unavailable")
	}
	if err := validateEvidenceReferenceRequest(workspaceID, sourceVersionID); err != nil {
		return domain.SourceVersionReference{}, err
	}
	reference, err := service.store.LoadSourceVersionReference(ctx, workspaceID, sourceVersionID)
	if err != nil {
		return domain.SourceVersionReference{}, err
	}
	if err := domain.ValidateSourceVersionReference(reference); err != nil ||
		reference.WorkspaceID != workspaceID || reference.SourceVersionID != sourceVersionID {
		return domain.SourceVersionReference{}, evidenceArtifactConsistency("source version store returned an invalid binding")
	}
	reference.CapturedAt = reference.CapturedAt.UTC()
	return reference, nil
}

// GetSourceSpan 返回经不可变 Artifact 复核的 Source Span 与有界 UTF-8 excerpt。
func (service *EvidenceReferenceService) GetSourceSpan(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
	spanID foundation.ID,
) (SourceSpanView, error) {
	if service == nil || nilDispatcherDependency(service.store) || nilDispatcherDependency(service.reader) {
		return SourceSpanView{}, evidenceReferenceDependencyError("evidence reference service is unavailable")
	}
	if err := validateEvidenceReferenceRequest(workspaceID, sourceVersionID, spanID); err != nil {
		return SourceSpanView{}, err
	}
	reference, err := service.store.LoadSourceSpanReference(ctx, workspaceID, sourceVersionID, spanID)
	if err != nil {
		return SourceSpanView{}, err
	}
	return service.openSourceSpan(ctx, reference, workspaceID, sourceVersionID, spanID)
}

// OpenCitationEvidence 按完整 frozen Index Citation tuple 打开不可变 Source Span。
func (service *EvidenceReferenceService) OpenCitationEvidence(ctx context.Context, query domain.CitationReferenceQuery) (SourceSpanView, error) {
	opened, err := service.OpenCitationEvidenceBatch(ctx, []domain.CitationReferenceQuery{query})
	if err != nil {
		return SourceSpanView{}, err
	}
	if len(opened) != 1 || opened[0].Query != query {
		return SourceSpanView{}, evidenceArtifactConsistency("citation evidence batch returned an invalid single result")
	}
	return opened[0].View, nil
}

// OpenCitationEvidenceBatch 单批验证 Citation tuple，并且每个 Source Version 只读取一次 Artifact。
func (service *EvidenceReferenceService) OpenCitationEvidenceBatch(ctx context.Context, queries []domain.CitationReferenceQuery) ([]OpenedCitationEvidence, error) {
	if service == nil || nilDispatcherDependency(service.store) || nilDispatcherDependency(service.reader) {
		return nil, evidenceReferenceDependencyError("citation evidence service is unavailable")
	}
	canonical, err := canonicalCitationQueries(queries)
	if err != nil {
		return nil, err
	}
	store, ok := service.store.(CitationEvidenceStore)
	if !ok || nilDispatcherDependency(store) {
		return nil, evidenceReferenceDependencyError("citation evidence store is unavailable")
	}
	bindings, err := store.LoadCitationSourceSpanReferences(ctx, canonical)
	if err != nil {
		return nil, err
	}
	if len(bindings) != len(canonical) {
		return nil, evidenceArtifactConsistency("citation evidence store returned an incomplete batch")
	}
	artifacts := make(map[foundation.ID]EvidenceArtifact)
	result := make([]OpenedCitationEvidence, len(canonical))
	for index, query := range canonical {
		binding := bindings[index]
		if binding.Query != query {
			return nil, evidenceArtifactConsistency("citation evidence store returned an out-of-order binding")
		}
		artifact, exists := artifacts[query.SourceVersionID]
		if !exists {
			artifact, err = service.reader.ReadEvidenceArtifact(ctx, query.WorkspaceID, query.SourceVersionID)
			if err != nil {
				return nil, err
			}
			if err := validateEvidenceArtifact(binding.Reference.SourceVersion, artifact); err != nil {
				return nil, err
			}
			artifacts[query.SourceVersionID] = artifact
		}
		view, err := openSourceSpanFromArtifact(binding.Reference, artifact, query.WorkspaceID, query.SourceVersionID, query.SourceSpanID)
		if err != nil {
			return nil, err
		}
		result[index] = OpenedCitationEvidence{Query: query, View: view}
	}
	return result, nil
}

func (service *EvidenceReferenceService) openSourceSpan(
	ctx context.Context,
	reference domain.SourceSpanReference,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
	spanID foundation.ID,
) (SourceSpanView, error) {
	if err := domain.ValidateSourceSpanReference(reference); err != nil ||
		reference.SourceVersion.WorkspaceID != workspaceID ||
		reference.SourceVersion.SourceVersionID != sourceVersionID || reference.Span.ID != spanID {
		return SourceSpanView{}, evidenceArtifactConsistency("source span store returned an invalid binding")
	}
	artifact, err := service.reader.ReadEvidenceArtifact(ctx, workspaceID, sourceVersionID)
	if err != nil {
		return SourceSpanView{}, err
	}
	if err := validateEvidenceArtifact(reference.SourceVersion, artifact); err != nil {
		return SourceSpanView{}, err
	}
	return openSourceSpanFromArtifact(reference, artifact, workspaceID, sourceVersionID, spanID)
}

func openSourceSpanFromArtifact(
	reference domain.SourceSpanReference,
	artifact EvidenceArtifact,
	workspaceID, sourceVersionID, spanID foundation.ID,
) (SourceSpanView, error) {
	if err := domain.ValidateSourceSpanReference(reference); err != nil || reference.SourceVersion.WorkspaceID != workspaceID ||
		reference.SourceVersion.SourceVersionID != sourceVersionID || reference.Span.ID != spanID {
		return SourceSpanView{}, evidenceArtifactConsistency("source span store returned an invalid binding")
	}
	excerpt, truncated, err := evidenceExcerpt(reference, artifact.Bytes)
	if err != nil {
		return SourceSpanView{}, err
	}
	reference.SourceVersion.CapturedAt = reference.SourceVersion.CapturedAt.UTC()
	reference.Selector = append([]byte(nil), reference.Selector...)
	return SourceSpanView{Reference: reference, Excerpt: excerpt, ExcerptTruncated: truncated}, nil
}

func canonicalCitationQueries(queries []domain.CitationReferenceQuery) ([]domain.CitationReferenceQuery, error) {
	if len(queries) == 0 || len(queries) > 500 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("citation reference batch count is invalid"))
	}
	canonical := append([]domain.CitationReferenceQuery(nil), queries...)
	for _, query := range canonical {
		if err := domain.ValidateCitationReferenceQuery(query); err != nil {
			return nil, err
		}
		if query.WorkspaceID != canonical[0].WorkspaceID || query.IndexVersionID != canonical[0].IndexVersionID {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("citation reference batch crosses workspace or index"))
		}
	}
	slices.SortFunc(canonical, func(left, right domain.CitationReferenceQuery) int {
		for _, pair := range [][2]foundation.ID{{left.ChunkID, right.ChunkID}, {left.SourceVersionID, right.SourceVersionID}, {left.SourceSpanID, right.SourceSpanID}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	for index := 1; index < len(canonical); index++ {
		if canonical[index] == canonical[index-1] {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("citation reference batch contains duplicates"))
		}
	}
	return canonical, nil
}

func validateEvidenceReferenceRequest(values ...foundation.ID) error {
	for _, value := range values {
		parsed, err := foundation.ParseID(string(value))
		if err != nil || parsed != value {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("evidence reference identity is invalid"))
		}
	}
	return nil
}

func validateEvidenceArtifact(reference domain.SourceVersionReference, artifact EvidenceArtifact) error {
	if artifact.WorkspaceID != reference.WorkspaceID || artifact.SourceVersionID != reference.SourceVersionID ||
		artifact.ContentArtifactID != reference.ContentArtifactID || artifact.ContentHash != reference.ContentHash ||
		artifact.ByteSize != reference.ByteSize || artifact.ByteSize != int64(len(artifact.Bytes)) ||
		!validEvidenceArtifactHash(artifact.ContentHash, artifact.Bytes) {
		return evidenceArtifactConsistency("content artifact does not match source version reference")
	}
	return nil
}

func evidenceExcerpt(reference domain.SourceSpanReference, content []byte) (string, bool, error) {
	start, end := reference.Span.StartByte, reference.Span.EndByte
	if start < 0 || end < start || end > int64(len(content)) {
		return "", false, evidenceArtifactConsistency("source span byte range exceeds content artifact")
	}
	full := content[int(start):int(end)]
	if !utf8.Valid(full) || !validEvidenceArtifactHash(reference.ExcerptHash, full) {
		return "", false, evidenceArtifactConsistency("source span excerpt does not match immutable artifact")
	}
	if len(full) <= domain.MaxEvidenceSnippetBytes {
		return string(full), false, nil
	}
	limit := domain.MaxEvidenceSnippetBytes
	for limit > 0 && !utf8.Valid(full[:limit]) {
		limit--
	}
	if limit == 0 {
		return "", false, evidenceArtifactConsistency("source span excerpt cannot be safely truncated")
	}
	return string(full[:limit]), true, nil
}

func validEvidenceArtifactHash(expected string, value []byte) bool {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]) == expected
}

func evidenceReferenceDependencyError(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, evidenceReferenceServiceUnavailableCode, false, errors.New(message))
}

func evidenceArtifactConsistency(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, evidenceArtifactInvalidCode, false, errors.New(message))
}
