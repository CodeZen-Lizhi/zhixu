// Package postgres persists Change Control aggregates in PostgreSQL.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontroleventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Repository 所需的最小 pgx 边界。
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository 是 Change Control 的 PostgreSQL 实现。
type Repository struct {
	db     DB
	events eventsapplication.Appender
}

var _ domain.Repository = (*Repository)(nil)
var _ domain.KnowledgeChangeProposalRepository = (*Repository)(nil)
var _ domain.PublishArtifactProposalRepository = (*Repository)(nil)
var _ domain.DownstreamUpdateProposalRepository = (*Repository)(nil)
var _ domain.RestoreDocumentProposalRepository = (*Repository)(nil)
var _ domain.AuthorizationRepository = (*Repository)(nil)

// NewRepository 创建 PostgreSQL Repository；可选事件追加器用于在同一事务发布 Proposal 状态通知。
func NewRepository(db DB, appenders ...eventsapplication.Appender) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_DATABASE_UNAVAILABLE", true, errors.New("database is nil"))
	}
	if len(appenders) > 1 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "CHANGE_CONTROL_EVENT_APPENDER_INVALID", false, errors.New("only one change control event appender may be configured"))
	}
	var events eventsapplication.Appender
	if len(appenders) == 1 && !isNilChangeControlDependency(appenders[0]) {
		events = appenders[0]
	}
	return &Repository{db: db, events: events}, nil
}

// CreateProposal 在同一事务中创建 Proposal 和不可变 Revision。
func (r *Repository) CreateProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	switch domain.NormalizeProposalType(proposal.Type) {
	case domain.ProposalTypeKnowledgeChange:
		return r.CreateKnowledgeChangeProposal(ctx, proposal)
	case domain.ProposalTypePublishArtifact:
		return r.CreatePublishArtifactProposal(ctx, proposal)
	case domain.ProposalTypeDownstreamUpdate:
		return r.CreateDownstreamUpdateProposal(ctx, proposal)
	case domain.ProposalTypeFilePatch:
		proposal.Type = domain.ProposalTypeFilePatch
	default:
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrProposalTypeInvalid)
	}
	riskLevel, riskErr := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if riskErr != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, riskErr)
	}
	proposal.RiskLevel = riskLevel
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, proposal.Revision); err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	if snapshot := proposal.Revision.BaseSnapshot; snapshot != nil {
		if domain.NormalizeTargetMode(proposal.Revision.TargetMode) != domain.TargetModeReplace ||
			snapshot.ProposalID != proposal.ID || snapshot.RevisionID != proposal.Revision.ID || snapshot.BaseHash != proposal.Revision.BaseHash ||
			domain.ValidateRevisionBaseSnapshot(*snapshot) != nil {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("proposal revision base snapshot is invalid"))
		}
	}
	targetMode := domain.NormalizeTargetMode(proposal.Revision.TargetMode)
	expectedRequestHash, err := domain.ComputeRequestHashWithTargetMode(
		proposal.WorkspaceID,
		proposal.Revision.TargetPath,
		targetMode,
		proposal.Revision.BaseHash,
		proposal.Revision.Content,
		proposal.Revision.EvidenceSummary,
		proposal.RiskLevel,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	legacyRequestHash := ""
	if targetMode == domain.TargetModeReplace {
		legacyRequestHash = domain.ComputeRequestHash(
			proposal.WorkspaceID,
			proposal.Revision.TargetPath,
			proposal.Revision.BaseHash,
			proposal.Revision.Content,
			proposal.Revision.EvidenceSummary,
			proposal.Revision.Risk,
			proposal.Revision.RollbackPlan,
		)
	}
	legacyRequest := targetMode == domain.TargetModeReplace && proposal.RequestHash == legacyRequestHash
	if proposal.RequestHash != expectedRequestHash && !legacyRequest {
		return r.resolveInvalidRequestHash(ctx, proposal, "file patch request hash mismatch")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if legacyRequest {
		existingID, storedType, storedRequestHash, storedRiskLevel, loadErr := loadProposalCreateBinding(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("legacy file patch request hash is only valid for persisted replay"))
		}
		if loadErr != nil {
			return domain.Proposal{}, classify(loadErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if storedType != proposal.Type || storedRiskLevel != proposal.RiskLevel || !proposalRequestHashReplayMatches(storedRequestHash, proposal.RequestHash, expectedRequestHash, legacyRequestHash) {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, existingID)
	}
	var insertedID string
	err = tx.QueryRow(ctx, `
			INSERT INTO change_control.proposal(id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at,current_revision_id)
			VALUES($1,$2,'file_patch',$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
			RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), string(proposal.RiskLevel), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC(), string(proposal.Revision.ID)).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		existingID, storedType, requestHash, storedRiskLevel, queryErr := loadProposalCreateBinding(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
		if queryErr != nil {
			return domain.Proposal{}, classify(queryErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if storedType != proposal.Type || storedRiskLevel != proposal.RiskLevel || !proposalRequestHashReplayMatches(requestHash, proposal.RequestHash, expectedRequestHash, legacyRequestHash) {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, foundation.ID(existingID))
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_CREATE_FAILED")
	}
	revision := proposal.Revision
	if _, err := tx.Exec(ctx, `
			INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.TargetPath,
		string(domain.NormalizeTargetMode(revision.TargetMode)), revision.BaseHash, revision.Content, revision.EvidenceSummary, revision.Risk,
		revision.RollbackPlan, revision.ChangeHash, revision.CreatedAt.UTC()); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if snapshot := revision.BaseSnapshot; snapshot != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO change_control.proposal_revision_base_snapshot(
				proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			string(snapshot.ProposalID), string(snapshot.RevisionID), snapshot.BaseHash, snapshot.Content,
			snapshot.ByteSize, snapshot.SchemaVersion, snapshot.CreatedAt.UTC()); err != nil {
			return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_SNAPSHOT_CREATE_FAILED")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_COMMIT_FAILED")
	}
	proposal.CurrentRevisionID = proposal.Revision.ID
	return proposal, nil
}

// CreateKnowledgeChangeProposal 在同一事务中创建 typed knowledge_change Proposal 和不可变 Revision。
func (r *Repository) CreateKnowledgeChangeProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypeKnowledgeChange
	riskLevel, riskErr := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if riskErr != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, riskErr)
	}
	proposal.RiskLevel = riskLevel
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeKnowledgeChange, proposal.Revision); err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	if proposal.Revision.KnowledgeChange == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("knowledge change revision payload is required"))
	}
	expectedRequestHash, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel(proposal.WorkspaceID, *proposal.Revision.KnowledgeChange, proposal.RiskLevel, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	legacyRequestHash, err := domain.ComputeKnowledgeChangeRequestHash(proposal.WorkspaceID, *proposal.Revision.KnowledgeChange, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	legacyRequest := proposal.RequestHash == legacyRequestHash
	if proposal.RequestHash != expectedRequestHash && !legacyRequest {
		return r.resolveInvalidRequestHash(ctx, proposal, "knowledge change request hash mismatch")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if legacyRequest {
		existingID, storedType, storedRequestHash, storedRiskLevel, loadErr := loadProposalCreateBinding(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("legacy knowledge change request hash is only valid for persisted replay"))
		}
		if loadErr != nil {
			return domain.Proposal{}, classify(loadErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if storedType != proposal.Type || storedRiskLevel != proposal.RiskLevel || !proposalRequestHashReplayMatches(storedRequestHash, proposal.RequestHash, expectedRequestHash, legacyRequestHash) {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, existingID)
	}
	var insertedID string
	err = tx.QueryRow(ctx, `
			INSERT INTO change_control.proposal(id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at,current_revision_id)
			VALUES($1,$2,'knowledge_change',$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
			RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), string(proposal.RiskLevel), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC(), string(proposal.Revision.ID)).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		existingID, storedType, requestHash, storedRiskLevel, queryErr := loadProposalCreateBinding(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
		if queryErr != nil {
			return domain.Proposal{}, classify(queryErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if storedType != proposal.Type || storedRiskLevel != proposal.RiskLevel || !proposalRequestHashReplayMatches(requestHash, proposal.RequestHash, expectedRequestHash, legacyRequestHash) {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, foundation.ID(existingID))
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_CREATE_FAILED")
	}
	targetRefs, err := json.Marshal(proposal.Revision.KnowledgeChange.TargetRefs)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	baseVersions, err := json.Marshal(proposal.Revision.KnowledgeChange.BaseVersions)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	changeSet, err := json.Marshal(proposal.Revision.KnowledgeChange.ChangeSet)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	evidenceRefs, err := json.Marshal(proposal.Revision.KnowledgeChange.EvidenceRefs)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	revision := proposal.Revision
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
		) VALUES($1,$2,$3,NULL,NULL,NULL,NULL,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.Risk, revision.RollbackPlan, revision.ChangeHash,
		targetRefs, baseVersions, changeSet, evidenceRefs, revision.KnowledgeChange.SchemaVersion, revision.CreatedAt.UTC()); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_COMMIT_FAILED")
	}
	proposal.CurrentRevisionID = proposal.Revision.ID
	return proposal, nil
}

// CreatePublishArtifactProposal persists only a frozen, reviewable Artifact publication request.
// It deliberately creates neither a formal Document nor a Safe Writeback execution.
func (r *Repository) CreatePublishArtifactProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypePublishArtifact
	riskLevel, riskErr := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if riskErr != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, riskErr)
	}
	proposal.RiskLevel = riskLevel
	if err := domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil || proposal.Revision.PublishArtifact == nil {
		if err == nil {
			err = errors.New("publish artifact revision payload is required")
		}
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	publication, err := domain.ValidatePublishArtifact(*proposal.Revision.PublishArtifact)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	proposal.Revision.PublishArtifact = &publication
	expectedRequestHash, err := domain.ComputePublishArtifactRequestHash(proposal.WorkspaceID, publication, proposal.RiskLevel, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	if proposal.RequestHash != expectedRequestHash {
		return r.resolveInvalidRequestHash(ctx, proposal, "publish artifact request hash mismatch")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var insertedID string
	err = tx.QueryRow(ctx, `
			INSERT INTO change_control.proposal(id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at,current_revision_id)
			VALUES($1,$2,'publish_artifact',$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
			RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), string(proposal.RiskLevel), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC(), string(proposal.Revision.ID)).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		existingID, storedType, requestHash, storedRiskLevel, queryErr := loadProposalCreateBinding(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
		if queryErr != nil {
			return domain.Proposal{}, classify(queryErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if storedType != proposal.Type || storedRiskLevel != proposal.RiskLevel || requestHash != expectedRequestHash {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, existingID)
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_CREATE_FAILED")
	}
	coverage, err := json.Marshal(publication.SourceCoverage)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	revision := proposal.Revision
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			artifact_id,artifact_revision_id,artifact_revision_no,artifact_version,artifact_content_hash,artifact_source_coverage,schema_version,created_at
		) VALUES($1,$2,$3,NULL,NULL,NULL,NULL,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.Risk, revision.RollbackPlan, revision.ChangeHash,
		string(publication.ArtifactID), string(publication.RevisionID), publication.RevisionNo, publication.ArtifactVersion, publication.ContentHash, coverage, publication.SchemaVersion, revision.CreatedAt.UTC()); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_COMMIT_FAILED")
	}
	proposal.CurrentRevisionID = proposal.Revision.ID
	return proposal, nil
}

// CreateDownstreamUpdateProposal 持久化一个只可审批、不可执行的下游更新意图。
func (r *Repository) CreateDownstreamUpdateProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypeDownstreamUpdate
	riskLevel, riskErr := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if riskErr != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, riskErr)
	}
	proposal.RiskLevel = riskLevel
	if err := domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil || proposal.Revision.DownstreamUpdate == nil {
		if err == nil {
			err = errors.New("downstream update revision payload is required")
		}
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	update, err := domain.ValidateDownstreamUpdate(*proposal.Revision.DownstreamUpdate)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	proposal.Revision.DownstreamUpdate = &update
	expectedRequestHash, err := domain.ComputeDownstreamUpdateRequestHash(
		proposal.WorkspaceID, update, proposal.RiskLevel, proposal.Revision.Risk, proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	if proposal.RequestHash != expectedRequestHash {
		return r.resolveInvalidRequestHash(ctx, proposal, "downstream update request hash mismatch")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var insertedID string
	err = tx.QueryRow(ctx, `
			INSERT INTO change_control.proposal(id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at,current_revision_id)
			VALUES($1,$2,'downstream_update',$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
			RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), string(proposal.RiskLevel), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC(), string(proposal.Revision.ID)).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		existingID, storedType, requestHash, storedRiskLevel, queryErr := loadProposalCreateBinding(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
		if queryErr != nil {
			return domain.Proposal{}, classify(queryErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if storedType != proposal.Type || storedRiskLevel != proposal.RiskLevel || requestHash != expectedRequestHash {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, existingID)
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_CREATE_FAILED")
	}
	currentUpdate, err := r.buildDownstreamUpdate(
		ctx, tx, update.WorkspaceID, update.ReportID, update.TargetType, update.TargetID, update.Action, true,
	)
	if err != nil {
		return domain.Proposal{}, err
	}
	if !reflect.DeepEqual(currentUpdate, update) {
		return domain.Proposal{}, downstreamImpactConflict("downstream update owner binding changed before create")
	}
	ownerBinding, err := json.Marshal(update.OwnerBinding)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	revision := proposal.Revision
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			downstream_workspace_id,downstream_report_id,downstream_analysis_version,downstream_report_fingerprint,
			downstream_source_event_id,downstream_source_event_version,downstream_target_type,downstream_target_id,
			downstream_base_version,downstream_action,downstream_owner_binding,downstream_reason,schema_version,created_at
		) VALUES($1,$2,$3,NULL,NULL,NULL,NULL,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.Risk, revision.RollbackPlan, revision.ChangeHash,
		string(update.WorkspaceID), string(update.ReportID), string(update.AnalysisVersion), update.ReportFingerprint,
		string(update.SourceEventID), update.SourceEventVersion, string(update.TargetType), string(update.TargetID),
		update.BaseVersion, string(update.Action), ownerBinding, update.Reason, update.SchemaVersion, revision.CreatedAt.UTC()); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_COMMIT_FAILED")
	}
	proposal.CurrentRevisionID = proposal.Revision.ID
	return proposal, nil
}

// CreateRestoreDocumentProposal persists a typed restore after locking its exact published Document binding.
func (r *Repository) CreateRestoreDocumentProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypeRestoreDocument
	if proposal.Revision.RestoreDocument == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrRestoreDocumentInvalid)
	}
	restore, err := domain.ValidateRestoreDocument(*proposal.Revision.RestoreDocument)
	if err != nil || restore.WorkspaceID != proposal.WorkspaceID || proposal.TargetPath != proposal.Revision.TargetPath {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrRestoreDocumentInvalid)
	}
	proposal.Revision.RestoreDocument = &restore
	riskLevel, err := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	proposal.RiskLevel = riskLevel
	if err := domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	expectedRequestHash, err := domain.ComputeRestoreDocumentRequestHash(
		proposal.WorkspaceID, restore, proposal.Revision.TargetPath, proposal.Revision.Content,
		proposal.Revision.EvidenceSummary, proposal.RiskLevel, proposal.Revision.Risk, proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	if proposal.RequestHash != expectedRequestHash {
		return r.resolveInvalidRequestHash(ctx, proposal, "restore document request hash mismatch")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO change_control.proposal(id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at,current_revision_id)
		VALUES($1,$2,'restore_document',$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), string(proposal.RiskLevel), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC(), string(proposal.Revision.ID)).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		existingID, storedType, requestHash, storedRiskLevel, queryErr := loadProposalCreateBinding(ctx, tx, proposal.WorkspaceID, proposal.IdempotencyKey)
		if queryErr != nil {
			return domain.Proposal{}, classify(queryErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if storedType != proposal.Type || storedRiskLevel != proposal.RiskLevel || requestHash != expectedRequestHash {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, existingID)
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_CREATE_FAILED")
	}
	var canonicalPath, lifecycle, currentRevisionID string
	var documentVersion int64
	err = tx.QueryRow(ctx, `SELECT canonical_path,lifecycle_status,COALESCE(current_published_revision_id::text,''),version
		FROM core.document WHERE workspace_id=$1 AND id=$2 FOR SHARE`, string(proposal.WorkspaceID), string(restore.DocumentID)).Scan(
		&canonicalPath, &lifecycle, &currentRevisionID, &documentVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "DOCUMENT_RESTORE_BINDING_QUERY_FAILED")
	}
	if lifecycle != "PUBLISHED" || currentRevisionID == "" || canonicalPath != proposal.Revision.TargetPath || documentVersion != restore.ExpectedDocumentVersion {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("published document binding changed before proposal creation"))
	}
	revision := proposal.Revision
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
		restore_workspace_id,restore_document_id,restore_target_commit,restore_expected_head,restore_expected_document_version,
		restore_preview_hash,restore_current_content_hash,restore_target_content_hash,schema_version,created_at
	) VALUES($1,$2,$3,$4,'REPLACE',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.TargetPath, revision.BaseHash,
		revision.Content, revision.EvidenceSummary, revision.Risk, revision.RollbackPlan, revision.ChangeHash,
		string(restore.WorkspaceID), string(restore.DocumentID), restore.TargetCommit, restore.ExpectedHead,
		restore.ExpectedDocumentVersion, restore.PreviewHash, restore.CurrentContentHash, restore.TargetContentHash,
		restore.SchemaVersion, revision.CreatedAt.UTC()); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_COMMIT_FAILED")
	}
	proposal.CurrentRevisionID = proposal.Revision.ID
	return proposal, nil
}

// FindProposalByIdempotencyKey 返回 Workspace 内已绑定创建键的完整 Proposal。
func (r *Repository) FindProposalByIdempotencyKey(ctx context.Context, workspaceID foundation.ID, idempotencyKey string) (domain.Proposal, bool, error) {
	var proposalID string
	err := r.db.QueryRow(ctx, `
		SELECT id::text
		FROM change_control.proposal
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), idempotencyKey).Scan(&proposalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Proposal{}, false, nil
	}
	if err != nil {
		return domain.Proposal{}, false, classify(err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	proposal, err := r.GetProposal(ctx, foundation.ID(proposalID))
	if err != nil {
		return domain.Proposal{}, false, err
	}
	return proposal, true, nil
}

// BuildDownstreamUpdate 从当前 Impact report 与 owner 事实重建下游更新载荷。
func (r *Repository) BuildDownstreamUpdate(
	ctx context.Context,
	workspaceID, reportID foundation.ID,
	targetType knowledge.ImpactObjectType,
	targetID foundation.ID,
	action knowledge.ImpactAction,
) (domain.DownstreamUpdate, error) {
	return r.buildDownstreamUpdate(ctx, r.db, workspaceID, reportID, targetType, targetID, action, false)
}

type downstreamUpdateQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Repository) buildDownstreamUpdate(
	ctx context.Context,
	queryer downstreamUpdateQueryer,
	workspaceID, reportID foundation.ID,
	targetType knowledge.ImpactObjectType,
	targetID foundation.ID,
	action knowledge.ImpactAction,
	lockOwner bool,
) (domain.DownstreamUpdate, error) {
	if targetType != knowledge.ImpactObjectArtifact && targetType != knowledge.ImpactObjectReviewCard {
		return domain.DownstreamUpdate{}, foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_TARGET_INVALID", false, errors.New("impact target type is unsupported"))
	}
	if targetType == knowledge.ImpactObjectArtifact && action != knowledge.ImpactActionRegenerateArtifact ||
		targetType == knowledge.ImpactObjectReviewCard && action != knowledge.ImpactActionRevalidateReviewCard {
		return domain.DownstreamUpdate{}, foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_TARGET_INVALID", false, errors.New("impact target action is unsupported"))
	}
	reportQuery := `
		SELECT source_event_id::text,source_event_version,analysis_version,fingerprint,status,schema_version,objects
		FROM ops.impact_report
		WHERE workspace_id=$1 AND id=$2`
	if lockOwner {
		reportQuery += ` FOR UPDATE`
	}
	var sourceEventID, analysisVersion, reportFingerprint, reportStatus, reportSchema string
	var sourceEventVersion int64
	var objectsRaw []byte
	if err := queryer.QueryRow(ctx, reportQuery, string(workspaceID), string(reportID)).Scan(
		&sourceEventID, &sourceEventVersion, &analysisVersion, &reportFingerprint, &reportStatus, &reportSchema, &objectsRaw,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DownstreamUpdate{}, foundation.NewError(foundation.ErrorNotFound, "KNOWLEDGE_IMPACT_NOT_FOUND", false, err)
		}
		return domain.DownstreamUpdate{}, classify(err, "DOWNSTREAM_UPDATE_SOURCE_QUERY_FAILED")
	}
	var superseded bool
	if err := queryer.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM ops.impact_report successor
			WHERE successor.workspace_id=$1 AND successor.supersedes_report_id=$2
		)`, string(workspaceID), string(reportID)).Scan(&superseded); err != nil {
		return domain.DownstreamUpdate{}, classify(err, "DOWNSTREAM_UPDATE_SOURCE_QUERY_FAILED")
	}
	if reportStatus != string(knowledge.ImpactReportReady) || reportSchema != knowledge.ImpactReportSchemaVersionV2 ||
		analysisVersion != string(knowledge.ImpactAnalysisVersionV2) || superseded {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report is not current and ready")
	}
	var objects []knowledge.ImpactObject
	if err := decodeStrictJSON(objectsRaw, &objects); err != nil {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report objects are invalid")
	}
	parsedEventID, err := foundation.ParseID(sourceEventID)
	if err != nil {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report source event is invalid")
	}
	expectedFingerprint, err := knowledge.ComputeImpactFingerprintForVersion(knowledge.ImpactAnalysisVersionV2, parsedEventID, sourceEventVersion, objects)
	if err != nil || expectedFingerprint != reportFingerprint {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report fingerprint is invalid")
	}
	var selected *knowledge.ImpactObject
	for index := range objects {
		object := &objects[index]
		if object.Type == targetType && object.ID == targetID {
			if selected != nil {
				return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report contains duplicate target objects")
			}
			selected = object
		}
	}
	if selected == nil || selected.WorkspaceID != workspaceID || selected.Action != action || !selected.RequiresProposal {
		return domain.DownstreamUpdate{}, foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_TARGET_INVALID", false, errors.New("target is not proposal-capable in the impact report"))
	}

	ownerLock := ""
	if lockOwner {
		ownerLock = " FOR SHARE"
	}
	ownerBinding := knowledge.EventOwnerBinding{}
	switch targetType {
	case knowledge.ImpactObjectArtifact:
		var artifactID, revisionID, contentHash string
		var artifactVersion, revisionNo int64
		err := queryer.QueryRow(ctx, `
			SELECT artifact.id::text,artifact.version,revision.id::text,revision.revision_no,revision.content_hash
			FROM learning.artifact artifact
			JOIN learning.artifact_revision revision
			  ON revision.workspace_id=artifact.workspace_id
			 AND revision.artifact_id=artifact.id
			 AND revision.id=artifact.current_revision_id
			WHERE artifact.workspace_id=$1 AND artifact.id=$2`+ownerLock,
			string(workspaceID), string(targetID)).Scan(&artifactID, &artifactVersion, &revisionID, &revisionNo, &contentHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("artifact owner snapshot is unavailable")
		}
		if err != nil {
			return domain.DownstreamUpdate{}, classify(err, "DOWNSTREAM_UPDATE_OWNER_QUERY_FAILED")
		}
		binding := knowledge.ArtifactImpactBinding{
			ArtifactID: foundation.ID(artifactID), ArtifactVersion: artifactVersion,
			RevisionID: foundation.ID(revisionID), RevisionNo: revisionNo, ContentHash: contentHash,
		}
		if selected.ArtifactBinding == nil || !reflect.DeepEqual(*selected.ArtifactBinding, binding) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("artifact owner binding changed")
		}
		ownerBinding.Artifact = &binding
	case knowledge.ImpactObjectReviewCard:
		var cardID, status, fingerprint, claimID, evidenceFingerprint string
		var cardVersion int64
		err := queryer.QueryRow(ctx, `
			SELECT card.id::text,card.version,card.status,card.fingerprint,card.claim_id::text,
			       learning.review_card_evidence_binding_fingerprint(card.evidence)
			FROM learning.review_card card
			WHERE card.workspace_id=$1 AND card.id=$2`+ownerLock,
			string(workspaceID), string(targetID)).Scan(&cardID, &cardVersion, &status, &fingerprint, &claimID, &evidenceFingerprint)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("review card owner snapshot is unavailable")
		}
		if err != nil {
			return domain.DownstreamUpdate{}, classify(err, "DOWNSTREAM_UPDATE_OWNER_QUERY_FAILED")
		}
		binding := knowledge.ReviewCardImpactBinding{
			CardID: foundation.ID(cardID), CardVersion: cardVersion, Status: status, Fingerprint: fingerprint,
			ClaimID: foundation.ID(claimID), EvidenceBindingFingerprint: evidenceFingerprint,
		}
		if selected.ReviewCardBinding == nil || !reflect.DeepEqual(*selected.ReviewCardBinding, binding) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("review card owner binding changed")
		}
		ownerBinding.ReviewCard = &binding
	}

	update := domain.DownstreamUpdate{
		WorkspaceID: workspaceID, ReportID: reportID, AnalysisVersion: knowledge.ImpactAnalysisVersionV2,
		ReportFingerprint: reportFingerprint, SourceEventID: parsedEventID, SourceEventVersion: sourceEventVersion,
		TargetType: targetType, TargetID: targetID, BaseVersion: selected.Version, Action: action,
		OwnerBinding: ownerBinding, Reason: selected.Reason, SchemaVersion: domain.DownstreamUpdateSchemaVersion,
	}
	canonical, err := domain.ValidateDownstreamUpdate(update)
	if err != nil {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("rebuilt downstream update is invalid")
	}
	return canonical, nil
}

func downstreamImpactConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "KNOWLEDGE_IMPACT_CONFLICT", false, errors.New(message))
}

func (r *Repository) resolveInvalidRequestHash(ctx context.Context, proposal domain.Proposal, mismatchMessage string) (domain.Proposal, error) {
	var existingID string
	err := r.db.QueryRow(ctx, `SELECT id::text FROM change_control.proposal WHERE workspace_id=$1 AND idempotency_key=$2`, string(proposal.WorkspaceID), proposal.IdempotencyKey).Scan(&existingID)
	if err == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Proposal{}, classify(err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New(mismatchMessage))
}

func loadProposalCreateBinding(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, idempotencyKey string) (foundation.ID, domain.ProposalType, string, domain.ProposalRiskLevel, error) {
	var proposalID, proposalType, requestHash, riskLevel string
	err := tx.QueryRow(ctx, `
			SELECT p.id::text,p.proposal_type,p.request_hash,p.risk_level
			FROM change_control.proposal p
			WHERE p.workspace_id=$1 AND p.idempotency_key=$2
			FOR UPDATE OF p`, string(workspaceID), idempotencyKey).Scan(&proposalID, &proposalType, &requestHash, &riskLevel)
	if err != nil {
		return "", "", "", "", err
	}
	storedType := domain.NormalizeProposalType(domain.ProposalType(proposalType))
	parsedRiskLevel, err := domain.ValidateProposalRiskLevelForType(storedType, domain.ProposalRiskLevel(riskLevel))
	if err != nil {
		return "", "", "", "", foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_RISK_LEVEL_INVALID", false, err)
	}
	return foundation.ID(proposalID), storedType, requestHash, parsedRiskLevel, nil
}

// proposalRequestHashReplayMatches 保持单向兼容：历史 v1 可由同一 v1 或等价 v2 重放，
// 新建 v2 只能由 v2 重放，禁止 v1 请求反向匹配 v2 持久事实。
func proposalRequestHashReplayMatches(storedHash, requestedHash, requestHashV2, requestHashV1 string) bool {
	switch storedHash {
	case requestHashV2:
		return requestedHash == requestHashV2
	case requestHashV1:
		return requestedHash == requestHashV1 || requestedHash == requestHashV2
	default:
		return false
	}
}

// GetProposal 使用单条查询返回一致的 Proposal、Revision 和 Approval 快照。
func (r *Repository) GetProposal(ctx context.Context, proposalID foundation.ID) (domain.Proposal, error) {
	row := r.db.QueryRow(ctx, `
			SELECT p.id::text,p.workspace_id::text,p.proposal_type,p.idempotency_key,p.request_hash,p.risk_level,
				(CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END)::text,
				wr.status,p.status,p.version,p.created_at,p.updated_at,p.current_revision_id::text,
			r.id::text,r.revision_no,r.target_path,r.target_mode,r.base_hash,r.content,r.evidence_summary,r.risk,r.rollback_plan,r.change_hash,
				r.target_refs,r.base_versions,r.change_set,r.evidence_refs,
				r.artifact_id::text,r.artifact_revision_id::text,r.artifact_revision_no,r.artifact_version,r.artifact_content_hash,r.artifact_source_coverage,
				r.downstream_workspace_id::text,r.downstream_report_id::text,r.downstream_analysis_version,r.downstream_report_fingerprint,
				r.downstream_source_event_id::text,r.downstream_source_event_version,r.downstream_target_type,r.downstream_target_id::text,
				r.downstream_base_version,r.downstream_action,r.downstream_owner_binding,r.downstream_reason,r.schema_version,r.created_at,
				r.restore_workspace_id::text,r.restore_document_id::text,r.restore_target_commit,r.restore_expected_head,
				r.restore_expected_document_version,r.restore_preview_hash,r.restore_current_content_hash,r.restore_target_content_hash,
			a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
		FROM change_control.proposal p
		JOIN LATERAL (
			SELECT id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			       target_refs,base_versions,change_set,evidence_refs,
				       artifact_id,artifact_revision_id,artifact_revision_no,artifact_version,artifact_content_hash,artifact_source_coverage,
				       downstream_workspace_id,downstream_report_id,downstream_analysis_version,downstream_report_fingerprint,
				       downstream_source_event_id,downstream_source_event_version,downstream_target_type,downstream_target_id,
			       downstream_base_version,downstream_action,downstream_owner_binding,downstream_reason,schema_version,created_at,
			       restore_workspace_id,restore_document_id,restore_target_commit,restore_expected_head,
			       restore_expected_document_version,restore_preview_hash,restore_current_content_hash,restore_target_content_hash
			FROM change_control.proposal_revision
			WHERE proposal_id=p.id AND (id=p.current_revision_id OR p.current_revision_id IS NULL) ORDER BY revision_no DESC LIMIT 1
		) r ON true
		LEFT JOIN change_control.approval a ON a.revision_id=r.id
		LEFT JOIN change_control.proposal_revision_dispatch d
		  ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
		LEFT JOIN workflow.run wr
		  ON wr.id=CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END
		 AND wr.workspace_id=p.workspace_id
		WHERE p.id=$1`, string(proposalID))
	proposal, err := scanProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_QUERY_FAILED")
	}
	return proposal, nil
}

// ListProposals 返回 Workspace 绑定的 Proposal 摘要，使用更新时间和 ID 的 keyset 游标。
func (r *Repository) ListProposals(ctx context.Context, request domain.ProposalListQuery) ([]domain.ProposalListItem, bool, error) {
	if request.WorkspaceID == "" || request.Limit < 1 || request.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_INVALID", false, errors.New("invalid list scope"))
	}
	if request.RiskLevel != "" {
		normalized, riskErr := domain.ParseProposalRiskLevel(request.RiskLevel)
		if riskErr != nil {
			return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_INVALID", false, riskErr)
		}
		request.RiskLevel = normalized
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, false, classify(err, "PROPOSAL_LIST_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	query, args := buildProposalListQuery(request)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, false, classify(err, "PROPOSAL_LIST_QUERY_FAILED")
	}
	defer rows.Close()
	items := make([]domain.ProposalListItem, 0, request.Limit)
	for rows.Next() {
		var proposalID, scopeID, proposalType, status, revisionID, target, riskLevel, risk, changeHash, targetMode string
		var currentRevisionID, workflowRunID, workflowRunStatus, approvalID, approvalHash, approvalDecision, approvedGitHead *string
		var createdAt, updatedAt time.Time
		var approvalDecidedAt *time.Time
		var version int64
		var revisionContentSize int
		if err := rows.Scan(
			&proposalID, &scopeID, &proposalType, &status, &createdAt, &updatedAt, &version, &currentRevisionID,
			&revisionID, &target, &riskLevel, &risk, &changeHash, &targetMode, &revisionContentSize, &workflowRunID, &workflowRunStatus,
			&approvalID, &approvalHash, &approvalDecision, &approvedGitHead, &approvalDecidedAt,
		); err != nil {
			return nil, false, classify(err, "PROPOSAL_LIST_SCAN_FAILED")
		}
		item := domain.ProposalListItem{
			ProposalID: foundation.ID(proposalID), WorkspaceID: foundation.ID(scopeID), Type: domain.ProposalType(proposalType),
			Status: domain.ProposalStatus(status), Target: target, RiskLevel: domain.ProposalRiskLevel(riskLevel), Risk: risk, RevisionID: foundation.ID(revisionID),
			ChangeHash: changeHash, Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt,
		}
		item.Type = domain.NormalizeProposalType(item.Type)
		parsedRiskLevel, riskErr := domain.ValidateProposalRiskLevelForType(item.Type, item.RiskLevel)
		if riskErr != nil {
			return nil, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_LIST_RISK_LEVEL_INVALID", false, riskErr)
		}
		item.RiskLevel = parsedRiskLevel
		approvalPresent := approvalID != nil || approvalHash != nil || approvalDecision != nil || approvedGitHead != nil || approvalDecidedAt != nil
		approvalComplete := approvalID != nil && approvalHash != nil && approvalDecision != nil && approvalDecidedAt != nil
		if approvalPresent && !approvalComplete || workflowRunID != nil && !approvalComplete || (workflowRunID == nil) != (workflowRunStatus == nil) {
			return nil, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_LIST_BINDING_INVALID", false, errors.New("proposal approval and workflow projection is incomplete"))
		}
		if approvalComplete {
			item.Approval = &domain.Approval{
				ID: foundation.ID(*approvalID), ProposalID: item.ProposalID, RevisionID: item.RevisionID,
				ChangeHash: *approvalHash, Decision: domain.Decision(*approvalDecision), ApprovedGitHead: approvedGitHead,
				DecidedAt: *approvalDecidedAt,
			}
			if item.Approval.Decision == domain.DecisionRejected && (item.Approval.ApprovedGitHead != nil || workflowRunID != nil) ||
				!domain.ProposalSupportsFileWriteback(item.Type) && (item.Approval.ApprovedGitHead != nil || workflowRunID != nil) ||
				workflowRunID != nil && (item.Approval.Decision != domain.DecisionApproved || item.Approval.ApprovedGitHead == nil) {
				return nil, false, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_LIST_BINDING_INVALID", false, errors.New("proposal approval contains an invalid durable writeback binding"))
			}
		}
		if workflowRunID != nil {
			value := foundation.ID(*workflowRunID)
			item.WorkflowRunID = &value
		}
		currentID := foundation.ID("")
		if currentRevisionID != nil {
			currentID = foundation.ID(*currentRevisionID)
		}
		workflowStatus := ""
		if workflowRunStatus != nil {
			workflowStatus = *workflowRunStatus
		}
		item.RevisionCapability = domain.EvaluateProposalRevisionCapability(domain.ProposalRevisionCapabilityFacts{
			ProposalType: item.Type, ProposalStatus: item.Status, TargetMode: domain.TargetMode(targetMode),
			CurrentRevisionID: currentID, RevisionID: item.RevisionID, RevisionContentSize: revisionContentSize,
			WorkflowRunID: item.WorkflowRunID, WorkflowRunStatus: workflowStatus,
		})
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, classify(err, "PROPOSAL_LIST_ROWS_FAILED")
	}
	hasMore := len(items) > request.Limit
	if hasMore {
		items = items[:request.Limit]
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, classify(err, "PROPOSAL_LIST_COMMIT_FAILED")
	}
	return items, hasMore, nil
}

func buildProposalListQuery(request domain.ProposalListQuery) (string, []any) {
	query := `SELECT p.id::text,p.workspace_id::text,p.proposal_type,p.status,p.created_at,p.updated_at,p.version,p.current_revision_id::text,
				r.id::text,CASE p.proposal_type
					WHEN 'file_patch' THEN COALESCE(r.target_path,'')
					WHEN 'restore_document' THEN COALESCE(r.target_path,'')
					WHEN 'knowledge_change' THEN '知识关系'
					WHEN 'publish_artifact' THEN 'Artifact 发布'
					WHEN 'downstream_update' THEN COALESCE(r.downstream_target_type || ':' || r.downstream_target_id::text,'下游更新')
					ELSE '' END,
				p.risk_level,r.risk,r.change_hash,r.target_mode,COALESCE(octet_length(r.content),0),
				(CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END)::text,
				wr.status,
		a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
		FROM change_control.proposal p
		JOIN LATERAL (
				SELECT id,target_path,target_mode,content,risk,change_hash,downstream_target_type,downstream_target_id
			FROM change_control.proposal_revision
				WHERE proposal_id=p.id AND (id=p.current_revision_id OR p.current_revision_id IS NULL) ORDER BY revision_no DESC LIMIT 1
		) r ON true
		LEFT JOIN change_control.approval a ON a.revision_id=r.id
		LEFT JOIN change_control.proposal_revision_dispatch d
		  ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
		LEFT JOIN workflow.run wr
		  ON wr.id=CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END
		 AND wr.workspace_id=p.workspace_id
		WHERE p.workspace_id=$1`
	args := []any{string(request.WorkspaceID)}
	appendFilter := func(column string, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		query += ` AND ` + column + `=$` + fmt.Sprint(len(args))
	}
	appendFilter("p.status", string(request.Status))
	appendFilter("p.proposal_type", string(request.Type))
	appendFilter("p.risk_level", string(request.RiskLevel))
	if request.CreatedAfter != nil {
		args = append(args, request.CreatedAfter.UTC())
		query += ` AND p.created_at>=$` + fmt.Sprint(len(args))
	}
	if request.CursorTime != nil {
		args = append(args, request.CursorTime.UTC(), string(request.CursorID))
		query += ` AND (p.updated_at,p.id)<($` + fmt.Sprint(len(args)-1) + `,$` + fmt.Sprint(len(args)) + `)`
	}
	query += ` ORDER BY p.updated_at DESC,p.id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, request.Limit+1)
	return query, args
}

// Approve 只允许 ready_for_review Proposal 被一次性批准或拒绝。
func (r *Repository) Approve(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	if approval.ApprovedGitHead != nil {
		normalized := strings.ToLower(strings.TrimSpace(*approval.ApprovedGitHead))
		approval.ApprovedGitHead = &normalized
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var workspaceID, status, revisionHash string
	var proposalVersion int64
	err = tx.QueryRow(ctx, `
			SELECT p.workspace_id::text,p.status,p.version,r.change_hash
			FROM change_control.proposal p
			JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
			WHERE p.id=$1 AND (p.current_revision_id=$2 OR p.current_revision_id IS NULL)
			FOR UPDATE`, string(approval.ProposalID), string(approval.RevisionID)).Scan(&workspaceID, &status, &proposalVersion, &revisionHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Approval{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_QUERY_FAILED")
	}
	if status != string(domain.StatusReady) {
		existing, existingErr := getApproval(ctx, tx, approval.RevisionID)
		if existingErr == nil && existing.ProposalID == approval.ProposalID && existing.ChangeHash == approval.ChangeHash && existing.Decision == approval.Decision && sameOptionalString(existing.ApprovedGitHead, approval.ApprovedGitHead) {
			if !isNilChangeControlDependency(r.events) {
				eventType, eventStatus := changecontroleventcontract.ProposalRejectedEventType, string(domain.StatusRejected)
				if existing.Decision == domain.DecisionApproved {
					eventType, eventStatus = changecontroleventcontract.ProposalApprovedEventType, string(domain.StatusApproved)
				}
				request := changecontroleventcontract.ProposalStatusRequest(
					foundation.ID(workspaceID), existing.ProposalID, existing.ID,
					eventType, eventStatus, proposalVersion, existing.DecidedAt,
				)
				if _, _, appendErr := r.events.AppendTx(ctx, tx, request); appendErr != nil {
					return domain.Approval{}, appendErr
				}
				if commitErr := tx.Commit(ctx); commitErr != nil {
					return domain.Approval{}, classify(commitErr, "APPROVAL_COMMIT_FAILED")
				}
			}
			return existing, nil
		}
		if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
			return domain.Approval{}, classify(existingErr, "APPROVAL_QUERY_FAILED")
		}
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}
	if revisionHash != approval.ChangeHash {
		return domain.Approval{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_MISMATCH", false, errors.New("change hash mismatch"))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, string(approval.ID), string(approval.ProposalID), string(approval.RevisionID), approval.ChangeHash, string(approval.Decision), approval.ApprovedGitHead, approval.DecidedAt.UTC()); err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_CREATE_FAILED")
	}
	newStatus := domain.StatusRejected
	if approval.Decision == domain.DecisionApproved {
		newStatus = domain.StatusApproved
	}
	command, err := tx.Exec(ctx, `
			UPDATE change_control.proposal SET status=$1,updated_at=$2,version=version+1
			WHERE id=$3 AND status=$4 AND version=$5
			  AND (current_revision_id=$6 OR current_revision_id IS NULL)`, string(newStatus), approval.DecidedAt.UTC(), string(approval.ProposalID), string(domain.StatusReady), proposalVersion, string(approval.RevisionID))
	if err != nil {
		return domain.Approval{}, classify(err, "PROPOSAL_DECISION_UPDATE_FAILED")
	}
	if command.RowsAffected() != 1 {
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
	}
	if !isNilChangeControlDependency(r.events) {
		eventType, eventStatus := changecontroleventcontract.ProposalRejectedEventType, string(domain.StatusRejected)
		if approval.Decision == domain.DecisionApproved {
			eventType, eventStatus = changecontroleventcontract.ProposalApprovedEventType, string(domain.StatusApproved)
		}
		request := changecontroleventcontract.ProposalStatusRequest(
			foundation.ID(workspaceID), approval.ProposalID, approval.ID,
			eventType, eventStatus, proposalVersion+1, approval.DecidedAt,
		)
		if _, _, err := r.events.AppendTx(ctx, tx, request); err != nil {
			return domain.Approval{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_COMMIT_FAILED")
	}
	return approval, nil
}

// MarkNeedsRevision 仅在调用方读取的 Proposal 版本和当前 Revision 仍然成立时记录目标基线冲突。
func (r *Repository) MarkNeedsRevision(ctx context.Context, proposalID, revisionID foundation.ID, expectedVersion int64, at time.Time) error {
	if proposalID == "" || revisionID == "" || expectedVersion <= 0 || at.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_STATE_INVALID", false, errors.New("proposal state binding is invalid"))
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return classify(err, "PROPOSAL_STATE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `
		UPDATE change_control.proposal SET status=$1,updated_at=$2,version=version+1
		WHERE id=$3 AND status IN ($4,$5) AND version=$6
		  AND (current_revision_id=$7 OR current_revision_id IS NULL)`,
		string(domain.StatusNeedsRevision), at.UTC(), string(proposalID), string(domain.StatusApproved), string(domain.StatusReady),
		expectedVersion, string(revisionID))
	if err != nil {
		return classify(err, "PROPOSAL_STATE_UPDATE_FAILED")
	}
	if command.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_STATE_CONFLICT", false, errors.New("proposal version, revision, or status changed"))
	}
	return tx.Commit(ctx)
}

// ValidateWorkflowContext 确认 Run/Node 已持久化且属于同一 Workspace。
func (r *Repository) ValidateWorkflowContext(ctx context.Context, workspaceID, runID, nodeID foundation.ID) error {
	var one int
	err := r.db.QueryRow(ctx, `
		SELECT 1
		FROM workflow.run r
		JOIN workflow.node_run n ON n.run_id=r.id
		WHERE r.id=$1 AND n.id=$2 AND r.workspace_id=$3
		  AND r.status IN ('pending','running','waiting_for_human')
		  AND n.status IN ('pending','running','waiting_for_human')`, string(runID), string(nodeID), string(workspaceID)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID", false, err)
	}
	if err != nil {
		return classify(err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	return nil
}

// GetAuthorization loads authorization metadata without exposing the raw credential.
func (r *Repository) GetAuthorization(ctx context.Context, workspaceID foundation.ID, idempotencyKey, tokenHash string) (domain.ToolAuthorization, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	authorization, err := scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(workspaceID), idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		authorization, err = scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE token_hash=$1 FOR UPDATE`, tokenHash))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ToolAuthorization{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_QUERY_FAILED")
	}
	if authorization.Status == domain.AuthorizationIssued && !databaseNow.Before(authorization.ExpiresAt) {
		expired, updateErr := scanAuthorization(tx.QueryRow(ctx, `UPDATE change_control.tool_authorization SET status='expired',version=version+1 WHERE id=$1 AND status='issued' RETURNING `+authorizationColumns, string(authorization.ID)))
		if updateErr != nil {
			return domain.ToolAuthorization{}, classifyAuthorization(updateErr, "WRITE_AUTHORIZATION_EXPIRE_FAILED")
		}
		authorization = expired
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	return authorization, nil
}

// CreateAuthorization 保存授权绑定；同一 Workspace/幂等键只返回原记录，不重新生成凭据。
func (r *Repository) CreateAuthorization(ctx context.Context, authorization domain.ToolAuthorization) (domain.AuthorizationIssueResult, error) {
	targetMode, modeErr := domain.ValidateTargetMode(authorization.TargetMode)
	if modeErr != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_TARGET_MODE_INVALID", false, modeErr)
	}
	authorization.TargetMode = targetMode
	requested := authorization
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.AuthorizationIssueResult{}, classify(err, "WRITE_AUTHORIZATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	requestedTTL := authorization.ExpiresAt.Sub(authorization.IssuedAt)
	authorization.IssuedAt = databaseNow.UTC()
	authorization.ExpiresAt = databaseNow.UTC().Add(requestedTTL)
	authorization, err = scanAuthorization(tx.QueryRow(ctx, authorizationInsert+` RETURNING `+authorizationColumns,
		string(authorization.ID), string(authorization.WorkspaceID), string(authorization.WorkflowRunID), string(authorization.NodeRunID),
		string(authorization.ProposalID), string(authorization.RevisionID), string(authorization.ApprovalID), authorization.ToolName,
		string(authorization.Capability), authorization.Scope, authorization.ApprovedChangeHash, string(authorization.TargetMode), authorization.TargetVersion,
		authorization.TokenHash, authorization.IdempotencyKey, string(authorization.Status), authorization.IssuedAt.UTC(), authorization.ExpiresAt.UTC(),
		authorizationTimePointer(authorization.RevokedAt), authorizationTimePointer(authorization.ConsumedAt), authorization.Version))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, queryErr := scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(requested.WorkspaceID), requested.IdempotencyKey))
		if queryErr != nil {
			return domain.AuthorizationIssueResult{}, classifyAuthorization(queryErr, "WRITE_AUTHORIZATION_IDEMPOTENCY_QUERY_FAILED")
		}
		if !sameAuthorizationIdentity(existing, requested) {
			return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT", false, errors.New("authorization idempotency key is bound to another request"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
		}
		return domain.AuthorizationIssueResult{Authorization: existing, Replayed: true}, nil
	}
	if err != nil {
		return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	return domain.AuthorizationIssueResult{Authorization: authorization}, nil
}

// ConsumeAuthorization 在数据库事务中执行过期、绑定和一次性消费检查。
func (r *Repository) ConsumeAuthorization(ctx context.Context, request domain.AuthorizationConsume) (domain.AuthorizationConsumeResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CONSUME_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	authorization, err := scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(request.WorkspaceID), request.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		authorization, err = scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE token_hash=$1 FOR UPDATE`, request.Credential))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_QUERY_FAILED")
	}
	if !sameAuthorizationBinding(authorization, request) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_BINDING_CONFLICT", false, errors.New("authorization binding does not match request"))
	}
	if authorization.Status == domain.AuthorizationConsumed {
		if authorization.IdempotencyKey != request.IdempotencyKey {
			return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("authorization was already consumed"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
		}
		return domain.AuthorizationConsumeResult{Authorization: authorization, Replayed: true}, nil
	}
	if authorization.Status == domain.AuthorizationRevoked {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_REVOKED", false, errors.New("authorization was revoked"))
	}
	if authorization.Status == domain.AuthorizationExpired || !databaseNow.Before(authorization.ExpiresAt) {
		if authorization.Status == domain.AuthorizationIssued {
			if _, updateErr := tx.Exec(ctx, `UPDATE change_control.tool_authorization SET status='expired',version=version+1 WHERE id=$1 AND status='issued'`, string(authorization.ID)); updateErr != nil {
				return domain.AuthorizationConsumeResult{}, classifyAuthorization(updateErr, "WRITE_AUTHORIZATION_EXPIRE_FAILED")
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
		}
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_EXPIRED", false, errors.New("authorization has expired"))
	}
	if err := verifyCurrentAuthorizationState(ctx, tx, authorization); err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	consumed, err := scanAuthorization(tx.QueryRow(ctx, `UPDATE change_control.tool_authorization SET status='consumed',consumed_at=$2,version=version+1 WHERE id=$1 AND status='issued' RETURNING `+authorizationColumns, string(authorization.ID), databaseNow.UTC()))
	if err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CONSUME_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	return domain.AuthorizationConsumeResult{Authorization: consumed}, nil
}

// RevokeAuthorization 使 issued 授权失效；对已终止状态重复撤销保持幂等。
func (r *Repository) RevokeAuthorization(ctx context.Context, id foundation.ID, at time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_REVOKE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM change_control.tool_authorization WHERE id=$1 FOR UPDATE`, string(id)).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorNotFound, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
	}
	if err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_QUERY_FAILED")
	}
	if status == string(domain.AuthorizationConsumed) {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("consumed authorization cannot be revoked"))
	}
	if status == string(domain.AuthorizationRevoked) || status == string(domain.AuthorizationExpired) {
		return classifyAuthorization(tx.Commit(ctx), "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	if _, err := tx.Exec(ctx, `UPDATE change_control.tool_authorization SET status='revoked',revoked_at=CURRENT_TIMESTAMP,version=version+1 WHERE id=$1 AND status='issued'`, string(id)); err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_REVOKE_FAILED")
	}
	return classifyAuthorization(tx.Commit(ctx), "WRITE_AUTHORIZATION_COMMIT_FAILED")
}

const authorizationColumns = `id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,proposal_id::text,revision_id::text,approval_id::text,tool_name,capability,scope,approved_change_hash,target_mode,target_version,token_hash,idempotency_key,status,issued_at,expires_at,revoked_at,consumed_at,version`

const authorizationInsert = `INSERT INTO change_control.tool_authorization(
		id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,tool_name,capability,scope,approved_change_hash,target_mode,target_version,token_hash,idempotency_key,status,issued_at,expires_at,revoked_at,consumed_at,version
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
	ON CONFLICT(workspace_id,idempotency_key) DO NOTHING`

func scanAuthorization(row pgx.Row) (domain.ToolAuthorization, error) {
	var authorization domain.ToolAuthorization
	var id, workspaceID, runID, nodeID, proposalID, revisionID, approvalID, capability, status string
	if err := row.Scan(&id, &workspaceID, &runID, &nodeID, &proposalID, &revisionID, &approvalID, &authorization.ToolName, &capability, &authorization.Scope, &authorization.ApprovedChangeHash, &authorization.TargetMode, &authorization.TargetVersion, &authorization.TokenHash, &authorization.IdempotencyKey, &status, &authorization.IssuedAt, &authorization.ExpiresAt, &authorization.RevokedAt, &authorization.ConsumedAt, &authorization.Version); err != nil {
		return domain.ToolAuthorization{}, err
	}
	authorization.ID, authorization.WorkspaceID, authorization.WorkflowRunID, authorization.NodeRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(runID), foundation.ID(nodeID)
	authorization.ProposalID, authorization.RevisionID, authorization.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	authorization.Capability, authorization.Status = domain.Capability(capability), domain.AuthorizationStatus(status)
	authorization.TargetMode = domain.NormalizeTargetMode(authorization.TargetMode)
	return authorization, nil
}

func sameAuthorizationIdentity(existing, requested domain.ToolAuthorization) bool {
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID && existing.NodeRunID == requested.NodeRunID && existing.ProposalID == requested.ProposalID && existing.RevisionID == requested.RevisionID && existing.ApprovalID == requested.ApprovalID && existing.ToolName == requested.ToolName && existing.Capability == requested.Capability && existing.Scope == requested.Scope && existing.ApprovedChangeHash == requested.ApprovedChangeHash && domain.NormalizeTargetMode(existing.TargetMode) == domain.NormalizeTargetMode(requested.TargetMode) && existing.TargetVersion == requested.TargetVersion && existing.IdempotencyKey == requested.IdempotencyKey && existing.ExpiresAt.Sub(existing.IssuedAt) == requested.ExpiresAt.Sub(requested.IssuedAt)
}

func sameAuthorizationBinding(authorization domain.ToolAuthorization, request domain.AuthorizationConsume) bool {
	return domain.ValidateAuthorizationConsumeBinding(authorization, request) == nil
}

func verifyCurrentAuthorizationState(ctx context.Context, tx pgx.Tx, authorization domain.ToolAuthorization) error {
	var workspaceID, status, targetPath, targetMode, baseHash, revisionHash, approvalProposal, approvalRevision, approvalHash, decision string
	err := tx.QueryRow(ctx, `
			SELECT p.workspace_id,p.status,r.target_path,r.target_mode,r.base_hash,r.change_hash,a.proposal_id,a.revision_id,a.change_hash,a.decision
			FROM change_control.proposal p
			JOIN change_control.proposal_revision r ON r.id=$2 AND r.proposal_id=p.id
			JOIN change_control.approval a ON a.id=$3 AND a.proposal_id=p.id AND a.revision_id=r.id
			JOIN change_control.proposal_revision_dispatch d
			  ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id AND d.workflow_run_id=$4
			WHERE p.id=$1 AND p.current_revision_id=r.id
			FOR UPDATE OF p`, string(authorization.ProposalID), string(authorization.RevisionID), string(authorization.ApprovalID), string(authorization.WorkflowRunID)).Scan(&workspaceID, &status, &targetPath, &targetMode, &baseHash, &revisionHash, &approvalProposal, &approvalRevision, &approvalHash, &decision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, err)
		}
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_APPROVAL_QUERY_FAILED")
	}
	mode, modeErr := domain.ValidateTargetMode(domain.TargetMode(targetMode))
	if modeErr != nil || workspaceID != string(authorization.WorkspaceID) || (status != string(domain.StatusApproved) && status != string(domain.StatusApplying)) || approvalProposal != string(authorization.ProposalID) || approvalRevision != string(authorization.RevisionID) || decision != string(domain.DecisionApproved) || revisionHash != authorization.ApprovedChangeHash || approvalHash != authorization.ApprovedChangeHash || baseHash != authorization.TargetVersion || domain.NormalizeTargetMode(authorization.TargetMode) != mode || authorization.Scope != domain.ExpectedAuthorizationScopeForTarget(targetPath, mode) {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, errors.New("authorization approval state is no longer valid"))
	}
	var one int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id
		WHERE r.id=$1 AND n.id=$2 AND r.workspace_id=$3
		  AND r.status IN ('pending','running','waiting_for_human')
		  AND n.status IN ('pending','running','waiting_for_human')
		FOR UPDATE OF r,n`, string(authorization.WorkflowRunID), string(authorization.NodeRunID), string(authorization.WorkspaceID)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID", false, err)
	}
	if err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	return nil
}

func classifyAuthorization(err error, code string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT", false, err)
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}

func authorizationTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func scanProposal(row pgx.Row) (domain.Proposal, error) {
	var proposalID, workspaceID, proposalType, idempotencyKey, requestHash, riskLevel, status string
	var currentRevisionID *string
	var workflowRunID, workflowRunStatus *string
	var revisionID, risk, rollback, changeHash string
	var targetPath, targetMode, baseHash, content, evidence, artifactID, artifactRevisionID, artifactContentHash, schemaVersion *string
	var downstreamWorkspaceID, downstreamReportID, downstreamAnalysisVersion, downstreamReportFingerprint *string
	var downstreamSourceEventID, downstreamTargetType, downstreamTargetID, downstreamAction, downstreamReason *string
	var restoreWorkspaceID, restoreDocumentID, restoreTargetCommit, restoreExpectedHead *string
	var restorePreviewHash, restoreCurrentContentHash, restoreTargetContentHash *string
	var artifactRevisionNo, artifactVersion, downstreamSourceEventVersion, downstreamBaseVersion *int64
	var restoreExpectedDocumentVersion *int64
	var targetRefsRaw, baseVersionsRaw, changeSetRaw, evidenceRefsRaw, artifactSourceCoverageRaw, downstreamOwnerBindingRaw []byte
	var approvalID, approvalHash, decision, approvedGitHead *string
	var createdAt, updatedAt, revisionCreatedAt time.Time
	var decidedAt *time.Time
	var revisionNo int
	var version int64
	err := row.Scan(
		&proposalID, &workspaceID, &proposalType, &idempotencyKey, &requestHash, &riskLevel, &workflowRunID, &workflowRunStatus, &status, &version, &createdAt, &updatedAt, &currentRevisionID,
		&revisionID, &revisionNo, &targetPath, &targetMode, &baseHash, &content, &evidence, &risk, &rollback, &changeHash,
		&targetRefsRaw, &baseVersionsRaw, &changeSetRaw, &evidenceRefsRaw,
		&artifactID, &artifactRevisionID, &artifactRevisionNo, &artifactVersion, &artifactContentHash, &artifactSourceCoverageRaw,
		&downstreamWorkspaceID, &downstreamReportID, &downstreamAnalysisVersion, &downstreamReportFingerprint,
		&downstreamSourceEventID, &downstreamSourceEventVersion, &downstreamTargetType, &downstreamTargetID,
		&downstreamBaseVersion, &downstreamAction, &downstreamOwnerBindingRaw, &downstreamReason, &schemaVersion, &revisionCreatedAt,
		&restoreWorkspaceID, &restoreDocumentID, &restoreTargetCommit, &restoreExpectedHead, &restoreExpectedDocumentVersion,
		&restorePreviewHash, &restoreCurrentContentHash, &restoreTargetContentHash,
		&approvalID, &approvalHash, &decision, &approvedGitHead, &decidedAt,
	)
	if err != nil {
		return domain.Proposal{}, err
	}
	proposal := domain.Proposal{
		ID: foundation.ID(proposalID), WorkspaceID: foundation.ID(workspaceID), Type: domain.ProposalType(proposalType),
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, RiskLevel: domain.ProposalRiskLevel(riskLevel),
		Status: domain.ProposalStatus(status), Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt,
		Revision: domain.Revision{
			ID: foundation.ID(revisionID), ProposalID: foundation.ID(proposalID), RevisionNo: revisionNo,
			Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash, CreatedAt: revisionCreatedAt,
		},
	}
	if currentRevisionID != nil {
		proposal.CurrentRevisionID = foundation.ID(*currentRevisionID)
	}
	proposal.Type = domain.NormalizeProposalType(proposal.Type)
	parsedRiskLevel, riskErr := domain.ValidateProposalRiskLevelForType(proposal.Type, domain.ProposalRiskLevel(riskLevel))
	if riskErr != nil {
		return domain.Proposal{}, riskErr
	}
	proposal.RiskLevel = parsedRiskLevel
	downstreamFieldsPresent := downstreamWorkspaceID != nil || downstreamReportID != nil || downstreamAnalysisVersion != nil || downstreamReportFingerprint != nil ||
		downstreamSourceEventID != nil || downstreamSourceEventVersion != nil || downstreamTargetType != nil || downstreamTargetID != nil ||
		downstreamBaseVersion != nil || downstreamAction != nil || downstreamOwnerBindingRaw != nil || downstreamReason != nil
	restoreFieldsPresent := restoreWorkspaceID != nil || restoreDocumentID != nil || restoreTargetCommit != nil || restoreExpectedHead != nil ||
		restoreExpectedDocumentVersion != nil || restorePreviewHash != nil || restoreCurrentContentHash != nil || restoreTargetContentHash != nil
	switch domain.NormalizeProposalType(proposal.Type) {
	case domain.ProposalTypeFilePatch:
		if targetPath == nil || targetMode == nil || baseHash == nil || content == nil || evidence == nil || schemaVersion != nil ||
			targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil || artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil || downstreamFieldsPresent || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("file patch proposal revision payload is inconsistent")
		}
		proposal.Type = domain.ProposalTypeFilePatch
		proposal.TargetPath = *targetPath
		proposal.Revision.TargetPath = *targetPath
		mode, modeErr := domain.ValidateTargetMode(domain.TargetMode(*targetMode))
		if modeErr != nil || domain.ValidateTargetBaseVersion(proposal.WorkspaceID, *targetPath, mode, *baseHash) != nil {
			return domain.Proposal{}, errors.New("file patch target mode is inconsistent")
		}
		proposal.Revision.TargetMode = mode
		proposal.Revision.BaseHash = *baseHash
		proposal.Revision.Content = *content
		proposal.Revision.EvidenceSummary = *evidence
	case domain.ProposalTypeRestoreDocument:
		if targetPath == nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash == nil || content == nil || evidence == nil || schemaVersion == nil ||
			targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil || artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil || downstreamFieldsPresent ||
			restoreWorkspaceID == nil || restoreDocumentID == nil || restoreTargetCommit == nil || restoreExpectedHead == nil || restoreExpectedDocumentVersion == nil || restorePreviewHash == nil || restoreCurrentContentHash == nil || restoreTargetContentHash == nil {
			return domain.Proposal{}, errors.New("restore document proposal revision payload is inconsistent")
		}
		restore, err := domain.ValidateRestoreDocument(domain.RestoreDocument{
			WorkspaceID: foundation.ID(*restoreWorkspaceID), DocumentID: foundation.ID(*restoreDocumentID),
			TargetCommit: *restoreTargetCommit, ExpectedHead: *restoreExpectedHead,
			ExpectedDocumentVersion: *restoreExpectedDocumentVersion, PreviewHash: *restorePreviewHash,
			CurrentContentHash: *restoreCurrentContentHash, TargetContentHash: *restoreTargetContentHash,
			SchemaVersion: *schemaVersion,
		})
		if err != nil || restore.WorkspaceID != proposal.WorkspaceID {
			return domain.Proposal{}, errors.New("restore document workspace binding is inconsistent")
		}
		proposal.TargetPath = *targetPath
		proposal.Revision.TargetPath = *targetPath
		proposal.Revision.TargetMode = domain.TargetModeReplace
		proposal.Revision.BaseHash = *baseHash
		proposal.Revision.Content = *content
		proposal.Revision.EvidenceSummary = *evidence
		proposal.Revision.RestoreDocument = &restore
	case domain.ProposalTypeKnowledgeChange:
		if targetPath != nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash != nil || content != nil || evidence != nil || schemaVersion == nil ||
			targetRefsRaw == nil || baseVersionsRaw == nil || changeSetRaw == nil || evidenceRefsRaw == nil || artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil || downstreamFieldsPresent || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("knowledge change proposal revision payload is inconsistent")
		}
		change := domain.KnowledgeChange{SchemaVersion: *schemaVersion}
		if err := json.Unmarshal(targetRefsRaw, &change.TargetRefs); err != nil {
			return domain.Proposal{}, err
		}
		if err := json.Unmarshal(baseVersionsRaw, &change.BaseVersions); err != nil {
			return domain.Proposal{}, err
		}
		if err := json.Unmarshal(changeSetRaw, &change.ChangeSet); err != nil {
			return domain.Proposal{}, err
		}
		if err := json.Unmarshal(evidenceRefsRaw, &change.EvidenceRefs); err != nil {
			return domain.Proposal{}, err
		}
		canonical, err := domain.ValidateKnowledgeChange(change)
		if err != nil {
			return domain.Proposal{}, err
		}
		proposal.Type = domain.ProposalTypeKnowledgeChange
		proposal.Revision.KnowledgeChange = &canonical
	case domain.ProposalTypePublishArtifact:
		if targetPath != nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash != nil || content != nil || evidence != nil || targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil ||
			artifactID == nil || artifactRevisionID == nil || artifactRevisionNo == nil || artifactVersion == nil || artifactContentHash == nil || artifactSourceCoverageRaw == nil || schemaVersion == nil || downstreamFieldsPresent || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("publish artifact proposal revision payload is inconsistent")
		}
		publication := domain.PublishArtifact{
			WorkspaceID: proposal.WorkspaceID, ArtifactID: foundation.ID(*artifactID), RevisionID: foundation.ID(*artifactRevisionID),
			RevisionNo: *artifactRevisionNo, ArtifactVersion: *artifactVersion, ContentHash: *artifactContentHash, SchemaVersion: *schemaVersion,
		}
		if err := json.Unmarshal(artifactSourceCoverageRaw, &publication.SourceCoverage); err != nil {
			return domain.Proposal{}, err
		}
		canonical, err := domain.ValidatePublishArtifact(publication)
		if err != nil {
			return domain.Proposal{}, err
		}
		proposal.Type = domain.ProposalTypePublishArtifact
		proposal.Revision.PublishArtifact = &canonical
	case domain.ProposalTypeDownstreamUpdate:
		if targetPath != nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash != nil || content != nil || evidence != nil || targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil ||
			artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil ||
			downstreamWorkspaceID == nil || downstreamReportID == nil || downstreamAnalysisVersion == nil || downstreamReportFingerprint == nil ||
			downstreamSourceEventID == nil || downstreamSourceEventVersion == nil || downstreamTargetType == nil || downstreamTargetID == nil ||
			downstreamBaseVersion == nil || downstreamAction == nil || downstreamOwnerBindingRaw == nil || downstreamReason == nil || schemaVersion == nil || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("downstream update proposal revision payload is inconsistent")
		}
		var ownerBinding knowledge.EventOwnerBinding
		if err := decodeStrictJSON(downstreamOwnerBindingRaw, &ownerBinding); err != nil {
			return domain.Proposal{}, err
		}
		update := domain.DownstreamUpdate{
			WorkspaceID: foundation.ID(*downstreamWorkspaceID), ReportID: foundation.ID(*downstreamReportID),
			AnalysisVersion: knowledge.ImpactAnalysisVersion(*downstreamAnalysisVersion), ReportFingerprint: *downstreamReportFingerprint,
			SourceEventID: foundation.ID(*downstreamSourceEventID), SourceEventVersion: *downstreamSourceEventVersion,
			TargetType: knowledge.ImpactObjectType(*downstreamTargetType), TargetID: foundation.ID(*downstreamTargetID), BaseVersion: *downstreamBaseVersion,
			Action: knowledge.ImpactAction(*downstreamAction), OwnerBinding: ownerBinding, Reason: *downstreamReason, SchemaVersion: *schemaVersion,
		}
		canonical, err := domain.ValidateDownstreamUpdate(update)
		if err != nil || canonical.WorkspaceID != proposal.WorkspaceID {
			if err == nil {
				err = errors.New("downstream update workspace binding is inconsistent")
			}
			return domain.Proposal{}, err
		}
		proposal.Type = domain.ProposalTypeDownstreamUpdate
		proposal.Revision.DownstreamUpdate = &canonical
	default:
		return domain.Proposal{}, errors.New("proposal type is unsupported")
	}
	if err := domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil {
		return domain.Proposal{}, err
	}
	if workflowRunID != nil {
		if workflowRunStatus == nil {
			return domain.Proposal{}, errors.New("proposal workflow status binding is incomplete")
		}
		if !domain.ProposalSupportsFileWriteback(proposal.Type) {
			return domain.Proposal{}, errors.New("typed proposal has an invalid writeback workflow binding")
		}
		value := foundation.ID(*workflowRunID)
		proposal.WorkflowRunID = &value
		proposal.WorkflowRunStatus = *workflowRunStatus
	} else if workflowRunStatus != nil {
		return domain.Proposal{}, errors.New("proposal workflow status exists without a workflow binding")
	}
	if approvalID != nil && approvalHash != nil && decision != nil && decidedAt != nil {
		if !domain.ProposalSupportsFileWriteback(proposal.Type) && approvedGitHead != nil {
			return domain.Proposal{}, errors.New("typed proposal has an invalid git approval binding")
		}
		proposal.Approval = &domain.Approval{
			ID: foundation.ID(*approvalID), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
			ChangeHash: *approvalHash, Decision: domain.Decision(*decision), ApprovedGitHead: approvedGitHead, DecidedAt: *decidedAt,
		}
	}
	return proposal, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("json payload contains multiple values")
		}
		return err
	}
	return nil
}

func getApproval(ctx context.Context, row interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, revisionID foundation.ID) (domain.Approval, error) {
	var id, proposalID, revision, changeHash, decision string
	var approvedGitHead *string
	var decidedAt time.Time
	err := row.QueryRow(ctx, `SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,approved_git_head,decided_at FROM change_control.approval WHERE revision_id=$1`, string(revisionID)).Scan(&id, &proposalID, &revision, &changeHash, &decision, &approvedGitHead, &decidedAt)
	if err != nil {
		return domain.Approval{}, err
	}
	return domain.Approval{ID: foundation.ID(id), ProposalID: foundation.ID(proposalID), RevisionID: foundation.ID(revision), ChangeHash: changeHash, Decision: domain.Decision(decision), ApprovedGitHead: approvedGitHead, DecidedAt: decidedAt}, nil
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(*left, *right)
}

func isNilChangeControlDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_ALREADY_DECIDED", false, err)
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}
