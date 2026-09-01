package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GORMProfileRepository persists immutable Profile revisions and finalizes the
// owning Agent Model Run in the same platform transaction scope.
type GORMProfileRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	modelRuns  agentapp.ScopedModelRunFinalizer
}

// NewGORMProfileRepository creates the staged GORM Profile repository from one
// shared platform Pool and the Agent-owned scoped Model Run port.
func NewGORMProfileRepository(
	pool *platformpostgres.Pool,
	modelRuns agentapp.ScopedModelRunFinalizer,
) (*GORMProfileRepository, error) {
	if pool == nil || nilProfilePort(modelRuns) {
		return nil, gormProfileUnavailable(errors.New("profile database and scoped model run finalizer are required"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormProfileUnavailable(errors.New("profile GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormProfileUnavailable(errors.New("profile GORM unit of work is unavailable"))
	}
	repository := &GORMProfileRepository{database: database, unitOfWork: unitOfWork, modelRuns: modelRuns}
	if !validCaptureGORMDatabase(repository.database) || nilProfilePort(repository.unitOfWork) {
		return nil, gormProfileUnavailable(errors.New("profile GORM transaction boundary is unavailable"))
	}
	return repository, nil
}

func (repository *GORMProfileRepository) ready(ctx context.Context) error {
	if repository == nil || !validCaptureGORMDatabase(repository.database) ||
		nilProfilePort(repository.unitOfWork) || nilProfilePort(repository.modelRuns) {
		return gormProfileUnavailable(errors.New("profile GORM repository is unavailable"))
	}
	if ctx == nil {
		return invalid("CAPTURE_CONTEXT_INVALID", "capture profile context is nil")
	}
	return nil
}

func (repository *GORMProfileRepository) within(
	ctx context.Context,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return invalid("CAPTURE_TRANSACTION_INVALID", "capture profile transaction callback is nil")
	}
	return repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return gormProfileUnavailable(errors.New("profile scoped transaction is unavailable"))
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx), scope)
	})
}

// PrepareProfile creates or exactly replays one frozen Profile attempt.
func (repository *GORMProfileRepository) PrepareProfile(
	ctx context.Context,
	command captureapp.PrepareProfileCommand,
) (captureapp.PreparedProfile, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.PreparedProfile{}, err
	}
	if !validPrepareProfile(command) {
		return captureapp.PreparedProfile{}, invalid(profileContextInvalidCode, "profile prepare command is invalid")
	}

	var prepared captureapp.PreparedProfile
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		if err := transaction.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`,
			string(command.WorkspaceID)+"\x1f"+string(command.SourceVersionID)).Error; err != nil {
			return err
		}
		if err := gormVerifyProfileCapture(callbackCtx, transaction, command.ProfileLookup); err != nil {
			return err
		}
		if ready, found, err := gormLookupReadyProfile(callbackCtx, transaction, command.ProfileLookup, command.Contract); err != nil {
			return err
		} else if found {
			prepared = captureapp.PreparedProfile{ProfileID: ready.ProfileID, ReadyRevisionID: ready.RevisionID}
			return nil
		}

		profile, found, err := gormLoadProfileBySource(callbackCtx, transaction, command.WorkspaceID, command.SourceVersionID, true)
		if err != nil {
			return err
		}
		if !found {
			profile = domain.Profile{
				ID: command.RequestedProfileID, WorkspaceID: command.WorkspaceID, CaptureID: command.CaptureID,
				SourceVersionID: command.SourceVersionID, Status: domain.ProfileStatusRunning, Version: 1,
				CreatedAt: command.StartedAt.UTC(), UpdatedAt: command.StartedAt.UTC(),
			}
			if err := profile.Validate(); err != nil {
				return invalid(profileContextInvalidCode, "new profile state is invalid")
			}
			row, err := gormCaptureRawRow(transaction, `INSERT INTO learning.document_knowledge_profile(
				id,workspace_id,capture_id,source_version_id,status,error_code,retryable,version,created_at,updated_at
			) VALUES(?,?,?,?,'RUNNING','',false,1,?,?)
			RETURNING `+profileReturning,
				string(profile.ID), string(profile.WorkspaceID), string(profile.CaptureID), string(profile.SourceVersionID),
				profile.CreatedAt.UTC(), profile.UpdatedAt.UTC())
			if err != nil {
				return err
			}
			profile, err = scanProfile(row)
			if err != nil {
				return err
			}
		} else if profile.CaptureID != command.CaptureID {
			return inconsistent(profileContextInvalidCode, "profile is bound to another capture")
		}

		existingAttempt, attemptFound, err := gormLoadProfileAttemptByNode(
			callbackCtx, transaction, profile.ID, command.NodeAttemptID, true,
		)
		if err != nil {
			return err
		}
		if attemptFound {
			prepared, err = replayPreparedAttempt(existingAttempt, command)
			return err
		}

		row, err := gormCaptureRawRow(transaction, `SELECT count(*)
			FROM learning.document_knowledge_profile_attempt WHERE profile_id=? AND status='RUNNING'`, string(profile.ID))
		if err != nil {
			return err
		}
		var activeCount int
		if err := row.Scan(&activeCount); err != nil {
			return err
		}
		if activeCount != 0 {
			return foundation.NewError(foundation.ErrorVersionConflict, profileAttemptConflictCode, true,
				errors.New("another profile attempt is running"))
		}

		if profile.Status != domain.ProfileStatusRunning || profile.ErrorCode != "" || profile.Retryable {
			row, err = gormCaptureRawRow(transaction, `UPDATE learning.document_knowledge_profile SET
				status='RUNNING',error_code='',retryable=false,version=version+1,updated_at=GREATEST(updated_at,?)
				WHERE id=? AND workspace_id=? AND version=? RETURNING `+profileReturning,
				command.StartedAt.UTC(), string(profile.ID), string(profile.WorkspaceID), profile.Version)
			if err != nil {
				return err
			}
			profile, err = scanProfile(row)
			if err != nil {
				return gormProfileCASFailure(callbackCtx, err, profileVersionConflictCode, "profile changed before attempt preparation")
			}
		}

		row, err = gormCaptureRawRow(transaction, `SELECT COALESCE(max(attempt_number),0)+1
			FROM learning.document_knowledge_profile_attempt WHERE profile_id=?`, string(profile.ID))
		if err != nil {
			return err
		}
		var attemptNumber int
		if err := row.Scan(&attemptNumber); err != nil {
			return err
		}
		row, err = gormCaptureRawRow(transaction, `INSERT INTO learning.document_knowledge_profile_attempt(
			id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,
			model_settings_revision,prompt_version,schema_version,workflow_run_id,node_run_id,node_attempt_id,
			attempt_number,status,error_code,retryable,started_at,version
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'RUNNING','',false,?,1) RETURNING `+profileAttemptReturning,
			string(command.ProfileAttemptID), string(profile.ID), string(command.WorkspaceID), string(command.SourceVersionID),
			string(command.Contract.ParseProjectionID), string(command.Contract.IndexVersionID),
			nullableProfileRevision(command.Contract.ModelSettingsRevision), command.Contract.PromptVersion,
			command.Contract.SchemaVersion, string(command.WorkflowRunID), string(command.NodeRunID),
			string(command.NodeAttemptID), attemptNumber, command.StartedAt.UTC())
		if err != nil {
			return err
		}
		attempt, err := scanProfileAttempt(row)
		if err != nil {
			return err
		}
		prepared = attempt.prepared()
		return nil
	})
	if err != nil {
		return captureapp.PreparedProfile{}, classifyGORMCapture(ctx, err, profilePersistenceFailedCode)
	}
	return prepared, nil
}

// BindProfileModelRun binds a RUNNING Agent Model Run before provider access.
func (repository *GORMProfileRepository) BindProfileModelRun(
	ctx context.Context,
	command captureapp.BindProfileModelRunCommand,
) (captureapp.PreparedProfile, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.PreparedProfile{}, err
	}
	if !validID(command.WorkspaceID) || !validID(command.ProfileID) || !validID(command.ProfileAttemptID) ||
		!validID(command.ModelRunID) || command.ExpectedProfileAttemptVersion < 1 {
		return captureapp.PreparedProfile{}, invalid(profileContextInvalidCode, "profile model run binding is invalid")
	}

	var prepared captureapp.PreparedProfile
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		attempt, found, err := gormLoadProfileAttemptByID(callbackCtx, transaction, command.WorkspaceID, command.ProfileAttemptID, true)
		if err != nil {
			return err
		}
		if !found || attempt.ProfileID != command.ProfileID {
			return foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_ATTEMPT_NOT_FOUND", false, sql.ErrNoRows)
		}
		alreadyBound, err := profileModelRunBindingDecision(attempt, command)
		if err != nil {
			return err
		}
		run, err := repository.modelRuns.GetModelRunScoped(callbackCtx, scope, command.WorkspaceID, command.ModelRunID, true)
		if err != nil {
			return err
		}
		if err := validateRunAttemptBinding(run, attempt); err != nil {
			return err
		}
		if !alreadyBound {
			row, err := gormCaptureRawRow(transaction, `UPDATE learning.document_knowledge_profile_attempt
				SET model_run_id=?,version=version+1
				WHERE id=? AND workspace_id=? AND status='RUNNING' AND version=?
				RETURNING `+profileAttemptReturning,
				string(command.ModelRunID), string(attempt.ID), string(attempt.WorkspaceID), command.ExpectedProfileAttemptVersion)
			if err != nil {
				return err
			}
			attempt, err = scanProfileAttempt(row)
			if err != nil {
				return gormProfileCASFailure(callbackCtx, err, profileVersionConflictCode, "profile attempt changed before model run binding")
			}
		}
		prepared = attempt.prepared()
		return nil
	})
	if err != nil {
		return captureapp.PreparedProfile{}, classifyGORMCapture(ctx, err, profilePersistenceFailedCode)
	}
	return prepared, nil
}

// CompleteProfile atomically appends Revision/Evidence and finalizes its Agent run.
func (repository *GORMProfileRepository) CompleteProfile(
	ctx context.Context,
	command captureapp.CompleteProfileCommand,
) (captureapp.ReadyProfile, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.ReadyProfile{}, err
	}
	evidenceIDs, err := validateCompleteProfile(command)
	if err != nil {
		return captureapp.ReadyProfile{}, err
	}

	var ready captureapp.ReadyProfile
	err = repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		attempt, found, err := gormLoadProfileAttemptByID(callbackCtx, transaction, command.WorkspaceID, command.ProfileAttemptID, true)
		if err != nil {
			return err
		}
		if !found {
			return foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_ATTEMPT_NOT_FOUND", false, sql.ErrNoRows)
		}
		if attempt.Status == profileAttemptSucceeded {
			if err := repository.verifyCompletedReplay(callbackCtx, transaction, scope, attempt, command, evidenceIDs); err != nil {
				return err
			}
			ready = captureapp.ReadyProfile{ProfileID: command.Revision.ProfileID, RevisionID: command.Revision.ID}
			return nil
		}
		if attempt.Status != profileAttemptRunning || attempt.Version != command.ExpectedProfileAttemptVersion ||
			attempt.ModelRunID == "" || attempt.ModelRunID != command.Revision.ModelRunID {
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
				errors.New("profile attempt changed before finalization"))
		}
		if err := validateRevisionAttemptBinding(command.Revision, attempt); err != nil {
			return err
		}
		dependenciesCurrent, err := gormLockProfileDependencyState(callbackCtx, transaction, attempt)
		if err != nil {
			return err
		}
		profile, found, err := gormLoadProfileBySource(callbackCtx, transaction, command.WorkspaceID, attempt.SourceVersionID, true)
		if err != nil {
			return err
		}
		if !found || profile.ID != attempt.ProfileID || profile.Status != domain.ProfileStatusRunning ||
			command.CompletedAt.Before(profile.UpdatedAt) {
			return inconsistent(profileContextInvalidCode, "profile pointer is not running for this attempt")
		}
		record, err := repository.modelRuns.GetModelRunRecordScoped(callbackCtx, scope, command.WorkspaceID, attempt.ModelRunID, true)
		if err != nil {
			return err
		}
		if err := validateSuccessfulRun(record, attempt, command.ExpectedModelRunVersion, command.CompletedAt); err != nil {
			return err
		}
		encoded, err := json.Marshal(command.Revision.Content)
		if err != nil {
			return inconsistent(profileContextInvalidCode, "profile content could not be encoded")
		}
		if err := transaction.Exec(`INSERT INTO learning.document_knowledge_profile_revision(
			id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,
			model_settings_revision,prompt_version,schema_version,content,content_digest,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?::jsonb,?,?)`,
			string(command.Revision.ID), string(command.Revision.ProfileID), string(command.Revision.WorkspaceID),
			string(command.Revision.SourceVersionID), string(command.Revision.ParseProjectionID), string(command.Revision.IndexVersionID),
			string(command.Revision.ModelRunID), nullableProfileRevision(command.Revision.ModelSettingsRevision),
			command.Revision.PromptVersion, command.Revision.SchemaVersion, captureJSONB(encoded), command.Revision.ContentDigest,
			command.Revision.CreatedAt.UTC()).Error; err != nil {
			return err
		}
		spanValues := make([]string, len(evidenceIDs))
		for index, id := range evidenceIDs {
			spanValues[index] = string(id)
		}
		if err := transaction.Exec(`INSERT INTO learning.document_knowledge_profile_evidence(
			revision_id,workspace_id,source_version_id,source_span_id,created_at
		) SELECT ?,?,?,value::uuid,? FROM unnest(?::text[]) AS value`,
			string(command.Revision.ID), string(command.WorkspaceID), string(command.Revision.SourceVersionID),
			command.CompletedAt.UTC(), pq.Array(spanValues)).Error; err != nil {
			return err
		}

		finalProfileStatus := domain.ProfileStatusReady
		if !dependenciesCurrent {
			finalProfileStatus = domain.ProfileStatusStale
		}
		result := transaction.Exec(`UPDATE learning.document_knowledge_profile SET
			current_revision_id=?,status=?,error_code='',retryable=false,version=version+1,updated_at=?
			WHERE id=? AND workspace_id=? AND status='RUNNING' AND version=?`,
			string(command.Revision.ID), string(finalProfileStatus), command.CompletedAt.UTC(),
			string(profile.ID), string(profile.WorkspaceID), profile.Version)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
				errors.New("profile changed before successful finalization"))
		}
		result = transaction.Exec(`UPDATE learning.document_knowledge_profile_attempt SET
			status='SUCCEEDED',error_code='',retryable=false,completed_at=?,version=version+1
			WHERE id=? AND workspace_id=? AND status='RUNNING' AND version=?`,
			command.CompletedAt.UTC(), string(attempt.ID), string(attempt.WorkspaceID), attempt.Version)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
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
		if _, replayed, err := repository.modelRuns.FinalizeModelRunScoped(callbackCtx, scope, agentapp.FinalizeModelRunCommand{
			ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun,
		}); err != nil {
			return err
		} else if replayed {
			return inconsistent(profileContextInvalidCode, "profile model run was terminal before atomic completion")
		}
		ready = captureapp.ReadyProfile{ProfileID: profile.ID, RevisionID: command.Revision.ID}
		return nil
	})
	if err != nil {
		return captureapp.ReadyProfile{}, classifyGORMCapture(ctx, err, profileFinalizationFailedCode)
	}
	return ready, nil
}

// FailProfile atomically terminates Profile/Attempt and an optional Agent run.
func (repository *GORMProfileRepository) FailProfile(ctx context.Context, command captureapp.FailProfileCommand) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if validateFailProfile(command) != nil {
		return invalid(profileContextInvalidCode, "profile failure command is invalid")
	}

	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		attempt, found, err := gormLoadProfileAttemptByID(callbackCtx, transaction, command.WorkspaceID, command.ProfileAttemptID, true)
		if err != nil {
			return err
		}
		if !found || attempt.ProfileID != command.ProfileID {
			return foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_ATTEMPT_NOT_FOUND", false, sql.ErrNoRows)
		}
		wantedAttemptStatus := profileAttemptFailed
		if command.ProfileStatus == domain.ProfileStatusCapabilityUnavailable {
			wantedAttemptStatus = profileAttemptCapabilityUnavailable
		}
		profile, found, err := gormLoadProfileBySource(callbackCtx, transaction, command.WorkspaceID, attempt.SourceVersionID, true)
		if err != nil {
			return err
		}
		if !found || profile.ID != attempt.ProfileID || command.CompletedAt.Before(profile.UpdatedAt) ||
			command.CompletedAt.Before(attempt.StartedAt) {
			return inconsistent(profileContextInvalidCode, "profile failure pointer is missing")
		}
		if attempt.Status != profileAttemptRunning {
			if exactFailedProfileReplay(attempt, profile, command, wantedAttemptStatus) {
				return repository.verifyFailedRunReplay(callbackCtx, scope, attempt, command)
			}
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
				errors.New("profile attempt already has another terminal result"))
		}
		if attempt.Version != command.ExpectedProfileAttemptVersion || attempt.ModelRunID != command.ModelRunID {
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
				errors.New("profile attempt changed before failure finalization"))
		}
		if command.ModelRunID != "" {
			record, err := repository.modelRuns.GetModelRunRecordScoped(callbackCtx, scope, command.WorkspaceID, command.ModelRunID, true)
			if err != nil {
				return err
			}
			if err := validateRunAttemptBinding(record.Run, attempt); err != nil {
				return err
			}
			if record.Run.Status != agentdomain.ModelRunRunning || record.Run.Version != command.ExpectedModelRunVersion {
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
			if _, replayed, err := repository.modelRuns.FinalizeModelRunScoped(callbackCtx, scope, agentapp.FinalizeModelRunCommand{
				ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun,
			}); err != nil {
				return err
			} else if replayed {
				return inconsistent(profileContextInvalidCode, "profile model run was terminal before atomic failure")
			}
		}

		result := transaction.Exec(`UPDATE learning.document_knowledge_profile SET
			status=?,error_code=?,retryable=?,version=version+1,updated_at=?
			WHERE id=? AND workspace_id=? AND version=?`,
			string(command.ProfileStatus), command.ErrorCode, command.Retryable, command.CompletedAt.UTC(),
			string(profile.ID), string(profile.WorkspaceID), profile.Version)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
				errors.New("profile changed before failure finalization"))
		}
		result = transaction.Exec(`UPDATE learning.document_knowledge_profile_attempt SET
			status=?,error_code=?,retryable=?,completed_at=?,version=version+1
			WHERE id=? AND workspace_id=? AND status='RUNNING' AND version=?`,
			wantedAttemptStatus, command.ErrorCode, command.Retryable, command.CompletedAt.UTC(),
			string(attempt.ID), string(attempt.WorkspaceID), attempt.Version)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
				errors.New("profile attempt changed before failure finalization"))
		}
		return nil
	})
	return classifyGORMCapture(ctx, err, profileFinalizationFailedCode)
}

func gormLockProfileDependencyState(ctx context.Context, transaction *gorm.DB, attempt profileAttemptRecord) (bool, error) {
	row, err := gormCaptureRawRow(transaction.WithContext(ctx), `SELECT active_revision FROM ops.model_settings_state
		WHERE singleton=true FOR SHARE`)
	if err != nil {
		return false, err
	}
	var activeSettingsRevision int64
	if err := row.Scan(&activeSettingsRevision); err != nil {
		return false, err
	}
	settingsCurrent := attempt.ModelSettingsRevision == nil && activeSettingsRevision == 0 ||
		attempt.ModelSettingsRevision != nil && *attempt.ModelSettingsRevision == activeSettingsRevision

	row, err = gormCaptureRawRow(transaction, `SELECT id::text FROM retrieval.index_version
		WHERE workspace_id=? AND status='active' FOR SHARE`, string(attempt.WorkspaceID))
	if err != nil {
		return false, err
	}
	var activeIndexID string
	if err := row.Scan(&activeIndexID); err != nil && !gormCaptureNoRows(err) {
		return false, err
	}
	if err := transaction.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`,
		string(attempt.WorkspaceID)+"\x1f"+string(attempt.SourceVersionID)).Error; err != nil {
		return false, err
	}

	row, err = gormCaptureRawRow(transaction, `SELECT manifest_source.parse_projection_id::text
		FROM retrieval.index_version AS index_version
		LEFT JOIN retrieval.index_manifest_source AS manifest_source
		  ON manifest_source.index_version_id=index_version.id
		 AND manifest_source.workspace_id=index_version.workspace_id
		 AND manifest_source.source_version_id=?
		 AND manifest_source.selection_status='included'
		WHERE index_version.workspace_id=? AND index_version.status='active'
		FOR SHARE OF index_version`, string(attempt.SourceVersionID), string(attempt.WorkspaceID))
	if err != nil {
		return false, err
	}
	var activeParseProjection sql.NullString
	if err := row.Scan(&activeParseProjection); err != nil && !gormCaptureNoRows(err) {
		return false, err
	}
	parseProjectionCurrent := !activeParseProjection.Valid || activeParseProjection.String == string(attempt.ParseProjectionID)
	return settingsCurrent && parseProjectionCurrent, nil
}

func (repository *GORMProfileRepository) verifyCompletedReplay(
	ctx context.Context,
	transaction *gorm.DB,
	scope foundation.TransactionScope,
	attempt profileAttemptRecord,
	command captureapp.CompleteProfileCommand,
	evidenceIDs []foundation.ID,
) error {
	if !profileTerminalAttemptReplay(attempt, command.ExpectedProfileAttemptVersion, command.CompletedAt) ||
		attempt.ProfileID != command.Revision.ProfileID || attempt.ModelRunID != command.Revision.ModelRunID ||
		validateRevisionAttemptBinding(command.Revision, attempt) != nil {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("completed profile attempt differs from replay"))
	}
	persisted, found, err := gormLoadProfileRevision(ctx, transaction, command.WorkspaceID, command.Revision.ID)
	if err != nil {
		return err
	}
	if !found || !sameProfileRevision(persisted, command.Revision) {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("persisted profile revision differs from replay"))
	}
	evidence, err := gormLoadProfileEvidence(ctx, transaction, persisted)
	if err != nil {
		return err
	}
	if !sameFoundationIDs(evidenceSpanIDs(evidence), evidenceIDs) {
		return foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("persisted profile evidence differs from replay"))
	}
	record, err := repository.modelRuns.GetModelRunRecordScoped(ctx, scope, command.WorkspaceID, attempt.ModelRunID, true)
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

func (repository *GORMProfileRepository) verifyFailedRunReplay(
	ctx context.Context,
	scope foundation.TransactionScope,
	attempt profileAttemptRecord,
	command captureapp.FailProfileCommand,
) error {
	if command.ModelRunID == "" {
		return nil
	}
	record, err := repository.modelRuns.GetModelRunRecordScoped(ctx, scope, command.WorkspaceID, command.ModelRunID, true)
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

func gormProfileCASFailure(ctx context.Context, err error, code, message string) error {
	if gormCaptureNoRows(err) {
		return foundation.NewError(foundation.ErrorVersionConflict, code, true, errors.New(message))
	}
	return classifyGORMCapture(ctx, err, code)
}

func gormProfileUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_PROFILE_REPOSITORY_UNAVAILABLE", false, cause)
}

var _ captureapp.ProfileGenerationRepository = (*GORMProfileRepository)(nil)
