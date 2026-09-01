package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var _ application.EvidenceReferenceStore = (*GORMSearchRepository)(nil)
var _ application.CitationEvidenceStore = (*GORMSearchRepository)(nil)
var _ application.SourceVersionReferenceBatchStore = (*GORMSearchRepository)(nil)
var _ application.ScopedSourceVersionReferenceBatchStore = (*GORMSearchRepository)(nil)
var _ application.ProvenanceCitationStore = (*GORMSearchRepository)(nil)

// LoadSourceVersionReference 读取指定 Workspace 内完整绑定的 Source Version 引用。
func (repository *GORMSearchRepository) LoadSourceVersionReference(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
) (domain.SourceVersionReference, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SourceVersionReference{}, err
	}
	if err := validateEvidenceReferenceIDs(workspaceID, sourceVersionID); err != nil {
		return domain.SourceVersionReference{}, err
	}
	row, err := gormSearchRawRow(ctx, repository.database, sourceVersionReferenceSQL, string(workspaceID), string(sourceVersionID))
	if err != nil {
		return domain.SourceVersionReference{}, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	reference, versionRelativePath, err := scanSourceVersionReference(row)
	if gormSearchNoRows(err) {
		return domain.SourceVersionReference{}, notFound(evidenceReferenceNotFoundCode, err)
	}
	if err != nil {
		return domain.SourceVersionReference{}, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	if reference.WorkspaceID != workspaceID || reference.SourceVersionID != sourceVersionID || reference.RelativePath != versionRelativePath {
		return domain.SourceVersionReference{}, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source version reference binding is inconsistent"))
	}
	if err := domain.ValidateSourceVersionReference(reference); err != nil {
		return domain.SourceVersionReference{}, err
	}
	return reference, nil
}

// LoadSourceVersionReferences 以一次参数化查询批量读取 Source Version 引用，并按请求顺序返回。
func (repository *GORMSearchRepository) LoadSourceVersionReferences(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionIDs []foundation.ID,
) ([]domain.SourceVersionReference, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	return repository.loadSourceVersionReferences(ctx, repository.database.WithContext(ctx), workspaceID, sourceVersionIDs)
}

// LoadSourceVersionReferencesScoped 在 caller-owned live scope 内执行同一批量读取，不管理事务生命周期。
func (repository *GORMSearchRepository) LoadSourceVersionReferencesScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	sourceVersionIDs []foundation.ID,
) ([]domain.SourceVersionReference, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if scope == nil {
		return nil, searchInvalid("RETRIEVAL_EVIDENCE_TRANSACTION_SCOPE_INVALID", errors.New("evidence transaction scope is nil"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, searchInvalid("RETRIEVAL_EVIDENCE_TRANSACTION_SCOPE_INVALID", errors.New("evidence transaction scope is invalid or inactive"))
	}
	return repository.loadSourceVersionReferences(ctx, transaction.WithContext(ctx), workspaceID, sourceVersionIDs)
}

func (repository *GORMSearchRepository) loadSourceVersionReferences(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	sourceVersionIDs []foundation.ID,
) ([]domain.SourceVersionReference, error) {
	ids, err := validateGORMSourceVersionBatch(workspaceID, sourceVersionIDs)
	if err != nil {
		return nil, err
	}
	rows, err := gormSearchRawRows(ctx, database, sourceVersionReferenceBatchSQL, string(workspaceID), pq.Array(ids))
	if err != nil {
		return nil, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_BATCH_QUERY_FAILED")
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.SourceVersionReference, len(sourceVersionIDs))
	seenOrdinals := make(map[int64]struct{}, len(sourceVersionIDs))
	for rows.Next() {
		var ordinal int64
		reference, versionRelativePath, scanErr := scanSourceVersionReferenceWithOrdinal(rows, &ordinal)
		if scanErr != nil {
			return nil, gormSearchClassify(ctx, scanErr, "RETRIEVAL_EVIDENCE_REFERENCE_BATCH_SCAN_FAILED")
		}
		if ordinal < 1 || ordinal > int64(len(result)) {
			return nil, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source version batch ordinal is invalid"))
		}
		index := int(ordinal - 1)
		if _, duplicate := seenOrdinals[ordinal]; duplicate || reference.WorkspaceID != workspaceID ||
			reference.SourceVersionID != sourceVersionIDs[index] || reference.RelativePath != versionRelativePath {
			return nil, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source version batch binding is inconsistent"))
		}
		if err := domain.ValidateSourceVersionReference(reference); err != nil {
			return nil, err
		}
		seenOrdinals[ordinal] = struct{}{}
		result[index] = reference
	}
	if err := rows.Err(); err != nil {
		return nil, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_BATCH_QUERY_FAILED")
	}
	if err := rows.Close(); err != nil {
		return nil, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_BATCH_QUERY_FAILED")
	}
	if len(seenOrdinals) != len(result) {
		return nil, notFound(evidenceReferenceNotFoundCode, errors.New("one or more source version references were not found"))
	}
	return result, nil
}

func validateGORMSourceVersionBatch(workspaceID foundation.ID, sourceVersionIDs []foundation.ID) ([]string, error) {
	if err := validateEvidenceReferenceIDs(workspaceID); err != nil {
		return nil, err
	}
	if len(sourceVersionIDs) == 0 || len(sourceVersionIDs) > application.MaxSourceVersionBatchSize {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("source version batch count is invalid"))
	}
	ids := make([]string, len(sourceVersionIDs))
	seen := make(map[foundation.ID]struct{}, len(sourceVersionIDs))
	for index, sourceVersionID := range sourceVersionIDs {
		if err := validateEvidenceReferenceIDs(sourceVersionID); err != nil {
			return nil, err
		}
		if _, duplicate := seen[sourceVersionID]; duplicate {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("source version batch contains duplicates"))
		}
		seen[sourceVersionID] = struct{}{}
		ids[index] = string(sourceVersionID)
	}
	return ids, nil
}

// LoadSourceSpanReference 读取 Source Version、Artifact、Projection 与 Span 全绑定的引用。
func (repository *GORMSearchRepository) LoadSourceSpanReference(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
	spanID foundation.ID,
) (domain.SourceSpanReference, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SourceSpanReference{}, err
	}
	if err := validateEvidenceReferenceIDs(workspaceID, sourceVersionID, spanID); err != nil {
		return domain.SourceSpanReference{}, err
	}
	row, err := gormSearchRawRow(
		ctx, repository.database, sourceSpanReferenceSQL, string(workspaceID), string(sourceVersionID), string(spanID),
	)
	if err != nil {
		return domain.SourceSpanReference{}, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	reference, versionRelativePath, err := scanSourceSpanReference(row)
	if gormSearchNoRows(err) {
		return domain.SourceSpanReference{}, notFound(evidenceReferenceNotFoundCode, err)
	}
	if err != nil {
		return domain.SourceSpanReference{}, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	if reference.SourceVersion.WorkspaceID != workspaceID || reference.SourceVersion.SourceVersionID != sourceVersionID ||
		reference.SourceVersion.RelativePath != versionRelativePath || reference.Span.ID != spanID {
		return domain.SourceSpanReference{}, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source span reference binding is inconsistent"))
	}
	if err := domain.ValidateSourceSpanReference(reference); err != nil {
		return domain.SourceSpanReference{}, err
	}
	return reference, nil
}

// LoadCitationSourceSpanReference 用单条参数化 SQL 证明完整 frozen Index Citation tuple。
func (repository *GORMSearchRepository) LoadCitationSourceSpanReference(
	ctx context.Context,
	query domain.CitationReferenceQuery,
) (domain.SourceSpanReference, error) {
	bindings, err := repository.LoadCitationSourceSpanReferences(ctx, []domain.CitationReferenceQuery{query})
	if err != nil {
		return domain.SourceSpanReference{}, err
	}
	if len(bindings) != 1 || bindings[0].Query != query {
		return domain.SourceSpanReference{}, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("citation source span binding is incomplete"))
	}
	return bindings[0].Reference, nil
}

// LoadCitationSourceSpanReferences 用单条参数化 SQL 批量证明完整 frozen Index Citation tuple。
func (repository *GORMSearchRepository) LoadCitationSourceSpanReferences(
	ctx context.Context,
	queries []domain.CitationReferenceQuery,
) ([]application.CitationSourceSpanBinding, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if len(queries) == 0 || len(queries) > 500 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("citation reference batch count is invalid"))
	}
	workspaceIDs := make([]string, len(queries))
	indexIDs := make([]string, len(queries))
	chunkIDs := make([]string, len(queries))
	sourceVersionIDs := make([]string, len(queries))
	spanIDs := make([]string, len(queries))
	seen := make(map[domain.CitationReferenceQuery]struct{}, len(queries))
	for index, query := range queries {
		if err := domain.ValidateCitationReferenceQuery(query); err != nil {
			return nil, err
		}
		if index > 0 && (query.WorkspaceID != queries[0].WorkspaceID || query.IndexVersionID != queries[0].IndexVersionID) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("citation reference batch crosses workspace or index"))
		}
		if _, duplicate := seen[query]; duplicate {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("citation reference batch contains duplicates"))
		}
		seen[query] = struct{}{}
		workspaceIDs[index], indexIDs[index], chunkIDs[index] = string(query.WorkspaceID), string(query.IndexVersionID), string(query.ChunkID)
		sourceVersionIDs[index], spanIDs[index] = string(query.SourceVersionID), string(query.SourceSpanID)
	}
	row, err := gormSearchRawRow(
		ctx,
		repository.database,
		citationReferenceBatchSQL,
		pq.Array(workspaceIDs),
		pq.Array(indexIDs),
		pq.Array(chunkIDs),
		pq.Array(sourceVersionIDs),
		pq.Array(spanIDs),
	)
	if err != nil {
		return nil, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	var encoded []byte
	if err := row.Scan(&encoded); err != nil {
		return nil, gormSearchClassify(ctx, err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	var stored []storedCitationReference
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return nil, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("citation reference batch payload is invalid"))
	}
	if len(stored) != len(queries) {
		return nil, notFound(evidenceReferenceNotFoundCode, sql.ErrNoRows)
	}
	result := make([]application.CitationSourceSpanBinding, len(stored))
	for index, value := range stored {
		binding, parseErr := value.binding()
		if parseErr != nil {
			return nil, parseErr
		}
		if binding.Query != queries[index] {
			return nil, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("citation reference batch is out of order"))
		}
		result[index] = binding
	}
	return result, nil
}

// ResolveProvenanceCitationReferences 将正式知识来源批量解析到当前 Active Index 的确定性 Citation tuple。
func (repository *GORMSearchRepository) ResolveProvenanceCitationReferences(
	ctx context.Context,
	queries []domain.ProvenanceReferenceQuery,
) ([]domain.CitationReferenceQuery, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if len(queries) == 0 || len(queries) > 500 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("provenance reference batch count is invalid"))
	}
	workspaceIDs := make([]string, len(queries))
	sourceVersionIDs := make([]string, len(queries))
	spanIDs := make([]string, len(queries))
	seen := make(map[domain.ProvenanceReferenceQuery]struct{}, len(queries))
	for index, query := range queries {
		if err := domain.ValidateProvenanceReferenceQuery(query); err != nil {
			return nil, err
		}
		if index > 0 && query.WorkspaceID != queries[0].WorkspaceID {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("provenance reference batch crosses workspace"))
		}
		if _, duplicate := seen[query]; duplicate {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("provenance reference batch contains duplicates"))
		}
		seen[query] = struct{}{}
		workspaceIDs[index] = string(query.WorkspaceID)
		sourceVersionIDs[index] = string(query.SourceVersionID)
		spanIDs[index] = string(query.SourceSpanID)
	}
	row, err := gormSearchRawRow(
		ctx,
		repository.database,
		provenanceCitationBatchSQL,
		pq.Array(workspaceIDs),
		pq.Array(sourceVersionIDs),
		pq.Array(spanIDs),
	)
	if err != nil {
		return nil, gormSearchClassify(ctx, err, "RETRIEVAL_PROVENANCE_CITATION_QUERY_FAILED")
	}
	var encoded []byte
	if err := row.Scan(&encoded); err != nil {
		return nil, gormSearchClassify(ctx, err, "RETRIEVAL_PROVENANCE_CITATION_QUERY_FAILED")
	}
	var stored []struct {
		WorkspaceID, IndexVersionID, ChunkID, SourceVersionID, SourceSpanID string
	}
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return nil, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("provenance citation batch payload is invalid"))
	}
	if len(stored) != len(queries) {
		return nil, notFound(evidenceReferenceNotFoundCode, sql.ErrNoRows)
	}
	result := make([]domain.CitationReferenceQuery, len(stored))
	for index, value := range stored {
		ids := []string{value.WorkspaceID, value.IndexVersionID, value.ChunkID, value.SourceVersionID, value.SourceSpanID}
		parsed := make([]foundation.ID, len(ids))
		for idIndex, raw := range ids {
			id, parseErr := foundation.ParseID(raw)
			if parseErr != nil {
				return nil, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("provenance citation identity is invalid"))
			}
			parsed[idIndex] = id
		}
		citation := domain.CitationReferenceQuery{
			WorkspaceID: parsed[0], IndexVersionID: parsed[1], ChunkID: parsed[2],
			SourceVersionID: parsed[3], SourceSpanID: parsed[4],
		}
		requested := queries[index]
		if domain.ValidateCitationReferenceQuery(citation) != nil || citation.WorkspaceID != requested.WorkspaceID ||
			citation.SourceVersionID != requested.SourceVersionID || citation.SourceSpanID != requested.SourceSpanID {
			return nil, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("provenance citation batch is out of order"))
		}
		result[index] = citation
	}
	return result, nil
}
