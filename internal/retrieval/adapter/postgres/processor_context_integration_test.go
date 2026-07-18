//go:build integration

package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProcessorContextLoaderReturnsStrictReadyCheckpointContext(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 610_000)
	loader, err := NewDeliveryRepository(database.DB(), foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := loader.LoadProcessorContext(ctx, fixture.Command.Fence)
	if err != nil {
		t.Fatal(err)
	}
	contextValue := loaded.Context
	if loaded.Disposition != application.ProcessorContextCurrent ||
		contextValue.Delivery.ID != fixture.DeliveryID || contextValue.Attempt.ID != fixture.AttemptID ||
		contextValue.Request.WorkspaceID != fixture.WorkspaceID || contextValue.Request.WritebackExecutionID != fixture.ExecutionID ||
		contextValue.Binding.WorkspaceID != fixture.WorkspaceID || contextValue.Binding.WritebackExecutionID != fixture.ExecutionID ||
		contextValue.TargetSourceID == "" || contextValue.IngestionAttempt == nil ||
		contextValue.Attempt.IngestionAttemptID == nil || contextValue.IngestionAttempt.ID != *contextValue.Attempt.IngestionAttemptID ||
		contextValue.IndexVersion == nil || contextValue.IndexVersion.ID != fixture.TargetIndexID ||
		contextValue.IndexVersion.Status != domain.IndexStatusReady || contextValue.Delivery.Regression == nil {
		t.Fatalf("loaded context = %#v", loaded)
	}
	if contextValue.IngestionAttempt.Status != "chunked" || contextValue.IngestionAttempt.SecurityStatus != "passed" ||
		contextValue.IngestionAttempt.ParseProjectionID == nil || contextValue.Delivery.ParseProjectionID == nil ||
		*contextValue.IngestionAttempt.ParseProjectionID != *contextValue.Delivery.ParseProjectionID {
		t.Fatalf("ingestion evidence = %#v", contextValue.IngestionAttempt)
	}
}

func TestProcessorContextLoaderReturnsStaleAndCommittedExplicitly(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 620_000)
	loader, err := NewDeliveryRepository(database.DB(), foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	staleFence := fixture.Command.Fence
	staleFence.AttemptID = snapshotID(620_999)
	stale, err := loader.LoadProcessorContext(ctx, staleFence)
	if err != nil || stale.Disposition != application.ProcessorContextStale {
		t.Fatalf("stale context = %#v, %v", stale, err)
	}

	satisfyCompletionGates(t, ctx, database.DB(), fixture)
	if _, err := repository.CompleteReindexTx(ctx, fixture.Command); err != nil {
		t.Fatal(err)
	}
	committed, err := loader.LoadProcessorContext(ctx, fixture.Command.Fence)
	if err != nil || committed.Disposition != application.ProcessorContextCommitted {
		t.Fatalf("committed context = %#v, %v", committed, err)
	}
}

func TestProcessorContextLoaderRejectsCommitMappingDrift(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 630_000)
	loader, err := NewDeliveryRepository(database.DB(), foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `UPDATE change_control.proposal_commit SET result_hash=$2 WHERE writeback_execution_id=$1`,
			string(fixture.ExecutionID), strings.Repeat("f", 64)); err != nil {
			t.Fatal(err)
		}
	})
	_, err = loader.LoadProcessorContext(ctx, fixture.Command.Fence)
	if processorContextErrorCode(err) != "REINDEX_PROCESSOR_CONTEXT_INVALID" {
		t.Fatalf("mapping drift error = %v", err)
	}
}

func TestProcessorContextLoaderRejectsExpiredLease(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedCompletionFixture(t, ctx, repository, database.DB(), 640_000)
	loader, err := NewDeliveryRepository(database.DB(), foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
		now := time.Now().UTC()
		if _, err := connection.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt
			SET started_at=$2,heartbeat_at=$3,lease_until=$4 WHERE id=$1`,
			string(fixture.AttemptID), now.Add(-3*time.Minute), now.Add(-2*time.Minute), now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	})
	_, err = loader.LoadProcessorContext(ctx, fixture.Command.Fence)
	if processorContextErrorCode(err) != "REINDEX_LEASE_EXPIRED" {
		t.Fatalf("expired lease error = %v", err)
	}
}

func processorContextErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
