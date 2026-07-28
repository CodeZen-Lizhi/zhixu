//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool := newReviewPathTestDatabase(t, ctx)
		fixture := seedReviewPathFixture(t, ctx, pool)
		repository, err := NewRepository(pool)
		if err != nil {
			t.Fatal(err)
		}
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

	t.Run("maintenance wins before Complete", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool := newReviewPathTestDatabase(t, ctx)
		fixture := seedReviewPathFixture(t, ctx, pool)
		repository, err := NewRepository(pool)
		if err != nil {
			t.Fatal(err)
		}
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

	t.Run("Complete wins before maintenance", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool := newReviewPathTestDatabase(t, ctx)
		fixture := seedReviewPathFixture(t, ctx, pool)
		repository, err := NewRepository(pool)
		if err != nil {
			t.Fatal(err)
		}
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

		maintenanceConnection, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer maintenanceConnection.Release()
		maintenanceArrived := make(chan struct{}, 1)
		maintenanceRelease := make(chan struct{})
		maintenanceRepository, err := NewRepository(&reviewPathBarrierDB{
			DB: maintenanceConnection,
			barrier: &reviewPathSQLBarrier{
				match: "WITH candidates AS (", arrived: maintenanceArrived, release: maintenanceRelease,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newReviewPathTestDatabase(t, ctx)
	fixture := seedReviewPathFixture(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
