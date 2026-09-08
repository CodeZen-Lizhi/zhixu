package postgres

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	profileQueryInvalidCode       = "CAPTURE_PROFILE_QUERY_INVALID"
	profileContextInvalidCode     = "CAPTURE_PROFILE_CONTEXT_INVALID"
	profileAttemptConflictCode    = "CAPTURE_PROFILE_ATTEMPT_CONFLICT"
	profileVersionConflictCode    = "CAPTURE_PROFILE_VERSION_CONFLICT"
	profilePersistenceFailedCode  = "CAPTURE_PROFILE_PERSISTENCE_FAILED"
	profileFinalizationFailedCode = "CAPTURE_PROFILE_FINALIZATION_FAILED"
)

func boundedProfileChunkContent(content sql.NullString) (string, error) {
	if !content.Valid || len(content.String) == 0 || len(content.String) > captureapp.MaxProfileChunkBytes ||
		!utf8.ValidString(content.String) {
		return "", inconsistent(profileContextInvalidCode, "profile chunk content is outside the supported boundary")
	}
	return content.String, nil
}

type profileAttemptRecord struct {
	ID                    foundation.ID
	ProfileID             foundation.ID
	WorkspaceID           foundation.ID
	SourceVersionID       foundation.ID
	ParseProjectionID     foundation.ID
	IndexVersionID        foundation.ID
	ModelSettingsRevision *int64
	PromptVersion         string
	SchemaVersion         string
	WorkflowRunID         foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	ModelRunID            foundation.ID
	AttemptNumber         int
	Status                string
	ErrorCode             string
	Retryable             bool
	StartedAt             time.Time
	CompletedAt           *time.Time
	Version               int64
}

const (
	profileAttemptRunning               = "RUNNING"
	profileAttemptSucceeded             = "SUCCEEDED"
	profileAttemptFailed                = "FAILED"
	profileAttemptCapabilityUnavailable = "CAPABILITY_UNAVAILABLE"
	profileAttemptReturning             = `id::text,profile_id::text,workspace_id::text,source_version_id::text,
		parse_projection_id::text,index_version_id::text,model_settings_revision,prompt_version,schema_version,
		workflow_run_id::text,node_run_id::text,node_attempt_id::text,model_run_id::text,
		attempt_number,status,error_code,retryable,started_at,completed_at,version`
)

func (attempt profileAttemptRecord) prepared() captureapp.PreparedProfile {
	return captureapp.PreparedProfile{
		ProfileID: attempt.ProfileID, ProfileAttemptID: attempt.ID, ProfileAttemptNumber: attempt.AttemptNumber,
		ProfileAttemptVersion: attempt.Version, StartedAt: attempt.StartedAt, ModelRunID: attempt.ModelRunID,
	}
}

func profileModelRunBindingDecision(attempt profileAttemptRecord, command captureapp.BindProfileModelRunCommand) (bool, error) {
	if attempt.Status != profileAttemptRunning {
		return false, foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, false,
			errors.New("profile attempt is already terminal"))
	}
	if attempt.Version == command.ExpectedProfileAttemptVersion {
		if attempt.ModelRunID == "" {
			return false, nil
		}
		if attempt.ModelRunID == command.ModelRunID {
			return true, nil
		}
	}
	if attempt.Version == command.ExpectedProfileAttemptVersion+1 && attempt.ModelRunID == command.ModelRunID {
		return true, nil
	}
	return false, foundation.NewError(foundation.ErrorVersionConflict, profileVersionConflictCode, true,
		errors.New("profile attempt changed before model run binding"))
}

func scanProfileAttempt(row rowScanner) (profileAttemptRecord, error) {
	var attempt profileAttemptRecord
	var id, profileID, workspaceID, sourceVersionID, parseProjectionID, indexVersionID string
	var workflowRunID, nodeRunID, nodeAttemptID string
	var modelSettings sql.NullInt64
	var modelRunID sql.NullString
	if err := row.Scan(&id, &profileID, &workspaceID, &sourceVersionID, &parseProjectionID, &indexVersionID,
		&modelSettings, &attempt.PromptVersion, &attempt.SchemaVersion, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&modelRunID, &attempt.AttemptNumber, &attempt.Status, &attempt.ErrorCode, &attempt.Retryable,
		&attempt.StartedAt, &attempt.CompletedAt, &attempt.Version); err != nil {
		return profileAttemptRecord{}, err
	}
	bindings := []struct {
		raw    string
		target *foundation.ID
	}{
		{id, &attempt.ID}, {profileID, &attempt.ProfileID}, {workspaceID, &attempt.WorkspaceID},
		{sourceVersionID, &attempt.SourceVersionID}, {parseProjectionID, &attempt.ParseProjectionID},
		{indexVersionID, &attempt.IndexVersionID}, {workflowRunID, &attempt.WorkflowRunID},
		{nodeRunID, &attempt.NodeRunID}, {nodeAttemptID, &attempt.NodeAttemptID},
	}
	for _, binding := range bindings {
		parsed, err := foundation.ParseID(binding.raw)
		if err != nil {
			return profileAttemptRecord{}, err
		}
		*binding.target = parsed
	}
	if modelSettings.Valid {
		value := modelSettings.Int64
		attempt.ModelSettingsRevision = &value
	}
	if modelRunID.Valid {
		parsed, err := foundation.ParseID(modelRunID.String)
		if err != nil {
			return profileAttemptRecord{}, err
		}
		attempt.ModelRunID = parsed
	}
	if !validProfileContract(captureapp.ProfileContract{
		ParseProjectionID: attempt.ParseProjectionID, IndexVersionID: attempt.IndexVersionID,
		ModelSettingsRevision: attempt.ModelSettingsRevision, PromptVersion: attempt.PromptVersion,
		SchemaVersion: attempt.SchemaVersion,
	}) || attempt.AttemptNumber < 1 || attempt.Version < 1 || attempt.StartedAt.IsZero() ||
		!validProfileAttemptState(attempt) {
		return profileAttemptRecord{}, inconsistent(profileContextInvalidCode, "persisted profile attempt is invalid")
	}
	return attempt, nil
}

func validProfileAttemptState(attempt profileAttemptRecord) bool {
	switch attempt.Status {
	case profileAttemptRunning:
		return attempt.CompletedAt == nil && attempt.ErrorCode == "" && !attempt.Retryable
	case profileAttemptSucceeded:
		return attempt.CompletedAt != nil && attempt.ErrorCode == "" && !attempt.Retryable && attempt.ModelRunID != ""
	case profileAttemptCapabilityUnavailable:
		return attempt.CompletedAt != nil && strictProfileErrorCode(attempt.ErrorCode) && !attempt.Retryable && attempt.ModelRunID == ""
	case profileAttemptFailed, "CANCELLED":
		return attempt.CompletedAt != nil && strictProfileErrorCode(attempt.ErrorCode)
	default:
		return false
	}
}

func replayPreparedAttempt(attempt profileAttemptRecord, command captureapp.PrepareProfileCommand) (captureapp.PreparedProfile, error) {
	if attempt.WorkspaceID != command.WorkspaceID || attempt.SourceVersionID != command.SourceVersionID ||
		attempt.WorkflowRunID != command.WorkflowRunID || attempt.NodeRunID != command.NodeRunID ||
		attempt.NodeAttemptID != command.NodeAttemptID || !sameProfileContract(attempt, command.Contract) {
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileAttemptConflictCode, false,
			errors.New("profile node attempt is bound to another frozen request"))
	}
	switch attempt.Status {
	case profileAttemptRunning:
		return attempt.prepared(), nil
	case profileAttemptCapabilityUnavailable:
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorDependencyUnavailable, attempt.ErrorCode, false,
			errors.New("profile capability is unavailable for this node attempt"))
	case profileAttemptFailed:
		kind := foundation.ErrorNonRetryableFailure
		if attempt.Retryable {
			kind = foundation.ErrorRetryableFailure
		}
		return captureapp.PreparedProfile{}, foundation.NewError(kind, attempt.ErrorCode, attempt.Retryable,
			errors.New("profile generation already failed for this node attempt"))
	default:
		return captureapp.PreparedProfile{}, foundation.NewError(foundation.ErrorVersionConflict, profileAttemptConflictCode, false,
			fmt.Errorf("profile node attempt is already %s", attempt.Status))
	}
}

func validateRunAttemptBinding(run agentdomain.ModelRun, attempt profileAttemptRecord) error {
	if agentdomain.ValidateModelRun(run) != nil || run.Status != agentdomain.ModelRunRunning || !modelRunMatchesAttempt(run, attempt) {
		return inconsistent(profileContextInvalidCode, "model run differs from the frozen profile attempt")
	}
	return nil
}

func modelRunMatchesAttempt(run agentdomain.ModelRun, attempt profileAttemptRecord) bool {
	return run.WorkspaceID == attempt.WorkspaceID && run.WorkflowRunID == attempt.WorkflowRunID &&
		run.NodeRunID == attempt.NodeRunID && run.NodeAttemptID == attempt.NodeAttemptID &&
		run.Retrieval.IndexVersionID == attempt.IndexVersionID &&
		sameNullableRevision(run.ModelSettingsRevision, attempt.ModelSettingsRevision) &&
		run.Prompt.Version == attempt.PromptVersion
}

func validateSuccessfulRun(record agentapp.ModelRunRecord, attempt profileAttemptRecord, expectedVersion int64, completedAt time.Time) error {
	if err := validateRunAttemptBinding(record.Run, attempt); err != nil {
		return err
	}
	if expectedVersion < 1 || record.Run.Version != expectedVersion || len(record.Calls) == 0 || len(record.Calls) > 3 ||
		completedAt.IsZero() || completedAt.Before(record.Run.UpdatedAt) {
		return inconsistent(profileContextInvalidCode, "profile model run is incomplete or changed")
	}
	succeeded := false
	for index, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != record.Run.ID || call.CallNo != index+1 ||
			call.Status == agentdomain.ModelCallStarted || call.Status == agentdomain.ModelCallUnknown {
			return inconsistent(profileContextInvalidCode, "profile model call history is incomplete")
		}
		if call.CompletedAt == nil || completedAt.Before(*call.CompletedAt) {
			return inconsistent(profileContextInvalidCode, "profile completion precedes a model call")
		}
		succeeded = succeeded || call.Status == agentdomain.ModelCallSucceeded
	}
	if !succeeded || record.Calls[len(record.Calls)-1].Status != agentdomain.ModelCallSucceeded {
		return inconsistent(profileContextInvalidCode, "profile model run has no final successful call")
	}
	return nil
}

func validateRevisionAttemptBinding(revision domain.ProfileRevision, attempt profileAttemptRecord) error {
	if revision.Validate() != nil || revision.ProfileID != attempt.ProfileID || revision.WorkspaceID != attempt.WorkspaceID ||
		revision.SourceVersionID != attempt.SourceVersionID || revision.ParseProjectionID != attempt.ParseProjectionID ||
		revision.IndexVersionID != attempt.IndexVersionID || revision.ModelRunID != attempt.ModelRunID ||
		!sameNullableRevision(revision.ModelSettingsRevision, attempt.ModelSettingsRevision) ||
		revision.PromptVersion != attempt.PromptVersion || revision.SchemaVersion != attempt.SchemaVersion {
		return inconsistent(profileContextInvalidCode, "profile revision differs from its frozen attempt")
	}
	return nil
}

func validateCompleteProfile(command captureapp.CompleteProfileCommand) ([]foundation.ID, error) {
	if !validID(command.WorkspaceID) || !validID(command.ProfileAttemptID) ||
		command.ExpectedProfileAttemptVersion < 1 || command.ExpectedModelRunVersion < 1 ||
		command.Revision.Validate() != nil || command.Revision.WorkspaceID != command.WorkspaceID ||
		command.CompletedAt.IsZero() || !command.CompletedAt.Equal(command.Revision.CreatedAt) ||
		len(command.Revision.Content.Topics) == 0 || len(command.Revision.Content.KnowledgePoints) == 0 ||
		len(command.Evidence) == 0 || len(command.Evidence) > captureapp.MaxProfileSourceChunks {
		return nil, invalid(profileContextInvalidCode, "profile completion command is invalid")
	}
	wanted := profileContentEvidence(command.Revision.Content)
	actual := make([]foundation.ID, len(command.Evidence))
	seen := make(map[foundation.ID]struct{}, len(command.Evidence))
	for index, evidence := range command.Evidence {
		if evidence.RevisionID != command.Revision.ID || evidence.WorkspaceID != command.WorkspaceID ||
			evidence.SourceVersionID != command.Revision.SourceVersionID || !validID(evidence.SourceSpanID) ||
			evidence.CreatedAt.IsZero() || !evidence.CreatedAt.Equal(command.CompletedAt) {
			return nil, invalid(profileContextInvalidCode, "profile evidence binding is invalid")
		}
		if _, duplicate := seen[evidence.SourceSpanID]; duplicate {
			return nil, invalid(profileContextInvalidCode, "profile evidence contains duplicate spans")
		}
		seen[evidence.SourceSpanID] = struct{}{}
		actual[index] = evidence.SourceSpanID
	}
	sort.Slice(actual, func(left, right int) bool { return actual[left] < actual[right] })
	if len(actual) == 0 || !sameFoundationIDs(wanted, actual) {
		return nil, invalid(profileContextInvalidCode, "profile evidence does not exactly cover the content")
	}
	return actual, nil
}

func validateFailProfile(command captureapp.FailProfileCommand) error {
	if !validID(command.WorkspaceID) || !validID(command.ProfileID) || !validID(command.ProfileAttemptID) ||
		command.ExpectedProfileAttemptVersion < 1 || !strictProfileErrorCode(command.ErrorCode) || command.CompletedAt.IsZero() ||
		(command.ProfileStatus != domain.ProfileStatusFailed && command.ProfileStatus != domain.ProfileStatusCapabilityUnavailable) {
		return errors.New("profile failure command is invalid")
	}
	if command.ProfileStatus == domain.ProfileStatusCapabilityUnavailable {
		if command.Retryable || command.ModelRunID != "" || command.ExpectedModelRunVersion != 0 || command.ModelRunStatus != "" {
			return errors.New("unavailable profile cannot bind a model run")
		}
		return nil
	}
	if command.ModelRunID == "" {
		if command.ExpectedModelRunVersion != 0 || command.ModelRunStatus != "" {
			return errors.New("unbound profile failure contains model run state")
		}
		return nil
	}
	if !validID(command.ModelRunID) || command.ExpectedModelRunVersion < 1 ||
		(command.ModelRunStatus != agentdomain.ModelRunFailed && command.ModelRunStatus != agentdomain.ModelRunUnknown) {
		return errors.New("profile failure model run state is invalid")
	}
	return nil
}

func scanProfileRevision(row rowScanner) (domain.ProfileRevision, error) {
	var revision domain.ProfileRevision
	var id, profileID, storedWorkspaceID, sourceVersionID, parseProjectionID, indexVersionID, modelRunID string
	var modelSettings sql.NullInt64
	var contentRaw []byte
	if err := row.Scan(&id, &profileID, &storedWorkspaceID, &sourceVersionID, &parseProjectionID, &indexVersionID,
		&modelRunID, &modelSettings, &revision.PromptVersion, &revision.SchemaVersion, &contentRaw,
		&revision.ContentDigest, &revision.CreatedAt); err != nil {
		return domain.ProfileRevision{}, err
	}
	bindings := []struct {
		raw    string
		target *foundation.ID
	}{
		{id, &revision.ID}, {profileID, &revision.ProfileID}, {storedWorkspaceID, &revision.WorkspaceID},
		{sourceVersionID, &revision.SourceVersionID}, {parseProjectionID, &revision.ParseProjectionID},
		{indexVersionID, &revision.IndexVersionID}, {modelRunID, &revision.ModelRunID},
	}
	for _, binding := range bindings {
		parsed, parseErr := foundation.ParseID(binding.raw)
		if parseErr != nil {
			return domain.ProfileRevision{}, parseErr
		}
		*binding.target = parsed
	}
	if modelSettings.Valid {
		value := modelSettings.Int64
		revision.ModelSettingsRevision = &value
	}
	content, err := decodeProfileContent(contentRaw)
	if err != nil {
		return domain.ProfileRevision{}, err
	}
	revision.Content = content
	if err := revision.Validate(); err != nil {
		return domain.ProfileRevision{}, err
	}
	return revision, nil
}

func decodeHeadingPath(raw []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var headings []string
	if err := decoder.Decode(&headings); err != nil || headings == nil {
		return nil, inconsistent(profileContextInvalidCode, "profile chunk heading path is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, inconsistent(profileContextInvalidCode, "profile chunk heading path contains trailing JSON")
	}
	for _, heading := range headings {
		if heading == "" || strings.TrimSpace(heading) != heading || !utf8.ValidString(heading) || len(heading) > 512 {
			return nil, inconsistent(profileContextInvalidCode, "profile chunk heading is invalid")
		}
	}
	return headings, nil
}

func decodeProfileContent(raw []byte) (domain.ProfileContent, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var content domain.ProfileContent
	if err := decoder.Decode(&content); err != nil {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "profile content is not strict JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "profile content contains trailing JSON")
	}
	normalized, err := domain.NormalizeProfileContent(content)
	if err != nil {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "profile content is invalid")
	}
	if len(normalized.Topics) == 0 || len(normalized.KnowledgePoints) == 0 {
		return domain.ProfileContent{}, inconsistent(profileContextInvalidCode, "ready profile content is incomplete")
	}
	return normalized, nil
}

func validProfileLookup(lookup captureapp.ProfileLookup) bool {
	return validID(lookup.WorkspaceID) && validID(lookup.CaptureID) && validID(lookup.SourceVersionID)
}

func validProfileContract(contract captureapp.ProfileContract) bool {
	return validID(contract.ParseProjectionID) && validID(contract.IndexVersionID) &&
		(contract.ModelSettingsRevision == nil || *contract.ModelSettingsRevision > 0) &&
		contract.PromptVersion != "" && strings.TrimSpace(contract.PromptVersion) == contract.PromptVersion &&
		len(contract.PromptVersion) <= 128 && contract.SchemaVersion == domain.ProfileSchemaVersion
}

func validPrepareProfile(command captureapp.PrepareProfileCommand) bool {
	return validProfileLookup(command.ProfileLookup) && validProfileContract(command.Contract) &&
		validID(command.RequestedProfileID) && validID(command.ProfileAttemptID) && validID(command.WorkflowRunID) &&
		validID(command.NodeRunID) && validID(command.NodeAttemptID) && !command.StartedAt.IsZero()
}

func sameProfileContract(attempt profileAttemptRecord, contract captureapp.ProfileContract) bool {
	return attempt.ParseProjectionID == contract.ParseProjectionID && attempt.IndexVersionID == contract.IndexVersionID &&
		sameNullableRevision(attempt.ModelSettingsRevision, contract.ModelSettingsRevision) &&
		attempt.PromptVersion == contract.PromptVersion && attempt.SchemaVersion == contract.SchemaVersion
}

func sameNullableRevision(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func nullableProfileRevision(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func profileContentEvidence(content domain.ProfileContent) []foundation.ID {
	set := make(map[foundation.ID]struct{})
	for _, candidate := range append(append([]domain.ProfileCandidate(nil), content.Topics...), content.Terms...) {
		for _, id := range candidate.SourceSpanIDs {
			set[id] = struct{}{}
		}
	}
	for _, point := range append(append([]domain.ProfilePoint(nil), content.KnowledgePoints...), content.Examples...) {
		for _, id := range point.SourceSpanIDs {
			set[id] = struct{}{}
		}
	}
	values := make([]foundation.ID, 0, len(set))
	for id := range set {
		values = append(values, id)
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return values
}

func evidenceSpanIDs(values []domain.ProfileEvidence) []foundation.ID {
	result := make([]foundation.ID, len(values))
	for index, value := range values {
		result[index] = value.SourceSpanID
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func sameFoundationIDs(left, right []foundation.ID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameProfileRevision(left, right domain.ProfileRevision) bool {
	return left.ID == right.ID && left.ProfileID == right.ProfileID && left.WorkspaceID == right.WorkspaceID &&
		left.SourceVersionID == right.SourceVersionID && left.ParseProjectionID == right.ParseProjectionID &&
		left.IndexVersionID == right.IndexVersionID && left.ModelRunID == right.ModelRunID &&
		sameNullableRevision(left.ModelSettingsRevision, right.ModelSettingsRevision) &&
		left.PromptVersion == right.PromptVersion && left.SchemaVersion == right.SchemaVersion &&
		left.ContentDigest == right.ContentDigest && reflect.DeepEqual(left.Content, right.Content) &&
		samePersistedTime(left.CreatedAt, right.CreatedAt)
}

func exactFailedProfileReplay(attempt profileAttemptRecord, profile domain.Profile, command captureapp.FailProfileCommand, wantedStatus string) bool {
	return attempt.Status == wantedStatus && attempt.ErrorCode == command.ErrorCode &&
		attempt.Retryable == command.Retryable && attempt.ModelRunID == command.ModelRunID &&
		profile.Status == command.ProfileStatus && profile.ErrorCode == command.ErrorCode &&
		profile.Retryable == command.Retryable && samePersistedTime(profile.UpdatedAt, command.CompletedAt) &&
		profileTerminalAttemptReplay(attempt, command.ExpectedProfileAttemptVersion, command.CompletedAt)
}

func profileTerminalAttemptReplay(attempt profileAttemptRecord, expectedVersion int64, completedAt time.Time) bool {
	return attempt.Version == expectedVersion+1 && attempt.CompletedAt != nil &&
		samePersistedTime(*attempt.CompletedAt, completedAt)
}

func samePersistedTime(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func strictProfileErrorCode(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func nilProfilePort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}
