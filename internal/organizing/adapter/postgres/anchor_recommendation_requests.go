package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ app.AnchorRecommendationService = (*GORMAnchorStore)(nil)
var _ app.AnchorRecommendationExecutionStore = (*GORMAnchorStore)(nil)
var _ app.AnchorRecommendationSourceRequester = (*GORMAnchorStore)(nil)

func (s *GORMAnchorStore) RequestAnchorSourceRecommendation(ctx context.Context, command app.RequestAnchorSourceRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	var result app.AnchorRecommendationRequest
	if app.ValidateAnchorCommand(command.WorkspaceID, command.IdempotencyKey) != nil || !validID(command.AnchorID) || command.ExpectedScopeVersion < 1 || command.Source.Validate() != nil || command.Source.WorkspaceID != command.WorkspaceID || len(command.Evidence) < 1 || len(command.Evidence) > 32 {
		return result, app.AnchorInvalid()
	}
	for _, evidence := range command.Evidence {
		if evidence.Validate() != nil || evidence.Source != command.Source {
			return result, app.AnchorInvalid()
		}
	}
	requestHash, err := anchorRecommendationHash(command)
	if err != nil {
		return result, err
	}
	err = s.within(ctx, true, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "anchor-recommendation:"+string(command.WorkspaceID)+":"+command.IdempotencyKey).Error; err != nil {
			return err
		}
		var existing anchorRecommendationRequestModel
		if err := tx.Where("workspace_id=? AND idempotency_key=?", string(command.WorkspaceID), command.IdempotencyKey).Take(&existing).Error; err == nil {
			if existing.RequestHash != requestHash {
				return app.AnchorConflict()
			}
			var err error
			result, err = existing.anchorRecommendationRequest()
			return err
		} else if !gormNoRows(err) {
			return err
		}
		anchor, err := readAnchor(tx, command.WorkspaceID, command.AnchorID, true)
		if err != nil {
			return err
		}
		if anchor.ScopeVersion != command.ExpectedScopeVersion {
			return app.AnchorConflict()
		}
		if err := s.sources.VerifySynthesisSourcesScoped(ctx, scope, command.WorkspaceID, command.Evidence); err != nil {
			return err
		}
		evidence, err := encodeAnchor(command.Evidence)
		if err != nil {
			return err
		}
		source, err := encodeAnchor(command.Source)
		if err != nil {
			return err
		}
		id, err := foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		anchorID, scopeVersion := string(anchor.ID), anchor.ScopeVersion
		row := anchorRecommendationRequestModel{ID: string(id), WorkspaceID: string(command.WorkspaceID), NoteID: string(anchor.NoteID), AnchorID: &anchorID, BasisRevisionID: string(anchor.BasisRevisionID), ExpectedScopeVersion: &scopeVersion, Kind: domain.AnchorSourceAssociation, Source: &source, Evidence: evidence, RequestHash: requestHash, IdempotencyKey: command.IdempotencyKey, Status: string(domain.AnchorRecommendationPending), Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		result, err = row.anchorRecommendationRequest()
		return err
	})
	return result, err
}

func (s *GORMAnchorStore) RequestInitialAnchorRecommendation(ctx context.Context, command app.RequestInitialAnchorRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	var result app.AnchorRecommendationRequest
	if err := command.Validate(); err != nil {
		return result, err
	}
	requestHash, err := anchorRecommendationHash(command)
	if err != nil {
		return result, err
	}
	err = s.within(ctx, true, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "anchor-recommendation:"+string(command.WorkspaceID)+":"+command.IdempotencyKey).Error; err != nil {
			return err
		}
		var existing anchorRecommendationRequestModel
		err := tx.Where("workspace_id=? AND idempotency_key=?", string(command.WorkspaceID), command.IdempotencyKey).Take(&existing).Error
		if err == nil {
			if existing.RequestHash != requestHash {
				return app.AnchorConflict()
			}
			request, err := existing.anchorRecommendationRequest()
			result = request
			return err
		}
		if !gormNoRows(err) {
			return err
		}
		note, err := loadSynthesisNote(tx, command.WorkspaceID, command.NoteID, true)
		if err != nil {
			return err
		}
		if note.Version != command.ExpectedNoteVersion || note.CurrentRevisionID != command.BasisRevisionID {
			return app.AnchorConflict()
		}
		revision, err := loadSynthesisRevision(tx, command.WorkspaceID, command.NoteID, command.BasisRevisionID)
		if err != nil {
			return err
		}
		evidence := anchorRecommendationEvidence(revision)
		if len(evidence) == 0 {
			return app.AnchorInvalid()
		}
		id, err := foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		encoded, err := encodeAnchor(evidence)
		if err != nil {
			return err
		}
		row := anchorRecommendationRequestModel{ID: string(id), WorkspaceID: string(command.WorkspaceID), NoteID: string(command.NoteID), BasisRevisionID: string(command.BasisRevisionID), Kind: app.AnchorInitialScopeRecommendation, Evidence: encoded, RequestHash: requestHash, IdempotencyKey: command.IdempotencyKey, Status: string(domain.AnchorRecommendationPending), Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		result, err = row.anchorRecommendationRequest()
		return err
	})
	return result, err
}

func (s *GORMAnchorStore) GetAnchorRecommendation(ctx context.Context, workspaceID, requestID foundation.ID) (app.AnchorRecommendationRequest, error) {
	var result app.AnchorRecommendationRequest
	if !validID(workspaceID) || !validID(requestID) {
		return result, app.AnchorInvalid()
	}
	err := s.within(ctx, false, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorRecommendationRequestModel
		if err := tx.Where("workspace_id=? AND id=?", string(workspaceID), string(requestID)).Take(&row).Error; err != nil {
			if gormNoRows(err) {
				return anchorNotFound()
			}
			return err
		}
		var err error
		result, err = row.anchorRecommendationRequest()
		return err
	})
	return result, err
}

func (s *GORMAnchorStore) ListAnchorRecommendations(ctx context.Context, query app.AnchorRecommendationListQuery) (app.AnchorRecommendationPage, error) {
	var result app.AnchorRecommendationPage
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 || query.Status != "" && !query.Status.Valid() {
		return result, app.AnchorInvalid()
	}
	err := s.within(ctx, false, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		q := tx.Where("workspace_id=?", string(query.WorkspaceID)).Order("id").Limit(query.Limit + 1)
		if query.NoteID != "" {
			q = q.Where("note_id=?", string(query.NoteID))
		}
		if query.AnchorID != "" {
			q = q.Where("anchor_id=?", string(query.AnchorID))
		}
		if query.Status != "" {
			q = q.Where("status=?", string(query.Status))
		}
		if query.AfterID != "" {
			q = q.Where("id>?", string(query.AfterID))
		}
		var rows []anchorRecommendationRequestModel
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > query.Limit {
			next := foundation.ID(rows[query.Limit-1].ID)
			result.NextAfterID = &next
			rows = rows[:query.Limit]
		}
		result.Items = make([]app.AnchorRecommendationRequest, 0, len(rows))
		for _, row := range rows {
			item, err := row.anchorRecommendationRequest()
			if err != nil {
				return err
			}
			result.Items = append(result.Items, item)
		}
		return nil
	})
	return result, err
}

func (s *GORMAnchorStore) RetryAnchorRecommendation(ctx context.Context, command app.RetryAnchorRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	var result app.AnchorRecommendationRequest
	if err := command.Validate(); err != nil {
		return result, err
	}
	retryHash, err := anchorRecommendationHash(command)
	if err != nil {
		return result, err
	}
	err = s.within(ctx, true, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorRecommendationRequestModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(command.WorkspaceID), string(command.RequestID)).Take(&row).Error; err != nil {
			if gormNoRows(err) {
				return anchorNotFound()
			}
			return err
		}
		if row.RetryIdempotencyKey != nil && row.RetryRequestHash != nil && *row.RetryIdempotencyKey == command.IdempotencyKey {
			if *row.RetryRequestHash != retryHash {
				return app.AnchorConflict()
			}
			result, err = row.anchorRecommendationRequest()
			return err
		}
		if row.Status != string(domain.AnchorRecommendationFailed) || !row.Retryable || row.Version != command.ExpectedVersion {
			return app.AnchorConflict()
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		updated := tx.Model(&anchorRecommendationRequestModel{}).Where("workspace_id=? AND id=? AND version=?", row.WorkspaceID, row.ID, row.Version).Updates(map[string]any{"status": string(domain.AnchorRecommendationPending), "workflow_run_id": nil, "node_run_id": nil, "node_attempt_id": nil, "model_input_hash": nil, "model_output": nil, "model_run_id": nil, "error_code": nil, "retryable": false, "retry_idempotency_key": command.IdempotencyKey, "retry_request_hash": retryHash, "version": row.Version + 1, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		row.Status, row.WorkflowRunID, row.NodeRunID, row.NodeAttemptID, row.ModelInputHash, row.ModelOutput, row.ModelRunID, row.ErrorCode, row.Retryable, row.RetryIdempotencyKey, row.RetryRequestHash, row.Version, row.UpdatedAt = string(domain.AnchorRecommendationPending), nil, nil, nil, nil, nil, nil, nil, false, stringPointer(command.IdempotencyKey), stringPointer(retryHash), row.Version+1, now
		var err error
		result, err = row.anchorRecommendationRequest()
		return err
	})
	return result, err
}

func (s *GORMAnchorStore) ClaimAnchorRecommendation(ctx context.Context, command app.ClaimAnchorRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	var result app.AnchorRecommendationRequest
	if !validID(command.WorkspaceID) || !validID(command.RequestID) || command.ExpectedVersion < 1 || !validID(command.WorkflowRunID) || !validID(command.NodeRunID) || !validID(command.NodeAttemptID) || !validHash(command.ModelInputHash) || command.WorkflowRunID == command.NodeRunID || command.WorkflowRunID == command.NodeAttemptID || command.NodeRunID == command.NodeAttemptID {
		return result, app.AnchorInvalid()
	}
	err := s.within(ctx, true, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorRecommendationRequestModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(command.WorkspaceID), string(command.RequestID)).Take(&row).Error; err != nil {
			if gormNoRows(err) {
				return anchorNotFound()
			}
			return err
		}
		if row.Status == string(domain.AnchorRecommendationRunning) && row.WorkflowRunID != nil && row.NodeRunID != nil && row.NodeAttemptID != nil && row.ModelInputHash != nil && *row.WorkflowRunID == string(command.WorkflowRunID) && *row.NodeRunID == string(command.NodeRunID) && *row.NodeAttemptID == string(command.NodeAttemptID) && *row.ModelInputHash == command.ModelInputHash {
			var err error
			result, err = row.anchorRecommendationRequest()
			return err
		}
		if row.Status != string(domain.AnchorRecommendationPending) || row.Version != command.ExpectedVersion {
			return app.AnchorConflict()
		}
		var bound int64
		if err := tx.Model(&anchorRecommendationRequestModel{}).Where("workspace_id=? AND node_attempt_id=? AND id<>?", string(command.WorkspaceID), string(command.NodeAttemptID), string(command.RequestID)).Count(&bound).Error; err != nil {
			return err
		}
		if bound != 0 {
			return app.AnchorConflict()
		}
		var existingRun int64
		if err := tx.Table("agent.model_run").Where("workspace_id=? AND node_attempt_id=?", string(command.WorkspaceID), string(command.NodeAttemptID)).Count(&existingRun).Error; err != nil {
			return err
		}
		if existingRun != 0 {
			return app.AnchorConflict()
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		updated := tx.Model(&anchorRecommendationRequestModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", row.WorkspaceID, row.ID, row.Version, string(domain.AnchorRecommendationPending)).Updates(map[string]any{"status": string(domain.AnchorRecommendationRunning), "workflow_run_id": string(command.WorkflowRunID), "node_run_id": string(command.NodeRunID), "node_attempt_id": string(command.NodeAttemptID), "model_input_hash": command.ModelInputHash, "version": row.Version + 1, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		workflowRunID, nodeRunID, nodeAttemptID := string(command.WorkflowRunID), string(command.NodeRunID), string(command.NodeAttemptID)
		inputHash := command.ModelInputHash
		row.Status, row.WorkflowRunID, row.NodeRunID, row.NodeAttemptID, row.ModelInputHash, row.Version, row.UpdatedAt = string(domain.AnchorRecommendationRunning), &workflowRunID, &nodeRunID, &nodeAttemptID, &inputHash, row.Version+1, now
		var err error
		result, err = row.anchorRecommendationRequest()
		return err
	})
	return result, err
}

func (s *GORMAnchorStore) FailAnchorRecommendation(ctx context.Context, command app.FailAnchorRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	var result app.AnchorRecommendationRequest
	if !validID(command.WorkspaceID) || !validID(command.RequestID) || command.ExpectedVersion < 1 || (command.Status != domain.AnchorRecommendationFailed && command.Status != domain.AnchorRecommendationRecoveryRequired) || command.ErrorCode == "" || command.ErrorCode != strings.TrimSpace(command.ErrorCode) || len(command.ErrorCode) > 128 || (command.Status == domain.AnchorRecommendationRecoveryRequired && command.Retryable) {
		return result, app.AnchorInvalid()
	}
	err := s.within(ctx, true, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadAnchorRecommendationRequest(tx, command.WorkspaceID, command.RequestID, true)
		if err != nil {
			return err
		}
		if row.Status != string(domain.AnchorRecommendationRunning) || row.Version != command.ExpectedVersion {
			return app.AnchorConflict()
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		updated := tx.Model(&anchorRecommendationRequestModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", row.WorkspaceID, row.ID, row.Version, string(domain.AnchorRecommendationRunning)).Updates(map[string]any{"status": string(command.Status), "error_code": command.ErrorCode, "retryable": command.Retryable, "version": row.Version + 1, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		row.Status, row.ErrorCode, row.Retryable, row.Version, row.UpdatedAt = string(command.Status), &command.ErrorCode, command.Retryable, row.Version+1, now
		result, err = row.anchorRecommendationRequest()
		return err
	})
	return result, err
}

func (s *GORMAnchorStore) CompleteAnchorRecommendation(ctx context.Context, command app.CompleteAnchorRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	var result app.AnchorRecommendationRequest
	if !validID(command.WorkspaceID) || !validID(command.RequestID) || command.ExpectedVersion < 1 || !validID(command.ModelRunID) || !validHash(command.ModelOutputHash) || len(command.ModelOutput) == 0 {
		return result, app.AnchorInvalid()
	}
	verifier, ok := s.verifier.(*AnchorRecommendationModelVerifier)
	if !ok || verifier == nil {
		return result, synthesisUnavailable(nil)
	}
	err := s.within(ctx, true, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadAnchorRecommendationRequest(tx, command.WorkspaceID, command.RequestID, true)
		if err != nil {
			return err
		}
		if row.Status != string(domain.AnchorRecommendationRunning) || row.Version != command.ExpectedVersion {
			return app.AnchorConflict()
		}
		if err := s.verifyLiveAnchorRecommendation(ctx, scope, tx, row); err != nil {
			return err
		}
		if err := verifier.VerifyAnchorRecommendationCompletionScoped(ctx, scope, row, command); err != nil {
			return err
		}
		output, err := app.DecodeAnchorModelOutput(command.ModelOutput)
		if err != nil {
			return app.AnchorConflict()
		}
		status := domain.AnchorRecommendationNoRecommendation
		if !output.NoRecommendation {
			if row.Kind != app.AnchorInitialScopeRecommendation {
				return app.AnchorConflict()
			}
			status = domain.AnchorRecommendationSucceeded
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		raw := append([]byte(nil), command.ModelOutput...)
		updated := tx.Model(&anchorRecommendationRequestModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", row.WorkspaceID, row.ID, row.Version, string(domain.AnchorRecommendationRunning)).Updates(map[string]any{"status": string(status), "model_run_id": string(command.ModelRunID), "model_output": raw, "version": row.Version + 1, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		row.Status, row.ModelRunID, row.ModelOutput, row.Version, row.UpdatedAt = string(status), stringPointer(string(command.ModelRunID)), &raw, row.Version+1, now
		result, err = row.anchorRecommendationRequest()
		return err
	})
	return result, err
}

func stringPointer(value string) *string { return &value }

func anchorRecommendationHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func anchorRecommendationEvidence(revision domain.SynthesisRevision) []domain.SynthesisSourceRef {
	result := make([]domain.SynthesisSourceRef, 0)
	seen := make(map[string]bool)
	for _, item := range revision.Items {
		for _, ref := range item.SourceReferences() {
			key, err := ref.IdentityKey()
			if err != nil || seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, ref)
			if len(result) == 32 {
				return result
			}
		}
	}
	return result
}

func (row anchorRecommendationRequestModel) anchorRecommendationRequest() (app.AnchorRecommendationRequest, error) {
	result := app.AnchorRecommendationRequest{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), NoteID: foundation.ID(row.NoteID), BasisRevisionID: foundation.ID(row.BasisRevisionID), Kind: row.Kind, Status: domain.AnchorRecommendationStatus(row.Status), ErrorCode: stringValue(row.ErrorCode), Retryable: row.Retryable, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.AnchorID != nil {
		result.AnchorID = foundation.ID(*row.AnchorID)
	}
	if row.ExpectedScopeVersion != nil {
		result.ExpectedScopeVersion = *row.ExpectedScopeVersion
	}
	if row.ModelRunID != nil {
		result.ModelRunID = foundation.ID(*row.ModelRunID)
	}
	if row.ModelInputHash != nil {
		result.ModelInputHash = *row.ModelInputHash
	}
	if row.ModelOutput != nil {
		result.ModelOutput = append(json.RawMessage(nil), (*row.ModelOutput)...)
	}
	if row.ProposalID != nil {
		result.ProposalID = foundation.ID(*row.ProposalID)
	}
	if row.Source != nil {
		var source domain.SynthesisSourceVersion
		if err := json.Unmarshal(*row.Source, &source); err != nil || source.Validate() != nil || source.WorkspaceID != result.WorkspaceID {
			return result, app.AnchorConflict()
		}
		result.Source = &source
	}
	if err := json.Unmarshal(row.Evidence, &result.Evidence); err != nil {
		return result, err
	}
	if !result.Status.Valid() || len(result.Evidence) < 1 || len(result.Evidence) > 32 {
		return result, app.AnchorConflict()
	}
	return result, nil
}
