package owner

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"gorm.io/gorm"
)

// GORMSynthesisSourceReader reuses the Workspace artifact reader. It never
// accepts a filesystem path and never treats a normalized chunk as raw evidence.
type GORMSynthesisSourceReader struct {
	database  *gorm.DB
	artifacts retrievalapp.EvidenceArtifactReader
}

var (
	_ organizingapp.SynthesisSourceReader = (*GORMSynthesisSourceReader)(nil)
	_ organizingapp.SynthesisSourceFence  = (*GORMSynthesisSourceReader)(nil)
)

func NewGORMSynthesisSourceReader(pool *platformpostgres.Pool, artifacts retrievalapp.EvidenceArtifactReader) (*GORMSynthesisSourceReader, error) {
	if pool == nil || nilDependency(artifacts) {
		return nil, synthesisSourceError(foundation.ErrorDependencyUnavailable, "SYNTHESIS_SOURCE_READER_UNAVAILABLE", true)
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	return &GORMSynthesisSourceReader{database: database, artifacts: artifacts}, nil
}

// The path comparison is exact and rooted in an immutable generated Document
// plus a real publication commit, never in a filename prefix. The hash branch
// also prevents importing an unchanged copy of a generated revision as evidence.
const synthesisDerivedSQL = `EXISTS (
	SELECT 1 FROM authoring.generated_document gd
	JOIN core.document d ON d.id=gd.document_id AND d.workspace_id=gd.workspace_id
	WHERE gd.workspace_id=s.workspace_id AND gd.origin_kind='SYNTHESIS_NOTE'
	AND ((d.canonical_path=s.original_location AND EXISTS (
		SELECT 1 FROM authoring.document_publication_binding b
		JOIN change_control.proposal_commit pc ON pc.proposal_id=b.proposal_id
		 AND pc.revision_id=b.proposal_revision_id AND pc.workspace_id=b.workspace_id
		 AND pc.target_path=b.target_path AND pc.target_mode=b.target_mode
		 AND pc.result_hash=b.content_hash
		WHERE b.document_id=d.id AND b.workspace_id=d.workspace_id
	)) OR EXISTS (
		SELECT 1 FROM authoring.generated_article_revision gr
		WHERE gr.document_id=d.id AND gr.workspace_id=d.workspace_id AND gr.content_hash=v.content_hash
	)))`

const synthesisSourceJoins = ` FROM core.source s
	JOIN core.source_version v ON v.source_id=s.id AND v.workspace_id=s.workspace_id
	JOIN core.content_artifact a ON a.id=v.content_artifact_id AND a.workspace_id=s.workspace_id
	 AND a.content_hash=v.content_hash AND a.byte_size=v.byte_size
	JOIN ingestion.source_version_projection vp ON vp.source_version_id=v.id AND vp.workspace_id=s.workspace_id
	JOIN ingestion.parse_projection pp ON pp.id=vp.parse_projection_id AND pp.workspace_id=s.workspace_id AND pp.content_artifact_id=a.id`

const synthesisSourceCurrentSQL = `(s.removed_at IS NULL
	AND NOT EXISTS (SELECT 1 FROM core.source_version newer WHERE newer.source_id=s.id
	 AND (newer.captured_at,newer.id)>(v.captured_at,v.id))
	AND EXISTS (SELECT 1 FROM ingestion.attempt ia
	 WHERE ia.workspace_id=s.workspace_id AND ia.source_version_id=v.id AND ia.parse_projection_id=pp.id
	 AND ia.status='chunked' AND ia.security_status='passed'
	 AND ia.parser_id=pp.parser_id AND ia.parser_version=pp.parser_version
	 AND ia.parser_config_hash=pp.parser_config_hash AND ia.schema_version=pp.schema_version)
	AND NOT EXISTS (SELECT 1 FROM ingestion.attempt ia
	 WHERE ia.workspace_id=s.workspace_id AND ia.source_version_id=v.id AND ia.security_status='quarantined'))`

type synthesisSourceState struct {
	Title     string     `gorm:"column:title"`
	RemovedAt *time.Time `gorm:"column:removed_at"`
	Current   bool       `gorm:"column:current"`
	Derived   bool       `gorm:"column:derived"`
}

func (reader *GORMSynthesisSourceReader) state(ctx context.Context, tx *gorm.DB, source domain.SynthesisSourceVersion) (synthesisSourceState, bool, error) {
	var state synthesisSourceState
	query := tx.WithContext(ctx).Raw(`SELECT s.logical_name AS title,s.removed_at,`+synthesisSourceCurrentSQL+` AS current,`+synthesisDerivedSQL+` AS derived`+synthesisSourceJoins+`
	 WHERE s.workspace_id=? AND s.id=? AND v.id=? AND a.id=? AND pp.id=? AND v.content_hash=?`,
		string(source.WorkspaceID), string(source.SourceID), string(source.SourceVersionID), string(source.ContentArtifactID), string(source.ParseProjectionID), source.ContentHash).Scan(&state)
	if query.Error != nil {
		return state, false, synthesisSourceDBError(ctx, query.Error)
	}
	return state, query.RowsAffected == 1, nil
}

func (reader *GORMSynthesisSourceReader) ready(ctx context.Context) error {
	if reader == nil || reader.database == nil || nilDependency(reader.artifacts) {
		return synthesisSourceError(foundation.ErrorDependencyUnavailable, "SYNTHESIS_SOURCE_READER_UNAVAILABLE", true)
	}
	if ctx == nil {
		return synthesisSourceError(foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid, false)
	}
	if err := ctx.Err(); err != nil {
		return synthesisSourceDBError(ctx, err)
	}
	return nil
}

func (reader *GORMSynthesisSourceReader) IsGeneratedSynthesisSourceScoped(ctx context.Context, scope foundation.TransactionScope, source domain.SynthesisSourceVersion) (bool, error) {
	if err := reader.ready(ctx); err != nil {
		return false, err
	}
	if err := source.Validate(); err != nil {
		return false, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return false, err
	}
	state, found, err := reader.state(ctx, tx, source)
	if err != nil {
		return false, err
	}
	if !found {
		return false, synthesisSourceError(foundation.ErrorNotFound, "SYNTHESIS_SOURCE_NOT_FOUND", false)
	}
	return state.Derived, nil
}

type synthesisSpanRow struct {
	ID                  string `gorm:"column:id"`
	StartByte           int64  `gorm:"column:start_byte"`
	EndByte             int64  `gorm:"column:end_byte"`
	ExcerptHash         string `gorm:"column:excerpt_hash"`
	EvidenceKind        string `gorm:"column:evidence_kind"`
	DerivedExcerpt      string `gorm:"column:derived_excerpt"`
	DerivedExcerptBytes int64  `gorm:"column:derived_excerpt_bytes"`
}

// Bound database transfer before opening model input. Oversized parser output
// retains its byte count and hash and is rejected, never truncated into evidence.
const synthesisSpanColumns = `sp.id,sp.start_byte,sp.end_byte,sp.excerpt_hash,sp.evidence_kind,
	COALESCE(octet_length(sp.derived_excerpt),0) AS derived_excerpt_bytes,
	CASE WHEN octet_length(sp.derived_excerpt)<=? THEN sp.derived_excerpt ELSE '' END AS derived_excerpt`

func (reader *GORMSynthesisSourceReader) spans(ctx context.Context, source domain.SynthesisSourceVersion, spanID foundation.ID) ([]synthesisSpanRow, error) {
	query := reader.database.WithContext(ctx).Table("ingestion.source_span sp").Select(synthesisSpanColumns, organizingapp.MaxSynthesisSourceExcerptBytes).
		Joins("JOIN ingestion.parse_projection pp ON pp.id=sp.parse_projection_id AND pp.workspace_id=sp.workspace_id AND pp.content_artifact_id=sp.content_artifact_id AND pp.parser_version=sp.parser_version AND pp.schema_version=sp.schema_version").
		Where("sp.workspace_id=? AND sp.content_artifact_id=? AND sp.parse_projection_id=?", string(source.WorkspaceID), string(source.ContentArtifactID), string(source.ParseProjectionID))
	if spanID != "" {
		query = query.Where("sp.id=?", string(spanID))
	}
	var rows []synthesisSpanRow
	err := query.Order("sp.start_byte,sp.end_byte,sp.id").Limit(domain.MaxSynthesisSources + 1).Find(&rows).Error
	return rows, synthesisSourceDBError(ctx, err)
}

func (reader *GORMSynthesisSourceReader) ReadSynthesisSource(ctx context.Context, source domain.SynthesisSourceVersion) ([]organizingapp.SynthesisSourceExcerpt, error) {
	if err := reader.ready(ctx); err != nil {
		return nil, err
	}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	state, found, err := reader.state(ctx, reader.database, source)
	if err != nil {
		return nil, err
	}
	if !found || state.RemovedAt != nil {
		return nil, synthesisSourceError(foundation.ErrorNotFound, "SYNTHESIS_SOURCE_NOT_FOUND", false)
	}
	if state.Derived {
		return nil, synthesisSourceError(foundation.ErrorVersionConflict, "SYNTHESIS_DERIVED_SOURCE_EXCLUDED", false)
	}
	if !state.Current {
		return nil, synthesisSourceError(foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_STALE", true)
	}
	spans, err := reader.spans(ctx, source, "")
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 || len(spans) > domain.MaxSynthesisSources {
		return nil, synthesisSourceError(foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT", false)
	}
	artifact, err := reader.artifacts.ReadEvidenceArtifact(ctx, source.WorkspaceID, source.SourceVersionID)
	if err != nil {
		return nil, err
	}
	if err := validateSynthesisArtifact(source, artifact); err != nil {
		return nil, err
	}
	result := make([]organizingapp.SynthesisSourceExcerpt, 0, len(spans))
	total := 0
	for _, span := range spans {
		item, err := synthesisExcerpt(source, state.Title, span, artifact.Bytes)
		if err != nil {
			return nil, err
		}
		total += len(item.Text)
		if total > organizingapp.MaxSynthesisSourceInputBytes {
			return nil, synthesisSourceError(foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT", false)
		}
		result = append(result, item)
	}
	return result, nil
}

func (reader *GORMSynthesisSourceReader) OpenSynthesisSource(ctx context.Context, ref domain.SynthesisSourceRef) (organizingapp.SynthesisSourceView, error) {
	view := organizingapp.SynthesisSourceView{Reference: ref, Availability: domain.MaterialUnavailable}
	if err := reader.ready(ctx); err != nil {
		return view, err
	}
	if err := ref.Validate(); err != nil {
		return view, err
	}
	state, found, err := reader.state(ctx, reader.database, ref.Source)
	if err != nil {
		return view, err
	}
	if !found || state.RemovedAt != nil || state.Derived {
		return view, nil
	}
	if !state.Current {
		view.Availability = domain.MaterialStale
		return view, nil
	}
	spans, err := reader.spans(ctx, ref.Source, ref.SourceSpanID)
	if err != nil {
		return view, err
	}
	if len(spans) != 1 || spans[0].ExcerptHash != ref.ExcerptHash {
		return view, nil
	}
	artifact, err := reader.artifacts.ReadEvidenceArtifact(ctx, ref.Source.WorkspaceID, ref.Source.SourceVersionID)
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && (classified.Kind == foundation.ErrorNotFound || classified.Kind == foundation.ErrorConsistencyViolation) {
			return view, nil
		}
		return view, err
	}
	if err := validateSynthesisArtifact(ref.Source, artifact); err != nil {
		return view, nil
	}
	excerpt, err := synthesisExcerpt(ref.Source, ref.Title, spans[0], artifact.Bytes)
	if err != nil {
		return view, nil
	}
	view.Availability, view.Text = domain.MaterialAvailable, excerpt.Text
	return view, nil
}

type synthesisFenceRef struct {
	SourceID     string `json:"source_id"`
	VersionID    string `json:"version_id"`
	ArtifactID   string `json:"artifact_id"`
	ProjectionID string `json:"projection_id"`
	SpanID       string `json:"span_id"`
	ContentHash  string `json:"content_hash"`
	ExcerptHash  string `json:"excerpt_hash"`
}

type synthesisSourceJSON string

func (value synthesisSourceJSON) Value() (driver.Value, error) { return string(value), nil }

const synthesisRequestedSQL = `jsonb_to_recordset(?) AS requested(source_id uuid,version_id uuid,artifact_id uuid,projection_id uuid,span_id uuid,content_hash text,excerpt_hash text)`

// VerifySynthesisSourcesScoped locks all selected Source/Version parents in a
// stable order, then rereads current facts in a fresh statement after waiting.
func (reader *GORMSynthesisSourceReader) VerifySynthesisSourcesScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID foundation.ID, refs []domain.SynthesisSourceRef) error {
	if err := reader.ready(ctx); err != nil {
		return err
	}
	if !validID(workspaceID) || len(refs) > domain.MaxSynthesisSources {
		return synthesisSourceError(foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid, false)
	}
	if len(refs) == 0 {
		return nil
	}
	rows := make([]synthesisFenceRef, 0, len(refs))
	seen := make(map[string]bool)
	for _, ref := range refs {
		if ref.Validate() != nil || ref.Source.WorkspaceID != workspaceID {
			return synthesisSourceError(foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid, false)
		}
		key, _ := ref.IdentityKey()
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, synthesisFenceRef{string(ref.Source.SourceID), string(ref.Source.SourceVersionID), string(ref.Source.ContentArtifactID), string(ref.Source.ParseProjectionID), string(ref.SourceSpanID), ref.Source.ContentHash, ref.ExcerptHash})
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	var locks []struct {
		ID string `gorm:"column:id"`
	}
	lock := tx.Raw(`SELECT s.id FROM `+synthesisRequestedSQL+` JOIN core.source s ON s.id=requested.source_id AND s.workspace_id=? JOIN core.source_version v ON v.id=requested.version_id AND v.source_id=s.id AND v.workspace_id=s.workspace_id ORDER BY s.id,v.id FOR UPDATE OF s,v`, synthesisSourceJSON(encoded), string(workspaceID)).Scan(&locks)
	if lock.Error != nil {
		return synthesisSourceDBError(ctx, lock.Error)
	}
	if len(locks) != len(rows) {
		return synthesisSourceError(foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_STALE", true)
	}
	var count int64
	query := tx.Raw(`SELECT count(*)`+synthesisSourceJoins+`
	 JOIN `+synthesisRequestedSQL+` ON requested.source_id=s.id AND requested.version_id=v.id AND requested.artifact_id=a.id AND requested.projection_id=pp.id AND requested.content_hash=v.content_hash
	 JOIN ingestion.source_span sp ON sp.id=requested.span_id AND sp.workspace_id=s.workspace_id AND sp.content_artifact_id=a.id AND sp.parse_projection_id=pp.id AND sp.excerpt_hash=requested.excerpt_hash AND sp.parser_version=pp.parser_version AND sp.schema_version=pp.schema_version
	 WHERE s.workspace_id=? AND `+synthesisSourceCurrentSQL+` AND NOT `+synthesisDerivedSQL, synthesisSourceJSON(encoded), string(workspaceID)).Scan(&count)
	if query.Error != nil {
		return synthesisSourceDBError(ctx, query.Error)
	}
	if count != int64(len(rows)) {
		return synthesisSourceError(foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_STALE", true)
	}
	return nil
}

func validateSynthesisArtifact(source domain.SynthesisSourceVersion, artifact retrievalapp.EvidenceArtifact) error {
	digest := sha256.Sum256(artifact.Bytes)
	if artifact.WorkspaceID != source.WorkspaceID || artifact.SourceVersionID != source.SourceVersionID || artifact.ContentArtifactID != source.ContentArtifactID || artifact.ContentHash != source.ContentHash || int64(len(artifact.Bytes)) != artifact.ByteSize || hex.EncodeToString(digest[:]) != source.ContentHash {
		return synthesisSourceError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSynthesisSourceInvalid, false)
	}
	return nil
}

func synthesisExcerpt(source domain.SynthesisSourceVersion, title string, span synthesisSpanRow, content []byte) (organizingapp.SynthesisSourceExcerpt, error) {
	var text string
	switch span.EvidenceKind {
	case "raw_bytes":
		if span.StartByte < 0 || span.EndByte < span.StartByte || span.EndByte > int64(len(content)) || span.EndByte-span.StartByte > organizingapp.MaxSynthesisSourceExcerptBytes {
			return organizingapp.SynthesisSourceExcerpt{}, synthesisSourceError(foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT", false)
		}
		text = string(content[span.StartByte:span.EndByte])
	case "derived_text":
		if span.DerivedExcerptBytes > organizingapp.MaxSynthesisSourceExcerptBytes || len(span.DerivedExcerpt) > organizingapp.MaxSynthesisSourceExcerptBytes {
			return organizingapp.SynthesisSourceExcerpt{}, synthesisSourceError(foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT", false)
		}
		if span.StartByte != 0 || span.EndByte != int64(len(content)) {
			return organizingapp.SynthesisSourceExcerpt{}, synthesisSourceError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSynthesisSourceInvalid, false)
		}
		text = span.DerivedExcerpt
	default:
		return organizingapp.SynthesisSourceExcerpt{}, synthesisSourceError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSynthesisSourceInvalid, false)
	}
	result := organizingapp.SynthesisSourceExcerpt{Reference: domain.SynthesisSourceRef{Source: source, SourceSpanID: foundation.ID(span.ID), ExcerptHash: span.ExcerptHash, Title: boundedTitle(title)}, Text: text}
	return result, result.Validate(source.WorkspaceID)
}

func synthesisSourceError(kind foundation.ErrorKind, code string, retryable bool) error {
	return foundation.NewError(kind, code, retryable, errors.New("synthesis original source cannot be verified"))
}
func synthesisSourceDBError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "SYNTHESIS_SOURCE_UNAVAILABLE", true, errors.Join(ctx.Err(), context.Cause(ctx)))
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "SYNTHESIS_SOURCE_UNAVAILABLE", true, err)
}
