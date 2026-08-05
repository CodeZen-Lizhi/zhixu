package application

import (
	"context"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestProfileRetrySchedulesOneOutboxAndReplaysExactView(t *testing.T) {
	view := retryProfileFixture(t)
	profile := view.Profile
	repository := &profileRetrySchedulerFake{view: view}
	service, err := NewProfileRetryService(ProfileRetryDependencies{
		Scheduler: repository, IDs: &sequenceIDs{}, Clock: foundation.FixedClock{Value: retryTestNow},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := ProfileRetryCommand{
		WorkspaceID: profile.WorkspaceID, SourceVersionID: profile.SourceVersionID,
		ExpectedVersion: profile.Version, IdempotencyKey: "profile-retry-1",
	}
	first, err := service.RetryProfile(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.View.Profile.Status != domain.ProfileStatusPending ||
		first.View.Profile.CurrentRevisionID != profile.CurrentRevisionID || first.View.Revision == nil ||
		!reflect.DeepEqual(first.View.Revision, view.Revision) || !reflect.DeepEqual(first.View.Evidence, view.Evidence) ||
		repository.scheduleCalls != 1 {
		t.Fatalf("first=%#v schedule_calls=%d", first, repository.scheduleCalls)
	}
	if repository.record.EventKey == "" || repository.record.OutboxID == "" || repository.record.Binding.SourceVersionID != profile.SourceVersionID {
		t.Fatalf("record=%#v", repository.record)
	}
	repository.replay = first
	replayed, err := service.RetryProfile(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !reflect.DeepEqual(replayed.View, first.View) || repository.scheduleCalls != 1 {
		t.Fatalf("replayed=%#v calls=%d", replayed, repository.scheduleCalls)
	}
}

func TestProfileRetryRejectsInvalidVersion(t *testing.T) {
	view := retryProfileFixture(t)
	profile := view.Profile
	service, err := NewProfileRetryService(ProfileRetryDependencies{
		Scheduler: &profileRetrySchedulerFake{view: view}, IDs: &sequenceIDs{}, Clock: foundation.FixedClock{Value: retryTestNow},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.RetryProfile(context.Background(), ProfileRetryCommand{
		WorkspaceID: profile.WorkspaceID, SourceVersionID: profile.SourceVersionID, IdempotencyKey: "profile-retry-invalid",
	})
	assertFoundationCode(t, err, "CAPTURE_PROFILE_RETRY_INVALID")
}

func TestProfileRetryRejectsMissingPreservedRevision(t *testing.T) {
	view := retryProfileFixture(t)
	view.Profile.Status = domain.ProfileStatusPending
	view.Profile.ErrorCode = ""
	view.Profile.Retryable = false
	view.Profile.Version++
	result := ProfileRetryResult{View: ProfileView{Profile: view.Profile}}
	if _, err := validateProfileRetryResult(result, view.Profile.WorkspaceID, view.Profile.SourceVersionID, false); err == nil {
		t.Fatal("profile retry accepted a current revision pointer without the revision payload")
	} else {
		assertFoundationCode(t, err, "CAPTURE_PROFILE_RETRY_RESULT_INVALID")
	}
}

type profileRetrySchedulerFake struct {
	view          ProfileView
	replay        ProfileRetryResult
	record        ProfileRetryRecord
	scheduleCalls int
}

func (repository *profileRetrySchedulerFake) ReplayProfileRetry(_ context.Context, _ ProfileRetryBinding) (ProfileRetryResult, bool, error) {
	if repository.replay.View.Profile.ID == "" {
		return ProfileRetryResult{}, false, nil
	}
	result := repository.replay
	result.Replayed = true
	return result, true, nil
}

func (repository *profileRetrySchedulerFake) ScheduleProfileRetry(_ context.Context, record ProfileRetryRecord) (ProfileRetryResult, error) {
	repository.scheduleCalls++
	repository.record = record
	view := repository.view
	profile := view.Profile
	profile.Status = domain.ProfileStatusPending
	profile.ErrorCode = ""
	profile.Retryable = false
	profile.Version++
	profile.UpdatedAt = retryTestNow
	view.Profile = profile
	return ProfileRetryResult{View: view}, nil
}

func retryProfileFixture(t *testing.T) ProfileView {
	t.Helper()
	spanID := foundation.ID("53000000-0000-4000-8000-000000000010")
	content := domain.ProfileContent{
		Summary:         "Profile summary",
		Topics:          []domain.ProfileCandidate{{Label: "Profile topic", SourceSpanIDs: []foundation.ID{spanID}}},
		KnowledgePoints: []domain.ProfilePoint{{Text: "Profile point", SourceSpanIDs: []foundation.ID{spanID}}},
	}
	digest, err := domain.ComputeProfileDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	profile := domain.Profile{
		ID: "53000000-0000-4000-8000-000000000002", WorkspaceID: captureTestWorkspaceID,
		CaptureID: "53000000-0000-4000-8000-000000000003", SourceVersionID: "53000000-0000-4000-8000-000000000004",
		CurrentRevisionID: "53000000-0000-4000-8000-000000000005",
		Status:            domain.ProfileStatusFailed, ErrorCode: "CAPTURE_PROFILE_GENERATION_FAILED", Retryable: true,
		Version: 3, CreatedAt: retryTestNow, UpdatedAt: retryTestNow,
	}
	revision := domain.ProfileRevision{
		ID: profile.CurrentRevisionID, ProfileID: profile.ID, WorkspaceID: profile.WorkspaceID, SourceVersionID: profile.SourceVersionID,
		ParseProjectionID: "53000000-0000-4000-8000-000000000006", IndexVersionID: "53000000-0000-4000-8000-000000000007",
		ModelRunID: "53000000-0000-4000-8000-000000000008", PromptVersion: "v1", SchemaVersion: domain.ProfileSchemaVersion,
		Content: content, ContentDigest: digest, CreatedAt: retryTestNow,
	}
	return ProfileView{Profile: profile, Revision: &revision, Evidence: []domain.ProfileEvidence{{
		RevisionID: revision.ID, WorkspaceID: profile.WorkspaceID, SourceVersionID: profile.SourceVersionID,
		SourceSpanID: spanID, CreatedAt: retryTestNow,
	}}}
}
