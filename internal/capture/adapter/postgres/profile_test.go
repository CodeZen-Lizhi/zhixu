package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var profileRepositoryTestNow = time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)

func TestNewProfileRepositoryRequiresDatabaseAndAgentFinalizer(t *testing.T) {
	finalizer := profileFinalizerFake{}
	if _, err := NewProfileRepository(nil, finalizer); profileRepositoryErrorCode(err) != "CAPTURE_PROFILE_REPOSITORY_UNAVAILABLE" {
		t.Fatalf("NewProfileRepository(nil) error = %#v", err)
	}
	if _, err := NewProfileRepository(&pgxpool.Pool{}, nil); profileRepositoryErrorCode(err) != "CAPTURE_PROFILE_REPOSITORY_UNAVAILABLE" {
		t.Fatalf("NewProfileRepository(nil finalizer) error = %#v", err)
	}
	if repository, err := NewProfileRepository(&pgxpool.Pool{}, finalizer); err != nil || repository == nil {
		t.Fatalf("NewProfileRepository() = %#v, %v", repository, err)
	}
}

func TestGetProfilesRejectsInvalidBatchBeforeDatabase(t *testing.T) {
	repository := &ProfileRepository{db: &pgxpool.Pool{}}
	workspaceID := profileRepositoryTestID(1)
	validSourceID := profileRepositoryTestID(2)
	tests := map[string]captureapp.ProfileBatchQuery{
		"empty":     {WorkspaceID: workspaceID},
		"duplicate": {WorkspaceID: workspaceID, SourceVersionIDs: []foundation.ID{validSourceID, validSourceID}},
		"invalid id": {WorkspaceID: workspaceID, SourceVersionIDs: []foundation.ID{
			"not-a-uuid",
		}},
		"oversized": {WorkspaceID: workspaceID, SourceVersionIDs: func() []foundation.ID {
			values := make([]foundation.ID, captureapp.MaxProfileBatchSize+1)
			for index := range values {
				values[index] = profileRepositoryTestID(index + 100)
			}
			return values
		}()},
	}
	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := repository.GetProfiles(context.Background(), query); profileRepositoryErrorCode(err) != profileQueryInvalidCode {
				t.Fatalf("GetProfiles() error = %#v", err)
			}
		})
	}
}

func TestValidateProfileCompletionRequiresExactEvidenceClosure(t *testing.T) {
	command := validProfileCompletionCommand(t)
	if _, err := validateCompleteProfile(command); err != nil {
		t.Fatalf("validateProfileCompletion() error = %v", err)
	}

	tests := map[string]func(*captureapp.CompleteProfileCommand){
		"empty": func(command *captureapp.CompleteProfileCommand) { command.Evidence = nil },
		"foreign revision": func(command *captureapp.CompleteProfileCommand) {
			command.Evidence[0].RevisionID = profileRepositoryTestID(90)
		},
		"foreign workspace": func(command *captureapp.CompleteProfileCommand) {
			command.Evidence[0].WorkspaceID = profileRepositoryTestID(91)
		},
		"duplicate": func(command *captureapp.CompleteProfileCommand) {
			command.Evidence = append(command.Evidence, command.Evidence[0])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := validProfileCompletionCommand(t)
			mutate(&candidate)
			if _, err := validateCompleteProfile(candidate); profileRepositoryErrorCode(err) != profileContextInvalidCode {
				t.Fatalf("validateProfileCompletion() error = %#v", err)
			}
		})
	}
}

func TestValidateProfileFailureSeparatesBoundAndUnboundModelRuns(t *testing.T) {
	command := captureapp.FailProfileCommand{
		WorkspaceID: profileRepositoryTestID(1), ProfileID: profileRepositoryTestID(2),
		ProfileAttemptID: profileRepositoryTestID(3), ExpectedProfileAttemptVersion: 1,
		ProfileStatus: domain.ProfileStatusCapabilityUnavailable, ErrorCode: "CAPTURE_PROFILE_CAPABILITY_UNAVAILABLE",
		CompletedAt: profileRepositoryTestNow,
	}
	if err := validateFailProfile(command); err != nil {
		t.Fatalf("validateProfileFailure(unbound) error = %v", err)
	}
	command.ModelRunID = profileRepositoryTestID(4)
	command.ExpectedModelRunVersion = 1
	command.ProfileStatus = domain.ProfileStatusFailed
	if err := validateFailProfile(command); err == nil {
		t.Fatalf("validateProfileFailure(missing status) error = %#v", err)
	}
	command.ModelRunStatus = agentdomain.ModelRunUnknown
	if err := validateFailProfile(command); err != nil {
		t.Fatalf("validateProfileFailure(bound) error = %v", err)
	}
}

func TestDecodeStrictJSONRejectsUnknownProfileContentField(t *testing.T) {
	raw := []byte(`{"summary":"summary","topics":[{"label":"topic","source_span_ids":["94000000-0000-4000-8000-000000000010"]}],"terms":[],"knowledge_points":[{"text":"point","source_span_ids":["94000000-0000-4000-8000-000000000010"]}],"examples":[],"source_id":"94000000-0000-4000-8000-000000000001"}`)
	_, err := decodeProfileContent(raw)
	if profileRepositoryErrorCode(err) != profileContextInvalidCode {
		t.Fatalf("decodeProfileContent() error = %#v", err)
	}
}

func TestBoundedProfileChunkContentRejectsMissingAndOversizedValues(t *testing.T) {
	if content, err := boundedProfileChunkContent(sql.NullString{String: "bounded profile evidence", Valid: true}); err != nil || content != "bounded profile evidence" {
		t.Fatalf("boundedProfileChunkContent(valid) = %q, %v", content, err)
	}
	for name, content := range map[string]sql.NullString{
		"missing":   {},
		"empty":     {Valid: true},
		"oversized": {String: strings.Repeat("x", captureapp.MaxProfileChunkBytes+1), Valid: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := boundedProfileChunkContent(content); profileRepositoryErrorCode(err) != profileContextInvalidCode {
				t.Fatalf("boundedProfileChunkContent() error = %#v", err)
			}
		})
	}
}

func TestProfileModelRunBindingAllowsOnlyCurrentOrExactResponseLossReplay(t *testing.T) {
	command := captureapp.BindProfileModelRunCommand{
		ExpectedProfileAttemptVersion: 2, ModelRunID: profileRepositoryTestID(8),
	}
	attempt := profileAttemptRecord{Status: profileAttemptRunning, Version: 2}
	if bound, err := profileModelRunBindingDecision(attempt, command); err != nil || bound {
		t.Fatalf("new binding decision = %v, %v", bound, err)
	}
	attempt.ModelRunID = command.ModelRunID
	if bound, err := profileModelRunBindingDecision(attempt, command); err != nil || !bound {
		t.Fatalf("current binding replay = %v, %v", bound, err)
	}
	attempt.Version++
	if bound, err := profileModelRunBindingDecision(attempt, command); err != nil || !bound {
		t.Fatalf("response-loss replay = %v, %v", bound, err)
	}
	attempt.Version++
	if _, err := profileModelRunBindingDecision(attempt, command); profileRepositoryErrorCode(err) != profileVersionConflictCode {
		t.Fatalf("stale replay error = %#v", err)
	}
	attempt.Version = command.ExpectedProfileAttemptVersion
	attempt.ModelRunID = profileRepositoryTestID(9)
	if _, err := profileModelRunBindingDecision(attempt, command); profileRepositoryErrorCode(err) != profileVersionConflictCode {
		t.Fatalf("foreign binding error = %#v", err)
	}
}

func TestProfileTerminalFailureReplayRequiresExactDurableResult(t *testing.T) {
	completedAt := profileRepositoryTestNow.Add(123 * time.Nanosecond)
	attemptCompletedAt := completedAt.Truncate(time.Microsecond)
	command := captureapp.FailProfileCommand{
		ExpectedProfileAttemptVersion: 2, ProfileStatus: domain.ProfileStatusFailed,
		ErrorCode: "CAPTURE_PROFILE_GENERATION_FAILED", Retryable: true, CompletedAt: completedAt,
	}
	attempt := profileAttemptRecord{
		Status: profileAttemptFailed, ErrorCode: command.ErrorCode, Retryable: true,
		CompletedAt: &attemptCompletedAt, Version: 3,
	}
	profile := domain.Profile{
		Status: domain.ProfileStatusFailed, ErrorCode: command.ErrorCode, Retryable: true,
		UpdatedAt: attemptCompletedAt,
	}
	if !exactFailedProfileReplay(attempt, profile, command, profileAttemptFailed) {
		t.Fatal("exact persisted failure was not accepted as a replay")
	}
	changedAttempt := attempt
	changedAt := attemptCompletedAt.Add(time.Second)
	changedAttempt.CompletedAt = &changedAt
	if exactFailedProfileReplay(changedAttempt, profile, command, profileAttemptFailed) {
		t.Fatal("failure replay accepted a different completion time")
	}
	profile.UpdatedAt = profile.UpdatedAt.Add(time.Second)
	if exactFailedProfileReplay(attempt, profile, command, profileAttemptFailed) {
		t.Fatal("failure replay accepted a later mutable profile state")
	}
}

func TestProfileCASNoRowsIsVersionConflict(t *testing.T) {
	err := profileCASFailure(pgx.ErrNoRows, profileVersionConflictCode, "profile changed")
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict || !classified.Retryable {
		t.Fatalf("CAS no-row error = %#v", err)
	}
}

func validProfileCompletionCommand(t *testing.T) captureapp.CompleteProfileCommand {
	t.Helper()
	spanID := profileRepositoryTestID(10)
	content := domain.ProfileContent{
		Summary: "Java AI summary",
		Topics: []domain.ProfileCandidate{{
			Label: "Java AI", SourceSpanIDs: []foundation.ID{spanID},
		}},
		KnowledgePoints: []domain.ProfilePoint{{
			Text: "Spring AI integrates model providers.", SourceSpanIDs: []foundation.ID{spanID},
		}},
	}
	digest, err := domain.ComputeProfileDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.ProfileRevision{
		ID: profileRepositoryTestID(4), ProfileID: profileRepositoryTestID(3), WorkspaceID: profileRepositoryTestID(1),
		SourceVersionID: profileRepositoryTestID(5), ParseProjectionID: profileRepositoryTestID(6),
		IndexVersionID: profileRepositoryTestID(7), ModelRunID: profileRepositoryTestID(8), PromptVersion: "v1",
		SchemaVersion: domain.ProfileSchemaVersion, Content: content, ContentDigest: digest, CreatedAt: profileRepositoryTestNow,
	}
	return captureapp.CompleteProfileCommand{
		WorkspaceID: revision.WorkspaceID, ProfileAttemptID: profileRepositoryTestID(9),
		ExpectedProfileAttemptVersion: 2, ExpectedModelRunVersion: 1, Revision: revision,
		Evidence: []domain.ProfileEvidence{{
			RevisionID: revision.ID, WorkspaceID: revision.WorkspaceID, SourceVersionID: revision.SourceVersionID,
			SourceSpanID: spanID, CreatedAt: revision.CreatedAt,
		}},
		CompletedAt: revision.CreatedAt,
	}
}

type profileFinalizerFake struct{}

func (profileFinalizerFake) GetModelRunTx(context.Context, any, foundation.ID, foundation.ID, bool) (agentdomain.ModelRun, error) {
	return agentdomain.ModelRun{}, errors.New("unused")
}

func (profileFinalizerFake) GetModelRunRecordTx(context.Context, any, foundation.ID, foundation.ID, bool) (agentapp.ModelRunRecord, error) {
	return agentapp.ModelRunRecord{}, errors.New("unused")
}

func (profileFinalizerFake) GetModelRunByAttemptTx(context.Context, any, foundation.ID, foundation.ID, bool) (agentdomain.ModelRun, bool, error) {
	return agentdomain.ModelRun{}, false, errors.New("unused")
}

func (profileFinalizerFake) FinalizeModelRunTx(context.Context, any, agentapp.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	return agentdomain.ModelRun{}, false, errors.New("unused")
}

func profileRepositoryTestID(value int) foundation.ID {
	id, err := foundation.ParseID(fmt.Sprintf("94000000-0000-4000-8000-%012d", value))
	if err != nil {
		panic(err)
	}
	return id
}

func profileRepositoryErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

var _ agentapp.ModelRunTxFinalizer = profileFinalizerFake{}
