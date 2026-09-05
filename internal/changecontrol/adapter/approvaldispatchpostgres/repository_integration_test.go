//go:build integration

package approvaldispatchpostgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changedispatch "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/dispatch"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	eventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingspostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApprovalDispatchAtomicallyCreatesAndReplaysWorkflowRiverBinding(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newApprovalDispatchTestDatabase(t, ctx)
	defer cleanup()
	repository, dispatcher := approvalDispatchFixture(t, ctx, pool)
	workspaceID, proposal := createDispatchProposal(t, ctx, pool, repository, "atomic")
	head := "abcdef0123456789abcdef0123456789abcdef01"
	command := changedispatch.Command{WorkspaceID: workspaceID, Approval: domain.Approval{
		ID: mustDispatchID(t), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionApproved,
		ApprovedGitHead: &head, DecidedAt: time.Now().UTC(),
	}, ObservedBaseHash: proposal.Revision.BaseHash, ObservedGitHead: head}

	type call struct {
		result changedispatch.Result
		err    error
	}
	calls := make(chan call, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := dispatcher.DecideAndDispatch(ctx, command)
			calls <- call{result: result, err: err}
		}()
	}
	wg.Wait()
	close(calls)
	var first changedispatch.Result
	var replayed int
	for call := range calls {
		if call.err != nil {
			t.Fatal(call.err)
		}
		if first.WorkflowRunID == "" {
			first = call.result
		} else if call.result.WorkflowRunID != first.WorkflowRunID || call.result.NodeRunID != first.NodeRunID || call.result.JobID != first.JobID {
			t.Fatalf("first=%#v other=%#v", first, call.result)
		}
		if call.result.Replayed {
			replayed++
		}
	}
	if replayed != 1 || first.WorkflowRunID == "" || first.NodeRunID == "" || first.JobID < 1 {
		t.Fatalf("first=%#v replayed=%d", first, replayed)
	}

	persisted, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil || persisted.WorkflowRunID == nil || *persisted.WorkflowRunID != first.WorkflowRunID || persisted.Status != domain.StatusApproved {
		t.Fatalf("proposal=%#v err=%v", persisted, err)
	}
	var approvals, runs, nodes, outbox, jobs int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM change_control.approval WHERE proposal_id=$1),
		(SELECT count(*) FROM workflow.run WHERE id=$2),
		(SELECT count(*) FROM workflow.node_run WHERE id=$3),
		(SELECT count(*) FROM workflow.outbox_event WHERE run_id=$2),
		(SELECT count(*) FROM workflow.river_job WHERE kind=$4 AND args->>'node_run_id'=$5)`, string(proposal.ID), string(first.WorkflowRunID), string(first.NodeRunID), riveradapter.NodeJobKind, string(first.NodeRunID)).Scan(&approvals, &runs, &nodes, &outbox, &jobs); err != nil {
		t.Fatal(err)
	}
	if approvals != 1 || runs != 1 || nodes != 1 || outbox != 1 || jobs != 1 {
		t.Fatalf("approval=%d run=%d node=%d outbox=%d jobs=%d", approvals, runs, nodes, outbox, jobs)
	}
	otherWorkspaceID := mustDispatchID(t)
	rootPath := "/tmp/approval-dispatch-other-" + string(otherWorkspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Approval Dispatch Other',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(otherWorkspaceID), rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.run SET workspace_id=$2 WHERE id=$1`, string(first.WorkflowRunID), string(otherWorkspaceID)); err == nil {
		t.Fatal("bound workflow run workspace was mutable")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatalf("workspace mutation err=%v", err)
		}
	}
	var input string
	if err := pool.QueryRow(ctx, `SELECT input::text FROM workflow.node_run WHERE id=$1`, string(first.NodeRunID)).Scan(&input); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"credential", "target_path", "content", "git"} {
		if strings.Contains(strings.ToLower(input), forbidden) {
			t.Fatalf("node input leaked %q: %s", forbidden, input)
		}
	}
}

// TestGORMApprovalDispatchAtomicallyBindsWorkflowAndRiver is the focused
// PostgreSQL gate for the staged Approval Dispatch adapter. It keeps one
// shared platform Pool across Change Control, Model Settings, Workflow,
// Events, and River so the caller-owned scope is exercised end to end.
func TestGORMApprovalDispatchAtomicallyBindsWorkflowAndRiver(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("PostgreSQL fixture did not provide a shared platform pool")
	}

	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	auditStore, err := auditpostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := modelsettingspostgres.NewGORMRepository(platform,
		modelsettingspostgres.WithGORMSecretSealer(sealer),
		modelsettingspostgres.WithGORMAuditAppender(auditStore),
	)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := changecontrolpostgres.NewGORMRepository(platform, events)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(
		platform,
		riveradapter.DefaultOptions(),
		settings,
		workflowpostgres.GORMRuntimeRepositoryHooks{CancellationSafety: repository},
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewGORMApprovalDispatchRepository(
		platform, runtime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, events,
	)
	if err != nil {
		t.Fatal(err)
	}

	workspaceID, proposal := createDispatchProposal(t, ctx, platform.DB(), repository, "gorm-atomic")
	head := strings.Repeat("c", 40)
	command := changedispatch.Command{
		WorkspaceID: workspaceID,
		Approval: domain.Approval{
			ID: mustDispatchID(t), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
			ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionApproved,
			ApprovedGitHead: &head, DecidedAt: time.Now().UTC(),
		},
		ObservedBaseHash: proposal.Revision.BaseHash,
		ObservedGitHead:  head,
	}
	first, err := dispatcher.DecideAndDispatch(ctx, command)
	if err != nil || first.Replayed || first.DispatchStatus != changedispatch.StatusQueued || first.WorkflowRunID == "" || first.NodeRunID == "" || first.JobID < 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	replayed, err := dispatcher.DecideAndDispatch(ctx, changedispatch.Command{
		WorkspaceID: workspaceID,
		Approval:    command.Approval,
	})
	if err != nil || !replayed.Replayed || replayed.DispatchStatus != changedispatch.StatusReplayed || replayed.WorkflowRunID != first.WorkflowRunID || replayed.NodeRunID != first.NodeRunID || replayed.JobID != first.JobID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}

	persisted, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil || persisted.WorkflowRunID == nil || *persisted.WorkflowRunID != first.WorkflowRunID || persisted.Status != domain.StatusApproved {
		t.Fatalf("proposal=%#v err=%v", persisted, err)
	}
	var approvals, runs, nodes, outbox, jobs int
	if err := platform.DB().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM change_control.approval WHERE proposal_id=$1),
		(SELECT count(*) FROM workflow.run WHERE id=$2),
		(SELECT count(*) FROM workflow.node_run WHERE id=$3),
		(SELECT count(*) FROM workflow.outbox_event WHERE run_id=$2),
		(SELECT count(*) FROM workflow.river_job WHERE kind=$4 AND args->>'node_run_id'=$5)`,
		string(proposal.ID), string(first.WorkflowRunID), string(first.NodeRunID), riveradapter.NodeJobKind, string(first.NodeRunID)).Scan(&approvals, &runs, &nodes, &outbox, &jobs); err != nil {
		t.Fatal(err)
	}
	if approvals != 1 || runs != 1 || nodes != 1 || outbox != 1 || jobs != 1 {
		t.Fatalf("approval=%d run=%d node=%d outbox=%d jobs=%d", approvals, runs, nodes, outbox, jobs)
	}
}

func TestApprovalDispatchRollsBackDecisionWhenRuntimeFails(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newApprovalDispatchTestDatabase(t, ctx)
	defer cleanup()
	repository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, proposal := createDispatchProposal(t, ctx, pool, repository, "rollback")
	dispatcher, err := NewApprovalDispatchRepository(pool, failingApprovalRuntime{}, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	head := "abcdef0123456789abcdef0123456789abcdef01"
	_, err = dispatcher.DecideAndDispatch(ctx, changedispatch.Command{WorkspaceID: workspaceID, Approval: domain.Approval{
		ID: mustDispatchID(t), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionApproved,
		ApprovedGitHead: &head, DecidedAt: time.Now().UTC(),
	}, ObservedBaseHash: proposal.Revision.BaseHash, ObservedGitHead: head})
	if err == nil {
		t.Fatal("runtime failure was swallowed")
	}
	persisted, getErr := repository.GetProposal(ctx, proposal.ID)
	if getErr != nil || persisted.Status != domain.StatusReady || persisted.Approval != nil || persisted.WorkflowRunID != nil {
		t.Fatalf("proposal=%#v err=%v dispatch err=%v", persisted, getErr, err)
	}
}

func TestApprovalDispatchRejectedNeverCreatesWorkflow(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newApprovalDispatchTestDatabase(t, ctx)
	defer cleanup()
	repository, dispatcher := approvalDispatchEventFixture(t, ctx, pool)
	workspaceID, proposal := createDispatchProposal(t, ctx, pool, repository, "rejected")
	command := changedispatch.Command{WorkspaceID: workspaceID, Approval: domain.Approval{
		ID: mustDispatchID(t), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionRejected, DecidedAt: time.Now().UTC(),
	}}
	first, err := dispatcher.DecideAndDispatch(ctx, command)
	if err != nil || first.Replayed || first.WorkflowRunID != "" || first.JobID != 0 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := dispatcher.DecideAndDispatch(ctx, command)
	if err != nil || !second.Replayed || second.WorkflowRunID != "" || second.JobID != 0 || second.Approval.ID != first.Approval.ID {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	var runs, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1`, string(workspaceID)).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || jobs != 0 {
		t.Fatalf("runs=%d jobs=%d", runs, jobs)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event
		WHERE workspace_id=$1 AND event_type=$2 AND resource_ref=$3`,
		string(workspaceID), eventcontract.ProposalRejectedEventType, "proposal:"+string(proposal.ID)).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("proposal rejected event count=%d, want 1", eventCount)
	}
	var eventVersion int64
	var sourceRef, status string
	if err := pool.QueryRow(ctx, `SELECT resource_version,source_event_ref,payload_summary->>'status'
		FROM ops.server_event WHERE workspace_id=$1 AND event_type=$2`,
		string(workspaceID), eventcontract.ProposalRejectedEventType).Scan(&eventVersion, &sourceRef, &status); err != nil {
		t.Fatal(err)
	}
	if eventVersion != 2 || sourceRef != eventcontract.ProposalRejectedEventType+":"+string(first.Approval.ID)+":v1" || status != string(domain.StatusRejected) {
		t.Fatalf("proposal rejected event version=%d source=%s status=%s", eventVersion, sourceRef, status)
	}
}

func TestApprovalDispatchRecoversCommitResponseLossByExactReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newApprovalDispatchTestDatabase(t, ctx)
	defer cleanup()
	repository, normal := approvalDispatchFixture(t, ctx, pool)
	workspaceID, proposal := createDispatchProposal(t, ctx, pool, repository, "response-loss")
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	lost, err := NewApprovalDispatchRepository(dispatchCommitResponseLossDB{pool: pool}, runtime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	head := "abcdef0123456789abcdef0123456789abcdef01"
	command := changedispatch.Command{WorkspaceID: workspaceID, Approval: domain.Approval{
		ID: mustDispatchID(t), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionApproved,
		ApprovedGitHead: &head, DecidedAt: time.Now().UTC(),
	}, ObservedBaseHash: proposal.Revision.BaseHash, ObservedGitHead: head}
	if _, err := lost.DecideAndDispatch(ctx, command); err == nil {
		t.Fatal("commit response loss was not surfaced")
	}
	replayed, err := normal.DecideAndDispatch(ctx, changedispatch.Command{WorkspaceID: workspaceID, Approval: command.Approval})
	if err != nil || !replayed.Replayed || replayed.DispatchStatus != changedispatch.StatusReplayed || replayed.WorkflowRunID == "" || replayed.JobID < 1 {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
}

type failingApprovalRuntime struct{}

func (failingApprovalRuntime) StartTx(context.Context, pgx.Tx, workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	return workflowapplication.RuntimeStartResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, errors.New("injected runtime failure"))
}

type dispatchCommitResponseLossDB struct{ pool *pgxpool.Pool }

func (d dispatchCommitResponseLossDB) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	return d.pool.Query(ctx, sql, arguments...)
}

func (d dispatchCommitResponseLossDB) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	return d.pool.QueryRow(ctx, sql, arguments...)
}

func (d dispatchCommitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return dispatchCommitResponseLossTx{Tx: tx}, nil
}

type dispatchCommitResponseLossTx struct{ pgx.Tx }

func (tx dispatchCommitResponseLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("injected approval dispatch commit response loss")
}

func approvalDispatchFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (*changecontrolpostgres.Repository, *ApprovalDispatchRepository) {
	t.Helper()
	repository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewApprovalDispatchRepository(pool, runtime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	return repository, dispatcher
}

func approvalDispatchEventFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (*changecontrolpostgres.Repository, *ApprovalDispatchRepository) {
	t.Helper()
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := changecontrolpostgres.NewRepository(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewApprovalDispatchRepository(pool, runtime, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, events)
	if err != nil {
		t.Fatal(err)
	}
	return repository, dispatcher
}

type dispatchProposalCreator interface {
	CreateProposal(context.Context, domain.Proposal) (domain.Proposal, error)
}

func createDispatchProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repository dispatchProposalCreator, suffix string) (foundation.ID, domain.Proposal) {
	t.Helper()
	workspaceID := mustDispatchID(t)
	now := time.Now().UTC().Add(-time.Second)
	root := "/tmp/approval-dispatch-" + suffix + "-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'active',1,$4,$4)`, string(workspaceID), "Dispatch "+suffix, root, now); err != nil {
		t.Fatal(err)
	}
	proposalID, revisionID := mustDispatchID(t), mustDispatchID(t)
	baseHash := strings.Repeat("a", 64)
	content := "# approved dispatch\n"
	requestHash, err := domain.ComputeRequestHashWithRiskLevel(workspaceID, "notes/a.md", baseHash, content, "evidence", domain.ProposalRiskLevelLow, "low", "revert")
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, Type: domain.ProposalTypeFilePatch, RiskLevel: domain.ProposalRiskLevelLow, TargetPath: "notes/a.md", IdempotencyKey: "proposal-" + suffix,
		RequestHash: requestHash,
		Status:      domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: "notes/a.md", BaseHash: baseHash, Content: content, EvidenceSummary: "evidence", Risk: "low", RollbackPlan: "revert", ChangeHash: domain.ComputeChangeHash("notes/a.md", baseHash, content), CreatedAt: now},
	}
	persisted, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceID, persisted
}

func mustDispatchID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newApprovalDispatchTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for Approval Dispatch integration tests")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_approval_dispatch_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewAtlasEmbeddedRunner(pool)
	if err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}
