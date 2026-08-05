package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitoperation"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
)

const (
	sagaExecutionID  foundation.ID = "10000000-0000-4000-8000-000000000001"
	sagaWorkspaceID  foundation.ID = "20000000-0000-4000-8000-000000000001"
	sagaRunID        foundation.ID = "30000000-0000-4000-8000-000000000001"
	sagaNodeID       foundation.ID = "40000000-0000-4000-8000-000000000001"
	sagaProposalID   foundation.ID = "50000000-0000-4000-8000-000000000001"
	sagaRevisionID   foundation.ID = "60000000-0000-4000-8000-000000000001"
	sagaApprovalID   foundation.ID = "70000000-0000-4000-8000-000000000001"
	sagaWriteAuthID  foundation.ID = "80000000-0000-4000-8000-000000000001"
	sagaGitAuthID    foundation.ID = "90000000-0000-4000-8000-000000000001"
	sagaSecondExecID foundation.ID = "10000000-0000-4000-8000-000000000002"
	sagaDefinitionID foundation.ID = "a0000000-0000-4000-8000-000000000001"
	sagaAttemptID    foundation.ID = "b0000000-0000-4000-8000-000000000001"
	sagaTarget                     = "docs/safe-writeback.md"
	sagaBaseHash                   = "1111111111111111111111111111111111111111111111111111111111111111"
	sagaResultHash                 = "2222222222222222222222222222222222222222222222222222222222222222"
	sagaChangeHash                 = "3333333333333333333333333333333333333333333333333333333333333333"
	sagaGitHead                    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sagaCommit                     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sagaDiffHash                   = "4444444444444444444444444444444444444444444444444444444444444444"
	sagaBaseBlob                   = "cccccccccccccccccccccccccccccccccccccccc"
	sagaResultBlob                 = "dddddddddddddddddddddddddddddddddddddddd"
	sagaLockToken                  = "5555555555555555555555555555555555555555555555555555555555555555"
)

type sagaAuditRecorder struct{}

func (*sagaAuditRecorder) EnsureStarted(context.Context, WritebackResumeIdentity, domain.WritebackExecution, WritebackAuditStep) error {
	return nil
}

func (*sagaAuditRecorder) RequireStarted(context.Context, WritebackResumeIdentity, domain.WritebackExecution, WritebackAuditStep) error {
	return nil
}

func (*sagaAuditRecorder) RecordSucceeded(context.Context, WritebackResumeIdentity, domain.WritebackExecution, WritebackAuditStep) error {
	return nil
}

type sagaPublicationFinalizer struct {
	publications     []WritebackPublication
	validated        []domain.Proposal
	errors           []error
	validationErrors []error
}

func (finalizer *sagaPublicationFinalizer) FinalizePublication(_ context.Context, publication WritebackPublication) error {
	finalizer.publications = append(finalizer.publications, publication)
	if len(finalizer.errors) == 0 {
		return nil
	}
	err := finalizer.errors[0]
	finalizer.errors = finalizer.errors[1:]
	return err
}

func (finalizer *sagaPublicationFinalizer) ValidateWritebackPreparation(_ context.Context, proposal domain.Proposal) error {
	finalizer.validated = append(finalizer.validated, proposal)
	if len(finalizer.validationErrors) == 0 {
		return nil
	}
	err := finalizer.validationErrors[0]
	finalizer.validationErrors = finalizer.validationErrors[1:]
	return err
}

func sagaResumeIdentity(owner string) WritebackResumeIdentity {
	return WritebackResumeIdentity{
		WorkspaceID: sagaWorkspaceID, DefinitionID: sagaDefinitionID, DefinitionVersion: 1,
		DefinitionHash: strings.Repeat("a", 64), WorkflowRunID: sagaRunID, NodeKey: "safe-writeback",
		NodeRunID: sagaNodeID, NodeAttemptID: sagaAttemptID, LeaseOwner: owner, LeaseFence: 1,
	}
}

type sagaIDGenerator struct {
	ids []foundation.ID
	n   int
}

func (g *sagaIDGenerator) New() (foundation.ID, error) {
	if g.n >= len(g.ids) {
		return "", errors.New("no id available")
	}
	id := g.ids[g.n]
	g.n++
	return id, nil
}

type sagaRepository struct {
	proposal           domain.Proposal
	execution          domain.WritebackExecution
	beginCommands      []domain.BeginWriteback
	checkpointStatuses []domain.WritebackStatus
	checkpointErrors   map[domain.WritebackStatus][]error
	leaseErr           error
	leaseErrors        []error
	leaseCalls         int
	publishCommands    []domain.PublishWriteback
	publishErrors      []error
	finalizeCalls      int
}

func (r *sagaRepository) GetProposal(context.Context, foundation.ID) (domain.Proposal, error) {
	return r.proposal, nil
}

func (r *sagaRepository) BeginWriteback(_ context.Context, command domain.BeginWriteback) (domain.WritebackExecution, error) {
	r.beginCommands = append(r.beginCommands, command)
	if r.execution.ID != "" {
		return r.execution, nil
	}
	r.execution = executionFixture(domain.WritebackStatusPrepared)
	r.execution.ID = command.ExecutionID
	return r.execution, nil
}

func (r *sagaRepository) ValidateWritebackLease(context.Context, foundation.ID, string) error {
	r.leaseCalls++
	if len(r.leaseErrors) > 0 {
		err := r.leaseErrors[0]
		r.leaseErrors = r.leaseErrors[1:]
		return err
	}
	return r.leaseErr
}

func (r *sagaRepository) GetWritebackExecution(context.Context, foundation.ID) (domain.WritebackExecution, error) {
	return r.execution, nil
}

func (r *sagaRepository) CheckpointWritebackExecution(_ context.Context, command domain.CheckpointWriteback) (domain.WritebackExecution, error) {
	r.checkpointStatuses = append(r.checkpointStatuses, command.Status)
	if failures := r.checkpointErrors[command.Status]; len(failures) > 0 {
		err := failures[0]
		r.checkpointErrors[command.Status] = failures[1:]
		if err != nil {
			return r.execution, err
		}
	}
	e := r.execution
	e.Status = command.Status
	e.Version++
	e.FailureCode = command.FailureCode
	e.ManualRecoveryRequired = command.ManualRecoveryRequired
	if command.ResultHash != "" {
		e.ResultHash = strings.ToLower(command.ResultHash)
	}
	if command.TemporaryRef != "" {
		e.TemporaryRef = command.TemporaryRef
	}
	if command.BackupRef != "" {
		e.BackupRef = command.BackupRef
	}
	if command.FileByteSize != 0 {
		e.FileByteSize = command.FileByteSize
	}
	if command.FileMode != 0 {
		e.FileMode = command.FileMode
	}
	if command.FileLockToken != "" {
		e.FileLockToken = command.FileLockToken
	}
	if command.FileResultLockToken != "" {
		e.FileResultLockToken = command.FileResultLockToken
	}
	if command.FileBackupLockToken != "" {
		e.FileBackupLockToken = command.FileBackupLockToken
	}
	if command.DiffHash != "" {
		e.DiffHash = strings.ToLower(command.DiffHash)
	}
	if command.BaseBlobID != "" {
		e.BaseBlobID = strings.ToLower(command.BaseBlobID)
	}
	if command.ResultBlobID != "" {
		e.ResultBlobID = strings.ToLower(command.ResultBlobID)
	}
	if command.BaseMode != "" {
		e.BaseMode = command.BaseMode
	}
	if command.GitCommit != "" {
		e.GitCommit = strings.ToLower(command.GitCommit)
	}
	if command.ParentGitCommit != "" {
		e.ParentGitCommit = strings.ToLower(command.ParentGitCommit)
	}
	r.execution = e
	return e, nil
}

func (r *sagaRepository) PublishWriteback(_ context.Context, command domain.PublishWriteback) (domain.PublishWritebackResult, error) {
	r.publishCommands = append(r.publishCommands, command)
	if len(r.publishErrors) > 0 {
		err := r.publishErrors[0]
		r.publishErrors = r.publishErrors[1:]
		if err != nil {
			return domain.PublishWritebackResult{}, err
		}
	}
	r.execution.Status = domain.WritebackStatusVerifying
	r.execution.Version++
	return domain.PublishWritebackResult{Execution: r.execution, Commit: command.Commit}, nil
}

func (r *sagaRepository) FinalizeWritebackCleanup(_ context.Context, _ foundation.ID, _ int64, at time.Time) (domain.WritebackExecution, error) {
	r.finalizeCalls++
	r.execution.CleanupCompletedAt = &at
	r.execution.Version++
	return r.execution, nil
}

type sagaWorkspace struct {
	lock          *sagaTargetLock
	acquireCalls  int
	resumeCalls   int
	resumes       []domain.ResumeWrite
	resumeErr     error
	resumePending bool
}

func (w *sagaWorkspace) AcquireTarget(context.Context, foundation.ID, string) (domain.TargetLock, error) {
	w.acquireCalls++
	return w.lock, nil
}

func (w *sagaWorkspace) ResumeTarget(_ context.Context, _ foundation.ID, _ string, resume domain.ResumeWrite) (domain.TargetLock, domain.PreparedWrite, *domain.AppliedWrite, error) {
	w.resumeCalls++
	w.resumes = append(w.resumes, resume)
	if w.resumeErr != nil {
		return nil, domain.PreparedWrite{}, nil, w.resumeErr
	}
	prepared := resume.Prepared
	if prepared.ExecutionID == "" {
		prepared = w.lock.prepared
	}
	if w.resumePending {
		return w.lock, prepared, nil, nil
	}
	applied := w.lock.applied
	return w.lock, prepared, &applied, nil
}

type sagaTargetLock struct {
	prepared     domain.PreparedWrite
	applied      domain.AppliedWrite
	prepareCalls int
	commitCalls  int
	restoreCalls int
	cleanupCalls int
	closeCalls   int
}

func (l *sagaTargetLock) Prepare(context.Context, domain.PrepareWrite) (domain.PreparedWrite, error) {
	l.prepareCalls++
	return l.prepared, nil
}

func (l *sagaTargetLock) CommitCAS(context.Context, domain.PreparedWrite) (domain.AppliedWrite, error) {
	l.commitCalls++
	return l.applied, nil
}

func (l *sagaTargetLock) RestoreCAS(context.Context, domain.AppliedWrite) (domain.RestoreResult, error) {
	l.restoreCalls++
	return domain.RestoreResult{Restored: true}, nil
}

func (l *sagaTargetLock) Cleanup(context.Context, domain.AppliedWrite) error {
	l.cleanupCalls++
	return nil
}

func (l *sagaTargetLock) Close() error {
	l.closeCalls++
	return nil
}

type sagaGit struct {
	inspectCalls int
	diffCalls    int
	findCalls    int
	commitCalls  int
	commit       *domain.GitCommit
	commitErr    error
}

func (g *sagaGit) Inspect(_ context.Context, workspaceID foundation.ID, approvedHead string) (domain.GitSnapshot, error) {
	g.inspectCalls++
	return domain.GitSnapshot{WorkspaceID: workspaceID, Branch: "main", Head: approvedHead, ObjectFormat: domain.GitObjectFormatSHA1, Clean: true}, nil
}

func (g *sagaGit) DiffApproved(_ context.Context, request domain.GitDiffRequest) (domain.GitDiff, error) {
	g.diffCalls++
	return domain.GitDiff{WorkspaceID: request.WorkspaceID, TargetPath: request.TargetPath, ApprovedGitHead: request.ApprovedGitHead, ResultHash: request.ResultHash, DiffHash: sagaDiffHash, BaseBlobID: sagaBaseBlob, ResultBlobID: sagaResultBlob, BaseMode: domain.GitFileModeRegular}, nil
}

func (g *sagaGit) CommitApproved(_ context.Context, request domain.GitCommitRequest) (domain.GitCommit, error) {
	g.commitCalls++
	if g.commitErr != nil {
		return domain.GitCommit{}, g.commitErr
	}
	commit := commitFixture(request, false, false)
	g.commit = &commit
	return commit, nil
}

func (g *sagaGit) FindWritebackCommit(_ context.Context, lookup domain.GitCommitLookup) (domain.GitCommit, error) {
	g.findCalls++
	if g.commit == nil {
		return domain.GitCommit{}, foundation.NewError(foundation.ErrorNotFound, "GIT_COMMIT_NOT_FOUND", false, domain.ErrGitNotFound)
	}
	commit := *g.commit
	commit.Recovered = true
	commit.Replayed = false
	return commit, nil
}

func (g *sagaGit) CreateReverseCommit(context.Context, domain.ReverseCommitRequest) (domain.GitCommit, error) {
	return domain.GitCommit{}, errors.New("not implemented")
}

func TestWritebackBeginDerivesAuthorizationBindingsAndReplays(t *testing.T) {
	proposal := proposalFixture(domain.StatusApproved)
	repository := &sagaRepository{proposal: proposal}
	service := newSagaService(t, repository, &sagaWorkspace{lock: targetLockFixture(t)}, &sagaGit{}, &sagaIDGenerator{ids: []foundation.ID{sagaExecutionID, sagaSecondExecID}})
	command := BeginWritebackCommand{
		WorkspaceID: sagaWorkspaceID, WorkflowRunID: sagaRunID, NodeRunID: sagaNodeID, ProposalID: sagaProposalID,
		LeaseOwner: "worker-a", IdempotencyKey: "begin-1", WriteCredential: "write-secret", GitCredential: "git-secret",
		WriteAuthorizationKey: "write-auth-1", GitAuthorizationKey: "git-auth-1",
	}
	first, err := service.Begin(context.Background(), command)
	if err != nil || first.ExecutionID != sagaExecutionID || first.Replayed {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	repository.proposal.Status = domain.StatusApplying
	second, err := service.Begin(context.Background(), command)
	if err != nil || second.ExecutionID != sagaExecutionID || !second.Replayed {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	begin := repository.beginCommands[0]
	if begin.WriteAuthorization.ToolName != "ApplyApprovedPatch" || begin.WriteAuthorization.Capability != domain.CapabilityWriteKnowledge || begin.GitAuthorization.ToolName != "CreateGitCommit" || begin.GitAuthorization.Capability != domain.CapabilityGitWrite || begin.WriteAuthorization.Scope != "target:"+sagaTarget || begin.WriteAuthorization.ApprovedChangeHash != proposal.Revision.ChangeHash || begin.WriteAuthorization.TargetVersion != proposal.Revision.BaseHash {
		t.Fatalf("begin command=%#v", begin)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), sagaTarget) {
		t.Fatalf("safe result leaked sensitive data: %s", encoded)
	}
}

func TestWritebackBeginRejectsOversizedCredentialBeforeRepositoryLookup(t *testing.T) {
	repository := &sagaRepository{proposal: proposalFixture(domain.StatusApproved)}
	service := newSagaService(t, repository, &sagaWorkspace{lock: targetLockFixture(t)}, &sagaGit{}, &sagaIDGenerator{ids: []foundation.ID{sagaExecutionID}})
	command := BeginWritebackCommand{
		WorkspaceID: sagaWorkspaceID, WorkflowRunID: sagaRunID, NodeRunID: sagaNodeID, ProposalID: sagaProposalID,
		LeaseOwner: "worker-a", IdempotencyKey: "begin-1", WriteCredential: strings.Repeat("x", domain.MaxAuthorizationCredentialBytes+1), GitCredential: "git-secret",
		WriteAuthorizationKey: "write-auth-1", GitAuthorizationKey: "git-auth-1",
	}
	_, err := service.Begin(context.Background(), command)
	var applicationError *foundation.Error
	if !errors.As(err, &applicationError) || applicationError.Code != "WRITEBACK_BEGIN_INVALID" {
		t.Fatalf("oversized credential error=%v", err)
	}
	if len(repository.beginCommands) != 0 {
		t.Fatal("oversized credential reached repository")
	}
}

func TestWritebackResumeCompletesStrictSagaAndCleanup(t *testing.T) {
	repository := &sagaRepository{proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusPrepared)}
	lock := targetLockFixture(t)
	workspace := &sagaWorkspace{lock: lock}
	git := &sagaGit{}
	publication := &sagaPublicationFinalizer{}
	service := newSagaServiceWithFinalizer(t, repository, workspace, git, publication, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err != nil {
		t.Fatal(err)
	}
	wantStatuses := []domain.WritebackStatus{
		domain.WritebackStatusFilePrepared, domain.WritebackStatusFileApplied, domain.WritebackStatusGitPrepared, domain.WritebackStatusGitCommitted,
	}
	if strings.TrimSpace(result.GitCommit) != sagaCommit || result.Status != domain.WritebackStatusVerifying || result.IndexStatus != WritebackIndexStatusPending || result.CleanupPending || repository.finalizeCalls != 1 || git.commitCalls != 1 || len(repository.publishCommands) != 1 || lock.cleanupCalls != 1 || len(publication.publications) != 1 {
		t.Fatalf("result=%#v checkpoints=%v commit=%d publish=%d cleanup=%d finalize=%d", result, repository.checkpointStatuses, git.commitCalls, len(repository.publishCommands), lock.cleanupCalls, repository.finalizeCalls)
	}
	finalized := publication.publications[0]
	if finalized.WorkspaceID != sagaWorkspaceID || finalized.ProposalID != sagaProposalID ||
		finalized.ProposalRevisionID != sagaRevisionID || finalized.WritebackID != sagaExecutionID ||
		finalized.GitCommit != sagaCommit || finalized.ResultHash != result.ResultHash {
		t.Fatalf("publication=%#v", finalized)
	}
	if len(repository.checkpointStatuses) < len(wantStatuses) {
		t.Fatalf("checkpoints=%v", repository.checkpointStatuses)
	}
	for index, status := range wantStatuses {
		if repository.checkpointStatuses[index] != status {
			t.Fatalf("checkpoint[%d]=%s want=%s all=%v", index, repository.checkpointStatuses[index], status, repository.checkpointStatuses)
		}
	}
}

func TestRestoreWritebackRevalidatesDocumentOwnerBeforePreparingFile(t *testing.T) {
	proposal := proposalFixture(domain.StatusApplying)
	proposal.Type = domain.ProposalTypeRestoreDocument
	proposal.Revision.RestoreDocument = &domain.RestoreDocument{
		WorkspaceID: sagaWorkspaceID, DocumentID: "50000000-0000-4000-8000-000000000002",
		TargetCommit: strings.Repeat("e", 40), ExpectedHead: sagaGitHead,
		ExpectedDocumentVersion: 7, PreviewHash: strings.Repeat("f", 64),
		CurrentContentHash: proposal.Revision.BaseHash,
		TargetContentHash:  domain.ComputeContentHash([]byte(proposal.Revision.Content)),
		SchemaVersion:      domain.RestoreDocumentSchemaVersion,
	}
	repository := &sagaRepository{proposal: proposal, execution: executionFixture(domain.WritebackStatusPrepared)}
	lock := targetLockFixture(t)
	workspace := &sagaWorkspace{lock: lock}
	stale := foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("document changed after approval"))
	publication := &sagaPublicationFinalizer{validationErrors: []error{stale}}
	service := newSagaServiceWithFinalizer(t, repository, workspace, &sagaGit{}, publication, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})

	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if !errors.Is(err, stale) || result.Status != domain.WritebackStatusNeedsRevision ||
		workspace.acquireCalls != 1 || lock.prepareCalls != 0 || len(publication.validated) != 1 {
		t.Fatalf("result=%#v err=%v acquire=%d prepare=%d validated=%d checkpoints=%v",
			result, err, workspace.acquireCalls, lock.prepareCalls, len(publication.validated), repository.checkpointStatuses)
	}
}

func TestRestoreWritebackRevalidatesDocumentOwnerBeforeResumingPreparedFile(t *testing.T) {
	proposal := proposalFixture(domain.StatusApplying)
	proposal.Type = domain.ProposalTypeRestoreDocument
	proposal.Revision.RestoreDocument = &domain.RestoreDocument{
		WorkspaceID: sagaWorkspaceID, DocumentID: "50000000-0000-4000-8000-000000000002",
		TargetCommit: strings.Repeat("e", 40), ExpectedHead: sagaGitHead,
		ExpectedDocumentVersion: 7, PreviewHash: strings.Repeat("f", 64),
		CurrentContentHash: proposal.Revision.BaseHash,
		TargetContentHash:  domain.ComputeContentHash([]byte(proposal.Revision.Content)),
		SchemaVersion:      domain.RestoreDocumentSchemaVersion,
	}
	repository := &sagaRepository{proposal: proposal, execution: executionFixture(domain.WritebackStatusFilePrepared)}
	lock := targetLockFixture(t)
	workspace := &sagaWorkspace{lock: lock, resumePending: true}
	stale := foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("document changed after file preparation"))
	publication := &sagaPublicationFinalizer{validationErrors: []error{stale}}
	service := newSagaServiceWithFinalizer(t, repository, workspace, &sagaGit{}, publication, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})

	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if !errors.Is(err, stale) || result.Status != domain.WritebackStatusNeedsRevision ||
		workspace.resumeCalls != 1 || lock.commitCalls != 0 || len(publication.validated) != 1 {
		t.Fatalf("result=%#v err=%v resume=%d commit=%d validated=%d checkpoints=%v",
			result, err, workspace.resumeCalls, lock.commitCalls, len(publication.validated), repository.checkpointStatuses)
	}
}

func TestWritebackPublicationFinalizerFailureRetriesWithoutSecondGitCommit(t *testing.T) {
	repository := &sagaRepository{proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusVerifying)}
	lock := targetLockFixture(t)
	publication := &sagaPublicationFinalizer{errors: []error{errors.New("authoring unavailable"), nil}}
	service := newSagaServiceWithFinalizer(t, repository, &sagaWorkspace{lock: lock}, &sagaGit{}, publication, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})

	first, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err == nil || first.Status != domain.WritebackStatusVerifying || repository.finalizeCalls != 0 || len(publication.publications) != 1 {
		t.Fatalf("first=%#v err=%v cleanup-finalize=%d publication=%d", first, err, repository.finalizeCalls, len(publication.publications))
	}
	second, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err != nil || second.CleanupPending || repository.finalizeCalls != 1 || len(publication.publications) != 2 || lock.cleanupCalls != 2 {
		t.Fatalf("second=%#v err=%v cleanup=%d cleanup-finalize=%d publication=%d", second, err, lock.cleanupCalls, repository.finalizeCalls, len(publication.publications))
	}
}

func TestWritebackRejectsDownstreamUpdateWithoutSideEffects(t *testing.T) {
	assertUnavailable := func(t *testing.T, err error) {
		t.Helper()
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != domain.DownstreamUpdateApplyUnavailableCode || classified.Kind != foundation.ErrorVersionConflict || classified.Retryable {
			t.Fatalf("error=%v", err)
		}
	}

	t.Run("begin", func(t *testing.T) {
		proposal := downstreamUpdateProposal()
		repository := &sagaRepository{proposal: proposal}
		workspace := &sagaWorkspace{lock: targetLockFixture(t)}
		git := &sagaGit{}
		service := newSagaService(t, repository, workspace, git, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})

		_, err := service.Begin(context.Background(), BeginWritebackCommand{
			WorkspaceID: proposal.WorkspaceID, WorkflowRunID: sagaRunID, NodeRunID: sagaNodeID, ProposalID: proposal.ID,
			LeaseOwner: "worker-a", IdempotencyKey: "downstream-begin", WriteCredential: "write-secret", GitCredential: "git-secret",
			WriteAuthorizationKey: "downstream-write", GitAuthorizationKey: "downstream-git",
		})
		assertUnavailable(t, err)
		if len(repository.beginCommands) != 0 || workspace.acquireCalls != 0 || git.inspectCalls != 0 {
			t.Fatalf("begin reached a writeback dependency: commands=%d acquire=%d inspect=%d", len(repository.beginCommands), workspace.acquireCalls, git.inspectCalls)
		}
	})

	t.Run("resume historical execution", func(t *testing.T) {
		proposal := downstreamUpdateProposal()
		execution := executionFixture(domain.WritebackStatusPrepared)
		execution.ProposalID = proposal.ID
		repository := &sagaRepository{proposal: proposal, execution: execution}
		workspace := &sagaWorkspace{lock: targetLockFixture(t)}
		git := &sagaGit{}
		service := newSagaService(t, repository, workspace, git, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})

		result, err := service.Resume(context.Background(), execution.ID, sagaResumeIdentity("worker-a"))
		assertUnavailable(t, err)
		if result.ExecutionID != execution.ID || len(repository.checkpointStatuses) != 0 || workspace.acquireCalls != 0 || workspace.resumeCalls != 0 || git.inspectCalls != 0 || git.commitCalls != 0 {
			t.Fatalf("resume reached a writeback dependency: result=%#v checkpoints=%v acquire=%d resume=%d inspect=%d commit=%d", result, repository.checkpointStatuses, workspace.acquireCalls, workspace.resumeCalls, git.inspectCalls, git.commitCalls)
		}
	})
}

func TestWritebackResumeMapsResponseLossIntentByStatus(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      domain.WritebackStatus
		wantCleanup bool
		wantRestore bool
	}{
		{name: "verifying cleanup", status: domain.WritebackStatusVerifying, wantCleanup: true},
		{name: "compensating restore", status: domain.WritebackStatusCompensatingFile, wantRestore: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &sagaRepository{proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(test.status)}
			workspace := &sagaWorkspace{lock: targetLockFixture(t)}
			service := newSagaService(t, repository, workspace, &sagaGit{}, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})

			_, _ = service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
			if len(workspace.resumes) == 0 {
				t.Fatal("resume intent was not sent to workspace")
			}
			resume := workspace.resumes[0]
			if resume.CleanupMayHaveCompleted != test.wantCleanup || resume.RestoreMayHaveCompleted != test.wantRestore {
				t.Fatalf("resume intent = cleanup:%v restore:%v", resume.CleanupMayHaveCompleted, resume.RestoreMayHaveCompleted)
			}
		})
	}
}

func TestWritebackCommitCheckpointCrashRecoversByLookupWithoutSecondCommit(t *testing.T) {
	repository := &sagaRepository{
		proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusGitPrepared),
		checkpointErrors: map[domain.WritebackStatus][]error{
			domain.WritebackStatusGitCommitted: {foundation.NewError(foundation.ErrorRetryableFailure, "WRITEBACK_CHECKPOINT_FAILED", true, errors.New("lost response"))},
		},
	}
	lock := targetLockFixture(t)
	git := &sagaGit{}
	service := newSagaService(t, repository, &sagaWorkspace{lock: lock}, git, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	if _, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a")); err == nil {
		t.Fatal("checkpoint failure was not returned")
	}
	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err != nil || result.Status != domain.WritebackStatusVerifying || git.commitCalls != 1 || git.findCalls < 2 {
		t.Fatalf("result=%#v err=%v commit=%d find=%d", result, err, git.commitCalls, git.findCalls)
	}
}

func TestWritebackCommitUnknownMovesToManualWithoutFileRestore(t *testing.T) {
	repository := &sagaRepository{proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusGitPrepared)}
	lock := targetLockFixture(t)
	git := &sagaGit{commitErr: foundation.NewError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, domain.ErrGitManualRecoveryRequired)}
	service := newSagaService(t, repository, &sagaWorkspace{lock: lock}, git, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err == nil || result.Status != domain.WritebackStatusManualRecovery || lock.restoreCalls != 0 || git.commitCalls != 1 {
		t.Fatalf("result=%#v err=%v restore=%d commit=%d", result, err, lock.restoreCalls, git.commitCalls)
	}
}

func TestWritebackCommitVersionConflictAfterLookupMissDoesNotRestoreFile(t *testing.T) {
	repository := &sagaRepository{proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusGitPrepared)}
	lock := targetLockFixture(t)
	git := &sagaGit{commitErr: foundation.NewError(foundation.ErrorVersionConflict, "GIT_HEAD_CONFLICT", false, domain.ErrGitVersionConflict)}
	service := newSagaService(t, repository, &sagaWorkspace{lock: lock}, git, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err == nil || result.Status != domain.WritebackStatusManualRecovery || lock.restoreCalls != 0 || git.commitCalls != 1 {
		t.Fatalf("result=%#v err=%v restore=%d commit=%d", result, err, lock.restoreCalls, git.commitCalls)
	}
}

func TestWritebackPublishFailureNeverRestoresFile(t *testing.T) {
	repository := &sagaRepository{
		proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusGitCommitted),
		publishErrors: []error{foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_OUTBOX_CONFLICT", false, domain.ErrWritebackPublishBindingConflict)},
	}
	lock := targetLockFixture(t)
	service := newSagaService(t, repository, &sagaWorkspace{lock: lock}, &sagaGit{}, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err == nil || result.Status != domain.WritebackStatusPublishRecovery || lock.restoreCalls != 0 || len(repository.publishCommands) != 1 {
		t.Fatalf("result=%#v err=%v restore=%d publish=%d", result, err, lock.restoreCalls, len(repository.publishCommands))
	}
}

func TestWritebackLeaseLossStopsBeforeNewSideEffect(t *testing.T) {
	repository := &sagaRepository{
		proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusPrepared),
		leaseErr: foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, domain.ErrWritebackLeaseLost),
	}
	workspace := &sagaWorkspace{lock: targetLockFixture(t)}
	git := &sagaGit{}
	service := newSagaService(t, repository, workspace, git, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	if _, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a")); err == nil || workspace.acquireCalls != 0 || git.inspectCalls != 0 {
		t.Fatalf("err=%v acquire=%d inspect=%d", err, workspace.acquireCalls, git.inspectCalls)
	}
}

func TestWritebackLeaseLossAtDurableFileStateDoesNotBecomeManual(t *testing.T) {
	repository := &sagaRepository{
		proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusGitPrepared),
		leaseErr: foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, domain.ErrWritebackLeaseLost),
	}
	workspace := &sagaWorkspace{lock: targetLockFixture(t)}
	service := newSagaService(t, repository, workspace, &sagaGit{}, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err == nil || result.Status != domain.WritebackStatusGitPrepared || repository.execution.Status != domain.WritebackStatusGitPrepared || len(repository.checkpointStatuses) != 0 || workspace.resumeCalls != 0 {
		t.Fatalf("result=%#v execution=%#v checkpoints=%v resume=%d err=%v", result, repository.execution, repository.checkpointStatuses, workspace.resumeCalls, err)
	}
}

func TestWritebackLeaseLossAfterAcquireStopsBeforePrepare(t *testing.T) {
	leaseLost := foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, domain.ErrWritebackLeaseLost)
	repository := &sagaRepository{
		proposal: proposalFixture(domain.StatusApplying), execution: executionFixture(domain.WritebackStatusPrepared),
		leaseErrors: []error{nil, nil, leaseLost},
	}
	lock := targetLockFixture(t)
	workspace := &sagaWorkspace{lock: lock}
	service := newSagaService(t, repository, workspace, &sagaGit{}, &sagaIDGenerator{ids: []foundation.ID{sagaSecondExecID}})
	result, err := service.Resume(context.Background(), sagaExecutionID, sagaResumeIdentity("worker-a"))
	if err == nil || result.Status != domain.WritebackStatusPrepared || workspace.acquireCalls != 1 || lock.prepareCalls != 0 || len(repository.checkpointStatuses) != 0 {
		t.Fatalf("result=%#v acquire=%d prepare=%d checkpoints=%v err=%v", result, workspace.acquireCalls, lock.prepareCalls, repository.checkpointStatuses, err)
	}
}

func TestBuildPublishWritebackUsesStableIDsAndPayload(t *testing.T) {
	execution := executionFixture(domain.WritebackStatusGitCommitted)
	first, err := buildPublishWriteback(execution, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildPublishWriteback(execution, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.Commit.ID != second.Commit.ID || first.Event.ID != second.Event.ID || string(first.Event.Payload) != string(second.Event.Payload) || first.Commit.ID == first.Event.ID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	request, err := reindexcontract.DecodeStrict(first.Event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := reindexcontract.ValidateBinding(request, reindexcontract.Binding{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.WorkflowRunID, NodeRunID: execution.NodeRunID,
		ProposalID: execution.ProposalID, RevisionID: execution.RevisionID, ApprovalID: execution.ApprovalID,
		WritebackExecutionID: execution.ID, TargetPath: execution.TargetPath,
		ResultHash: execution.ResultHash, GitCommit: execution.GitCommit,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPublishWritebackPreservesEncodingErrorContract(t *testing.T) {
	execution := executionFixture(domain.WritebackStatusGitCommitted)
	execution.TargetPath = "notes/../unsafe.md"
	_, err := buildPublishWriteback(execution, time.Unix(1, 0))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNonRetryableFailure || classified.Code != "WRITEBACK_OUTBOX_ENCODING_FAILED" {
		t.Fatalf("error=%#v", err)
	}
}

func newSagaService(t *testing.T, repository *sagaRepository, workspace *sagaWorkspace, git *sagaGit, ids foundation.IDGenerator) *WritebackService {
	return newSagaServiceWithFinalizer(t, repository, workspace, git, &sagaPublicationFinalizer{}, ids)
}

func newSagaServiceWithFinalizer(t *testing.T, repository *sagaRepository, workspace *sagaWorkspace, git *sagaGit, publication WritebackPublicationFinalizer, ids foundation.IDGenerator) *WritebackService {
	t.Helper()
	service, err := NewWritebackService(WritebackServiceDependencies{
		Repository: repository, Workspace: workspace, Git: git, GitOperations: sagaGitOperationLocker{}, Audit: &sagaAuditRecorder{}, IDs: ids,
		Publication: publication, Clock: foundation.FixedClock{Value: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type sagaGitOperationLocker struct{}

func (sagaGitOperationLocker) Acquire(context.Context, foundation.ID) (gitoperation.Lease, error) {
	return sagaGitOperationLease{}, nil
}

type sagaGitOperationLease struct{}

func (sagaGitOperationLease) Release(context.Context) error { return nil }

func proposalFixture(status domain.ProposalStatus) domain.Proposal {
	content := "# approved\n"
	changeHash := domain.ComputeChangeHash(sagaTarget, sagaBaseHash, content)
	approvedGitHead := sagaGitHead
	return domain.Proposal{
		ID: sagaProposalID, WorkspaceID: sagaWorkspaceID, TargetPath: sagaTarget, Status: status,
		Revision: domain.Revision{ID: sagaRevisionID, ProposalID: sagaProposalID, TargetPath: sagaTarget, BaseHash: sagaBaseHash, Content: content, ChangeHash: changeHash},
		Approval: &domain.Approval{ID: sagaApprovalID, ProposalID: sagaProposalID, RevisionID: sagaRevisionID, ChangeHash: changeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead},
	}
}

func executionFixture(status domain.WritebackStatus) domain.WritebackExecution {
	proposal := proposalFixture(domain.StatusApplying)
	prepared := preparedFixture(sagaExecutionID)
	execution := domain.WritebackExecution{
		ID: sagaExecutionID, WorkspaceID: sagaWorkspaceID, WorkflowRunID: sagaRunID, NodeRunID: sagaNodeID,
		ProposalID: sagaProposalID, RevisionID: sagaRevisionID, ApprovalID: sagaApprovalID,
		WriteAuthorizationID: sagaWriteAuthID, GitAuthorizationID: sagaGitAuthID,
		TargetPath: sagaTarget, BaseHash: sagaBaseHash, ResultHash: domain.ComputeWritebackResultHash([]byte(proposal.Revision.Content)),
		ApprovedChangeHash: proposal.Revision.ChangeHash, ApprovedGitHead: sagaGitHead,
		Status: status, IdempotencyKey: "writeback-1", Version: 1,
	}
	if status != domain.WritebackStatusPrepared {
		execution.TemporaryRef, execution.BackupRef = prepared.TemporaryRef, prepared.BackupRef
		execution.FileByteSize, execution.FileMode, execution.FileLockToken = prepared.ByteSize, prepared.Mode, prepared.LockToken
		execution.FileResultLockToken, execution.FileBackupLockToken = prepared.ResultLockToken, prepared.BackupLockToken
	}
	if status == domain.WritebackStatusGitPrepared || status == domain.WritebackStatusGitCommitted || status == domain.WritebackStatusPublishRecovery || status == domain.WritebackStatusVerifying {
		execution.DiffHash, execution.BaseBlobID, execution.ResultBlobID, execution.BaseMode = sagaDiffHash, sagaBaseBlob, sagaResultBlob, domain.GitFileModeRegular
	}
	if status == domain.WritebackStatusGitCommitted || status == domain.WritebackStatusPublishRecovery || status == domain.WritebackStatusVerifying {
		execution.GitCommit, execution.ParentGitCommit = sagaCommit, sagaGitHead
	}
	return execution
}

func preparedFixture(executionID foundation.ID) domain.PreparedWrite {
	prefix, err := domain.WritebackLocatorPrefix(executionID, sagaLockToken)
	if err != nil {
		panic(err)
	}
	return domain.PreparedWrite{
		ExecutionID: executionID, TemporaryRef: "docs/" + prefix + strings.Repeat("1", 32) + ".tmp",
		BackupRef: "docs/" + prefix + strings.Repeat("2", 32) + ".bak", ExpectedBaseHash: sagaBaseHash,
		ApprovedChangeHash: proposalFixture(domain.StatusApplying).Revision.ChangeHash,
		ResultHash:         domain.ComputeWritebackResultHash([]byte(proposalFixture(domain.StatusApplying).Revision.Content)), ByteSize: int64(len(proposalFixture(domain.StatusApplying).Revision.Content)), Mode: 0o644, LockToken: sagaLockToken,
		ResultLockToken: strings.Repeat("d", 64), BackupLockToken: strings.Repeat("e", 64),
	}
}

func targetLockFixture(t *testing.T) *sagaTargetLock {
	t.Helper()
	prepared := preparedFixture(sagaExecutionID)
	applied := domain.AppliedWrite{
		ExecutionID: prepared.ExecutionID, TemporaryRef: prepared.TemporaryRef, BackupRef: prepared.BackupRef,
		BaseHash: prepared.ExpectedBaseHash, ApprovedChangeHash: prepared.ApprovedChangeHash, ResultHash: prepared.ResultHash,
		ByteSize: prepared.ByteSize, Mode: prepared.Mode, LockToken: prepared.LockToken,
		ResultLockToken: prepared.ResultLockToken, BackupLockToken: prepared.BackupLockToken,
	}
	return &sagaTargetLock{prepared: prepared, applied: applied}
}

func commitFixture(request domain.GitCommitRequest, recovered, replayed bool) domain.GitCommit {
	return domain.GitCommit{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
		WritebackExecutionID: request.WritebackExecutionID, ProposalID: request.ProposalID, RevisionID: request.RevisionID, ApprovalID: request.ApprovalID,
		Operation: request.Operation, TargetPath: request.TargetPath, ApprovedGitHead: request.ApprovedGitHead,
		GitCommit: sagaCommit, ParentGitCommit: request.ApprovedGitHead, ResultHash: request.ResultHash, DiffHash: request.DiffHash,
		BaseBlobID: request.BaseBlobID, ResultBlobID: request.ResultBlobID, BaseMode: request.BaseMode,
		Recovered: recovered, Replayed: replayed,
	}
}
