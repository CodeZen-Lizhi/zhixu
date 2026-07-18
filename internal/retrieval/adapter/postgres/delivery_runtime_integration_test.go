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
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeliveryRuntimeClaimLeaseFenceAndCheckpointReplay(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	deliveryID, sourceVersionID := seedDeliveryRuntimeFixture(t, ctx, database.DB(), "a1000000")
	repository, err := NewDeliveryRepository(database.DB(), &deliveryRuntimeIDs{values: []foundation.ID{
		"a2000000-0000-4000-8000-000000000001", "a2000000-0000-4000-8000-000000000002",
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := application.NewDeliveryRuntime(repository)
	if err != nil {
		t.Fatal(err)
	}
	claimCommand := application.DeliveryClaimCommand{DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: 41, RiverAttempt: 1,
		DeliveryKey: "delivery-1", LeaseOwner: "worker-1", LeaseDuration: time.Second}
	claimed, err := runtime.Claim(ctx, claimCommand)
	if err != nil || claimed.Disposition != application.DeliveryClaimed || claimed.Replayed || claimed.LeaseReclaimed {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	replayed, err := runtime.Claim(ctx, claimCommand)
	if err != nil || !replayed.Replayed || replayed.Fence != claimed.Fence {
		t.Fatalf("Claim(replay) = %#v, %v", replayed, err)
	}
	heldCommand := claimCommand
	heldCommand.LeaseOwner = "worker-2"
	if _, err := runtime.Claim(ctx, heldCommand); deliveryRuntimeErrorCode(err) != "REINDEX_LEASE_HELD" {
		t.Fatalf("Claim(held) error = %v", err)
	}
	futureCommand := claimCommand
	futureCommand.DispatchNo = 2
	if _, err := runtime.Claim(ctx, futureCommand); deliveryRuntimeErrorCode(err) != "REINDEX_DISPATCH_BINDING_CONFLICT" {
		t.Fatalf("Claim(future generation) error = %v", err)
	}

	heartbeat, err := runtime.Heartbeat(ctx, application.DeliveryHeartbeatCommand{Fence: claimed.Fence, LeaseDuration: 2 * time.Second})
	if err != nil || heartbeat.Disposition != application.DeliveryMutationApplied || heartbeat.Fence.DeliveryVersion <= claimed.Fence.DeliveryVersion {
		t.Fatalf("Heartbeat() = %#v, %v", heartbeat, err)
	}
	oldHeartbeat, err := runtime.Heartbeat(ctx, application.DeliveryHeartbeatCommand{Fence: claimed.Fence, LeaseDuration: time.Second})
	if err != nil || oldHeartbeat.Disposition != application.DeliveryMutationApplied || !oldHeartbeat.Replayed {
		t.Fatalf("Heartbeat(old fence) = %#v, %v", oldHeartbeat, err)
	}

	checkpointCommand := application.DeliveryCheckpointCommand{Fence: heartbeat.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: sourceVersionID,
	}}
	checkpoint, err := runtime.Checkpoint(ctx, checkpointCommand)
	if err != nil || checkpoint.Disposition != application.DeliveryMutationApplied || checkpoint.Replayed {
		t.Fatalf("Checkpoint() = %#v, %v", checkpoint, err)
	}
	checkpointReplay, err := runtime.Checkpoint(ctx, checkpointCommand)
	if err != nil || checkpointReplay.Disposition != application.DeliveryMutationApplied || !checkpointReplay.Replayed || checkpointReplay.Fence != checkpoint.Fence {
		t.Fatalf("Checkpoint(response-loss replay) = %#v, %v", checkpointReplay, err)
	}
	latestFenceCommand := checkpointCommand
	latestFenceCommand.Fence = checkpoint.Fence
	latestFenceReplay, err := runtime.Checkpoint(ctx, latestFenceCommand)
	if err != nil || !latestFenceReplay.Replayed || latestFenceReplay.Fence != checkpoint.Fence || latestFenceReplay.Delivery.Version != checkpoint.Delivery.Version {
		t.Fatalf("Checkpoint(latest fence replay) = %#v, %v", latestFenceReplay, err)
	}

	if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET lease_until=CURRENT_TIMESTAMP WHERE id=$1`, string(claimed.Attempt.ID)); err == nil {
		t.Fatal("database must reject shortening an active lease")
	}
}

func TestDeliveryRuntimeExpiredReclaimAndOldOwnerFailClosed(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	deliveryID, _ := seedDeliveryRuntimeFixture(t, ctx, database.DB(), "b1000000")
	repository, err := NewDeliveryRepository(database.DB(), &deliveryRuntimeIDs{values: []foundation.ID{
		"b2000000-0000-4000-8000-000000000001", "b2000000-0000-4000-8000-000000000002",
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := application.NewDeliveryRuntime(repository)
	firstCommand := application.DeliveryClaimCommand{DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: 51, RiverAttempt: 1,
		DeliveryKey: "expired-1", LeaseOwner: "worker-old", LeaseDuration: 50 * time.Millisecond}
	first, err := runtime.Claim(ctx, firstCommand)
	if err != nil {
		var classified *foundation.Error
		_ = errors.As(err, &classified)
		t.Fatalf("Claim(first) error=%v classified=%#v", err, classified)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt
		SET lease_until=clock_timestamp() + interval '1 minute' WHERE id=$1`, string(first.Attempt.ID)); err == nil {
		t.Fatal("database must reject lease extension without a matching heartbeat")
	}
	if _, err := runtime.Heartbeat(ctx, application.DeliveryHeartbeatCommand{Fence: first.Fence, LeaseDuration: time.Second}); deliveryRuntimeErrorCode(err) != "REINDEX_LEASE_EXPIRED" {
		t.Fatalf("Heartbeat(expired) error = %v", err)
	}
	secondCommand := firstCommand
	secondCommand.RiverAttempt = 2
	secondCommand.DeliveryKey = "expired-2"
	secondCommand.LeaseOwner = "worker-new"
	secondCommand.LeaseDuration = time.Second
	second, err := runtime.Claim(ctx, secondCommand)
	if err != nil || !second.LeaseReclaimed || second.Attempt.AttemptNo != first.Attempt.AttemptNo+1 {
		var classified *foundation.Error
		_ = errors.As(err, &classified)
		t.Fatalf("Claim(reclaim) = %#v, err=%v classified=%#v", second, err, classified)
	}
	assertOldFenceLeaseLost(t, runtime, ctx, first.Fence)
	var oldStatus domain.DeliveryAttemptStatus
	if err := database.QueryRow(ctx, `SELECT status FROM retrieval.reindex_delivery_attempt WHERE id=$1`, string(first.Attempt.ID)).Scan(&oldStatus); err != nil || oldStatus != domain.DeliveryAttemptLeaseLost {
		t.Fatalf("old attempt status=%s, err=%v", oldStatus, err)
	}
}

func TestDeliveryRuntimeExpiredLeaseRejectsCheckpointResponseLossReplay(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	deliveryID, sourceVersionID := seedDeliveryRuntimeFixture(t, ctx, database.DB(), "b3000000")
	repository, err := NewDeliveryRepository(database.DB(), &deliveryRuntimeIDs{values: []foundation.ID{
		"b4000000-0000-4000-8000-000000000001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := application.NewDeliveryRuntime(repository)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := runtime.Claim(ctx, application.DeliveryClaimCommand{
		DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: 52, RiverAttempt: 1,
		DeliveryKey: "response-loss-expiry", LeaseOwner: "worker-old", LeaseDuration: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := application.DeliveryCheckpointCommand{Fence: claimed.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: sourceVersionID,
	}}
	if _, err := runtime.Checkpoint(ctx, command); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := runtime.Checkpoint(ctx, command); deliveryRuntimeErrorCode(err) != "REINDEX_LEASE_EXPIRED" {
		t.Fatalf("Checkpoint(expired response-loss replay) error = %v", err)
	}
}

func TestDeliveryRuntimeRejectsInvalidIngestionAttemptBinding(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	deliveryID, sourceVersionID := seedDeliveryRuntimeFixture(t, ctx, database.DB(), "b5000000")
	projectionID, failedAttemptID := seedDeliveryRuntimeFailedIngestion(t, ctx, database.DB(), "b5000000", sourceVersionID)
	repository, err := NewDeliveryRepository(database.DB(), &deliveryRuntimeIDs{values: []foundation.ID{
		"b6000000-0000-4000-8000-000000000001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := application.NewDeliveryRuntime(repository)
	claimed, err := runtime.Claim(ctx, application.DeliveryClaimCommand{
		DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: 53, RiverAttempt: 1,
		DeliveryKey: "invalid-ingestion", LeaseOwner: "worker", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceCheckpoint, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: claimed.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: sourceVersionID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: sourceCheckpoint.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointIngested, SourceVersionID: sourceVersionID,
		IngestionAttemptID: failedAttemptID, ParseProjectionID: projectionID,
	}})
	if deliveryRuntimeErrorCode(err) != "REINDEX_DELIVERY_INGESTION_BINDING_INVALID" {
		t.Fatalf("Checkpoint(invalid ingestion binding) error = %v", err)
	}
	var storedAttempt *string
	if err := database.QueryRow(ctx, `SELECT ingestion_attempt_id::text FROM retrieval.reindex_delivery_attempt WHERE id=$1`, string(claimed.Attempt.ID)).Scan(&storedAttempt); err != nil || storedAttempt != nil {
		t.Fatalf("stored ingestion attempt = %v, err=%v", storedAttempt, err)
	}
	if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET ingestion_attempt_id=$2 WHERE id=$1`,
		string(claimed.Attempt.ID), string(failedAttemptID)); err == nil {
		t.Fatal("database must reject invalid ingestion evidence assigned directly")
	}
}

func TestDeliveryRuntimeRetryAttemptInheritsFrozenIngestionWithoutChangingDeliveryCheckpoint(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	deliveryID, sourceVersionID := seedDeliveryRuntimeFixture(t, ctx, database.DB(), "c3000000")
	projectionID, ingestionAttemptID := seedDeliveryRuntimeSuccessfulIngestion(t, ctx, database.DB(), "c3000000", sourceVersionID)
	repository, err := NewDeliveryRepository(database.DB(), &deliveryRuntimeIDs{values: []foundation.ID{
		"c4000000-0000-4000-8000-000000000001", "c4000000-0000-4000-8000-000000000002",
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := application.NewDeliveryRuntime(repository)
	if err != nil {
		t.Fatal(err)
	}
	first, err := runtime.Claim(ctx, application.DeliveryClaimCommand{
		DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: 54, RiverAttempt: 1,
		DeliveryKey: "rebind-first", LeaseOwner: "worker-first", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceCheckpoint, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: first.Fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: sourceVersionID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ingestionCheckpoint := domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointIngested, SourceVersionID: sourceVersionID,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: projectionID,
	}
	ingested, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: sourceCheckpoint.Fence, Checkpoint: ingestionCheckpoint})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runtime.Fail(ctx, application.DeliveryFailureCommand{Fence: ingested.Fence,
		Failure: domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure,
			Code: "REINDEX_INDEX_TEMPORARY", Summary: "temporary index dependency failure"}, RetryDelay: time.Second,
	})
	if err != nil || failed.Delivery.Status != domain.DeliveryStatusRetryWait {
		t.Fatalf("Fail() = %#v, %v", failed, err)
	}
	if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery SET
		status='dispatched',dispatch_no=dispatch_no+1,next_attempt_at=NULL,
		failure_class=NULL,error_kind=NULL,error_code=NULL,error_summary=NULL,
		version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND status='retry_wait'`, string(deliveryID)); err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Claim(ctx, application.DeliveryClaimCommand{
		DeliveryID: deliveryID, DispatchNo: 2, RiverJobID: 55, RiverAttempt: 1,
		DeliveryKey: "rebind-second", LeaseOwner: "worker-second", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempt.IngestionAttemptID == nil || *second.Attempt.IngestionAttemptID != ingestionAttemptID ||
		second.Delivery.ParseProjectionID == nil || *second.Delivery.ParseProjectionID != projectionID {
		t.Fatalf("second claim = %#v", second)
	}
	inherited, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: second.Fence, Checkpoint: ingestionCheckpoint})
	if err != nil || inherited.Disposition != application.DeliveryMutationApplied || !inherited.Replayed ||
		inherited.Fence != second.Fence || inherited.Delivery.Version != second.Delivery.Version ||
		inherited.Attempt.IngestionAttemptID == nil || *inherited.Attempt.IngestionAttemptID != ingestionAttemptID ||
		inherited.Delivery.SourceVersionID == nil || *inherited.Delivery.SourceVersionID != sourceVersionID ||
		inherited.Delivery.ParseProjectionID == nil || *inherited.Delivery.ParseProjectionID != projectionID ||
		inherited.Delivery.IndexVersionID != nil || inherited.Delivery.Regression != nil {
		t.Fatalf("Checkpoint(inherited replay) = %#v, %v", inherited, err)
	}
	replay, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: second.Fence, Checkpoint: ingestionCheckpoint})
	if err != nil || !replay.Replayed || replay.Fence != second.Fence {
		t.Fatalf("Checkpoint(inherited exact replay) = %#v, %v", replay, err)
	}
}

func TestReindexDeliveryCheckpointPrefixRejectsDirectPartialTuple(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	deliveryID, sourceVersionID := seedDeliveryRuntimeFixture(t, ctx, database.DB(), "b7000000")
	projectionID, _ := seedDeliveryRuntimeFailedIngestion(t, ctx, database.DB(), "b7000000", sourceVersionID)
	if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery SET parse_projection_id=$2 WHERE id=$1`, string(deliveryID), string(projectionID)); err == nil {
		t.Fatal("database must reject parse projection without source checkpoint")
	}
}

func TestDeliveryRuntimeFailureClassesAndTerminalReplay(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		failure    domain.DeliveryFailure
		delay      time.Duration
		wantStatus domain.DeliveryStatus
	}{
		{"retry wait", "c1000000", domain.DeliveryFailure{Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "REINDEX_RETRY", Summary: "retry safely"}, time.Minute, domain.DeliveryStatusRetryWait},
		{"failed", "d1000000", domain.DeliveryFailure{Class: domain.DeliveryFailureNonRetryable, ErrorKind: foundation.ErrorInvalidInput, Code: "REINDEX_INVALID", Summary: "invalid request"}, 0, domain.DeliveryStatusFailed},
		{"manual", "e1000000", domain.DeliveryFailure{Class: domain.DeliveryFailureManualRecovery, ErrorKind: foundation.ErrorConsistencyViolation, Code: "REINDEX_UNCERTAIN", Summary: "result uncertain"}, 0, domain.DeliveryStatusManualRecovery},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, database, ctx := newRetrievalTestRepository(t)
			deliveryID, _ := seedDeliveryRuntimeFixture(t, ctx, database.DB(), test.prefix)
			attemptID := foundation.ID(strings.Replace("f2000000-0000-4000-8000-000000000001", "f2", string(rune('a'+index))+"2", 1))
			repository, err := NewDeliveryRepository(database.DB(), &deliveryRuntimeIDs{values: []foundation.ID{attemptID}})
			if err != nil {
				t.Fatal(err)
			}
			runtime, _ := application.NewDeliveryRuntime(repository)
			claim, err := runtime.Claim(ctx, application.DeliveryClaimCommand{DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: int64(61 + index), RiverAttempt: 1,
				DeliveryKey: "failure", LeaseOwner: "worker", LeaseDuration: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			command := application.DeliveryFailureCommand{Fence: claim.Fence, Failure: test.failure, RetryDelay: test.delay}
			failed, err := runtime.Fail(ctx, command)
			if err != nil || failed.Disposition != application.DeliveryMutationCommitted || failed.Delivery.Status != test.wantStatus {
				t.Fatalf("Fail() = %#v, %v", failed, err)
			}
			replay, err := runtime.Fail(ctx, command)
			if err != nil || replay.Disposition != application.DeliveryMutationCommitted || !replay.Replayed {
				t.Fatalf("Fail(replay) = %#v, %v", replay, err)
			}
			wrong := command
			wrong.Failure.Code += "_OTHER"
			wrongResult, err := runtime.Fail(ctx, wrong)
			if err != nil || wrongResult.Disposition != application.DeliveryMutationStale {
				t.Fatalf("Fail(conflict replay) = %#v, %v", wrongResult, err)
			}
			if test.wantStatus == domain.DeliveryStatusFailed {
				if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery SET updated_at=CURRENT_TIMESTAMP,version=version+1 WHERE id=$1`, string(deliveryID)); err == nil {
					t.Fatal("failed delivery must be immutable")
				}
			}
		})
	}
}

func assertOldFenceLeaseLost(t *testing.T, runtime *application.DeliveryRuntime, ctx context.Context, fence domain.DeliveryFence) {
	t.Helper()
	if _, err := runtime.Heartbeat(ctx, application.DeliveryHeartbeatCommand{Fence: fence, LeaseDuration: time.Second}); deliveryRuntimeErrorCode(err) != "REINDEX_LEASE_LOST" {
		t.Fatalf("old Heartbeat error = %v", err)
	}
	if _, err := runtime.Checkpoint(ctx, application.DeliveryCheckpointCommand{Fence: fence, Checkpoint: domain.DeliveryCheckpoint{
		Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: "b1000000-0000-4000-8000-000000000005",
	}}); deliveryRuntimeErrorCode(err) != "REINDEX_LEASE_LOST" {
		t.Fatalf("old Checkpoint error = %v", err)
	}
	if _, err := runtime.Fail(ctx, application.DeliveryFailureCommand{Fence: fence, Failure: domain.DeliveryFailure{
		Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "REINDEX_OLD_OWNER", Summary: "old owner",
	}, RetryDelay: time.Minute}); deliveryRuntimeErrorCode(err) != "REINDEX_LEASE_LOST" {
		t.Fatalf("old Fail error = %v", err)
	}
}

func seedDeliveryRuntimeFixture(t *testing.T, ctx context.Context, database *pgxpool.Pool, prefix string) (foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID := foundation.ID(prefix + "-0000-4000-8000-000000000001")
	sourceID := foundation.ID(prefix + "-0000-4000-8000-000000000002")
	artifactID := foundation.ID(prefix + "-0000-4000-8000-000000000004")
	sourceVersionID := foundation.ID(prefix + "-0000-4000-8000-000000000005")
	deliveryID := foundation.ID(prefix + "-0000-4000-8000-000000000006")
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$3,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "delivery-runtime-"+prefix, "/tmp/delivery-runtime-"+prefix)
	batch.Queue(`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		VALUES($1,$2,'file','runtime.md','runtime.md',CURRENT_TIMESTAMP)`, string(sourceID), string(workspaceID))
	batch.Queue(`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		VALUES($1,$2,$3,1,'.knowledge/sources/' || $3,CURRENT_TIMESTAMP)`, string(artifactID), string(workspaceID), strings.Repeat("a", 64))
	batch.Queue(`INSERT INTO core.source_version(id,source_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id)
		VALUES($1,$2,$3,1,'text/markdown','runtime.md','passed',CURRENT_TIMESTAMP,$4)`, string(sourceVersionID), string(sourceID), strings.Repeat("a", 64), string(artifactID))
	batch.Queue(`SET session_replication_role = replica`)
	batch.Queue(`INSERT INTO workflow.outbox_event(id,workspace_id,event_type,idempotency_key,payload,occurred_at)
		VALUES($1,$2,'reindex.requested',$3,'{}',CURRENT_TIMESTAMP)`, prefix+"-0000-4000-8000-000000000007", string(workspaceID), "runtime-outbox-"+prefix)
	batch.Queue(`INSERT INTO change_control.writeback_execution(
		id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,write_authorization_id,git_authorization_id,
		target_path,base_hash,result_hash,approved_change_hash,approved_git_head,status,idempotency_key,version,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'runtime.md',$10,$10,$10,$11,'prepared',$12,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		prefix+"-0000-4000-8000-000000000008", string(workspaceID), prefix+"-0000-4000-8000-000000000009",
		prefix+"-0000-4000-8000-00000000000a", prefix+"-0000-4000-8000-00000000000b", prefix+"-0000-4000-8000-00000000000c",
		prefix+"-0000-4000-8000-00000000000d", prefix+"-0000-4000-8000-00000000000e", prefix+"-0000-4000-8000-00000000000f",
		strings.Repeat("b", 64), strings.Repeat("c", 40), "runtime-writeback-"+prefix)
	batch.Queue(`INSERT INTO retrieval.reindex_delivery(id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,
		dispatch_no,attempt_no,version,manual_recovery_required,created_at,updated_at)
		VALUES($1,'reindex-consumer',$2,$3,$4,'dispatched',1,0,1,false,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(deliveryID),
		prefix+"-0000-4000-8000-000000000007", string(workspaceID), prefix+"-0000-4000-8000-000000000008")
	batch.Queue(`SET session_replication_role = origin`)
	results := database.SendBatch(ctx, batch)
	for range 9 {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			t.Fatal(err)
		}
	}
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	return deliveryID, sourceVersionID
}

func seedDeliveryRuntimeFailedIngestion(t *testing.T, ctx context.Context, database *pgxpool.Pool, prefix string, sourceVersionID foundation.ID) (foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID := foundation.ID(prefix + "-0000-4000-8000-000000000001")
	artifactID := foundation.ID(prefix + "-0000-4000-8000-000000000004")
	projectionID := foundation.ID(prefix + "-0000-4000-8000-000000000010")
	attemptID := foundation.ID(prefix + "-0000-4000-8000-000000000011")
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.parse_projection(
		id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at
	) VALUES($1,$2,$3,'goldmark','v1',$4,'v1',$5,CURRENT_TIMESTAMP)`, string(projectionID), string(workspaceID),
		string(artifactID), strings.Repeat("d", 64), strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		VALUES($1,$2,$3,CURRENT_TIMESTAMP)`, string(sourceVersionID), string(projectionID), string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,status,security_status,failure_stage,error_code,retryable,
		parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,
		started_at,completed_at,version
	) VALUES($1,$2,$3,'parse_failed','passed','parse','PARSER_FAILED',false,'goldmark','v1',$4,'structure-v1','v1',$5,1,
		CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,1)`, string(attemptID), string(workspaceID), string(sourceVersionID),
		strings.Repeat("d", 64), "delivery-failed-ingestion-"+prefix); err != nil {
		t.Fatal(err)
	}
	return projectionID, attemptID
}

func seedDeliveryRuntimeSuccessfulIngestion(t *testing.T, ctx context.Context, database *pgxpool.Pool, prefix string, sourceVersionID foundation.ID) (foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID := foundation.ID(prefix + "-0000-4000-8000-000000000001")
	artifactID := foundation.ID(prefix + "-0000-4000-8000-000000000004")
	projectionID := foundation.ID(prefix + "-0000-4000-8000-000000000012")
	attemptID := foundation.ID(prefix + "-0000-4000-8000-000000000013")
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.parse_projection(
		id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at
	) VALUES($1,$2,$3,'goldmark','v1',$4,'v1',$5,CURRENT_TIMESTAMP)`, string(projectionID), string(workspaceID),
		string(artifactID), strings.Repeat("d", 64), strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		VALUES($1,$2,$3,CURRENT_TIMESTAMP)`, string(sourceVersionID), string(projectionID), string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,status,security_status,retryable,
		parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,
		parse_projection_id,started_at,completed_at,version
	) VALUES($1,$2,$3,'chunked','passed',false,'goldmark','v1',$4,'structure-v1','v1',$5,1,$6,
		CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,1)`, string(attemptID), string(workspaceID), string(sourceVersionID),
		strings.Repeat("d", 64), "delivery-successful-ingestion-"+prefix, string(projectionID)); err != nil {
		t.Fatal(err)
	}
	return projectionID, attemptID
}

type deliveryRuntimeIDs struct {
	values []foundation.ID
	index  int
}

func (ids *deliveryRuntimeIDs) New() (foundation.ID, error) {
	value := ids.values[ids.index]
	ids.index++
	return value, nil
}

func deliveryRuntimeErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
