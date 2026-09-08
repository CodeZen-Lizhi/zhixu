package postgres

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

func chunkVersions(w domain.ProjectionWrite) (string, string) {
	if len(w.Chunks) == 0 {
		return "", ""
	}
	return w.Chunks[0].ChunkStrategyVersion, w.Chunks[0].SchemaVersion
}

func spanKey(span domain.SourceSpan) string {
	selector, _ := marshalObject(span.Selector)
	evidenceKind := span.EvidenceKind
	if evidenceKind == "" {
		evidenceKind = domain.EvidenceRawBytes
	}
	return fmt.Sprintf("%s|%d|%d|%d|%d|%s|%s|%s|%s", span.SpanType, span.StartLine, span.EndLine,
		span.StartByte, span.EndByte, selector, span.ExcerptHash, evidenceKind, span.SchemaVersion)
}

type rowScanner interface{ Scan(...any) error }

func scanAttempt(row rowScanner) (domain.AttemptRecord, error) {
	var record domain.AttemptRecord
	var id, workspaceID, sourceVersionID string
	var workflowID, projectionID *string
	var status, security string
	var warnings []byte
	if err := row.Scan(&id, &workspaceID, &sourceVersionID, &workflowID, &projectionID, &status, &security,
		&record.FailureStage, &record.ErrorCode, &record.Retryable, &record.ParserID, &record.ParserVersion,
		&record.ParserConfigHash, &record.ChunkStrategyVersion, &record.SchemaVersion, &record.IdempotencyKey,
		&record.AttemptNumber, &warnings, &record.StartedAt, &record.CompletedAt, &record.Version); err != nil {
		return domain.AttemptRecord{}, err
	}
	var err error
	if record.ID, err = foundation.ParseID(id); err != nil {
		return domain.AttemptRecord{}, err
	}
	if record.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.AttemptRecord{}, err
	}
	if record.SourceVersionID, err = foundation.ParseID(sourceVersionID); err != nil {
		return domain.AttemptRecord{}, err
	}
	if workflowID != nil {
		parsed, parseErr := foundation.ParseID(*workflowID)
		if parseErr != nil {
			return domain.AttemptRecord{}, parseErr
		}
		record.WorkflowRunID = &parsed
	}
	if projectionID != nil {
		parsed, parseErr := foundation.ParseID(*projectionID)
		if parseErr != nil {
			return domain.AttemptRecord{}, parseErr
		}
		record.ParseProjectionID = &parsed
	}
	record.Status = domain.AttemptStatus(status)
	record.SecurityStatus = domain.SecurityStatus(security)
	if err := json.Unmarshal(warnings, &record.Warnings); err != nil {
		return domain.AttemptRecord{}, fmt.Errorf("decode attempt warnings: %w", err)
	}
	return record, nil
}

func scanProjection(row rowScanner) (domain.ParseProjection, error) {
	var projection domain.ParseProjection
	var id, workspaceID, artifactID string
	var warnings []byte
	if err := row.Scan(&id, &workspaceID, &artifactID, &projection.ParserID, &projection.ParserVersion,
		&projection.ParserConfigHash, &projection.SchemaVersion, &projection.NormalizedContentHash, &warnings, &projection.CreatedAt); err != nil {
		return domain.ParseProjection{}, err
	}
	var err error
	if projection.ID, err = foundation.ParseID(id); err != nil {
		return domain.ParseProjection{}, err
	}
	if projection.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.ParseProjection{}, err
	}
	if projection.ContentArtifactID, err = foundation.ParseID(artifactID); err != nil {
		return domain.ParseProjection{}, err
	}
	if err := json.Unmarshal(warnings, &projection.Warnings); err != nil {
		return domain.ParseProjection{}, err
	}
	return projection, nil
}

func scanSpan(row rowScanner) (domain.SourceSpan, error) {
	var span domain.SourceSpan
	var id, workspaceID, artifactID, projectionID string
	var evidenceKind string
	var selector []byte
	if err := row.Scan(&id, &workspaceID, &artifactID, &projectionID, &span.SpanType, &span.StartLine,
		&span.EndLine, &span.StartByte, &span.EndByte, &selector, &span.ExcerptHash, &evidenceKind, &span.DerivedExcerpt,
		&span.ParserVersion, &span.SchemaVersion); err != nil {
		return domain.SourceSpan{}, err
	}
	var err error
	if span.ID, err = foundation.ParseID(id); err != nil {
		return domain.SourceSpan{}, err
	}
	if span.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.SourceSpan{}, err
	}
	if span.ContentArtifactID, err = foundation.ParseID(artifactID); err != nil {
		return domain.SourceSpan{}, err
	}
	if span.ParseProjectionID, err = foundation.ParseID(projectionID); err != nil {
		return domain.SourceSpan{}, err
	}
	if err := json.Unmarshal(selector, &span.Selector); err != nil {
		return domain.SourceSpan{}, err
	}
	span.EvidenceKind = domain.EvidenceKind(evidenceKind)
	return span, nil
}

func scanChunk(row rowScanner) (domain.CanonicalChunk, error) {
	var chunk domain.CanonicalChunk
	var id, workspaceID, projectionID, spanID string
	var heading []byte
	if err := row.Scan(&id, &workspaceID, &projectionID, &chunk.Sequence, &heading, &chunk.Content,
		&chunk.ContentHash, &spanID, &chunk.ByteCount, &chunk.RuneCount, &chunk.ParserVersion,
		&chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.AtomicOversized, &chunk.Status); err != nil {
		return domain.CanonicalChunk{}, err
	}
	var err error
	if chunk.ID, err = foundation.ParseID(id); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if chunk.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if chunk.ParseProjectionID, err = foundation.ParseID(projectionID); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if chunk.SourceSpanID, err = foundation.ParseID(spanID); err != nil {
		return domain.CanonicalChunk{}, err
	}
	if err := json.Unmarshal(heading, &chunk.HeadingPath); err != nil {
		return domain.CanonicalChunk{}, err
	}
	return chunk, nil
}

func optionalID(id *foundation.ID) any {
	if id == nil || *id == "" {
		return nil
	}
	return string(*id)
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || *left == "" {
		return right == nil || *right == ""
	}
	return right != nil && *right == *left
}

func marshalWarnings(value []domain.Warning) ([]byte, error) {
	if value == nil {
		value = []domain.Warning{}
	}
	return json.Marshal(value)
}

func marshalObject(value map[string]string) ([]byte, error) {
	if value == nil {
		value = map[string]string{}
	}
	return json.Marshal(value)
}

func marshalStrings(value []string) ([]byte, error) {
	if value == nil {
		value = []string{}
	}
	return json.Marshal(value)
}
func utcPointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}
func invalid(code string, err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, err)
}
