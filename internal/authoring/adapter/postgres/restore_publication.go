package postgres

import (
	"crypto/sha1" // #nosec G505 -- UUID v5 requires SHA-1; it is not used for security.
	"encoding/hex"
	"errors"
	"fmt"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"strings"
)

const restoreArticleRevisionLabel = "restore-article-revision"

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
