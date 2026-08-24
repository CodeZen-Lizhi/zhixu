package postgres

import (
	"context"
	"errors"
	"strings"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

func gormValidateRestoreWriteback(repository *GORMRepository, ctx context.Context, check authoringapp.RestoreWritebackCheck) error {
	if err := repository.ready(); err != nil {
		return err
	}
	if ctx == nil || !validID(check.WorkspaceID) || !validID(check.DocumentID) || check.ExpectedDocumentVersion < 1 || !validHash(check.CurrentContentHash) {
		return foundation.NewError(foundation.ErrorInvalidInput, "RESTORE_DOCUMENT_OWNER_CHECK_INVALID", false, errors.New("restore document owner check is invalid"))
	}
	path, e := domain.CanonicalizeTargetPath(check.TargetPath)
	if e != nil || path != check.TargetPath {
		return foundation.NewError(foundation.ErrorInvalidInput, "RESTORE_DOCUMENT_OWNER_CHECK_INVALID", false, errors.New("restore target path is invalid"))
	}
	row, e := gormRow(repository.database.WithContext(ctx), `SELECT document.version,document.canonical_path,document.lifecycle_status,revision.content_hash FROM core.document document JOIN core.article_revision revision ON revision.id=document.current_published_revision_id AND revision.workspace_id=document.workspace_id AND revision.document_id=document.id WHERE document.workspace_id=? AND document.id=?`, string(check.WorkspaceID), string(check.DocumentID))
	if e != nil {
		return classifyGORM(ctx, e, "RESTORE_DOCUMENT_OWNER_CHECK_FAILED")
	}
	var version int64
	var target, lifecycle, hash string
	if e = row.Scan(&version, &target, &lifecycle, &hash); e != nil {
		if gormNoRows(e) {
			return versionConflict("restore document owner no longer has a published revision")
		}
		return classifyGORM(ctx, e, "RESTORE_DOCUMENT_OWNER_CHECK_FAILED")
	}
	if version != check.ExpectedDocumentVersion || target != path || lifecycle != string(domain.DocumentPublished) || !strings.EqualFold(hash, check.CurrentContentHash) {
		return versionConflict("restore document owner facts changed after approval")
	}
	return nil
}

func gormFinalizeRestorePublication(repository *GORMRepository, ctx context.Context, record authoringapp.RestorePublicationRecord) (published bool, err error) {
	if err = validateRestorePublicationRecord(record); err != nil {
		return false, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if e := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "restore-publication:"+string(record.WritebackID)).Error; e != nil {
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_PUBLICATION_LOCK_FAILED")
		}
		row, e := gormRow(tx.WithContext(ctx), `SELECT proposal_type FROM change_control.proposal WHERE id=? AND workspace_id=? FOR UPDATE`, string(record.ProposalID), string(record.WorkspaceID))
		if e != nil {
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_PROPOSAL_QUERY_FAILED")
		}
		var proposalType string
		if e = row.Scan(&proposalType); e != nil {
			if gormNoRows(e) {
				return notFound(e)
			}
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_PROPOSAL_QUERY_FAILED")
		}
		if changecontroldomain.NormalizeProposalType(changecontroldomain.ProposalType(proposalType)) != changecontroldomain.ProposalTypeRestoreDocument {
			return nil
		}
		facts, e := gormLoadRestoreFacts(ctx, tx, record)
		if e != nil {
			return e
		}
		revisionID, e := restoreArticleRevisionID(record.WritebackID)
		if e != nil {
			return e
		}
		if e := tx.Exec(`INSERT INTO authoring.document_restore_publication(writeback_execution_id,workspace_id,document_id,article_revision_id,proposal_id,proposal_revision_id,proposal_commit_id,expected_document_version,target_path,current_content_hash,target_content_hash,git_commit,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(writeback_execution_id) DO NOTHING`, string(record.WritebackID), string(facts.WorkspaceID), string(facts.DocumentID), string(revisionID), string(record.ProposalID), string(record.ProposalRevisionID), string(facts.ProposalCommitID), facts.ExpectedDocumentVersion, facts.TargetPath, facts.CurrentContentHash, facts.TargetContentHash, record.GitCommit, record.PublishedAt.UTC()).Error; e != nil {
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_PUBLICATION_BIND_FAILED")
		}
		var exact bool
		row, e = gormRow(tx.WithContext(ctx), `SELECT EXISTS(SELECT 1 FROM authoring.document_restore_publication WHERE writeback_execution_id=? AND workspace_id=? AND document_id=? AND article_revision_id=? AND proposal_id=? AND proposal_revision_id=? AND proposal_commit_id=? AND expected_document_version=? AND target_path=? AND current_content_hash=? AND target_content_hash=? AND git_commit=?)`, string(record.WritebackID), string(facts.WorkspaceID), string(facts.DocumentID), string(revisionID), string(record.ProposalID), string(record.ProposalRevisionID), string(facts.ProposalCommitID), facts.ExpectedDocumentVersion, facts.TargetPath, facts.CurrentContentHash, facts.TargetContentHash, record.GitCommit)
		if e != nil {
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_PUBLICATION_BIND_QUERY_FAILED")
		}
		if e = row.Scan(&exact); e != nil {
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_PUBLICATION_BIND_QUERY_FAILED")
		}
		if !exact {
			return inconsistent("restore publication replay binding changed")
		}
		existing, e := gormScanRevision(tx.WithContext(ctx), `SELECT `+revisionColumns+` FROM core.article_revision WHERE id=? AND workspace_id=? AND document_id=? FOR UPDATE`, string(revisionID), string(facts.WorkspaceID), string(facts.DocumentID))
		if e == nil {
			if e := validateExistingRestoreRevision(existing, facts, record); e != nil {
				return e
			}
			return nil
		} else if !gormNoRows(e) {
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_REVISION_QUERY_FAILED")
		}
		document, e := gormLoadDocument(ctx, tx, facts.WorkspaceID, facts.DocumentID, true)
		if e != nil {
			return e
		}
		if document.Version != facts.ExpectedDocumentVersion || document.CanonicalPath != facts.TargetPath || document.Lifecycle != domain.DocumentPublished || document.CurrentPublishedRevisionID == "" {
			return versionConflict("restore document owner facts changed before publication")
		}
		current, e := gormLoadRevision(ctx, tx, facts.WorkspaceID, facts.DocumentID, document.CurrentPublishedRevisionID, true)
		if e != nil {
			return e
		}
		if current.Status != domain.RevisionPublished || !strings.EqualFold(current.ContentHash, facts.CurrentContentHash) {
			return versionConflict("restore source revision changed before publication")
		}
		latest, e := gormLoadLatestRevision(ctx, tx, facts.WorkspaceID, facts.DocumentID, true)
		if e != nil {
			return e
		}
		res := tx.Exec(`UPDATE core.article_revision SET status='SUPERSEDED' WHERE id=? AND workspace_id=? AND document_id=? AND status='PUBLISHED'`, string(current.ID), string(current.WorkspaceID), string(current.DocumentID))
		if res.Error != nil {
			return classifyGORM(ctx, res.Error, "RESTORE_DOCUMENT_REVISION_SUPERSEDE_FAILED")
		}
		if res.RowsAffected != 1 {
			return inconsistent("restore current published revision lost its lock")
		}
		if e := tx.Exec(`INSERT INTO core.article_revision(id,workspace_id,document_id,source_version_id,parent_revision_id,revision_no,content,content_hash,status,optimization_mode,git_commit,created_by_type,created_at) VALUES(?,?,?,NULL,?,?,?,?, 'PUBLISHED','NONE',?,'SYSTEM',?)`, string(revisionID), string(facts.WorkspaceID), string(facts.DocumentID), string(current.ID), latest.RevisionNo+1, facts.Content, facts.TargetContentHash, record.GitCommit, record.PublishedAt.UTC()).Error; e != nil {
			return classifyGORM(ctx, e, "RESTORE_DOCUMENT_REVISION_CREATE_FAILED")
		}
		res = tx.Exec(`UPDATE core.document SET current_published_revision_id=?,version=version+1,updated_at=GREATEST(updated_at,?) WHERE id=? AND workspace_id=? AND version=? AND canonical_path=? AND lifecycle_status='PUBLISHED' AND current_published_revision_id=?`, string(revisionID), record.PublishedAt.UTC(), string(document.ID), string(document.WorkspaceID), document.Version, document.CanonicalPath, string(current.ID))
		if res.Error != nil {
			return classifyGORM(ctx, res.Error, "RESTORE_DOCUMENT_OWNER_UPDATE_FAILED")
		}
		if res.RowsAffected != 1 {
			return versionConflict("restore document compare-and-swap did not update one row")
		}
		published = true
		return nil
	}, "RESTORE_DOCUMENT_PUBLICATION_TRANSACTION_FAILED")
	if err != nil {
		return false, err
	}
	return published, err
}

func gormLoadRestoreFacts(ctx context.Context, tx *gorm.DB, r authoringapp.RestorePublicationRecord) (facts restorePublicationFacts, err error) {
	row, e := gormRow(tx.WithContext(ctx), `SELECT proposal.status,revision.restore_workspace_id::text,revision.restore_document_id::text,revision.target_path,revision.content,revision.base_hash,revision.restore_expected_document_version,revision.restore_current_content_hash,revision.restore_target_content_hash,revision.schema_version,commit_mapping.id::text,commit_mapping.git_commit,commit_mapping.result_hash,commit_mapping.target_path,commit_mapping.target_mode FROM change_control.proposal proposal JOIN change_control.proposal_revision revision ON revision.proposal_id=proposal.id AND revision.id=? JOIN change_control.proposal_commit commit_mapping ON commit_mapping.proposal_id=proposal.id AND commit_mapping.revision_id=revision.id AND commit_mapping.writeback_execution_id=? WHERE proposal.id=? AND proposal.workspace_id=?`, string(r.ProposalRevisionID), string(r.WritebackID), string(r.ProposalID), string(r.WorkspaceID))
	if e != nil {
		return facts, classifyGORM(ctx, e, "RESTORE_DOCUMENT_PUBLICATION_QUERY_FAILED")
	}
	var status, base, schema, git, hash, path, mode string
	if e = row.Scan(&status, &facts.WorkspaceID, &facts.DocumentID, &facts.TargetPath, &facts.Content, &base, &facts.ExpectedDocumentVersion, &facts.CurrentContentHash, &facts.TargetContentHash, &schema, &facts.ProposalCommitID, &git, &hash, &path, &mode); e != nil {
		if gormNoRows(e) {
			return facts, inconsistent("restore proposal_commit binding is missing")
		}
		return facts, classifyGORM(ctx, e, "RESTORE_DOCUMENT_PUBLICATION_QUERY_FAILED")
	}
	if (status != string(changecontroldomain.StatusVerifying) && status != string(changecontroldomain.StatusCompleted)) || facts.WorkspaceID != r.WorkspaceID || !validID(facts.DocumentID) || schema != changecontroldomain.RestoreDocumentSchemaVersion || facts.ExpectedDocumentVersion < 1 || base != facts.CurrentContentHash || path != facts.TargetPath || mode != string(changecontroldomain.TargetModeReplace) || !strings.EqualFold(git, r.GitCommit) || !strings.EqualFold(hash, r.ResultHash) || !strings.EqualFold(facts.TargetContentHash, r.ResultHash) || !strings.EqualFold(domain.ComputeContentHash(facts.Content), facts.TargetContentHash) {
		return facts, inconsistent("restore proposal_commit facts do not match publication")
	}
	canonical, e := domain.CanonicalizeTargetPath(facts.TargetPath)
	if e != nil || canonical != facts.TargetPath || !validID(facts.ProposalCommitID) || !validHash(facts.CurrentContentHash) || !validHash(facts.TargetContentHash) {
		return facts, inconsistent("restore publication facts are invalid")
	}
	return facts, nil
}
