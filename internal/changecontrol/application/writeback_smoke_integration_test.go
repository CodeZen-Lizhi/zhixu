package application_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	changecontrolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSafeWritebackWorkflowNodePostgreSQLGitFilesystemSmoke(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	targetPath := "docs/safe-writeback.md"
	baseContent := []byte("# Safe Writeback\n\nbase\n")
	approvedContent := "# Safe Writeback\n\napproved\n"
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(targetPath)), baseContent, 0o644); err != nil {
		t.Fatal(err)
	}
	runSmokeGit(t, ctx, root, "init", "--initial-branch=main")
	runSmokeGit(t, ctx, root, "config", "user.name", "ZHIXU Smoke")
	runSmokeGit(t, ctx, root, "config", "user.email", "smoke@example.invalid")
	runSmokeGit(t, ctx, root, "add", "--", targetPath)
	runSmokeGit(t, ctx, root, "commit", "-m", "base")
	baseGitHead := strings.TrimSpace(runSmokeGit(t, ctx, root, "rev-parse", "HEAD"))

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
		ID: workspaceID, Name: "Safe Writeback Smoke", RootPath: root,
		Git:    workspacedomain.GitBaseline{RepositoryPath: root, Branch: "main", Head: baseGitHead, CheckedAt: databaseNow},
		Status: workspacedomain.WorkspaceStatusActive, Version: 1, CreatedAt: databaseNow, UpdatedAt: databaseNow,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,'{}',$4)`, string(definitionID), string(workspaceID), "safe-writeback-smoke-"+string(definitionID), databaseNow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(workspaceID), string(definitionID), databaseNow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,version,created_at,updated_at) VALUES($1,$2,'safe-writeback','side_effect','running',1,'{}','smoke-worker',$3,1,$4,$4)`, string(nodeID), string(runID), databaseNow.Add(10*time.Minute), databaseNow); err != nil {
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
	changeService, err := application.NewService(changeRepository, ids, foundation.FixedClock{Value: databaseNow}, targetReader, gitRepository)
	if err != nil {
		t.Fatal(err)
	}
	baseHash := domain.ComputeWritebackResultHash(baseContent)
	created, err := changeService.CreateProposal(ctx, application.CreateCommand{
		WorkspaceID: workspaceID, TargetPath: targetPath, IdempotencyKey: "proposal-smoke-" + string(nextID()),
		BaseHash: baseHash, Content: approvedContent, EvidenceSummary: "integration evidence", Risk: "low", RollbackPlan: "strict reverse commit",
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
	writeAuthorization := issue("ApplyApprovedPatch", domain.CapabilityWriteKnowledge, "write-auth-smoke-"+string(nextID()))
	gitAuthorization := issue("CreateGitCommit", domain.CapabilityGitWrite, "git-auth-smoke-"+string(nextID()))
	validator, err := changecontrollocalfs.NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	workspaceStore, err := changecontrollocalfs.NewWriter(workspaceRepository, validator)
	if err != nil {
		t.Fatal(err)
	}
	writebackService, err := application.NewWritebackService(application.WritebackServiceDependencies{
		Repository: &smokeFaultRepository{
			WritebackSagaRepository: changeRepository,
			failFileApplied:         true,
			failGitCommitted:        true,
			losePublishResponse:     true,
			loseCleanupResponse:     true,
		},
		Workspace: workspaceStore, Git: gitRepository, IDs: ids, Clock: foundation.FixedClock{Value: databaseNow.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	node, err := changecontrolworkflow.NewNode(writebackService)
	if err != nil {
		t.Fatal(err)
	}
	beginCommand := application.BeginWritebackCommand{
		WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: created.Proposal.ID,
		LeaseOwner: "smoke-worker", IdempotencyKey: "writeback-smoke-" + string(nextID()),
		WriteCredential: writeAuthorization.Credential, GitCredential: gitAuthorization.Credential,
		WriteAuthorizationKey: writeAuthorization.Authorization.IdempotencyKey, GitAuthorizationKey: gitAuthorization.Authorization.IdempotencyKey,
	}
	begin, err := writebackService.Begin(ctx, beginCommand)
	if err != nil || begin.Status != domain.WritebackStatusPrepared || begin.Replayed {
		t.Fatalf("begin=%#v err=%v", begin, err)
	}
	replayedBegin, err := writebackService.Begin(ctx, beginCommand)
	if err != nil || replayedBegin.ExecutionID != begin.ExecutionID || !replayedBegin.Replayed {
		t.Fatalf("replayed begin=%#v err=%v", replayedBegin, err)
	}
	nodeInput := changecontrolworkflow.Input{
		SchemaVersion: application.SafeWritebackSchemaVersion,
		ExecutionID:   begin.ExecutionID,
		WorkspaceID:   workspaceID,
		WorkflowRunID: runID,
		NodeRunID:     nodeID,
	}
	if _, err := node.Execute(ctx, nodeInput, "smoke-worker"); !hasSmokeErrorCode(err, "SMOKE_FILE_APPLIED_CHECKPOINT_LOST") {
		t.Fatalf("file checkpoint crash err=%v", err)
	}
	assertSmokeExecutionState(t, ctx, tx, begin.ExecutionID, domain.WritebackStatusFilePrepared, false)
	if _, err := node.Execute(ctx, nodeInput, "smoke-worker"); !hasSmokeErrorCode(err, "SMOKE_GIT_COMMITTED_CHECKPOINT_LOST") {
		t.Fatalf("git checkpoint crash err=%v", err)
	}
	assertSmokeExecutionState(t, ctx, tx, begin.ExecutionID, domain.WritebackStatusGitPrepared, false)
	if _, err := node.Execute(ctx, nodeInput, "smoke-worker"); !hasSmokeErrorCode(err, "SMOKE_CLEANUP_RESPONSE_LOST") {
		t.Fatalf("cleanup response loss err=%v", err)
	}
	assertSmokeExecutionState(t, ctx, tx, begin.ExecutionID, domain.WritebackStatusVerifying, true)
	result, err := node.Execute(ctx, nodeInput, "smoke-worker")
	if err != nil || result.Status != domain.WritebackStatusVerifying || result.IndexStatus != application.WritebackIndexStatusPending || result.GitCommit == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	replayedResult, err := node.Execute(ctx, nodeInput, "smoke-worker")
	if err != nil || replayedResult.GitCommit != result.GitCommit || replayedResult != result {
		t.Fatalf("replayed result=%#v err=%v", replayedResult, err)
	}

	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(targetPath)))
	if err != nil || string(content) != approvedContent {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if head := strings.TrimSpace(runSmokeGit(t, ctx, root, "rev-parse", "HEAD")); head != result.GitCommit {
		t.Fatalf("HEAD=%s want=%s", head, result.GitCommit)
	}
	if count, _ := strconv.Atoi(strings.TrimSpace(runSmokeGit(t, ctx, root, "rev-list", "--count", "HEAD"))); count != 2 {
		t.Fatalf("commit count=%d", count)
	}
	message := runSmokeGit(t, ctx, root, "log", "-1", "--format=%B")
	if !strings.Contains(message, domain.GitTrailerWritebackID+": "+string(begin.ExecutionID)) || !strings.Contains(message, domain.GitTrailerProposalID+": "+string(created.Proposal.ID)) {
		t.Fatalf("commit trailers=%q", message)
	}
	assertSmokeCount(t, ctx, tx, `SELECT count(*) FROM change_control.proposal_commit WHERE writeback_execution_id=$1`, 1, string(begin.ExecutionID))
	assertSmokeCount(t, ctx, tx, `SELECT count(*) FROM workflow.outbox_event WHERE idempotency_key=$1`, 1, domain.ExpectedWritebackReindexKey(domain.WritebackExecution{WorkspaceID: workspaceID, ProposalID: created.Proposal.ID, RevisionID: created.Proposal.Revision.ID, GitCommit: result.GitCommit}))
	assertSmokeCount(t, ctx, tx, `SELECT count(*) FROM change_control.tool_authorization WHERE id IN ($1,$2) AND status='consumed'`, 2, string(writeAuthorization.Authorization.ID), string(gitAuthorization.Authorization.ID))
	assertSmokeCount(t, ctx, tx, `SELECT count(*) FROM change_control.tool_authorization WHERE token_hash IN ($1,$2)`, 0, writeAuthorization.Credential, gitAuthorization.Credential)
	assertSmokeCount(t, ctx, tx, `SELECT count(*) FROM change_control.writeback_execution e WHERE to_jsonb(e)::text LIKE '%' || $1 || '%' OR to_jsonb(e)::text LIKE '%' || $2 || '%'`, 0, writeAuthorization.Credential, gitAuthorization.Credential)
	var status string
	var cleanupCompletedAt *time.Time
	var resultLockToken, backupLockToken string
	if err := tx.QueryRow(ctx, `SELECT status,cleanup_completed_at,file_result_lock_token,file_backup_lock_token FROM change_control.writeback_execution WHERE id=$1`, string(begin.ExecutionID)).Scan(&status, &cleanupCompletedAt, &resultLockToken, &backupLockToken); err != nil || status != string(domain.WritebackStatusVerifying) || cleanupCompletedAt == nil || !domain.ValidHash(resultLockToken) || !domain.ValidHash(backupLockToken) || resultLockToken == backupLockToken {
		t.Fatalf("execution status=%s cleanup=%v result_token=%q backup_token=%q err=%v", status, cleanupCompletedAt, resultLockToken, backupLockToken, err)
	}
}

type smokeFaultRepository struct {
	domain.WritebackSagaRepository
	failFileApplied     bool
	failGitCommitted    bool
	losePublishResponse bool
	loseCleanupResponse bool
}

func (r *smokeFaultRepository) CheckpointWritebackExecution(ctx context.Context, command domain.CheckpointWriteback) (domain.WritebackExecution, error) {
	if command.Status == domain.WritebackStatusFileApplied && r.failFileApplied {
		r.failFileApplied = false
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorRetryableFailure, "SMOKE_FILE_APPLIED_CHECKPOINT_LOST", true, errors.New("simulated crash after file rename"))
	}
	if command.Status == domain.WritebackStatusGitCommitted && r.failGitCommitted {
		r.failGitCommitted = false
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorRetryableFailure, "SMOKE_GIT_COMMITTED_CHECKPOINT_LOST", true, errors.New("simulated crash after git commit"))
	}
	return r.WritebackSagaRepository.CheckpointWritebackExecution(ctx, command)
}

func (r *smokeFaultRepository) PublishWriteback(ctx context.Context, command domain.PublishWriteback) (domain.PublishWritebackResult, error) {
	result, err := r.WritebackSagaRepository.PublishWriteback(ctx, command)
	if err != nil {
		return result, err
	}
	if r.losePublishResponse {
		r.losePublishResponse = false
		return domain.PublishWritebackResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "SMOKE_PUBLISH_RESPONSE_LOST", true, errors.New("simulated publish response loss"))
	}
	return result, nil
}

func (r *smokeFaultRepository) FinalizeWritebackCleanup(ctx context.Context, executionID foundation.ID, version int64, at time.Time) (domain.WritebackExecution, error) {
	execution, err := r.WritebackSagaRepository.FinalizeWritebackCleanup(ctx, executionID, version, at)
	if err != nil {
		return execution, err
	}
	if r.loseCleanupResponse {
		r.loseCleanupResponse = false
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorRetryableFailure, "SMOKE_CLEANUP_RESPONSE_LOST", true, errors.New("simulated cleanup finalize response loss"))
	}
	return execution, nil
}

func hasSmokeErrorCode(err error, code string) bool {
	var applicationError *foundation.Error
	return errors.As(err, &applicationError) && applicationError.Code == code
}

func assertSmokeExecutionState(t *testing.T, ctx context.Context, tx pgx.Tx, executionID foundation.ID, want domain.WritebackStatus, cleanupComplete bool) {
	t.Helper()
	var status string
	var cleanupCompletedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT status,cleanup_completed_at FROM change_control.writeback_execution WHERE id=$1`, string(executionID)).Scan(&status, &cleanupCompletedAt); err != nil {
		t.Fatal(err)
	}
	if status != string(want) || (cleanupCompletedAt != nil) != cleanupComplete {
		t.Fatalf("execution status=%s cleanup=%v want status=%s cleanup=%v", status, cleanupCompletedAt, want, cleanupComplete)
	}
}

func runSmokeGit(t *testing.T, ctx context.Context, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", arguments, err, output)
	}
	return string(output)
}

func assertSmokeCount(t *testing.T, ctx context.Context, tx pgx.Tx, query string, want int, arguments ...any) {
	t.Helper()
	var count int
	if err := tx.QueryRow(ctx, query, arguments...).Scan(&count); err != nil || count != want {
		t.Fatalf("count=%d want=%d err=%v query=%s", count, want, err, query)
	}
}
