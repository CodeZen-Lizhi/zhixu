package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	historyWorkspaceID = foundation.ID("30000000-0000-4000-8000-000000000001")
	historyDocumentID  = foundation.ID("30000000-0000-4000-8000-000000000002")
	historyRevisionID  = foundation.ID("30000000-0000-4000-8000-000000000003")
	historyProposalID  = foundation.ID("30000000-0000-4000-8000-000000000004")
	historyProposalRev = foundation.ID("30000000-0000-4000-8000-000000000005")
	historyHead        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	historyTarget      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	historyOlder       = "cccccccccccccccccccccccccccccccccccccccc"
)

func TestListHistoryProjectsCurrentManagedPartialAndExternalEntries(t *testing.T) {
	state := historyState(true, true)
	commits := []domain.Commit{historyCommit(historyHead, "current"), historyCommit(historyTarget, "older")}
	documents := &historyDocumentReaderStub{
		document: historyDocument(),
		mappings: []domain.CommitMapping{{
			GitCommit: historyHead, ArticleRevisionID: historyRevisionID, ArticleRevisionNo: 4,
			ProposalID: historyProposalID, ProposalRevisionID: historyProposalRev,
			ProposalType: "restore_document",
		}},
	}
	git := &historyGitStub{state: state, page: domain.CommitPage{State: state, Commits: commits, HasMore: true}}
	service := newHistoryTestService(t, documents, git, &historyProposalStub{}, &historyLookupStub{})

	page, err := service.ListHistory(context.Background(), HistoryQuery{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if page.Head != historyHead || !page.Dirty || page.NextCursor == "" || len(page.Items) != 3 {
		t.Fatalf("page=%+v", page)
	}
	if current := page.Items[0]; current.Kind != domain.EntryCurrentChange || current.Commit != domain.WorktreeRef || len(current.ParentCommits) != 1 || current.ParentCommits[0] != historyHead {
		t.Fatalf("current=%+v", current)
	}
	managed := page.Items[1]
	if managed.Kind != domain.EntryManaged || managed.ProposalID != historyProposalID || managed.WorkflowRunID != "" || managed.WritebackID != "" {
		t.Fatalf("managed=%+v", managed)
	}
	if external := page.Items[2]; external.Kind != domain.EntryExternal || external.ProposalID != "" || external.ArticleRevisionID != "" {
		t.Fatalf("external=%+v", external)
	}
	if documents.mapCalls != 1 || len(documents.mappedCommits) != 2 {
		t.Fatalf("mapCalls=%d commits=%v", documents.mapCalls, documents.mappedCommits)
	}
}

func TestListHistoryRejectsStaleCursorBeforeScanningNextPage(t *testing.T) {
	state := historyState(false, false)
	git := &historyGitStub{state: state, page: domain.CommitPage{State: state, Commits: []domain.Commit{historyCommit(historyHead, "current")}, HasMore: true}}
	service := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, &historyProposalStub{}, &historyLookupStub{})
	first, err := service.ListHistory(context.Background(), HistoryQuery{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first=(%+v,%v)", first, err)
	}
	git.state.Head = historyOlder
	git.page.State = git.state
	git.listCalls = 0
	_, err = service.ListHistory(context.Background(), HistoryQuery{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, Limit: 1, Cursor: first.NextCursor})
	assertHistoryErrorCode(t, err, ErrorCodeCursorStale)
	if git.listCalls != 0 {
		t.Fatalf("List called %d times after cursor baseline mismatch", git.listCalls)
	}
}

func TestListHistoryRejectsAmbiguousAndEscapingMappings(t *testing.T) {
	state := historyState(false, false)
	commit := historyCommit(historyHead, "current")
	tests := []struct {
		name     string
		mappings []domain.CommitMapping
	}{
		{name: "duplicate", mappings: []domain.CommitMapping{{GitCommit: historyHead, ArticleRevisionID: historyRevisionID, ArticleRevisionNo: 1}, {GitCommit: historyHead, ProposalID: historyProposalID, ProposalType: "file_patch"}}},
		{name: "outside page", mappings: []domain.CommitMapping{{GitCommit: historyTarget, ArticleRevisionID: historyRevisionID, ArticleRevisionNo: 1}}},
		{name: "orphan relation", mappings: []domain.CommitMapping{{GitCommit: historyHead, WorkflowRunID: historyRevisionID}}},
		{name: "invalid optional id", mappings: []domain.CommitMapping{{GitCommit: historyHead, ProposalID: historyProposalID, WorkflowRunID: "invalid", ProposalType: "file_patch"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newHistoryTestService(t,
				&historyDocumentReaderStub{document: historyDocument(), mappings: test.mappings},
				&historyGitStub{state: state, page: domain.CommitPage{State: state, Commits: []domain.Commit{commit}}},
				&historyProposalStub{}, &historyLookupStub{},
			)
			_, err := service.ListHistory(context.Background(), HistoryQuery{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, Limit: 30})
			assertHistoryErrorCode(t, err, ErrorCodeResultInvalid)
		})
	}
}

func TestListHistoryRejectsRepositoryStateAndParentContractDrift(t *testing.T) {
	tests := []struct {
		name  string
		state domain.RepositoryState
		page  []domain.Commit
	}{
		{
			name:  "oversized branch",
			state: domain.RepositoryState{WorkspaceID: historyWorkspaceID, Branch: strings.Repeat("a", 513), Head: historyHead, ObjectFormat: "sha1"},
		},
		{
			name:  "too many parents",
			state: historyState(false, false),
			page: []domain.Commit{{
				ID: historyHead, ParentIDs: make([]string, domain.MaxCommitParents+1), AuthorName: "User",
				AuthorEmail: "user@example.com", CommittedAt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC), Subject: "merge",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for index := range test.page {
				for parent := range test.page[index].ParentIDs {
					test.page[index].ParentIDs[parent] = historyTarget
				}
			}
			service := newHistoryTestService(t,
				&historyDocumentReaderStub{document: historyDocument()},
				&historyGitStub{state: test.state, page: domain.CommitPage{State: test.state, Commits: test.page}},
				&historyProposalStub{}, &historyLookupStub{},
			)
			_, err := service.ListHistory(context.Background(), HistoryQuery{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, Limit: 30})
			assertHistoryErrorCode(t, err, ErrorCodeResultInvalid)
		})
	}
}

func TestCompareVersionsRejectsMalformedOwnerOutput(t *testing.T) {
	state := historyState(false, false)
	patch := "\x00"
	git := &historyGitStub{state: state, diff: historyDiff(historyTarget, historyHead, "old", "new", patch)}
	service := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, &historyProposalStub{}, &historyLookupStub{})
	_, err := service.CompareVersions(context.Background(), CompareQuery{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, Left: historyTarget, Right: historyHead})
	assertHistoryErrorCode(t, err, ErrorCodeResultInvalid)
}

func TestPreviewRestoreProducesBoundDirtyPreviewAndRejectsNoopTarget(t *testing.T) {
	state := historyState(true, false)
	git := historyGitForRestore(state, "current\n", "target\n")
	service := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, &historyProposalStub{}, &historyLookupStub{})
	preview, err := service.PreviewRestore(context.Background(), PreviewCommand{
		WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, TargetCommit: historyTarget, ExpectedDocumentVersion: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.BlockedByDirtyWorktree || preview.ExpectedHead != historyHead || !domain.ValidHash(preview.PreviewHash) {
		t.Fatalf("preview=%+v", preview)
	}
	recomputed, err := domain.ComputePreviewHash(preview)
	if err != nil || recomputed != preview.PreviewHash {
		t.Fatalf("recomputed=(%q,%v) preview=%q", recomputed, err, preview.PreviewHash)
	}

	git = historyGitForRestore(historyState(false, false), "same\n", "same\n")
	service = newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, &historyProposalStub{}, &historyLookupStub{})
	_, err = service.PreviewRestore(context.Background(), PreviewCommand{
		WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, TargetCommit: historyTarget, ExpectedDocumentVersion: 7,
	})
	assertHistoryErrorCode(t, err, ErrorCodeRestoreStale)
}

func TestCreateRestoreProposalReplaysBeforeReadingMutableFacts(t *testing.T) {
	command := historyRestoreCommand()
	existing := historyReceipt(command)
	existing.Status = "completed"
	lookup := &historyLookupStub{found: true, receipt: existing}
	documents := &historyDocumentReaderStub{document: historyDocument()}
	git := &historyGitStub{}
	proposals := &historyProposalStub{}
	service := newHistoryTestService(t, documents, git, proposals, lookup)

	receipt, err := service.CreateRestoreProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Replayed || receipt.Status != "completed" || documents.getCalls != 0 || git.inspectCalls != 0 || proposals.calls != 0 {
		t.Fatalf("receipt=%+v get=%d inspect=%d create=%d", receipt, documents.getCalls, git.inspectCalls, proposals.calls)
	}
}

func TestCreateRestoreProposalRejectsDirtyAndTargetRereadDrift(t *testing.T) {
	command := historyRestoreCommand()
	dirtyGit := historyGitForRestore(historyState(true, true), "current\n", "target\n")
	dirtyPreviewService := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, dirtyGit, &historyProposalStub{}, &historyLookupStub{})
	preview, err := dirtyPreviewService.PreviewRestore(context.Background(), PreviewCommand{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, TargetCommit: historyTarget, ExpectedDocumentVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	command.PreviewHash = preview.PreviewHash
	proposal := &historyProposalStub{}
	service := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, dirtyGit, proposal, &historyLookupStub{})
	_, err = service.CreateRestoreProposal(context.Background(), command)
	assertHistoryErrorCode(t, err, ErrorCodeDirty)
	if proposal.calls != 0 {
		t.Fatalf("proposal create called on dirty worktree")
	}

	cleanGit := historyGitForRestore(historyState(false, false), "current\n", "target\n")
	previewService := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, cleanGit, &historyProposalStub{}, &historyLookupStub{})
	cleanPreview, err := previewService.PreviewRestore(context.Background(), PreviewCommand{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, TargetCommit: historyTarget, ExpectedDocumentVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	command.PreviewHash = cleanPreview.PreviewHash
	cleanGit.readCalls = 0
	cleanGit.readBlobFn = func(commit, path string, call int) domain.Blob {
		content := []byte("current\n")
		if commit == historyTarget {
			content = []byte("target\n")
			if call == 3 {
				content = []byte("drift\n")
			}
		}
		return domain.Blob{Commit: commit, Path: path, Content: content, ContentHash: domain.ComputeHash(content)}
	}
	proposal = &historyProposalStub{}
	service = newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, cleanGit, proposal, &historyLookupStub{})
	_, err = service.CreateRestoreProposal(context.Background(), command)
	assertHistoryErrorCode(t, err, ErrorCodeResultInvalid)
	if proposal.calls != 0 {
		t.Fatalf("proposal create called after target reread drift")
	}
}

func TestCreateRestoreProposalRejectsStalePreviewBindings(t *testing.T) {
	git := historyGitForRestore(historyState(false, false), "current\n", "target\n")
	previewService := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, &historyProposalStub{}, &historyLookupStub{})
	preview, err := previewService.PreviewRestore(context.Background(), PreviewCommand{
		WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, TargetCommit: historyTarget, ExpectedDocumentVersion: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*RestoreCommand)
	}{
		{name: "head changed", mutate: func(command *RestoreCommand) { command.ExpectedHead = historyOlder }},
		{name: "preview changed", mutate: func(command *RestoreCommand) { command.PreviewHash = strings.Repeat("f", 64) }},
		{name: "document version changed", mutate: func(command *RestoreCommand) { command.ExpectedDocumentVersion-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := historyRestoreCommand()
			command.PreviewHash = preview.PreviewHash
			test.mutate(&command)
			proposal := &historyProposalStub{}
			service := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, proposal, &historyLookupStub{})
			_, createErr := service.CreateRestoreProposal(context.Background(), command)
			assertHistoryErrorCode(t, createErr, ErrorCodeRestoreStale)
			if proposal.calls != 0 {
				t.Fatalf("proposal create called for stale binding")
			}
		})
	}
}

func TestCreateRestoreProposalCreatesServerOwnedTargetContent(t *testing.T) {
	git := historyGitForRestore(historyState(false, false), "current\n", "target\n")
	previewService := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, &historyProposalStub{}, &historyLookupStub{})
	preview, err := previewService.PreviewRestore(context.Background(), PreviewCommand{WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, TargetCommit: historyTarget, ExpectedDocumentVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	command := historyRestoreCommand()
	command.PreviewHash = preview.PreviewHash
	proposal := &historyProposalStub{}
	proposal.createFn = func(record CreateRestoreProposalRecord) RestoreProposalReceipt {
		result := historyReceipt(command)
		result.ChangeHash = domain.ComputeHash([]byte(record.TargetContent))
		return result
	}
	service := newHistoryTestService(t, &historyDocumentReaderStub{document: historyDocument()}, git, proposal, &historyLookupStub{})
	receipt, err := service.CreateRestoreProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replayed || proposal.calls != 1 || proposal.record.TargetContent != "target\n" || proposal.record.TargetContentHash != domain.ComputeHash([]byte("target\n")) {
		t.Fatalf("receipt=%+v record=%+v", receipt, proposal.record)
	}
}

type historyDocumentReaderStub struct {
	document      DocumentSnapshot
	mappings      []domain.CommitMapping
	getCalls      int
	mapCalls      int
	mappedCommits []string
}

func (stub *historyDocumentReaderStub) GetDocument(context.Context, foundation.ID, foundation.ID) (DocumentSnapshot, error) {
	stub.getCalls++
	return stub.document, nil
}

func (stub *historyDocumentReaderStub) MapCommits(_ context.Context, _, _ foundation.ID, _ string, commits []string) ([]domain.CommitMapping, error) {
	stub.mapCalls++
	stub.mappedCommits = append([]string(nil), commits...)
	return append([]domain.CommitMapping(nil), stub.mappings...), nil
}

type historyGitStub struct {
	state        domain.RepositoryState
	page         domain.CommitPage
	diff         domain.Diff
	inspectErr   error
	listErr      error
	compareErr   error
	readErr      error
	readBlobFn   func(string, string, int) domain.Blob
	inspectCalls int
	listCalls    int
	compareCalls int
	readCalls    int
}

func (stub *historyGitStub) Inspect(context.Context, foundation.ID, string) (domain.RepositoryState, error) {
	stub.inspectCalls++
	return stub.state, stub.inspectErr
}

func (stub *historyGitStub) List(_ context.Context, _ foundation.ID, _ string, offset, limit int) (domain.CommitPage, error) {
	stub.listCalls++
	if stub.listErr != nil {
		return domain.CommitPage{}, stub.listErr
	}
	if offset > 0 && limit == 1 && len(stub.page.Commits) > 0 {
		index := offset
		if index >= len(stub.page.Commits) {
			index = len(stub.page.Commits) - 1
		}
		return domain.CommitPage{State: stub.state, Commits: []domain.Commit{stub.page.Commits[index]}}, nil
	}
	return stub.page, nil
}

func (stub *historyGitStub) Compare(context.Context, foundation.ID, string, string, string) (domain.Diff, error) {
	stub.compareCalls++
	return stub.diff, stub.compareErr
}

func (stub *historyGitStub) ReadBlob(_ context.Context, _ foundation.ID, commit, path string) (domain.Blob, error) {
	stub.readCalls++
	if stub.readErr != nil {
		return domain.Blob{}, stub.readErr
	}
	if stub.readBlobFn != nil {
		return stub.readBlobFn(commit, path, stub.readCalls), nil
	}
	return domain.Blob{}, nil
}

type historyProposalStub struct {
	calls    int
	record   CreateRestoreProposalRecord
	result   RestoreProposalReceipt
	err      error
	createFn func(CreateRestoreProposalRecord) RestoreProposalReceipt
}

func (stub *historyProposalStub) CreateRestoreDocumentProposal(_ context.Context, record CreateRestoreProposalRecord) (RestoreProposalReceipt, error) {
	stub.calls++
	stub.record = record
	if stub.createFn != nil {
		return stub.createFn(record), stub.err
	}
	return stub.result, stub.err
}

type historyLookupStub struct {
	calls   int
	receipt RestoreProposalReceipt
	found   bool
	err     error
}

func (stub *historyLookupStub) FindRestoreProposal(context.Context, foundation.ID, string) (RestoreProposalReceipt, bool, error) {
	stub.calls++
	return stub.receipt, stub.found, stub.err
}

func newHistoryTestService(t *testing.T, documents DocumentReader, git GitHistoryReader, proposals RestoreProposalCreator, lookup RestoreProposalLookup) *Service {
	t.Helper()
	cursors, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{Documents: documents, Git: git, Proposals: proposals, Lookup: lookup, Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func historyDocument() DocumentSnapshot {
	return DocumentSnapshot{
		ID: historyDocumentID, WorkspaceID: historyWorkspaceID, CanonicalPath: "notes/java-ai.md",
		Title: "Java AI", Lifecycle: "PUBLISHED", CurrentPublishedRevisionID: historyRevisionID, Version: 7,
	}
}

func historyState(dirty, pathDirty bool) domain.RepositoryState {
	return domain.RepositoryState{WorkspaceID: historyWorkspaceID, Branch: "main", Head: historyHead, ObjectFormat: "sha1", Dirty: dirty, PathDirty: pathDirty}
}

func historyCommit(id, subject string) domain.Commit {
	return domain.Commit{ID: id, ParentIDs: []string{historyTarget}, AuthorName: "User", AuthorEmail: "user@example.com", CommittedAt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC), Subject: subject}
}

func historyDiff(left, right, leftContent, rightContent, patch string) domain.Diff {
	return domain.Diff{
		WorkspaceID: historyWorkspaceID, Path: "notes/java-ai.md", Head: historyHead,
		Left: left, Right: right, LeftContent: leftContent, RightContent: rightContent,
		Patch: patch, DiffHash: domain.ComputeHash([]byte(patch)),
	}
}

func historyGitForRestore(state domain.RepositoryState, current, target string) *historyGitStub {
	patch := "-current\n+target\n"
	stub := &historyGitStub{state: state, diff: historyDiff(historyHead, historyTarget, current, target, patch)}
	stub.readBlobFn = func(commit, path string, _ int) domain.Blob {
		content := []byte(current)
		if commit == historyTarget {
			content = []byte(target)
		}
		return domain.Blob{Commit: commit, Path: path, Content: content, ContentHash: domain.ComputeHash(content)}
	}
	return stub
}

func historyRestoreCommand() RestoreCommand {
	return RestoreCommand{
		WorkspaceID: historyWorkspaceID, DocumentID: historyDocumentID, TargetCommit: historyTarget,
		ExpectedHead: historyHead, ExpectedDocumentVersion: 7, PreviewHash: strings.Repeat("d", 64),
		IdempotencyKey: "restore-history-test",
	}
}

func historyReceipt(command RestoreCommand) RestoreProposalReceipt {
	return RestoreProposalReceipt{
		WorkspaceID: command.WorkspaceID, DocumentID: command.DocumentID,
		ProposalID: historyProposalID, ProposalRevisionID: historyProposalRev,
		TargetCommit: command.TargetCommit, ExpectedHead: command.ExpectedHead,
		ExpectedDocumentVersion: command.ExpectedDocumentVersion, PreviewHash: command.PreviewHash,
		ProposalType: "restore_document", Status: "ready_for_review", ChangeHash: strings.Repeat("e", 64),
	}
}

func historyErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
