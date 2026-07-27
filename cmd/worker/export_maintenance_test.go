package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	exportapplication "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRunExportMaintenanceRunsAllPhasesAndAdvancesCursor(t *testing.T) {
	startCursor := foundation.ID("10000000-0000-4000-8000-000000000001")
	nextCursor := foundation.ID("10000000-0000-4000-8000-000000000002")
	service := &exportMaintenanceStub{
		recoverResult: 2,
		sweepResult:   exportapplication.SweepResult{Expired: 1, Cleaned: 1},
		orphanResult: exportapplication.OrphanSweepResult{
			ScannedWorkspaces: 2,
			Deleted:           1,
			NextWorkspaceID:   nextCursor,
		},
	}

	got := runExportMaintenance(context.Background(), exportMaintenanceTestLogger(), service, startCursor, exportMaintenancePeriodicPhase)
	if got != nextCursor {
		t.Fatalf("maintenance cursor=%q want=%q", got, nextCursor)
	}
	if !reflect.DeepEqual(service.calls, []string{"recover", "sweep", "orphans"}) {
		t.Fatalf("maintenance calls=%v", service.calls)
	}
	if service.recoverWorkspaceID != "" || service.recoverLimit != exportapplication.MaxListLimit {
		t.Fatalf("recover workspace=%q limit=%d", service.recoverWorkspaceID, service.recoverLimit)
	}
	if service.sweepLimit != exportapplication.MaxListLimit {
		t.Fatalf("sweep limit=%d", service.sweepLimit)
	}
	if service.orphanCursor != startCursor || service.orphanWorkspaceLimit != exportOrphanWorkspaceBatch ||
		service.orphanFileLimit != exportapplication.MaxListLimit || service.orphanGrace != exportapplication.DefaultOrphanGrace {
		t.Fatalf("orphan request cursor=%q workspace_limit=%d file_limit=%d grace=%s",
			service.orphanCursor, service.orphanWorkspaceLimit, service.orphanFileLimit, service.orphanGrace)
	}
}

func TestRunExportMaintenanceContinuesAfterEarlierFailuresAndRetainsCursorOnOrphanFailure(t *testing.T) {
	startCursor := foundation.ID("10000000-0000-4000-8000-000000000003")
	service := &exportMaintenanceStub{
		recoverErr: errors.New("recover failed"),
		sweepErr:   errors.New("sweep failed"),
		orphanResult: exportapplication.OrphanSweepResult{
			NextWorkspaceID: foundation.ID("10000000-0000-4000-8000-000000000004"),
		},
		orphanErr: errors.New("orphan sweep failed"),
	}

	got := runExportMaintenance(context.Background(), exportMaintenanceTestLogger(), service, startCursor, exportMaintenanceStartupPhase)
	if got != startCursor {
		t.Fatalf("maintenance cursor=%q want retained=%q", got, startCursor)
	}
	if !reflect.DeepEqual(service.calls, []string{"recover", "sweep", "orphans"}) {
		t.Fatalf("maintenance calls=%v", service.calls)
	}
}

func TestRunExportMaintenanceWithoutServiceKeepsCursor(t *testing.T) {
	cursor := foundation.ID("10000000-0000-4000-8000-000000000005")
	if got := runExportMaintenance(context.Background(), exportMaintenanceTestLogger(), nil, cursor, exportMaintenancePeriodicPhase); got != cursor {
		t.Fatalf("maintenance cursor=%q want=%q", got, cursor)
	}
}

func exportMaintenanceTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type exportMaintenanceStub struct {
	calls []string

	recoverWorkspaceID foundation.ID
	recoverLimit       int
	recoverResult      int
	recoverErr         error

	sweepLimit  int
	sweepResult exportapplication.SweepResult
	sweepErr    error

	orphanCursor         foundation.ID
	orphanWorkspaceLimit int
	orphanFileLimit      int
	orphanGrace          time.Duration
	orphanResult         exportapplication.OrphanSweepResult
	orphanErr            error
}

func (stub *exportMaintenanceStub) Recover(_ context.Context, workspaceID foundation.ID, limit int) (int, error) {
	stub.calls = append(stub.calls, "recover")
	stub.recoverWorkspaceID = workspaceID
	stub.recoverLimit = limit
	return stub.recoverResult, stub.recoverErr
}

func (stub *exportMaintenanceStub) Sweep(_ context.Context, limit int) (exportapplication.SweepResult, error) {
	stub.calls = append(stub.calls, "sweep")
	stub.sweepLimit = limit
	return stub.sweepResult, stub.sweepErr
}

func (stub *exportMaintenanceStub) SweepOrphansAll(_ context.Context, afterWorkspaceID foundation.ID, workspaceLimit, fileLimit int, grace time.Duration) (exportapplication.OrphanSweepResult, error) {
	stub.calls = append(stub.calls, "orphans")
	stub.orphanCursor = afterWorkspaceID
	stub.orphanWorkspaceLimit = workspaceLimit
	stub.orphanFileLimit = fileLimit
	stub.orphanGrace = grace
	return stub.orphanResult, stub.orphanErr
}
