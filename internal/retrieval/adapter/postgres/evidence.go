package postgres

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const evidenceReferenceNotFoundCode = "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND"

const sourceVersionReferenceSQL = `
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
		COALESCE(attempt.security_status,sv.security_status),
		COALESCE(attempt.status,''),
		COALESCE(run.status,''),
		COALESCE(manifest.selection_status,''),
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
	LEFT JOIN LATERAL (
		SELECT status,security_status,workflow_run_id
		FROM ingestion.attempt
		WHERE source_version_id=sv.id
		ORDER BY started_at DESC,id DESC
		LIMIT 1
	) AS attempt ON true
	LEFT JOIN workflow.run AS run ON run.id=attempt.workflow_run_id
	LEFT JOIN retrieval.index_version AS active_index
	  ON active_index.workspace_id=s.workspace_id
	 AND active_index.status='active'
	LEFT JOIN retrieval.index_manifest_source AS manifest
	  ON manifest.index_version_id=active_index.id
	 AND manifest.source_id=s.id
	 AND (manifest.selection_status='excluded' OR manifest.source_version_id=sv.id)
	WHERE sv.id=$2`

const sourceVersionReferenceBatchSQL = `
	WITH requested(source_version_id, ordinality) AS (
		SELECT id, ordinality FROM unnest($2::uuid[]) WITH ORDINALITY AS input(id, ordinality)
	)
	SELECT requested.ordinality,
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
		COALESCE(attempt.security_status,sv.security_status),
		COALESCE(attempt.status,''),
		COALESCE(run.status,''),
		COALESCE(manifest.selection_status,''),
		sv.captured_at
	FROM requested
	JOIN core.source_version AS sv ON sv.id=requested.source_version_id
	JOIN core.source AS s ON s.id=sv.source_id AND s.workspace_id=$1
	JOIN core.content_artifact AS ca ON ca.id=sv.content_artifact_id AND ca.workspace_id=s.workspace_id AND ca.content_hash=sv.content_hash AND ca.byte_size=sv.byte_size
	LEFT JOIN LATERAL (
		SELECT status,security_status,workflow_run_id
		FROM ingestion.attempt
		WHERE source_version_id=sv.id
		ORDER BY started_at DESC,id DESC
		LIMIT 1
	) AS attempt ON true
	LEFT JOIN workflow.run AS run ON run.id=attempt.workflow_run_id
	LEFT JOIN retrieval.index_version AS active_index ON active_index.workspace_id=s.workspace_id AND active_index.status='active'
	LEFT JOIN retrieval.index_manifest_source AS manifest ON manifest.index_version_id=active_index.id AND manifest.source_id=s.id AND (manifest.selection_status='excluded' OR manifest.source_version_id=sv.id)
	WHERE sv.id=requested.source_version_id
	ORDER BY requested.ordinality`

const sourceSpanReferenceSQL = `
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
		sp.evidence_kind,
		sp.derived_excerpt,
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
	WHERE sv.id=$2`

type storedCitationReference struct {
	WorkspaceID, IndexVersionID, ChunkID  string
	SourceID, SourceVersionID, ArtifactID string
	ProjectionID, SpanID                  string
	SourceType, LogicalName               string
	RelativePath, VersionRelativePath     string
	ContentHash, MediaType                string
	SecurityStatus                        string
	ByteSize                              int64
	CapturedAt                            time.Time
	StartLine, EndLine                    int32
	StartByte, EndByte                    int64
	SpanType                              string
	Selector                              json.RawMessage
	ExcerptHash, ParserVersion            string
	EvidenceKind                          domain.EvidenceKind
	DerivedExcerpt, SchemaVersion         string
}

func (value storedCitationReference) binding() (application.CitationSourceSpanBinding, error) {
	queryIDs := []string{value.WorkspaceID, value.IndexVersionID, value.ChunkID, value.SourceVersionID, value.SpanID}
	parsedQuery := make([]foundation.ID, len(queryIDs))
	for index, text := range queryIDs {
		id, err := foundation.ParseID(text)
		if err != nil {
			return application.CitationSourceSpanBinding{}, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("citation query identity is invalid"))
		}
		parsedQuery[index] = id
	}
	query := domain.CitationReferenceQuery{
		WorkspaceID: parsedQuery[0], IndexVersionID: parsedQuery[1], ChunkID: parsedQuery[2],
		SourceVersionID: parsedQuery[3], SourceSpanID: parsedQuery[4],
	}
	if err := domain.ValidateCitationReferenceQuery(query); err != nil {
		return application.CitationSourceSpanBinding{}, err
	}
	reference := domain.SourceSpanReference{
		SourceVersion: domain.SourceVersionReference{
			SourceType: value.SourceType, LogicalName: value.LogicalName, RelativePath: value.RelativePath,
			ContentHash: value.ContentHash, ByteSize: value.ByteSize, MediaType: value.MediaType,
			SecurityStatus: value.SecurityStatus, CapturedAt: value.CapturedAt,
		},
		Span: domain.EvidenceSpan{
			StartLine: value.StartLine, EndLine: value.EndLine, StartByte: value.StartByte, EndByte: value.EndByte,
		},
		SpanType: value.SpanType, Selector: append(json.RawMessage(nil), value.Selector...), ExcerptHash: value.ExcerptHash,
		EvidenceKind: value.EvidenceKind, DerivedExcerpt: value.DerivedExcerpt,
		ParserVersion: value.ParserVersion, SchemaVersion: value.SchemaVersion,
	}
	if err := parseEvidenceReferenceIDs(&reference, value.WorkspaceID, value.SourceID, value.SourceVersionID, value.ArtifactID, value.ProjectionID, value.SpanID); err != nil {
		return application.CitationSourceSpanBinding{}, err
	}
	if value.RelativePath != value.VersionRelativePath || reference.SourceVersion.WorkspaceID != query.WorkspaceID ||
		reference.SourceVersion.SourceVersionID != query.SourceVersionID || reference.Span.ID != query.SourceSpanID {
		return application.CitationSourceSpanBinding{}, consistency(domain.ErrorCodeEvidenceReferenceInvalid, errors.New("citation source span binding is inconsistent"))
	}
	if err := domain.ValidateSourceSpanReference(reference); err != nil {
		return application.CitationSourceSpanBinding{}, err
	}
	return application.CitationSourceSpanBinding{Query: query, Reference: reference}, nil
}

const citationReferenceBatchSQL = `
WITH requested AS MATERIALIZED (
	SELECT
		workspace_id,
		index_version_id,
		chunk_id,
		source_version_id,
		source_span_id,
		ordinality
	FROM unnest($1::uuid[],$2::uuid[],$3::uuid[],$4::uuid[],$5::uuid[])
		WITH ORDINALITY AS value(workspace_id,index_version_id,chunk_id,source_version_id,source_span_id,ordinality)
), bound AS (
	SELECT requested.ordinality,
		jsonb_build_object(
			'WorkspaceID',s.workspace_id::text,
			'IndexVersionID',idx.id::text,
			'ChunkID',chunk.id::text,
			'SourceID',s.id::text,
			'SourceVersionID',sv.id::text,
			'ArtifactID',ca.id::text,
			'ProjectionID',pp.id::text,
			'SpanID',sp.id::text,
			'SourceType',s.type,
			'LogicalName',s.logical_name,
			'RelativePath',s.original_location,
			'VersionRelativePath',sv.original_content_location,
			'ContentHash',sv.content_hash,
			'ByteSize',sv.byte_size,
			'MediaType',sv.mime_type,
			'SecurityStatus',sv.security_status,
			'CapturedAt',sv.captured_at,
			'StartLine',sp.start_line,
			'EndLine',sp.end_line,
			'StartByte',sp.start_byte,
			'EndByte',sp.end_byte,
			'SpanType',sp.span_type,
			'Selector',sp.selector,
			'ExcerptHash',sp.excerpt_hash,
			'EvidenceKind',sp.evidence_kind,
			'DerivedExcerpt',sp.derived_excerpt,
			'ParserVersion',sp.parser_version,
			'SchemaVersion',sp.schema_version
		) AS reference
	FROM requested
	JOIN retrieval.index_version AS idx
	  ON idx.id=requested.index_version_id
	 AND idx.workspace_id=requested.workspace_id
	JOIN retrieval.index_manifest_chunk AS manifest
	  ON manifest.index_version_id=idx.id
	 AND manifest.workspace_id=idx.workspace_id
	 AND manifest.chunk_id=requested.chunk_id
	JOIN ingestion.canonical_chunk AS chunk
	  ON chunk.id=manifest.chunk_id
	 AND chunk.workspace_id=manifest.workspace_id
	 AND chunk.content_hash=manifest.content_hash
	JOIN retrieval.index_manifest_source AS source_manifest
	  ON source_manifest.index_version_id=idx.id
	 AND source_manifest.workspace_id=idx.workspace_id
	 AND source_manifest.source_version_id=requested.source_version_id
	 AND source_manifest.parse_projection_id=chunk.parse_projection_id
	 AND source_manifest.selection_status='included'
	JOIN core.source_version AS sv
	  ON sv.id=source_manifest.source_version_id
	 AND sv.source_id=source_manifest.source_id
	JOIN core.source AS s
	  ON s.id=source_manifest.source_id
	 AND s.workspace_id=idx.workspace_id
	JOIN core.content_artifact AS ca
	  ON ca.id=sv.content_artifact_id
	 AND ca.workspace_id=idx.workspace_id
	 AND ca.content_hash=sv.content_hash
	 AND ca.byte_size=sv.byte_size
	JOIN ingestion.parse_projection AS pp
	  ON pp.id=source_manifest.parse_projection_id
	 AND pp.workspace_id=idx.workspace_id
	 AND pp.content_artifact_id=ca.id
	JOIN ingestion.source_span AS sp
	  ON sp.id=chunk.source_span_id
	 AND sp.id=requested.source_span_id
	 AND sp.workspace_id=idx.workspace_id
	 AND sp.content_artifact_id=ca.id
	 AND sp.parse_projection_id=pp.id
	 AND sp.parser_version=pp.parser_version
	 AND sp.schema_version=pp.schema_version
)
SELECT COALESCE(jsonb_agg(reference ORDER BY ordinality),'[]'::jsonb) FROM bound`

const provenanceCitationBatchSQL = `
WITH requested AS MATERIALIZED (
	SELECT workspace_id,source_version_id,source_span_id,ordinality
	FROM unnest($1::uuid[],$2::uuid[],$3::uuid[])
		WITH ORDINALITY AS value(workspace_id,source_version_id,source_span_id,ordinality)
), ranked AS (
	SELECT requested.ordinality,requested.workspace_id,idx.id AS index_version_id,
		chunk.id AS chunk_id,requested.source_version_id,requested.source_span_id,
		row_number() OVER (PARTITION BY requested.ordinality ORDER BY chunk.sequence,chunk.id) AS rank
	FROM requested
	JOIN retrieval.index_version AS idx
	  ON idx.workspace_id=requested.workspace_id AND idx.status='active'
	JOIN retrieval.index_manifest_source AS source_manifest
	  ON source_manifest.index_version_id=idx.id
	 AND source_manifest.workspace_id=idx.workspace_id
	 AND source_manifest.source_version_id=requested.source_version_id
	 AND source_manifest.selection_status='included'
	JOIN ingestion.canonical_chunk AS chunk
	  ON chunk.workspace_id=idx.workspace_id
	 AND chunk.parse_projection_id=source_manifest.parse_projection_id
	 AND chunk.source_span_id=requested.source_span_id
	JOIN retrieval.index_manifest_chunk AS manifest
	  ON manifest.index_version_id=idx.id
	 AND manifest.workspace_id=idx.workspace_id
	 AND manifest.chunk_id=chunk.id
	 AND manifest.content_hash=chunk.content_hash
), bound AS (
	SELECT ordinality,jsonb_build_object(
		'WorkspaceID',workspace_id::text,
		'IndexVersionID',index_version_id::text,
		'ChunkID',chunk_id::text,
		'SourceVersionID',source_version_id::text,
		'SourceSpanID',source_span_id::text
	) AS reference
	FROM ranked WHERE rank=1
)
SELECT COALESCE(jsonb_agg(reference ORDER BY ordinality),'[]'::jsonb) FROM bound`

func scanSourceVersionReference(row interface{ Scan(...any) error }) (domain.SourceVersionReference, string, error) {
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
		&reference.IngestionStatus,
		&reference.WorkflowStatus,
		&reference.IndexStatus,
		&reference.CapturedAt,
	); err != nil {
		return domain.SourceVersionReference{}, "", err
	}
	if err := parseSourceVersionReferenceIDs(&reference, workspaceText, sourceText, versionText, artifactText); err != nil {
		return domain.SourceVersionReference{}, "", err
	}
	return reference, versionRelativePath, nil
}

func scanSourceSpanReference(row interface{ Scan(...any) error }) (domain.SourceSpanReference, string, error) {
	var (
		reference                 domain.SourceSpanReference
		workspaceText, sourceText string
		versionText, artifactText string
		projectionText, spanText  string
		versionRelativePath       string
		selector                  []byte
	)
	if err := row.Scan(
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
		&reference.EvidenceKind,
		&reference.DerivedExcerpt,
		&reference.ParserVersion,
		&reference.SchemaVersion,
	); err != nil {
		return domain.SourceSpanReference{}, "", err
	}
	if err := parseEvidenceReferenceIDs(&reference, workspaceText, sourceText, versionText, artifactText, projectionText, spanText); err != nil {
		return domain.SourceSpanReference{}, "", err
	}
	reference.Selector = append(json.RawMessage(nil), selector...)
	return reference, versionRelativePath, nil
}

func scanSourceVersionReferenceWithOrdinal(row interface{ Scan(...any) error }, ordinal *int64) (domain.SourceVersionReference, string, error) {
	var reference domain.SourceVersionReference
	var workspaceText, sourceText, versionText, artifactText string
	var versionRelativePath string
	if err := row.Scan(
		ordinal,
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
		&reference.IngestionStatus,
		&reference.WorkflowStatus,
		&reference.IndexStatus,
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
