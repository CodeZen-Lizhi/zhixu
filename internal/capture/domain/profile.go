package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ProfileSchemaVersion identifies the first strict document knowledge profile contract.
const ProfileSchemaVersion = "document-knowledge-profile/v1"

// ProfileStatus is the lifecycle of a derived document knowledge profile.
type ProfileStatus string

const (
	// ProfileStatusPending has not started yet.
	ProfileStatusPending ProfileStatus = "PENDING"
	// ProfileStatusRunning is generating a new revision.
	ProfileStatusRunning ProfileStatus = "RUNNING"
	// ProfileStatusReady points at a validated immutable revision.
	ProfileStatusReady ProfileStatus = "READY"
	// ProfileStatusFailed records a stable generation failure.
	ProfileStatusFailed ProfileStatus = "FAILED"
	// ProfileStatusCapabilityUnavailable records an intentionally disabled model capability.
	ProfileStatusCapabilityUnavailable ProfileStatus = "CAPABILITY_UNAVAILABLE"
	// ProfileStatusStale means dependencies changed while the previous revision remains readable.
	ProfileStatusStale ProfileStatus = "STALE"
)

// Profile is the mutable pointer to the latest immutable document profile revision.
type Profile struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	CaptureID         foundation.ID
	SourceVersionID   foundation.ID
	CurrentRevisionID foundation.ID
	Status            ProfileStatus
	ErrorCode         string
	Retryable         bool
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Validate 校验画像状态、版本与当前 Revision 指针的生命周期组合。
func (profile Profile) Validate() error {
	if !validID(profile.ID) || !validID(profile.WorkspaceID) || !validID(profile.CaptureID) ||
		!validID(profile.SourceVersionID) || (profile.CurrentRevisionID != "" && !validID(profile.CurrentRevisionID)) ||
		!profile.Status.Valid() || profile.Version < 1 || profile.CreatedAt.IsZero() || profile.UpdatedAt.Before(profile.CreatedAt) {
		return profileInvalid("profile binding or lifecycle is invalid")
	}
	switch profile.Status {
	case ProfileStatusReady:
		if profile.CurrentRevisionID == "" || profile.ErrorCode != "" || profile.Retryable {
			return profileInvalid("ready profile state is invalid")
		}
	case ProfileStatusPending, ProfileStatusRunning:
		if profile.ErrorCode != "" || profile.Retryable {
			return profileInvalid("active profile state is invalid")
		}
	case ProfileStatusStale:
		if profile.CurrentRevisionID == "" || profile.ErrorCode != "" || profile.Retryable {
			return profileInvalid("stale profile state is invalid")
		}
	case ProfileStatusFailed, ProfileStatusCapabilityUnavailable:
		if !validProfileErrorCode(profile.ErrorCode) {
			return profileInvalid("failed profile error is invalid")
		}
	}
	return nil
}

// ProfileCandidate is a non-authoritative label backed by source evidence.
type ProfileCandidate struct {
	Label         string          `json:"label"`
	Aliases       []string        `json:"aliases,omitempty"`
	SourceSpanIDs []foundation.ID `json:"source_span_ids"`
}

// ProfilePoint is a candidate knowledge point or example backed by source evidence.
type ProfilePoint struct {
	Text          string          `json:"text"`
	SourceSpanIDs []foundation.ID `json:"source_span_ids"`
}

// ProfileContent is the strict derived candidate projection returned by the profile model.
type ProfileContent struct {
	Summary         string             `json:"summary"`
	Topics          []ProfileCandidate `json:"topics"`
	Terms           []ProfileCandidate `json:"terms"`
	KnowledgePoints []ProfilePoint     `json:"knowledge_points"`
	Examples        []ProfilePoint     `json:"examples"`
}

// ProfileRevision freezes one validated profile output and all producing contracts.
type ProfileRevision struct {
	ID                    foundation.ID
	ProfileID             foundation.ID
	WorkspaceID           foundation.ID
	SourceVersionID       foundation.ID
	ParseProjectionID     foundation.ID
	IndexVersionID        foundation.ID
	ModelRunID            foundation.ID
	ModelSettingsRevision *int64
	PromptVersion         string
	SchemaVersion         string
	Content               ProfileContent
	ContentDigest         string
	CreatedAt             time.Time
}

// ProfileEvidence binds an immutable revision to a validated Source Span.
type ProfileEvidence struct {
	RevisionID      foundation.ID
	WorkspaceID     foundation.ID
	SourceVersionID foundation.ID
	SourceSpanID    foundation.ID
	CreatedAt       time.Time
}

// ComputeProfileDigest returns a stable digest for a normalized Profile content value.
func ComputeProfileDigest(content ProfileContent) (string, error) {
	normalized, err := NormalizeProfileContent(content)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "PROFILE_ENCODING_FAILED", false, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Validate checks that an immutable Profile revision is complete and evidence-backed.
func (revision ProfileRevision) Validate() error {
	if !validID(revision.ID) || !validID(revision.ProfileID) || !validID(revision.WorkspaceID) ||
		!validID(revision.SourceVersionID) || !validID(revision.ParseProjectionID) ||
		!validID(revision.IndexVersionID) || !validID(revision.ModelRunID) ||
		(revision.ModelSettingsRevision != nil && *revision.ModelSettingsRevision < 1) ||
		revision.SchemaVersion != ProfileSchemaVersion ||
		!validRequiredText(revision.PromptVersion, 128) || !validHash(revision.ContentDigest) || revision.CreatedAt.IsZero() {
		return profileInvalid("profile revision binding is invalid")
	}
	digest, err := ComputeProfileDigest(revision.Content)
	if err != nil {
		return err
	}
	if digest != revision.ContentDigest {
		return profileInvalid("profile revision digest does not match content")
	}
	return nil
}

// NormalizeProfileContent 校验并规范化可持久化的派生画像内容。
func NormalizeProfileContent(content ProfileContent) (ProfileContent, error) {
	content.Summary = strings.TrimSpace(content.Summary)
	if content.Summary == "" || len(content.Summary) > 16*1024 {
		return ProfileContent{}, profileInvalid("profile summary is invalid")
	}
	if len(content.Topics) == 0 || len(content.KnowledgePoints) == 0 {
		return ProfileContent{}, profileInvalid("profile requires a topic and knowledge point")
	}
	var err error
	content.Topics, err = normalizeCandidates(content.Topics)
	if err != nil {
		return ProfileContent{}, err
	}
	content.Terms, err = normalizeCandidates(content.Terms)
	if err != nil {
		return ProfileContent{}, err
	}
	content.KnowledgePoints, err = normalizePoints(content.KnowledgePoints)
	if err != nil {
		return ProfileContent{}, err
	}
	content.Examples, err = normalizePoints(content.Examples)
	if err != nil {
		return ProfileContent{}, err
	}
	return content, nil
}

func normalizeCandidates(values []ProfileCandidate) ([]ProfileCandidate, error) {
	if len(values) > 128 {
		return nil, profileInvalid("profile candidate limit exceeded")
	}
	result := append([]ProfileCandidate(nil), values...)
	for index := range result {
		result[index].Label = strings.TrimSpace(result[index].Label)
		if result[index].Label == "" || len(result[index].Label) > 512 || len(result[index].Aliases) > 32 {
			return nil, profileInvalid("profile candidate is invalid")
		}
		aliases, err := normalizeStrings(result[index].Aliases)
		if err != nil {
			return nil, err
		}
		result[index].Aliases = aliases
		spans, err := normalizeSpanIDs(result[index].SourceSpanIDs)
		if err != nil {
			return nil, err
		}
		result[index].SourceSpanIDs = spans
	}
	return result, nil
}

func normalizePoints(values []ProfilePoint) ([]ProfilePoint, error) {
	if len(values) > 256 {
		return nil, profileInvalid("profile point limit exceeded")
	}
	result := append([]ProfilePoint(nil), values...)
	for index := range result {
		result[index].Text = strings.TrimSpace(result[index].Text)
		if result[index].Text == "" || len(result[index].Text) > 4096 {
			return nil, profileInvalid("profile point is invalid")
		}
		spans, err := normalizeSpanIDs(result[index].SourceSpanIDs)
		if err != nil {
			return nil, err
		}
		result[index].SourceSpanIDs = spans
	}
	return result, nil
}

func normalizeStrings(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
			return nil, profileInvalid("profile candidate alias is invalid")
		}
		if _, found := seen[value]; found {
			return nil, profileInvalid("profile candidate aliases are duplicated")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// Valid reports whether a document profile lifecycle status is supported.
func (status ProfileStatus) Valid() bool {
	switch status {
	case ProfileStatusPending, ProfileStatusRunning, ProfileStatusReady, ProfileStatusFailed,
		ProfileStatusCapabilityUnavailable, ProfileStatusStale:
		return true
	default:
		return false
	}
}

func normalizeSpanIDs(values []foundation.ID) ([]foundation.ID, error) {
	if len(values) == 0 || len(values) > 64 {
		return nil, profileInvalid("profile evidence is required")
	}
	seen := make(map[foundation.ID]struct{}, len(values))
	result := make([]foundation.ID, 0, len(values))
	for _, value := range values {
		if !validID(value) {
			return nil, profileInvalid("profile evidence id is invalid")
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func profileInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "PROFILE_INVALID", false, errors.New(message))
}

func validProfileErrorCode(value string) bool {
	return validRequiredText(value, 128) && !strings.ContainsAny(value, "\r\n\x00")
}
