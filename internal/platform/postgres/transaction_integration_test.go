//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	river "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

const foundationProbeTable = "foundation_test.transaction_probe"

type foundationIntegrationFixture struct {
	platform *platformpostgres.Pool
	admin    *pgxpool.Pool
	database string
}

func newFoundationIntegrationFixture(t *testing.T) *foundationIntegrationFixture {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatalf("open integration admin pool: %v", err)
	}
	databaseName := "zhixu_foundation_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	databaseIdentifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+databaseIdentifier); err != nil {
		admin.Close()
		t.Fatalf("create isolated integration database: %v", err)
	}
	parsed.Path = "/" + databaseName
	databaseURL := parsed.String()
	rawPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cleanupFoundationDatabase(t, admin, databaseIdentifier)
		t.Fatalf("open isolated integration pool: %v", err)
	}
	if _, err := rawPool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		rawPool.Close()
		cleanupFoundationDatabase(t, admin, databaseIdentifier)
		t.Fatalf("install pgvector extension: %v", err)
	}
	if _, err := rawPool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS workflow; CREATE SCHEMA IF NOT EXISTS foundation_test; CREATE TABLE "+foundationProbeTable+" (id text PRIMARY KEY, value text NOT NULL)"); err != nil {
		rawPool.Close()
		cleanupFoundationDatabase(t, admin, databaseIdentifier)
		t.Fatalf("create foundation integration fixture: %v", err)
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(rawPool), &rivermigrate.Config{Schema: "workflow"})
	if err != nil {
		rawPool.Close()
		cleanupFoundationDatabase(t, admin, databaseIdentifier)
		t.Fatalf("create River migrator: %v", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		rawPool.Close()
		cleanupFoundationDatabase(t, admin, databaseIdentifier)
		t.Fatalf("migrate River schema: %v", err)
	}
	validation, err := migrator.Validate(ctx, nil)
	if err != nil || validation == nil || !validation.OK {
		rawPool.Close()
		cleanupFoundationDatabase(t, admin, databaseIdentifier)
		if err != nil {
			t.Fatalf("validate River schema: %v", err)
		}
		t.Fatalf("validate River schema: %#v", validation)
	}
	rawPool.Close()
	platform, err := platformpostgres.Open(ctx, databaseURL, 4, 0)
	if err != nil {
		cleanupFoundationDatabase(t, admin, databaseIdentifier)
		t.Fatalf("open shared GORM platform pool: %v", err)
	}
	fixture := &foundationIntegrationFixture{platform: platform, admin: admin, database: databaseIdentifier}
	t.Cleanup(func() {
		fixture.platform.Close()
		cleanupFoundationDatabase(t, fixture.admin, fixture.database)
	})
	return fixture
}

func cleanupFoundationDatabase(t *testing.T, admin *pgxpool.Pool, databaseIdentifier string) {
	t.Helper()
	if admin == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.Exec(ctx, "DROP DATABASE "+databaseIdentifier+" WITH (FORCE)"); err != nil {
		t.Errorf("drop isolated foundation database: %v", err)
	}
	admin.Close()
}

func foundationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 20*time.Second)
}

func TestRealGORMUnitOfWorkCommitRollbackAndScopeLifetime(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	ctx, cancel := foundationContext(t)
	defer cancel()
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	committedID := "commit-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := uow.Within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationReadCommitted}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if result := transaction.WithContext(callbackCtx).Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", committedID, "committed"); result.Error != nil {
			return result.Error
		}
		if _, err := platformpostgres.SQLTransaction(scope); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	assertProbeValue(t, fixture.platform, ctx, committedID, "committed")
	var postgresError *pgconn.PgError
	err = uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return transaction.Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", committedID, "duplicate").Error
	})
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		t.Fatalf("duplicate error=%v, want PostgreSQL SQLSTATE 23505", err)
	}
	readOnlyID := "readonly-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	postgresError = nil
	err = uow.Within(ctx, foundation.TransactionOptions{ReadOnly: true}, func(_ context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return transaction.Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", readOnlyID, "must-fail").Error
	})
	if !errors.As(err, &postgresError) || postgresError.Code != "25006" {
		t.Fatalf("read-only error=%v, want PostgreSQL SQLSTATE 25006", err)
	}
	assertProbeMissing(t, fixture.platform, ctx, readOnlyID)

	rollbackID := "rollback-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	rollbackCause := errors.New("rollback requested by caller")
	err = uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if result := transaction.Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", rollbackID, "rolled-back"); result.Error != nil {
			return result.Error
		}
		return rollbackCause
	})
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback error=%v, want cause %v", err, rollbackCause)
	}
	assertProbeMissing(t, fixture.platform, ctx, rollbackID)

	var scope foundation.TransactionScope
	if err := uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, current foundation.TransactionScope) error {
		scope = current
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := platformpostgres.GORMTransaction(scope); err == nil {
		t.Fatal("stale scope unexpectedly remained usable for GORM")
	}
	if _, err := platformpostgres.SQLTransaction(scope); err == nil {
		t.Fatal("stale scope unexpectedly remained usable for database/sql")
	}
}

func TestRealGORMUnitOfWorkPreservesCancelCause(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("caller stopped the transaction")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	err = uow.Within(ctx, foundation.TransactionOptions{}, func(context.Context, foundation.TransactionScope) error {
		t.Fatal("transaction callback ran after cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("cancel error=%v, want context.Canceled and custom cause", err)
	}
	if err := uow.Within(context.Background(), foundation.TransactionOptions{Isolation: foundation.TransactionIsolation(255)}, func(context.Context, foundation.TransactionScope) error {
		return nil
	}); err == nil {
		t.Fatal("invalid transaction isolation unexpectedly succeeded")
	}
	if _, err := fixture.platform.UnitOfWork(); err != nil {
		t.Fatalf("unit of work became unavailable before close: %v", err)
	}
	activeCause := errors.New("active query cancellation requested")
	activeCtx, activeCancel := context.WithCancelCause(context.Background())
	err = uow.Within(activeCtx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		timer := time.AfterFunc(100*time.Millisecond, func() { activeCancel(activeCause) })
		defer timer.Stop()
		return transaction.WithContext(callbackCtx).Exec("SELECT pg_sleep(5)").Error
	})
	activeCancel(nil)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, activeCause) {
		t.Fatalf("active cancellation error=%v, want context.Canceled and custom cause", err)
	}
	statementTimeoutCtx, statementTimeoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer statementTimeoutCancel()
	var statementTimeoutError *pgconn.PgError
	err = uow.Within(statementTimeoutCtx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if result := transaction.WithContext(callbackCtx).Exec("SET LOCAL statement_timeout = '50ms'"); result.Error != nil {
			return result.Error
		}
		return transaction.WithContext(callbackCtx).Exec("SELECT pg_sleep(1)").Error
	})
	if !errors.As(err, &statementTimeoutError) || statementTimeoutError.Code != "57014" {
		t.Fatalf("statement timeout error=%v, want PostgreSQL SQLSTATE 57014", err)
	}
}

func TestRealGORMRiverInsertCommitsWithBusinessWriteAndWorkerConsumes(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	ctx, cancel := foundationContext(t)
	defer cancel()
	workers := river.NewWorkers()
	recorded := make(chan application.ExecutionContext, 1)
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	const nodeKind = "foundation.integration"
	if err := registry.Register(nodeKind, 1, foundationRecordingExecutor{recorded: recorded}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	worker, err := river.NewNodeWorker(registry, foundationExecutionProvider{kind: nodeKind})
	if err != nil {
		t.Fatal(err)
	}
	if err := river.AddWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	runtimeClient, err := river.NewClient(fixture.platform.DB(), workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := runtimeClient.Stop(stopCtx); err != nil {
			t.Errorf("stop River worker: %v", err)
		}
	})
	insertClient, err := river.NewClient(fixture.platform.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := river.NewScopedJobInserter(fixture.platform, insertClient, foundationAllowEnqueueFence{})
	if err != nil {
		t.Fatal(err)
	}
	nodeRunID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	args, err := river.NewNodeJobArgs(nodeRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	var receipt river.JobReceipt
	if err := uow.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if result := transaction.WithContext(callbackCtx).Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", string(nodeRunID), "ready"); result.Error != nil {
			return result.Error
		}
		receipt, err = inserter.InsertTx(callbackCtx, scope, args, river.InsertOptions{})
		if err != nil {
			return err
		}
		var visible int
		if err := fixture.platform.DB().QueryRow(callbackCtx, "SELECT count(*) FROM workflow.river_job WHERE id=$1", receipt.JobID).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			return fmt.Errorf("uncommitted River job visible to another connection: count=%d", visible)
		}
		return nil
	}); err != nil {
		t.Fatalf("commit GORM and River transaction: %v", err)
	}
	assertProbeValue(t, fixture.platform, ctx, string(nodeRunID), "ready")
	select {
	case execution := <-recorded:
		if execution.NodeRunID != nodeRunID || execution.DispatchNo != 1 || execution.NodeKind != nodeKind {
			t.Fatalf("worker execution=%#v", execution)
		}
	case <-ctx.Done():
		t.Fatalf("wait for pgx River worker consumption: %v", ctx.Err())
	}
	deadline := time.NewTicker(25 * time.Millisecond)
	defer deadline.Stop()
	for {
		var state string
		if err := fixture.platform.DB().QueryRow(ctx, "SELECT state FROM workflow.river_job WHERE id=$1", receipt.JobID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == string(rivertype.JobStateCompleted) {
			break
		}
		select {
		case <-deadline.C:
		case <-ctx.Done():
			t.Fatalf("River job state did not reach completed: state=%q err=%v", state, ctx.Err())
		}
	}
}

func TestRealGORMRiverWorkerAndTransactionsShareBoundedPool(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	ctx, cancel := foundationContext(t)
	defer cancel()
	const transactionCount = 8
	workers := river.NewWorkers()
	recorded := make(chan application.ExecutionContext, transactionCount)
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	const nodeKind = "foundation.integration.pressure"
	if err := registry.Register(nodeKind, 1, foundationRecordingExecutor{recorded: recorded}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	worker, err := river.NewNodeWorker(registry, foundationExecutionProvider{kind: nodeKind})
	if err != nil {
		t.Fatal(err)
	}
	if err := river.AddWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	runtimeClient, err := river.NewClient(fixture.platform.DB(), workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := runtimeClient.Stop(stopCtx); err != nil {
			t.Errorf("stop River worker: %v", err)
		}
	})
	insertClient, err := river.NewClient(fixture.platform.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := river.NewScopedJobInserter(fixture.platform, insertClient, foundationAllowEnqueueFence{})
	if err != nil {
		t.Fatal(err)
	}
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	generator := foundation.NewUUIDGenerator(nil)
	argsList := make([]river.NodeJobArgs, transactionCount)
	prefix := "worker-pressure-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "-"
	for index := range argsList {
		nodeRunID, err := generator.New()
		if err != nil {
			t.Fatal(err)
		}
		argsList[index], err = river.NewNodeJobArgs(nodeRunID, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	errorsCh := make(chan error, transactionCount)
	var waitGroup sync.WaitGroup
	waitGroup.Add(transactionCount)
	for index, args := range argsList {
		index, args := index, args
		go func() {
			defer waitGroup.Done()
			errorsCh <- uow.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
				transaction, err := platformpostgres.GORMTransaction(scope)
				if err != nil {
					return err
				}
				probeID := prefix + strconv.Itoa(index)
				if result := transaction.WithContext(callbackCtx).Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", probeID, "worker-pressure"); result.Error != nil {
					return result.Error
				}
				if _, err := inserter.InsertTx(callbackCtx, scope, args, river.InsertOptions{}); err != nil {
					return err
				}
				return transaction.WithContext(callbackCtx).Exec("SELECT pg_sleep(0.05)").Error
			})
		}()
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent transaction with River worker: %v", err)
		}
	}
	seen := make(map[foundation.ID]struct{}, transactionCount)
	for index := 0; index < transactionCount; index++ {
		select {
		case execution := <-recorded:
			if execution.NodeKind != nodeKind || execution.DispatchNo != 1 {
				t.Fatalf("worker execution=%#v", execution)
			}
			seen[execution.NodeRunID] = struct{}{}
		case <-ctx.Done():
			t.Fatalf("wait for concurrent River worker consumption: %v", ctx.Err())
		}
	}
	if len(seen) != transactionCount {
		t.Fatalf("worker executions=%d, want %d", len(seen), transactionCount)
	}
	var count int
	if err := fixture.platform.DB().QueryRow(ctx, "SELECT count(*) FROM "+foundationProbeTable+" WHERE id LIKE $1", prefix+"%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != transactionCount {
		t.Fatalf("concurrent worker transaction rows=%d, want %d", count, transactionCount)
	}
}

func TestRealGORMRiverRollbackRemovesBusinessWriteAndJob(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	ctx, cancel := foundationContext(t)
	defer cancel()
	insertClient, err := river.NewClient(fixture.platform.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := river.NewScopedJobInserter(fixture.platform, insertClient, foundationAllowEnqueueFence{})
	if err != nil {
		t.Fatal(err)
	}
	nodeRunID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	args, err := river.NewNodeJobArgs(nodeRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	rollbackCause := errors.New("rollback business and job")
	var receipt river.JobReceipt
	err = uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if result := transaction.Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", string(nodeRunID), "must-rollback"); result.Error != nil {
			return result.Error
		}
		receipt, err = inserter.InsertTx(ctx, scope, args, river.InsertOptions{})
		if err != nil {
			return err
		}
		return rollbackCause
	})
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback error=%v, want %v", err, rollbackCause)
	}
	assertProbeMissing(t, fixture.platform, ctx, string(nodeRunID))
	var jobs int
	if err := fixture.platform.DB().QueryRow(ctx, "SELECT count(*) FROM workflow.river_job WHERE id=$1", receipt.JobID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("rolled-back River jobs=%d, want 0", jobs)
	}
}

func TestRealGORMScopedFenceRejectsBeforeRiverInsert(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	ctx, cancel := foundationContext(t)
	defer cancel()
	insertClient, err := river.NewClient(fixture.platform.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	fenceCause := errors.New("enqueue fence rejected rollout drain")
	inserter, err := river.NewScopedJobInserter(fixture.platform, insertClient, foundationRejectEnqueueFence{err: fenceCause})
	if err != nil {
		t.Fatal(err)
	}
	nodeRunID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	args, err := river.NewNodeJobArgs(nodeRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	var receipt river.JobReceipt
	err = uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if result := transaction.Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", string(nodeRunID), "fence-must-rollback"); result.Error != nil {
			return result.Error
		}
		receipt, err = inserter.InsertTx(ctx, scope, args, river.InsertOptions{})
		return err
	})
	if !errors.Is(err, fenceCause) {
		t.Fatalf("fence error=%v, want %v", err, fenceCause)
	}
	if receipt.JobID != 0 {
		t.Fatalf("rejected fence returned receipt=%#v", receipt)
	}
	assertProbeMissing(t, fixture.platform, ctx, string(nodeRunID))
	var jobs int
	if err := fixture.platform.DB().QueryRow(ctx, "SELECT count(*) FROM workflow.river_job WHERE args->>'node_run_id'=$1", string(nodeRunID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("fence-rejected River jobs=%d, want 0", jobs)
	}
}

func TestRealGORMCommitResponseLossLeavesCommittedFactsAndExactRiverReplay(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	ctx, cancel := foundationContext(t)
	defer cancel()
	insertClient, err := river.NewClient(fixture.platform.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := river.NewScopedJobInserter(fixture.platform, insertClient, foundationAllowEnqueueFence{})
	if err != nil {
		t.Fatal(err)
	}
	nodeRunID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	args, err := river.NewNodeJobArgs(nodeRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	commitLoss := errors.New("commit acknowledgement was lost after PostgreSQL commit")
	lossUOW := postCommitErrorUnitOfWork{inner: uow, err: commitLoss}
	var original river.JobReceipt
	err = lossUOW.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if result := transaction.Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", string(nodeRunID), "committed-before-ack-loss"); result.Error != nil {
			return result.Error
		}
		original, err = inserter.InsertTx(ctx, scope, args, river.InsertOptions{})
		return err
	})
	if !errors.Is(err, commitLoss) {
		t.Fatalf("response-loss error=%v, want %v", err, commitLoss)
	}
	assertProbeValue(t, fixture.platform, ctx, string(nodeRunID), "committed-before-ack-loss")
	var persisted int
	if err := fixture.platform.DB().QueryRow(ctx, "SELECT count(*) FROM workflow.river_job WHERE id=$1", original.JobID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 {
		t.Fatalf("persisted River jobs=%d, want 1", persisted)
	}
	var replay river.JobReceipt
	if err := uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
		var err error
		replay, err = inserter.InsertTx(ctx, scope, args, river.InsertOptions{})
		return err
	}); err != nil {
		t.Fatalf("exact River replay: %v", err)
	}
	if !replay.Duplicate || replay.JobID != original.JobID {
		t.Fatalf("replay receipt=%#v, original=%#v", replay, original)
	}
}

func TestRealGORMPoolUsesSingleFacadeAndClosesIdempotently(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	root, err := fixture.platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	if !root.Config.SkipDefaultTransaction || root.Config.PrepareStmt || !root.Config.DisableAutomaticPing || root.Config.TranslateError {
		t.Fatalf("unexpected GORM config: %+v", root.Config)
	}
	sqlDB, ok := root.ConnPool.(*sql.DB)
	if !ok || sqlDB == nil {
		t.Fatalf("GORM root ConnPool=%T, want *sql.DB", root.ConnPool)
	}
	if _, err := fixture.platform.RiverSQLDriver(); err != nil {
		t.Fatal(err)
	}
	if fixture.platform.DB() == nil {
		t.Fatal("platform did not retain a pgx pool")
	}
	rows, err := root.Raw("SELECT 1").Rows()
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := sqlDB.Stats(); stats.Idle != 0 {
		t.Fatalf("database/sql facade retained idle connections=%d, want 0", stats.Idle)
	}
	fixture.platform.Close()
	fixture.platform.Close()
	if _, err := fixture.platform.GORM(); err == nil {
		t.Fatal("closed pool still exposed GORM root")
	}
	if _, err := fixture.platform.UnitOfWork(); err == nil {
		t.Fatal("closed pool still exposed unit of work")
	}
	if _, err := fixture.platform.RiverSQLDriver(); err == nil {
		t.Fatal("closed pool still exposed River database/sql driver")
	}
	if err := fixture.platform.Ping(context.Background()); err == nil {
		t.Fatal("closed pool unexpectedly passed Ping")
	}
}

func TestRealGORMPoolHandlesBoundedConcurrentTransactions(t *testing.T) {
	fixture := newFoundationIntegrationFixture(t)
	ctx, cancel := foundationContext(t)
	defer cancel()
	uow, err := fixture.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	prefix := "pressure-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "-"
	const transactionCount = 8
	errorsCh := make(chan error, transactionCount)
	var waitGroup sync.WaitGroup
	waitGroup.Add(transactionCount)
	for index := 0; index < transactionCount; index++ {
		index := index
		go func() {
			defer waitGroup.Done()
			id := prefix + strconv.Itoa(index)
			errorsCh <- uow.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
				transaction, err := platformpostgres.GORMTransaction(scope)
				if err != nil {
					return err
				}
				if result := transaction.WithContext(callbackCtx).Exec("INSERT INTO "+foundationProbeTable+" (id, value) VALUES (?, ?)", id, "pool-pressure"); result.Error != nil {
					return result.Error
				}
				return transaction.WithContext(callbackCtx).Exec("SELECT pg_sleep(0.05)").Error
			})
		}()
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent transaction: %v", err)
		}
	}
	var count int
	if err := fixture.platform.DB().QueryRow(ctx, "SELECT count(*) FROM "+foundationProbeTable+" WHERE id LIKE $1", prefix+"%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != transactionCount {
		t.Fatalf("concurrent committed rows=%d, want %d", count, transactionCount)
	}
}

func assertProbeValue(t *testing.T, pool *platformpostgres.Pool, ctx context.Context, id, value string) {
	t.Helper()
	var got string
	if err := pool.DB().QueryRow(ctx, "SELECT value FROM "+foundationProbeTable+" WHERE id=$1", id).Scan(&got); err != nil {
		t.Fatalf("read probe %q: %v", id, err)
	}
	if got != value {
		t.Fatalf("probe %q value=%q, want %q", id, got, value)
	}
}

func assertProbeMissing(t *testing.T, pool *platformpostgres.Pool, ctx context.Context, id string) {
	t.Helper()
	var count int
	if err := pool.DB().QueryRow(ctx, "SELECT count(*) FROM "+foundationProbeTable+" WHERE id=$1", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("probe %q count=%d, want 0", id, count)
	}
}

type foundationAllowEnqueueFence struct{}

func (foundationAllowEnqueueFence) CheckEnqueue(context.Context, foundation.TransactionScope) error {
	return nil
}

type foundationRejectEnqueueFence struct{ err error }

func (fence foundationRejectEnqueueFence) CheckEnqueue(context.Context, foundation.TransactionScope) error {
	return fence.err
}

type foundationExecutionProvider struct{ kind string }

func (provider foundationExecutionProvider) LoadExecutionContext(_ context.Context, args river.NodeJobArgs) (application.ExecutionContext, error) {
	return application.ExecutionContext{NodeRunID: args.NodeRunID, DispatchNo: args.DispatchNo, NodeKind: provider.kind, InputSchemaVersion: 1, Input: json.RawMessage(`{"value":1}`)}, nil
}

type foundationRecordingExecutor struct {
	recorded chan<- application.ExecutionContext
}

func (executor foundationRecordingExecutor) Execute(ctx context.Context, execution application.ExecutionContext) (application.ExecutionResult, error) {
	select {
	case executor.recorded <- execution:
		return application.ExecutionResult{Output: json.RawMessage(`{"ok":true}`)}, nil
	case <-ctx.Done():
		return application.ExecutionResult{}, ctx.Err()
	}
}

type postCommitErrorUnitOfWork struct {
	inner foundation.UnitOfWork
	err   error
}

func (unitOfWork postCommitErrorUnitOfWork) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := unitOfWork.inner.Within(ctx, options, work); err != nil {
		return err
	}
	return unitOfWork.err
}
