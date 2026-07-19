//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryPolicyRefusedStartCASUnknownTimelineAndSecretBoundary(t *testing.T) {
	pool, ctx := newToolRepositoryIntegrationPool(t)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	primary := seedToolRuntime(t, ctx, pool, 1, toolTestID(1))
	secondary := seedToolRuntime(t, ctx, pool, 2, primary.workspaceID)
	otherWorkspaceID := toolTestID(31)

	t.Run("policy binding and lease", func(t *testing.T) {
		policy, err := repository.ResolveToolPolicy(ctx, primary.identity)
		if err != nil || policy.WorkflowKey != primary.workflowKey || policy.NodeKind != "agent.tool" ||
			len(policy.Permissions) != 2 || len(policy.AllowedTools) != 2 || !policy.AttemptLeaseTo.After(time.Now().UTC()) {
			t.Fatalf("ResolveToolPolicy=%#v err=%v", policy, err)
		}
		policy.Permissions[0] = capability.GitWrite
		policy.AllowedTools[0] = domain.ToolRef{Name: "ReadSource", Version: 1}
		again, err := repository.ResolveToolPolicy(ctx, primary.identity)
		if err != nil || again.Permissions[0] == capability.GitWrite || again.AllowedTools[0].Name == "ReadSource" {
			t.Fatalf("policy was mutable: %#v err=%v", again, err)
		}
		invalid := []domain.TrustedExecutionIdentity{
			withIdentity(primary.identity, func(value *domain.TrustedExecutionIdentity) { value.WorkspaceID = otherWorkspaceID }),
			withIdentity(primary.identity, func(value *domain.TrustedExecutionIdentity) { value.WorkflowRunID = secondary.runID }),
			withIdentity(primary.identity, func(value *domain.TrustedExecutionIdentity) { value.NodeRunID = secondary.nodeID }),
			withIdentity(primary.identity, func(value *domain.TrustedExecutionIdentity) { value.NodeAttemptID = secondary.attemptID }),
			withIdentity(primary.identity, func(value *domain.TrustedExecutionIdentity) { value.LeaseOwner = "other-worker" }),
			withIdentity(primary.identity, func(value *domain.TrustedExecutionIdentity) { value.LeaseFence++ }),
			withIdentity(primary.identity, func(value *domain.TrustedExecutionIdentity) { value.DefinitionHash = hash64('f') }),
		}
		for index, identity := range invalid {
			if _, err := repository.ResolveToolPolicy(ctx, identity); errorCode(err) != ErrorCodeContextStale {
				t.Fatalf("invalid policy %d code=%s err=%v", index, errorCode(err), err)
			}
		}
		if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(primary.nodeID)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(primary.attemptID)); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ResolveToolPolicy(ctx, primary.identity); errorCode(err) != ErrorCodeContextStale {
			t.Fatalf("expired lease code=%s err=%v", errorCode(err), err)
		}
		expiredCall := startedCall(primary, 99, toolTestID(199), searchToolRef, "", hash64('6'))
		if _, err := repository.StartCall(ctx, startCommand(primary, expiredCall)); errorCode(err) != ErrorCodeContextStale {
			t.Fatalf("expired lease StartCall code=%s err=%v", errorCode(err), err)
		}
		renewToolLease(t, ctx, pool, primary, 5*time.Minute)
	})

	t.Run("refused does not occupy executor idempotency", func(t *testing.T) {
		refused := refusedCall(primary, 1, toolTestID(101), "UnknownTool", hash64('a'))
		result, err := repository.RecordRefused(ctx, application.RecordRefusedCommand{Identity: primary.identity, Call: refused})
		if err != nil || result.Replayed || result.Call.Status != domain.CallRefused || result.Call.IdempotencyKey != "" {
			t.Fatalf("RecordRefused=%#v err=%v", result, err)
		}
		replay, err := repository.RecordRefused(ctx, application.RecordRefusedCommand{Identity: primary.identity, Call: refused})
		if err != nil || !replay.Replayed || replay.Call.ID != result.Call.ID {
			t.Fatalf("RecordRefused replay=%#v err=%v", replay, err)
		}
		var idempotencyKey *string
		if err := pool.QueryRow(ctx, `SELECT idempotency_key FROM workflow.tool_call WHERE id=$1`, string(result.Call.ID)).Scan(&idempotencyKey); err != nil || idempotencyKey != nil {
			t.Fatalf("refused idempotency=%v err=%v", idempotencyKey, err)
		}
	})

	var readStarted domain.ToolCall
	t.Run("start replay conflict and terminal CAS", func(t *testing.T) {
		rawArgumentsCanary := `{"authorization":"Bearer top-secret","path":"/Users/private/source.md","body":"raw-source-canary"}`
		_ = rawArgumentsCanary
		call := startedCall(primary, 2, toolTestID(102), searchToolRef, "", hash64('b'))
		started, err := repository.StartCall(ctx, startCommand(primary, call))
		if err != nil || started.Disposition != application.StartCallCreated || started.Call.Status != domain.CallStarted {
			t.Fatalf("StartCall=%#v err=%v", started, err)
		}
		readStarted = started.Call
		replayInput := call
		replayInput.ID = toolTestID(103)
		replay, err := repository.StartCall(ctx, startCommand(primary, replayInput))
		if err != nil || replay.Disposition != application.StartCallReplayed || replay.Call.ID != started.Call.ID {
			t.Fatalf("StartCall replay=%#v err=%v", replay, err)
		}
		conflict := replayInput
		conflict.RequestHash = hash64('c')
		if _, err := repository.StartCall(ctx, startCommand(primary, conflict)); errorCode(err) != ErrorCodeIdempotencyConflict {
			t.Fatalf("StartCall conflict code=%s err=%v", errorCode(err), err)
		}

		completed := successfulReadCall(started.Call, hash64('d'))
		terminal, err := repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: completed})
		if err != nil || terminal.Replayed || terminal.Call.Status != domain.CallSucceeded || terminal.Call.Version != 2 {
			t.Fatalf("FinalizeCall=%#v err=%v", terminal, err)
		}
		replayed, err := repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: completed})
		if err != nil || !replayed.Replayed || replayed.Call.ID != terminal.Call.ID {
			t.Fatalf("FinalizeCall replay=%#v err=%v", replayed, err)
		}
		different := completed
		different.ResponseHash = hash64('e')
		if _, err := repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: different}); errorCode(err) != ErrorCodeCallVersionConflict {
			t.Fatalf("FinalizeCall conflict code=%s err=%v", errorCode(err), err)
		}
	})

	t.Run("terminal CAS rejects every changed started binding", func(t *testing.T) {
		mutations := []struct {
			name   string
			mutate func(*domain.ToolCall)
		}{
			{name: "request hash", mutate: func(call *domain.ToolCall) { call.RequestHash = hash64('8') }},
			{name: "tool", mutate: func(call *domain.ToolCall) {
				call.RequestedToolName = "ReadSource"
				call.Tool = &domain.ToolRef{Name: "ReadSource", Version: 2}
			}},
			{name: "schema", mutate: func(call *domain.ToolCall) {
				call.InputSchema = &domain.SchemaRef{ID: "tool.other-input", Version: 2}
			}},
			{name: "capability", mutate: func(call *domain.ToolCall) { call.Capability = capability.ReadExternal }},
			{name: "idempotency", mutate: func(call *domain.ToolCall) { call.IdempotencyKey = "changed-binding" }},
			{name: "summary", mutate: func(call *domain.ToolCall) {
				call.RequestSummary = json.RawMessage(`{"field_count":2}`)
			}},
		}
		ordinal := 0
		for _, terminal := range []struct {
			name    string
			unknown bool
		}{{name: "finalize"}, {name: "unknown", unknown: true}} {
			for _, mutation := range mutations {
				ordinal++
				t.Run(terminal.name+"/"+mutation.name, func(t *testing.T) {
					assertTerminalBindingCAS(t, ctx, pool, repository, primary, 20+ordinal, toolTestID(120+ordinal), terminal.unknown, mutation.mutate)
				})
			}
		}
	})

	t.Run("unsafe stable references never reach persistence", func(t *testing.T) {
		call := startedCall(primary, 50, toolTestID(150), searchToolRef, "read-reference", hash64('0'))
		started, err := repository.StartCall(ctx, startCommand(primary, call))
		if err != nil || started.Disposition != application.StartCallCreated {
			t.Fatalf("StartCall=%+v err=%v", started, err)
		}
		unsafe := successfulReadCall(started.Call, hash64('1'))
		unsafe.ResultRef = "/Users/private/source.md"
		if _, err := repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: unsafe}); errorCode(err) != domain.ErrorCodeCallInvalid {
			t.Fatalf("unsafe result ref code=%s err=%v", errorCode(err), err)
		}
		unsafe = successfulReadCall(started.Call, hash64('1'))
		unsafe.SideEffectType = "writeback_execution"
		unsafe.SideEffectID = "Authorization: Bearer secret"
		if _, err := repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: unsafe}); errorCode(err) != domain.ErrorCodeCallInvalid {
			t.Fatalf("unsafe side effect ref code=%s err=%v", errorCode(err), err)
		}
		if _, err := repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: successfulReadCall(started.Call, hash64('1'))}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("policy denial and model write fail closed", func(t *testing.T) {
		call := startedCall(primary, 3, toolTestID(104), searchToolRef, "", hash64('f'))
		wrongWorkflow := startCommand(primary, call)
		wrongWorkflow.AllowedWorkflows = []domain.WorkflowBinding{{Key: "other-workflow", Version: 1}}
		if _, err := repository.StartCall(ctx, wrongWorkflow); errorCode(err) != ErrorCodeWorkflowBindingDenied {
			t.Fatalf("workflow denial code=%s err=%v", errorCode(err), err)
		}
		wrongCapability := call
		wrongCapability.Capability = capability.GitWrite
		if _, err := repository.StartCall(ctx, startCommand(primary, wrongCapability)); errorCode(err) != ErrorCodePermissionDenied {
			t.Fatalf("permission denial code=%s err=%v", errorCode(err), err)
		}
		notAllowed := call
		otherRef := domain.ToolRef{Name: "ReadSource", Version: 1}
		notAllowed.Tool = &otherRef
		notAllowed.RequestedToolName = otherRef.Name
		if _, err := repository.StartCall(ctx, startCommand(primary, notAllowed)); errorCode(err) != ErrorCodeToolNotAllowed {
			t.Fatalf("allowlist denial code=%s err=%v", errorCode(err), err)
		}
		modelWrite := startedCall(primary, 3, toolTestID(105), rebuildToolRef, "write-model", hash64('1'))
		modelWrite.InvocationPolicy = domain.InvocationModelRequestable
		if _, err := repository.StartCall(ctx, startCommand(primary, modelWrite)); errorCode(err) != domain.ErrorCodeCallInvalid {
			t.Fatalf("model write code=%s err=%v", errorCode(err), err)
		}
	})

	t.Run("concurrent side effect gets one executor and cross binding conflicts", func(t *testing.T) {
		left := startedCall(primary, 3, toolTestID(106), rebuildToolRef, "rebuild-once", hash64('2'))
		right := left
		right.ID, right.CallNo = toolTestID(107), 4
		commands := []application.StartCallCommand{startCommand(primary, left), startCommand(primary, right)}
		results := make([]application.StartCallResult, len(commands))
		errorsFound := make([]error, len(commands))
		var wait sync.WaitGroup
		for index := range commands {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				results[index], errorsFound[index] = repository.StartCall(ctx, commands[index])
			}(index)
		}
		wait.Wait()
		created, replayed := 0, 0
		var durable domain.ToolCall
		for index := range results {
			if errorsFound[index] != nil {
				t.Fatalf("concurrent start %d err=%v", index, errorsFound[index])
			}
			switch results[index].Disposition {
			case application.StartCallCreated:
				created++
				durable = results[index].Call
			case application.StartCallReplayed:
				replayed++
			}
		}
		if created != 1 || replayed != 1 || results[0].Call.ID != results[1].Call.ID {
			t.Fatalf("concurrent results=%#v created=%d replayed=%d", results, created, replayed)
		}
		crossBinding := startedCall(secondary, 1, toolTestID(108), rebuildToolRef, "rebuild-once", hash64('2'))
		if _, err := repository.StartCall(ctx, startCommand(secondary, crossBinding)); errorCode(err) != ErrorCodeIdempotencyConflict {
			t.Fatalf("cross binding code=%s err=%v", errorCode(err), err)
		}
		unknown := durable
		unknown.Status, unknown.ErrorCode, unknown.Version = domain.CallUnknown, "TOOL_OUTCOME_UNKNOWN", 2
		unknown.CompletedAt = timePointer(time.Now().UTC())
		marked, err := repository.MarkUnknown(ctx, application.MarkUnknownCommand{ExpectedVersion: 1, Call: unknown})
		if err != nil || marked.Replayed || marked.Call.Status != domain.CallUnknown {
			t.Fatalf("MarkUnknown=%#v err=%v", marked, err)
		}
		replayedUnknown, err := repository.StartCall(ctx, startCommand(primary, right))
		if err != nil || replayedUnknown.Disposition != application.StartCallReplayed || replayedUnknown.Call.Status != domain.CallUnknown {
			t.Fatalf("UNKNOWN replay=%#v err=%v", replayedUnknown, err)
		}
	})

	t.Run("terminal response loss recovers canonical result", func(t *testing.T) {
		call := startedCall(secondary, 1, toolTestID(109), searchToolRef, "", hash64('3'))
		started, err := repository.StartCall(ctx, startCommand(secondary, call))
		if err != nil || started.Disposition != application.StartCallCreated {
			t.Fatalf("response loss start=%#v err=%v", started, err)
		}
		faultDB := &commitResponseLossDB{pool: pool}
		faultRepository, err := NewRepository(faultDB)
		if err != nil {
			t.Fatal(err)
		}
		completed := successfulReadCall(started.Call, hash64('4'))
		result, err := faultRepository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: completed})
		if err != nil || !result.Replayed || result.Call.Status != domain.CallSucceeded {
			t.Fatalf("response loss result=%#v err=%v", result, err)
		}
	})

	t.Run("started response loss recovers canonical call", func(t *testing.T) {
		fixture := seedToolRuntime(t, ctx, pool, 64, primary.workspaceID)
		faultRepository, err := NewRepository(&commitResponseLossDB{pool: pool})
		if err != nil {
			t.Fatal(err)
		}
		call := startedCall(fixture, 1, toolTestID(1641), searchToolRef, "", hash64('5'))
		result, err := faultRepository.StartCall(ctx, startCommand(fixture, call))
		if err != nil || result.Disposition != application.StartCallReplayed || result.Call.ID != call.ID || result.Call.Status != domain.CallStarted {
			t.Fatalf("started response loss=%+v err=%v", result, err)
		}
	})

	t.Run("refused response loss recovers canonical call", func(t *testing.T) {
		fixture := seedToolRuntime(t, ctx, pool, 65, primary.workspaceID)
		faultRepository, err := NewRepository(&commitResponseLossDB{pool: pool})
		if err != nil {
			t.Fatal(err)
		}
		call := refusedCall(fixture, 1, toolTestID(1651), "UnknownTool", hash64('6'))
		result, err := faultRepository.RecordRefused(ctx, application.RecordRefusedCommand{Identity: fixture.identity, Call: call})
		if err != nil || !result.Replayed || result.Call.ID != call.ID || result.Call.Status != domain.CallRefused {
			t.Fatalf("refused response loss=%+v err=%v", result, err)
		}
	})

	t.Run("timeline is stable scoped and contains no raw secret", func(t *testing.T) {
		timeline, err := repository.ListTimeline(ctx, application.ToolCallTimelineQuery{
			WorkspaceID: primary.workspaceID, WorkflowRunID: primary.runID, Limit: 20,
		})
		if err != nil || len(timeline) < 3 {
			t.Fatalf("ListTimeline len=%d err=%v", len(timeline), err)
		}
		for index := 1; index < len(timeline); index++ {
			if !timelineLess(timeline[index-1], timeline[index]) {
				t.Fatalf("timeline not stable at %d: %#v", index, timeline)
			}
		}
		nodeTimeline, err := repository.ListTimeline(ctx, application.ToolCallTimelineQuery{
			WorkspaceID: primary.workspaceID, WorkflowRunID: primary.runID, NodeRunID: primary.nodeID, Limit: 20,
		})
		if err != nil || len(nodeTimeline) != len(timeline) {
			t.Fatalf("node timeline len=%d want=%d err=%v", len(nodeTimeline), len(timeline), err)
		}
		crossWorkspace, err := repository.ListTimeline(ctx, application.ToolCallTimelineQuery{
			WorkspaceID: otherWorkspaceID, WorkflowRunID: primary.runID, Limit: 20,
		})
		if err != nil || len(crossWorkspace) != 0 {
			t.Fatalf("cross workspace timeline=%#v err=%v", crossWorkspace, err)
		}
		canaries := []string{"Bearer top-secret", "/Users/private/source.md", "raw-source-canary", "Authorization", "Cookie"}
		for _, canary := range canaries {
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_call call WHERE row_to_json(call)::text LIKE '%' || $1 || '%'`, canary).Scan(&count); err != nil || count != 0 {
				t.Fatalf("secret canary %q count=%d err=%v", canary, count, err)
			}
		}
		if readStarted.Status != domain.CallStarted {
			t.Fatalf("unexpected retained fixture=%#v", readStarted)
		}
	})
}

func TestToolCallInsertGuardLocksAndValidatesRuntimeFence(t *testing.T) {
	pool, ctx := newToolRepositoryIntegrationPool(t)
	workspaceID := toolTestID(301)

	t.Run("accepts one fully aligned active runtime", func(t *testing.T) {
		fixture := seedToolRuntime(t, ctx, pool, 30, workspaceID)
		if err := rawInsertStartedCall(ctx, pool, fixture, 1, toolTestID(1301)); err != nil {
			t.Fatalf("aligned insert failed: %v", err)
		}
	})

	cases := []struct {
		name   string
		mutate func(context.Context, *pgxpool.Pool, toolRuntimeFixture) error
	}{
		{name: "node attempt fence", mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture toolRuntimeFixture) error {
			_, err := pool.Exec(ctx, `UPDATE workflow.node_run SET attempt=attempt+1,updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID))
			return err
		}},
		{name: "lease owner", mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture toolRuntimeFixture) error {
			_, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_owner='other-worker',updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID))
			return err
		}},
		{name: "dual lease", mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture toolRuntimeFixture) error {
			_, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=lease_until+interval '1 second',updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID))
			return err
		}},
		{name: "run status", mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture toolRuntimeFixture) error {
			_, err := pool.Exec(ctx, `UPDATE workflow.run SET status='paused',updated_at=clock_timestamp() WHERE id=$1`, string(fixture.runID))
			return err
		}},
		{name: "node status", mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture toolRuntimeFixture) error {
			_, err := pool.Exec(ctx, `UPDATE workflow.node_run SET status='paused',updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID))
			return err
		}},
		{name: "attempt status", mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture toolRuntimeFixture) error {
			_, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET status='succeeded',lease_owner=NULL,lease_until=NULL,ended_at=clock_timestamp() WHERE id=$1`, string(fixture.attemptID))
			return err
		}},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedToolRuntime(t, ctx, pool, 31+index, workspaceID)
			if err := testCase.mutate(ctx, pool, fixture); err != nil {
				t.Fatal(err)
			}
			if err := rawInsertStartedCall(ctx, pool, fixture, 1, toolTestID(1310+index)); postgresErrorCode(err) != "55000" {
				t.Fatalf("insert code=%s err=%v", postgresErrorCode(err), err)
			}
		})
	}

	t.Run("long transaction observes lease expiration with wall clock", func(t *testing.T) {
		fixture := seedToolRuntime(t, ctx, pool, 40, workspaceID)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var transactionNow, leaseUntil time.Time
		if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP,clock_timestamp()+interval '75 milliseconds'`).Scan(&transactionNow, &leaseUntil); err != nil {
			t.Fatal(err)
		}
		if !leaseUntil.After(transactionNow) {
			t.Fatalf("invalid test clock transaction_now=%s lease_until=%s", transactionNow, leaseUntil)
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow.node_run SET lease_until=$2,updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID), leaseUntil); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=$2,heartbeat_at=clock_timestamp() WHERE id=$1`, string(fixture.attemptID), leaseUntil); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_sleep(0.15)`); err != nil {
			t.Fatal(err)
		}
		if err := rawInsertStartedCall(ctx, tx, fixture, 1, toolTestID(1340)); postgresErrorCode(err) != "55000" {
			t.Fatalf("expired insert code=%s err=%v", postgresErrorCode(err), err)
		}
	})
}

func TestRepositoryListsStaleStartedWithSkipLockedAndRecoversUnknown(t *testing.T) {
	pool, ctx := newToolRepositoryIntegrationPool(t)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedToolRuntime(t, ctx, pool, 60, toolTestID(601))
	call := startedCall(fixture, 1, toolTestID(1601), searchToolRef, "", hash64('7'))
	started, err := repository.StartCall(ctx, startCommand(fixture, call))
	if err != nil || started.Disposition != application.StartCallCreated {
		t.Fatalf("StartCall=%+v err=%v", started, err)
	}
	active, err := repository.RecoverStaleStarted(ctx, 10)
	if err != nil || len(active) != 0 {
		t.Fatalf("active candidates=%+v err=%v", active, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(fixture.attemptID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()-interval '1 second',updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(ctx, `SELECT id FROM workflow.tool_call WHERE id=$1 FOR UPDATE`, string(started.Call.ID)); err != nil {
		t.Fatal(err)
	}
	locked, err := repository.RecoverStaleStarted(ctx, 10)
	if err != nil || len(locked) != 0 {
		t.Fatalf("locked candidates=%+v err=%v", locked, err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	recovered, err := repository.RecoverStaleStarted(ctx, 10)
	if err != nil || len(recovered) != 1 || recovered[0].ID != started.Call.ID || recovered[0].Status != domain.CallUnknown {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	after, err := repository.RecoverStaleStarted(ctx, 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("recovered candidates=%+v err=%v", after, err)
	}

	concurrentFixture := seedToolRuntime(t, ctx, pool, 61, fixture.workspaceID)
	concurrentCall := startedCall(concurrentFixture, 1, toolTestID(1602), searchToolRef, "", hash64('8'))
	if _, err := repository.StartCall(ctx, startCommand(concurrentFixture, concurrentCall)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(concurrentFixture.attemptID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()-interval '1 second',updated_at=clock_timestamp() WHERE id=$1`, string(concurrentFixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	results := make([][]domain.ToolCall, 2)
	errorsFound := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsFound[index] = repository.RecoverStaleStarted(ctx, 10)
		}(index)
	}
	wait.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || len(results[0])+len(results[1]) != 1 {
		t.Fatalf("concurrent recovery results=%+v errors=%v", results, errorsFound)
	}
}

func TestRepositoryTrustedWritePreservesOriginalAttemptAndBypassesGenericRecovery(t *testing.T) {
	pool, ctx := newToolRepositoryIntegrationPool(t)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedTrustedWriteRuntime(t, ctx, pool, 62, toolTestID(621))
	receiptID := toolTestID(1621)
	call := trustedWriteStartedCall(fixture, fixture.attemptID, toolTestID(1622), receiptID)
	started, err := repository.StartTrustedWriteCall(ctx, application.TrustedWriteStartCommand{
		Identity: fixture.identity, Call: call,
		AllowedWorkflows: []domain.WorkflowBinding{{Key: fixture.workflowKey, Version: 1}},
	})
	if err != nil || started.Disposition != application.StartCallCreated {
		t.Fatalf("StartTrustedWriteCall=%+v err=%v", started, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET
		status='lease_lost',failure_class='lease_lost',error_kind='VersionConflict',error_code='WORKFLOW_LEASE_LOST',error_summary='lease expired',
		lease_owner=NULL,lease_until=NULL,ended_at=GREATEST(clock_timestamp(),lease_until),heartbeat_at=GREATEST(clock_timestamp(),lease_until)
		WHERE id=$1`, string(fixture.attemptID)); err != nil {
		t.Fatal(err)
	}
	newAttemptID := toolTestID(1623)
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET attempt=2,lease_owner='tool-worker-2',lease_until=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID), leaseUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at)
		VALUES($1,$2,2,2,1,$3,'tool-worker-2',$4,'running',clock_timestamp(),clock_timestamp())`, string(newAttemptID), string(fixture.nodeID), "trusted-write-redelivery", leaseUntil); err != nil {
		t.Fatal(err)
	}
	reclaimedIdentity := fixture.identity
	reclaimedIdentity.NodeAttemptID = newAttemptID
	reclaimedIdentity.LeaseOwner = "tool-worker-2"
	reclaimedIdentity.LeaseFence = 2
	reclaimedCall := trustedWriteStartedCall(fixture, newAttemptID, toolTestID(1624), receiptID)
	replayed, err := repository.StartTrustedWriteCall(ctx, application.TrustedWriteStartCommand{
		Identity: reclaimedIdentity, Call: reclaimedCall,
		AllowedWorkflows: []domain.WorkflowBinding{{Key: fixture.workflowKey, Version: 1}},
	})
	if err != nil || replayed.Disposition != application.StartCallReplayed || replayed.Call.ID != started.Call.ID || replayed.Call.NodeAttemptID != fixture.attemptID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	loadCommand := application.TrustedWriteLoadCommand{
		Identity: reclaimedIdentity, Tool: applyApprovedPatchRef, Capability: capability.WriteKnowledge,
		AllowedWorkflows: []domain.WorkflowBinding{{Key: fixture.workflowKey, Version: 1}},
		IdempotencyKey:   call.IdempotencyKey,
	}
	invalidWorkflow := loadCommand
	invalidWorkflow.AllowedWorkflows = []domain.WorkflowBinding{{Key: "different-workflow", Version: 1}}
	if _, err := repository.LoadTrustedWriteCall(ctx, invalidWorkflow); errorCode(err) != ErrorCodeWorkflowBindingDenied {
		t.Fatalf("invalid reconcile workflow code=%s err=%v", errorCode(err), err)
	}
	invalidCapability := loadCommand
	invalidCapability.Capability = capability.EvaluationRun
	if _, err := repository.LoadTrustedWriteCall(ctx, invalidCapability); errorCode(err) != ErrorCodePermissionDenied {
		t.Fatalf("invalid reconcile capability code=%s err=%v", errorCode(err), err)
	}
	invalidAttempt := loadCommand
	invalidAttempt.Identity.NodeAttemptID = toolTestID(1625)
	if _, err := repository.LoadTrustedWriteCall(ctx, invalidAttempt); errorCode(err) != ErrorCodeContextStale {
		t.Fatalf("invalid reconcile attempt code=%s err=%v", errorCode(err), err)
	}
	invalidHash := loadCommand
	invalidHash.Identity.DefinitionHash = hash64('b')
	if _, err := repository.LoadTrustedWriteCall(ctx, invalidHash); errorCode(err) != ErrorCodeContextStale {
		t.Fatalf("invalid reconcile definition code=%s err=%v", errorCode(err), err)
	}
	invalidNode := loadCommand
	invalidNode.Identity.NodeKey = "different-node"
	if _, err := repository.LoadTrustedWriteCall(ctx, invalidNode); errorCode(err) != ErrorCodeContextStale {
		t.Fatalf("invalid reconcile node code=%s err=%v", errorCode(err), err)
	}

	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(newAttemptID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()-interval '1 second',updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.RecoverStaleStarted(ctx, 10)
	if err != nil || len(recovered) != 0 {
		t.Fatalf("generic recovery touched trusted write: recovered=%+v err=%v", recovered, err)
	}
	if _, err := repository.LoadTrustedWriteCall(ctx, loadCommand); errorCode(err) != ErrorCodeContextStale {
		t.Fatalf("expired reconcile lease code=%s err=%v", errorCode(err), err)
	}
	renewedLease := time.Now().UTC().Add(5 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=$2,updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID), renewedLease); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=$2,heartbeat_at=clock_timestamp() WHERE id=$1`, string(newAttemptID), renewedLease); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.LoadTrustedWriteCall(ctx, loadCommand)
	if err != nil || loaded.ID != started.Call.ID || loaded.Status != domain.CallStarted {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}

	completedAt := time.Now().UTC()
	succeeded := loaded
	succeeded.Status = domain.CallSucceeded
	succeeded.ResponseHash = hash64('a')
	succeeded.ResponseBytes = 32
	succeeded.ResponseSummary = json.RawMessage(`{"status":"APPLIED"}`)
	succeeded.ResultRef = "writeback-execution:" + string(receiptID)
	succeeded.CompletedAt = &completedAt
	succeeded.DurationMillis = 1
	succeeded.Version++
	finalized, err := repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: loaded.Version, Call: succeeded})
	if err != nil || finalized.Call.Status != domain.CallSucceeded || finalized.Call.SideEffectID != string(receiptID) {
		t.Fatalf("finalized=%+v err=%v", finalized, err)
	}
}

func TestRepositoryStaleRecoverySkipsConcurrentHeartbeatAndRechecksFreshLease(t *testing.T) {
	pool, ctx := newToolRepositoryIntegrationPool(t)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedToolRuntime(t, ctx, pool, 63, toolTestID(631))
	call := startedCall(fixture, 1, toolTestID(1631), searchToolRef, "", hash64('5'))
	started, err := repository.StartCall(ctx, startCommand(fixture, call))
	if err != nil || started.Disposition != application.StartCallCreated {
		t.Fatalf("StartCall=%+v err=%v", started, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, string(fixture.attemptID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()-interval '1 second',updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}

	heartbeat, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = heartbeat.Rollback(ctx) }()
	future := time.Now().UTC().Add(5 * time.Minute)
	if _, err := heartbeat.Exec(ctx, `UPDATE workflow.node_run SET lease_until=$2,updated_at=clock_timestamp() WHERE id=$1`, string(fixture.nodeID), future); err != nil {
		t.Fatal(err)
	}
	if _, err := heartbeat.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=$2,heartbeat_at=clock_timestamp() WHERE id=$1`, string(fixture.attemptID), future); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.RecoverStaleStarted(ctx, 10)
	if err != nil || len(recovered) != 0 {
		t.Fatalf("recovery raced heartbeat: recovered=%+v err=%v", recovered, err)
	}
	if err := heartbeat.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err = repository.RecoverStaleStarted(ctx, 10)
	if err != nil || len(recovered) != 0 {
		t.Fatalf("fresh heartbeat was not rechecked: recovered=%+v err=%v", recovered, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.tool_call WHERE id=$1`, string(started.Call.ID)).Scan(&status); err != nil || status != string(domain.CallStarted) {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

type toolRuntimeFixture struct {
	workspaceID, definitionID, runID, nodeID, attemptID foundation.ID
	workflowKey                                         string
	identity                                            domain.TrustedExecutionIdentity
}

var (
	searchToolRef         = domain.ToolRef{Name: "SearchKnowledge", Version: 1}
	rebuildToolRef        = domain.ToolRef{Name: "RebuildIndex", Version: 1}
	applyApprovedPatchRef = domain.ToolRef{Name: "ApplyApprovedPatch", Version: 1}
)

func seedTrustedWriteRuntime(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ordinal int, workspaceID foundation.ID) toolRuntimeFixture {
	t.Helper()
	fixture := toolRuntimeFixture{
		workspaceID: workspaceID, definitionID: toolTestID(ordinal*10 + 2), runID: toolTestID(ordinal*10 + 3),
		nodeID: toolTestID(ordinal*10 + 4), attemptID: toolTestID(ordinal*10 + 5), workflowKey: "change-control.safe-writeback",
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
		VALUES($1,$2,$3,$3,clock_timestamp(),'active',clock_timestamp(),clock_timestamp())`, string(workspaceID), "trusted-write-workspace", "/tmp/trusted-write-"+string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	graph := workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key: "safe-writeback", Kind: "change_control.safe_writeback", InputSchemaVersion: 1, OutputSchemaVersion: 1,
		RetryPolicy:         workflowdomain.RetryPolicy{},
		RequiredPermissions: []workflowdomain.Permission{capability.GitWrite, capability.WriteKnowledge},
	}}}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphHash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,$4,clock_timestamp())`, []any{string(fixture.definitionID), string(workspaceID), fixture.workflowKey, graphJSON}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,idempotency_key,request_hash,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',$4,$5,1,clock_timestamp(),clock_timestamp())`, []any{string(fixture.runID), string(workspaceID), string(fixture.definitionID), "trusted-write-run", hash64('7')}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,idempotency_key,input_schema_version,output_schema_version,dispatch_no,
			lease_owner,lease_until,version,created_at,updated_at)
			VALUES($1,$2,'safe-writeback','change_control.safe_writeback','running',1,'{}',$3,1,1,1,'tool-worker',$4,2,clock_timestamp(),clock_timestamp())`, []any{string(fixture.nodeID), string(fixture.runID), "trusted-write-node", leaseUntil}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at)
			VALUES($1,$2,1,1,0,$3,'tool-worker',$4,'running',clock_timestamp(),clock_timestamp())`, []any{string(fixture.attemptID), string(fixture.nodeID), "trusted-write-delivery", leaseUntil}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	fixture.identity = domain.TrustedExecutionIdentity{
		WorkspaceID: workspaceID, DefinitionID: fixture.definitionID, DefinitionVersion: 1, DefinitionHash: graphHash,
		WorkflowRunID: fixture.runID, NodeKey: "safe-writeback", NodeRunID: fixture.nodeID, NodeAttemptID: fixture.attemptID,
		LeaseOwner: "tool-worker", LeaseFence: 1,
	}
	return fixture
}

func trustedWriteStartedCall(fixture toolRuntimeFixture, attemptID, callID, receiptID foundation.ID) domain.ToolCall {
	tool := applyApprovedPatchRef
	return domain.ToolCall{
		ID: callID, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, NodeAttemptID: attemptID,
		CallNo: 1, RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: hash64('9'),
		InputSchema: &domain.SchemaRef{ID: "tool.apply_approved_patch.input", Version: 1}, OutputSchema: &domain.SchemaRef{ID: "tool.apply_approved_patch.output", Version: 1},
		Capability: capability.WriteKnowledge, SideEffectLevel: domain.SideEffectDomainWrite, InvocationPolicy: domain.InvocationTrustedWorkflowOnly,
		IdempotencyKey: "safe-writeback-tool:" + string(receiptID) + ":apply:v1", RequestHash: hash64('8'), RequestBytes: 128,
		RequestSummary: json.RawMessage(`{"field_count":1}`), SideEffectType: "writeback_execution", SideEffectID: string(receiptID),
		Status: domain.CallStarted, Version: 1, StartedAt: time.Now().UTC(),
	}
}

func seedToolRuntime(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ordinal int, workspaceID foundation.ID) toolRuntimeFixture {
	t.Helper()
	fixture := toolRuntimeFixture{
		workspaceID: workspaceID, definitionID: toolTestID(ordinal*10 + 2), runID: toolTestID(ordinal*10 + 3),
		nodeID: toolTestID(ordinal*10 + 4), attemptID: toolTestID(ordinal*10 + 5), workflowKey: fmt.Sprintf("tool-workflow-%d", ordinal),
	}
	var workspaceExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.workspace WHERE id=$1)`, string(workspaceID)).Scan(&workspaceExists); err != nil {
		t.Fatal(err)
	}
	if !workspaceExists {
		if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
			VALUES($1,$2,$3,$3,clock_timestamp(),'active',clock_timestamp(),clock_timestamp())`, string(workspaceID), "tool-workspace-"+string(workspaceID), "/tmp/tool-workspace-"+string(workspaceID)); err != nil {
			t.Fatal(err)
		}
	}
	graph := workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key: "tool-node", Kind: "agent.tool", InputSchemaVersion: 1, OutputSchemaVersion: 1,
		RetryPolicy:         workflowdomain.RetryPolicy{},
		RequiredPermissions: []workflowdomain.Permission{capability.IndexMaintenance, capability.ReadLocal},
		AllowedTools:        []domain.ToolRef{rebuildToolRef, searchToolRef},
	}}}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphHash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,$4,clock_timestamp())`, []any{string(fixture.definitionID), string(workspaceID), fixture.workflowKey, graphJSON}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,idempotency_key,request_hash,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',$4,$5,1,clock_timestamp(),clock_timestamp())`, []any{string(fixture.runID), string(workspaceID), string(fixture.definitionID), "tool-run-" + fixture.workflowKey, hash64('7')}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,idempotency_key,input_schema_version,output_schema_version,dispatch_no,
			lease_owner,lease_until,version,created_at,updated_at)
			VALUES($1,$2,'tool-node','agent.tool','running',1,'{}',$3,1,1,1,'tool-worker',$4,2,clock_timestamp(),clock_timestamp())`, []any{string(fixture.nodeID), string(fixture.runID), "tool-node-" + fixture.workflowKey, leaseUntil}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at)
			VALUES($1,$2,1,1,0,$3,'tool-worker',$4,'running',clock_timestamp(),clock_timestamp())`, []any{string(fixture.attemptID), string(fixture.nodeID), "tool-delivery-" + fixture.workflowKey, leaseUntil}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	fixture.identity = domain.TrustedExecutionIdentity{
		WorkspaceID: workspaceID, DefinitionID: fixture.definitionID, DefinitionVersion: 1, DefinitionHash: graphHash,
		WorkflowRunID: fixture.runID, NodeKey: "tool-node", NodeRunID: fixture.nodeID, NodeAttemptID: fixture.attemptID,
		LeaseOwner: "tool-worker", LeaseFence: 1,
	}
	return fixture
}

func renewToolLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture toolRuntimeFixture, duration time.Duration) {
	t.Helper()
	leaseUntil := time.Now().UTC().Add(duration)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE workflow.node_run SET lease_until=$2 WHERE id=$1`, string(fixture.nodeID), leaseUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=$2,heartbeat_at=clock_timestamp() WHERE id=$1`, string(fixture.attemptID), leaseUntil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func startedCall(fixture toolRuntimeFixture, callNo int, id foundation.ID, ref domain.ToolRef, idempotencyKey, requestHash string) domain.ToolCall {
	tool := ref
	capabilityValue := capability.ReadLocal
	sideEffect := domain.SideEffectNone
	invocation := domain.InvocationModelRequestable
	if ref == rebuildToolRef {
		capabilityValue = capability.IndexMaintenance
		sideEffect = domain.SideEffectDomainWrite
		invocation = domain.InvocationTrustedWorkflowOnly
	}
	return domain.ToolCall{
		ID: id, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, NodeAttemptID: fixture.attemptID,
		CallNo: callNo, RequestedToolName: ref.Name, Tool: &tool, DefinitionHash: hash64('9'),
		InputSchema: &domain.SchemaRef{ID: "tool.input", Version: 1}, OutputSchema: &domain.SchemaRef{ID: "tool.output", Version: 1},
		Capability: capabilityValue, SideEffectLevel: sideEffect, InvocationPolicy: invocation, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, RequestBytes: 64, RequestSummary: json.RawMessage(`{"field_count":1}`),
		Status: domain.CallStarted, Version: 1, StartedAt: time.Now().UTC(),
	}
}

func refusedCall(fixture toolRuntimeFixture, callNo int, id foundation.ID, requestedTool, requestHash string) domain.ToolCall {
	now := time.Now().UTC()
	return domain.ToolCall{
		ID: id, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, NodeAttemptID: fixture.attemptID,
		CallNo: callNo, RequestedToolName: requestedTool, RequestHash: requestHash, RequestBytes: 32,
		RequestSummary: json.RawMessage(`{"field_count":0}`), Status: domain.CallRefused, ErrorCode: "TOOL_NOT_REGISTERED",
		Version: 1, StartedAt: now, CompletedAt: &now,
	}
}

func successfulReadCall(started domain.ToolCall, responseHash string) domain.ToolCall {
	result := started
	completedAt := time.Now().UTC()
	result.Status, result.ResponseHash, result.ResponseBytes = domain.CallSucceeded, responseHash, 48
	result.ResponseSummary = json.RawMessage(`{"result_count":1}`)
	result.Version, result.CompletedAt, result.DurationMillis = 2, &completedAt, 1
	return result
}

func unknownCall(started domain.ToolCall) domain.ToolCall {
	result := started
	completedAt := time.Now().UTC()
	result.Status, result.ErrorCode, result.Version = domain.CallUnknown, "TOOL_OUTCOME_UNKNOWN", 2
	result.CompletedAt, result.DurationMillis = &completedAt, 1
	return result
}

func assertTerminalBindingCAS(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *Repository,
	fixture toolRuntimeFixture,
	callNo int,
	callID foundation.ID,
	unknown bool,
	mutate func(*domain.ToolCall),
) {
	t.Helper()
	call := startedCall(fixture, callNo, callID, searchToolRef, fmt.Sprintf("read-binding-%d", callNo), hash64('5'))
	started, err := repository.StartCall(ctx, startCommand(fixture, call))
	if err != nil || started.Disposition != application.StartCallCreated {
		t.Fatalf("StartCall=%#v err=%v", started, err)
	}
	terminal := successfulReadCall(started.Call, hash64('6'))
	if unknown {
		terminal = unknownCall(started.Call)
	}
	changed := terminal
	mutate(&changed)
	if unknown {
		_, err = repository.MarkUnknown(ctx, application.MarkUnknownCommand{ExpectedVersion: 1, Call: changed})
	} else {
		_, err = repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: changed})
	}
	if errorCode(err) != ErrorCodeCallVersionConflict {
		t.Fatalf("changed binding code=%s err=%v", errorCode(err), err)
	}
	var status string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT status,version FROM workflow.tool_call WHERE id=$1`, string(started.Call.ID)).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.CallStarted) || version != 1 {
		t.Fatalf("changed binding mutated row status=%s version=%d", status, version)
	}
	if unknown {
		_, err = repository.MarkUnknown(ctx, application.MarkUnknownCommand{ExpectedVersion: 1, Call: terminal})
	} else {
		_, err = repository.FinalizeCall(ctx, application.FinalizeCallCommand{ExpectedVersion: 1, Call: terminal})
	}
	if err != nil {
		t.Fatalf("correct terminal failed: %v", err)
	}
}

type toolCallSQLExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func rawInsertStartedCall(ctx context.Context, executor toolCallSQLExecutor, fixture toolRuntimeFixture, callNo int, id foundation.ID) error {
	call := startedCall(fixture, callNo, id, searchToolRef, "", hash64('4'))
	_, err := executor.Exec(ctx, `INSERT INTO workflow.tool_call(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
		requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
		output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
		request_hash,request_bytes,request_summary,status,retryable,version,started_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,'STARTED',false,1,clock_timestamp())`,
		string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo,
		call.RequestedToolName, call.Tool.Version, call.DefinitionHash, call.InputSchema.ID, call.InputSchema.Version,
		call.OutputSchema.ID, call.OutputSchema.Version, string(call.Capability), string(call.SideEffectLevel), string(call.InvocationPolicy),
		call.RequestHash, call.RequestBytes, call.RequestSummary)
	return err
}

func postgresErrorCode(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return ""
}

func startCommand(fixture toolRuntimeFixture, call domain.ToolCall) application.StartCallCommand {
	return application.StartCallCommand{
		Identity: fixture.identity, Call: call,
		AllowedWorkflows: []domain.WorkflowBinding{{Key: fixture.workflowKey, Version: 1}},
	}
}

func withIdentity(identity domain.TrustedExecutionIdentity, change func(*domain.TrustedExecutionIdentity)) domain.TrustedExecutionIdentity {
	change(&identity)
	return identity
}

func timePointer(value time.Time) *time.Time { return &value }

func hash64(character byte) string { return strings.Repeat(string(character), 64) }

func toolTestID(ordinal int) foundation.ID {
	return foundation.ID(fmt.Sprintf("90000000-0000-4000-8000-%012d", ordinal))
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

type commitResponseLossDB struct {
	pool *pgxpool.Pool
	lost atomic.Bool
}

func (database *commitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if !database.lost.Swap(true) {
		return &commitResponseLossTx{Tx: tx}, nil
	}
	return tx, nil
}

type commitResponseLossTx struct{ pgx.Tx }

func (tx *commitResponseLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("simulated response loss after commit")
}

func newToolRepositoryIntegrationPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Tool repository integration tests")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := fmt.Sprintf("zhixu_tools_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + databaseName
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err == nil {
		var runner *platformmigration.Runner
		runner, err = platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if err == nil {
			err = runner.Up(ctx)
		}
		migrationPool.Close()
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	return pool, ctx
}
