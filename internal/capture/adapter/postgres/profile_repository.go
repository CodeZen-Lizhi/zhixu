package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	profileQueryInvalidCode       = "CAPTURE_PROFILE_QUERY_INVALID"
	profileContextInvalidCode     = "CAPTURE_PROFILE_CONTEXT_INVALID"
	profileAttemptConflictCode    = "CAPTURE_PROFILE_ATTEMPT_CONFLICT"
	profileVersionConflictCode    = "CAPTURE_PROFILE_VERSION_CONFLICT"
	profilePersistenceFailedCode  = "CAPTURE_PROFILE_PERSISTENCE_FAILED"
	profileFinalizationFailedCode = "CAPTURE_PROFILE_FINALIZATION_FAILED"
)

// ProfileRepository 持久化不可变画像 Revision，并在同一事务内终结 Agent Model Run。
type ProfileRepository struct {
	db        *pgxpool.Pool
	modelRuns agentapp.ModelRunTxFinalizer
}

// NewProfileRepository 创建画像 PostgreSQL 仓储。
func NewProfileRepository(db *pgxpool.Pool, modelRuns agentapp.ModelRunTxFinalizer) (*ProfileRepository, error) {
	if db == nil || nilProfilePort(modelRuns) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_PROFILE_REPOSITORY_UNAVAILABLE", false,
			errors.New("profile database and model run finalizer are required"))
	}
	return &ProfileRepository{db: db, modelRuns: modelRuns}, nil
}

// LookupReady 查找完整契约完全相同的不可变画像 Revision。
func (repository *ProfileRepository) LookupReady(ctx context.Context, lookup captureapp.ProfileLookup, contract captureapp.ProfileContract) (captureapp.ReadyProfile, bool, error) {
	if repository == nil || repository.db == nil || !validProfileLookup(lookup) || !validProfileContract(contract) {
		return captureapp.ReadyProfile{}, false, invalid(profileQueryInvalidCode, "profile lookup is invalid")
	}
	ready, found, err := lookupReadyProfile(ctx, repository.db, lookup, contract)
	if err != nil {
		return captureapp.ReadyProfile{}, false, classify(err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	return ready, found, nil
}

// LoadSource 读取冻结 Index Manifest 内属于指定 Source Version/Parse Projection 的有序 Chunk。
func (repository *ProfileRepository) LoadSource(ctx context.Context, lookup captureapp.ProfileLookup, parseProjectionID, indexVersionID foundation.ID) (captureapp.ProfileSourceSnapshot, error) {
	if repository == nil || repository.db == nil || !validProfileLookup(lookup) ||
		!validID(parseProjectionID) || !validID(indexVersionID) {
		return captureapp.ProfileSourceSnapshot{}, invalid(profileQueryInvalidCode, "profile source lookup is invalid")
	}
	var embeddingID sql.NullString
	err := repository.db.QueryRow(ctx, `
		SELECT index_version.embedding_version_id::text
		FROM core.capture AS capture
		JOIN core.source_version AS source_version
		  ON source_version.id=$3 AND source_version.source_id=capture.source_id
		JOIN retrieval.index_version AS index_version
		  ON index_version.id=$5 AND index_version.workspace_id=capture.workspace_id
		JOIN retrieval.index_manifest_source AS manifest_source
		  ON manifest_source.index_version_id=index_version.id
		 AND manifest_source.workspace_id=index_version.workspace_id
		 AND manifest_source.source_id=capture.source_id
		JOIN ingestion.source_version_projection AS source_projection
		  ON source_projection.source_version_id=source_version.id
		 AND source_projection.parse_projection_id=$4
		 AND source_projection.workspace_id=capture.workspace_id
		WHERE capture.workspace_id=$1 AND capture.id=$2
		  AND capture.latest_source_version_id=source_version.id
		  AND manifest_source.selection_status='included'
		  AND manifest_source.source_version_id=source_version.id
		  AND manifest_source.parse_projection_id=source_projection.parse_projection_id
		  AND index_version.status IN ('ready','active','retiring')`,
		string(lookup.WorkspaceID), string(lookup.CaptureID), string(lookup.SourceVersionID),
		string(parseProjectionID), string(indexVersionID)).Scan(&embeddingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return captureapp.ProfileSourceSnapshot{}, inconsistent(profileContextInvalidCode, "profile source is outside the frozen index manifest")
	}
	if err != nil {
		return captureapp.ProfileSourceSnapshot{}, classify(err, "CAPTURE_PROFILE_SOURCE_QUERY_FAILED")
	}

	rows, err := repository.db.Query(ctx, `
		SELECT chunk.id::text,chunk.source_span_id::text,chunk.sequence,chunk.heading_path,
		       CASE WHEN octet_length(chunk.content) BETWEEN 1 AND $4 THEN chunk.content END
		FROM retrieval.index_manifest_chunk AS manifest_chunk
		JOIN ingestion.canonical_chunk AS chunk
		  ON chunk.id=manifest_chunk.chunk_id AND chunk.workspace_id=manifest_chunk.workspace_id
		JOIN retrieval.chunk_projection AS projection
		  ON projection.index_version_id=manifest_chunk.index_version_id
		 AND projection.chunk_id=manifest_chunk.chunk_id
		 AND projection.workspace_id=manifest_chunk.workspace_id
		WHERE manifest_chunk.workspace_id=$1 AND manifest_chunk.index_version_id=$2
		  AND chunk.parse_projection_id=$3 AND chunk.status='active'
		  AND projection.lexical_status='ready'
		ORDER BY chunk.sequence,chunk.id
		LIMIT $5`, string(lookup.WorkspaceID), string(indexVersionID), string(parseProjectionID),
		captureapp.MaxProfileChunkBytes, captureapp.MaxProfileSourceChunks+1)
	if err != nil {
		return captureapp.ProfileSourceSnapshot{}, classify(err, "CAPTURE_PROFILE_SOURCE_QUERY_FAILED")
	}
	defer rows.Close()
	chunks := make([]captureapp.ProfileSourceChunk, 0)
	for rows.Next() {
		var chunkID, spanID string
		var headingRaw []byte
		var content sql.NullString
		var chunk captureapp.ProfileSourceChunk
		if err := rows.Scan(&chunkID, &spanID, &chunk.Sequence, &headingRaw, &content); err != nil {
			return captureapp.ProfileSourceSnapshot{}, classify(err, "CAPTURE_PROFILE_SOURCE_SCAN_FAILED")
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
		return captureapp.ProfileSourceSnapshot{}, classify(err, "CAPTURE_PROFILE_SOURCE_QUERY_FAILED")
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

func boundedProfileChunkContent(content sql.NullString) (string, error) {
	if !content.Valid || len(content.String) == 0 || len(content.String) > captureapp.MaxProfileChunkBytes ||
		!utf8.ValidString(content.String) {
		return "", inconsistent(profileContextInvalidCode, "profile chunk content is outside the supported boundary")
	}
	return content.String, nil
}

// PrepareProfile 创建或精确恢复一个冻结契约的 Profile Attempt。
func (repository *ProfileRepository) PrepareProfile(ctx context.Context, command captureapp.PrepareProfileCommand) (captureapp.PreparedProfile, error) {
	if repository == nil || repository.db == nil || !validPrepareProfile(command) {
		return captureapp.PreparedProfile{}, invalid(profileContextInvalidCode, "profile prepare command is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		string(command.WorkspaceID)+"\x1f"+string(command.SourceVersionID)); err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	if err := verifyProfileCapture(ctx, tx, command.ProfileLookup); err != nil {
		return captureapp.PreparedProfile{}, err
	}
	if ready, found, err := lookupReadyProfile(ctx, tx, command.ProfileLookup, command.Contract); err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
		}
		return captureapp.PreparedProfile{ProfileID: ready.ProfileID, ReadyRevisionID: ready.RevisionID}, nil
	}

	profile, found, err := loadProfileForUpdate(ctx, tx, command.WorkspaceID, command.SourceVersionID)
	if err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	if !found {
		profile = domain.Profile{
			ID: command.RequestedProfileID, WorkspaceID: command.WorkspaceID, CaptureID: command.CaptureID,
			SourceVersionID: command.SourceVersionID, Status: domain.ProfileStatusRunning, Version: 1,
			CreatedAt: command.StartedAt.UTC(), UpdatedAt: command.StartedAt.UTC(),
		}
		if err := profile.Validate(); err != nil {
			return captureapp.PreparedProfile{}, invalid(profileContextInvalidCode, "new profile state is invalid")
		}
		profile, err = scanProfile(tx.QueryRow(ctx, `INSERT INTO learning.document_knowledge_profile(
			id,workspace_id,capture_id,source_version_id,status,error_code,retryable,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'RUNNING','',false,1,$5,$5)
		RETURNING id::text,workspace_id::text,capture_id::text,source_version_id::text,
			current_revision_id::text,status,error_code,retryable,version,created_at,updated_at`,
			string(profile.ID), string(profile.WorkspaceID), string(profile.CaptureID), string(profile.SourceVersionID), profile.CreatedAt))
		if err != nil {
			return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
		}
	} else if profile.CaptureID != command.CaptureID {
		return captureapp.PreparedProfile{}, inconsistent(profileContextInvalidCode, "profile is bound to another capture")
	}

	existingAttempt, attemptFound, err := loadProfileAttemptByNode(ctx, tx, profile.ID, command.NodeAttemptID, true)
	if err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	if attemptFound {
		prepared, replayErr := replayPreparedAttempt(existingAttempt, command)
		if replayErr != nil {
			return captureapp.PreparedProfile{}, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
		}
		return prepared, nil
	}
	var activeCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_attempt
		WHERE profile_id=$1 AND status='RUNNING'`, string(profile.ID)).Scan(&activeCount); err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	if activeCount != 0 {
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileAttemptConflictCode, true,
			errors.New("another profile attempt is running"))
	}
	if profile.Status != domain.ProfileStatusRunning || profile.ErrorCode != "" || profile.Retryable {
		profile, err = scanProfile(tx.QueryRow(ctx, `UPDATE learning.document_knowledge_profile SET
			status='RUNNING',error_code='',retryable=false,version=version+1,updated_at=GREATEST(updated_at,$3)
			WHERE id=$1 AND workspace_id=$2 AND version=$4
			RETURNING id::text,workspace_id::text,capture_id::text,source_version_id::text,
				current_revision_id::text,status,error_code,retryable,version,created_at,updated_at`,
			string(profile.ID), string(profile.WorkspaceID), command.StartedAt.UTC(), profile.Version))
		if err != nil {
			return captureapp.PreparedProfile{}, profileCASFailure(err, profileVersionConflictCode,
				"profile changed before attempt preparation")
		}
	}
	var attemptNumber int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt_number),0)+1
		FROM learning.document_knowledge_profile_attempt WHERE profile_id=$1`, string(profile.ID)).Scan(&attemptNumber); err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	attempt, err := scanProfileAttempt(tx.QueryRow(ctx, `INSERT INTO learning.document_knowledge_profile_attempt(
		id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,
		model_settings_revision,prompt_version,schema_version,workflow_run_id,node_run_id,node_attempt_id,
		attempt_number,status,error_code,retryable,started_at,version
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'RUNNING','',false,$14,1)
	RETURNING `+profileAttemptReturning,
		string(command.ProfileAttemptID), string(profile.ID), string(command.WorkspaceID), string(command.SourceVersionID),
		string(command.Contract.ParseProjectionID), string(command.Contract.IndexVersionID),
		nullableProfileRevision(command.Contract.ModelSettingsRevision), command.Contract.PromptVersion, command.Contract.SchemaVersion,
		string(command.WorkflowRunID), string(command.NodeRunID), string(command.NodeAttemptID), attemptNumber, command.StartedAt.UTC()))
	if err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	if err := tx.Commit(ctx); err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	return attempt.prepared(), nil
}

// BindProfileModelRun 在任何 Provider 调用前把 Profile Attempt 绑定到 RUNNING Model Run。
func (repository *ProfileRepository) BindProfileModelRun(ctx context.Context, command captureapp.BindProfileModelRunCommand) (captureapp.PreparedProfile, error) {
	if repository == nil || repository.db == nil || !validID(command.WorkspaceID) || !validID(command.ProfileID) ||
		!validID(command.ProfileAttemptID) || !validID(command.ModelRunID) || command.ExpectedProfileAttemptVersion < 1 {
		return captureapp.PreparedProfile{}, invalid(profileContextInvalidCode, "profile model run binding is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	attempt, found, err := loadProfileAttemptByID(ctx, tx, command.WorkspaceID, command.ProfileAttemptID, true)
	if err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	if !found || attempt.ProfileID != command.ProfileID {
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_ATTEMPT_NOT_FOUND", false, pgx.ErrNoRows)
	}
	alreadyBound, err := profileModelRunBindingDecision(attempt, command)
	if err != nil {
		return captureapp.PreparedProfile{}, err
	}
	run, err := repository.modelRuns.GetModelRunTx(ctx, tx, command.WorkspaceID, command.ModelRunID, true)
	if err != nil {
		return captureapp.PreparedProfile{}, err
	}
	if err := validateRunAttemptBinding(run, attempt); err != nil {
		return captureapp.PreparedProfile{}, err
	}
	if !alreadyBound {
		attempt, err = scanProfileAttempt(tx.QueryRow(ctx, `UPDATE learning.document_knowledge_profile_attempt
			SET model_run_id=$3,version=version+1
			WHERE id=$1 AND workspace_id=$2 AND status='RUNNING' AND version=$4
			RETURNING `+profileAttemptReturning,
			string(attempt.ID), string(attempt.WorkspaceID), string(command.ModelRunID), command.ExpectedProfileAttemptVersion))
		if err != nil {
			return captureapp.PreparedProfile{}, profileCASFailure(err, profileVersionConflictCode,
				"profile attempt changed before model run binding")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return captureapp.PreparedProfile{}, classify(err, profilePersistenceFailedCode)
	}
	return attempt.prepared(), nil
}

// CompleteProfile 原子写入 Revision/Evidence、推进 current pointer，并成功终结 Model Run。
func (repository *ProfileRepository) CompleteProfile(ctx context.Context, command captureapp.CompleteProfileCommand) (captureapp.ReadyProfile, error) {
	if repository == nil || repository.db == nil {
		return captureapp.ReadyProfile{}, invalid(profileContextInvalidCode, "profile repository is unavailable")
	}
	evidenceIDs, err := validateCompleteProfile(command)
	if err != nil {
		return captureapp.ReadyProfile{}, err
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	attempt, found, err := loadProfileAttemptByID(ctx, tx, command.WorkspaceID, command.ProfileAttemptID, true)
	if err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	}
	if !found {
		return captureapp.ReadyProfile{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_ATTEMPT_NOT_FOUND", false, pgx.ErrNoRows)
	}
	if attempt.Status == profileAttemptSucceeded {
		if err := repository.verifyCompletedReplay(ctx, tx, attempt, command, evidenceIDs); err != nil {
			return captureapp.ReadyProfile{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
		}
		return captureapp.ReadyProfile{ProfileID: command.Revision.ProfileID, RevisionID: command.Revision.ID}, nil
	}
	if attempt.Status != profileAttemptRunning || attempt.Version != command.ExpectedProfileAttemptVersion ||
		attempt.ModelRunID == "" || attempt.ModelRunID != command.Revision.ModelRunID {
		return captureapp.ReadyProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
			errors.New("profile attempt changed before finalization"))
	}
	if err := validateRevisionAttemptBinding(command.Revision, attempt); err != nil {
		return captureapp.ReadyProfile{}, err
	}
	dependenciesCurrent, err := lockProfileDependencyState(ctx, tx, attempt)
	if err != nil {
		return captureapp.ReadyProfile{}, err
	}
	profile, found, err := loadProfileForUpdate(ctx, tx, command.WorkspaceID, attempt.SourceVersionID)
	if err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	}
	if !found || profile.ID != attempt.ProfileID || profile.Status != domain.ProfileStatusRunning ||
		command.CompletedAt.Before(profile.UpdatedAt) {
		return captureapp.ReadyProfile{}, inconsistent(profileContextInvalidCode, "profile pointer is not running for this attempt")
	}
	record, err := repository.modelRuns.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, attempt.ModelRunID, true)
	if err != nil {
		return captureapp.ReadyProfile{}, err
	}
	if err := validateSuccessfulRun(record, attempt, command.ExpectedModelRunVersion, command.CompletedAt); err != nil {
		return captureapp.ReadyProfile{}, err
	}
	encoded, err := json.Marshal(command.Revision.Content)
	if err != nil {
		return captureapp.ReadyProfile{}, inconsistent(profileContextInvalidCode, "profile content could not be encoded")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.document_knowledge_profile_revision(
		id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,
		model_settings_revision,prompt_version,schema_version,content,content_digest,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		string(command.Revision.ID), string(command.Revision.ProfileID), string(command.Revision.WorkspaceID),
		string(command.Revision.SourceVersionID), string(command.Revision.ParseProjectionID), string(command.Revision.IndexVersionID),
		string(command.Revision.ModelRunID), nullableProfileRevision(command.Revision.ModelSettingsRevision),
		command.Revision.PromptVersion, command.Revision.SchemaVersion, encoded, command.Revision.ContentDigest,
		command.Revision.CreatedAt.UTC()); err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	}
	spanValues := make([]string, len(evidenceIDs))
	for index, id := range evidenceIDs {
		spanValues[index] = string(id)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.document_knowledge_profile_evidence(
		revision_id,workspace_id,source_version_id,source_span_id,created_at
	) SELECT $1,$2,$3,value::uuid,$5 FROM unnest($4::text[]) AS value`,
		string(command.Revision.ID), string(command.WorkspaceID), string(command.Revision.SourceVersionID),
		spanValues, command.CompletedAt.UTC()); err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	}
	finalProfileStatus := domain.ProfileStatusReady
	if !dependenciesCurrent {
		finalProfileStatus = domain.ProfileStatusStale
	}
	if tag, err := tx.Exec(ctx, `UPDATE learning.document_knowledge_profile SET
		current_revision_id=$3,status=$4,error_code='',retryable=false,version=version+1,updated_at=$5
		WHERE id=$1 AND workspace_id=$2 AND status='RUNNING' AND version=$6`,
		string(profile.ID), string(profile.WorkspaceID), string(command.Revision.ID), string(finalProfileStatus),
		command.CompletedAt.UTC(), profile.Version); err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	} else if tag.RowsAffected() != 1 {
		return captureapp.ReadyProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
			errors.New("profile changed before successful finalization"))
	}
	if tag, err := tx.Exec(ctx, `UPDATE learning.document_knowledge_profile_attempt SET
		status='SUCCEEDED',error_code='',retryable=false,completed_at=$3,version=version+1
		WHERE id=$1 AND workspace_id=$2 AND status='RUNNING' AND version=$4`,
		string(attempt.ID), string(attempt.WorkspaceID), command.CompletedAt.UTC(), attempt.Version); err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	} else if tag.RowsAffected() != 1 {
		return captureapp.ReadyProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
			errors.New("profile attempt changed before successful finalization"))
	}
	completedAt := command.CompletedAt.UTC()
	finalRun := record.Run
	finalRun.Status = agentdomain.ModelRunSucceeded
	finalRun.FinalResultType = agentdomain.ResultTypeDocumentKnowledgeProfile
	finalRun.FinalErrorCode = ""
	finalRun.Version = record.Run.Version + 1
	finalRun.UpdatedAt = completedAt
	finalRun.CompletedAt = &completedAt
	if _, replayed, err := repository.modelRuns.FinalizeModelRunTx(ctx, tx, agentapp.FinalizeModelRunCommand{
		ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun,
	}); err != nil {
		return captureapp.ReadyProfile{}, err
	} else if replayed {
		return captureapp.ReadyProfile{}, inconsistent(profileContextInvalidCode, "profile model run was terminal before atomic completion")
	}
	if err := tx.Commit(ctx); err != nil {
		return captureapp.ReadyProfile{}, classify(err, profileFinalizationFailedCode)
	}
	return captureapp.ReadyProfile{ProfileID: profile.ID, RevisionID: command.Revision.ID}, nil
}

func lockProfileDependencyState(ctx context.Context, tx pgx.Tx, attempt profileAttemptRecord) (bool, error) {
	var activeSettingsRevision int64
	if err := tx.QueryRow(ctx, `SELECT active_revision FROM ops.model_settings_state
		WHERE singleton=true FOR SHARE`).Scan(&activeSettingsRevision); err != nil {
		return false, classify(err, profileFinalizationFailedCode)
	}
	settingsCurrent := attempt.ModelSettingsRevision == nil && activeSettingsRevision == 0 ||
		attempt.ModelSettingsRevision != nil && *attempt.ModelSettingsRevision == activeSettingsRevision

	// Index activation locks the current Index Version before touching Profile rows.
	// Preserve that order, then re-read active state after the per-Source fence.
	var activeIndexID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM retrieval.index_version
		WHERE workspace_id=$1 AND status='active' FOR SHARE`, string(attempt.WorkspaceID)).Scan(&activeIndexID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, classify(err, profileFinalizationFailedCode)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		string(attempt.WorkspaceID)+"\x1f"+string(attempt.SourceVersionID)); err != nil {
		return false, classify(err, profileFinalizationFailedCode)
	}

	var activeParseProjection sql.NullString
	err = tx.QueryRow(ctx, `SELECT manifest_source.parse_projection_id::text
		FROM retrieval.index_version AS index_version
		LEFT JOIN retrieval.index_manifest_source AS manifest_source
		  ON manifest_source.index_version_id=index_version.id
		 AND manifest_source.workspace_id=index_version.workspace_id
		 AND manifest_source.source_version_id=$2
		 AND manifest_source.selection_status='included'
		WHERE index_version.workspace_id=$1 AND index_version.status='active'
		FOR SHARE OF index_version`, string(attempt.WorkspaceID), string(attempt.SourceVersionID)).Scan(&activeParseProjection)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, classify(err, profileFinalizationFailedCode)
	}
	parseProjectionCurrent := !activeParseProjection.Valid || activeParseProjection.String == string(attempt.ParseProjectionID)
	return settingsCurrent && parseProjectionCurrent, nil
}

// FailProfile 原子终结 Profile/Attempt，并在已绑定时同时终结 Agent Model Run。
func (repository *ProfileRepository) FailProfile(ctx context.Context, command captureapp.FailProfileCommand) error {
	if repository == nil || repository.db == nil || validateFailProfile(command) != nil {
		return invalid(profileContextInvalidCode, "profile failure command is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return classify(err, profileFinalizationFailedCode)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	attempt, found, err := loadProfileAttemptByID(ctx, tx, command.WorkspaceID, command.ProfileAttemptID, true)
	if err != nil {
		return classify(err, profileFinalizationFailedCode)
	}
	if !found || attempt.ProfileID != command.ProfileID {
		return foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_ATTEMPT_NOT_FOUND", false, pgx.ErrNoRows)
	}
	wantedAttemptStatus := profileAttemptFailed
	if command.ProfileStatus == domain.ProfileStatusCapabilityUnavailable {
		wantedAttemptStatus = profileAttemptCapabilityUnavailable
	}
	profile, found, err := loadProfileForUpdate(ctx, tx, command.WorkspaceID, attempt.SourceVersionID)
	if err != nil {
		return classify(err, profileFinalizationFailedCode)
	}
	if !found || profile.ID != attempt.ProfileID || command.CompletedAt.Before(profile.UpdatedAt) ||
		command.CompletedAt.Before(attempt.StartedAt) {
		return inconsistent(profileContextInvalidCode, "profile failure pointer is missing")
	}
	if attempt.Status != profileAttemptRunning {
		if exactFailedProfileReplay(attempt, profile, command, wantedAttemptStatus) {
			if err := repository.verifyFailedRunReplay(ctx, tx, attempt, command); err != nil {
				return err
			}
			if err := tx.Commit(ctx); err != nil {
				return classify(err, profileFinalizationFailedCode)
			}
			return nil
		}
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("profile attempt already has another terminal result"))
	}
	if attempt.Version != command.ExpectedProfileAttemptVersion || attempt.ModelRunID != command.ModelRunID {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
			errors.New("profile attempt changed before failure finalization"))
	}
	if command.ModelRunID != "" {
		record, err := repository.modelRuns.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, command.ModelRunID, true)
		if err != nil {
			return err
		}
		if err := validateRunAttemptBinding(record.Run, attempt); err != nil ||
			record.Run.Status != agentdomain.ModelRunRunning || record.Run.Version != command.ExpectedModelRunVersion {
			if err != nil {
				return err
			}
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
				errors.New("model run changed before profile failure finalization"))
		}
		completedAt := command.CompletedAt.UTC()
		finalRun := record.Run
		finalRun.Status = command.ModelRunStatus
		finalRun.FinalErrorCode = command.ErrorCode
		finalRun.Version = record.Run.Version + 1
		finalRun.UpdatedAt = completedAt
		finalRun.CompletedAt = &completedAt
		if _, replayed, err := repository.modelRuns.FinalizeModelRunTx(ctx, tx, agentapp.FinalizeModelRunCommand{
			ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun,
		}); err != nil {
			return err
		} else if replayed {
			return inconsistent(profileContextInvalidCode, "profile model run was terminal before atomic failure")
		}
	}
	if tag, err := tx.Exec(ctx, `UPDATE learning.document_knowledge_profile SET
		status=$3,error_code=$4,retryable=$5,version=version+1,updated_at=$6
		WHERE id=$1 AND workspace_id=$2 AND version=$7`,
		string(profile.ID), string(profile.WorkspaceID), string(command.ProfileStatus), command.ErrorCode,
		command.Retryable, command.CompletedAt.UTC(), profile.Version); err != nil {
		return classify(err, profileFinalizationFailedCode)
	} else if tag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true, errors.New("profile changed before failure finalization"))
	}
	if tag, err := tx.Exec(ctx, `UPDATE learning.document_knowledge_profile_attempt SET
		status=$3,error_code=$4,retryable=$5,completed_at=$6,version=version+1
		WHERE id=$1 AND workspace_id=$2 AND status='RUNNING' AND version=$7`,
		string(attempt.ID), string(attempt.WorkspaceID), wantedAttemptStatus, command.ErrorCode,
		command.Retryable, command.CompletedAt.UTC(), attempt.Version); err != nil {
		return classify(err, profileFinalizationFailedCode)
	} else if tag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true, errors.New("profile attempt changed before failure finalization"))
	}
	if err := tx.Commit(ctx); err != nil {
		return classify(err, profileFinalizationFailedCode)
	}
	return nil
}

// GetProfile 返回 Source Version 的权威画像状态、当前 Revision 与完整 Evidence。
func (repository *ProfileRepository) GetProfile(ctx context.Context, query captureapp.ProfileQuery) (captureapp.ProfileView, error) {
	if repository == nil || repository.db == nil || !validID(query.WorkspaceID) || !validID(query.SourceVersionID) {
		return captureapp.ProfileView{}, invalid(profileQueryInvalidCode, "profile detail query is invalid")
	}
	profile, found, err := loadProfileBySource(ctx, repository.db, query.WorkspaceID, query.SourceVersionID)
	if err != nil {
		return captureapp.ProfileView{}, classify(err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	if !found {
		return captureapp.ProfileView{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_NOT_FOUND", false, pgx.ErrNoRows)
	}
	return loadProfileView(ctx, repository.db, profile)
}

// GetProfiles returns existing Workspace-bound Profile views in stable Source Version order.
// It uses three bounded set queries for profiles, current revisions, and evidence.
func (repository *ProfileRepository) GetProfiles(ctx context.Context, query captureapp.ProfileBatchQuery) ([]captureapp.ProfileView, error) {
	if repository == nil || repository.db == nil || !validID(query.WorkspaceID) ||
		len(query.SourceVersionIDs) == 0 || len(query.SourceVersionIDs) > captureapp.MaxProfileBatchSize {
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

	rows, err := repository.db.Query(ctx, `SELECT id::text,workspace_id::text,capture_id::text,source_version_id::text,
		current_revision_id::text,status,error_code,retryable,version,created_at,updated_at
		FROM learning.document_knowledge_profile
		WHERE workspace_id=$1 AND source_version_id=ANY($2::uuid[])
		ORDER BY source_version_id`, string(query.WorkspaceID), sourceVersionIDs)
	if err != nil {
		return nil, classify(err, "CAPTURE_PROFILE_BATCH_QUERY_FAILED")
	}
	profiles := make([]domain.Profile, 0, len(sourceVersionIDs))
	for rows.Next() {
		profile, scanErr := scanProfile(rows)
		if scanErr != nil {
			rows.Close()
			return nil, classify(scanErr, "CAPTURE_PROFILE_BATCH_SCAN_FAILED")
		}
		if profile.WorkspaceID != query.WorkspaceID || profile.Validate() != nil {
			rows.Close()
			return nil, inconsistent(profileContextInvalidCode, "profile batch returned an invalid profile")
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classify(err, "CAPTURE_PROFILE_BATCH_QUERY_FAILED")
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

	revisionRows, err := repository.db.Query(ctx, `SELECT id::text,profile_id::text,workspace_id::text,source_version_id::text,
		parse_projection_id::text,index_version_id::text,model_run_id::text,model_settings_revision,
		prompt_version,schema_version,content,content_digest,created_at
		FROM learning.document_knowledge_profile_revision
		WHERE workspace_id=$1 AND id=ANY($2::uuid[])
		ORDER BY id`, string(query.WorkspaceID), revisionIDs)
	if err != nil {
		return nil, classify(err, "CAPTURE_PROFILE_BATCH_REVISION_QUERY_FAILED")
	}
	foundRevisions := make(map[foundation.ID]struct{}, len(revisionIDs))
	for revisionRows.Next() {
		revision, scanErr := scanProfileRevision(revisionRows)
		if scanErr != nil {
			revisionRows.Close()
			return nil, classify(scanErr, "CAPTURE_PROFILE_BATCH_REVISION_SCAN_FAILED")
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
		return nil, classify(err, "CAPTURE_PROFILE_BATCH_REVISION_QUERY_FAILED")
	}
	revisionRows.Close()
	if len(foundRevisions) != len(revisionIDs) {
		return nil, inconsistent(profileContextInvalidCode, "profile batch current revision is missing")
	}

	evidenceRows, err := repository.db.Query(ctx, `SELECT revision_id::text,source_version_id::text,source_span_id::text,created_at
		FROM learning.document_knowledge_profile_evidence
		WHERE workspace_id=$1 AND revision_id=ANY($2::uuid[])
		ORDER BY revision_id,source_span_id`, string(query.WorkspaceID), revisionIDs)
	if err != nil {
		return nil, classify(err, "CAPTURE_PROFILE_BATCH_EVIDENCE_QUERY_FAILED")
	}
	for evidenceRows.Next() {
		var revisionRaw, sourceVersionRaw, sourceSpanRaw string
		var item domain.ProfileEvidence
		if err := evidenceRows.Scan(&revisionRaw, &sourceVersionRaw, &sourceSpanRaw, &item.CreatedAt); err != nil {
			evidenceRows.Close()
			return nil, classify(err, "CAPTURE_PROFILE_BATCH_EVIDENCE_SCAN_FAILED")
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
		return nil, classify(err, "CAPTURE_PROFILE_BATCH_EVIDENCE_QUERY_FAILED")
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

type profileDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type profileReadDB interface {
	profileDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type profileAttemptRecord struct {
	ID                    foundation.ID
	ProfileID             foundation.ID
	WorkspaceID           foundation.ID
	SourceVersionID       foundation.ID
	ParseProjectionID     foundation.ID
	IndexVersionID        foundation.ID
	ModelSettingsRevision *int64
	PromptVersion         string
	SchemaVersion         string
	WorkflowRunID         foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	ModelRunID            foundation.ID
	AttemptNumber         int
	Status                string
	ErrorCode             string
	Retryable             bool
	StartedAt             time.Time
	CompletedAt           *time.Time
	Version               int64
}

const (
	profileAttemptRunning               = "RUNNING"
	profileAttemptSucceeded             = "SUCCEEDED"
	profileAttemptFailed                = "FAILED"
	profileAttemptCapabilityUnavailable = "CAPABILITY_UNAVAILABLE"
	profileAttemptReturning             = `id::text,profile_id::text,workspace_id::text,source_version_id::text,
		parse_projection_id::text,index_version_id::text,model_settings_revision,prompt_version,schema_version,
		workflow_run_id::text,node_run_id::text,node_attempt_id::text,model_run_id::text,
		attempt_number,status,error_code,retryable,started_at,completed_at,version`
)

func (attempt profileAttemptRecord) prepared() captureapp.PreparedProfile {
	return captureapp.PreparedProfile{
		ProfileID: attempt.ProfileID, ProfileAttemptID: attempt.ID, ProfileAttemptNumber: attempt.AttemptNumber,
		ProfileAttemptVersion: attempt.Version, StartedAt: attempt.StartedAt, ModelRunID: attempt.ModelRunID,
	}
}

func profileModelRunBindingDecision(attempt profileAttemptRecord, command captureapp.BindProfileModelRunCommand) (bool, error) {
	if attempt.Status != profileAttemptRunning {
		return false, foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("profile attempt is already terminal"))
	}
	if attempt.Version == command.ExpectedProfileAttemptVersion {
		if attempt.ModelRunID == "" {
			return false, nil
		}
		if attempt.ModelRunID == command.ModelRunID {
			return true, nil
		}
	}
	if attempt.Version == command.ExpectedProfileAttemptVersion+1 && attempt.ModelRunID == command.ModelRunID {
		return true, nil
	}
	return false, foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
		errors.New("profile attempt changed before model run binding"))
}

func profileCASFailure(err error, code, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorVersionConflict, code, true, errors.New(message))
	}
	return classify(err, code)
}

func lookupReadyProfile(ctx context.Context, db profileDB, lookup captureapp.ProfileLookup, contract captureapp.ProfileContract) (captureapp.ReadyProfile, bool, error) {
	var profileID, revisionID string
	err := db.QueryRow(ctx, `SELECT profile.id::text,revision.id::text
		FROM learning.document_knowledge_profile AS profile
		JOIN learning.document_knowledge_profile_revision AS revision
		  ON revision.id=profile.current_revision_id
		 AND revision.profile_id=profile.id
		 AND revision.workspace_id=profile.workspace_id
		 AND revision.source_version_id=profile.source_version_id
		WHERE profile.workspace_id=$1 AND profile.capture_id=$2 AND profile.source_version_id=$3
		  AND profile.status='READY'
		  AND revision.parse_projection_id=$4 AND revision.index_version_id=$5
		  AND revision.model_settings_revision IS NOT DISTINCT FROM $6::bigint
		  AND revision.prompt_version=$7 AND revision.schema_version=$8
		ORDER BY revision.created_at DESC,revision.id DESC LIMIT 1`,
		string(lookup.WorkspaceID), string(lookup.CaptureID), string(lookup.SourceVersionID),
		string(contract.ParseProjectionID), string(contract.IndexVersionID), nullableProfileRevision(contract.ModelSettingsRevision),
		contract.PromptVersion, contract.SchemaVersion).Scan(&profileID, &revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return captureapp.ReadyProfile{}, false, nil
	}
	if err != nil {
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

func verifyProfileCapture(ctx context.Context, tx pgx.Tx, lookup captureapp.ProfileLookup) error {
	var found bool
	err := tx.QueryRow(ctx, `SELECT true FROM core.capture AS capture
		JOIN core.source_version AS source_version
		  ON source_version.id=$3 AND source_version.source_id=capture.source_id
		WHERE capture.workspace_id=$1 AND capture.id=$2
		  AND capture.latest_source_version_id=source_version.id`,
		string(lookup.WorkspaceID), string(lookup.CaptureID), string(lookup.SourceVersionID)).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return inconsistent(profileContextInvalidCode, "profile capture/source binding does not exist")
	}
	if err != nil {
		return classify(err, profilePersistenceFailedCode)
	}
	return nil
}

func loadProfileBySource(ctx context.Context, db profileDB, workspaceID, sourceVersionID foundation.ID) (domain.Profile, bool, error) {
	profile, err := scanProfile(db.QueryRow(ctx, `SELECT id::text,workspace_id::text,capture_id::text,source_version_id::text,
		current_revision_id::text,status,error_code,retryable,version,created_at,updated_at
		FROM learning.document_knowledge_profile WHERE workspace_id=$1 AND source_version_id=$2`,
		string(workspaceID), string(sourceVersionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, false, nil
	}
	return profile, err == nil, err
}

func loadProfileForUpdate(ctx context.Context, tx pgx.Tx, workspaceID, sourceVersionID foundation.ID) (domain.Profile, bool, error) {
	profile, err := scanProfile(tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,capture_id::text,source_version_id::text,
		current_revision_id::text,status,error_code,retryable,version,created_at,updated_at
		FROM learning.document_knowledge_profile WHERE workspace_id=$1 AND source_version_id=$2 FOR UPDATE`,
		string(workspaceID), string(sourceVersionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, false, nil
	}
	return profile, err == nil, err
}

func loadProfileView(ctx context.Context, db profileReadDB, profile domain.Profile) (captureapp.ProfileView, error) {
	if err := profile.Validate(); err != nil {
		return captureapp.ProfileView{}, inconsistent(profileContextInvalidCode, "persisted profile is invalid")
	}
	view := captureapp.ProfileView{Profile: profile, Evidence: []domain.ProfileEvidence{}}
	if profile.CurrentRevisionID == "" {
		return view, nil
	}
	revision, found, err := loadProfileRevision(ctx, db, profile.WorkspaceID, profile.CurrentRevisionID)
	if err != nil {
		return captureapp.ProfileView{}, classify(err, "CAPTURE_PROFILE_REVISION_QUERY_FAILED")
	}
	if !found || revision.ProfileID != profile.ID || revision.WorkspaceID != profile.WorkspaceID ||
		revision.SourceVersionID != profile.SourceVersionID {
		return captureapp.ProfileView{}, inconsistent(profileContextInvalidCode, "profile current revision binding is invalid")
	}
	evidence, err := loadProfileEvidence(ctx, db, revision)
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

func loadProfileAttemptByNode(ctx context.Context, db profileDB, profileID, nodeAttemptID foundation.ID, forUpdate bool) (profileAttemptRecord, bool, error) {
	query := `SELECT ` + profileAttemptReturning + ` FROM learning.document_knowledge_profile_attempt
		WHERE profile_id=$1 AND node_attempt_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	attempt, err := scanProfileAttempt(db.QueryRow(ctx, query, string(profileID), string(nodeAttemptID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return profileAttemptRecord{}, false, nil
	}
	return attempt, err == nil, err
}

func loadProfileAttemptByID(ctx context.Context, db profileDB, workspaceID, attemptID foundation.ID, forUpdate bool) (profileAttemptRecord, bool, error) {
	query := `SELECT ` + profileAttemptReturning + ` FROM learning.document_knowledge_profile_attempt
		WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	attempt, err := scanProfileAttempt(db.QueryRow(ctx, query, string(workspaceID), string(attemptID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return profileAttemptRecord{}, false, nil
	}
	return attempt, err == nil, err
}

func scanProfileAttempt(row rowScanner) (profileAttemptRecord, error) {
	var attempt profileAttemptRecord
	var id, profileID, workspaceID, sourceVersionID, parseProjectionID, indexVersionID string
	var workflowRunID, nodeRunID, nodeAttemptID string
	var modelSettings sql.NullInt64
	var modelRunID sql.NullString
	if err := row.Scan(&id, &profileID, &workspaceID, &sourceVersionID, &parseProjectionID, &indexVersionID,
		&modelSettings, &attempt.PromptVersion, &attempt.SchemaVersion, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&modelRunID, &attempt.AttemptNumber, &attempt.Status, &attempt.ErrorCode, &attempt.Retryable,
		&attempt.StartedAt, &attempt.CompletedAt, &attempt.Version); err != nil {
		return profileAttemptRecord{}, err
	}
	bindings := []struct {
		raw    string
		target *foundation.ID
	}{
		{id, &attempt.ID}, {profileID, &attempt.ProfileID}, {workspaceID, &attempt.WorkspaceID},
		{sourceVersionID, &attempt.SourceVersionID}, {parseProjectionID, &attempt.ParseProjectionID},
		{indexVersionID, &attempt.IndexVersionID}, {workflowRunID, &attempt.WorkflowRunID},
		{nodeRunID, &attempt.NodeRunID}, {nodeAttemptID, &attempt.NodeAttemptID},
	}
	for _, binding := range bindings {
		parsed, err := foundation.ParseID(binding.raw)
		if err != nil {
			return profileAttemptRecord{}, err
		}
		*binding.target = parsed
	}
	if modelSettings.Valid {
		value := modelSettings.Int64
		attempt.ModelSettingsRevision = &value
	}
	if modelRunID.Valid {
		parsed, err := foundation.ParseID(modelRunID.String)
		if err != nil {
			return profileAttemptRecord{}, err
		}
		attempt.ModelRunID = parsed
	}
	if !validProfileContract(captureapp.ProfileContract{
		ParseProjectionID: attempt.ParseProjectionID, IndexVersionID: attempt.IndexVersionID,
		ModelSettingsRevision: attempt.ModelSettingsRevision, PromptVersion: attempt.PromptVersion,
		SchemaVersion: attempt.SchemaVersion,
	}) || attempt.AttemptNumber < 1 || attempt.Version < 1 || attempt.StartedAt.IsZero() ||
		!validProfileAttemptState(attempt) {
		return profileAttemptRecord{}, inconsistent(profileContextInvalidCode, "persisted profile attempt is invalid")
	}
	return attempt, nil
}

func validProfileAttemptState(attempt profileAttemptRecord) bool {
	switch attempt.Status {
	case profileAttemptRunning:
		return attempt.CompletedAt == nil && attempt.ErrorCode == "" && !attempt.Retryable
	case profileAttemptSucceeded:
		return attempt.CompletedAt != nil && attempt.ErrorCode == "" && !attempt.Retryable && attempt.ModelRunID != ""
	case profileAttemptCapabilityUnavailable:
		return attempt.CompletedAt != nil && strictProfileErrorCode(attempt.ErrorCode) && !attempt.Retryable && attempt.ModelRunID == ""
	case profileAttemptFailed, "CANCELLED":
		return attempt.CompletedAt != nil && strictProfileErrorCode(attempt.ErrorCode)
	default:
		return false
	}
}

func replayPreparedAttempt(attempt profileAttemptRecord, command captureapp.PrepareProfileCommand) (captureapp.PreparedProfile, error) {
	if attempt.WorkspaceID != command.WorkspaceID || attempt.SourceVersionID != command.SourceVersionID ||
		attempt.WorkflowRunID != command.WorkflowRunID || attempt.NodeRunID != command.NodeRunID ||
		attempt.NodeAttemptID != command.NodeAttemptID || !sameProfileContract(attempt, command.Contract) {
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileAttemptConflictCode, false,
			errors.New("profile node attempt is bound to another frozen request"))
	}
	switch attempt.Status {
	case profileAttemptRunning:
		return attempt.prepared(), nil
	case profileAttemptCapabilityUnavailable:
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorDependencyUnavailable, attempt.ErrorCode, false,
			errors.New("profile capability is unavailable for this node attempt"))
	case profileAttemptFailed:
		kind := foundation.ErrorNonRetryableFailure
		if attempt.Retryable {
			kind = foundation.ErrorRetryableFailure
		}
		return captureapp.PreparedProfile{}, foundation.NewError(kind, attempt.ErrorCode, attempt.Retryable,
			errors.New("profile generation already failed for this node attempt"))
	default:
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileAttemptConflictCode, false,
			fmt.Errorf("profile node attempt is already %s", attempt.Status))
	}
}

func validateRunAttemptBinding(run agentdomain.ModelRun, attempt profileAttemptRecord) error {
	if agentdomain.ValidateModelRun(run) != nil || run.Status != agentdomain.ModelRunRunning || !modelRunMatchesAttempt(run, attempt) {
		return inconsistent(profileContextInvalidCode, "model run differs from the frozen profile attempt")
	}
	return nil
}

func modelRunMatchesAttempt(run agentdomain.ModelRun, attempt profileAttemptRecord) bool {
	return run.WorkspaceID == attempt.WorkspaceID && run.WorkflowRunID == attempt.WorkflowRunID &&
		run.NodeRunID == attempt.NodeRunID && run.NodeAttemptID == attempt.NodeAttemptID &&
		run.Retrieval.IndexVersionID == attempt.IndexVersionID &&
		sameNullableRevision(run.ModelSettingsRevision, attempt.ModelSettingsRevision) &&
		run.Prompt.Version == attempt.PromptVersion
}

func validateSuccessfulRun(record agentapp.ModelRunRecord, attempt profileAttemptRecord, expectedVersion int64, completedAt time.Time) error {
	if err := validateRunAttemptBinding(record.Run, attempt); err != nil {
		return err
	}
	if expectedVersion < 1 || record.Run.Version != expectedVersion || len(record.Calls) == 0 || len(record.Calls) > 3 ||
		completedAt.IsZero() || completedAt.Before(record.Run.UpdatedAt) {
		return inconsistent(profileContextInvalidCode, "profile model run is incomplete or changed")
	}
	succeeded := false
	for index, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != record.Run.ID || call.CallNo != index+1 ||
			call.Status == agentdomain.ModelCallStarted || call.Status == agentdomain.ModelCallUnknown {
			return inconsistent(profileContextInvalidCode, "profile model call history is incomplete")
		}
		if call.CompletedAt == nil || completedAt.Before(*call.CompletedAt) {
			return inconsistent(profileContextInvalidCode, "profile completion precedes a model call")
		}
		succeeded = succeeded || call.Status == agentdomain.ModelCallSucceeded
	}
	if !succeeded || record.Calls[len(record.Calls)-1].Status != agentdomain.ModelCallSucceeded {
		return inconsistent(profileContextInvalidCode, "profile model run has no final successful call")
	}
	return nil
}

func validateRevisionAttemptBinding(revision domain.ProfileRevision, attempt profileAttemptRecord) error {
	if revision.Validate() != nil || revision.ProfileID != attempt.ProfileID || revision.WorkspaceID != attempt.WorkspaceID ||
		revision.SourceVersionID != attempt.SourceVersionID || revision.ParseProjectionID != attempt.ParseProjectionID ||
		revision.IndexVersionID != attempt.IndexVersionID || revision.ModelRunID != attempt.ModelRunID ||
		!sameNullableRevision(revision.ModelSettingsRevision, attempt.ModelSettingsRevision) ||
		revision.PromptVersion != attempt.PromptVersion || revision.SchemaVersion != attempt.SchemaVersion {
		return inconsistent(profileContextInvalidCode, "profile revision differs from its frozen attempt")
	}
	return nil
}

func validateCompleteProfile(command captureapp.CompleteProfileCommand) ([]foundation.ID, error) {
	if !validID(command.WorkspaceID) || !validID(command.ProfileAttemptID) ||
		command.ExpectedProfileAttemptVersion < 1 || command.ExpectedModelRunVersion < 1 ||
		command.Revision.Validate() != nil || command.Revision.WorkspaceID != command.WorkspaceID ||
		command.CompletedAt.IsZero() || !command.CompletedAt.Equal(command.Revision.CreatedAt) ||
		len(command.Revision.Content.Topics) == 0 || len(command.Revision.Content.KnowledgePoints) == 0 ||
		len(command.Evidence) == 0 || len(command.Evidence) > captureapp.MaxProfileSourceChunks {
		return nil, invalid(profileContextInvalidCode, "profile completion command is invalid")
	}
	wanted := profileContentEvidence(command.Revision.Content)
	actual := make([]foundation.ID, len(command.Evidence))
	seen := make(map[foundation.ID]struct{}, len(command.Evidence))
	for index, evidence := range command.Evidence {
		if evidence.RevisionID != command.Revision.ID || evidence.WorkspaceID != command.WorkspaceID ||
			evidence.SourceVersionID != command.Revision.SourceVersionID || !validID(evidence.SourceSpanID) ||
			evidence.CreatedAt.IsZero() || !evidence.CreatedAt.Equal(command.CompletedAt) {
			return nil, invalid(profileContextInvalidCode, "profile evidence binding is invalid")
		}
		if _, duplicate := seen[evidence.SourceSpanID]; duplicate {
			return nil, invalid(profileContextInvalidCode, "profile evidence contains duplicate spans")
		}
		seen[evidence.SourceSpanID] = struct{}{}
		actual[index] = evidence.SourceSpanID
	}
	sort.Slice(actual, func(left, right int) bool { return actual[left] < actual[right] })
	if len(actual) == 0 || !sameFoundationIDs(wanted, actual) {
		return nil, invalid(profileContextInvalidCode, "profile evidence does not exactly cover the content")
	}
	return actual, nil
}

func validateFailProfile(command captureapp.FailProfileCommand) error {
	if !validID(command.WorkspaceID) || !validID(command.ProfileID) || !validID(command.ProfileAttemptID) ||
		command.ExpectedProfileAttemptVersion < 1 || !strictProfileErrorCode(command.ErrorCode) || command.CompletedAt.IsZero() ||
		(command.ProfileStatus != domain.ProfileStatusFailed && command.ProfileStatus != domain.ProfileStatusCapabilityUnavailable) {
		return errors.New("profile failure command is invalid")
	}
	if command.ProfileStatus == domain.ProfileStatusCapabilityUnavailable {
		if command.Retryable || command.ModelRunID != "" || command.ExpectedModelRunVersion != 0 || command.ModelRunStatus != "" {
			return errors.New("unavailable profile cannot bind a model run")
		}
		return nil
	}
	if command.ModelRunID == "" {
		if command.ExpectedModelRunVersion != 0 || command.ModelRunStatus != "" {
			return errors.New("unbound profile failure contains model run state")
		}
		return nil
	}
	if !validID(command.ModelRunID) || command.ExpectedModelRunVersion < 1 ||
		(command.ModelRunStatus != agentdomain.ModelRunFailed && command.ModelRunStatus != agentdomain.ModelRunUnknown) {
		return errors.New("profile failure model run state is invalid")
	}
	return nil
}

func (repository *ProfileRepository) verifyCompletedReplay(ctx context.Context, tx pgx.Tx, attempt profileAttemptRecord, command captureapp.CompleteProfileCommand, evidenceIDs []foundation.ID) error {
	if !profileTerminalAttemptReplay(attempt, command.ExpectedProfileAttemptVersion, command.CompletedAt) ||
		attempt.ProfileID != command.Revision.ProfileID || attempt.ModelRunID != command.Revision.ModelRunID ||
		validateRevisionAttemptBinding(command.Revision, attempt) != nil {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("completed profile attempt differs from replay"))
	}
	persisted, found, err := loadProfileRevision(ctx, tx, command.WorkspaceID, command.Revision.ID)
	if err != nil {
		return classify(err, profileFinalizationFailedCode)
	}
	if !found || !sameProfileRevision(persisted, command.Revision) {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("persisted profile revision differs from replay"))
	}
	evidence, err := loadProfileEvidence(ctx, tx, persisted)
	if err != nil {
		return err
	}
	if !sameFoundationIDs(evidenceSpanIDs(evidence), evidenceIDs) {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("persisted profile evidence differs from replay"))
	}
	record, err := repository.modelRuns.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, attempt.ModelRunID, true)
	if err != nil {
		return err
	}
	if agentdomain.ValidateModelRun(record.Run) != nil || !modelRunMatchesAttempt(record.Run, attempt) ||
		record.Run.Status != agentdomain.ModelRunSucceeded ||
		record.Run.FinalResultType != agentdomain.ResultTypeDocumentKnowledgeProfile || record.Run.FinalErrorCode != "" ||
		record.Run.Version != command.ExpectedModelRunVersion+1 || record.Run.CompletedAt == nil ||
		!samePersistedTime(*record.Run.CompletedAt, command.CompletedAt) {
		return inconsistent(profileContextInvalidCode, "profile completion replay model run differs")
	}
	return nil
}

func (repository *ProfileRepository) verifyFailedRunReplay(ctx context.Context, tx pgx.Tx, attempt profileAttemptRecord, command captureapp.FailProfileCommand) error {
	if command.ModelRunID == "" {
		return nil
	}
	record, err := repository.modelRuns.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, command.ModelRunID, true)
	if err != nil {
		return err
	}
	if agentdomain.ValidateModelRun(record.Run) != nil || !modelRunMatchesAttempt(record.Run, attempt) ||
		record.Run.Status != command.ModelRunStatus || record.Run.FinalResultType != "" ||
		record.Run.FinalErrorCode != command.ErrorCode || record.Run.Version != command.ExpectedModelRunVersion+1 ||
		record.Run.CompletedAt == nil || !samePersistedTime(*record.Run.CompletedAt, command.CompletedAt) {
		return inconsistent(profileContextInvalidCode, "profile failure replay model run differs")
	}
	return nil
}

func loadProfileRevision(ctx context.Context, db profileDB, workspaceID, revisionID foundation.ID) (domain.ProfileRevision, bool, error) {
	revision, err := scanProfileRevision(db.QueryRow(ctx, `SELECT id::text,profile_id::text,workspace_id::text,source_version_id::text,
		parse_projection_id::text,index_version_id::text,model_run_id::text,model_settings_revision,
		prompt_version,schema_version,content,content_digest,created_at
		FROM learning.document_knowledge_profile_revision WHERE workspace_id=$1 AND id=$2`,
		string(workspaceID), string(revisionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ProfileRevision{}, false, nil
	}
	if err != nil {
		return domain.ProfileRevision{}, false, err
	}
	return revision, true, nil
}

func scanProfileRevision(row rowScanner) (domain.ProfileRevision, error) {
	var revision domain.ProfileRevision
	var id, profileID, storedWorkspaceID, sourceVersionID, parseProjectionID, indexVersionID, modelRunID string
	var modelSettings sql.NullInt64
	var contentRaw []byte
	if err := row.Scan(&id, &profileID, &storedWorkspaceID, &sourceVersionID, &parseProjectionID, &indexVersionID,
		&modelRunID, &modelSettings, &revision.PromptVersion, &revision.SchemaVersion, &contentRaw,
		&revision.ContentDigest, &revision.CreatedAt); err != nil {
		return domain.ProfileRevision{}, err
	}
	bindings := []struct {
		raw    string
		target *foundation.ID
	}{
		{id, &revision.ID}, {profileID, &revision.ProfileID}, {storedWorkspaceID, &revision.WorkspaceID},
		{sourceVersionID, &revision.SourceVersionID}, {parseProjectionID, &revision.ParseProjectionID},
		{indexVersionID, &revision.IndexVersionID}, {modelRunID, &revision.ModelRunID},
	}
	for _, binding := range bindings {
		parsed, parseErr := foundation.ParseID(binding.raw)
		if parseErr != nil {
			return domain.ProfileRevision{}, parseErr
		}
		*binding.target = parsed
	}
	if modelSettings.Valid {
		value := modelSettings.Int64
		revision.ModelSettingsRevision = &value
	}
	content, err := decodeProfileContent(contentRaw)
	if err != nil {
		return domain.ProfileRevision{}, err
	}
	revision.Content = content
	if err := revision.Validate(); err != nil {
		return domain.ProfileRevision{}, err
	}
	return revision, nil
}

func loadProfileEvidence(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, revision domain.ProfileRevision) ([]domain.ProfileEvidence, error) {
	rows, err := db.Query(ctx, `SELECT source_span_id::text,created_at
		FROM learning.document_knowledge_profile_evidence
		WHERE revision_id=$1 AND workspace_id=$2 AND source_version_id=$3
		ORDER BY source_span_id`, string(revision.ID), string(revision.WorkspaceID), string(revision.SourceVersionID))
	if err != nil {
		return nil, classify(err, "CAPTURE_PROFILE_EVIDENCE_QUERY_FAILED")
	}
	defer rows.Close()
	evidence := make([]domain.ProfileEvidence, 0)
	for rows.Next() {
		var rawID string
		var item domain.ProfileEvidence
		if err := rows.Scan(&rawID, &item.CreatedAt); err != nil {
			return nil, classify(err, "CAPTURE_PROFILE_EVIDENCE_SCAN_FAILED")
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
		return nil, classify(err, "CAPTURE_PROFILE_EVIDENCE_QUERY_FAILED")
	}
	return evidence, nil
}

func decodeHeadingPath(raw []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var headings []string
	if err := decoder.Decode(&headings); err != nil || headings == nil {
		return nil, inconsistent(profileContextInvalidCode, "profile chunk heading path is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, inconsistent(profileContextInvalidCode, "profile chunk heading path contains trailing JSON")
	}
	for _, heading := range headings {
		if heading == "" || strings.TrimSpace(heading) != heading || !utf8.ValidString(heading) || len(heading) > 512 {
			return nil, inconsistent(profileContextInvalidCode, "profile chunk heading is invalid")
		}
	}
	return headings, nil
}

func decodeProfileContent(raw []byte) (domain.ProfileContent, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var content domain.ProfileContent
	if err := decoder.Decode(&content); err != nil {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "profile content is not strict JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "profile content contains trailing JSON")
	}
	normalized, err := domain.NormalizeProfileContent(content)
	if err != nil {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "profile content is invalid")
	}
	if len(normalized.Topics) == 0 || len(normalized.KnowledgePoints) == 0 {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "ready profile content is incomplete")
	}
	return normalized, nil
}

func validProfileLookup(lookup captureapp.ProfileLookup) bool {
	return validID(lookup.WorkspaceID) && validID(lookup.CaptureID) && validID(lookup.SourceVersionID)
}

func validProfileContract(contract captureapp.ProfileContract) bool {
	return validID(contract.ParseProjectionID) && validID(contract.IndexVersionID) &&
		(contract.ModelSettingsRevision == nil || *contract.ModelSettingsRevision > 0) &&
		contract.PromptVersion != "" && strings.TrimSpace(contract.PromptVersion) == contract.PromptVersion &&
		len(contract.PromptVersion) <= 128 && contract.SchemaVersion == domain.ProfileSchemaVersion
}

func validPrepareProfile(command captureapp.PrepareProfileCommand) bool {
	return validProfileLookup(command.ProfileLookup) && validProfileContract(command.Contract) &&
		validID(command.RequestedProfileID) && validID(command.ProfileAttemptID) && validID(command.WorkflowRunID) &&
		validID(command.NodeRunID) && validID(command.NodeAttemptID) && !command.StartedAt.IsZero()
}

func sameProfileContract(attempt profileAttemptRecord, contract captureapp.ProfileContract) bool {
	return attempt.ParseProjectionID == contract.ParseProjectionID && attempt.IndexVersionID == contract.IndexVersionID &&
		sameNullableRevision(attempt.ModelSettingsRevision, contract.ModelSettingsRevision) &&
		attempt.PromptVersion == contract.PromptVersion && attempt.SchemaVersion == contract.SchemaVersion
}

func sameNullableRevision(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func nullableProfileRevision(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func profileContentEvidence(content domain.ProfileContent) []foundation.ID {
	set := make(map[foundation.ID]struct{})
	for _, candidate := range append(append([]domain.ProfileCandidate(nil), content.Topics...), content.Terms...) {
		for _, id := range candidate.SourceSpanIDs {
			set[id] = struct{}{}
		}
	}
	for _, point := range append(append([]domain.ProfilePoint(nil), content.KnowledgePoints...), content.Examples...) {
		for _, id := range point.SourceSpanIDs {
			set[id] = struct{}{}
		}
	}
	values := make([]foundation.ID, 0, len(set))
	for id := range set {
		values = append(values, id)
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return values
}

func evidenceSpanIDs(values []domain.ProfileEvidence) []foundation.ID {
	result := make([]foundation.ID, len(values))
	for index, value := range values {
		result[index] = value.SourceSpanID
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func sameFoundationIDs(left, right []foundation.ID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameProfileRevision(left, right domain.ProfileRevision) bool {
	return left.ID == right.ID && left.ProfileID == right.ProfileID && left.WorkspaceID == right.WorkspaceID &&
		left.SourceVersionID == right.SourceVersionID && left.ParseProjectionID == right.ParseProjectionID &&
		left.IndexVersionID == right.IndexVersionID && left.ModelRunID == right.ModelRunID &&
		sameNullableRevision(left.ModelSettingsRevision, right.ModelSettingsRevision) &&
		left.PromptVersion == right.PromptVersion && left.SchemaVersion == right.SchemaVersion &&
		left.ContentDigest == right.ContentDigest && reflect.DeepEqual(left.Content, right.Content) &&
		samePersistedTime(left.CreatedAt, right.CreatedAt)
}

func exactFailedProfileReplay(attempt profileAttemptRecord, profile domain.Profile, command captureapp.FailProfileCommand, wantedStatus string) bool {
	return attempt.Status == wantedStatus && attempt.ErrorCode == command.ErrorCode &&
		attempt.Retryable == command.Retryable && attempt.ModelRunID == command.ModelRunID &&
		profile.Status == command.ProfileStatus && profile.ErrorCode == command.ErrorCode &&
		profile.Retryable == command.Retryable && samePersistedTime(profile.UpdatedAt, command.CompletedAt) &&
		profileTerminalAttemptReplay(attempt, command.ExpectedProfileAttemptVersion, command.CompletedAt)
}

func profileTerminalAttemptReplay(attempt profileAttemptRecord, expectedVersion int64, completedAt time.Time) bool {
	return attempt.Version == expectedVersion+1 && attempt.CompletedAt != nil &&
		samePersistedTime(*attempt.CompletedAt, completedAt)
}

func samePersistedTime(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func strictProfileErrorCode(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func nilProfilePort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

var _ captureapp.ProfileGenerationRepository = (*ProfileRepository)(nil)
var _ captureapp.ProfileReader = (*ProfileRepository)(nil)
