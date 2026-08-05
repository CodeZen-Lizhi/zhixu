package postgres

import (
	"context"
	"crypto/sha1" // #nosec G505 -- UUID v5 requires SHA-1; it is not used for security.
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const restoreArticleRevisionLabel = "restore-article-revision"

// ValidateRestoreWriteback verifies the mutable Document facts immediately
// before Safe Writeback first touches the target file.
func (repository *Repository) ValidateRestoreWriteback(ctx context.Context, check authoringapp.RestoreWritebackCheck) error {
	if ctx == nil || !validID(check.WorkspaceID) || !validID(check.DocumentID) || check.ExpectedDocumentVersion < 1 ||
		!validHash(check.CurrentContentHash) {
		return foundation.NewError(foundation.ErrorInvalidInput, "RESTORE_DOCUMENT_OWNER_CHECK_INVALID", false, errors.New("restore document owner check is invalid"))
	}
	canonicalPath, err := domain.CanonicalizeTargetPath(check.TargetPath)
	if err != nil || canonicalPath != check.TargetPath {
		return foundation.NewError(foundation.ErrorInvalidInput, "RESTORE_DOCUMENT_OWNER_CHECK_INVALID", false, errors.New("restore target path is invalid"))
	}
	var version int64
	var targetPath, lifecycle, currentHash string
	err = repository.db.QueryRow(ctx, `
		SELECT document.version,document.canonical_path,document.lifecycle_status,revision.content_hash
		FROM core.document AS document
		JOIN core.article_revision AS revision
		  ON revision.id=document.current_published_revision_id
		 AND revision.workspace_id=document.workspace_id
		 AND revision.document_id=document.id
		WHERE document.workspace_id=$1 AND document.id=$2`,
		string(check.WorkspaceID), string(check.DocumentID)).Scan(&version, &targetPath, &lifecycle, &currentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return versionConflict("restore document owner no longer has a published revision")
	}
	if err != nil {
		return classify(err, "RESTORE_DOCUMENT_OWNER_CHECK_FAILED")
	}
	if version != check.ExpectedDocumentVersion || targetPath != canonicalPath || lifecycle != string(domain.DocumentPublished) ||
		!strings.EqualFold(currentHash, check.CurrentContentHash) {
		return versionConflict("restore document owner facts changed after approval")
	}
	return nil
}

// FinalizeRestorePublication appends one immutable published Article Revision
// from an exact restore proposal_commit. Replays derive the same Revision ID
// from the durable Writeback identity and therefore never append twice.
func (repository *Repository) FinalizeRestorePublication(ctx context.Context, record authoringapp.RestorePublicationRecord) (bool, error) {
	if err := validateRestorePublicationRecord(record); err != nil {
		return false, err
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, classify(err, "RESTORE_DOCUMENT_PUBLICATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "restore-publication:"+string(record.WritebackID)); err != nil {
		return false, classify(err, "RESTORE_DOCUMENT_PUBLICATION_LOCK_FAILED")
	}

	var proposalType string
	if err := tx.QueryRow(ctx, `SELECT proposal_type FROM change_control.proposal
		WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, string(record.ProposalID), string(record.WorkspaceID)).Scan(&proposalType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, notFound(err)
		}
		return false, classify(err, "RESTORE_DOCUMENT_PROPOSAL_QUERY_FAILED")
	}
	if changecontroldomain.NormalizeProposalType(changecontroldomain.ProposalType(proposalType)) != changecontroldomain.ProposalTypeRestoreDocument {
		return false, commitTransaction(ctx, tx)
	}

	facts, err := loadRestorePublicationFacts(ctx, tx, record)
	if err != nil {
		return false, err
	}
	revisionID, err := restoreArticleRevisionID(record.WritebackID)
	if err != nil {
		return false, err
	}
	if err := ensureRestorePublicationBinding(ctx, tx, facts, revisionID, record); err != nil {
		return false, err
	}
	if existing, found, err := loadExistingRestoreRevision(ctx, tx, facts, revisionID); err != nil {
		return false, err
	} else if found {
		if err := validateExistingRestoreRevision(existing, facts, record); err != nil {
			return false, err
		}
		return false, commitTransaction(ctx, tx)
	}

	document, err := loadDocument(ctx, tx, facts.WorkspaceID, facts.DocumentID, true)
	if err != nil {
		return false, err
	}
	if document.Version != facts.ExpectedDocumentVersion || document.CanonicalPath != facts.TargetPath ||
		document.Lifecycle != domain.DocumentPublished || document.CurrentPublishedRevisionID == "" {
		return false, versionConflict("restore document owner facts changed before publication")
	}
	current, err := loadRevisionByID(ctx, tx, facts.WorkspaceID, facts.DocumentID, document.CurrentPublishedRevisionID, true)
	if err != nil {
		return false, err
	}
	if current.Status != domain.RevisionPublished || !strings.EqualFold(current.ContentHash, facts.CurrentContentHash) {
		return false, versionConflict("restore source revision changed before publication")
	}
	latest, err := loadLatestRevision(ctx, tx, facts.WorkspaceID, facts.DocumentID)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE core.article_revision SET status='SUPERSEDED'
		WHERE id=$1 AND workspace_id=$2 AND document_id=$3 AND status='PUBLISHED'`,
		string(current.ID), string(current.WorkspaceID), string(current.DocumentID))
	if err != nil {
		return false, classify(err, "RESTORE_DOCUMENT_REVISION_SUPERSEDE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return false, inconsistent("restore current published revision lost its lock")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,source_version_id,parent_revision_id,revision_no,
		content,content_hash,status,optimization_mode,git_commit,created_by_type,created_at
	) VALUES($1,$2,$3,NULL,$4,$5,$6,$7,'PUBLISHED','NONE',$8,'SYSTEM',$9)`,
		string(revisionID), string(facts.WorkspaceID), string(facts.DocumentID), string(current.ID), latest.RevisionNo+1,
		facts.Content, facts.TargetContentHash, record.GitCommit, record.PublishedAt.UTC()); err != nil {
		return false, classify(err, "RESTORE_DOCUMENT_REVISION_CREATE_FAILED")
	}
	tag, err = tx.Exec(ctx, `UPDATE core.document
		SET current_published_revision_id=$1,version=version+1,updated_at=GREATEST(updated_at,$2)
		WHERE id=$3 AND workspace_id=$4 AND version=$5 AND canonical_path=$6
		  AND lifecycle_status='PUBLISHED' AND current_published_revision_id=$7`,
		string(revisionID), record.PublishedAt.UTC(), string(document.ID), string(document.WorkspaceID),
		document.Version, document.CanonicalPath, string(current.ID))
	if err != nil {
		return false, classify(err, "RESTORE_DOCUMENT_OWNER_UPDATE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return false, versionConflict("restore document compare-and-swap did not update one row")
	}
	if err := commitTransaction(ctx, tx); err != nil {
		return false, err
	}
	return true, nil
}

type restorePublicationFacts struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	ProposalCommitID        foundation.ID
	TargetPath              string
	Content                 string
	ExpectedDocumentVersion int64
	CurrentContentHash      string
	TargetContentHash       string
}

func loadRestorePublicationFacts(ctx context.Context, tx pgx.Tx, record authoringapp.RestorePublicationRecord) (restorePublicationFacts, error) {
	var facts restorePublicationFacts
	var proposalStatus, baseHash, schemaVersion, commitGit, commitHash, commitPath, commitMode string
	err := tx.QueryRow(ctx, `
		SELECT proposal.status,revision.restore_workspace_id::text,revision.restore_document_id::text,
		       revision.target_path,revision.content,revision.base_hash,revision.restore_expected_document_version,
		       revision.restore_current_content_hash,revision.restore_target_content_hash,revision.schema_version,
		       commit_mapping.id::text,commit_mapping.git_commit,commit_mapping.result_hash,
		       commit_mapping.target_path,commit_mapping.target_mode
		FROM change_control.proposal AS proposal
		JOIN change_control.proposal_revision AS revision
		  ON revision.proposal_id=proposal.id AND revision.id=$3
		JOIN change_control.proposal_commit AS commit_mapping
		  ON commit_mapping.proposal_id=proposal.id
		 AND commit_mapping.revision_id=revision.id
		 AND commit_mapping.writeback_execution_id=$4
		WHERE proposal.id=$1 AND proposal.workspace_id=$2`,
		string(record.ProposalID), string(record.WorkspaceID), string(record.ProposalRevisionID), string(record.WritebackID)).Scan(
		&proposalStatus, &facts.WorkspaceID, &facts.DocumentID, &facts.TargetPath, &facts.Content, &baseHash,
		&facts.ExpectedDocumentVersion, &facts.CurrentContentHash, &facts.TargetContentHash, &schemaVersion,
		&facts.ProposalCommitID, &commitGit, &commitHash, &commitPath, &commitMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return restorePublicationFacts{}, inconsistent("restore proposal_commit binding is missing")
	}
	if err != nil {
		return restorePublicationFacts{}, classify(err, "RESTORE_DOCUMENT_PUBLICATION_QUERY_FAILED")
	}
	if proposalStatus != string(changecontroldomain.StatusVerifying) && proposalStatus != string(changecontroldomain.StatusCompleted) ||
		facts.WorkspaceID != record.WorkspaceID || !validID(facts.DocumentID) ||
		schemaVersion != changecontroldomain.RestoreDocumentSchemaVersion || facts.ExpectedDocumentVersion < 1 ||
		baseHash != facts.CurrentContentHash || commitPath != facts.TargetPath || commitMode != string(changecontroldomain.TargetModeReplace) ||
		!strings.EqualFold(commitGit, record.GitCommit) || !strings.EqualFold(commitHash, record.ResultHash) ||
		!strings.EqualFold(facts.TargetContentHash, record.ResultHash) ||
		!strings.EqualFold(domain.ComputeContentHash(facts.Content), facts.TargetContentHash) {
		return restorePublicationFacts{}, inconsistent("restore proposal_commit facts do not match the publication")
	}
	canonicalPath, pathErr := domain.CanonicalizeTargetPath(facts.TargetPath)
	if pathErr != nil || canonicalPath != facts.TargetPath || !validID(facts.ProposalCommitID) ||
		!validHash(facts.CurrentContentHash) || !validHash(facts.TargetContentHash) {
		return restorePublicationFacts{}, inconsistent("restore publication facts are invalid")
	}
	return facts, nil
}

func ensureRestorePublicationBinding(ctx context.Context, tx pgx.Tx, facts restorePublicationFacts, revisionID foundation.ID, record authoringapp.RestorePublicationRecord) error {
	if _, err := tx.Exec(ctx, `INSERT INTO authoring.document_restore_publication(
		writeback_execution_id,workspace_id,document_id,article_revision_id,proposal_id,
		proposal_revision_id,proposal_commit_id,expected_document_version,target_path,
		current_content_hash,target_content_hash,git_commit,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	ON CONFLICT(writeback_execution_id) DO NOTHING`,
		string(record.WritebackID), string(facts.WorkspaceID), string(facts.DocumentID), string(revisionID),
		string(record.ProposalID), string(record.ProposalRevisionID), string(facts.ProposalCommitID),
		facts.ExpectedDocumentVersion, facts.TargetPath, facts.CurrentContentHash, facts.TargetContentHash,
		record.GitCommit, record.PublishedAt.UTC()); err != nil {
		return classify(err, "RESTORE_DOCUMENT_PUBLICATION_BIND_FAILED")
	}
	var exact bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM authoring.document_restore_publication
		WHERE writeback_execution_id=$1 AND workspace_id=$2 AND document_id=$3
		  AND article_revision_id=$4 AND proposal_id=$5 AND proposal_revision_id=$6
		  AND proposal_commit_id=$7 AND expected_document_version=$8 AND target_path=$9
		  AND current_content_hash=$10 AND target_content_hash=$11 AND git_commit=$12
	)`, string(record.WritebackID), string(facts.WorkspaceID), string(facts.DocumentID), string(revisionID),
		string(record.ProposalID), string(record.ProposalRevisionID), string(facts.ProposalCommitID),
		facts.ExpectedDocumentVersion, facts.TargetPath, facts.CurrentContentHash, facts.TargetContentHash,
		record.GitCommit).Scan(&exact); err != nil {
		return classify(err, "RESTORE_DOCUMENT_PUBLICATION_BIND_QUERY_FAILED")
	}
	if !exact {
		return inconsistent("restore publication replay binding changed")
	}
	return nil
}

func loadExistingRestoreRevision(ctx context.Context, tx pgx.Tx, facts restorePublicationFacts, revisionID foundation.ID) (domain.ArticleRevision, bool, error) {
	revision, err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionColumns+`
		FROM core.article_revision WHERE id=$1 AND workspace_id=$2 AND document_id=$3 FOR UPDATE`,
		string(revisionID), string(facts.WorkspaceID), string(facts.DocumentID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ArticleRevision{}, false, nil
	}
	if err != nil {
		return domain.ArticleRevision{}, false, classify(err, "RESTORE_DOCUMENT_REVISION_QUERY_FAILED")
	}
	return revision, true, nil
}

func validateExistingRestoreRevision(revision domain.ArticleRevision, facts restorePublicationFacts, record authoringapp.RestorePublicationRecord) error {
	if revision.WorkspaceID != facts.WorkspaceID || revision.DocumentID != facts.DocumentID || revision.ParentRevisionID == "" ||
		(revision.Status != domain.RevisionPublished && revision.Status != domain.RevisionSuperseded) ||
		revision.Content != facts.Content || !strings.EqualFold(revision.ContentHash, record.ResultHash) ||
		!strings.EqualFold(revision.GitCommit, record.GitCommit) || revision.CreatedByType != "SYSTEM" {
		return inconsistent("stored restore Article Revision does not match its proposal_commit")
	}
	return nil
}

func validateRestorePublicationRecord(record authoringapp.RestorePublicationRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.ProposalID) || !validID(record.ProposalRevisionID) ||
		!validID(record.WritebackID) || !changecontroldomain.ValidGitObjectID(record.GitCommit) ||
		!validHash(record.ResultHash) || record.PublishedAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, "RESTORE_DOCUMENT_PUBLICATION_INVALID", false, errors.New("restore publication record is invalid"))
	}
	return nil
}

func restoreArticleRevisionID(writebackID foundation.ID) (foundation.ID, error) {
	parsed, err := foundation.ParseID(string(writebackID))
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "RESTORE_DOCUMENT_REVISION_ID_FAILED", false, err)
	}
	namespace, err := hex.DecodeString(strings.ReplaceAll(string(parsed), "-", ""))
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "RESTORE_DOCUMENT_REVISION_ID_FAILED", false, err)
	}
	sum := sha1.Sum(append(namespace, []byte(restoreArticleRevisionLabel)...)) // #nosec G401 -- UUID v5 requires SHA-1.
	raw := sum[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	return foundation.ParseID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
}
