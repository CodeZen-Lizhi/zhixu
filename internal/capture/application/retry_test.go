package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRetrySchedulesOneOutboxAndReplaysExactReceipt(t *testing.T) {
	capture := retryCaptureFixture()
	capture.Status = domain.StatusFetchFailed
	capture.FetchStatus = domain.StageFailed
	capture.FailureStage = "FETCH"
	capture.ErrorCode = "CAPTURE_FETCH_TIMEOUT"
	capture.Retryable = true
	repository := &retryRepositoryFake{capture: capture}
	service := newRetryService(t, repository)
	command := RetryCommand{
		WorkspaceID: capture.WorkspaceID, CaptureID: capture.ID,
		ExpectedVersion: capture.Version, IdempotencyKey: "retry-url-1",
	}

	first, err := service.Retry(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Capture.Status != domain.StatusReceived || repository.scheduleCalls != 1 {
		t.Fatalf("first=%#v schedule_calls=%d", first, repository.scheduleCalls)
	}
	if repository.record.EventKey == "" || repository.record.OutboxID == "" || repository.record.ExpectedVersion != command.ExpectedVersion {
		t.Fatalf("retry record = %#v", repository.record)
	}
	repository.replay = first
	replayed, err := service.Retry(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Capture.ID != first.Capture.ID || repository.scheduleCalls != 1 {
		t.Fatalf("replayed=%#v schedule_calls=%d", replayed, repository.scheduleCalls)
	}
}

func TestRetryRejectsInvalidVersionAndMissingScheduler(t *testing.T) {
	capture := retryCaptureFixture()
	service := newRetryService(t, &captureRepositoryFake{})
	_, err := service.Retry(context.Background(), RetryCommand{
		WorkspaceID: capture.WorkspaceID, CaptureID: capture.ID, ExpectedVersion: 0, IdempotencyKey: "retry",
	})
	assertFoundationCode(t, err, "CAPTURE_RETRY_INVALID")

	_, err = service.Retry(context.Background(), RetryCommand{
		WorkspaceID: capture.WorkspaceID, CaptureID: capture.ID, ExpectedVersion: 1, IdempotencyKey: "retry",
	})
	assertFoundationCode(t, err, "CAPTURE_SERVICE_UNAVAILABLE")
}

type retryRepositoryFake struct {
	captureRepositoryFake
	capture       domain.Capture
	replay        RetryResult
	record        RetryRecord
	scheduleCalls int
}

func (repository *retryRepositoryFake) ReplayRetry(_ context.Context, binding CommandBinding) (RetryResult, bool, error) {
	if repository.replay.Capture.ID == "" {
		return RetryResult{}, false, nil
	}
	result := repository.replay
	result.Replayed = true
	return result, true, nil
}

func (repository *retryRepositoryFake) ScheduleRetry(_ context.Context, record RetryRecord) (RetryResult, error) {
	repository.scheduleCalls++
	repository.record = record
	capture := repository.capture
	capture.Status = domain.StatusReceived
	capture.FetchStatus = domain.StagePending
	capture.IngestionStatus = domain.StagePending
	capture.IndexStatus = domain.StagePending
	capture.ProfileStatus = domain.StagePending
	capture.FailureStage = ""
	capture.ErrorCode = ""
	capture.Retryable = false
	capture.Version++
	capture.UpdatedAt = retryTestNow
	return RetryResult{Capture: capture}, nil
}

func newRetryService(t *testing.T, repository Repository) *Service {
	t.Helper()
	service, err := NewService(Dependencies{
		Repository: repository, Content: &contentWriterFake{},
		IDs: &sequenceIDs{}, Clock: foundation.FixedClock{Value: retryTestNow},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

var retryTestNow = time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)

func retryCaptureFixture() domain.Capture {
	return domain.Capture{
		ID: "51000000-0000-4000-8000-000000000002", WorkspaceID: captureTestWorkspaceID,
		Kind: domain.KindURL, DisplayName: "Java AI", OriginalLocation: "captures/51000000-0000-4000-8000-000000000002/input",
		OriginalURL: "https://example.com/java-ai", SourceID: "51000000-0000-4000-8000-000000000003",
		Status: domain.StatusReceived, FetchStatus: domain.StagePending, IngestionStatus: domain.StagePending,
		IndexStatus: domain.StagePending, ProfileStatus: domain.StagePending, Version: 1,
		CapturedAt: retryTestNow, UpdatedAt: retryTestNow,
	}
}

func assertFoundationCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}
