package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontroleventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"gorm.io/gorm"
)

var _ domain.Repository = (*GORMRepository)(nil)
var _ domain.ProposalListRepository = (*GORMRepository)(nil)
var _ domain.KnowledgeChangeProposalRepository = (*GORMRepository)(nil)
var _ domain.PublishArtifactProposalRepository = (*GORMRepository)(nil)
var _ domain.DownstreamUpdateProposalRepository = (*GORMRepository)(nil)
var _ domain.RestoreDocumentProposalRepository = (*GORMRepository)(nil)

// CreateProposal persists the file-patch Proposal and its first immutable
// Revision. Other types retain their own strict request and payload rules.
func (repository *GORMRepository) CreateProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	switch domain.NormalizeProposalType(proposal.Type) {
	case domain.ProposalTypeKnowledgeChange:
		return repository.CreateKnowledgeChangeProposal(ctx, proposal)
	case domain.ProposalTypePublishArtifact:
		return repository.CreatePublishArtifactProposal(ctx, proposal)
	case domain.ProposalTypeDownstreamUpdate:
		return repository.CreateDownstreamUpdateProposal(ctx, proposal)
	case domain.ProposalTypeFilePatch:
		proposal.Type = domain.ProposalTypeFilePatch
	default:
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrProposalTypeInvalid)
	}
	risk, err := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if err != nil || domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision) != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrProposalTypeInvalid)
	}
	if snapshot := proposal.Revision.BaseSnapshot; snapshot != nil &&
		(domain.NormalizeTargetMode(proposal.Revision.TargetMode) != domain.TargetModeReplace ||
			snapshot.ProposalID != proposal.ID || snapshot.RevisionID != proposal.Revision.ID || snapshot.BaseHash != proposal.Revision.BaseHash ||
			domain.ValidateRevisionBaseSnapshot(*snapshot) != nil) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("proposal revision base snapshot is invalid"))
	}
	proposal.RiskLevel = risk
	expected, err := domain.ComputeRequestHashWithTargetMode(proposal.WorkspaceID, proposal.Revision.TargetPath, proposal.Revision.TargetMode, proposal.Revision.BaseHash, proposal.Revision.Content, proposal.Revision.EvidenceSummary, proposal.RiskLevel, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	legacy := ""
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeReplace {
		legacy = domain.ComputeRequestHash(proposal.WorkspaceID, proposal.Revision.TargetPath, proposal.Revision.BaseHash, proposal.Revision.Content, proposal.Revision.EvidenceSummary, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	}
	if err != nil || proposal.RequestHash != expected && proposal.RequestHash != legacy {
		return repository.gormChangeInvalidProposalHash(ctx, proposal)
	}
	if proposal.RequestHash == legacy && legacy != expected {
		return repository.gormChangeReplayLegacyProposal(ctx, proposal, expected, legacy)
	}
	return repository.gormChangeCreateTypedProposal(ctx, proposal, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, func(_ context.Context, _ *gorm.DB, revision domain.Revision) ([]any, error) {
		return []any{string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.TargetPath, string(domain.NormalizeTargetMode(revision.TargetMode)), revision.BaseHash, revision.Content, revision.EvidenceSummary, revision.Risk, revision.RollbackPlan, revision.ChangeHash, revision.CreatedAt.UTC()}, nil
	})
}

func (repository *GORMRepository) CreateKnowledgeChangeProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypeKnowledgeChange
	if proposal.Revision.KnowledgeChange == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("knowledge change revision payload is required"))
	}
	risk, err := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if err != nil || domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision) != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrProposalTypeInvalid)
	}
	proposal.RiskLevel = risk
	expected, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel(proposal.WorkspaceID, *proposal.Revision.KnowledgeChange, risk, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	legacy, legacyErr := domain.ComputeKnowledgeChangeRequestHash(proposal.WorkspaceID, *proposal.Revision.KnowledgeChange, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	if err != nil || legacyErr != nil || proposal.RequestHash != expected && proposal.RequestHash != legacy {
		return repository.gormChangeInvalidProposalHash(ctx, proposal)
	}
	if proposal.RequestHash == legacy && legacy != expected {
		return repository.gormChangeReplayLegacyProposal(ctx, proposal, expected, legacy)
	}
	return repository.gormChangeCreateTypedProposal(ctx, proposal, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
		) VALUES(?,?,?,NULL,'REPLACE',NULL,NULL,NULL,?,?,?,?,?,?,?,?,?)`, func(_ context.Context, _ *gorm.DB, revision domain.Revision) ([]any, error) {
		change := *revision.KnowledgeChange
		targets, err := json.Marshal(change.TargetRefs)
		if err != nil {
			return nil, err
		}
		bases, err := json.Marshal(change.BaseVersions)
		if err != nil {
			return nil, err
		}
		set, err := json.Marshal(change.ChangeSet)
		if err != nil {
			return nil, err
		}
		evidence, err := json.Marshal(change.EvidenceRefs)
		if err != nil {
			return nil, err
		}
		return []any{string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.Risk, revision.RollbackPlan, revision.ChangeHash, changeControlJSONB(targets), changeControlJSONB(bases), changeControlJSONB(set), changeControlJSONB(evidence), change.SchemaVersion, revision.CreatedAt.UTC()}, nil
	})
}

func (repository *GORMRepository) CreatePublishArtifactProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypePublishArtifact
	if proposal.Revision.PublishArtifact == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("publish artifact revision payload is required"))
	}
	publication, err := domain.ValidatePublishArtifact(*proposal.Revision.PublishArtifact)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	proposal.Revision.PublishArtifact = &publication
	risk, err := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if err != nil || domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision) != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrProposalTypeInvalid)
	}
	proposal.RiskLevel = risk
	expected, err := domain.ComputePublishArtifactRequestHash(proposal.WorkspaceID, publication, risk, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	if err != nil || proposal.RequestHash != expected {
		return repository.gormChangeInvalidProposalHash(ctx, proposal)
	}
	return repository.gormChangeCreateTypedProposal(ctx, proposal, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			artifact_id,artifact_revision_id,artifact_revision_no,artifact_version,artifact_content_hash,artifact_source_coverage,schema_version,created_at
		) VALUES(?,?,?,NULL,'REPLACE',NULL,NULL,NULL,?,?,?,?,?,?,?,?,?,?,?)`, func(_ context.Context, _ *gorm.DB, revision domain.Revision) ([]any, error) {
		coverage, err := json.Marshal(publication.SourceCoverage)
		if err != nil {
			return nil, err
		}
		return []any{string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.Risk, revision.RollbackPlan, revision.ChangeHash, string(publication.ArtifactID), string(publication.RevisionID), publication.RevisionNo, publication.ArtifactVersion, publication.ContentHash, changeControlJSONB(coverage), publication.SchemaVersion, revision.CreatedAt.UTC()}, nil
	})
}

func (repository *GORMRepository) CreateDownstreamUpdateProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypeDownstreamUpdate
	if proposal.Revision.DownstreamUpdate == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("downstream update revision payload is required"))
	}
	update, err := domain.ValidateDownstreamUpdate(*proposal.Revision.DownstreamUpdate)
	if err != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	proposal.Revision.DownstreamUpdate = &update
	risk, err := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if err != nil || domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision) != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrProposalTypeInvalid)
	}
	proposal.RiskLevel = risk
	expected, err := domain.ComputeDownstreamUpdateRequestHash(proposal.WorkspaceID, update, risk, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	if err != nil || proposal.RequestHash != expected {
		return repository.gormChangeInvalidProposalHash(ctx, proposal)
	}
	return repository.gormChangeCreateTypedProposal(ctx, proposal, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			downstream_workspace_id,downstream_report_id,downstream_analysis_version,downstream_report_fingerprint,
			downstream_source_event_id,downstream_source_event_version,downstream_target_type,downstream_target_id,
			downstream_base_version,downstream_action,downstream_owner_binding,downstream_reason,schema_version,created_at
		) VALUES(?,?,?,NULL,'REPLACE',NULL,NULL,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, func(callbackCtx context.Context, tx *gorm.DB, revision domain.Revision) ([]any, error) {
		current, err := repository.gormChangeBuildDownstreamUpdate(callbackCtx, tx, update.WorkspaceID, update.ReportID, update.TargetType, update.TargetID, update.Action, true)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(current, update) {
			return nil, foundation.NewError(foundation.ErrorVersionConflict, "KNOWLEDGE_IMPACT_CONFLICT", false, errors.New("downstream update owner binding changed before create"))
		}
		binding, err := json.Marshal(update.OwnerBinding)
		if err != nil {
			return nil, err
		}
		return []any{string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.Risk, revision.RollbackPlan, revision.ChangeHash, string(update.WorkspaceID), string(update.ReportID), string(update.AnalysisVersion), update.ReportFingerprint, string(update.SourceEventID), update.SourceEventVersion, string(update.TargetType), string(update.TargetID), update.BaseVersion, string(update.Action), changeControlJSONB(binding), update.Reason, update.SchemaVersion, revision.CreatedAt.UTC()}, nil
	})
}

func (repository *GORMRepository) CreateRestoreDocumentProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	proposal.Type = domain.ProposalTypeRestoreDocument
	if proposal.Revision.RestoreDocument == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrRestoreDocumentInvalid)
	}
	restore, err := domain.ValidateRestoreDocument(*proposal.Revision.RestoreDocument)
	if err != nil || restore.WorkspaceID != proposal.WorkspaceID || proposal.TargetPath != proposal.Revision.TargetPath {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrRestoreDocumentInvalid)
	}
	proposal.Revision.RestoreDocument = &restore
	risk, err := domain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel)
	if err != nil || domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision) != nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, domain.ErrProposalTypeInvalid)
	}
	proposal.RiskLevel = risk
	expected, err := domain.ComputeRestoreDocumentRequestHash(proposal.WorkspaceID, restore, proposal.Revision.TargetPath, proposal.Revision.Content, proposal.Revision.EvidenceSummary, risk, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
	if err != nil || proposal.RequestHash != expected {
		return repository.gormChangeInvalidProposalHash(ctx, proposal)
	}
	return repository.gormChangeCreateTypedProposal(ctx, proposal, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			restore_workspace_id,restore_document_id,restore_target_commit,restore_expected_head,restore_expected_document_version,
			restore_preview_hash,restore_current_content_hash,restore_target_content_hash,schema_version,created_at
		) VALUES(?,?,?,?,'REPLACE',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, func(callbackCtx context.Context, tx *gorm.DB, revision domain.Revision) ([]any, error) {
		row, err := gormChangeRawRow(callbackCtx, tx, `SELECT canonical_path,lifecycle_status,COALESCE(current_published_revision_id::text,''),version FROM core.document WHERE workspace_id=? AND id=? FOR SHARE`, string(proposal.WorkspaceID), string(restore.DocumentID))
		if err != nil {
			return nil, classifyGORMChange(callbackCtx, err, "DOCUMENT_RESTORE_BINDING_QUERY_FAILED")
		}
		var canonicalPath, lifecycle, currentRevisionID string
		var documentVersion int64
		if err := row.Scan(&canonicalPath, &lifecycle, &currentRevisionID, &documentVersion); gormChangeNoRows(err) {
			return nil, foundation.NewError(foundation.ErrorNotFound, "DOCUMENT_HISTORY_NOT_FOUND", false, err)
		} else if err != nil {
			return nil, classifyGORMChange(callbackCtx, err, "DOCUMENT_RESTORE_BINDING_QUERY_FAILED")
		}
		if lifecycle != "PUBLISHED" || currentRevisionID == "" || canonicalPath != revision.TargetPath || documentVersion != restore.ExpectedDocumentVersion {
			return nil, foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("published document binding changed before proposal creation"))
		}
		return []any{string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.TargetPath, revision.BaseHash, revision.Content, revision.EvidenceSummary, revision.Risk, revision.RollbackPlan, revision.ChangeHash, string(restore.WorkspaceID), string(restore.DocumentID), restore.TargetCommit, restore.ExpectedHead, restore.ExpectedDocumentVersion, restore.PreviewHash, restore.CurrentContentHash, restore.TargetContentHash, restore.SchemaVersion, revision.CreatedAt.UTC()}, nil
	})
}

func (repository *GORMRepository) gormChangeCreateTypedProposal(ctx context.Context, proposal domain.Proposal, revisionSQL string, arguments func(context.Context, *gorm.DB, domain.Revision) ([]any, error)) (domain.Proposal, error) {
	var replayID foundation.ID
	inserted := false
	err := repository.within(ctx, foundation.TransactionOptions{},
		"PROPOSAL_TRANSACTION_FAILED", "PROPOSAL_COMMIT_FAILED", classifyGORMChange,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			row, err := gormChangeRawRow(callbackCtx, tx, `
			INSERT INTO change_control.proposal(id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at,current_revision_id)
			VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,idempotency_key) DO NOTHING RETURNING id::text`,
				string(proposal.ID), string(proposal.WorkspaceID), string(proposal.Type), string(proposal.RiskLevel), proposal.IdempotencyKey, proposal.RequestHash, string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC(), string(proposal.Revision.ID))
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_CREATE_FAILED")
			}
			var id string
			if err := row.Scan(&id); gormChangeNoRows(err) {
				binding, loadErr := gormChangeRawRow(callbackCtx, tx, `SELECT id::text,proposal_type,request_hash,risk_level FROM change_control.proposal WHERE workspace_id=? AND idempotency_key=? FOR UPDATE`, string(proposal.WorkspaceID), proposal.IdempotencyKey)
				if loadErr != nil {
					return classifyGORMChange(callbackCtx, loadErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
				}
				var storedID, storedType, storedHash, storedRisk string
				if scanErr := binding.Scan(&storedID, &storedType, &storedHash, &storedRisk); scanErr != nil {
					return classifyGORMChange(callbackCtx, scanErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
				}
				if storedType != string(proposal.Type) || storedRisk != string(proposal.RiskLevel) || !gormChangeProposalHashMatches(proposal, storedHash) {
					return foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
				}
				replayID = foundation.ID(storedID)
				return foundation.NewError(foundation.ErrorNonRetryableFailure, "GORM_PROPOSAL_REPLAY", false, nil)
			} else if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_CREATE_FAILED")
			}
			inserted = true
			values, valueErr := arguments(callbackCtx, tx, proposal.Revision)
			if valueErr != nil {
				return valueErr
			}
			if _, err := gormChangeExec(callbackCtx, tx, revisionSQL, values...); err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_CREATE_FAILED")
			}
			if snapshot := proposal.Revision.BaseSnapshot; snapshot != nil {
				if _, err := gormChangeExec(callbackCtx, tx, `INSERT INTO change_control.proposal_revision_base_snapshot(proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at) VALUES(?,?,?,?,?,?,?)`, string(snapshot.ProposalID), string(snapshot.RevisionID), snapshot.BaseHash, snapshot.Content, snapshot.ByteSize, snapshot.SchemaVersion, snapshot.CreatedAt.UTC()); err != nil {
					return classifyGORMChange(callbackCtx, err, "PROPOSAL_REVISION_SNAPSHOT_CREATE_FAILED")
				}
			}
			return nil
		})
	if err != nil {
		var replay *foundation.Error
		if errors.As(err, &replay) && replay.Code == "GORM_PROPOSAL_REPLAY" {
			return repository.GetProposal(ctx, replayID)
		}
		return domain.Proposal{}, err
	}
	if !inserted {
		return repository.GetProposal(ctx, replayID)
	}
	proposal.CurrentRevisionID = proposal.Revision.ID
	return proposal, nil
}

// FindProposalByIdempotencyKey resolves the durable aggregate after a narrow,
// workspace-scoped key lookup; it never treats a key from another workspace as
// a replay.
func (repository *GORMRepository) FindProposalByIdempotencyKey(ctx context.Context, workspaceID foundation.ID, key string) (domain.Proposal, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Proposal{}, false, err
	}
	row, err := gormChangeRawRow(ctx, repository.database, `SELECT id::text FROM change_control.proposal WHERE workspace_id=? AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return domain.Proposal{}, false, classifyGORMChange(ctx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	var id string
	if err := row.Scan(&id); gormChangeNoRows(err) {
		return domain.Proposal{}, false, nil
	} else if err != nil {
		return domain.Proposal{}, false, classifyGORMChange(ctx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	proposal, err := repository.GetProposal(ctx, foundation.ID(id))
	if err != nil {
		return domain.Proposal{}, false, err
	}
	return proposal, true, nil
}

func (repository *GORMRepository) GetProposal(ctx context.Context, proposalID foundation.ID) (domain.Proposal, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Proposal{}, err
	}
	row, err := gormChangeRawRow(ctx, repository.database, gormChangeProposalSnapshotSQL, string(proposalID))
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_QUERY_FAILED")
	}
	proposal, err := scanProposal(row)
	if gormChangeNoRows(err) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_QUERY_FAILED")
	}
	return proposal, nil
}

func (repository *GORMRepository) ListProposals(ctx context.Context, request domain.ProposalListQuery) ([]domain.ProposalListItem, bool, error) {
	if request.WorkspaceID == "" || request.Limit < 1 || request.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_INVALID", false, errors.New("invalid list scope"))
	}
	if request.RiskLevel != "" {
		parsed, err := domain.ParseProposalRiskLevel(request.RiskLevel)
		if err != nil {
			return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_INVALID", false, err)
		}
		request.RiskLevel = parsed
	}
	items := make([]domain.ProposalListItem, 0, request.Limit+1)
	err := repository.within(ctx, foundation.TransactionOptions{ReadOnly: true},
		"PROPOSAL_LIST_TRANSACTION_FAILED", "PROPOSAL_LIST_COMMIT_FAILED", classifyGORMChange,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			query, arguments := buildProposalListQuery(request)
			rows, err := gormChangeRawRows(callbackCtx, tx, query, arguments...)
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_LIST_QUERY_FAILED")
			}
			defer rows.Close()
			for rows.Next() {
				item, scanErr := gormChangeScanProposalListItem(rows)
				if scanErr != nil {
					return classifyGORMChange(callbackCtx, scanErr, "PROPOSAL_LIST_SCAN_FAILED")
				}
				items = append(items, item)
			}
			if err := rows.Err(); err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_LIST_ROWS_FAILED")
			}
			return nil
		})
	if err != nil {
		return nil, false, err
	}
	more := len(items) > request.Limit
	if more {
		items = items[:request.Limit]
	}
	return items, more, nil
}

func (repository *GORMRepository) Approve(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	if approval.ApprovedGitHead != nil {
		normalized := strings.ToLower(strings.TrimSpace(*approval.ApprovedGitHead))
		approval.ApprovedGitHead = &normalized
	}
	var result domain.Approval
	err := repository.within(ctx, foundation.TransactionOptions{},
		"APPROVAL_TRANSACTION_FAILED", "APPROVAL_COMMIT_FAILED", classifyGORMChange,
		func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
			row, err := gormChangeRawRow(callbackCtx, tx, `
			SELECT proposal.workspace_id::text,proposal.status,proposal.version,revision.change_hash
			FROM change_control.proposal AS proposal JOIN change_control.proposal_revision AS revision ON revision.proposal_id=proposal.id AND revision.id=?
			WHERE proposal.id=? AND (proposal.current_revision_id=? OR proposal.current_revision_id IS NULL) FOR UPDATE`, string(approval.RevisionID), string(approval.ProposalID), string(approval.RevisionID))
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "APPROVAL_QUERY_FAILED")
			}
			var workspaceID, status, revisionHash string
			var version int64
			if err := row.Scan(&workspaceID, &status, &version, &revisionHash); gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
			} else if err != nil {
				return classifyGORMChange(callbackCtx, err, "APPROVAL_QUERY_FAILED")
			}
			if status != string(domain.StatusReady) {
				existing, lookupErr := gormChangeLoadApproval(callbackCtx, tx, approval.RevisionID)
				if lookupErr == nil && existing.ProposalID == approval.ProposalID && existing.ChangeHash == approval.ChangeHash && existing.Decision == approval.Decision && gormChangeSameOptionalString(existing.ApprovedGitHead, approval.ApprovedGitHead) {
					result = existing
					if !isNilChangeControlDependency(repository.events) {
						eventType, eventStatus := changecontroleventcontract.ProposalRejectedEventType, string(domain.StatusRejected)
						if existing.Decision == domain.DecisionApproved {
							eventType, eventStatus = changecontroleventcontract.ProposalApprovedEventType, string(domain.StatusApproved)
						}
						_, _, appendErr := repository.events.AppendScoped(callbackCtx, scope, changecontroleventcontract.ProposalStatusRequest(foundation.ID(workspaceID), existing.ProposalID, existing.ID, eventType, eventStatus, version, existing.DecidedAt))
						return appendErr
					}
					return foundation.NewError(foundation.ErrorNonRetryableFailure, "GORM_APPROVAL_REPLAY", false, nil)
				}
				if lookupErr != nil && !gormChangeNoRows(lookupErr) {
					return classifyGORMChange(callbackCtx, lookupErr, "APPROVAL_QUERY_FAILED")
				}
				return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
			}
			if revisionHash != approval.ChangeHash {
				return foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_MISMATCH", false, errors.New("change hash mismatch"))
			}
			if _, err := gormChangeExec(callbackCtx, tx, `INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at) VALUES(?,?,?,?,?,?,?)`, string(approval.ID), string(approval.ProposalID), string(approval.RevisionID), approval.ChangeHash, string(approval.Decision), approval.ApprovedGitHead, approval.DecidedAt.UTC()); err != nil {
				return classifyGORMChange(callbackCtx, err, "APPROVAL_CREATE_FAILED")
			}
			newStatus := domain.StatusRejected
			eventType := changecontroleventcontract.ProposalRejectedEventType
			if approval.Decision == domain.DecisionApproved {
				newStatus, eventType = domain.StatusApproved, changecontroleventcontract.ProposalApprovedEventType
			}
			changed, err := gormChangeExec(callbackCtx, tx, `UPDATE change_control.proposal SET status=?,updated_at=?,version=version+1 WHERE id=? AND status=? AND version=? AND (current_revision_id=? OR current_revision_id IS NULL)`, string(newStatus), approval.DecidedAt.UTC(), string(approval.ProposalID), string(domain.StatusReady), version, string(approval.RevisionID))
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_DECISION_UPDATE_FAILED")
			}
			if changed != 1 {
				return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
			}
			if !isNilChangeControlDependency(repository.events) {
				if _, _, err := repository.events.AppendScoped(callbackCtx, scope, changecontroleventcontract.ProposalStatusRequest(foundation.ID(workspaceID), approval.ProposalID, approval.ID, eventType, string(newStatus), version+1, approval.DecidedAt)); err != nil {
					return err
				}
			}
			result = approval
			return nil
		})
	if err != nil {
		var replay *foundation.Error
		if errors.As(err, &replay) && replay.Code == "GORM_APPROVAL_REPLAY" {
			return result, nil
		}
		return domain.Approval{}, err
	}
	return result, nil
}

func (repository *GORMRepository) MarkNeedsRevision(ctx context.Context, proposalID, revisionID foundation.ID, expectedVersion int64, at time.Time) error {
	if proposalID == "" || revisionID == "" || expectedVersion <= 0 || at.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_STATE_INVALID", false, errors.New("proposal state binding is invalid"))
	}
	return repository.within(ctx, foundation.TransactionOptions{},
		"PROPOSAL_STATE_TRANSACTION_FAILED", "PROPOSAL_STATE_TRANSACTION_FAILED", classifyGORMChange,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			changed, err := gormChangeExec(callbackCtx, tx, `UPDATE change_control.proposal SET status=?,updated_at=?,version=version+1 WHERE id=? AND status IN (?,?) AND version=? AND (current_revision_id=? OR current_revision_id IS NULL)`, string(domain.StatusNeedsRevision), at.UTC(), string(proposalID), string(domain.StatusApproved), string(domain.StatusReady), expectedVersion, string(revisionID))
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_STATE_UPDATE_FAILED")
			}
			if changed != 1 {
				return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_STATE_CONFLICT", false, errors.New("proposal version, revision, or status changed"))
			}
			return nil
		})
}

func (repository *GORMRepository) gormChangeInvalidProposalHash(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Proposal{}, err
	}
	row, err := gormChangeRawRow(ctx, repository.database, `SELECT id::text FROM change_control.proposal WHERE workspace_id=? AND idempotency_key=?`, string(proposal.WorkspaceID), proposal.IdempotencyKey)
	if err != nil {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	var existingID string
	if err := row.Scan(&existingID); err == nil {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
	} else if !gormChangeNoRows(err) {
		return domain.Proposal{}, classifyGORMChange(ctx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
	}
	return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("proposal request hash mismatch"))
}

func gormChangeProposalHashMatches(proposal domain.Proposal, stored string) bool {
	if stored == proposal.RequestHash {
		return true
	}
	legacy, ok := gormChangeLegacyRequestHash(proposal)
	return ok && stored == legacy
}

func (repository *GORMRepository) gormChangeReplayLegacyProposal(ctx context.Context, proposal domain.Proposal, currentHash, legacyHash string) (domain.Proposal, error) {
	var replayID foundation.ID
	err := repository.within(ctx, foundation.TransactionOptions{},
		"PROPOSAL_TRANSACTION_FAILED", "PROPOSAL_COMMIT_FAILED", classifyGORMChange,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			row, err := gormChangeRawRow(callbackCtx, tx, `
			SELECT id::text,proposal_type,request_hash,risk_level
			FROM change_control.proposal
			WHERE workspace_id=? AND idempotency_key=?
			FOR UPDATE`, string(proposal.WorkspaceID), proposal.IdempotencyKey)
			if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
			}
			var id, proposalType, storedHash, riskLevel string
			if err := row.Scan(&id, &proposalType, &storedHash, &riskLevel); gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("legacy request hash is only valid for persisted replay"))
			} else if err != nil {
				return classifyGORMChange(callbackCtx, err, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
			}
			if proposalType != string(proposal.Type) || riskLevel != string(proposal.RiskLevel) ||
				!proposalRequestHashReplayMatches(storedHash, proposal.RequestHash, currentHash, legacyHash) {
				return foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
			}
			replayID = foundation.ID(id)
			return foundation.NewError(foundation.ErrorNonRetryableFailure, "GORM_PROPOSAL_REPLAY", false, nil)
		})
	if err != nil {
		var replay *foundation.Error
		if errors.As(err, &replay) && replay.Code == "GORM_PROPOSAL_REPLAY" {
			return repository.GetProposal(ctx, replayID)
		}
		return domain.Proposal{}, err
	}
	return domain.Proposal{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED", false, errors.New("legacy proposal replay committed unexpectedly"))
}

func gormChangeLegacyRequestHash(proposal domain.Proposal) (string, bool) {
	switch domain.NormalizeProposalType(proposal.Type) {
	case domain.ProposalTypeFilePatch:
		if domain.NormalizeTargetMode(proposal.Revision.TargetMode) != domain.TargetModeReplace {
			return "", false
		}
		return domain.ComputeRequestHash(proposal.WorkspaceID, proposal.Revision.TargetPath, proposal.Revision.BaseHash, proposal.Revision.Content, proposal.Revision.EvidenceSummary, proposal.Revision.Risk, proposal.Revision.RollbackPlan), true
	case domain.ProposalTypeKnowledgeChange:
		if proposal.Revision.KnowledgeChange == nil {
			return "", false
		}
		value, err := domain.ComputeKnowledgeChangeRequestHash(proposal.WorkspaceID, *proposal.Revision.KnowledgeChange, proposal.Revision.Risk, proposal.Revision.RollbackPlan)
		return value, err == nil
	default:
		return "", false
	}
}

const gormChangeProposalSnapshotSQL = `
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
			target_refs,base_versions,change_set,evidence_refs,artifact_id,artifact_revision_id,artifact_revision_no,artifact_version,artifact_content_hash,artifact_source_coverage,
			downstream_workspace_id,downstream_report_id,downstream_analysis_version,downstream_report_fingerprint,downstream_source_event_id,downstream_source_event_version,downstream_target_type,downstream_target_id,
			downstream_base_version,downstream_action,downstream_owner_binding,downstream_reason,schema_version,created_at,
			restore_workspace_id,restore_document_id,restore_target_commit,restore_expected_head,restore_expected_document_version,restore_preview_hash,restore_current_content_hash,restore_target_content_hash
		FROM change_control.proposal_revision
		WHERE proposal_id=p.id AND (id=p.current_revision_id OR p.current_revision_id IS NULL) ORDER BY revision_no DESC LIMIT 1
	) r ON true
	LEFT JOIN change_control.approval a ON a.revision_id=r.id
	LEFT JOIN change_control.proposal_revision_dispatch d ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
	LEFT JOIN workflow.run wr ON wr.id=CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END AND wr.workspace_id=p.workspace_id
	WHERE p.id=?`

func gormChangeLoadApproval(ctx context.Context, database *gorm.DB, revisionID foundation.ID) (domain.Approval, error) {
	row, err := gormChangeRawRow(ctx, database, `SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,approved_git_head,decided_at FROM change_control.approval WHERE revision_id=?`, string(revisionID))
	if err != nil {
		return domain.Approval{}, err
	}
	var id, proposalID, revision, hash, decision string
	var head *string
	var decided time.Time
	if err := row.Scan(&id, &proposalID, &revision, &hash, &decision, &head, &decided); err != nil {
		return domain.Approval{}, err
	}
	return domain.Approval{ID: foundation.ID(id), ProposalID: foundation.ID(proposalID), RevisionID: foundation.ID(revision), ChangeHash: hash, Decision: domain.Decision(decision), ApprovedGitHead: head, DecidedAt: decided}, nil
}

func gormChangeSameOptionalString(left, right *string) bool { return sameOptionalString(left, right) }

func gormChangeScanProposalListItem(rows interface{ Scan(...any) error }) (domain.ProposalListItem, error) {
	var proposalID, workspaceID, proposalType, status, revisionID, target, riskLevel, risk, changeHash, targetMode string
	var currentRevisionID, workflowRunID, workflowStatus, approvalID, approvalHash, approvalDecision, approvedHead *string
	var createdAt, updatedAt time.Time
	var approvalDecidedAt *time.Time
	var version int64
	var contentSize int
	if err := rows.Scan(&proposalID, &workspaceID, &proposalType, &status, &createdAt, &updatedAt, &version, &currentRevisionID, &revisionID, &target, &riskLevel, &risk, &changeHash, &targetMode, &contentSize, &workflowRunID, &workflowStatus, &approvalID, &approvalHash, &approvalDecision, &approvedHead, &approvalDecidedAt); err != nil {
		return domain.ProposalListItem{}, err
	}
	item := domain.ProposalListItem{ProposalID: foundation.ID(proposalID), WorkspaceID: foundation.ID(workspaceID), Type: domain.NormalizeProposalType(domain.ProposalType(proposalType)), Status: domain.ProposalStatus(status), Target: target, RiskLevel: domain.ProposalRiskLevel(riskLevel), Risk: risk, RevisionID: foundation.ID(revisionID), ChangeHash: changeHash, Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt}
	parsedRisk, err := domain.ValidateProposalRiskLevelForType(item.Type, item.RiskLevel)
	if err != nil {
		return domain.ProposalListItem{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_LIST_RISK_LEVEL_INVALID", false, err)
	}
	item.RiskLevel = parsedRisk
	present := approvalID != nil || approvalHash != nil || approvalDecision != nil || approvedHead != nil || approvalDecidedAt != nil
	complete := approvalID != nil && approvalHash != nil && approvalDecision != nil && approvalDecidedAt != nil
	if present && !complete || workflowRunID != nil && !complete || (workflowRunID == nil) != (workflowStatus == nil) {
		return domain.ProposalListItem{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_LIST_BINDING_INVALID", false, errors.New("proposal approval and workflow projection is incomplete"))
	}
	if complete {
		item.Approval = &domain.Approval{ID: foundation.ID(*approvalID), ProposalID: item.ProposalID, RevisionID: item.RevisionID, ChangeHash: *approvalHash, Decision: domain.Decision(*approvalDecision), ApprovedGitHead: approvedHead, DecidedAt: *approvalDecidedAt}
		if item.Approval.Decision == domain.DecisionRejected && (approvedHead != nil || workflowRunID != nil) || !domain.ProposalSupportsFileWriteback(item.Type) && (approvedHead != nil || workflowRunID != nil) || workflowRunID != nil && (item.Approval.Decision != domain.DecisionApproved || approvedHead == nil) {
			return domain.ProposalListItem{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_LIST_BINDING_INVALID", false, errors.New("proposal approval contains an invalid durable writeback binding"))
		}
	}
	if workflowRunID != nil {
		value := foundation.ID(*workflowRunID)
		item.WorkflowRunID = &value
	}
	current := foundation.ID("")
	if currentRevisionID != nil {
		current = foundation.ID(*currentRevisionID)
	}
	workflow := ""
	if workflowStatus != nil {
		workflow = *workflowStatus
	}
	item.RevisionCapability = domain.EvaluateProposalRevisionCapability(domain.ProposalRevisionCapabilityFacts{ProposalType: item.Type, ProposalStatus: item.Status, TargetMode: domain.TargetMode(targetMode), CurrentRevisionID: current, RevisionID: item.RevisionID, RevisionContentSize: contentSize, WorkflowRunID: item.WorkflowRunID, WorkflowRunStatus: workflow})
	return item, nil
}

// BuildDownstreamUpdate rebuilds the proposal payload from the durable impact
// report. The write path requests row locks; this read path does not.
func (repository *GORMRepository) BuildDownstreamUpdate(ctx context.Context, workspaceID, reportID foundation.ID, targetType knowledge.ImpactObjectType, targetID foundation.ID, action knowledge.ImpactAction) (domain.DownstreamUpdate, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.DownstreamUpdate{}, err
	}
	return repository.gormChangeBuildDownstreamUpdate(ctx, repository.database, workspaceID, reportID, targetType, targetID, action, false)
}

func (repository *GORMRepository) gormChangeBuildDownstreamUpdate(ctx context.Context, database *gorm.DB, workspaceID, reportID foundation.ID, targetType knowledge.ImpactObjectType, targetID foundation.ID, action knowledge.ImpactAction, lock bool) (domain.DownstreamUpdate, error) {
	if targetType != knowledge.ImpactObjectArtifact && targetType != knowledge.ImpactObjectReviewCard || targetType == knowledge.ImpactObjectArtifact && action != knowledge.ImpactActionRegenerateArtifact || targetType == knowledge.ImpactObjectReviewCard && action != knowledge.ImpactActionRevalidateReviewCard {
		return domain.DownstreamUpdate{}, foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_TARGET_INVALID", false, errors.New("impact target type or action is unsupported"))
	}
	reportQuery := `SELECT source_event_id::text,source_event_version,analysis_version,fingerprint,status,schema_version,objects FROM ops.impact_report WHERE workspace_id=? AND id=?`
	if lock {
		reportQuery += ` FOR UPDATE`
	}
	row, err := gormChangeRawRow(ctx, database, reportQuery, string(workspaceID), string(reportID))
	if err != nil {
		return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_SOURCE_QUERY_FAILED")
	}
	var sourceEventID, analysisVersion, fingerprint, status, schema string
	var sourceVersion int64
	var objectsRaw []byte
	if err := row.Scan(&sourceEventID, &sourceVersion, &analysisVersion, &fingerprint, &status, &schema, &objectsRaw); gormChangeNoRows(err) {
		return domain.DownstreamUpdate{}, foundation.NewError(foundation.ErrorNotFound, "KNOWLEDGE_IMPACT_NOT_FOUND", false, err)
	} else if err != nil {
		return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_SOURCE_QUERY_FAILED")
	}
	row, err = gormChangeRawRow(ctx, database, `SELECT EXISTS(SELECT 1 FROM ops.impact_report successor WHERE successor.workspace_id=? AND successor.supersedes_report_id=?)`, string(workspaceID), string(reportID))
	if err != nil {
		return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_SOURCE_QUERY_FAILED")
	}
	var superseded bool
	if err := row.Scan(&superseded); err != nil {
		return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_SOURCE_QUERY_FAILED")
	}
	if status != string(knowledge.ImpactReportReady) || schema != knowledge.ImpactReportSchemaVersionV2 || analysisVersion != string(knowledge.ImpactAnalysisVersionV2) || superseded {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report is not current and ready")
	}
	var objects []knowledge.ImpactObject
	if err := decodeStrictJSON(objectsRaw, &objects); err != nil {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report objects are invalid")
	}
	eventID, err := foundation.ParseID(sourceEventID)
	if err != nil {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report source event is invalid")
	}
	expected, err := knowledge.ComputeImpactFingerprintForVersion(knowledge.ImpactAnalysisVersionV2, eventID, sourceVersion, objects)
	if err != nil || expected != fingerprint {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report fingerprint is invalid")
	}
	var selected *knowledge.ImpactObject
	for index := range objects {
		candidate := &objects[index]
		if candidate.Type == targetType && candidate.ID == targetID {
			if selected != nil {
				return domain.DownstreamUpdate{}, downstreamImpactConflict("impact report contains duplicate target objects")
			}
			selected = candidate
		}
	}
	if selected == nil || selected.WorkspaceID != workspaceID || selected.Action != action || !selected.RequiresProposal {
		return domain.DownstreamUpdate{}, foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_TARGET_INVALID", false, errors.New("target is not proposal-capable in the impact report"))
	}
	ownerLock := ""
	if lock {
		ownerLock = " FOR SHARE"
	}
	binding := knowledge.EventOwnerBinding{}
	switch targetType {
	case knowledge.ImpactObjectArtifact:
		var artifactID, revisionID, contentHash string
		var artifactVersion, revisionNo int64
		row, err = gormChangeRawRow(ctx, database, `SELECT artifact.id::text,artifact.version,revision.id::text,revision.revision_no,revision.content_hash FROM learning.artifact artifact JOIN learning.artifact_revision revision ON revision.workspace_id=artifact.workspace_id AND revision.artifact_id=artifact.id AND revision.id=artifact.current_revision_id WHERE artifact.workspace_id=? AND artifact.id=?`+ownerLock, string(workspaceID), string(targetID))
		if err != nil {
			return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_OWNER_QUERY_FAILED")
		}
		if err := row.Scan(&artifactID, &artifactVersion, &revisionID, &revisionNo, &contentHash); gormChangeNoRows(err) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("artifact owner snapshot is unavailable")
		} else if err != nil {
			return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_OWNER_QUERY_FAILED")
		}
		artifact := knowledge.ArtifactImpactBinding{ArtifactID: foundation.ID(artifactID), ArtifactVersion: artifactVersion, RevisionID: foundation.ID(revisionID), RevisionNo: revisionNo, ContentHash: contentHash}
		if selected.ArtifactBinding == nil || !reflect.DeepEqual(*selected.ArtifactBinding, artifact) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("artifact owner binding changed")
		}
		binding.Artifact = &artifact
	case knowledge.ImpactObjectReviewCard:
		var cardID, cardStatus, cardFingerprint, claimID, evidenceFingerprint string
		var cardVersion int64
		row, err = gormChangeRawRow(ctx, database, `SELECT card.id::text,card.version,card.status,card.fingerprint,card.claim_id::text,learning.review_card_evidence_binding_fingerprint(card.evidence) FROM learning.review_card card WHERE card.workspace_id=? AND card.id=?`+ownerLock, string(workspaceID), string(targetID))
		if err != nil {
			return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_OWNER_QUERY_FAILED")
		}
		if err := row.Scan(&cardID, &cardVersion, &cardStatus, &cardFingerprint, &claimID, &evidenceFingerprint); gormChangeNoRows(err) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("review card owner snapshot is unavailable")
		} else if err != nil {
			return domain.DownstreamUpdate{}, classifyGORMChange(ctx, err, "DOWNSTREAM_UPDATE_OWNER_QUERY_FAILED")
		}
		card := knowledge.ReviewCardImpactBinding{CardID: foundation.ID(cardID), CardVersion: cardVersion, Status: cardStatus, Fingerprint: cardFingerprint, ClaimID: foundation.ID(claimID), EvidenceBindingFingerprint: evidenceFingerprint}
		if selected.ReviewCardBinding == nil || !reflect.DeepEqual(*selected.ReviewCardBinding, card) {
			return domain.DownstreamUpdate{}, downstreamImpactConflict("review card owner binding changed")
		}
		binding.ReviewCard = &card
	}
	canonical, err := domain.ValidateDownstreamUpdate(domain.DownstreamUpdate{WorkspaceID: workspaceID, ReportID: reportID, AnalysisVersion: knowledge.ImpactAnalysisVersionV2, ReportFingerprint: fingerprint, SourceEventID: eventID, SourceEventVersion: sourceVersion, TargetType: targetType, TargetID: targetID, BaseVersion: selected.Version, Action: action, OwnerBinding: binding, Reason: selected.Reason, SchemaVersion: domain.DownstreamUpdateSchemaVersion})
	if err != nil {
		return domain.DownstreamUpdate{}, downstreamImpactConflict("rebuilt downstream update is invalid")
	}
	return canonical, nil
}
