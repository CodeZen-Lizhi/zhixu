//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ccpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

func TestCompleteReindexTxAtomicallyActivatesAndReplaysAfterResponseLoss(t *testing.T) {
	for _, implementation := range []string{"gorm"} {
		t.Run(implementation, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestStore(t, implementation)
			completion := newCompletionTestStore(t, database, repository)
			fixture := seedCompletionFixture(t, ctx, repository, database, 700)
			satisfyCompletionGates(t, ctx, database.DB(), fixture)

			loss := *completion.(*GORMCompletionRepository)
			loss.unitOfWork = &completionResponseLossDB{UnitOfWork: loss.unitOfWork, lose: true}
			lossRepository := &loss
			if _, err := lossRepository.CompleteReindexTx(ctx, fixture.Command); completionErrorCode(err) != "REINDEX_COMPLETION_COMMIT_FAILED" {
				t.Fatalf("response loss error=%v cause=%v", err, errors.Unwrap(err))
			}
			assertCompletionCommitted(t, ctx, database.DB(), fixture)

			replayCommand := fixture.Command
			replayCommand.ActivationID = snapshotID(799_999)
			replayed, err := completion.CompleteReindexTx(ctx, replayCommand)
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
			historicalReplay, err := completion.CompleteReindexTx(ctx, replayCommand)
			if err != nil || !historicalReplay.Replayed || historicalReplay.ActiveIndexVersion.ID != fixture.TargetIndexID ||
				historicalReplay.ActiveIndexVersion.Status != domain.IndexStatusActive {
				t.Fatalf("historical replay=%#v err=%v", historicalReplay, err)
			}
			actualTarget, err := repository.GetIndex(ctx, fixture.WorkspaceID, fixture.TargetIndexID)
			if err != nil || actualTarget.Status != domain.IndexStatusRetiring {
				t.Fatalf("actual historical target=%#v err=%v", actualTarget, err)
			}
		})
	}
}

func TestCompleteReindexTxRequiresCleanupAndSucceededWorkflowWithoutMutatingIndexes(t *testing.T) {
	for _, implementation := range []string{"gorm"} {
		t.Run(implementation, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestStore(t, implementation)
			completion := newCompletionTestStore(t, database, repository)
			fixture := seedCompletionFixture(t, ctx, repository, database, 710)
			if _, err := completion.CompleteReindexTx(ctx, fixture.Command); !completionPrerequisitePendingError(err) {
				t.Fatalf("gate error=%v", err)
			}
			assertCompletionRolledBack(t, ctx, database.DB(), fixture)

			if _, err := database.DB().Exec(ctx, `UPDATE change_control.writeback_execution SET
				cleanup_completed_at=CURRENT_TIMESTAMP,version=version+1,updated_at=CURRENT_TIMESTAMP
				WHERE id=$1 AND status='verifying'`, string(fixture.ExecutionID)); err != nil {
				t.Fatal(err)
			}
			if _, err := completion.CompleteReindexTx(ctx, fixture.Command); !completionPrerequisitePendingError(err) {
				t.Fatalf("workflow gate error=%v", err)
			}
			assertCompletionRolledBack(t, ctx, database.DB(), fixture)
		})
	}
}

func TestCompleteReindexTxSupportsV2HybridAndRejectsVersionKindMismatch(t *testing.T) {
	t.Run("v2 hybrid", func(t *testing.T) {
		repository, database, ctx := newRetrievalTestRepository(t)
		fixture := seedCompletionFixture(t, ctx, repository, database, 712)
		upgradeCompletionFixtureToHybrid(t, ctx, database.DB(), fixture, 712)
		satisfyCompletionGates(t, ctx, database.DB(), fixture)
		result, err := repository.CompleteReindexTx(ctx, fixture.Command)
		if err != nil || result.ActiveIndexVersion.EmbeddingVersionID == nil {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		assertCompletionCommitted(t, ctx, database.DB(), fixture)
	})

	t.Run("v2 cannot complete fts only", func(t *testing.T) {
		repository, database, ctx := newRetrievalTestRepository(t)
		fixture := seedCompletionFixture(t, ctx, repository, database, 713)
		withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
			if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery SET regression_code=$2 WHERE id=$1`,
				string(fixture.DeliveryID), domain.RegressionCodeSnapshotStructureV2); err != nil {
				t.Fatal(err)
			}
		})
		satisfyCompletionGates(t, ctx, database.DB(), fixture)
		if _, err := repository.CompleteReindexTx(ctx, fixture.Command); completionErrorCode(err) != "REINDEX_COMPLETION_GATE_FAILED" {
			t.Fatalf("mismatch error=%v", err)
		}
		assertCompletionRolledBack(t, ctx, database.DB(), fixture)
	})

	t.Run("v1 cannot complete hybrid", func(t *testing.T) {
		repository, database, ctx := newRetrievalTestRepository(t)
		fixture := seedCompletionFixture(t, ctx, repository, database, 714)
		upgradeCompletionFixtureToHybrid(t, ctx, database.DB(), fixture, 714)
		withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
			if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery SET regression_code=$2 WHERE id=$1`,
				string(fixture.DeliveryID), domain.RegressionCodeSnapshotStructureV1); err != nil {
				t.Fatal(err)
			}
		})
		satisfyCompletionGates(t, ctx, database.DB(), fixture)
		if _, err := repository.CompleteReindexTx(ctx, fixture.Command); completionErrorCode(err) != "REINDEX_COMPLETION_GATE_FAILED" {
			t.Fatalf("mismatch error=%v", err)
		}
		assertCompletionRolledBack(t, ctx, database.DB(), fixture)
	})
}

func TestCompleteReindexTxRequiresLatestFullFenceAndLiveDatabaseLease(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database, 715)
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
	for _, implementation := range []string{"gorm"} {
		t.Run(implementation, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestStore(t, implementation)
			completion := newCompletionTestStore(t, database, repository)
			fixture := seedCompletionFixture(t, ctx, repository, database, 716)
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
				_, completeErr := completion.CompleteReindexTx(ctx, fixture.Command)
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
		})
	}
}

func TestCompleteReindexTxRechecksLeaseAtFinalMutation(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database, 717)
	satisfyCompletionGates(t, ctx, database.DB(), fixture)
	withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt
            SET lease_until=clock_timestamp() + interval '150 milliseconds' WHERE id=$1`, string(fixture.AttemptID)); err != nil {
			t.Fatal(err)
		}
	})
	delayed := false
	interceptCompletionMutation(t, database, "UPDATE retrieval.reindex_delivery_attempt", "", func(*gorm.DB) {
		delayed = true
		time.Sleep(250 * time.Millisecond)
	})
	if _, err := repository.CompleteReindexTx(ctx, fixture.Command); completionErrorCode(err) != "REINDEX_LEASE_EXPIRED" {
		t.Fatalf("completion after mutation delay error=%v", err)
	}
	if !delayed {
		t.Fatal("completion did not reach the final mutation delay")
	}
	assertCompletionRolledBack(t, ctx, database.DB(), fixture)
}

func TestCompleteReindexTxFaultInjectionRollsBackEveryMutationStage(t *testing.T) {
	stages := []struct {
		name  string
		match string
		table string
		owner bool
	}{
		{name: "activation receipt", match: "INSERT INTO retrieval.index_activation"},
		{name: "previous retire", match: "UPDATE retrieval.index_version SET status='retiring'"},
		{name: "target activate", match: "UPDATE retrieval.index_version SET status='active'"},
		{name: "attempt succeeded", match: "UPDATE retrieval.reindex_delivery_attempt"},
		{name: "delivery succeeded", match: "UPDATE retrieval.reindex_delivery SET"},
		{name: "execution completed", table: "writeback_execution"},
		{name: "proposal completed", table: "proposal"},
		{name: "gorm owner rollback", owner: true},
	}
	for index, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestStore(t, "gorm")
			fixture := seedCompletionFixture(t, ctx, repository, database, 720+index)
			satisfyCompletionGates(t, ctx, database.DB(), fixture)
			completion := newCompletionTestStore(t, database, repository).(*GORMCompletionRepository)
			fault := &completionFaultDB{ScopedReindexCompletion: completion.completion}
			triggered := false
			if stage.owner {
				completion.completion = fault
			} else {
				interceptCompletionMutation(t, database, stage.match, stage.table, func(transaction *gorm.DB) {
					triggered = true
					transaction.AddError(errors.New("injected completion fault"))
				})
			}
			if _, err := completion.CompleteReindexTx(ctx, fixture.Command); err == nil {
				t.Fatal("fault injection must fail")
			}
			if stage.owner && !fault.ownerCompleted || !stage.owner && !triggered {
				t.Fatal("completion failed before the requested mutation injection")
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

func newCompletionTestStore(t *testing.T, pool *platformpostgres.Pool, _ retrievalTestStore) application.CompletionStore {
	t.Helper()
	owner, err := ccpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := NewGORMCompletionRepository(pool, owner)
	if err != nil {
		t.Fatal(err)
	}
	return completion
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

func seedCompletionFixture(t *testing.T, ctx context.Context, repository retrievalTestStore, pool *platformpostgres.Pool, ordinal int) completionFixture {
	t.Helper()
	database := pool.DB()
	writeback := seedDispatcherWriteback(t, ctx, pool, "", "complete-"+string(rune(ordinal)))
	var runID, nodeID, proposalID foundation.ID
	var resultHash string
	if err := database.QueryRow(ctx, `SELECT workflow_run_id::text,node_run_id::text,proposal_id::text,result_hash
		FROM change_control.writeback_execution WHERE id=$1`, string(writeback.ExecutionID)).Scan(&runID, &nodeID, &proposalID, &resultHash); err != nil {
		t.Fatal(err)
	}
	dispatcher := newGORMTestDispatcher(t, pool, nil)
	if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var deliveryID foundation.ID
	if err := database.QueryRow(ctx, `SELECT id::text FROM retrieval.reindex_delivery WHERE outbox_event_id=$1`, string(writeback.EventID)).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	ids := &deliveryRuntimeIDs{values: []foundation.ID{snapshotID(ordinal + 100_000)}}
	deliveryRepository, err := NewGORMDeliveryRepository(pool, ids)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := application.NewDeliveryRuntime(deliveryRepository)
	if err != nil {
		t.Fatal(err)
	}
	var riverJobID int64
	if err := database.QueryRow(ctx, `SELECT id FROM workflow.river_job WHERE args->>'delivery_id'=$1`, string(deliveryID)).Scan(&riverJobID); err != nil {
		t.Fatal(err)
	}
	claimed, err := runtime.Claim(ctx, application.DeliveryClaimCommand{
		DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: riverJobID, RiverAttempt: 1,
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
		RegressionCode: domain.SnapshotStructureRegressionV1,
		WorkspaceID:    writeback.WorkspaceID, DeliveryID: deliveryID, TargetSourceID: target.SourceID,
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

func upgradeCompletionFixtureToHybrid(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture completionFixture, ordinal int) {
	t.Helper()
	embeddingID := snapshotID(ordinal + 550_000)
	if _, err := database.Exec(ctx, `INSERT INTO retrieval.embedding_version(
		id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at
	) VALUES($1,'test','direct','v1',$2,3,'l2','cosine',$3,now())`, string(embeddingID),
		"completion-embed-"+string(fixture.DeliveryID), snapshotHash("completion-embedding", ordinal)); err != nil {
		t.Fatal(err)
	}
	fusion, err := domain.CanonicalRRFConfig(domain.RRFConfig{
		SchemaVersion: 1, Method: domain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 200, VectorCandidateLimit: 200,
		FusedCandidateLimit: 100, RerankCandidateLimit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	withReplicaRole(t, ctx, database, func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE retrieval.index_version
			SET embedding_version_id=$2,fusion_config=$3::jsonb,degraded_capabilities=$4::jsonb
			WHERE id=$1`, string(fixture.TargetIndexID), string(embeddingID), fusion, json.RawMessage(`[]`)); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, `UPDATE retrieval.chunk_projection
			SET embedding_version_id=$2,embedding='[1,0,0]'::vector,vector_status='ready',failure_code=NULL
			WHERE index_version_id=$1`, string(fixture.TargetIndexID), string(embeddingID)); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery SET regression_code=$2 WHERE id=$1`,
			string(fixture.DeliveryID), domain.RegressionCodeSnapshotStructureV2); err != nil {
			t.Fatal(err)
		}
	})
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
	application.ScopedReindexCompletion
	ownerCompleted bool
}

// owner 的两次 CAS 已执行后再失败，验证 Activation、Delivery 与两个 owner 写入共同回滚。
func (database *completionFaultDB) CompleteReindexScoped(ctx context.Context, scope foundation.TransactionScope, transition application.ReindexCompletionTransition) error {
	if err := database.ScopedReindexCompletion.CompleteReindexScoped(ctx, scope, transition); err != nil {
		return err
	}
	database.ownerCompleted = true
	return errors.New("injected completion fault")
}

// 每个测试有独立 GORM root，拦截真实 mutation，清理时移除 callback。
func interceptCompletionMutation(t *testing.T, pool *platformpostgres.Pool, match, table string, inject func(*gorm.DB)) {
	t.Helper()
	root, err := pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	intercept := func(transaction *gorm.DB) {
		query := strings.Join(strings.Fields(transaction.Statement.SQL.String()), " ")
		if match != "" && strings.Contains(query, match) {
			inject(transaction)
		}
	}
	interceptUpdate := func(transaction *gorm.DB) {
		if table != "" && transaction.Statement.Table == table {
			inject(transaction)
		}
	}
	const name = "retrieval_test_completion_mutation"
	if err := root.Callback().Row().Before("gorm:row").Register(name, intercept); err != nil {
		t.Fatal(err)
	}
	if err := root.Callback().Raw().Before("gorm:raw").Register(name, intercept); err != nil {
		t.Fatal(err)
	}
	if err := root.Callback().Update().Before("gorm:update").Register(name, interceptUpdate); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, err := range []error{root.Callback().Row().Remove(name), root.Callback().Raw().Remove(name), root.Callback().Update().Remove(name)} {
			if err != nil {
				t.Errorf("remove completion mutation callback: %v", err)
			}
		}
	})
}

type completionResponseLossDB struct {
	foundation.UnitOfWork
	lose bool
}

func (database *completionResponseLossDB) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := database.UnitOfWork.Within(ctx, options, work); err != nil {
		return err
	}
	if database.lose {
		database.lose = false
		return errors.New("commit response lost")
	}
	return nil
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
