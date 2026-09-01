//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lib/pq"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestReviewLearningPathPostgreSQLLegacyAndGORMCreateReadParity(t *testing.T) {
	runReviewPathIntegrationVariants(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, store pathapp.Store) {
		bridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
		service := newReviewPathService(t, store, bridge)
		command := pathapp.CreateReviewCommand{
			WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
			IdempotencyKey: "review-path-parity-create",
		}
		created, err := service.CreateForReview(ctx, command)
		if err != nil || created.Replayed || created.Path.ID == "" || len(created.Steps) == 0 {
			t.Fatalf("create result=%+v err=%v", created, err)
		}
		read, err := store.GetByReviewAnswer(ctx, fixture.workspaceID, fixture.answerID)
		if err != nil || read.Path.ID != created.Path.ID || len(read.Steps) != len(created.Steps) {
			t.Fatalf("read result=%+v err=%v created=%+v", read, err, created)
		}
		readByID, err := store.Get(ctx, fixture.workspaceID, created.Path.ID)
		if err != nil || readByID.Path.ID != created.Path.ID || len(readByID.Steps) != len(created.Steps) {
			t.Fatalf("read by id result=%+v err=%v created=%+v", readByID, err, created)
		}
		replay, err := service.CreateForReview(ctx, command)
		if err != nil || !replay.Replayed || replay.Path.ID != created.Path.ID {
			t.Fatalf("replay result=%+v err=%v", replay, err)
		}
		closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
		assertReviewPathTerminalClosure(t, closure, fixture.answerID)
		if len(closure.paths) != 1 || len(closure.steps) != len(created.Steps) || len(closure.holds) != 0 ||
			len(closure.pathCommands) != 1 {
			t.Fatalf("create closure=%+v", closure)
		}

		step := created.Steps[0]
		stepCommand := pathapp.UpdateStepCommand{
			WorkspaceID: fixture.workspaceID, PathID: created.Path.ID, StepID: step.ID,
			ExpectedVersion: 1, Status: pathdomain.StepStatusInProgress,
			IdempotencyKey: "review-path-parity-step",
		}
		stepResult, err := service.UpdateStep(ctx, stepCommand)
		if err != nil || stepResult.Replayed || stepResult.Step.Status != pathdomain.StepStatusInProgress ||
			stepResult.Step.Version != 2 || stepResult.Path.Version != 2 {
			t.Fatalf("step result=%+v err=%v", stepResult, err)
		}
		stepReplay, err := service.UpdateStep(ctx, stepCommand)
		if err != nil || !stepReplay.Replayed || stepReplay.Step.ID != step.ID || stepReplay.Path.Version != 2 {
			t.Fatalf("step replay=%+v err=%v", stepReplay, err)
		}

		statusCommand := pathapp.UpdatePathStatusCommand{
			WorkspaceID: fixture.workspaceID, PathID: created.Path.ID,
			ExpectedVersion: 2, Status: pathdomain.StatusPaused,
			IdempotencyKey: "review-path-parity-status",
		}
		paused, err := service.UpdateStatus(ctx, statusCommand)
		if err != nil || paused.Replayed || paused.Path.Status != pathdomain.StatusPaused || paused.Path.Version != 3 {
			t.Fatalf("status result=%+v err=%v", paused, err)
		}
		statusReplay, err := service.UpdateStatus(ctx, statusCommand)
		if err != nil || !statusReplay.Replayed || statusReplay.Path.ID != created.Path.ID || statusReplay.Path.Version != 3 {
			t.Fatalf("status replay=%+v err=%v", statusReplay, err)
		}
	})
}

func TestReviewLearningPathPostgreSQLGORMConcurrentSameAndDifferentKeys(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		firstKey  string
		secondKey string
		sameKey   bool
	}{
		{name: "same key and hash", firstKey: "review-path-gorm-concurrent-same", secondKey: "review-path-gorm-concurrent-same", sameKey: true},
		{name: "different keys", firstKey: "review-path-gorm-concurrent-first", secondKey: "review-path-gorm-concurrent-second"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runReviewPathGORMIntegration(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, store pathapp.Store) {
				bridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
				firstService := newReviewPathService(t, store, bridge)
				secondService := newReviewPathService(t, store, bridge)
				start := make(chan struct{})
				outcomes := make(chan reviewPathCreateOutcome, 2)
				go func() {
					<-start
					runReviewPathCreate(ctx, outcomes, firstService, fixture, testCase.firstKey)
				}()
				go func() {
					<-start
					runReviewPathCreate(ctx, outcomes, secondService, fixture, testCase.secondKey)
				}()
				close(start)
				first := waitReviewPathCreate(t, ctx, outcomes)
				second := waitReviewPathCreate(t, ctx, outcomes)

				originals, replays, conflicts := 0, 0, 0
				for _, outcome := range []reviewPathCreateOutcome{first, second} {
					if outcome.err != nil {
						if reviewPathErrorCode(outcome.err) != pathdomain.ErrorCodeReservationPending {
							t.Fatalf("unexpected concurrent GORM Create error: %v", outcome.err)
						}
						conflicts++
						continue
					}
					if outcome.result.Replayed {
						replays++
					} else {
						originals++
					}
				}
				if originals != 1 {
					t.Fatalf("GORM originals=%d replays=%d conflicts=%d", originals, replays, conflicts)
				}
				if testCase.sameKey && (replays != 1 || conflicts != 0) {
					t.Fatalf("same-key GORM outcomes originals=%d replays=%d conflicts=%d", originals, replays, conflicts)
				}
				if !testCase.sameKey && replays+conflicts != 1 {
					t.Fatalf("different-key GORM outcomes originals=%d replays=%d conflicts=%d", originals, replays, conflicts)
				}
				closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
				assertReviewPathTerminalClosure(t, closure, fixture.answerID)
				expectedPathCommands := originals + replays
				if testCase.sameKey {
					expectedPathCommands = 1
				}
				if len(closure.paths) != 1 || len(closure.holds) != 0 || len(closure.pathCommands) != expectedPathCommands {
					t.Fatalf("concurrent GORM closure=%+v", closure)
				}
			})
		})
	}
}

func TestReviewLearningPathPostgreSQLGORMHistoryTriggerRollback(t *testing.T) {
	runReviewPathGORMIntegration(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, store pathapp.Store) {
		repository, ok := store.(*GORMRepository)
		if !ok {
			t.Fatalf("GORM variant returned %T", store)
		}
		bridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
		service := newReviewPathService(t, repository, bridge)
		created, err := service.CreateForReview(ctx, pathapp.CreateReviewCommand{
			WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
			IdempotencyKey: "review-path-gorm-history",
		})
		if err != nil || created.Replayed || len(created.Steps) == 0 {
			t.Fatalf("create GORM history fixture result=%+v err=%v", created, err)
		}

		_, err = repository.UpdateStep(ctx, pathapp.UpdateStepRecord{
			WorkspaceID: fixture.workspaceID, PathID: created.Path.ID, StepID: created.Steps[0].ID,
			ExpectedVersion: created.Path.Version, Status: pathdomain.StepStatusInProgress,
			IdempotencyKey: "review-path-gorm-history-step", RequestHash: reviewPathHash("review-path-gorm-history-step"),
			At: created.Path.UpdatedAt.Add(-time.Second),
		})
		var classified *foundation.Error
		if reviewPathErrorCode(err) != pathdomain.ErrorCodePersistenceInvalid || !errors.As(err, &classified) ||
			classified.Kind != foundation.ErrorConsistencyViolation || classified.Retryable {
			t.Fatalf("history trigger error=%v classified=%+v", err, classified)
		}

		closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
		assertReviewPathTerminalClosure(t, closure, fixture.answerID)
		if len(closure.paths) != 1 || closure.paths[0].version != 1 || len(closure.steps) != len(created.Steps) ||
			closure.steps[0].status != string(pathdomain.StepStatusPending) || closure.steps[0].version != 1 ||
			len(closure.pathCommands) != 1 {
			t.Fatalf("history trigger rollback closure=%+v", closure)
		}
	})
}

func TestReviewLearningPathPostgreSQLGORMCompleteResponseLossReplay(t *testing.T) {
	runReviewPathGORMIntegration(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, store pathapp.Store) {
		repository, ok := store.(*GORMRepository)
		if !ok {
			t.Fatalf("GORM variant returned %T", store)
		}
		commitLoss := errors.New("simulated GORM Review Path completion response loss")
		lossUnitOfWork := &reviewPathPostCommitErrorUnitOfWork{inner: repository.unitOfWork, err: commitLoss}
		lossRepository := *repository
		lossRepository.unitOfWork = lossUnitOfWork
		bridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
		entered := make(chan reviewPathCompleteCall, 1)
		release := make(chan struct{})
		lossService := newReviewPathService(t, &reviewPathBlockingCompleteStore{
			Store: &lossRepository, entered: entered, release: release,
		}, bridge)
		command := pathapp.CreateReviewCommand{
			WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
			IdempotencyKey: "review-path-gorm-response-loss",
		}
		outcomes := make(chan reviewPathCreateOutcome, 1)
		go runReviewPathCreate(ctx, outcomes, lossService, fixture, command.IdempotencyKey)
		waitReviewPathCompleteCall(t, ctx, entered)
		lossUnitOfWork.armed.Store(true)
		close(release)
		outcome := waitReviewPathCreate(t, ctx, outcomes)
		if reviewPathErrorCode(outcome.err) != pathdomain.ErrorCodeDependencyUnavailable || !errors.Is(outcome.err, commitLoss) ||
			!lossUnitOfWork.lost.Load() {
			t.Fatalf("GORM Complete response-loss result=%+v err=%v", outcome.result, outcome.err)
		}

		service := newReviewPathService(t, repository, bridge)
		replayed, err := service.CreateForReview(ctx, command)
		if err != nil || !replayed.Replayed {
			t.Fatalf("same-key GORM response-loss replay=%+v err=%v", replayed, err)
		}
		otherKey, err := service.CreateForReview(ctx, pathapp.CreateReviewCommand{
			WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
			IdempotencyKey: "review-path-gorm-response-loss-other-key",
		})
		if err != nil || !otherKey.Replayed || otherKey.Path.ID != replayed.Path.ID || otherKey.Path.Artifact != replayed.Path.Artifact {
			t.Fatalf("other-key GORM response-loss replay=%+v err=%v original=%+v", otherKey, err, replayed)
		}
		closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
		assertReviewPathTerminalClosure(t, closure, fixture.answerID)
		if len(closure.paths) != 1 || len(closure.artifacts) != 1 || len(closure.holds) != 0 || len(closure.pathCommands) != 2 {
			t.Fatalf("GORM response-loss closure=%+v", closure)
		}
	})
}

func TestReviewLearningPathPostgreSQLGORMCancellationAndUniqueReceiptRollback(t *testing.T) {
	runReviewPathGORMIntegration(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, store pathapp.Store) {
		repository, ok := store.(*GORMRepository)
		if !ok {
			t.Fatalf("GORM variant returned %T", store)
		}

		cancelled, cancel := context.WithCancelCause(ctx)
		cancelCause := errors.New("Review Path GORM cancellation cause")
		cancel(cancelCause)
		if _, err := repository.GetByReviewAnswer(cancelled, fixture.workspaceID, fixture.answerID); reviewPathErrorCode(err) != pathdomain.ErrorCodeDependencyUnavailable ||
			!errors.Is(err, context.Canceled) || !errors.Is(err, cancelCause) {
			t.Fatalf("GORM cancellation error=%v", err)
		}

		bridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
		service := newReviewPathService(t, repository, bridge)
		created, err := service.CreateForReview(ctx, pathapp.CreateReviewCommand{
			WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
			IdempotencyKey: "review-path-gorm-unique-receipt",
		})
		if err != nil || created.Replayed {
			t.Fatalf("create GORM unique receipt fixture result=%+v err=%v", created, err)
		}

		key := "review-path-gorm-duplicate-receipt"
		hash := reviewPathHash(key)
		err = repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			tx, transactionErr := platformpostgres.GORMTransaction(scope)
			if transactionErr != nil {
				return transactionErr
			}
			if insertErr := gormInsertResultReceipt(
				callbackCtx, tx, fixture.workspaceID, key, hash,
				pathapp.CommandTypePathStatus, created.Path.Version, created,
			); insertErr != nil {
				return insertErr
			}
			return gormInsertResultReceipt(
				callbackCtx, tx, fixture.workspaceID, key, hash,
				pathapp.CommandTypePathStatus, created.Path.Version, created,
			)
		})
		if reviewPathErrorCode(err) != pathdomain.ErrorCodeIdempotencyConflict {
			t.Fatalf("GORM duplicate receipt error=%v", err)
		}
		if _, found, lookupErr := repository.FindPathStatusReplay(ctx, fixture.workspaceID, key, hash, created.Path.Version); lookupErr != nil || found {
			t.Fatalf("duplicate receipt rollback found=%t err=%v", found, lookupErr)
		}
		closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
		assertReviewPathTerminalClosure(t, closure, fixture.answerID)
		if len(closure.pathCommands) != 1 || len(closure.holds) != 0 {
			t.Fatalf("duplicate receipt rollback closure=%+v", closure)
		}
	})
}

func TestReviewLearningPathPostgreSQLGORMTransactionLockBarrierAndDeadline(t *testing.T) {
	runReviewPathGORMIntegration(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, store pathapp.Store) {
		repository, ok := store.(*GORMRepository)
		if !ok {
			t.Fatalf("GORM variant returned %T", store)
		}
		if _, err := repository.Get(ctx, fixture.workspaceID, reviewPathIntegrationID(99)); reviewPathErrorCode(err) != pathdomain.ErrorCodePathNotFound {
			t.Fatalf("GORM no-row error=%v", err)
		}

		lockWorkspace := func() pgx.Tx {
			t.Helper()
			transaction, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var workspaceID string
			if err := transaction.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, string(fixture.workspaceID)).Scan(&workspaceID); err != nil {
				_ = transaction.Rollback(context.Background())
				t.Fatal(err)
			}
			return transaction
		}

		lockTransaction := lockWorkspace()
		defer func() { _ = lockTransaction.Rollback(context.Background()) }()
		type beginOutcome struct {
			reservation pathapp.Reservation
			err         error
		}
		outcomes := make(chan beginOutcome, 1)
		key := "review-path-gorm-lock-barrier"
		go func() {
			reservation, _, _, beginErr := repository.BeginReviewCreate(
				ctx, fixture.workspaceID, fixture.answerID, key, reviewPathHash(key),
			)
			outcomes <- beginOutcome{reservation: reservation, err: beginErr}
		}()
		waitReviewPathWorkspaceLock(t, ctx, pool)
		select {
		case outcome := <-outcomes:
			t.Fatalf("GORM Begin escaped Workspace lock early: %+v", outcome)
		default:
		}
		if err := lockTransaction.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var outcome beginOutcome
		select {
		case outcome = <-outcomes:
		case <-ctx.Done():
			t.Fatal("GORM Begin did not finish after Workspace lock release")
		}
		if outcome.err != nil || outcome.reservation.Status != pathapp.ReservationPending || outcome.reservation.AttemptNo != 1 {
			t.Fatalf("GORM Begin after Workspace lock result=%+v err=%v", outcome.reservation, outcome.err)
		}

		deadlineLock := lockWorkspace()
		deadlineCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		_, _, _, err := repository.BeginReviewCreate(
			deadlineCtx, fixture.workspaceID, fixture.answerID, "review-path-gorm-lock-deadline", reviewPathHash("review-path-gorm-lock-deadline"),
		)
		cancel()
		if reviewPathErrorCode(err) != pathdomain.ErrorCodeDependencyUnavailable || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GORM lock deadline error=%v", err)
		}
		if err := deadlineLock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		waitReviewPathNoWorkspaceLock(t, ctx, pool)
		reservation, _, _, err := repository.BeginReviewCreate(ctx, fixture.workspaceID, fixture.answerID, key, reviewPathHash(key))
		if err != nil || reservation.Status != pathapp.ReservationPending || reservation.AttemptNo != 1 {
			t.Fatalf("GORM reused Pool after deadline reservation=%+v err=%v", reservation, err)
		}
	})
}

func TestReviewLearningPathPostgreSQLGORMStatementBoundsForeignKeyAndPlans(t *testing.T) {
	runReviewPathGORMIntegration(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, store pathapp.Store) {
		repository, ok := store.(*GORMRepository)
		if !ok {
			t.Fatalf("GORM variant returned %T", store)
		}
		counter := installReviewPathStatementCounter(t, repository)

		var snapshot pathapp.ReviewSnapshot
		counter.reset()
		err := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
			var loadErr error
			snapshot, loadErr = gormLoadReviewSnapshot(callbackCtx, tx, fixture.workspaceID, fixture.answerID)
			return loadErr
		})
		if err != nil || len(snapshot.Gap.Citations) != 1 {
			t.Fatalf("GORM review snapshot=%+v err=%v", snapshot, err)
		}
		if statements := counter.statements(); statements != 3 {
			t.Fatalf("GORM review snapshot statements=%d, want 3", statements)
		}

		bridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
		service := newReviewPathService(t, repository, bridge)
		created, err := service.CreateForReview(ctx, pathapp.CreateReviewCommand{
			WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
			IdempotencyKey: "review-path-gorm-statement-bounds",
		})
		if err != nil || created.Replayed {
			t.Fatalf("create statement-bound fixture result=%+v err=%v", created, err)
		}
		counter.reset()
		read, err := repository.Get(ctx, fixture.workspaceID, created.Path.ID)
		if err != nil || read.Path.ID != created.Path.ID || len(read.Steps) != len(created.Steps) {
			t.Fatalf("GORM bounded read result=%+v err=%v", read, err)
		}
		if statements := counter.statements(); statements != 2 {
			t.Fatalf("GORM path read statements=%d, want 2", statements)
		}
		if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
			t.Fatalf("GORM path read retained %d PostgreSQL connections", acquired)
		}

		counter.reset()
		maintained, err := repository.MaintainReservations(ctx, time.Now().UTC().Add(-48*time.Hour), pathapp.MaxMaintenanceBatch)
		if err != nil || maintained != 0 {
			t.Fatalf("GORM maintenance count=%d err=%v", maintained, err)
		}
		if statements := counter.statements(); statements != 1 {
			t.Fatalf("GORM maintenance statements=%d, want 1", statements)
		}

		missingPathID := reviewPathIntegrationID(99)
		foreignKeyKey := "review-path-gorm-foreign-key"
		foreignKeyErr := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
			_, execErr := gormLearningPathExec(callbackCtx, tx, gormInsertResultReceiptSQL,
				string(fixture.workspaceID), foreignKeyKey, reviewPathHash(foreignKeyKey),
				pathapp.CommandTypePathStatus, string(missingPathID), 1, 1, learningPathJSONB([]byte(`{}`)))
			return gormLearningPathClassify(callbackCtx, execErr)
		})
		var classified *foundation.Error
		var postgresError *pgconn.PgError
		if reviewPathErrorCode(foreignKeyErr) != pathdomain.ErrorCodePersistenceInvalid ||
			!errors.As(foreignKeyErr, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Retryable ||
			!errors.As(foreignKeyErr, &postgresError) || postgresError.Code != "23503" {
			t.Fatalf("GORM foreign-key error=%v classified=%+v PostgreSQL=%+v", foreignKeyErr, classified, postgresError)
		}
		var receiptCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.learning_path_command
			WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.workspaceID), foreignKeyKey).Scan(&receiptCount); err != nil {
			t.Fatal(err)
		}
		if receiptCount != 0 {
			t.Fatalf("foreign-key failure retained %d receipts", receiptCount)
		}

		citation := snapshot.Gap.Citations[0]
		workspace := string(fixture.workspaceID)
		evidencePlan := explainReviewPathGORMQuery(t, ctx, repository, pool, gormReviewEvidenceSQL,
			pq.Array([]string{string(citation.ClaimID)}),
			pq.Array([]string{string(citation.SourceVersionID)}),
			pq.Array([]string{string(citation.SourceSpanID)}),
			pq.Array([]string{citation.EvidenceHash}),
			workspace, string(fixture.cardID), workspace, workspace, workspace, workspace, workspace, workspace, workspace)
		if evidencePlan.ActualRows != 1 || !reviewPathPlanContains(evidencePlan, func(node reviewPathExplainPlan) bool {
			return node.NodeType == "Function Scan" && node.ActualRows == 1
		}) {
			t.Fatalf("evidence EXPLAIN is not a bounded one-row set projection: %+v", evidencePlan)
		}

		const capacityRows = 5_000
		capacityAt := time.Now().UTC().Add(-25 * time.Hour)
		seedReviewPathMaintenancePlanRows(t, ctx, pool, fixture, capacityRows, capacityAt)
		transaction, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = transaction.Rollback(context.Background()) }()
		maintenancePlan := explainReviewPathGORMQuery(t, ctx, repository, transaction,
			gormMaintainReservationsSQL, time.Now().UTC().Add(-24*time.Hour), pathapp.MaxMaintenanceBatch)
		if err := transaction.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if !reviewPathPlanContains(maintenancePlan, func(node reviewPathExplainPlan) bool {
			return node.IndexName == "idx_learning_path_creation_pending_maintenance"
		}) {
			t.Fatalf("maintenance EXPLAIN did not use pending index: %+v", maintenancePlan)
		}
	})
}

func TestReviewLearningPathPostgreSQLConcurrentSameAndDifferentKeys(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		firstKey  string
		secondKey string
		sameKey   bool
	}{
		{name: "same key and hash", firstKey: "review-path-concurrent-same", secondKey: "review-path-concurrent-same", sameKey: true},
		{name: "different keys", firstKey: "review-path-concurrent-first", secondKey: "review-path-concurrent-second"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := newReviewPathTestDatabase(t, ctx)
			fixture := seedReviewPathFixture(t, ctx, pool)

			firstConnection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer firstConnection.Release()
			secondConnection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer secondConnection.Release()

			arrived := make(chan struct{}, 2)
			issuing := make(chan struct{})
			release := make(chan struct{})
			firstBarrier := &reviewPathSQLBarrier{
				match: "SELECT id::text FROM core.workspace", arrived: arrived,
				issuing: issuing, release: release,
			}
			secondBarrier := &reviewPathSQLBarrier{
				match: "SELECT id::text FROM core.workspace", arrived: arrived,
				issuing: issuing, release: release,
			}
			firstRepository, err := NewRepository(&reviewPathBarrierDB{DB: firstConnection, barrier: firstBarrier})
			if err != nil {
				t.Fatal(err)
			}
			secondRepository, err := NewRepository(&reviewPathBarrierDB{DB: secondConnection, barrier: secondBarrier})
			if err != nil {
				t.Fatal(err)
			}
			firstBridge, _ := newReviewPathProductionBridge(t, firstConnection, fixture.contentHash)
			secondBridge, _ := newReviewPathProductionBridge(t, secondConnection, fixture.contentHash)
			firstService := newReviewPathService(t, firstRepository, firstBridge)
			secondService := newReviewPathService(t, secondRepository, secondBridge)

			lockTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lockTx.Rollback(context.Background()) }()
			var lockedWorkspace string
			if err := lockTx.QueryRow(ctx, `SELECT id::text FROM core.workspace
				WHERE id=$1 FOR UPDATE`, string(fixture.workspaceID)).Scan(&lockedWorkspace); err != nil {
				t.Fatal(err)
			}

			outcomes := make(chan reviewPathCreateOutcome, 2)
			go runReviewPathCreate(ctx, outcomes, firstService, fixture, testCase.firstKey)
			go runReviewPathCreate(ctx, outcomes, secondService, fixture, testCase.secondKey)
			waitReviewPathSignal(t, ctx, arrived, "first create did not reach the Workspace row lock")
			waitReviewPathSignal(t, ctx, arrived, "second create did not reach the Workspace row lock")
			close(release)
			waitReviewPathSignal(t, ctx, issuing, "first create did not issue its locked query")
			waitReviewPathSignal(t, ctx, issuing, "second create did not issue its locked query")
			if err := lockTx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			first := waitReviewPathCreate(t, ctx, outcomes)
			second := waitReviewPathCreate(t, ctx, outcomes)

			originals, replays, conflicts := 0, 0, 0
			var completedPathID foundation.ID
			for _, outcome := range []reviewPathCreateOutcome{first, second} {
				if outcome.err != nil {
					if reviewPathErrorCode(outcome.err) != pathdomain.ErrorCodeReservationPending {
						t.Fatalf("unexpected concurrent Create error: %v", outcome.err)
					}
					conflicts++
					continue
				}
				if completedPathID != "" && outcome.result.Path.ID != completedPathID {
					t.Fatalf("concurrent results used different Paths: %s and %s", completedPathID, outcome.result.Path.ID)
				}
				completedPathID = outcome.result.Path.ID
				if outcome.result.Replayed {
					replays++
				} else {
					originals++
				}
			}
			if originals != 1 {
				t.Fatalf("originals=%d replays=%d conflicts=%d", originals, replays, conflicts)
			}
			if testCase.sameKey && (replays != 1 || conflicts != 0) {
				t.Fatalf("same-key outcomes originals=%d replays=%d conflicts=%d", originals, replays, conflicts)
			}
			if !testCase.sameKey && replays+conflicts != 1 {
				t.Fatalf("different-key outcomes originals=%d replays=%d conflicts=%d", originals, replays, conflicts)
			}

			closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
			assertReviewPathTerminalClosure(t, closure, fixture.answerID)
			expectedPathCommands := originals + replays
			if testCase.sameKey {
				expectedPathCommands = 1
			}
			if closure.reservation.status != string(pathapp.ReservationCompleted) ||
				len(closure.paths) != 1 || len(closure.artifacts) != 1 || len(closure.holds) != 0 ||
				len(closure.pathCommands) != expectedPathCommands {
				t.Fatalf("concurrent completion closure=%+v", closure)
			}
		})
	}
}

func TestReviewLearningPathPostgreSQLMaintenanceFencesLateHoldAndComplete(t *testing.T) {
	t.Run("maintenance wins before late Artifact hold", func(t *testing.T) {
		runReviewPathIntegrationVariants(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, repository pathapp.Store) {
			productionBridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
			entered := make(chan pathapp.DraftRequest, 1)
			release := make(chan struct{})
			service := newReviewPathService(t, repository, &reviewPathBlockingBridge{
				ArtifactBridge: productionBridge, entered: entered, release: release,
			})

			outcomeChannel := make(chan reviewPathCreateOutcome, 1)
			go runReviewPathCreate(ctx, outcomeChannel, service, fixture, "review-path-late-hold")
			request := waitReviewPathDraftRequest(t, ctx, entered)
			if request.ArtifactDigest == "" {
				t.Fatal("late Artifact request has no attempt digest")
			}
			staleAt := time.Now().UTC().Add(-25 * time.Hour)
			makeReviewPathReservationStale(t, ctx, pool, fixture, staleAt)
			abandoned, err := repository.MaintainReservations(ctx, time.Now().UTC().Add(-24*time.Hour), pathapp.MaxMaintenanceBatch)
			if err != nil || abandoned != 1 {
				t.Fatalf("maintenance abandoned=%d err=%v", abandoned, err)
			}
			close(release)
			outcome := waitReviewPathCreate(t, ctx, outcomeChannel)
			if outcome.err == nil {
				t.Fatal("late Artifact hold escaped an ABANDONED reservation")
			}

			closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
			assertReviewPathTerminalClosure(t, closure, fixture.answerID)
			if closure.reservation.status != string(pathapp.ReservationAbandoned) ||
				closure.reservation.artifactDigest != request.ArtifactDigest ||
				len(closure.artifacts) != 0 || len(closure.artifactCommands) != 0 || len(closure.holds) != 0 {
				t.Fatalf("late hold rollback closure=%+v", closure)
			}
		})
	})

	t.Run("maintenance wins before Complete", func(t *testing.T) {
		runReviewPathIntegrationVariants(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, repository pathapp.Store) {
			bridge, artifactRepository := newReviewPathProductionBridge(t, pool, fixture.contentHash)
			entered := make(chan reviewPathCompleteCall, 1)
			release := make(chan struct{})
			blockingStore := &reviewPathBlockingCompleteStore{
				Store: repository, entered: entered, release: release,
			}
			service := newReviewPathService(t, blockingStore, bridge)

			outcomeChannel := make(chan reviewPathCreateOutcome, 1)
			go runReviewPathCreate(ctx, outcomeChannel, service, fixture, "review-path-maintenance-wins")
			complete := waitReviewPathCompleteCall(t, ctx, entered)
			staleAt := time.Now().UTC().Add(-25 * time.Hour)
			makeReviewPathReservationStale(t, ctx, pool, fixture, staleAt)
			abandoned, err := repository.MaintainReservations(ctx, time.Now().UTC().Add(-24*time.Hour), pathapp.MaxMaintenanceBatch)
			if err != nil || abandoned != 1 {
				t.Fatalf("maintenance abandoned=%d err=%v", abandoned, err)
			}
			close(release)
			outcome := waitReviewPathCreate(t, ctx, outcomeChannel)
			if outcome.err == nil {
				t.Fatal("late Complete escaped an ABANDONED reservation")
			}

			closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
			assertReviewPathTerminalClosure(t, closure, fixture.answerID)
			if closure.reservation.status != string(pathapp.ReservationAbandoned) ||
				len(closure.artifacts) != 1 || len(closure.holds) != 1 ||
				closure.holds[0].artifactID != string(complete.path.Artifact.ArtifactID) {
				t.Fatalf("maintenance/Complete closure=%+v", closure)
			}
			if _, err := artifactRepository.Get(ctx, fixture.workspaceID, complete.path.Artifact.ArtifactID); err == nil {
				t.Fatal("ORPHANED Artifact became publicly readable")
			}
		})
	})

}

func TestReviewLearningPathPostgreSQLCompleteWinsBeforeMaintenance(t *testing.T) {
	runReviewPathIntegrationVariants(t, func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reviewPathFixture, repository pathapp.Store) {
		bridge, artifactRepository := newReviewPathProductionBridge(t, pool, fixture.contentHash)
		staleAt := time.Now().UTC().Add(-25 * time.Hour)
		completeEntered := make(chan reviewPathCompleteCall, 1)
		completeRelease := make(chan struct{})
		blockingStore := &reviewPathBlockingCompleteStore{
			Store: repository, entered: completeEntered, release: completeRelease,
			pathCreatedAtOverride: &staleAt,
		}
		service := newReviewPathService(t, blockingStore, bridge)

		outcomeChannel := make(chan reviewPathCreateOutcome, 1)
		go runReviewPathCreate(ctx, outcomeChannel, service, fixture, "review-path-complete-wins")
		waitReviewPathCompleteCall(t, ctx, completeEntered)
		makeReviewPathReservationStale(t, ctx, pool, fixture, staleAt)

		maintenanceArrived := make(chan struct{}, 1)
		maintenanceRelease := make(chan struct{})
		maintenanceRepository := reviewPathMaintenanceBarrierStore(t, ctx, pool, repository, &reviewPathSQLBarrier{
			match: "WITH candidates AS (", arrived: maintenanceArrived, release: maintenanceRelease,
		})
		type maintenanceOutcome struct {
			count int
			err   error
		}
		maintenanceOutcomes := make(chan maintenanceOutcome, 1)
		go func() {
			count, maintainErr := maintenanceRepository.MaintainReservations(
				ctx, time.Now().UTC().Add(-24*time.Hour), pathapp.MaxMaintenanceBatch,
			)
			maintenanceOutcomes <- maintenanceOutcome{count: count, err: maintainErr}
		}()
		waitReviewPathSignal(t, ctx, maintenanceArrived, "maintenance did not reach its locked candidate query")
		close(completeRelease)
		created := waitReviewPathCreate(t, ctx, outcomeChannel)
		if created.err != nil || created.result.Replayed {
			t.Fatalf("Complete winner result=%+v err=%v", created.result, created.err)
		}
		close(maintenanceRelease)
		var maintained maintenanceOutcome
		select {
		case maintained = <-maintenanceOutcomes:
		case <-ctx.Done():
			t.Fatal("maintenance did not finish after Complete")
		}
		if maintained.err != nil || maintained.count != 0 {
			t.Fatalf("maintenance after Complete count=%d err=%v", maintained.count, maintained.err)
		}

		closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
		assertReviewPathTerminalClosure(t, closure, fixture.answerID)
		if closure.reservation.status != string(pathapp.ReservationCompleted) || len(closure.holds) != 0 {
			t.Fatalf("Complete/maintenance closure=%+v", closure)
		}
		if _, err := artifactRepository.Get(ctx, fixture.workspaceID, created.result.Path.Artifact.ArtifactID); err != nil {
			t.Fatalf("completed Artifact is not publicly readable: %v", err)
		}
	})
}

func TestReviewLearningPathPostgreSQLCompleteResponseLossReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newReviewPathTestDatabase(t, ctx)
	fixture := seedReviewPathFixture(t, ctx, pool)
	lossDB := &reviewPathCommitLossDB{DB: pool}
	lossRepository, err := NewRepository(lossDB)
	if err != nil {
		t.Fatal(err)
	}
	bridge, _ := newReviewPathProductionBridge(t, pool, fixture.contentHash)
	lossService := newReviewPathService(t, lossRepository, bridge)
	command := pathapp.CreateReviewCommand{
		WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
		IdempotencyKey: "review-path-response-loss",
	}
	if _, err := lossService.CreateForReview(ctx, command); err == nil ||
		reviewPathErrorCode(err) != pathdomain.ErrorCodeDependencyUnavailable || !lossDB.lost.Load() {
		t.Fatalf("Complete response-loss error=%v", err)
	}

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := newReviewPathService(t, repository, bridge)
	replayed, err := service.CreateForReview(ctx, command)
	if err != nil || !replayed.Replayed {
		t.Fatalf("same-key response-loss replay=%+v err=%v", replayed, err)
	}
	otherKey, err := service.CreateForReview(ctx, pathapp.CreateReviewCommand{
		WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID,
		IdempotencyKey: "review-path-response-loss-other-key",
	})
	if err != nil || !otherKey.Replayed || otherKey.Path.ID != replayed.Path.ID ||
		otherKey.Path.Artifact != replayed.Path.Artifact {
		t.Fatalf("other-key completed replay=%+v err=%v original=%+v", otherKey, err, replayed)
	}

	closure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
	assertReviewPathTerminalClosure(t, closure, fixture.answerID)
	if closure.reservation.status != string(pathapp.ReservationCompleted) ||
		len(closure.paths) != 1 || len(closure.artifacts) != 1 || len(closure.holds) != 0 ||
		len(closure.pathCommands) != 2 {
		t.Fatalf("response-loss closure=%+v", closure)
	}
}

func TestReviewLearningPathPostgreSQLAbandonedReopenAndAttemptFence(t *testing.T) {
	runReviewPathIntegrationVariants(t, testReviewPathAbandonedReopenAndAttemptFence)
}

func testReviewPathAbandonedReopenAndAttemptFence(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture reviewPathFixture,
	repository pathapp.Store,
) {
	bridge, artifactRepository := newReviewPathProductionBridge(t, pool, fixture.contentHash)
	completeEntered := make(chan reviewPathCompleteCall, 1)
	completeRelease := make(chan struct{})
	blockingStore := &reviewPathBlockingCompleteStore{
		Store: repository, entered: completeEntered, release: completeRelease,
	}
	service := newReviewPathService(t, blockingStore, bridge)
	commandKey := "review-path-abandoned-reopen"

	firstOutcomeChannel := make(chan reviewPathCreateOutcome, 1)
	go runReviewPathCreate(ctx, firstOutcomeChannel, service, fixture, commandKey)
	oldAttempt := waitReviewPathCompleteCall(t, ctx, completeEntered)
	staleAt := time.Now().UTC().Add(-25 * time.Hour)
	makeReviewPathReservationStale(t, ctx, pool, fixture, staleAt)
	abandoned, err := repository.MaintainReservations(ctx, time.Now().UTC().Add(-24*time.Hour), pathapp.MaxMaintenanceBatch)
	if err != nil || abandoned != 1 {
		t.Fatalf("maintenance abandoned=%d err=%v", abandoned, err)
	}
	close(completeRelease)
	firstOutcome := waitReviewPathCreate(t, ctx, firstOutcomeChannel)
	if firstOutcome.err == nil {
		t.Fatal("old attempt completed after maintenance")
	}
	oldClosure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
	assertReviewPathTerminalClosure(t, oldClosure, fixture.answerID)
	if oldClosure.reservation.status != string(pathapp.ReservationAbandoned) ||
		oldClosure.reservation.attemptNo != oldAttempt.reservation.AttemptNo ||
		len(oldClosure.artifacts) != 1 || len(oldClosure.holds) != 1 {
		t.Fatalf("old attempt closure=%+v", oldClosure)
	}
	oldArtifactID := oldAttempt.path.Artifact.ArtifactID
	if _, found := findReviewPathArtifact(oldClosure, oldArtifactID); !found {
		t.Fatalf("old attempt Artifact %s is missing", oldArtifactID)
	}
	if _, err := artifactRepository.Get(ctx, fixture.workspaceID, oldArtifactID); err == nil {
		t.Fatal("old ORPHANED Artifact became publicly readable")
	}

	for _, rejected := range []struct {
		name, key, hash string
	}{
		{name: "different key", key: commandKey + "-different", hash: oldAttempt.reservation.RequestHash},
		{name: "same key different hash", key: commandKey, hash: reviewPathHash("different-request")},
	} {
		t.Run(rejected.name, func(t *testing.T) {
			if _, _, _, err := repository.BeginReviewCreate(
				ctx, fixture.workspaceID, fixture.answerID, rejected.key, rejected.hash,
			); reviewPathErrorCode(err) != pathdomain.ErrorCodeReservationPending {
				t.Fatalf("rejected identity error=%v", err)
			}
		})
	}

	reopenService := newReviewPathService(t, repository, bridge)
	reopened, err := reopenService.CreateForReview(ctx, pathapp.CreateReviewCommand{
		WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID, IdempotencyKey: commandKey,
	})
	if err != nil || reopened.Replayed {
		t.Fatalf("reopened result=%+v err=%v", reopened, err)
	}
	newClosure := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
	assertReviewPathTerminalClosure(t, newClosure, fixture.answerID)
	if newClosure.reservation.status != string(pathapp.ReservationCompleted) ||
		newClosure.reservation.attemptNo != oldClosure.reservation.attemptNo+1 ||
		newClosure.reservation.artifactDigest == oldClosure.reservation.artifactDigest ||
		newClosure.reservation.artifactID == string(oldArtifactID) ||
		len(newClosure.artifacts) != 2 || len(newClosure.holds) != 1 ||
		newClosure.holds[0].artifactID != string(oldArtifactID) ||
		newClosure.holds[0].digest != oldClosure.reservation.artifactDigest {
		t.Fatalf("reopened closure old=%+v new=%+v", oldClosure, newClosure)
	}
	if _, err := artifactRepository.Get(ctx, fixture.workspaceID, reopened.Path.Artifact.ArtifactID); err != nil {
		t.Fatalf("new completed Artifact is not publicly readable: %v", err)
	}

	if _, _, err := repository.PrepareReviewCreate(
		ctx, oldAttempt.reservation, oldAttempt.reservation.ArtifactDigest,
	); reviewPathErrorCode(err) != pathdomain.ErrorCodeIdempotencyConflict {
		t.Fatalf("old attempt Prepare error=%v", err)
	}
	if _, err := repository.CompleteReviewCreate(
		ctx, oldAttempt.reservation, oldAttempt.path, oldAttempt.steps,
	); reviewPathErrorCode(err) != pathdomain.ErrorCodeIdempotencyConflict {
		t.Fatalf("old attempt Complete error=%v", err)
	}
	afterFence := loadReviewPathClosure(t, ctx, pool, fixture.workspaceID, fixture.answerID)
	assertReviewPathTerminalClosure(t, afterFence, fixture.answerID)
	if afterFence.reservation.attemptNo != newClosure.reservation.attemptNo ||
		afterFence.reservation.artifactDigest != newClosure.reservation.artifactDigest ||
		len(afterFence.paths) != 1 || len(afterFence.artifacts) != 2 || len(afterFence.holds) != 1 {
		t.Fatalf("old attempt fence changed terminal closure: before=%+v after=%+v", newClosure, afterFence)
	}
}

type reviewPathCreateOutcome struct {
	result pathapp.Result
	err    error
}

func runReviewPathCreate(
	ctx context.Context,
	outcomes chan<- reviewPathCreateOutcome,
	service *pathapp.Service,
	fixture reviewPathFixture,
	key string,
) {
	result, err := service.CreateForReview(ctx, pathapp.CreateReviewCommand{
		WorkspaceID: fixture.workspaceID, ReviewAnswerID: fixture.answerID, IdempotencyKey: key,
	})
	select {
	case outcomes <- reviewPathCreateOutcome{result: result, err: err}:
	case <-ctx.Done():
	}
}

func waitReviewPathCreate(t *testing.T, ctx context.Context, outcomes <-chan reviewPathCreateOutcome) reviewPathCreateOutcome {
	t.Helper()
	select {
	case outcome := <-outcomes:
		return outcome
	case <-ctx.Done():
		t.Fatal("Review Path Create did not finish")
		return reviewPathCreateOutcome{}
	}
}

func waitReviewPathSignal(t *testing.T, ctx context.Context, signals <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signals:
	case <-ctx.Done():
		t.Fatal(message)
	}
}

func waitReviewPathWorkspaceLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1
			FROM pg_stat_activity
			WHERE datname=current_database()
			  AND wait_event_type='Lock'
			  AND query LIKE $1
		)`, "%core.workspace%").Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("GORM transaction did not become visible waiting on the Workspace row lock")
		case <-ctx.Done():
			t.Fatal("GORM transaction context ended before reaching the Workspace row lock")
		}
	}
}

func waitReviewPathNoWorkspaceLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1
			FROM pg_stat_activity
			WHERE datname=current_database()
			  AND wait_event_type='Lock'
			  AND query LIKE $1
		)`, "%core.workspace%").Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if !waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("GORM transaction still waits on the Workspace row lock after cancellation")
		case <-ctx.Done():
			t.Fatal("GORM transaction context ended while checking Workspace lock cleanup")
		}
	}
}

type reviewPathStatementCounter struct {
	count atomic.Int64
}

func (counter *reviewPathStatementCounter) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return counter
}

func (*reviewPathStatementCounter) Info(context.Context, string, ...any)  {}
func (*reviewPathStatementCounter) Warn(context.Context, string, ...any)  {}
func (*reviewPathStatementCounter) Error(context.Context, string, ...any) {}

func (counter *reviewPathStatementCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.count.Add(1)
}

func (counter *reviewPathStatementCounter) reset() {
	counter.count.Store(0)
}

func (counter *reviewPathStatementCounter) statements() int64 {
	return counter.count.Load()
}

func installReviewPathStatementCounter(t *testing.T, repository *GORMRepository) *reviewPathStatementCounter {
	t.Helper()
	if repository == nil || repository.database == nil || repository.database.Config == nil {
		t.Fatal("GORM repository has no shared database config")
	}
	counter := &reviewPathStatementCounter{}
	original := repository.database.Config.Logger
	repository.database.Config.Logger = counter
	t.Cleanup(func() {
		repository.database.Config.Logger = original
	})
	return counter
}

type reviewPathExplainQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type reviewPathExplainPlan struct {
	NodeType     string                  `json:"Node Type"`
	RelationName string                  `json:"Relation Name"`
	IndexName    string                  `json:"Index Name"`
	ActualRows   float64                 `json:"Actual Rows"`
	Plans        []reviewPathExplainPlan `json:"Plans"`
}

func explainReviewPathGORMQuery(
	t *testing.T,
	ctx context.Context,
	repository *GORMRepository,
	database reviewPathExplainQuerier,
	query string,
	arguments ...any,
) reviewPathExplainPlan {
	t.Helper()
	statement := repository.database.Session(&gorm.Session{DryRun: true}).Raw(query, arguments...)
	if statement.Error != nil || statement.Statement == nil || statement.Statement.SQL.Len() == 0 {
		t.Fatalf("build Review Path EXPLAIN statement: %v", statement.Error)
	}
	var raw []byte
	if err := database.QueryRow(ctx,
		"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) "+statement.Statement.SQL.String(),
		statement.Statement.Vars...,
	).Scan(&raw); err != nil {
		t.Fatalf("execute Review Path EXPLAIN: %v", err)
	}
	var documents []struct {
		Plan reviewPathExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("decode Review Path EXPLAIN: documents=%d err=%v", len(documents), err)
	}
	return documents[0].Plan
}

func reviewPathPlanContains(plan reviewPathExplainPlan, predicate func(reviewPathExplainPlan) bool) bool {
	if predicate(plan) {
		return true
	}
	for _, child := range plan.Plans {
		if reviewPathPlanContains(child, predicate) {
			return true
		}
	}
	return false
}

func seedReviewPathMaintenancePlanRows(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture reviewPathFixture,
	count int,
	updatedAt time.Time,
) {
	t.Helper()
	tag, err := pool.Exec(ctx, `WITH source_answer AS (
		SELECT workspace_id,session_id,card_id,user_answer,scorer_version,score,feedback,
			rating,schedule_snapshot,request_hash,created_at
		FROM learning.review_answer
		WHERE workspace_id=$1 AND id=$2
	), inserted_answers AS (
		INSERT INTO learning.review_answer(
			id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,
			scorer_version,score,feedback,rating,schedule_snapshot,request_hash,created_at
		)
		SELECT md5('review-path-plan-answer-' || series.value::text)::uuid,
			source_answer.workspace_id,source_answer.session_id,source_answer.card_id,
			'review-path-plan-question-' || series.value::text,
			'review-path-plan-answer-' || series.value::text,
			source_answer.user_answer,source_answer.scorer_version,source_answer.score,
			source_answer.feedback,source_answer.rating,source_answer.schedule_snapshot,
			source_answer.request_hash,source_answer.created_at
		FROM source_answer
		CROSS JOIN generate_series(1,$3) AS series(value)
		RETURNING id,workspace_id,idempotency_key
	)
	INSERT INTO learning.learning_path_creation_reservation(
		workspace_id,review_answer_id,idempotency_key,request_hash,source_snapshot,
		source_snapshot_digest,attempt_no,status,created_at,updated_at
	)
	SELECT workspace_id,id,idempotency_key,repeat('a',64),'{}'::jsonb,
		repeat('b',64),1,'PENDING',$4,$4
	FROM inserted_answers`, string(fixture.workspaceID), string(fixture.answerID), count, updatedAt.UTC())
	if err != nil {
		t.Fatalf("seed Review Path maintenance plan rows: %v", err)
	}
	if tag.RowsAffected() != int64(count) {
		t.Fatalf("seeded maintenance rows=%d, want %d", tag.RowsAffected(), count)
	}
	if _, err := pool.Exec(ctx, `ANALYZE learning.learning_path_creation_reservation`); err != nil {
		t.Fatalf("analyze Review Path maintenance fixture: %v", err)
	}
}

func reviewPathMaintenanceBarrierStore(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store pathapp.Store,
	barrier *reviewPathSQLBarrier,
) pathapp.Store {
	t.Helper()
	switch repository := store.(type) {
	case *Repository:
		connection, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(connection.Release)
		wrapped, err := NewRepository(&reviewPathBarrierDB{DB: connection, barrier: barrier})
		if err != nil {
			t.Fatal(err)
		}
		return wrapped
	case *GORMRepository:
		const callbackName = "review_path:maintenance_barrier"
		rowCallbacks := repository.database.Callback().Row()
		if err := rowCallbacks.Before("gorm:row").Register(callbackName, func(database *gorm.DB) {
			if database != nil && database.Statement != nil {
				barrier.wait(database.Statement.Context, database.Statement.SQL.String())
			}
		}); err != nil {
			t.Fatalf("register GORM maintenance barrier: %v", err)
		}
		t.Cleanup(func() {
			if err := rowCallbacks.Remove(callbackName); err != nil {
				t.Errorf("remove GORM maintenance barrier: %v", err)
			}
		})
		return repository
	default:
		t.Fatalf("unsupported Review Path store %T", store)
		return nil
	}
}

type reviewPathSQLBarrier struct {
	match   string
	arrived chan<- struct{}
	issuing chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (barrier *reviewPathSQLBarrier) wait(ctx context.Context, query string) {
	if barrier == nil || !strings.Contains(query, barrier.match) {
		return
	}
	barrier.once.Do(func() {
		select {
		case barrier.arrived <- struct{}{}:
		case <-ctx.Done():
			return
		}
		select {
		case <-barrier.release:
		case <-ctx.Done():
			return
		}
		if barrier.issuing != nil {
			select {
			case barrier.issuing <- struct{}{}:
			case <-ctx.Done():
			}
		}
	})
}

type reviewPathBarrierDB struct {
	DB
	barrier *reviewPathSQLBarrier
}

func (database *reviewPathBarrierDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &reviewPathBarrierTx{Tx: tx, barrier: database.barrier}, nil
}

type reviewPathBarrierTx struct {
	pgx.Tx
	barrier *reviewPathSQLBarrier
}

func (tx *reviewPathBarrierTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.barrier.wait(ctx, query)
	return tx.Tx.QueryRow(ctx, query, args...)
}

type reviewPathBlockingBridge struct {
	pathapp.ArtifactBridge
	entered chan<- pathapp.DraftRequest
	release <-chan struct{}
}

func (bridge *reviewPathBlockingBridge) CreateDraft(ctx context.Context, request pathapp.DraftRequest) (pathdomain.ArtifactBinding, error) {
	select {
	case bridge.entered <- request:
	case <-ctx.Done():
		return pathdomain.ArtifactBinding{}, ctx.Err()
	}
	select {
	case <-bridge.release:
		return bridge.ArtifactBridge.CreateDraft(ctx, request)
	case <-ctx.Done():
		return pathdomain.ArtifactBinding{}, ctx.Err()
	}
}

func waitReviewPathDraftRequest(t *testing.T, ctx context.Context, entered <-chan pathapp.DraftRequest) pathapp.DraftRequest {
	t.Helper()
	select {
	case request := <-entered:
		return request
	case <-ctx.Done():
		t.Fatal("Create did not reach the Artifact bridge")
		return pathapp.DraftRequest{}
	}
}

type reviewPathCompleteCall struct {
	reservation pathapp.Reservation
	path        pathdomain.Path
	steps       []pathdomain.Step
}

type reviewPathBlockingCompleteStore struct {
	pathapp.Store
	entered               chan<- reviewPathCompleteCall
	release               <-chan struct{}
	pathCreatedAtOverride *time.Time
}

func (store *reviewPathBlockingCompleteStore) CompleteReviewCreate(
	ctx context.Context,
	reservation pathapp.Reservation,
	path pathdomain.Path,
	steps []pathdomain.Step,
) (pathapp.Result, error) {
	call := reviewPathCompleteCall{
		reservation: reservation, path: path, steps: append([]pathdomain.Step(nil), steps...),
	}
	select {
	case store.entered <- call:
	case <-ctx.Done():
		return pathapp.Result{}, ctx.Err()
	}
	select {
	case <-store.release:
		if store.pathCreatedAtOverride != nil {
			path.CreatedAt = store.pathCreatedAtOverride.UTC()
		}
		return store.Store.CompleteReviewCreate(ctx, reservation, path, steps)
	case <-ctx.Done():
		return pathapp.Result{}, ctx.Err()
	}
}

func waitReviewPathCompleteCall(t *testing.T, ctx context.Context, entered <-chan reviewPathCompleteCall) reviewPathCompleteCall {
	t.Helper()
	select {
	case call := <-entered:
		return call
	case <-ctx.Done():
		t.Fatal("Create did not reach Complete")
		return reviewPathCompleteCall{}
	}
}

func makeReviewPathReservationStale(
	t *testing.T,
	ctx context.Context,
	db interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	},
	fixture reviewPathFixture,
	staleAt time.Time,
) {
	t.Helper()
	tag, err := db.Exec(ctx, `UPDATE learning.learning_path_creation_reservation
		SET created_at=$3,prepared_at=$3,updated_at=$3
		WHERE workspace_id=$1 AND review_answer_id=$2
		  AND status='PENDING' AND artifact_digest IS NOT NULL`,
		string(fixture.workspaceID), string(fixture.answerID), staleAt.UTC())
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("stale reservation update affected %d rows", tag.RowsAffected())
	}
}

// reviewPathPostCommitErrorUnitOfWork preserves the real shared Pool/UoW and
// returns a controlled error only after its inner transaction committed.
type reviewPathPostCommitErrorUnitOfWork struct {
	inner foundation.UnitOfWork
	err   error
	armed atomic.Bool
	lost  atomic.Bool
}

func (unitOfWork *reviewPathPostCommitErrorUnitOfWork) Within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work foundation.TransactionFunc,
) error {
	if err := unitOfWork.inner.Within(ctx, options, work); err != nil {
		return err
	}
	if unitOfWork.armed.Load() && unitOfWork.lost.CompareAndSwap(false, true) {
		return unitOfWork.err
	}
	return nil
}

type reviewPathCommitLossDB struct {
	DB
	lost atomic.Bool
}

func (database *reviewPathCommitLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &reviewPathCommitLossTx{Tx: tx, database: database}, nil
}

type reviewPathCommitLossTx struct {
	pgx.Tx
	database *reviewPathCommitLossDB
	terminal bool
}

func (tx *reviewPathCommitLossTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, query, args...)
	if err == nil && strings.Contains(query, "SET status='COMPLETED'") {
		tx.terminal = true
	}
	return tag, err
}

func (tx *reviewPathCommitLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	if tx.terminal && tx.database.lost.CompareAndSwap(false, true) {
		return errors.New("simulated Review Path completion response loss")
	}
	return nil
}

func reviewPathErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
