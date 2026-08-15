package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type revisionTestRepository struct {
	*fakeRepo
	receiptResult domain.AppendProposalRevisionResult
	receiptHash   string
	receiptFound  bool
	receiptCalls  int
	snapshot      domain.RevisionBaseSnapshot
	snapshotFound bool
	snapshotCalls int
	appendResult  domain.AppendProposalRevisionResult
	appendCommand domain.AppendProposalRevision
	appendCalls   int
	getCalls      int
}

func (repository *revisionTestRepository) GetProposal(ctx context.Context, proposalID foundation.ID) (domain.Proposal, error) {
	repository.getCalls++
	return repository.fakeRepo.GetProposal(ctx, proposalID)
}

func (repository *revisionTestRepository) LookupRevisionCommandReceipt(_ context.Context, lookup domain.RevisionCommandReceiptLookup) (domain.AppendProposalRevisionResult, bool, error) {
	repository.receiptCalls++
	if !repository.receiptFound {
		return domain.AppendProposalRevisionResult{}, false, nil
	}
	if lookup.RequestHash != repository.receiptHash {
		return domain.AppendProposalRevisionResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("request hash differs"))
	}
	return repository.receiptResult, true, nil
}

func (repository *revisionTestRepository) GetRevisionBaseSnapshot(_ context.Context, proposalID, revisionID foundation.ID) (domain.RevisionBaseSnapshot, bool, error) {
	repository.snapshotCalls++
	if !repository.snapshotFound {
		return domain.RevisionBaseSnapshot{}, false, nil
	}
	if repository.snapshot.ProposalID != proposalID || repository.snapshot.RevisionID != revisionID {
		return domain.RevisionBaseSnapshot{}, false, errors.New("unexpected snapshot lookup")
	}
	return repository.snapshot, true, nil
}

func (repository *revisionTestRepository) AppendRevision(_ context.Context, command domain.AppendProposalRevision) (domain.AppendProposalRevisionResult, error) {
	repository.appendCalls++
	repository.appendCommand = command
	return repository.appendResult, nil
}

type revisionMergeRecorder struct {
	result domainMergeResult
	input  RevisionMergeDocuments
	calls  int
}

type domainMergeResult struct {
	candidate []byte
	conflicts []RevisionMergeConflict
}

func (merger *revisionMergeRecorder) Merge(_ context.Context, input RevisionMergeDocuments) (RevisionMergeResult, error) {
	merger.calls++
	merger.input = RevisionMergeDocuments{Base: cloneBytes(input.Base), Current: cloneBytes(input.Current), Proposed: cloneBytes(input.Proposed)}
	return RevisionMergeResult{
		Candidate: cloneBytes(merger.result.candidate), Conflicts: append([]RevisionMergeConflict(nil), merger.result.conflicts...),
		Algorithm: domain.ProposalRevisionMergeAlgorithm, Contract: domain.ProposalRevisionMergeAlgorithmVersion,
	}, nil
}

func newRevisionTestService(t *testing.T, repository *revisionTestRepository, targets *fakeTargets, merger RevisionMergeEngine) *Service {
	t.Helper()
	service, err := NewService(repository, &seqIDs{}, foundation.FixedClock{Value: time.Unix(10, 0).UTC()}, targets, &fakeApprovalGitInspector{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetRevisionMergeEngine(merger)
	return service
}

func revisionAppendCommand() AppendRevisionCommand {
	return AppendRevisionCommand{
		WorkspaceID: "workspace", ProposalID: "proposal", IdempotencyKey: "append-key", ExpectedProposalVersion: 2,
		SourceRevisionID: "source", SourceChangeHash: strings.Repeat("a", 64), ExpectedCurrentHash: domain.RawContentHash("current\n"),
		PreviewFingerprint: strings.Repeat("b", 64), MergeAlgorithm: domain.ProposalRevisionMergeAlgorithm,
		MergeAlgorithmVersion: domain.ProposalRevisionMergeAlgorithmVersion, FinalContent: "merged\n",
		EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback",
	}
}

func TestAppendProposalRevisionReplaysBeforeMutableStateOrWorkspaceReads(t *testing.T) {
	command := revisionAppendCommand()
	domainCommand := domain.AppendProposalRevision{
		WorkspaceID: command.WorkspaceID, ProposalID: command.ProposalID, IdempotencyKey: command.IdempotencyKey,
		ExpectedProposalVersion: command.ExpectedProposalVersion, SourceRevisionID: command.SourceRevisionID,
		SourceChangeHash: command.SourceChangeHash, ExpectedCurrentHash: command.ExpectedCurrentHash,
		PreviewFingerprint: command.PreviewFingerprint, MergeAlgorithm: command.MergeAlgorithm,
		MergeAlgorithmVersion: command.MergeAlgorithmVersion, FinalContent: command.FinalContent,
		EvidenceSummary: command.EvidenceSummary, Risk: command.Risk, RollbackPlan: command.RollbackPlan,
	}
	prior := domain.Proposal{ID: "proposal", WorkspaceID: "workspace", Status: domain.StatusReady, Version: 3,
		Revision: domain.Revision{ID: "result", ProposalID: "proposal", RevisionNo: 2}}
	repository := &revisionTestRepository{
		fakeRepo: &fakeRepo{proposal: domain.Proposal{Status: domain.StatusCompleted}}, receiptFound: true,
		receiptHash:   domain.ComputeRevisionRequestHash(domainCommand),
		receiptResult: domain.AppendProposalRevisionResult{Proposal: prior, Revision: prior.Revision, Replayed: true},
	}
	targets := &fakeTargets{err: errors.New("workspace must not be read")}
	service := newRevisionTestService(t, repository, targets, nil)

	result, err := service.AppendProposalRevision(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.Revision.ID != "result" {
		t.Fatalf("result = %#v", result)
	}
	if repository.receiptCalls != 1 || repository.getCalls != 0 || repository.snapshotCalls != 0 || repository.appendCalls != 0 || targets.calls != 0 {
		t.Fatalf("replay touched mutable dependencies: receipt=%d get=%d snapshot=%d append=%d target=%d", repository.receiptCalls, repository.getCalls, repository.snapshotCalls, repository.appendCalls, targets.calls)
	}
}

func TestAppendProposalRevisionRejectsSameKeyDifferentRequestBeforeMutableReads(t *testing.T) {
	command := revisionAppendCommand()
	repository := &revisionTestRepository{fakeRepo: &fakeRepo{}, receiptFound: true, receiptHash: strings.Repeat("c", 64)}
	targets := &fakeTargets{err: errors.New("workspace must not be read")}
	service := newRevisionTestService(t, repository, targets, nil)

	_, err := service.AppendProposalRevision(context.Background(), command)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("error = %v", err)
	}
	if repository.getCalls != 0 || repository.snapshotCalls != 0 || repository.appendCalls != 0 || targets.calls != 0 {
		t.Fatalf("mismatched replay touched mutable dependencies: %#v targets=%d", repository, targets.calls)
	}
}

func TestAppendProposalRevisionRequestsCancellationBeforePreview(t *testing.T) {
	proposal := revisionProposal("base\n", "proposed\n")
	proposal.Status = domain.StatusNeedsRevision
	runID := foundation.ID("workflow-run")
	proposal.WorkflowRunID = &runID
	proposal.WorkflowRunStatus = "running"
	repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}}
	targets := &fakeTargets{err: errors.New("workspace must not be read while cancellation is pending")}
	merger := &revisionMergeRecorder{}
	service := newRevisionTestService(t, repository, targets, merger)
	var cancellation RevisionWorkflowCancellation
	service.SetRevisionWorkflowCanceller(RevisionWorkflowCancellerFunc(func(_ context.Context, command RevisionWorkflowCancellation) (RevisionWorkflowCancellationResult, error) {
		cancellation = command
		return RevisionWorkflowCancellationResult{}, nil
	}))
	command := revisionAppendCommand()
	command.SourceChangeHash = proposal.Revision.ChangeHash
	command.ExpectedCurrentHash = domain.RawContentHash("current\n")

	_, err := service.AppendProposalRevision(context.Background(), command)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_WORKFLOW_ACTIVE" {
		t.Fatalf("error = %v", err)
	}
	if cancellation.WorkflowRunID != runID || cancellation.RevisionID != proposal.Revision.ID || cancellation.IdempotencyKey == "" {
		t.Fatalf("cancellation = %#v", cancellation)
	}
	if repository.getCalls != 1 || repository.snapshotCalls != 0 || repository.appendCalls != 0 || targets.calls != 0 || merger.calls != 0 {
		t.Fatalf("get=%d snapshot=%d append=%d target=%d merge=%d", repository.getCalls, repository.snapshotCalls, repository.appendCalls, targets.calls, merger.calls)
	}
}

func TestAppendProposalRevisionReportsCancellationDependencyFailures(t *testing.T) {
	tests := []struct {
		name      string
		canceller RevisionWorkflowCanceller
	}{
		{name: "not configured"},
		{name: "owner unavailable", canceller: RevisionWorkflowCancellerFunc(func(context.Context, RevisionWorkflowCancellation) (RevisionWorkflowCancellationResult, error) {
			return RevisionWorkflowCancellationResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_CONTROL_FAILED", true, errors.New("database unavailable"))
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal := revisionProposal("base\n", "proposed\n")
			proposal.Status = domain.StatusNeedsRevision
			runID := foundation.ID("workflow-run")
			proposal.WorkflowRunID = &runID
			proposal.WorkflowRunStatus = "running"
			repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}}
			targets := &fakeTargets{err: errors.New("workspace must not be read")}
			merger := &revisionMergeRecorder{}
			service := newRevisionTestService(t, repository, targets, merger)
			if test.canceller != nil {
				service.SetRevisionWorkflowCanceller(test.canceller)
			}
			command := revisionAppendCommand()
			command.SourceChangeHash = proposal.Revision.ChangeHash

			_, err := service.AppendProposalRevision(context.Background(), command)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable ||
				classified.Code != "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE" || !classified.Retryable {
				t.Fatalf("error = %#v", classified)
			}
			if repository.appendCalls != 0 || targets.calls != 0 || merger.calls != 0 {
				t.Fatalf("append=%d target=%d merge=%d", repository.appendCalls, targets.calls, merger.calls)
			}
		})
	}
}

func TestAppendProposalRevisionDoesNotCancelWorkflowForStaleSource(t *testing.T) {
	proposal := revisionProposal("base\n", "proposed\n")
	proposal.Status = domain.StatusNeedsRevision
	runID := foundation.ID("current-workflow-run")
	proposal.WorkflowRunID = &runID
	proposal.WorkflowRunStatus = "running"
	repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}}
	service := newRevisionTestService(t, repository, &fakeTargets{}, &revisionMergeRecorder{})
	cancelCalls := 0
	service.SetRevisionWorkflowCanceller(RevisionWorkflowCancellerFunc(func(context.Context, RevisionWorkflowCancellation) (RevisionWorkflowCancellationResult, error) {
		cancelCalls++
		return RevisionWorkflowCancellationResult{}, nil
	}))
	command := revisionAppendCommand()
	command.SourceRevisionID = "stale-source"

	_, err := service.AppendProposalRevision(context.Background(), command)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_STALE" || cancelCalls != 0 {
		t.Fatalf("cancel calls=%d error=%v", cancelCalls, err)
	}
}

func TestValidateResolvedConflictsReturnsExpectedConflictIDs(t *testing.T) {
	err := validateResolvedConflicts(
		[]domain.RevisionConflict{{ID: "conflict-b"}, {ID: "conflict-a"}},
		[]string{"conflict-a"},
	)
	var classified *foundation.Error
	var unresolved *UnresolvedRevisionConflicts
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED" {
		t.Fatalf("error = %v", err)
	}
	if !errors.As(err, &unresolved) || len(unresolved.ConflictIDs) != 2 || unresolved.ConflictIDs[0] != "conflict-a" || unresolved.ConflictIDs[1] != "conflict-b" {
		t.Fatalf("unresolved conflicts = %#v", unresolved)
	}
}

func TestAppendProposalRevisionPersistsServerReadCurrentSnapshot(t *testing.T) {
	base := "base\n"
	current := "current\n"
	proposed := "proposed\n"
	proposal := revisionProposal(base, proposed)
	snapshot := domain.RevisionBaseSnapshot{
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, BaseHash: proposal.Revision.BaseHash,
		Content: base, ByteSize: len(base), SchemaVersion: domain.ProposalRevisionBaseSnapshotSchemaVersion, CreatedAt: time.Unix(1, 0).UTC(),
	}
	mergeResult := RevisionMergeResult{
		Candidate: []byte("merged\n"), Algorithm: domain.ProposalRevisionMergeAlgorithm, Contract: domain.ProposalRevisionMergeAlgorithmVersion,
	}
	preview := buildRevisionPreview(proposal, proposal.Revision.ID, snapshot.BaseHash, domain.RawContentHash(current), mergeResult)
	resultRevision := proposal.Revision
	resultRevision.ID = "result"
	resultRevision.RevisionNo = 2
	resultRevision.BaseHash = domain.RawContentHash(current)
	resultRevision.Content = "merged\n"
	resultRevision.ChangeHash = domain.ComputeChangeHash(resultRevision.TargetPath, resultRevision.BaseHash, resultRevision.Content)
	resultRevision.BaseSnapshot = &domain.RevisionBaseSnapshot{
		ProposalID: proposal.ID, RevisionID: resultRevision.ID, BaseHash: resultRevision.BaseHash,
		Content: current, ByteSize: len(current), SchemaVersion: domain.ProposalRevisionBaseSnapshotSchemaVersion, CreatedAt: time.Unix(10, 0).UTC(),
	}
	resultProposal := proposal
	resultProposal.Version++
	resultProposal.CurrentRevisionID = resultRevision.ID
	resultProposal.Revision = resultRevision
	repository := &revisionTestRepository{
		fakeRepo: &fakeRepo{proposal: proposal}, snapshot: snapshot, snapshotFound: true,
		appendResult: domain.AppendProposalRevisionResult{Proposal: resultProposal, Revision: resultRevision},
	}
	merger := &revisionMergeRecorder{result: domainMergeResult{candidate: mergeResult.Candidate}}
	service := newRevisionTestService(t, repository, &fakeTargets{hash: domain.RawContentHash(current), content: []byte(current)}, merger)
	command := revisionAppendCommand()
	command.SourceChangeHash = proposal.Revision.ChangeHash
	command.ExpectedCurrentHash = domain.RawContentHash(current)
	command.PreviewFingerprint = preview.Fingerprint

	result, err := service.AppendProposalRevision(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision.ID != resultRevision.ID || result.Replayed || repository.receiptCalls != 1 || repository.appendCalls != 1 {
		t.Fatalf("result=%#v receipt calls=%d append calls=%d", result, repository.receiptCalls, repository.appendCalls)
	}
	persisted := repository.appendCommand
	if persisted.CurrentContent != current || persisted.ExpectedCurrentHash != domain.RawContentHash(current) || persisted.NewRevisionID == "" || persisted.CreatedAt.IsZero() {
		t.Fatalf("append command = %#v", persisted)
	}
	if persisted.RequestHash != domain.ComputeRevisionRequestHash(persisted) {
		t.Fatalf("request hash = %s", persisted.RequestHash)
	}
}

func TestAppendProposalRevisionRejectsCurrentDriftAfterPreview(t *testing.T) {
	base := "base\n"
	current := "current\n"
	drifted := "drifted\n"
	proposal := revisionProposal(base, "proposed\n")
	snapshot := domain.RevisionBaseSnapshot{
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, BaseHash: proposal.Revision.BaseHash,
		Content: base, ByteSize: len(base), SchemaVersion: domain.ProposalRevisionBaseSnapshotSchemaVersion, CreatedAt: time.Unix(1, 0).UTC(),
	}
	mergeResult := RevisionMergeResult{Candidate: []byte("merged\n"), Algorithm: domain.ProposalRevisionMergeAlgorithm, Contract: domain.ProposalRevisionMergeAlgorithmVersion}
	preview := buildRevisionPreview(proposal, proposal.Revision.ID, snapshot.BaseHash, domain.RawContentHash(current), mergeResult)
	repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}, snapshot: snapshot, snapshotFound: true}
	merger := &revisionMergeRecorder{result: domainMergeResult{candidate: mergeResult.Candidate}}
	targets := &fakeTargets{
		contentSequence: [][]byte{[]byte(current), []byte(drifted)},
		hashSequence:    []string{domain.RawContentHash(current), domain.RawContentHash(drifted)},
	}
	service := newRevisionTestService(t, repository, targets, merger)
	command := revisionAppendCommand()
	command.SourceChangeHash = proposal.Revision.ChangeHash
	command.ExpectedCurrentHash = domain.RawContentHash(current)
	command.PreviewFingerprint = preview.Fingerprint

	_, err := service.AppendProposalRevision(context.Background(), command)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_STALE" {
		t.Fatalf("error = %v", err)
	}
	if targets.calls != 2 || repository.appendCalls != 0 {
		t.Fatalf("current reads=%d append calls=%d", targets.calls, repository.appendCalls)
	}
}

func TestPreviewProposalRevisionUsesPersistedBaseSnapshot(t *testing.T) {
	base := "base\n"
	current := "current\n"
	proposed := "proposed\n"
	proposal := revisionProposal(base, proposed)
	snapshot := domain.RevisionBaseSnapshot{
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, BaseHash: proposal.Revision.BaseHash,
		Content: base, ByteSize: len(base), SchemaVersion: domain.ProposalRevisionBaseSnapshotSchemaVersion, CreatedAt: time.Unix(1, 0).UTC(),
	}
	repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}, snapshot: snapshot, snapshotFound: true}
	merger := &revisionMergeRecorder{result: domainMergeResult{candidate: []byte("merged\n")}}
	service := newRevisionTestService(t, repository, &fakeTargets{hash: domain.RawContentHash(current), content: []byte(current)}, merger)

	result, err := service.PreviewProposalRevision(context.Background(), domain.RevisionMergeInput{
		WorkspaceID: proposal.WorkspaceID, ProposalID: proposal.ID, SourceRevisionID: proposal.Revision.ID,
		SourceChangeHash: proposal.Revision.ChangeHash, ExpectedProposalVersion: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(merger.input.Base) != base || string(merger.input.Current) != current || string(merger.input.Proposed) != proposed {
		t.Fatalf("merge input = %#v", merger.input)
	}
	if result.BaseContent != base || result.CurrentContent != current || result.ProposedContent != proposed ||
		result.WorkspaceID != proposal.WorkspaceID || result.SourceRevisionNo != proposal.Revision.RevisionNo || result.TargetPath != proposal.Revision.TargetPath {
		t.Fatalf("preview result = %#v", result)
	}
}

func TestPreviewProposalRevisionRejectsMissingCurrentRevisionPointer(t *testing.T) {
	proposal := revisionProposal("base\n", "proposed\n")
	proposal.CurrentRevisionID = ""
	repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}}
	targets := &fakeTargets{hash: domain.RawContentHash("base\n"), content: []byte("base\n")}
	merger := &revisionMergeRecorder{result: domainMergeResult{candidate: []byte("proposed\n")}}
	service := newRevisionTestService(t, repository, targets, merger)

	_, err := service.PreviewProposalRevision(context.Background(), domain.RevisionMergeInput{
		WorkspaceID: proposal.WorkspaceID, ProposalID: proposal.ID, SourceRevisionID: proposal.Revision.ID,
		SourceChangeHash: proposal.Revision.ChangeHash, ExpectedProposalVersion: proposal.Version,
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_STALE" {
		t.Fatalf("error = %v", err)
	}
	if targets.calls != 0 || merger.calls != 0 || repository.snapshotCalls != 0 {
		t.Fatalf("stale pointer reached mutable dependencies: target=%d merge=%d snapshot=%d", targets.calls, merger.calls, repository.snapshotCalls)
	}
}

func TestPreviewProposalRevisionLegacyBaseRequiresUnchangedCurrent(t *testing.T) {
	base := "base\n"
	proposal := revisionProposal(base, "proposed\n")
	tests := []struct {
		name       string
		current    string
		wantCode   string
		mergeCalls int
	}{
		{name: "exact fallback", current: base, mergeCalls: 1},
		{name: "drifted", current: "drifted\n", wantCode: "PROPOSAL_BASE_SNAPSHOT_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}}
			merger := &revisionMergeRecorder{result: domainMergeResult{candidate: []byte("merged\n")}}
			service := newRevisionTestService(t, repository, &fakeTargets{hash: domain.RawContentHash(test.current), content: []byte(test.current)}, merger)
			result, err := service.PreviewProposalRevision(context.Background(), domain.RevisionMergeInput{
				WorkspaceID: proposal.WorkspaceID, ProposalID: proposal.ID, SourceRevisionID: proposal.Revision.ID,
				SourceChangeHash: proposal.Revision.ChangeHash, ExpectedProposalVersion: proposal.Version,
			})
			if test.wantCode == "" {
				if err != nil || result.BaseContent != base {
					t.Fatalf("result=%#v error=%v", result, err)
				}
			} else {
				var classified *foundation.Error
				if !errors.As(err, &classified) || classified.Code != test.wantCode {
					t.Fatalf("error = %v", err)
				}
			}
			if merger.calls != test.mergeCalls {
				t.Fatalf("merge calls = %d", merger.calls)
			}
		})
	}
}

func TestPreviewProposalRevisionRejectsOversizedPersistedContentBeforeExternalReads(t *testing.T) {
	proposal := revisionProposal("base\n", strings.Repeat("x", domain.ProposalRevisionMaxBytes+1))
	repository := &revisionTestRepository{fakeRepo: &fakeRepo{proposal: proposal}}
	targets := &fakeTargets{err: errors.New("workspace must not be read")}
	merger := &revisionMergeRecorder{}
	service := newRevisionTestService(t, repository, targets, merger)

	_, err := service.PreviewProposalRevision(context.Background(), domain.RevisionMergeInput{
		WorkspaceID: proposal.WorkspaceID, ProposalID: proposal.ID, SourceRevisionID: proposal.Revision.ID,
		SourceChangeHash: proposal.Revision.ChangeHash, ExpectedProposalVersion: proposal.Version,
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_REVISION_INPUT_TOO_LARGE" {
		t.Fatalf("error = %v", err)
	}
	if targets.calls != 0 || repository.snapshotCalls != 0 || merger.calls != 0 {
		t.Fatalf("oversized preview touched external dependencies: target=%d snapshot=%d merge=%d", targets.calls, repository.snapshotCalls, merger.calls)
	}
}

func TestCreateProposalSnapshotCapturePreservesOversizedCreateCapability(t *testing.T) {
	t.Run("base hash mismatch", func(t *testing.T) {
		repository := &fakeRepo{}
		targets := &fakeTargets{content: []byte("actual\n"), hash: domain.RawContentHash("actual\n")}
		service := newTestService(repository, targets)
		_, err := service.CreateProposal(context.Background(), revisionCreateCommand(domain.RawContentHash("expected\n"), "proposed\n", "mismatch"))
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != "TARGET_BASE_HASH_CONFLICT" || repository.createCalls != 0 {
			t.Fatalf("error=%v create calls=%d", err, repository.createCalls)
		}
	})

	t.Run("proposed over merge limit", func(t *testing.T) {
		repository := &fakeRepo{}
		targets := &fakeTargets{err: errors.New("target must not be read")}
		service := newTestService(repository, targets)
		result, err := service.CreateProposal(context.Background(), revisionCreateCommand(testHash, strings.Repeat("x", domain.ProposalRevisionMaxBytes+1), "large-proposed"))
		if err != nil {
			t.Fatal(err)
		}
		if targets.calls != 0 || repository.createCalls != 1 || result.Proposal.Revision.BaseSnapshot != nil {
			t.Fatalf("result=%#v target calls=%d create calls=%d", result, targets.calls, repository.createCalls)
		}
	})

	t.Run("base over merge limit", func(t *testing.T) {
		repository := &fakeRepo{}
		tooLarge := foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURRENT_CONTENT_TOO_LARGE", false, errors.New("target exceeds merge limit"))
		targets := &fakeTargets{err: tooLarge}
		service := newTestService(repository, targets)
		result, err := service.CreateProposal(context.Background(), revisionCreateCommand(testHash, "proposed\n", "large-base"))
		if err != nil {
			t.Fatal(err)
		}
		if targets.calls != 1 || repository.createCalls != 1 || result.Proposal.Revision.BaseSnapshot != nil {
			t.Fatalf("result=%#v target calls=%d create calls=%d", result, targets.calls, repository.createCalls)
		}
	})
}

func TestCreateProposalExactReplayDoesNotReadWorkspaceAgain(t *testing.T) {
	base := "base\n"
	baseHash := domain.RawContentHash(base)
	repository := &fakeRepo{}
	targets := &fakeTargets{content: []byte(base), hash: baseHash}
	service := newTestService(repository, targets)
	command := revisionCreateCommand(baseHash, "proposed\n", "replay")
	if _, err := service.CreateProposal(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	targets.err = errors.New("workspace changed after create")
	result, err := service.CreateProposal(context.Background(), command)
	if err != nil || !result.Replayed {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if targets.calls != 1 || repository.createCalls != 1 {
		t.Fatalf("target calls=%d create calls=%d", targets.calls, repository.createCalls)
	}
}

func revisionProposal(base, proposed string) domain.Proposal {
	baseHash := domain.RawContentHash(base)
	revision := domain.Revision{
		ID: "source", ProposalID: "proposal", RevisionNo: 1, TargetPath: "notes/a.md", TargetMode: domain.TargetModeReplace,
		BaseHash: baseHash, Content: proposed, EvidenceSummary: "evidence", Risk: "risk", RollbackPlan: "rollback",
		CreatedAt: time.Unix(1, 0).UTC(),
	}
	revision.ChangeHash = domain.ComputeChangeHash(revision.TargetPath, revision.BaseHash, revision.Content)
	return domain.Proposal{
		ID: "proposal", WorkspaceID: "workspace", Type: domain.ProposalTypeFilePatch, RiskLevel: domain.ProposalRiskLevelLow,
		TargetPath: revision.TargetPath, Status: domain.StatusReady, Version: 2, CurrentRevisionID: revision.ID,
		CreatedAt: revision.CreatedAt, UpdatedAt: revision.CreatedAt, Revision: revision,
	}
}

func revisionCreateCommand(baseHash, content, key string) CreateCommand {
	return CreateCommand{
		WorkspaceID: "workspace", TargetPath: "notes/a.md", BaseHash: baseHash, IdempotencyKey: key,
		Content: content, EvidenceSummary: "evidence", RiskLevel: domain.ProposalRiskLevelLow,
		Risk: "risk", RollbackPlan: "rollback",
	}
}
