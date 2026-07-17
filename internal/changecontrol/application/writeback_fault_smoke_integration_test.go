package application

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestWritebackSagaRealFaultSmoke 在真实 PostgreSQL、LocalFS 和 Git 上模拟
// “响应已提交但调用方未收到”的四个检查点，验证销毁旧 Service/Writer 后仍可恢复。
func TestWritebackSagaRealFaultSmoke(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Safe Writeback v1 smoke requires local POSIX filesystem semantics")
	}
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ids := foundation.NewUUIDGenerator(nil)
	nextID := func() foundation.ID {
		id, idErr := ids.New()
		if idErr != nil {
			t.Fatal(idErr)
		}
		return id
	}
	workspaceID, definitionID, runID, nodeID := nextID(), nextID(), nextID(), nextID()
	root := t.TempDir()
	targetPath := "docs/fault-smoke.md"
	baseContent := []byte("# Fault Smoke\n\nbase\n")
	approvedContent := "# Fault Smoke\n\napproved\n"
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(targetPath)), baseContent, 0o644); err != nil {
		t.Fatal(err)
	}
	runFaultSmokeGit(t, ctx, root, "init", "--initial-branch=main")
	runFaultSmokeGit(t, ctx, root, "config", "user.name", "ZHIXU Fault Smoke")
	runFaultSmokeGit(t, ctx, root, "config", "user.email", "fault-smoke@example.invalid")
	runFaultSmokeGit(t, ctx, root, "add", "--", targetPath)
	runFaultSmokeGit(t, ctx, root, "commit", "-m", "base")
	baseGitHead := strings.TrimSpace(runFaultSmokeGit(t, ctx, root, "rev-parse", "HEAD"))

	workspaceRepository, err := workspacepostgres.NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	databaseNow = databaseNow.UTC()
	if _, err := workspaceRepository.CreateWorkspace(ctx, workspacedomain.Workspace{
		ID: workspaceID, Name: "Safe Writeback Fault Smoke", RootPath: root,
		Git:    workspacedomain.GitBaseline{RepositoryPath: root, Branch: "main", Head: baseGitHead, CheckedAt: databaseNow},
		Status: workspacedomain.WorkspaceStatusActive, Version: 1, CreatedAt: databaseNow, UpdatedAt: databaseNow,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,'{}',$4)`, string(definitionID), string(workspaceID), "fault-smoke-"+string(definitionID), databaseNow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(workspaceID), string(definitionID), databaseNow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,version,created_at,updated_at) VALUES($1,$2,'safe-writeback','side_effect','running',1,'{}','fault-worker',$3,1,$4,$4)`, string(nodeID), string(runID), databaseNow.Add(10*time.Minute), databaseNow); err != nil {
		t.Fatal(err)
	}

	changeRepository, err := changecontrolpostgres.NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	targetReader, err := changecontrollocalfs.NewReader(workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	gitRepository, err := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	changeService, err := NewService(changeRepository, ids, foundation.FixedClock{Value: databaseNow}, targetReader, gitRepository)
	if err != nil {
		t.Fatal(err)
	}
	baseHash := domain.ComputeWritebackResultHash(baseContent)
	created, err := changeService.CreateProposal(ctx, CreateCommand{
		WorkspaceID: workspaceID, TargetPath: targetPath, IdempotencyKey: "fault-proposal-" + string(nextID()),
		BaseHash: baseHash, Content: approvedContent, EvidenceSummary: "fault smoke evidence", Risk: "low", RollbackPlan: "strict reverse commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := changeService.DecideProposal(ctx, created.Proposal.ID, created.Proposal.Revision.ID, created.Proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || approval.ApprovedGitHead == nil || *approval.ApprovedGitHead != baseGitHead {
		t.Fatalf("approval=%#v err=%v", approval, err)
	}
	// Direct-node smoke bypasses ApprovalDispatch, so bind the approved Proposal
	// to the already-created Workflow Run exactly as the production UoW does.
	if _, err := tx.Exec(ctx, `UPDATE change_control.proposal SET workflow_run_id=$2,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND workflow_run_id IS NULL`, string(created.Proposal.ID), string(runID)); err != nil {
		t.Fatal(err)
	}
	issue := func(tool string, capability domain.Capability, key string) domain.AuthorizationIssueResult {
		result, issueErr := changeService.IssueWriteAuthorization(ctx, domain.AuthorizationIssue{
			WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID,
			ProposalID: created.Proposal.ID, RevisionID: created.Proposal.Revision.ID, ApprovalID: approval.ID,
			ToolName: tool, Capability: capability, Scope: domain.ExpectedAuthorizationScope(targetPath), IdempotencyKey: key, TTL: 2 * time.Minute,
		})
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return result
	}
	writeAuthorization := issue("ApplyApprovedPatch", domain.CapabilityWriteKnowledge, "fault-write-auth-"+string(nextID()))
	gitAuthorization := issue("CreateGitCommit", domain.CapabilityGitWrite, "fault-git-auth-"+string(nextID()))

	beginCommand := BeginWritebackCommand{
		WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: created.Proposal.ID,
		LeaseOwner: "fault-worker", IdempotencyKey: "fault-writeback-" + string(nextID()),
		WriteCredential: writeAuthorization.Credential, GitCredential: gitAuthorization.Credential,
		WriteAuthorizationKey: writeAuthorization.Authorization.IdempotencyKey, GitAuthorizationKey: gitAuthorization.Authorization.IdempotencyKey,
	}
	realService := func(repository writebackRepository) *WritebackService {
		validator, validatorErr := changecontrollocalfs.NewDefaultMarkdownValidator()
		if validatorErr != nil {
			t.Fatal(validatorErr)
		}
		workspaceStore, writerErr := changecontrollocalfs.NewWriter(workspaceRepository, validator)
		if writerErr != nil {
			t.Fatal(writerErr)
		}
		gitClient, gitErr := gitcli.NewWritebackClient(gitcli.New(""), workspaceRepository)
		if gitErr != nil {
			t.Fatal(gitErr)
		}
		service, serviceErr := NewWritebackService(WritebackServiceDependencies{
			Repository: repository, Workspace: workspaceStore, Git: gitClient,
			IDs: ids, Clock: foundation.FixedClock{Value: databaseNow.Add(time.Minute)},
		})
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		return service
	}

	beginService := realService(changeRepository)
	begin, err := beginService.Begin(ctx, beginCommand)
	if err != nil || begin.ExecutionID == "" {
		t.Fatalf("begin=%#v err=%v", begin, err)
	}

	// file_prepared 已经提交，但响应丢失；销毁旧 Service/Writer 后从 durable checkpoint 继续。
	filePreparedFault := &faultWritebackRepository{writebackRepository: changeRepository, checkpointStatus: domain.WritebackStatusFilePrepared}
	if _, err := realService(filePreparedFault).Resume(ctx, begin.ExecutionID, "fault-worker"); !isRetryableWritebackError(err) {
		t.Fatalf("file_prepared response loss err=%v", err)
	}
	filePrepared, err := changeRepository.GetWritebackExecution(ctx, begin.ExecutionID)
	if err != nil || filePrepared.Status != domain.WritebackStatusFilePrepared {
		t.Fatalf("file_prepared=%#v err=%v", filePrepared, err)
	}

	// git_prepared 已经提交，但响应丢失；下一实例必须 exact lookup 后只创建一个 Commit。
	gitPreparedFault := &faultWritebackRepository{writebackRepository: changeRepository, checkpointStatus: domain.WritebackStatusGitPrepared}
	if _, err := realService(gitPreparedFault).Resume(ctx, begin.ExecutionID, "fault-worker"); !isRetryableWritebackError(err) {
		t.Fatalf("git_prepared response loss err=%v", err)
	}
	gitPrepared, err := changeRepository.GetWritebackExecution(ctx, begin.ExecutionID)
	if err != nil || gitPrepared.Status != domain.WritebackStatusGitPrepared {
		t.Fatalf("git_prepared=%#v err=%v", gitPrepared, err)
	}

	// Publish 与 cleanup finalize 的响应同时丢失；下一实例只能重放发布/清理，不能重复 Commit。
	publishFault := &faultWritebackRepository{writebackRepository: changeRepository, publishFault: true, finalizeFault: true}
	if _, err := realService(publishFault).Resume(ctx, begin.ExecutionID, "fault-worker"); !isRetryableWritebackError(err) {
		t.Fatalf("publish/finalize response loss err=%v", err)
	}
	verifying, err := changeRepository.GetWritebackExecution(ctx, begin.ExecutionID)
	if err != nil || verifying.Status != domain.WritebackStatusVerifying || verifying.CleanupCompletedAt == nil {
		t.Fatalf("verifying=%#v err=%v", verifying, err)
	}
	result, err := realService(changeRepository).Resume(ctx, begin.ExecutionID, "fault-worker")
	if err != nil || result.Status != domain.WritebackStatusVerifying || result.CleanupPending || result.GitCommit == "" {
		t.Fatalf("replayed result=%#v err=%v", result, err)
	}
	if count := strings.TrimSpace(runFaultSmokeGit(t, ctx, root, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("commit count=%s want=2", count)
	}
	assertFaultSmokeCount(t, ctx, tx, `SELECT count(*) FROM change_control.proposal_commit WHERE writeback_execution_id=$1`, 1, string(begin.ExecutionID))
	assertFaultSmokeCount(t, ctx, tx, `SELECT count(*) FROM workflow.outbox_event WHERE idempotency_key=$1`, 1, domain.ExpectedWritebackReindexKey(domain.WritebackExecution{WorkspaceID: workspaceID, ProposalID: created.Proposal.ID, RevisionID: created.Proposal.Revision.ID, GitCommit: result.GitCommit}))
}

type faultWritebackRepository struct {
	writebackRepository
	checkpointStatus domain.WritebackStatus
	checkpointUsed   bool
	publishFault     bool
	finalizeFault    bool
}

func (r *faultWritebackRepository) CheckpointWritebackExecution(ctx context.Context, command domain.CheckpointWriteback) (domain.WritebackExecution, error) {
	updated, err := r.writebackRepository.CheckpointWritebackExecution(ctx, command)
	if err == nil && !r.checkpointUsed && r.checkpointStatus == command.Status {
		r.checkpointUsed = true
		return updated, smokeResponseLost("checkpoint")
	}
	return updated, err
}

func (r *faultWritebackRepository) PublishWriteback(ctx context.Context, command domain.PublishWriteback) (domain.PublishWritebackResult, error) {
	result, err := r.writebackRepository.PublishWriteback(ctx, command)
	if err == nil && r.publishFault {
		r.publishFault = false
		return result, smokeResponseLost("publish")
	}
	return result, err
}

func (r *faultWritebackRepository) FinalizeWritebackCleanup(ctx context.Context, executionID foundation.ID, expectedVersion int64, at time.Time) (domain.WritebackExecution, error) {
	result, err := r.writebackRepository.FinalizeWritebackCleanup(ctx, executionID, expectedVersion, at)
	if err == nil && r.finalizeFault {
		r.finalizeFault = false
		return result, smokeResponseLost("cleanup finalize")
	}
	return result, err
}

func smokeResponseLost(operation string) error {
	return foundation.NewError(foundation.ErrorRetryableFailure, "SMOKE_RESPONSE_LOST_"+strings.ToUpper(strings.ReplaceAll(operation, " ", "_")), true, errors.New("durable operation committed before response was lost"))
}

func isRetryableWritebackError(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Retryable
}

func runFaultSmokeGit(t *testing.T, ctx context.Context, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", arguments, err, output)
	}
	return string(output)
}

func assertFaultSmokeCount(t *testing.T, ctx context.Context, tx pgx.Tx, query string, want int, arguments ...any) {
	t.Helper()
	var count int
	if err := tx.QueryRow(ctx, query, arguments...).Scan(&count); err != nil || count != want {
		t.Fatalf("count=%d want=%d err=%v query=%s", count, want, err, query)
	}
}
