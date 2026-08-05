// Package domain defines the bounded Document file-history read model.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// WorktreeRef identifies the current working tree in compare requests.
	WorktreeRef = "WORKTREE"
	// MaxHistoryLimit is the maximum number of commits returned by one page.
	MaxHistoryLimit = 50
	// MaxHistoryOffset keeps cursor-backed Git scans bounded.
	MaxHistoryOffset = 5000
	// MaxCommitParents matches the public history response contract.
	MaxCommitParents = 64
	// MaxDiffBytes is the maximum complete diff exposed by History APIs.
	MaxDiffBytes = 2 * 1024 * 1024
	// MaxBlobBytes matches the governed single-file writeback limit.
	MaxBlobBytes = 10 * 1024 * 1024
)

// EntryKind distinguishes governed commits, external commits and current changes.
type EntryKind string

const (
	EntryManaged       EntryKind = "MANAGED"
	EntryExternal      EntryKind = "EXTERNAL"
	EntryCurrentChange EntryKind = "CURRENT_CHANGE"
)

// RepositoryState is the immutable Git baseline used by one History response.
type RepositoryState struct {
	WorkspaceID  foundation.ID
	Branch       string
	Head         string
	ObjectFormat string
	Dirty        bool
	PathDirty    bool
}

// Commit is one current-branch commit that changes the canonical Document path.
type Commit struct {
	ID          string
	ParentIDs   []string
	AuthorName  string
	AuthorEmail string
	CommittedAt time.Time
	Subject     string
}

// CommitPage is a bounded Git history page plus the exact baseline used to read it.
type CommitPage struct {
	State   RepositoryState
	Commits []Commit
	HasMore bool
}

// CommitMapping enriches one Git commit from existing Authoring and Change Control facts.
type CommitMapping struct {
	GitCommit          string
	ArticleRevisionID  foundation.ID
	ArticleRevisionNo  int
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	ApprovalID         foundation.ID
	WorkflowRunID      foundation.ID
	WritebackID        foundation.ID
	ProposalType       string
	ApprovalDecidedAt  *time.Time
}

// Entry is one discriminated item in the Document file timeline.
type Entry struct {
	Kind               EntryKind
	Commit             string
	ParentCommits      []string
	AuthorName         string
	AuthorEmail        string
	CommittedAt        time.Time
	Summary            string
	ArticleRevisionID  foundation.ID
	ArticleRevisionNo  int
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	ApprovalID         foundation.ID
	WorkflowRunID      foundation.ID
	WritebackID        foundation.ID
	ProposalType       string
	ApprovalDecidedAt  *time.Time
}

// Page is a stable Document-scoped History response.
type Page struct {
	WorkspaceID     foundation.ID
	DocumentID      foundation.ID
	DocumentVersion int64
	Path            string
	Branch          string
	Head            string
	Dirty           bool
	Items           []Entry
	NextCursor      string
}

// Diff compares two verified versions of the current canonical path.
type Diff struct {
	WorkspaceID  foundation.ID
	DocumentID   foundation.ID
	Path         string
	Head         string
	Left         string
	Right        string
	LeftContent  string
	RightContent string
	Patch        string
	DiffHash     string
}

// Blob is exact content read from a verified current-branch commit.
type Blob struct {
	Commit      string
	Path        string
	Content     []byte
	ContentHash string
}

// RestorePreview freezes the server-derived reverse change before Proposal creation.
type RestorePreview struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	Path                    string
	TargetCommit            string
	ExpectedHead            string
	ExpectedDocumentVersion int64
	CurrentContentHash      string
	TargetContentHash       string
	CurrentContent          string
	TargetContent           string
	Patch                   string
	DiffHash                string
	PreviewHash             string
	BlockedByDirtyWorktree  bool
}

// ComputeHash returns the lower-case SHA-256 of exact bytes.
func ComputeHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// ComputePreviewHash binds a restore preview to its complete server-owned identity.
func ComputePreviewHash(preview RestorePreview) (string, error) {
	if !ValidID(preview.WorkspaceID) || !ValidID(preview.DocumentID) || !ValidPath(preview.Path) ||
		!ValidObjectID(preview.TargetCommit) || !ValidObjectID(preview.ExpectedHead) ||
		preview.ExpectedDocumentVersion < 1 || !ValidHash(preview.CurrentContentHash) ||
		!ValidHash(preview.TargetContentHash) || !ValidHash(preview.DiffHash) {
		return "", errors.New("restore preview binding is invalid")
	}
	payload := struct {
		Schema          string        `json:"schema"`
		WorkspaceID     foundation.ID `json:"workspace_id"`
		DocumentID      foundation.ID `json:"document_id"`
		Path            string        `json:"path"`
		TargetCommit    string        `json:"target_commit"`
		ExpectedHead    string        `json:"expected_head"`
		DocumentVersion int64         `json:"document_version"`
		CurrentHash     string        `json:"current_hash"`
		TargetHash      string        `json:"target_hash"`
		DiffHash        string        `json:"diff_hash"`
	}{
		Schema: "document-restore-preview/v1", WorkspaceID: preview.WorkspaceID,
		DocumentID: preview.DocumentID, Path: preview.Path, TargetCommit: preview.TargetCommit,
		ExpectedHead: preview.ExpectedHead, DocumentVersion: preview.ExpectedDocumentVersion,
		CurrentHash: preview.CurrentContentHash, TargetHash: preview.TargetContentHash, DiffHash: preview.DiffHash,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return ComputeHash(encoded), nil
}

// ValidID reports whether value is one canonical project UUID.
func ValidID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

// ValidHash reports whether value is a canonical SHA-256 hex digest.
func ValidHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ValidObjectID accepts canonical SHA-1 or SHA-256 Git object IDs.
func ValidObjectID(value string) bool {
	if (len(value) != 40 && len(value) != 64) || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ValidPath validates the already-canonical relative POSIX path shape.
func ValidPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > 4096 ||
		strings.HasPrefix(value, "/") || strings.Contains(value, "\\") ||
		strings.ContainsAny(value, "\x00\r\n") || path.Clean(value) != value ||
		!strings.EqualFold(path.Ext(value), ".md") {
		return false
	}
	first, _, _ := strings.Cut(value, "/")
	return !strings.EqualFold(first, ".git") && !strings.EqualFold(first, ".knowledge")
}

// ValidVersionRef accepts WORKTREE or one canonical Git object ID.
func ValidVersionRef(value string) bool { return value == WorktreeRef || ValidObjectID(value) }
