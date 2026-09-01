//go:build integration && testcontainers

package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestHealthScheduleRepositoryClaimsOnceReclaimsSameDueAndAcknowledges(t *testing.T) {
	ctx := context.Background()
	pool := newHealthIntegrationPool(t)

	ids := foundation.NewUUIDGenerator(nil)
	workspaceID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootPath := "/tmp/health-schedule-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
VALUES($1,'health-schedule-delivery',$2,$2,$3,'inactive',1,$3,$3)`, string(workspaceID), rootPath, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })

	repository, err := NewScheduleRepository(pool, ids, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	dueAt := now.Add(-72 * time.Hour)
	schedule, err := repository.Create(ctx, healthapp.ScheduleCreateCommand{
		WorkspaceID:    workspaceID,
		Scope:          domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Cadence:        domain.ScheduleCadenceDaily,
		Timezone:       "UTC",
		MaxItems:       100,
		NextRunAt:      &dueAt,
		IdempotencyKey: "schedule-create-1",
	})
	if err != nil {
		t.Fatalf("create schedule: %v: %v", err, errors.Unwrap(err))
	}

	type claimResult struct {
		claims []healthapp.DueScheduleClaim
		err    error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			claims, claimErr := repository.ClaimDue(ctx, 1)
			results <- claimResult{claims: claims, err: claimErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var first healthapp.DueScheduleClaim
	claimCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		claimCount += len(result.claims)
		if len(result.claims) == 1 {
			first = result.claims[0]
		}
	}
	if claimCount != 1 || first.Schedule.ID != schedule.ID || !first.DueAt.Equal(dueAt) || first.Schedule.NextRunAt == nil || !first.Schedule.NextRunAt.After(now) || first.Schedule.LastRunAt != nil {
		t.Fatalf("claim count=%d first=%#v", claimCount, first)
	}
	firstNextRun := *first.Schedule.NextRunAt

	if claims, claimErr := repository.ClaimDue(ctx, 1); claimErr != nil || len(claims) != 0 {
		t.Fatalf("active lease claims=%#v err=%v", claims, claimErr)
	}
	_, err = repository.Update(ctx, healthapp.ScheduleUpdateCommand{
		WorkspaceID: workspaceID, ScheduleID: schedule.ID, ExpectedVersion: first.Schedule.Version,
		Cadence: domain.ScheduleCadenceDisabled, Timezone: "UTC", MaxItems: 100,
		IdempotencyKey: "schedule-update-pending",
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "HEALTH_SCHEDULE_DISPATCH_PENDING" {
		t.Fatalf("pending update error=%v", err)
	}

	var reclaimed healthapp.DueScheduleClaim
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		claims, claimErr := repository.ClaimDue(ctx, 1)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if len(claims) == 1 {
			reclaimed = claims[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reclaimed.Schedule.ID == "" || !reclaimed.DueAt.Equal(first.DueAt) || reclaimed.Schedule.NextRunAt == nil || !reclaimed.Schedule.NextRunAt.Equal(firstNextRun) {
		t.Fatalf("reclaimed=%#v first=%#v", reclaimed, first)
	}
	if err := repository.AcknowledgeDue(ctx, reclaimed); err != nil {
		t.Fatal(err)
	}
	if err := repository.AcknowledgeDue(ctx, reclaimed); err != nil {
		t.Fatalf("ack replay failed: %v", err)
	}

	var pendingDueAt, leaseUntil, lastRunAt *time.Time
	var persistedNext time.Time
	if err := pool.QueryRow(ctx, `SELECT pending_due_at,dispatch_lease_until,last_run_at,next_run_at
FROM ops.health_schedule WHERE id=$1`, string(schedule.ID)).Scan(&pendingDueAt, &leaseUntil, &lastRunAt, &persistedNext); err != nil {
		t.Fatal(err)
	}
	if pendingDueAt != nil || leaseUntil != nil || lastRunAt == nil || !lastRunAt.Equal(dueAt) || !persistedNext.Equal(firstNextRun) {
		t.Fatalf("pending=%v lease=%v last=%v next=%v", pendingDueAt, leaseUntil, lastRunAt, persistedNext)
	}

	loaded, err := repository.Get(ctx, workspaceID, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	releasedDueAt := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	updated, err := repository.Update(ctx, healthapp.ScheduleUpdateCommand{
		WorkspaceID: workspaceID, ScheduleID: schedule.ID, ExpectedVersion: loaded.Version,
		Cadence: domain.ScheduleCadenceDaily, Timezone: "UTC", MaxItems: 100, NextRunAt: &releasedDueAt,
		IdempotencyKey: "schedule-update-release",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimDue(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("release claim=%#v err=%v updated=%#v", claimed, err, updated)
	}
	releasedNext := *claimed[0].Schedule.NextRunAt
	if err := repository.ReleaseDue(ctx, claimed[0]); err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.ClaimDue(ctx, 1)
	if err != nil || len(replayed) != 1 || !replayed[0].DueAt.Equal(releasedDueAt) || replayed[0].Schedule.NextRunAt == nil || !replayed[0].Schedule.NextRunAt.Equal(releasedNext) {
		t.Fatalf("released replay=%#v err=%v", replayed, err)
	}
	if err := repository.AcknowledgeDue(ctx, replayed[0]); err != nil {
		t.Fatal(err)
	}
	createReplay, err := repository.Create(ctx, healthapp.ScheduleCreateCommand{
		WorkspaceID:    workspaceID,
		Scope:          schedule.Scope,
		Cadence:        domain.ScheduleCadenceDaily,
		Timezone:       "UTC",
		MaxItems:       100,
		NextRunAt:      &dueAt,
		IdempotencyKey: "schedule-create-1",
	})
	if err != nil || !reflect.DeepEqual(createReplay, schedule) {
		t.Fatalf("create replay=%#v original=%#v err=%v", createReplay, schedule, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_schedule_command(
workspace_id,idempotency_key,request_hash,command_type,schedule_id,schedule_version,receipt,created_at)
SELECT workspace_id,'schedule-tampered-json',request_hash,command_type,schedule_id,schedule_version,
       jsonb_set(receipt,'{schedule,MaxItems}','999'::jsonb,false),created_at
FROM ops.health_schedule_command
WHERE workspace_id=$1 AND idempotency_key='schedule-create-1'`, string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(ctx, healthapp.ScheduleCreateCommand{
		WorkspaceID:    workspaceID,
		Scope:          schedule.Scope,
		Cadence:        domain.ScheduleCadenceDaily,
		Timezone:       "UTC",
		MaxItems:       100,
		NextRunAt:      &dueAt,
		IdempotencyKey: "schedule-tampered-json",
	}); err == nil {
		t.Fatal("valid JSON schedule receipt tampering unexpectedly replayed")
	}
	updateReplay, err := repository.Update(ctx, healthapp.ScheduleUpdateCommand{
		WorkspaceID: workspaceID, ScheduleID: schedule.ID, ExpectedVersion: loaded.Version,
		Cadence: domain.ScheduleCadenceDaily, Timezone: "UTC", MaxItems: 100, NextRunAt: &releasedDueAt,
		IdempotencyKey: "schedule-update-release",
	})
	if err != nil || !reflect.DeepEqual(updateReplay, updated) {
		t.Fatalf("update replay=%#v original=%#v err=%v", updateReplay, updated, err)
	}

	// Receipt 不是可变的业务事实；损坏的 envelope 必须阻断 replay，而不能读取当前 schedule 伪造成功。
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_schedule_command(
workspace_id,idempotency_key,request_hash,command_type,schedule_id,schedule_version,receipt,created_at)
VALUES($1,'schedule-tampered-receipt',repeat('a',64),'CREATE',$2,1,
'{"schema_version":"health-schedule-command-receipt/v1","schedule":{}}'::jsonb,$3)`, string(workspaceID), string(schedule.ID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(ctx, healthapp.ScheduleCreateCommand{
		WorkspaceID:    workspaceID,
		Scope:          schedule.Scope,
		Cadence:        domain.ScheduleCadenceDaily,
		Timezone:       "UTC",
		MaxItems:       100,
		NextRunAt:      &dueAt,
		IdempotencyKey: "schedule-tampered-receipt",
	}); err == nil {
		t.Fatal("tampered schedule receipt unexpectedly replayed")
	}

	// Commit response-loss 后回查 receipt，不能重复创建第二条 schedule。
	lossDB := &healthScanCommitLossDB{Pool: pool}
	lossRepository, err := NewScheduleRepository(lossDB, ids)
	if err != nil {
		t.Fatal(err)
	}
	lossCurrent, err := repository.Get(ctx, workspaceID, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	lossKey := "schedule-update-response-loss"
	lossNextRun := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Microsecond)
	lossSchedule, err := lossRepository.Update(ctx, healthapp.ScheduleUpdateCommand{
		WorkspaceID:     workspaceID,
		ScheduleID:      schedule.ID,
		ExpectedVersion: lossCurrent.Version,
		Cadence:         domain.ScheduleCadenceWeekly,
		Timezone:        "UTC",
		MaxItems:        100,
		NextRunAt:       &lossNextRun,
		IdempotencyKey:  lossKey,
	})
	if err != nil || !lossDB.lost.Load() || lossSchedule.ID == "" {
		t.Fatalf("response-loss create=%#v lost=%v err=%v", lossSchedule, lossDB.lost.Load(), err)
	}
	lossReplay, err := lossRepository.Update(ctx, healthapp.ScheduleUpdateCommand{
		WorkspaceID:     workspaceID,
		ScheduleID:      schedule.ID,
		ExpectedVersion: lossCurrent.Version,
		Cadence:         domain.ScheduleCadenceWeekly,
		Timezone:        "UTC",
		MaxItems:        100,
		NextRunAt:       &lossNextRun,
		IdempotencyKey:  lossKey,
	})
	if err != nil || !reflect.DeepEqual(lossReplay, lossSchedule) {
		t.Fatalf("response-loss replay=%#v original=%#v err=%v", lossReplay, lossSchedule, err)
	}
}
