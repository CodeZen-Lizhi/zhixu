package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const commandRetryCapture = "RETRY_CAPTURE"

// Retry schedules a fresh Workflow for a retryable terminal Capture without changing its immutable input.
func (service *Service) Retry(ctx context.Context, command RetryCommand) (RetryResult, error) {
	if err := contextDone(ctx); err != nil {
		return RetryResult{}, err
	}
	if !validID(command.WorkspaceID) || !validID(command.CaptureID) || command.ExpectedVersion < 1 {
		return RetryResult{}, invalid("CAPTURE_RETRY_INVALID", "capture retry identity or version is invalid")
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return RetryResult{}, err
	}
	retries, ok := service.dependencies.Repository.(RetryScheduler)
	if !ok || retries == nil {
		return RetryResult{}, unavailable("capture retry repository is unavailable")
	}
	binding, err := retryBinding(command.WorkspaceID, command.CaptureID, command.ExpectedVersion, key)
	if err != nil {
		return RetryResult{}, err
	}
	if replay, found, replayErr := retries.ReplayRetry(ctx, binding); replayErr != nil {
		return RetryResult{}, replayErr
	} else if found {
		return validateRetryResult(replay, command.WorkspaceID, command.CaptureID, true)
	}
	outboxID, err := service.dependencies.IDs.New()
	if err != nil {
		return RetryResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return RetryResult{}, err
	}
	result, err := retries.ScheduleRetry(ctx, RetryRecord{
		Binding: binding, CaptureID: command.CaptureID, ExpectedVersion: command.ExpectedVersion,
		OutboxID: outboxID, EventKey: "capture.process:v1:" + string(command.CaptureID) + ":" + binding.RequestHash,
		CreatedAt: now,
	})
	if err != nil {
		return RetryResult{}, err
	}
	return validateRetryResult(result, command.WorkspaceID, command.CaptureID, result.Replayed)
}

func retryBinding(workspaceID, captureID foundation.ID, expectedVersion int64, key string) (CommandBinding, error) {
	encoded, err := json.Marshal(struct {
		Schema          string        `json:"schema"`
		WorkspaceID     foundation.ID `json:"workspace_id"`
		CaptureID       foundation.ID `json:"capture_id"`
		ExpectedVersion int64         `json:"expected_version"`
	}{
		Schema: "capture-retry-command/v1", WorkspaceID: workspaceID,
		CaptureID: captureID, ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return CommandBinding{}, inconsistent("CAPTURE_RETRY_ENCODING_FAILED", "capture retry request could not be encoded")
	}
	digest := sha256.Sum256(encoded)
	return CommandBinding{
		WorkspaceID: workspaceID, IdempotencyKey: key,
		RequestHash: hex.EncodeToString(digest[:]), CommandType: commandRetryCapture,
	}, nil
}

func validateRetryResult(result RetryResult, workspaceID, captureID foundation.ID, replayed bool) (RetryResult, error) {
	if err := result.Capture.Validate(); err != nil || result.Capture.WorkspaceID != workspaceID ||
		result.Capture.ID != captureID || result.Replayed != replayed {
		return RetryResult{}, inconsistent("CAPTURE_RETRY_RESULT_INVALID", "capture retry repository returned an invalid result")
	}
	if result.Capture.Status != domain.StatusReceived && result.Capture.Status != domain.StatusSourceSaved {
		return RetryResult{}, inconsistent("CAPTURE_RETRY_RESULT_INVALID", "capture retry result is not queued")
	}
	return result, nil
}
