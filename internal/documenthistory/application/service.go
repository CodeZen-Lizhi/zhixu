// Package application coordinates Git history with Authoring and Change Control facts.
package application

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Dependencies are the explicit owners used by Document History.
type Dependencies struct {
	Documents DocumentReader
	Git       GitHistoryReader
	Proposals RestoreProposalCreator
	Lookup    RestoreProposalLookup
	Cursors   *CursorCodec
}

// Service exposes bounded history, comparison and governed restore commands.
type Service struct{ dependencies Dependencies }

// NewService validates and freezes all cross-owner dependencies.
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Documents == nil || dependencies.Git == nil || dependencies.Proposals == nil || dependencies.Lookup == nil || dependencies.Cursors == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeGitUnavailable, false, errors.New("document history dependencies are incomplete"))
	}
	return &Service{dependencies: dependencies}, nil
}

// ListHistory returns the current-change marker and a stable page of path commits.
func (service *Service) ListHistory(ctx context.Context, query HistoryQuery) (domain.Page, error) {
	if err := validateContext(ctx); err != nil {
		return domain.Page{}, err
	}
	if !domain.ValidID(query.WorkspaceID) || !domain.ValidID(query.DocumentID) || query.Limit < 1 || query.Limit > domain.MaxHistoryLimit {
		return domain.Page{}, invalid("history query is invalid")
	}
	document, err := service.dependencies.Documents.GetDocument(ctx, query.WorkspaceID, query.DocumentID)
	if err != nil {
		return domain.Page{}, err
	}
	if err := validateDocument(document, query.WorkspaceID, query.DocumentID); err != nil {
		return domain.Page{}, err
	}
	state, err := service.dependencies.Git.Inspect(ctx, query.WorkspaceID, document.CanonicalPath)
	if err != nil {
		return domain.Page{}, err
	}
	if err := validateState(state, query.WorkspaceID); err != nil {
		return domain.Page{}, err
	}
	offset := 0
	var cursor cursorPayload
	if query.Cursor != "" {
		cursor, err = service.dependencies.Cursors.decode(query.Cursor)
		if err != nil {
			return domain.Page{}, err
		}
		if cursor.WorkspaceID != query.WorkspaceID || cursor.DocumentID != query.DocumentID || cursor.Path != document.CanonicalPath ||
			cursor.Branch != state.Branch || cursor.Head != state.Head || cursor.Limit != query.Limit {
			return domain.Page{}, conflict(ErrorCodeCursorStale, "history baseline changed")
		}
		offset = cursor.Offset
	}
	gitPage, err := service.dependencies.Git.List(ctx, query.WorkspaceID, document.CanonicalPath, offset, query.Limit)
	if err != nil {
		return domain.Page{}, err
	}
	if err := validateCommitPage(gitPage, state, query.Limit); err != nil {
		return domain.Page{}, err
	}
	if query.Cursor != "" {
		previous, previousErr := service.dependencies.Git.List(ctx, query.WorkspaceID, document.CanonicalPath, offset-1, 1)
		if previousErr != nil {
			return domain.Page{}, previousErr
		}
		if len(previous.Commits) != 1 || previous.Commits[0].ID != cursor.LastCommit || previous.State.Head != state.Head {
			return domain.Page{}, conflict(ErrorCodeCursorStale, "history cursor position changed")
		}
	}
	commitIDs := make([]string, len(gitPage.Commits))
	for index, commit := range gitPage.Commits {
		commitIDs[index] = commit.ID
	}
	mappings, err := service.dependencies.Documents.MapCommits(ctx, query.WorkspaceID, query.DocumentID, document.CanonicalPath, commitIDs)
	if err != nil {
		return domain.Page{}, err
	}
	mappingByCommit, err := validateMappings(mappings, commitIDs)
	if err != nil {
		return domain.Page{}, err
	}
	items := make([]domain.Entry, 0, len(gitPage.Commits)+1)
	if offset == 0 && state.PathDirty {
		items = append(items, domain.Entry{Kind: domain.EntryCurrentChange, Commit: domain.WorktreeRef, ParentCommits: []string{state.Head}, Summary: "当前未提交改动"})
	}
	for _, commit := range gitPage.Commits {
		mapping, managed := mappingByCommit[commit.ID]
		kind := domain.EntryExternal
		if managed {
			kind = domain.EntryManaged
		}
		items = append(items, domain.Entry{
			Kind: kind, Commit: commit.ID, ParentCommits: append([]string(nil), commit.ParentIDs...),
			AuthorName: commit.AuthorName, AuthorEmail: commit.AuthorEmail, CommittedAt: commit.CommittedAt,
			Summary: commit.Subject, ArticleRevisionID: mapping.ArticleRevisionID, ArticleRevisionNo: mapping.ArticleRevisionNo,
			ProposalID: mapping.ProposalID, ProposalRevisionID: mapping.ProposalRevisionID, ApprovalID: mapping.ApprovalID,
			WorkflowRunID: mapping.WorkflowRunID, WritebackID: mapping.WritebackID, ProposalType: mapping.ProposalType,
			ApprovalDecidedAt: mapping.ApprovalDecidedAt,
		})
	}
	page := domain.Page{WorkspaceID: query.WorkspaceID, DocumentID: query.DocumentID, DocumentVersion: document.Version, Path: document.CanonicalPath, Branch: state.Branch, Head: state.Head, Dirty: state.Dirty, Items: items}
	if gitPage.HasMore && len(gitPage.Commits) > 0 {
		last := gitPage.Commits[len(gitPage.Commits)-1]
		if offset+len(gitPage.Commits) > domain.MaxHistoryOffset {
			return domain.Page{}, foundation.NewError(foundation.ErrorPermissionDenied, ErrorCodeOutputTooLarge, false, errors.New("history exceeds the bounded cursor window"))
		}
		page.NextCursor, err = service.dependencies.Cursors.encode(cursorPayload{
			Version: cursorVersion, WorkspaceID: query.WorkspaceID, DocumentID: query.DocumentID, Path: document.CanonicalPath,
			Branch: state.Branch, Head: state.Head, Limit: query.Limit, Offset: offset + len(gitPage.Commits), LastCommit: last.ID,
		})
		if err != nil {
			return domain.Page{}, err
		}
	}
	return page, nil
}

// CompareVersions returns one complete, bounded diff with explicit left/right identities.
func (service *Service) CompareVersions(ctx context.Context, query CompareQuery) (domain.Diff, error) {
	if err := validateContext(ctx); err != nil {
		return domain.Diff{}, err
	}
	if !domain.ValidID(query.WorkspaceID) || !domain.ValidID(query.DocumentID) || !domain.ValidVersionRef(query.Left) || !domain.ValidVersionRef(query.Right) || query.Left == query.Right {
		return domain.Diff{}, invalid("compare query is invalid")
	}
	document, err := service.dependencies.Documents.GetDocument(ctx, query.WorkspaceID, query.DocumentID)
	if err != nil {
		return domain.Diff{}, err
	}
	if err := validateDocument(document, query.WorkspaceID, query.DocumentID); err != nil {
		return domain.Diff{}, err
	}
	diff, err := service.dependencies.Git.Compare(ctx, query.WorkspaceID, document.CanonicalPath, query.Left, query.Right)
	if err != nil {
		return domain.Diff{}, err
	}
	if diff.WorkspaceID != query.WorkspaceID || diff.Path != document.CanonicalPath || diff.Left != query.Left || diff.Right != query.Right ||
		!domain.ValidObjectID(diff.Head) || !domain.ValidHash(diff.DiffHash) || diff.DiffHash != domain.ComputeHash([]byte(diff.Patch)) ||
		len(diff.Patch) > domain.MaxDiffBytes || !utf8.ValidString(diff.Patch) || strings.IndexByte(diff.Patch, 0) >= 0 ||
		len(diff.LeftContent) > domain.MaxBlobBytes || len(diff.RightContent) > domain.MaxBlobBytes ||
		!utf8.ValidString(diff.LeftContent) || !utf8.ValidString(diff.RightContent) ||
		strings.IndexByte(diff.LeftContent, 0) >= 0 || strings.IndexByte(diff.RightContent, 0) >= 0 {
		return domain.Diff{}, inconsistent("git compare result is invalid")
	}
	diff.DocumentID = query.DocumentID
	return diff, nil
}

// PreviewRestore computes the reverse diff from current HEAD to a target commit.
func (service *Service) PreviewRestore(ctx context.Context, command PreviewCommand) (domain.RestorePreview, error) {
	if err := validateContext(ctx); err != nil {
		return domain.RestorePreview{}, err
	}
	if !domain.ValidID(command.WorkspaceID) || !domain.ValidID(command.DocumentID) || !domain.ValidObjectID(command.TargetCommit) || command.ExpectedDocumentVersion < 1 {
		return domain.RestorePreview{}, invalid("restore preview command is invalid")
	}
	document, err := service.dependencies.Documents.GetDocument(ctx, command.WorkspaceID, command.DocumentID)
	if err != nil {
		return domain.RestorePreview{}, err
	}
	if err := validateDocument(document, command.WorkspaceID, command.DocumentID); err != nil {
		return domain.RestorePreview{}, err
	}
	if document.Version != command.ExpectedDocumentVersion {
		return domain.RestorePreview{}, conflict(ErrorCodeRestoreStale, "document version changed before restore preview")
	}
	state, err := service.dependencies.Git.Inspect(ctx, command.WorkspaceID, document.CanonicalPath)
	if err != nil {
		return domain.RestorePreview{}, err
	}
	if err := validateState(state, command.WorkspaceID); err != nil {
		return domain.RestorePreview{}, err
	}
	if command.TargetCommit == state.Head {
		return domain.RestorePreview{}, conflict(ErrorCodeRestoreStale, "target commit already equals current HEAD")
	}
	currentBlob, err := service.dependencies.Git.ReadBlob(ctx, command.WorkspaceID, state.Head, document.CanonicalPath)
	if err != nil {
		return domain.RestorePreview{}, err
	}
	targetBlob, err := service.dependencies.Git.ReadBlob(ctx, command.WorkspaceID, command.TargetCommit, document.CanonicalPath)
	if err != nil {
		return domain.RestorePreview{}, err
	}
	diff, err := service.dependencies.Git.Compare(ctx, command.WorkspaceID, document.CanonicalPath, state.Head, command.TargetCommit)
	if err != nil {
		return domain.RestorePreview{}, err
	}
	if currentBlob.Commit != state.Head || currentBlob.Path != document.CanonicalPath || len(currentBlob.Content) > domain.MaxBlobBytes ||
		!utf8.Valid(currentBlob.Content) || bytes.IndexByte(currentBlob.Content, 0) >= 0 || currentBlob.ContentHash != domain.ComputeHash(currentBlob.Content) ||
		targetBlob.Commit != command.TargetCommit || targetBlob.Path != document.CanonicalPath || len(targetBlob.Content) > domain.MaxBlobBytes ||
		!utf8.Valid(targetBlob.Content) || bytes.IndexByte(targetBlob.Content, 0) >= 0 || targetBlob.ContentHash != domain.ComputeHash(targetBlob.Content) ||
		diff.WorkspaceID != command.WorkspaceID || diff.Path != document.CanonicalPath || diff.Head != state.Head ||
		diff.Left != state.Head || diff.Right != command.TargetCommit || diff.LeftContent != string(currentBlob.Content) ||
		diff.RightContent != string(targetBlob.Content) || diff.DiffHash != domain.ComputeHash([]byte(diff.Patch)) ||
		len(diff.Patch) > domain.MaxDiffBytes || !utf8.ValidString(diff.Patch) || strings.IndexByte(diff.Patch, 0) >= 0 {
		return domain.RestorePreview{}, inconsistent("restore preview inputs are inconsistent")
	}
	if currentBlob.ContentHash == targetBlob.ContentHash {
		return domain.RestorePreview{}, conflict(ErrorCodeRestoreStale, "target commit has the same document content as current HEAD")
	}
	preview := domain.RestorePreview{
		WorkspaceID: command.WorkspaceID, DocumentID: command.DocumentID, Path: document.CanonicalPath,
		TargetCommit: command.TargetCommit, ExpectedHead: state.Head, ExpectedDocumentVersion: document.Version,
		CurrentContentHash: currentBlob.ContentHash, TargetContentHash: targetBlob.ContentHash,
		CurrentContent: string(currentBlob.Content), TargetContent: string(targetBlob.Content),
		Patch: diff.Patch, DiffHash: diff.DiffHash, BlockedByDirtyWorktree: state.Dirty,
	}
	preview.PreviewHash, err = domain.ComputePreviewHash(preview)
	if err != nil {
		return domain.RestorePreview{}, inconsistent("restore preview cannot be reproduced")
	}
	return preview, nil
}

// CreateRestoreProposal replays first, then re-verifies all mutable Git and Document facts.
func (service *Service) CreateRestoreProposal(ctx context.Context, command RestoreCommand) (RestoreProposalReceipt, error) {
	if err := validateContext(ctx); err != nil {
		return RestoreProposalReceipt{}, err
	}
	key, err := validateIdempotencyKey(command.IdempotencyKey)
	if err != nil || !domain.ValidID(command.WorkspaceID) || !domain.ValidID(command.DocumentID) ||
		!domain.ValidObjectID(command.TargetCommit) || !domain.ValidObjectID(command.ExpectedHead) ||
		command.ExpectedDocumentVersion < 1 || !domain.ValidHash(command.PreviewHash) {
		return RestoreProposalReceipt{}, invalid("restore proposal command is invalid")
	}
	if existing, found, lookupErr := service.dependencies.Lookup.FindRestoreProposal(ctx, command.WorkspaceID, key); lookupErr != nil {
		return RestoreProposalReceipt{}, lookupErr
	} else if found {
		if !sameRestoreRequest(existing, command) {
			return RestoreProposalReceipt{}, conflict(ErrorCodeIdempotencyReuse, "idempotency key is bound to another restore request")
		}
		if !validRestoreReceipt(existing, true) {
			return RestoreProposalReceipt{}, inconsistent("restore proposal replay result is invalid")
		}
		existing.Replayed = true
		return existing, nil
	}
	preview, err := service.PreviewRestore(ctx, PreviewCommand{WorkspaceID: command.WorkspaceID, DocumentID: command.DocumentID, TargetCommit: command.TargetCommit, ExpectedDocumentVersion: command.ExpectedDocumentVersion})
	if err != nil {
		return RestoreProposalReceipt{}, err
	}
	if preview.BlockedByDirtyWorktree {
		return RestoreProposalReceipt{}, conflict(ErrorCodeDirty, "restore cannot overwrite a dirty working tree")
	}
	if preview.ExpectedHead != command.ExpectedHead || preview.ExpectedDocumentVersion != command.ExpectedDocumentVersion || preview.PreviewHash != command.PreviewHash {
		return RestoreProposalReceipt{}, conflict(ErrorCodeRestoreStale, "restore preview is stale")
	}
	targetBlob, err := service.dependencies.Git.ReadBlob(ctx, command.WorkspaceID, command.TargetCommit, preview.Path)
	if err != nil {
		return RestoreProposalReceipt{}, err
	}
	if targetBlob.Commit != command.TargetCommit || targetBlob.Path != preview.Path ||
		len(targetBlob.Content) > domain.MaxBlobBytes || !utf8.Valid(targetBlob.Content) || bytes.IndexByte(targetBlob.Content, 0) >= 0 ||
		targetBlob.ContentHash != preview.TargetContentHash || targetBlob.ContentHash != domain.ComputeHash(targetBlob.Content) {
		return RestoreProposalReceipt{}, inconsistent("restore target changed before proposal creation")
	}
	receipt, err := service.dependencies.Proposals.CreateRestoreDocumentProposal(ctx, CreateRestoreProposalRecord{
		WorkspaceID: command.WorkspaceID, DocumentID: command.DocumentID, TargetPath: preview.Path,
		TargetCommit: command.TargetCommit, ExpectedHead: command.ExpectedHead,
		ExpectedDocumentVersion: command.ExpectedDocumentVersion, PreviewHash: command.PreviewHash,
		CurrentContentHash: preview.CurrentContentHash, TargetContentHash: preview.TargetContentHash,
		TargetContent: string(targetBlob.Content), IdempotencyKey: key,
	})
	if err != nil {
		return RestoreProposalReceipt{}, err
	}
	if !sameRestoreRequest(receipt, command) || !validRestoreReceipt(receipt, receipt.Replayed) {
		return RestoreProposalReceipt{}, inconsistent("restore proposal result is invalid")
	}
	return receipt, nil
}

func validateDocument(document DocumentSnapshot, workspaceID, documentID foundation.ID) error {
	if document.ID != documentID || document.WorkspaceID != workspaceID || !domain.ValidID(document.ID) ||
		!domain.ValidPath(document.CanonicalPath) || document.Version < 1 || document.Lifecycle != "PUBLISHED" ||
		!domain.ValidID(document.CurrentPublishedRevisionID) {
		return inconsistent("document binding is invalid")
	}
	return nil
}

func validateState(state domain.RepositoryState, workspaceID foundation.ID) error {
	if state.WorkspaceID != workspaceID || state.Branch == "" || len(state.Branch) > 512 || strings.ContainsFunc(state.Branch, unicode.IsControl) || !domain.ValidObjectID(state.Head) ||
		(state.ObjectFormat != "sha1" && state.ObjectFormat != "sha256") ||
		(state.ObjectFormat == "sha1" && len(state.Head) != 40) || (state.ObjectFormat == "sha256" && len(state.Head) != 64) {
		return inconsistent("git repository state is invalid")
	}
	return nil
}

func validateCommitPage(page domain.CommitPage, state domain.RepositoryState, limit int) error {
	if page.State != state || len(page.Commits) > limit {
		return inconsistent("git history page baseline is invalid")
	}
	seen := make(map[string]struct{}, len(page.Commits))
	for _, commit := range page.Commits {
		if !domain.ValidObjectID(commit.ID) || commit.CommittedAt.IsZero() || commit.Subject == "" || len(commit.Subject) > 4096 {
			return inconsistent("git history commit is invalid")
		}
		if _, duplicate := seen[commit.ID]; duplicate {
			return inconsistent("git history contains duplicate commits")
		}
		seen[commit.ID] = struct{}{}
		if len(commit.ParentIDs) > domain.MaxCommitParents {
			return inconsistent("git history parent list is too large")
		}
		for _, parent := range commit.ParentIDs {
			if !domain.ValidObjectID(parent) {
				return inconsistent("git history parent is invalid")
			}
		}
	}
	return nil
}

func validateMappings(mappings []domain.CommitMapping, requested []string) (map[string]domain.CommitMapping, error) {
	allowed := make(map[string]struct{}, len(requested))
	for _, commit := range requested {
		allowed[commit] = struct{}{}
	}
	result := make(map[string]domain.CommitMapping, len(mappings))
	for _, mapping := range mappings {
		if _, ok := allowed[mapping.GitCommit]; !ok || !domain.ValidObjectID(mapping.GitCommit) {
			return nil, inconsistent("commit mapping escapes the requested page")
		}
		if _, duplicate := result[mapping.GitCommit]; duplicate {
			return nil, inconsistent("commit mapping is ambiguous")
		}
		if mapping.ArticleRevisionID != "" && (!domain.ValidID(mapping.ArticleRevisionID) || mapping.ArticleRevisionNo < 1) {
			return nil, inconsistent("article revision mapping is invalid")
		}
		if mapping.ArticleRevisionID == "" && mapping.ArticleRevisionNo != 0 {
			return nil, inconsistent("article revision mapping contains residual fields")
		}
		if mapping.ProposalID != "" && (!domain.ValidID(mapping.ProposalID) ||
			mapping.ProposalRevisionID != "" && !domain.ValidID(mapping.ProposalRevisionID) ||
			mapping.ApprovalID != "" && !domain.ValidID(mapping.ApprovalID) ||
			mapping.WorkflowRunID != "" && !domain.ValidID(mapping.WorkflowRunID) ||
			mapping.WritebackID != "" && !domain.ValidID(mapping.WritebackID) ||
			mapping.ProposalType != "" && mapping.ProposalType != "file_patch" && mapping.ProposalType != "restore_document" ||
			mapping.ApprovalDecidedAt != nil && mapping.ApprovalID == "") {
			return nil, inconsistent("writeback commit mapping is invalid")
		}
		if mapping.ProposalID == "" && (mapping.ProposalRevisionID != "" || mapping.ApprovalID != "" || mapping.WorkflowRunID != "" || mapping.WritebackID != "" || mapping.ProposalType != "" || mapping.ApprovalDecidedAt != nil) {
			return nil, inconsistent("writeback commit mapping contains residual fields")
		}
		result[mapping.GitCommit] = mapping
	}
	return result, nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return invalid("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "DOCUMENT_HISTORY_CONTEXT_DONE", false, err)
	}
	return nil
}

func validateIdempotencyKey(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 || !utf8.ValidString(value) {
		return "", invalid("Idempotency-Key is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", invalid("Idempotency-Key is invalid")
		}
	}
	return value, nil
}

func sameRestoreRequest(receipt RestoreProposalReceipt, command RestoreCommand) bool {
	return receipt.WorkspaceID == command.WorkspaceID && receipt.DocumentID == command.DocumentID &&
		receipt.TargetCommit == command.TargetCommit && receipt.ExpectedHead == command.ExpectedHead &&
		receipt.ExpectedDocumentVersion == command.ExpectedDocumentVersion && receipt.PreviewHash == command.PreviewHash
}

func validRestoreReceipt(receipt RestoreProposalReceipt, replayed bool) bool {
	if !domain.ValidID(receipt.WorkspaceID) || !domain.ValidID(receipt.DocumentID) ||
		!domain.ValidID(receipt.ProposalID) || !domain.ValidID(receipt.ProposalRevisionID) ||
		receipt.ProposalType != "restore_document" || !domain.ValidHash(receipt.ChangeHash) {
		return false
	}
	if !replayed {
		return receipt.Status == "ready_for_review"
	}
	switch receipt.Status {
	case "draft", "validating", "ready_for_review", "approved", "applying", "applied", "verifying", "completed",
		"rejected", "needs_revision", "deferred", "apply_failed", "verify_failed", "rolled_back", "cancelled":
		return true
	default:
		return false
	}
}
