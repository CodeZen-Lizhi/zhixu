//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCompleteReindexTxAtomicallyActivatesAndReplaysAfterResponseLoss(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 700)
	satisfyCompletionGates(t, ctx, database.DB(), fixture)

	lossRepository, err := NewRepository(&completionResponseLossDB{pool: database.DB(), lose: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lossRepository.CompleteReindexTx(ctx, fixture.Command); completionErrorCode(err) != "REINDEX_COMPLETION_COMMIT_FAILED" {
		var classified *foundation.Error
		_ = errors.As(err, &classified)
		t.Fatalf("response loss error=%v classified=%#v cause=%v", err, classified, classified.Cause)
	}
	assertCompletionCommitted(t, ctx, database.DB(), fixture)

	replayCommand := fixture.Command
	replayCommand.ActivationID = snapshotID(799_999)
	replayed, err := repository.CompleteReindexTx(ctx, replayCommand)
	if err != nil || !replayed.Replayed || replayed.Activation.ID != fixture.Command.ActivationID ||
		replayed.ActiveIndexVersion.ID != fixture.TargetIndexID || replayed.PreviousIndexVersion == nil ||
		replayed.PreviousIndexVersion.ID != fixture.OldActiveID {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	assertCompletionCommitted(t, ctx, database.DB(), fixture)

	chunk := loadCompletionManifestChunk(t, ctx, database.DB(), fixture.TargetIndexID)
	currentTarget, err := repository.GetIndex(ctx, fixture.WorkspaceID, fixture.TargetIndexID)
	if err != nil {
		t.Fatal(err)
	}
	laterBase := time.Now().UTC()
	laterReady := createReadyIndex(t, ctx, repository, fixture.WorkspaceID, chunk, string(snapshotID(799_990)),
		"completion-later-"+string(fixture.DeliveryID), laterBase)
	if _, err := repository.Activate(ctx, domain.ActivationCommand{
		ActivationID: snapshotID(799_991), WorkspaceID: fixture.WorkspaceID,
		TargetIndexVersionID: laterReady.ID, ExpectedTargetVersion: laterReady.Version,
		ExpectedCurrentIndexVersionID: &currentTarget.ID, ExpectedCurrentVersion: &currentTarget.Version,
		IdempotencyKey: "completion-later-activate-" + string(fixture.DeliveryID),
		ReasonCode:     "BUILD_VERIFIED", At: laterBase.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	historicalReplay, err := repository.CompleteReindexTx(ctx, replayCommand)
	if err != nil || !historicalReplay.Replayed || historicalReplay.ActiveIndexVersion.ID != fixture.TargetIndexID ||
		historicalReplay.ActiveIndexVersion.Status != domain.IndexStatusActive {
		t.Fatalf("historical replay=%#v err=%v", historicalReplay, err)
	}
	actualTarget, err := repository.GetIndex(ctx, fixture.WorkspaceID, fixture.TargetIndexID)
	if err != nil || actualTarget.Status != domain.IndexStatusRetiring {
		t.Fatalf("actual historical target=%#v err=%v", actualTarget, err)
	}
}

func TestCompleteReindexTxRequiresCleanupAndSucceededWorkflowWithoutMutatingIndexes(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 710)
	if _, err := repository.CompleteReindexTx(ctx, fixture.Command); !completionPrerequisitePendingError(err) {
		t.Fatalf("gate error=%v", err)
	}
	assertCompletionRolledBack(t, ctx, database.DB(), fixture)

	if _, err := database.DB().Exec(ctx, `UPDATE change_control.writeback_execution SET
		cleanup_completed_at=CURRENT_TIMESTAMP,version=version+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND status='verifying'`, string(fixture.ExecutionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CompleteReindexTx(ctx, fixture.Command); !completionPrerequisitePendingError(err) {
		t.Fatalf("workflow gate error=%v", err)
	}
	assertCompletionRolledBack(t, ctx, database.DB(), fixture)
}

func TestCompleteReindexTxRequiresLatestFullFenceAndLiveDatabaseLease(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 715)
	satisfyCompletionGates(t, ctx, database.DB(), fixture)
	var unrelatedActivationID foundation.ID
	if err := database.QueryRow(ctx, `SELECT id::text FROM retrieval.index_activation
		WHERE workspace_id=$1 AND target_index_version_id=$2`, string(fixture.WorkspaceID), string(fixture.OldActiveID)).Scan(&unrelatedActivationID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery SET activation_id=$2,
		version=version+1,updated_at=clock_timestamp() WHERE id=$1 AND status='processing'`, string(fixture.DeliveryID), string(unrelatedActivationID)); err == nil {
		t.Fatal("database must reject activation evidence on a non-succeeded delivery")
	}

	stale := fixture.Command
	stale.Fence.Owner = "another-worker"
	if _, err := repository.CompleteReindexTx(ctx, stale); completionErrorCode(err) != "REINDEX_COMPLETION_FENCE_STALE" {
		t.Fatalf("stale fence error=%v", err)
	}
	assertCompletionRolledBack(t, ctx, database.DB(), fixture)

	withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt
			SET lease_until=heartbeat_at WHERE id=$1`, string(fixture.AttemptID)); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := repository.CompleteReindexTx(ctx, fixture.Command); completionErrorCode(err) != "REINDEX_LEASE_EXPIRED" {
		t.Fatalf("expired lease error=%v", err)
	}
	assertCompletionRolledBack(t, ctx, database.DB(), fixture)
}

func TestCompleteReindexTxRechecksLeaseAfterWorkspaceLockWait(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 716)
	satisfyCompletionGates(t, ctx, database.DB(), fixture)
	withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt
			SET lease_until=clock_timestamp() + interval '150 milliseconds' WHERE id=$1`, string(fixture.AttemptID)); err != nil {
			t.Fatal(err)
		}
	})
	lockTx, err := database.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(fixture.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, completeErr := repository.CompleteReindexTx(ctx, fixture.Command)
		result <- completeErr
	}()
	time.Sleep(250 * time.Millisecond)
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; completionErrorCode(err) != "REINDEX_LEASE_EXPIRED" {
		t.Fatalf("completion after lock wait error=%v", err)
	}
	assertCompletionRolledBack(t, ctx, database.DB(), fixture)
}

func TestCompleteReindexTxRechecksLeaseAtFinalMutation(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	baseRepository, err := NewRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedCompletionFixture(t, ctx, baseRepository, database.DB(), 717)
	satisfyCompletionGates(t, ctx, database.DB(), fixture)
	withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt
			SET lease_until=clock_timestamp() + interval '150 milliseconds' WHERE id=$1`, string(fixture.AttemptID)); err != nil {
			t.Fatal(err)
		}
	})
	delayedRepository, err := NewRepository(&completionDelayDB{
		pool: database.DB(), match: "UPDATE retrieval.reindex_delivery_attempt", delay: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delayedRepository.CompleteReindexTx(ctx, fixture.Command); completionErrorCode(err) != "REINDEX_LEASE_EXPIRED" {
		t.Fatalf("completion after mutation delay error=%v", err)
	}
	assertCompletionRolledBack(t, ctx, database.DB(), fixture)
}

func TestCompleteReindexTxFaultInjectionRollsBackEveryMutationStage(t *testing.T) {
	stages := []struct {
		name  string
		match string
	}{
		{"activation receipt", "INSERT INTO retrieval.index_activation"},
		{"previous retire", "UPDATE retrieval.index_version SET status='retiring'"},
		{"target activate", "UPDATE retrieval.index_version SET status='active'"},
		{"attempt succeeded", "UPDATE retrieval.reindex_delivery_attempt"},
		{"delivery succeeded", "UPDATE retrieval.reindex_delivery SET"},
		{"execution completed", "UPDATE change_control.writeback_execution SET"},
		{"proposal completed", "UPDATE change_control.proposal SET"},
	}
	for index, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestRepository(t)
			fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 720+index)
			satisfyCompletionGates(t, ctx, database.DB(), fixture)
			faultRepository, err := NewRepository(&completionFaultDB{pool: database.DB(), match: stage.match})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := faultRepository.CompleteReindexTx(ctx, fixture.Command); err == nil {
				t.Fatal("fault injection must fail")
			}
			assertCompletionRolledBack(t, ctx, database.DB(), fixture)
		})
	}
}

type completionFixture struct {
	Command       domain.CompleteReindexCommand
	WorkspaceID   foundation.ID
	ExecutionID   foundation.ID
	ProposalID    foundation.ID
	RunID         foundation.ID
	NodeID        foundation.ID
	DeliveryID    foundation.ID
	AttemptID     foundation.ID
	TargetIndexID foundation.ID
	OldActiveID   foundation.ID
}

func loadCompletionManifestChunk(t *testing.T, ctx context.Context, database *pgxpool.Pool, indexID foundation.ID) domain.ManifestChunk {
	t.Helper()
	var chunk domain.ManifestChunk
	if err := database.QueryRow(ctx, `SELECT chunk_id::text,workspace_id::text,content_hash,sequence,
		parser_version,chunk_strategy_version,schema_version,created_at
		FROM retrieval.index_manifest_chunk WHERE index_version_id=$1 ORDER BY sequence,chunk_id LIMIT 1`, string(indexID)).Scan(
		&chunk.ChunkID, &chunk.WorkspaceID, &chunk.ContentHash, &chunk.Sequence,
		&chunk.ParserVersion, &chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.CreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	return chunk
}

func seedCompletionFixture(t *testing.T, ctx context.Context, repository *Repository, database *pgxpool.Pool, ordinal int) completionFixture {
	t.Helper()
	writeback := seedDispatcherWriteback(t, ctx, database, "", "complete-"+string(rune(ordinal)))
	var runID, nodeID, proposalID foundation.ID
	var resultHash string
	if err := database.QueryRow(ctx, `SELECT workflow_run_id::text,node_run_id::text,proposal_id::text,result_hash
		FROM change_control.writeback_execution WHERE id=$1`, string(writeback.ExecutionID)).Scan(&runID, &nodeID, &proposalID, &resultHash); err != nil {
		t.Fatal(err)
	}
	withReplicaRole(t, ctx, database, func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE change_control.proposal SET workflow_run_id=$2 WHERE id=$1`, string(proposalID), string(runID)); err != nil {
			t.Fatal(err)
		}
	})
	dispatcher, err := NewDispatcher(database, foundation.NewUUIDGenerator(nil), &dispatcherInserterFake{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var deliveryID foundation.ID
	if err := database.QueryRow(ctx, `SELECT id::text FROM retrieval.reindex_delivery WHERE outbox_event_id=$1`, string(writeback.EventID)).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	deliveryRepository, err := NewDeliveryRepository(database, &deliveryRuntimeIDs{values: []foundation.ID{snapshotID(ordinal + 100_000)}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := application.NewDeliveryRuntime(deliveryRepository)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := runtime.Claim(ctx, application.DeliveryClaimCommand{
		DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: int64(ordinal + 1), RiverAttempt: 1,
		DeliveryKey: "completion-delivery-" + string(deliveryID), LeaseOwner: "completion-worker", LeaseDuration: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Add(-20 * time.Minute)
	target := seedSnapshotSourceVersion(t, ctx, database, writeback.WorkspaceID, ordinal+200_000, 1, true, now, now, 2)
	withReplicaRole(t, ctx, database, func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE core.content_artifact SET content_hash=$2,managed_location='.knowledge/sources/' || $2 WHERE id=$1`, string(target.ArtifactID), resultHash); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, `UPDATE core.source_version SET content_hash=$2 WHERE id=$1`, string(target.VersionID), resultHash); err != nil {
			t.Fatal(err)
		}
	})
	target.ContentHash = resultHash
	chunk := snapshotManifestChunk(t, ctx, database, target)
	oldReady := createReadyIndex(t, ctx, repository, writeback.WorkspaceID, chunk, string(snapshotID(ordinal+300_000)), "completion-old-"+string(deliveryID), now.Add(time.Minute))
	oldActivated, err := repository.Activate(ctx, domain.ActivationCommand{
		ActivationID: snapshotID(ordinal + 300_001), WorkspaceID: writeback.WorkspaceID,
		TargetIndexVersionID: oldReady.ID, ExpectedTargetVersion: oldReady.Version,
		IdempotencyKey: "completion-old-activate-" + string(deliveryID), ReasonCode: "BUILD_VERIFIED", At: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := snapshotCommand(writeback.WorkspaceID, ordinal+400_000, target, "completion-target-"+string(deliveryID), now.Add(5*time.Minute))
	created, err := repository.BeginWorkspaceSnapshot(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: writeback.WorkspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: created.IndexVersion.Version, At: now.Add(6 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	var ingestionAttemptID foundation.ID
	if err := database.QueryRow(ctx, `SELECT id::text FROM ingestion.attempt WHERE source_version_id=$1 AND status='chunked'`, string(target.VersionID)).Scan(&ingestionAttemptID); err != nil {
		t.Fatal(err)
	}
	sourceCheckpoint, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: claimed.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: target.VersionID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ingestedCheckpoint, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: sourceCheckpoint.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointIngested, SourceVersionID: target.VersionID,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: target.ProjectionID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	excluded := created.ExcludedSourceCount
	indexCheckpoint, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: ingestedCheckpoint.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointIndexBuilding, SourceVersionID: target.VersionID,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: target.ProjectionID,
		IndexVersionID: created.IndexVersion.ID, ExcludedSourceCount: &excluded,
	}})
	if err != nil {
		t.Fatal(err)
	}
	regression, err := repository.RunSnapshotRegression(ctx, domain.SnapshotRegressionCommand{
		WorkspaceID: writeback.WorkspaceID, DeliveryID: deliveryID, TargetSourceID: target.SourceID,
		TargetSourceVersionID: target.VersionID, TargetResultHash: resultHash,
		TargetParseProjectionID: target.ProjectionID, IndexVersionID: created.IndexVersion.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	regressionCheckpoint, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: indexCheckpoint.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointRegressionPassed, SourceVersionID: target.VersionID,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: target.ProjectionID,
		IndexVersionID: created.IndexVersion.ID, ExcludedSourceCount: &excluded,
		RegressionCode: domain.RegressionCodeSnapshotStructureV1, RegressionHash: regression.Hash,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := repository.TransitionIndex(ctx, domain.IndexTransition{
		WorkspaceID: writeback.WorkspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedVersion: created.IndexVersion.Version, Status: domain.IndexStatusReady, At: now.Add(7 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return completionFixture{
		Command: domain.CompleteReindexCommand{
			WorkspaceID: writeback.WorkspaceID, Fence: regressionCheckpoint.Fence,
			ActivationID: snapshotID(ordinal + 500_000), ActivationIdempotencyKey: "completion-activate-" + string(deliveryID),
			ActivationReasonCode: "REINDEX_REGRESSION_PASSED",
		},
		WorkspaceID: writeback.WorkspaceID, ExecutionID: writeback.ExecutionID, ProposalID: proposalID,
		RunID: runID, NodeID: nodeID, DeliveryID: deliveryID, AttemptID: claimed.Attempt.ID,
		TargetIndexID: ready.ID, OldActiveID: oldActivated.ActiveIndexVersion.ID,
	}
}

func satisfyCompletionGates(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture completionFixture) {
	t.Helper()
	if _, err := database.Exec(ctx, `UPDATE change_control.writeback_execution SET
		cleanup_completed_at=CURRENT_TIMESTAMP,version=version+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND status='verifying'`, string(fixture.ExecutionID)); err != nil {
		t.Fatal(err)
	}
	withReplicaRole(t, ctx, database, func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE workflow.node_run SET status='succeeded',lease_owner=NULL,lease_until=NULL,
			completed_at=CURRENT_TIMESTAMP,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND status='running'`, string(fixture.NodeID)); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, `UPDATE workflow.run SET status='succeeded',completed_at=CURRENT_TIMESTAMP,
			version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND status='running'`, string(fixture.RunID)); err != nil {
			t.Fatal(err)
		}
	})
}

func assertCompletionCommitted(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture completionFixture) {
	t.Helper()
	var deliveryStatus, attemptStatus, executionStatus, proposalStatus, targetStatus, oldStatus string
	var activations int
	if err := database.QueryRow(ctx, `SELECT
		(SELECT status FROM retrieval.reindex_delivery WHERE id=$1),
		(SELECT status FROM retrieval.reindex_delivery_attempt WHERE id=$2),
		(SELECT status FROM change_control.writeback_execution WHERE id=$3),
		(SELECT status FROM change_control.proposal WHERE id=$4),
		(SELECT status FROM retrieval.index_version WHERE id=$5),
		(SELECT status FROM retrieval.index_version WHERE id=$6),
		(SELECT count(*) FROM retrieval.index_activation WHERE workspace_id=$7 AND idempotency_key=$8)`,
		string(fixture.DeliveryID), string(fixture.AttemptID), string(fixture.ExecutionID), string(fixture.ProposalID),
		string(fixture.TargetIndexID), string(fixture.OldActiveID), string(fixture.WorkspaceID), fixture.Command.ActivationIdempotencyKey).Scan(
		&deliveryStatus, &attemptStatus, &executionStatus, &proposalStatus, &targetStatus, &oldStatus, &activations,
	); err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != "succeeded" || attemptStatus != "succeeded" || executionStatus != "completed" ||
		proposalStatus != "completed" || targetStatus != "active" || oldStatus != "retiring" || activations != 1 {
		t.Fatalf("delivery=%s attempt=%s execution=%s proposal=%s target=%s old=%s activations=%d",
			deliveryStatus, attemptStatus, executionStatus, proposalStatus, targetStatus, oldStatus, activations)
	}
}

func assertCompletionRolledBack(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture completionFixture) {
	t.Helper()
	var deliveryStatus, attemptStatus, executionStatus, proposalStatus, targetStatus, oldStatus string
	var activations int
	if err := database.QueryRow(ctx, `SELECT
		(SELECT status FROM retrieval.reindex_delivery WHERE id=$1),
		(SELECT status FROM retrieval.reindex_delivery_attempt WHERE id=$2),
		(SELECT status FROM change_control.writeback_execution WHERE id=$3),
		(SELECT status FROM change_control.proposal WHERE id=$4),
		(SELECT status FROM retrieval.index_version WHERE id=$5),
		(SELECT status FROM retrieval.index_version WHERE id=$6),
		(SELECT count(*) FROM retrieval.index_activation WHERE workspace_id=$7 AND idempotency_key=$8)`,
		string(fixture.DeliveryID), string(fixture.AttemptID), string(fixture.ExecutionID), string(fixture.ProposalID),
		string(fixture.TargetIndexID), string(fixture.OldActiveID), string(fixture.WorkspaceID), fixture.Command.ActivationIdempotencyKey).Scan(
		&deliveryStatus, &attemptStatus, &executionStatus, &proposalStatus, &targetStatus, &oldStatus, &activations,
	); err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != "processing" || attemptStatus != "processing" || executionStatus != "verifying" ||
		proposalStatus != "verifying" || targetStatus != "ready" || oldStatus != "active" || activations != 0 {
		t.Fatalf("rollback delivery=%s attempt=%s execution=%s proposal=%s target=%s old=%s activations=%d",
			deliveryStatus, attemptStatus, executionStatus, proposalStatus, targetStatus, oldStatus, activations)
	}
}

type completionFaultDB struct {
	pool  *pgxpool.Pool
	match string
}

type completionDelayDB struct {
	pool  *pgxpool.Pool
	match string
	delay time.Duration
}

func (database *completionDelayDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return database.pool.QueryRow(ctx, sql, args...)
}

func (database *completionDelayDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &completionDelayTx{Tx: tx, match: database.match, delay: database.delay}, nil
}

type completionDelayTx struct {
	pgx.Tx
	match string
	delay time.Duration
}

func (tx *completionDelayTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, tx.match) {
		time.Sleep(tx.delay)
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (database *completionFaultDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return database.pool.QueryRow(ctx, sql, args...)
}

func (database *completionFaultDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &completionFaultTx{Tx: tx, match: database.match}, nil
}

type completionFaultTx struct {
	pgx.Tx
	match string
}

func (tx *completionFaultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, tx.match) {
		return pgconn.CommandTag{}, errors.New("injected completion fault")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func (tx *completionFaultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, tx.match) {
		return completionFaultRow{}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

type completionFaultRow struct{}

func (completionFaultRow) Scan(...any) error { return errors.New("injected completion fault") }

type completionResponseLossDB struct {
	pool *pgxpool.Pool
	lose bool
}

func (database *completionResponseLossDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return database.pool.QueryRow(ctx, sql, args...)
}

func (database *completionResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if database.lose {
		database.lose = false
		return &responseLossTx{Tx: tx}, nil
	}
	return tx, nil
}

func completionErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func completionPrerequisitePendingError(err error) bool {
	var pending *foundation.Error
	return errors.As(err, &pending) && pending.Code == "REINDEX_COMPLETION_PREREQUISITE_PENDING" &&
		pending.Kind == foundation.ErrorRetryableFailure && pending.Retryable
}
