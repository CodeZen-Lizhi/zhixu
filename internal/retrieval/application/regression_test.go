package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestRegressionServiceRunsIndependentStoreSeam(t *testing.T) {
	command := domain.SnapshotRegressionCommand{
		WorkspaceID: "a0000000-0000-4000-8000-000000000001", DeliveryID: "a0000000-0000-4000-8000-000000000002",
		TargetSourceID: "a0000000-0000-4000-8000-000000000003", TargetSourceVersionID: "a0000000-0000-4000-8000-000000000004",
		TargetResultHash:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TargetParseProjectionID: "a0000000-0000-4000-8000-000000000005", IndexVersionID: "a0000000-0000-4000-8000-000000000006",
	}
	want := domain.SnapshotRegressionResult{
		Code:     domain.SnapshotStructureRegressionV1,
		Hash:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PassedAt: time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC),
	}
	store := &regressionStoreStub{result: want}
	service, err := NewRegressionService(store)
	if err != nil {
		t.Fatal(err)
	}

	got, err := service.RunSnapshotStructureV1(context.Background(), command)
	if err != nil || got != want || store.command != command {
		t.Fatalf("RunSnapshotStructureV1() = %#v, %v, command=%#v", got, err, store.command)
	}
}

func TestRegressionServicePreservesStoreError(t *testing.T) {
	want := foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSnapshotStructureRegressionFailed, false, errors.New("failed"))
	service, err := NewRegressionService(&regressionStoreStub{err: want})
	if err != nil {
		t.Fatal(err)
	}
	_, got := service.RunSnapshotStructureV1(context.Background(), domain.SnapshotRegressionCommand{})
	if !errors.Is(got, want) {
		t.Fatalf("error = %#v", got)
	}
}

func TestNewRegressionServiceRejectsMissingStore(t *testing.T) {
	service, err := NewRegressionService(nil)
	if service != nil || err == nil {
		t.Fatalf("NewRegressionService(nil) = %#v, %v", service, err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || classified.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

type regressionStoreStub struct {
	command domain.SnapshotRegressionCommand
	result  domain.SnapshotRegressionResult
	err     error
}

func (store *regressionStoreStub) RunSnapshotRegression(_ context.Context, command domain.SnapshotRegressionCommand) (domain.SnapshotRegressionResult, error) {
	store.command = command
	return store.result, store.err
}
