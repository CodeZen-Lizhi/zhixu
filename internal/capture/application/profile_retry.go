package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const profileRetryCommandSchema = "capture-profile-retry-command/v1"

// ProfileRetryCommand rebuilds a failed, unavailable, or stale document profile for one immutable Source Version.
type ProfileRetryCommand struct {
	WorkspaceID     foundation.ID
	SourceVersionID foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// ProfileRetryBinding is the complete identity of an idempotent profile retry request.
type ProfileRetryBinding struct {
	WorkspaceID     foundation.ID
	SourceVersionID foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
	RequestHash     string
}

// ProfileRetryRecord contains all facts that must commit with a profile retry receipt and processing event.
type ProfileRetryRecord struct {
	Binding   ProfileRetryBinding
	OutboxID  foundation.ID
	EventKey  string
	CreatedAt time.Time
}

// ProfileRetryResult is the exact Profile view returned for a newly queued retry or an idempotent replay.
type ProfileRetryResult struct {
	View     ProfileView
	Replayed bool
}

// ProfileRetryScheduler atomically queues a profile rebuild while preserving completed source and revision facts.
type ProfileRetryScheduler interface {
	ReplayProfileRetry(context.Context, ProfileRetryBinding) (ProfileRetryResult, bool, error)
	ScheduleProfileRetry(context.Context, ProfileRetryRecord) (ProfileRetryResult, error)
}

// ProfileRetrier is the application boundary used by the Profile HTTP endpoint.
type ProfileRetrier interface {
	RetryProfile(context.Context, ProfileRetryCommand) (ProfileRetryResult, error)
}

// ProfileRetryDependencies contains the minimal durable ports required by profile retry.
type ProfileRetryDependencies struct {
	Scheduler ProfileRetryScheduler
	IDs       foundation.IDGenerator
	Clock     foundation.Clock
}

// ProfileRetryService validates requests and delegates the atomic state transition to the repository.
type ProfileRetryService struct {
	dependencies ProfileRetryDependencies
}

// NewProfileRetryService creates an idempotent profile retry application service.
func NewProfileRetryService(dependencies ProfileRetryDependencies) (*ProfileRetryService, error) {
	if dependencies.Scheduler == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, unavailable("profile retry dependencies are unavailable")
	}
	return &ProfileRetryService{dependencies: dependencies}, nil
}

// RetryProfile queues a rebuild without discarding the immutable Source or the last readable Profile Revision.
func (service *ProfileRetryService) RetryProfile(ctx context.Context, command ProfileRetryCommand) (ProfileRetryResult, error) {
	if err := contextDone(ctx); err != nil {
		return ProfileRetryResult{}, err
	}
	if !validID(command.WorkspaceID) || !validID(command.SourceVersionID) || command.ExpectedVersion < 1 {
		return ProfileRetryResult{}, invalid("CAPTURE_PROFILE_RETRY_INVALID", "profile retry identity or version is invalid")
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return ProfileRetryResult{}, err
	}
	binding, err := profileRetryBinding(command.WorkspaceID, command.SourceVersionID, command.ExpectedVersion, key)
	if err != nil {
		return ProfileRetryResult{}, err
	}
	if replay, found, replayErr := service.dependencies.Scheduler.ReplayProfileRetry(ctx, binding); replayErr != nil {
		return ProfileRetryResult{}, replayErr
	} else if found {
		return validateProfileRetryResult(replay, command.WorkspaceID, command.SourceVersionID, true)
	}
	outboxID, err := service.dependencies.IDs.New()
	if err != nil {
		return ProfileRetryResult{}, err
	}
	now := service.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return ProfileRetryResult{}, inconsistent("CAPTURE_CLOCK_INVALID", "capture clock returned zero time")
	}
	result, err := service.dependencies.Scheduler.ScheduleProfileRetry(ctx, ProfileRetryRecord{
		Binding: binding, OutboxID: outboxID,
		EventKey:  "capture.profile-retry:v1:" + string(command.SourceVersionID) + ":" + binding.RequestHash,
		CreatedAt: now,
	})
	if err != nil {
		return ProfileRetryResult{}, err
	}
	return validateProfileRetryResult(result, command.WorkspaceID, command.SourceVersionID, result.Replayed)
}

func profileRetryBinding(workspaceID, sourceVersionID foundation.ID, expectedVersion int64, key string) (ProfileRetryBinding, error) {
	encoded, err := json.Marshal(struct {
		Schema          string        `json:"schema"`
		WorkspaceID     foundation.ID `json:"workspace_id"`
		SourceVersionID foundation.ID `json:"source_version_id"`
		ExpectedVersion int64         `json:"expected_version"`
	}{
		Schema: profileRetryCommandSchema, WorkspaceID: workspaceID,
		SourceVersionID: sourceVersionID, ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return ProfileRetryBinding{}, inconsistent("CAPTURE_PROFILE_RETRY_ENCODING_FAILED", "profile retry request could not be encoded")
	}
	digest := sha256.Sum256(encoded)
	return ProfileRetryBinding{
		WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, ExpectedVersion: expectedVersion,
		IdempotencyKey: key, RequestHash: hex.EncodeToString(digest[:]),
	}, nil
}

func validateProfileRetryResult(result ProfileRetryResult, workspaceID, sourceVersionID foundation.ID, replayed bool) (ProfileRetryResult, error) {
	if err := result.View.Profile.Validate(); err != nil || result.View.Profile.WorkspaceID != workspaceID ||
		result.View.Profile.SourceVersionID != sourceVersionID || result.Replayed != replayed ||
		!validProfileRetryView(result.View) {
		return ProfileRetryResult{}, inconsistent("CAPTURE_PROFILE_RETRY_RESULT_INVALID", "profile retry repository returned an invalid result")
	}
	return result, nil
}

func validProfileRetryView(view ProfileView) bool {
	if view.Profile.CurrentRevisionID == "" {
		return view.Revision == nil && len(view.Evidence) == 0
	}
	if view.Revision == nil || view.Revision.Validate() != nil || view.Revision.ID != view.Profile.CurrentRevisionID ||
		view.Revision.ProfileID != view.Profile.ID || view.Revision.WorkspaceID != view.Profile.WorkspaceID ||
		view.Revision.SourceVersionID != view.Profile.SourceVersionID || len(view.Evidence) == 0 {
		return false
	}
	required := make(map[foundation.ID]struct{})
	for _, candidate := range append(append([]domain.ProfileCandidate(nil), view.Revision.Content.Topics...), view.Revision.Content.Terms...) {
		for _, spanID := range candidate.SourceSpanIDs {
			required[spanID] = struct{}{}
		}
	}
	for _, point := range append(append([]domain.ProfilePoint(nil), view.Revision.Content.KnowledgePoints...), view.Revision.Content.Examples...) {
		for _, spanID := range point.SourceSpanIDs {
			required[spanID] = struct{}{}
		}
	}
	if len(required) != len(view.Evidence) {
		return false
	}
	seen := make(map[foundation.ID]struct{}, len(view.Evidence))
	for _, evidence := range view.Evidence {
		if evidence.RevisionID != view.Revision.ID || evidence.WorkspaceID != view.Profile.WorkspaceID ||
			evidence.SourceVersionID != view.Profile.SourceVersionID || !validID(evidence.SourceSpanID) ||
			!evidence.CreatedAt.Equal(view.Revision.CreatedAt) {
			return false
		}
		if _, duplicate := seen[evidence.SourceSpanID]; duplicate {
			return false
		}
		if _, found := required[evidence.SourceSpanID]; !found {
			return false
		}
		seen[evidence.SourceSpanID] = struct{}{}
	}
	return true
}

var _ ProfileRetrier = (*ProfileRetryService)(nil)
