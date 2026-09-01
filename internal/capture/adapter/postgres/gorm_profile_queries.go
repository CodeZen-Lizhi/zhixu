package postgres

import (
	"context"
	"database/sql"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const profileReturning = `id::text,workspace_id::text,capture_id::text,source_version_id::text,
	current_revision_id::text,status,error_code,retryable,version,created_at,updated_at`

// LookupReady finds an immutable READY revision with the exact frozen contract.
func (repository *GORMProfileRepository) LookupReady(
	ctx context.Context,
	lookup captureapp.ProfileLookup,
	contract captureapp.ProfileContract,
) (captureapp.ReadyProfile, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.ReadyProfile{}, false, err
	}
	if !validProfileLookup(lookup) || !validProfileContract(contract) {
		return captureapp.ReadyProfile{}, false, invalid(profileQueryInvalidCode, "profile lookup is invalid")
	}
	ready, found, err := gormLookupReadyProfile(ctx, repository.database.WithContext(ctx), lookup, contract)
	if err != nil {
		return captureapp.ReadyProfile{}, false, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	return ready, found, nil
}

// LoadSource reads bounded ordered chunks from the frozen index manifest.
func (repository *GORMProfileRepository) LoadSource(
	ctx context.Context,
	lookup captureapp.ProfileLookup,
	parseProjectionID foundation.ID,
	indexVersionID foundation.ID,
) (captureapp.ProfileSourceSnapshot, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.ProfileSourceSnapshot{}, err
	}
	if !validProfileLookup(lookup) || !validID(parseProjectionID) || !validID(indexVersionID) {
		return captureapp.ProfileSourceSnapshot{}, invalid(profileQueryInvalidCode, "profile source lookup is invalid")
	}
	database := repository.database.WithContext(ctx)
	row, err := gormCaptureRawRow(database, `SELECT index_version.embedding_version_id::text
		FROM core.capture AS capture
		JOIN core.source_version AS source_version
		  ON source_version.id=? AND source_version.source_id=capture.source_id
		JOIN retrieval.index_version AS index_version
		  ON index_version.id=? AND index_version.workspace_id=capture.workspace_id
		JOIN retrieval.index_manifest_source AS manifest_source
		  ON manifest_source.index_version_id=index_version.id
		 AND manifest_source.workspace_id=index_version.workspace_id
		 AND manifest_source.source_id=capture.source_id
		JOIN ingestion.source_version_projection AS source_projection
		  ON source_projection.source_version_id=source_version.id
		 AND source_projection.parse_projection_id=?
		 AND source_projection.workspace_id=capture.workspace_id
		WHERE capture.workspace_id=? AND capture.id=?
		  AND capture.latest_source_version_id=source_version.id
		  AND manifest_source.selection_status='included'
		  AND manifest_source.source_version_id=source_version.id
		  AND manifest_source.parse_projection_id=source_projection.parse_projection_id
		  AND index_version.status IN ('ready','active','retiring')`,
		string(lookup.SourceVersionID), string(indexVersionID), string(parseProjectionID),
		string(lookup.WorkspaceID), string(lookup.CaptureID))
	if err != nil {
		return captureapp.ProfileSourceSnapshot{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_SOURCE_QUERY_FAILED")
	}
	var embeddingID sql.NullString
	if err := row.Scan(&embeddingID); gormCaptureNoRows(err) {
		return captureapp.ProfileSourceSnapshot{}, inconsistent(profileContextInvalidCode, "profile source is outside the frozen index manifest")
	} else if err != nil {
		return captureapp.ProfileSourceSnapshot{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_SOURCE_QUERY_FAILED")
	}

	rows, err := gormCaptureRows(database, `SELECT chunk.id::text,chunk.source_span_id::text,chunk.sequence,chunk.heading_path,
		       CASE WHEN octet_length(chunk.content) BETWEEN 1 AND ? THEN chunk.content END
		FROM retrieval.index_manifest_chunk AS manifest_chunk
		JOIN ingestion.canonical_chunk AS chunk
		  ON chunk.id=manifest_chunk.chunk_id AND chunk.workspace_id=manifest_chunk.workspace_id
		JOIN retrieval.chunk_projection AS projection
		  ON projection.index_version_id=manifest_chunk.index_version_id
		 AND projection.chunk_id=manifest_chunk.chunk_id
		 AND projection.workspace_id=manifest_chunk.workspace_id
		WHERE manifest_chunk.workspace_id=? AND manifest_chunk.index_version_id=?
		  AND chunk.parse_projection_id=? AND chunk.status='active'
		  AND projection.lexical_status='ready'
		ORDER BY chunk.sequence,chunk.id LIMIT ?`,
		captureapp.MaxProfileChunkBytes, string(lookup.WorkspaceID), string(indexVersionID),
		string(parseProjectionID), captureapp.MaxProfileSourceChunks+1)
	if err != nil {
		return captureapp.ProfileSourceSnapshot{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_SOURCE_QUERY_FAILED")
	}
	defer rows.Close()
	chunks := make([]captureapp.ProfileSourceChunk, 0)
	for rows.Next() {
		var chunkID, spanID string
		var headingRaw []byte
		var content sql.NullString
		var chunk captureapp.ProfileSourceChunk
		if err := rows.Scan(&chunkID, &spanID, &chunk.Sequence, &headingRaw, &content); err != nil {
			return captureapp.ProfileSourceSnapshot{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_SOURCE_SCAN_FAILED")
		}
		if chunk.Content, err = boundedProfileChunkContent(content); err != nil {
			return captureapp.ProfileSourceSnapshot{}, err
		}
		if chunk.ChunkID, err = foundation.ParseID(chunkID); err != nil {
			return captureapp.ProfileSourceSnapshot{}, inconsistent(profileContextInvalidCode, "profile chunk identity is invalid")
		}
		if chunk.SourceSpanID, err = foundation.ParseID(spanID); err != nil {
			return captureapp.ProfileSourceSnapshot{}, inconsistent(profileContextInvalidCode, "profile source span identity is invalid")
		}
		if chunk.HeadingPath, err = decodeHeadingPath(headingRaw); err != nil {
			return captureapp.ProfileSourceSnapshot{}, err
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return captureapp.ProfileSourceSnapshot{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_SOURCE_QUERY_FAILED")
	}
	if len(chunks) == 0 || len(chunks) > captureapp.MaxProfileSourceChunks {
		return captureapp.ProfileSourceSnapshot{}, inconsistent(profileContextInvalidCode, "profile source chunk count is outside the supported boundary")
	}
	snapshot := captureapp.ProfileSourceSnapshot{
		WorkspaceID: lookup.WorkspaceID, SourceVersionID: lookup.SourceVersionID,
		ParseProjectionID: parseProjectionID, IndexVersionID: indexVersionID, Chunks: chunks,
	}
	if embeddingID.Valid {
		parsed, parseErr := foundation.ParseID(embeddingID.String)
		if parseErr != nil {
			return captureapp.ProfileSourceSnapshot{}, inconsistent(profileContextInvalidCode, "profile embedding identity is invalid")
		}
		snapshot.EmbeddingVersionID = &parsed
	}
	return snapshot, nil
}

// GetProfile returns the authoritative Profile state and current evidence view.
func (repository *GORMProfileRepository) GetProfile(
	ctx context.Context,
	query captureapp.ProfileQuery,
) (captureapp.ProfileView, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.ProfileView{}, err
	}
	if !validID(query.WorkspaceID) || !validID(query.SourceVersionID) {
		return captureapp.ProfileView{}, invalid(profileQueryInvalidCode, "profile detail query is invalid")
	}
	database := repository.database.WithContext(ctx)
	profile, found, err := gormLoadProfileBySource(ctx, database, query.WorkspaceID, query.SourceVersionID, false)
	if err != nil {
		return captureapp.ProfileView{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	if !found {
		return captureapp.ProfileView{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_NOT_FOUND", false, sql.ErrNoRows)
	}
	return gormLoadProfileView(ctx, database, profile)
}

// GetProfiles returns bounded Profile views using three set queries.
func (repository *GORMProfileRepository) GetProfiles(
	ctx context.Context,
	query captureapp.ProfileBatchQuery,
) ([]captureapp.ProfileView, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if !validID(query.WorkspaceID) || len(query.SourceVersionIDs) == 0 ||
		len(query.SourceVersionIDs) > captureapp.MaxProfileBatchSize {
		return nil, invalid(profileQueryInvalidCode, "profile batch query is invalid")
	}
	sourceVersionIDs := make([]string, len(query.SourceVersionIDs))
	seen := make(map[foundation.ID]struct{}, len(query.SourceVersionIDs))
	for index, sourceVersionID := range query.SourceVersionIDs {
		if !validID(sourceVersionID) {
			return nil, invalid(profileQueryInvalidCode, "profile batch source version identity is invalid")
		}
		if _, duplicate := seen[sourceVersionID]; duplicate {
			return nil, invalid(profileQueryInvalidCode, "profile batch source versions are duplicated")
		}
		seen[sourceVersionID] = struct{}{}
		sourceVersionIDs[index] = string(sourceVersionID)
	}
	database := repository.database.WithContext(ctx)
	rows, err := gormCaptureRows(database, `SELECT `+profileReturning+`
		FROM learning.document_knowledge_profile
		WHERE workspace_id=? AND source_version_id=ANY(?::uuid[]) ORDER BY source_version_id`,
		string(query.WorkspaceID), pq.Array(sourceVersionIDs))
	if err != nil {
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_BATCH_QUERY_FAILED")
	}
	profiles := make([]domain.Profile, 0, len(sourceVersionIDs))
	for rows.Next() {
		profile, scanErr := scanProfile(rows)
		if scanErr != nil {
			rows.Close()
			return nil, classifyGORMCapture(ctx, scanErr, "CAPTURE_PROFILE_BATCH_SCAN_FAILED")
		}
		if profile.WorkspaceID != query.WorkspaceID || profile.Validate() != nil {
			rows.Close()
			return nil, inconsistent(profileContextInvalidCode, "profile batch returned an invalid profile")
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_BATCH_QUERY_FAILED")
	}
	rows.Close()

	views := make([]captureapp.ProfileView, len(profiles))
	revisionIDs := make([]string, 0, len(profiles))
	viewByRevision := make(map[foundation.ID]int, len(profiles))
	for index, profile := range profiles {
		views[index] = captureapp.ProfileView{Profile: profile, Evidence: []domain.ProfileEvidence{}}
		if profile.CurrentRevisionID != "" {
			revisionIDs = append(revisionIDs, string(profile.CurrentRevisionID))
			viewByRevision[profile.CurrentRevisionID] = index
		}
	}
	if len(revisionIDs) == 0 {
		return views, nil
	}

	revisionRows, err := gormCaptureRows(database, `SELECT id::text,profile_id::text,workspace_id::text,source_version_id::text,
		parse_projection_id::text,index_version_id::text,model_run_id::text,model_settings_revision,
		prompt_version,schema_version,content,content_digest,created_at
		FROM learning.document_knowledge_profile_revision
		WHERE workspace_id=? AND id=ANY(?::uuid[]) ORDER BY id`,
		string(query.WorkspaceID), pq.Array(revisionIDs))
	if err != nil {
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_BATCH_REVISION_QUERY_FAILED")
	}
	foundRevisions := make(map[foundation.ID]struct{}, len(revisionIDs))
	for revisionRows.Next() {
		revision, scanErr := scanProfileRevision(revisionRows)
		if scanErr != nil {
			revisionRows.Close()
			return nil, classifyGORMCapture(ctx, scanErr, "CAPTURE_PROFILE_BATCH_REVISION_SCAN_FAILED")
		}
		viewIndex, exists := viewByRevision[revision.ID]
		if !exists || revision.WorkspaceID != query.WorkspaceID || revision.ProfileID != views[viewIndex].Profile.ID ||
			revision.SourceVersionID != views[viewIndex].Profile.SourceVersionID {
			revisionRows.Close()
			return nil, inconsistent(profileContextInvalidCode, "profile batch revision binding is invalid")
		}
		copy := revision
		views[viewIndex].Revision = &copy
		foundRevisions[revision.ID] = struct{}{}
	}
	if err := revisionRows.Err(); err != nil {
		revisionRows.Close()
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_BATCH_REVISION_QUERY_FAILED")
	}
	revisionRows.Close()
	if len(foundRevisions) != len(revisionIDs) {
		return nil, inconsistent(profileContextInvalidCode, "profile batch current revision is missing")
	}

	evidenceRows, err := gormCaptureRows(database, `SELECT revision_id::text,source_version_id::text,source_span_id::text,created_at
		FROM learning.document_knowledge_profile_evidence
		WHERE workspace_id=? AND revision_id=ANY(?::uuid[]) ORDER BY revision_id,source_span_id`,
		string(query.WorkspaceID), pq.Array(revisionIDs))
	if err != nil {
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_BATCH_EVIDENCE_QUERY_FAILED")
	}
	for evidenceRows.Next() {
		var revisionRaw, sourceVersionRaw, sourceSpanRaw string
		var item domain.ProfileEvidence
		if err := evidenceRows.Scan(&revisionRaw, &sourceVersionRaw, &sourceSpanRaw, &item.CreatedAt); err != nil {
			evidenceRows.Close()
			return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_BATCH_EVIDENCE_SCAN_FAILED")
		}
		item.RevisionID, err = foundation.ParseID(revisionRaw)
		if err != nil {
			evidenceRows.Close()
			return nil, inconsistent(profileContextInvalidCode, "profile batch evidence revision identity is invalid")
		}
		item.WorkspaceID = query.WorkspaceID
		item.SourceVersionID, err = foundation.ParseID(sourceVersionRaw)
		if err != nil {
			evidenceRows.Close()
			return nil, inconsistent(profileContextInvalidCode, "profile batch evidence source identity is invalid")
		}
		item.SourceSpanID, err = foundation.ParseID(sourceSpanRaw)
		if err != nil {
			evidenceRows.Close()
			return nil, inconsistent(profileContextInvalidCode, "profile batch evidence span identity is invalid")
		}
		viewIndex, exists := viewByRevision[item.RevisionID]
		if !exists || item.SourceVersionID != views[viewIndex].Profile.SourceVersionID {
			evidenceRows.Close()
			return nil, inconsistent(profileContextInvalidCode, "profile batch evidence binding is invalid")
		}
		views[viewIndex].Evidence = append(views[viewIndex].Evidence, item)
	}
	if err := evidenceRows.Err(); err != nil {
		evidenceRows.Close()
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_BATCH_EVIDENCE_QUERY_FAILED")
	}
	evidenceRows.Close()
	for _, view := range views {
		if view.Revision == nil {
			continue
		}
		if len(view.Evidence) == 0 || !sameFoundationIDs(profileContentEvidence(view.Revision.Content), evidenceSpanIDs(view.Evidence)) {
			return nil, inconsistent(profileContextInvalidCode, "profile batch evidence does not match current revision content")
		}
	}
	return views, nil
}

func gormLookupReadyProfile(
	ctx context.Context,
	database *gorm.DB,
	lookup captureapp.ProfileLookup,
	contract captureapp.ProfileContract,
) (captureapp.ReadyProfile, bool, error) {
	row, err := gormCaptureRawRow(database.WithContext(ctx), `SELECT profile.id::text,revision.id::text
		FROM learning.document_knowledge_profile AS profile
		JOIN learning.document_knowledge_profile_revision AS revision
		  ON revision.id=profile.current_revision_id
		 AND revision.profile_id=profile.id
		 AND revision.workspace_id=profile.workspace_id
		 AND revision.source_version_id=profile.source_version_id
		WHERE profile.workspace_id=? AND profile.capture_id=? AND profile.source_version_id=?
		  AND profile.status='READY'
		  AND revision.parse_projection_id=? AND revision.index_version_id=?
		  AND revision.model_settings_revision IS NOT DISTINCT FROM ?::bigint
		  AND revision.prompt_version=? AND revision.schema_version=?
		ORDER BY revision.created_at DESC,revision.id DESC LIMIT 1`,
		string(lookup.WorkspaceID), string(lookup.CaptureID), string(lookup.SourceVersionID),
		string(contract.ParseProjectionID), string(contract.IndexVersionID), nullableProfileRevision(contract.ModelSettingsRevision),
		contract.PromptVersion, contract.SchemaVersion)
	if err != nil {
		return captureapp.ReadyProfile{}, false, err
	}
	var profileID, revisionID string
	if err := row.Scan(&profileID, &revisionID); gormCaptureNoRows(err) {
		return captureapp.ReadyProfile{}, false, nil
	} else if err != nil {
		return captureapp.ReadyProfile{}, false, err
	}
	parsedProfileID, err := foundation.ParseID(profileID)
	if err != nil {
		return captureapp.ReadyProfile{}, false, err
	}
	parsedRevisionID, err := foundation.ParseID(revisionID)
	if err != nil {
		return captureapp.ReadyProfile{}, false, err
	}
	return captureapp.ReadyProfile{ProfileID: parsedProfileID, RevisionID: parsedRevisionID}, true, nil
}

func gormVerifyProfileCapture(ctx context.Context, database *gorm.DB, lookup captureapp.ProfileLookup) error {
	row, err := gormCaptureRawRow(database.WithContext(ctx), `SELECT true FROM core.capture AS capture
		JOIN core.source_version AS source_version
		  ON source_version.id=? AND source_version.source_id=capture.source_id
		WHERE capture.workspace_id=? AND capture.id=?
		  AND capture.latest_source_version_id=source_version.id`,
		string(lookup.SourceVersionID), string(lookup.WorkspaceID), string(lookup.CaptureID))
	if err != nil {
		return err
	}
	var found bool
	if err := row.Scan(&found); gormCaptureNoRows(err) {
		return inconsistent(profileContextInvalidCode, "profile capture/source binding does not exist")
	} else if err != nil {
		return err
	}
	return nil
}

func gormLoadProfileBySource(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	sourceVersionID foundation.ID,
	forUpdate bool,
) (domain.Profile, bool, error) {
	query := `SELECT ` + profileReturning + ` FROM learning.document_knowledge_profile
		WHERE workspace_id=? AND source_version_id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormCaptureRawRow(database.WithContext(ctx), query, string(workspaceID), string(sourceVersionID))
	if err != nil {
		return domain.Profile{}, false, err
	}
	profile, err := scanProfile(row)
	if gormCaptureNoRows(err) {
		return domain.Profile{}, false, nil
	}
	return profile, err == nil, err
}

func gormLoadProfileView(ctx context.Context, database *gorm.DB, profile domain.Profile) (captureapp.ProfileView, error) {
	if err := profile.Validate(); err != nil {
		return captureapp.ProfileView{}, inconsistent(profileContextInvalidCode, "persisted profile is invalid")
	}
	view := captureapp.ProfileView{Profile: profile, Evidence: []domain.ProfileEvidence{}}
	if profile.CurrentRevisionID == "" {
		return view, nil
	}
	revision, found, err := gormLoadProfileRevision(ctx, database, profile.WorkspaceID, profile.CurrentRevisionID)
	if err != nil {
		return captureapp.ProfileView{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_REVISION_QUERY_FAILED")
	}
	if !found || revision.ProfileID != profile.ID || revision.WorkspaceID != profile.WorkspaceID ||
		revision.SourceVersionID != profile.SourceVersionID {
		return captureapp.ProfileView{}, inconsistent(profileContextInvalidCode, "profile current revision binding is invalid")
	}
	evidence, err := gormLoadProfileEvidence(ctx, database, revision)
	if err != nil {
		return captureapp.ProfileView{}, err
	}
	if len(evidence) == 0 || !sameFoundationIDs(profileContentEvidence(revision.Content), evidenceSpanIDs(evidence)) {
		return captureapp.ProfileView{}, inconsistent(profileContextInvalidCode, "profile evidence does not match current revision content")
	}
	view.Revision = &revision
	view.Evidence = evidence
	return view, nil
}

func gormLoadProfileAttemptByNode(
	ctx context.Context,
	database *gorm.DB,
	profileID foundation.ID,
	nodeAttemptID foundation.ID,
	forUpdate bool,
) (profileAttemptRecord, bool, error) {
	query := `SELECT ` + profileAttemptReturning + ` FROM learning.document_knowledge_profile_attempt
		WHERE profile_id=? AND node_attempt_id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return gormScanProfileAttempt(ctx, database, query, string(profileID), string(nodeAttemptID))
}

func gormLoadProfileAttemptByID(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	attemptID foundation.ID,
	forUpdate bool,
) (profileAttemptRecord, bool, error) {
	query := `SELECT ` + profileAttemptReturning + ` FROM learning.document_knowledge_profile_attempt
		WHERE workspace_id=? AND id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return gormScanProfileAttempt(ctx, database, query, string(workspaceID), string(attemptID))
}

func gormScanProfileAttempt(ctx context.Context, database *gorm.DB, query string, arguments ...any) (profileAttemptRecord, bool, error) {
	row, err := gormCaptureRawRow(database.WithContext(ctx), query, arguments...)
	if err != nil {
		return profileAttemptRecord{}, false, err
	}
	attempt, err := scanProfileAttempt(row)
	if gormCaptureNoRows(err) {
		return profileAttemptRecord{}, false, nil
	}
	return attempt, err == nil, err
}

func gormLoadProfileRevision(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	revisionID foundation.ID,
) (domain.ProfileRevision, bool, error) {
	row, err := gormCaptureRawRow(database.WithContext(ctx), `SELECT id::text,profile_id::text,workspace_id::text,source_version_id::text,
		parse_projection_id::text,index_version_id::text,model_run_id::text,model_settings_revision,
		prompt_version,schema_version,content,content_digest,created_at
		FROM learning.document_knowledge_profile_revision WHERE workspace_id=? AND id=?`,
		string(workspaceID), string(revisionID))
	if err != nil {
		return domain.ProfileRevision{}, false, err
	}
	revision, err := scanProfileRevision(row)
	if gormCaptureNoRows(err) {
		return domain.ProfileRevision{}, false, nil
	}
	return revision, err == nil, err
}

func gormLoadProfileEvidence(ctx context.Context, database *gorm.DB, revision domain.ProfileRevision) ([]domain.ProfileEvidence, error) {
	rows, err := gormCaptureRows(database.WithContext(ctx), `SELECT source_span_id::text,created_at
		FROM learning.document_knowledge_profile_evidence
		WHERE revision_id=? AND workspace_id=? AND source_version_id=? ORDER BY source_span_id`,
		string(revision.ID), string(revision.WorkspaceID), string(revision.SourceVersionID))
	if err != nil {
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_EVIDENCE_QUERY_FAILED")
	}
	defer rows.Close()
	evidence := make([]domain.ProfileEvidence, 0)
	for rows.Next() {
		var rawID string
		var item domain.ProfileEvidence
		if err := rows.Scan(&rawID, &item.CreatedAt); err != nil {
			return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_EVIDENCE_SCAN_FAILED")
		}
		item.SourceSpanID, err = foundation.ParseID(rawID)
		if err != nil {
			return nil, inconsistent(profileContextInvalidCode, "persisted profile evidence identity is invalid")
		}
		item.RevisionID = revision.ID
		item.WorkspaceID = revision.WorkspaceID
		item.SourceVersionID = revision.SourceVersionID
		evidence = append(evidence, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_EVIDENCE_QUERY_FAILED")
	}
	return evidence, nil
}

var _ captureapp.ProfileReader = (*GORMProfileRepository)(nil)
var _ captureapp.ProfileBatchReader = (*GORMProfileRepository)(nil)
