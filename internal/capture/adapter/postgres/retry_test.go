package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestProfileRetryReceiptRoundTripAndStrictDecoder(t *testing.T) {
	completion := validProfileCompletionCommand(t)
	revision := completion.Revision
	profile := domain.Profile{
		ID: revision.ProfileID, WorkspaceID: revision.WorkspaceID,
		CaptureID: "52000000-0000-4000-8000-000000000002", SourceVersionID: revision.SourceVersionID,
		CurrentRevisionID: revision.ID, Status: domain.ProfileStatusPending, Version: 3,
		CreatedAt: revision.CreatedAt.Add(-time.Minute), UpdatedAt: revision.CreatedAt.Add(time.Minute),
	}
	view := captureapp.ProfileView{Profile: profile, Revision: &revision, Evidence: completion.Evidence}
	binding := captureapp.ProfileRetryBinding{
		WorkspaceID: profile.WorkspaceID, SourceVersionID: profile.SourceVersionID, ExpectedVersion: 2,
		IdempotencyKey: "profile-retry-1", RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	encoded, err := encodeProfileRetryReceipt(binding, captureapp.ProfileRetryResult{View: view})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeProfileRetryReceipt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoreProfileRetryView(receipt)
	if err != nil || !reflect.DeepEqual(restored, view) {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"schema_version":"capture-profile-retry-receipt/v1"} {}`),
		[]byte(`{"schema_version":"capture-profile-retry-receipt/v1","unknown":true}`),
	} {
		if _, err := decodeProfileRetryReceipt(raw); postgresCaptureErrorCode(err) != "CAPTURE_PROFILE_RETRY_RECEIPT_INVALID" {
			t.Fatalf("decodeProfileRetryReceipt(%s) error=%#v", raw, err)
		}
	}
	invalid := view
	invalid.Revision = nil
	invalid.Evidence = nil
	if _, err := encodeProfileRetryReceipt(binding, captureapp.ProfileRetryResult{View: invalid}); postgresCaptureErrorCode(err) != "CAPTURE_PROFILE_RETRY_RECEIPT_INVALID" {
		t.Fatalf("missing revision receipt error=%#v", err)
	}
}

func TestProfileRetryRepositoryRejectsUnavailableDependencies(t *testing.T) {
	var repository *GORMProfileRepository
	_, _, err := repository.ReplayProfileRetry(context.Background(), captureapp.ProfileRetryBinding{})
	if postgresCaptureErrorCode(err) != "CAPTURE_PROFILE_REPOSITORY_UNAVAILABLE" {
		t.Fatalf("error=%#v", err)
	}
}

func TestQueuedRetryPreservesImmutableInputAndSelectsResumeState(t *testing.T) {
	now := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	urlCapture := retryCapture(domain.KindURL, domain.StatusFetchFailed)
	urlCapture.FetchStatus = domain.StageFailed
	queued, err := queuedRetry(urlCapture, now)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != domain.StatusReceived || queued.FetchStatus != domain.StagePending ||
		queued.OriginalURL != urlCapture.OriginalURL || queued.SourceID != urlCapture.SourceID {
		t.Fatalf("queued URL capture = %#v", queued)
	}

	fileCapture := retryCapture(domain.KindFile, domain.StatusProcessingFailed)
	fileCapture.LatestSourceVersionID = "52000000-0000-4000-8000-000000000004"
	fileCapture.OriginalInputHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fileCapture.OriginalURL = ""
	fileCapture.FetchStatus = domain.StageNotApplicable
	queued, err = queuedRetry(fileCapture, now)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != domain.StatusSourceSaved || queued.FetchStatus != domain.StageNotApplicable ||
		queued.OriginalInputHash != fileCapture.OriginalInputHash || queued.LatestSourceVersionID != fileCapture.LatestSourceVersionID {
		t.Fatalf("queued file capture = %#v", queued)
	}
}

func TestQueuedRetryRejectsNonRetryableAndActiveCapture(t *testing.T) {
	now := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	capture := retryCapture(domain.KindURL, domain.StatusFetchFailed)
	capture.Retryable = false
	if _, err := queuedRetry(capture, now); postgresCaptureErrorCode(err) != "CAPTURE_RETRY_NOT_ALLOWED" {
		t.Fatalf("non-retryable error = %#v", err)
	}
	capture.Retryable = true
	capture.Status = domain.StatusFetching
	if _, err := queuedRetry(capture, now); postgresCaptureErrorCode(err) != "CAPTURE_RETRY_NOT_ALLOWED" {
		t.Fatalf("active capture error = %#v", err)
	}
}

func TestRetryReceiptDecoderRejectsTrailingAndUnknownJSON(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"schema_version":"capture-retry-receipt/v1"} {}`),
		[]byte(`{"schema_version":"capture-retry-receipt/v1","unknown":true}`),
	} {
		if _, err := decodeRetryReceipt(raw); postgresCaptureErrorCode(err) != "CAPTURE_RETRY_RECEIPT_INVALID" {
			t.Fatalf("decodeRetryReceipt(%s) error = %#v", raw, err)
		}
	}
}

func TestCreateReceiptDecoderRejectsTrailingInvalidJSON(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"schema_version":"capture-create-receipt/v1"} {}`),
		[]byte(`{"schema_version":"capture-create-receipt/v1"} trailing`),
	} {
		if _, err := decodeReceipt(raw); postgresCaptureErrorCode(err) != "CAPTURE_RECEIPT_INVALID" {
			t.Fatalf("decodeReceipt(%s) error = %#v", raw, err)
		}
	}
}

func retryCapture(kind domain.Kind, status domain.Status) domain.Capture {
	now := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	return domain.Capture{
		ID: "52000000-0000-4000-8000-000000000002", WorkspaceID: "52000000-0000-4000-8000-000000000001",
		Kind: kind, DisplayName: "Java AI", OriginalLocation: "captures/52000000-0000-4000-8000-000000000002/input",
		OriginalURL: "https://example.com/java-ai", SourceID: "52000000-0000-4000-8000-000000000003",
		Status: status, FetchStatus: domain.StagePending, IngestionStatus: domain.StagePending,
		IndexStatus: domain.StagePending, ProfileStatus: domain.StagePending,
		FailureStage: "FETCH", ErrorCode: "CAPTURE_FETCH_TIMEOUT", Retryable: true,
		Version: 2, CapturedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
}

func postgresCaptureErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
