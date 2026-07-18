package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

const evidenceReferenceNotFoundCode = "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND"

var _ application.EvidenceReferenceStore = (*SearchRepository)(nil)

// LoadSourceVersionReference 读取指定 Workspace 内完整绑定的 Source Version 引用。
func (r *SearchRepository) LoadSourceVersionReference(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
) (domain.SourceVersionReference, error) {
	if err := validateEvidenceReferenceIDs(workspaceID, sourceVersionID); err != nil {
		return domain.SourceVersionReference{}, err
	}
	reference, versionRelativePath, err := scanSourceVersionReference(r.db.QueryRow(ctx, `
		SELECT
			s.workspace_id::text,
			s.id::text,
			sv.id::text,
			ca.id::text,
			s.type,
			s.logical_name,
			s.original_location,
			sv.original_content_location,
			sv.content_hash,
			sv.byte_size,
			sv.mime_type,
			sv.security_status,
			sv.captured_at
		FROM core.source_version AS sv
		JOIN core.source AS s
		  ON s.id=sv.source_id
		 AND s.workspace_id=$1
		JOIN core.content_artifact AS ca
		  ON ca.id=sv.content_artifact_id
		 AND ca.workspace_id=s.workspace_id
		 AND ca.content_hash=sv.content_hash
		 AND ca.byte_size=sv.byte_size
		WHERE sv.id=$2`, string(workspaceID), string(sourceVersionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SourceVersionReference{}, notFound(evidenceReferenceNotFoundCode, err)
	}
	if err != nil {
		return domain.SourceVersionReference{}, classify(err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	if reference.WorkspaceID != workspaceID || reference.SourceVersionID != sourceVersionID || reference.RelativePath != versionRelativePath {
		return domain.SourceVersionReference{}, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source version reference binding is inconsistent"))
	}
	if err := domain.ValidateSourceVersionReference(reference); err != nil {
		return domain.SourceVersionReference{}, err
	}
	return reference, nil
}

// LoadSourceSpanReference 读取 Source Version、Artifact、Projection 与 Span 全绑定的引用。
func (r *SearchRepository) LoadSourceSpanReference(
	ctx context.Context,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
	spanID foundation.ID,
) (domain.SourceSpanReference, error) {
	if err := validateEvidenceReferenceIDs(workspaceID, sourceVersionID, spanID); err != nil {
		return domain.SourceSpanReference{}, err
	}
	var (
		reference                 domain.SourceSpanReference
		workspaceText, sourceText string
		versionText, artifactText string
		projectionText, spanText  string
		versionRelativePath       string
		selector                  []byte
	)
	err := r.db.QueryRow(ctx, `
		SELECT
			s.workspace_id::text,
			s.id::text,
			sv.id::text,
			ca.id::text,
			s.type,
			s.logical_name,
			s.original_location,
			sv.original_content_location,
			sv.content_hash,
			sv.byte_size,
			sv.mime_type,
			sv.security_status,
			sv.captured_at,
			pp.id::text,
			sp.id::text,
			sp.start_line,
			sp.end_line,
			sp.start_byte,
			sp.end_byte,
			sp.span_type,
			sp.selector,
			sp.excerpt_hash,
			sp.parser_version,
			sp.schema_version
		FROM core.source_version AS sv
		JOIN core.source AS s
		  ON s.id=sv.source_id
		 AND s.workspace_id=$1
		JOIN core.content_artifact AS ca
		  ON ca.id=sv.content_artifact_id
		 AND ca.workspace_id=s.workspace_id
		 AND ca.content_hash=sv.content_hash
		 AND ca.byte_size=sv.byte_size
		JOIN ingestion.source_version_projection AS svp
		  ON svp.source_version_id=sv.id
		 AND svp.workspace_id=s.workspace_id
		JOIN ingestion.parse_projection AS pp
		  ON pp.id=svp.parse_projection_id
		 AND pp.workspace_id=s.workspace_id
		 AND pp.content_artifact_id=ca.id
		JOIN ingestion.source_span AS sp
		  ON sp.id=$3
		 AND sp.workspace_id=s.workspace_id
		 AND sp.content_artifact_id=ca.id
		 AND sp.parse_projection_id=pp.id
		 AND sp.parser_version=pp.parser_version
		 AND sp.schema_version=pp.schema_version
		WHERE sv.id=$2`, string(workspaceID), string(sourceVersionID), string(spanID)).Scan(
		&workspaceText,
		&sourceText,
		&versionText,
		&artifactText,
		&reference.SourceVersion.SourceType,
		&reference.SourceVersion.LogicalName,
		&reference.SourceVersion.RelativePath,
		&versionRelativePath,
		&reference.SourceVersion.ContentHash,
		&reference.SourceVersion.ByteSize,
		&reference.SourceVersion.MediaType,
		&reference.SourceVersion.SecurityStatus,
		&reference.SourceVersion.CapturedAt,
		&projectionText,
		&spanText,
		&reference.Span.StartLine,
		&reference.Span.EndLine,
		&reference.Span.StartByte,
		&reference.Span.EndByte,
		&reference.SpanType,
		&selector,
		&reference.ExcerptHash,
		&reference.ParserVersion,
		&reference.SchemaVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SourceSpanReference{}, notFound(evidenceReferenceNotFoundCode, err)
	}
	if err != nil {
		return domain.SourceSpanReference{}, classify(err, "RETRIEVAL_EVIDENCE_REFERENCE_QUERY_FAILED")
	}
	if err := parseEvidenceReferenceIDs(&reference, workspaceText, sourceText, versionText, artifactText, projectionText, spanText); err != nil {
		return domain.SourceSpanReference{}, err
	}
	reference.Selector = append(json.RawMessage(nil), selector...)
	if reference.SourceVersion.WorkspaceID != workspaceID || reference.SourceVersion.SourceVersionID != sourceVersionID ||
		reference.SourceVersion.RelativePath != versionRelativePath || reference.Span.ID != spanID {
		return domain.SourceSpanReference{}, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source span reference binding is inconsistent"))
	}
	if err := domain.ValidateSourceSpanReference(reference); err != nil {
		return domain.SourceSpanReference{}, err
	}
	return reference, nil
}

func scanSourceVersionReference(row pgx.Row) (domain.SourceVersionReference, string, error) {
	var reference domain.SourceVersionReference
	var workspaceText, sourceText, versionText, artifactText string
	var versionRelativePath string
	if err := row.Scan(
		&workspaceText,
		&sourceText,
		&versionText,
		&artifactText,
		&reference.SourceType,
		&reference.LogicalName,
		&reference.RelativePath,
		&versionRelativePath,
		&reference.ContentHash,
		&reference.ByteSize,
		&reference.MediaType,
		&reference.SecurityStatus,
		&reference.CapturedAt,
	); err != nil {
		return domain.SourceVersionReference{}, "", err
	}
	if err := parseSourceVersionReferenceIDs(&reference, workspaceText, sourceText, versionText, artifactText); err != nil {
		return domain.SourceVersionReference{}, "", err
	}
	return reference, versionRelativePath, nil
}

func parseEvidenceReferenceIDs(
	reference *domain.SourceSpanReference,
	workspaceText, sourceText, versionText, artifactText, projectionText, spanText string,
) error {
	if err := parseSourceVersionReferenceIDs(&reference.SourceVersion, workspaceText, sourceText, versionText, artifactText); err != nil {
		return err
	}
	projectionID, err := foundation.ParseID(projectionText)
	if err != nil {
		return consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source span projection id is invalid"))
	}
	spanID, err := foundation.ParseID(spanText)
	if err != nil {
		return consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source span id is invalid"))
	}
	reference.ParseProjectionID = projectionID
	reference.Span.ID = spanID
	return nil
}

func parseSourceVersionReferenceIDs(reference *domain.SourceVersionReference, values ...string) error {
	if len(values) != 4 {
		return consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source version reference identity count is invalid"))
	}
	parsed := make([]foundation.ID, len(values))
	for index, value := range values {
		id, err := foundation.ParseID(value)
		if err != nil {
			return consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("source version reference identity is invalid"))
		}
		parsed[index] = id
	}
	reference.WorkspaceID = parsed[0]
	reference.SourceID = parsed[1]
	reference.SourceVersionID = parsed[2]
	reference.ContentArtifactID = parsed[3]
	return nil
}

func validateEvidenceReferenceIDs(values ...foundation.ID) error {
	for _, value := range values {
		parsed, err := foundation.ParseID(string(value))
		if err != nil || parsed != value {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid, false, errors.New("evidence reference identity is invalid"))
		}
	}
	return nil
}
