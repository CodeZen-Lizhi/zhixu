package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// SchemaVersion is the durable Memory aggregate schema version.
	SchemaVersion = "memory/v1"
	// MaxContentBytes bounds one user-controlled Memory content document.
	MaxContentBytes = 16 * 1024
	// MaxSourceRefBytes bounds an opaque source reference without storing source text.
	MaxSourceRefBytes = 512
	// MaxIdempotencyKeyBytes bounds command idempotency keys.
	MaxIdempotencyKeyBytes = 128
	// MaxListLimit bounds Memory list and effective-context reads.
	MaxListLimit = 100
	// SingleUserOwnerID 是当前单用户认证模型下稳定的 Memory 所有者标识。
	SingleUserOwnerID foundation.ID = "00000000-0000-5000-8000-000000000001"
)

const (
	// PrincipalUser 表示独立于 Session/API Token 生命周期的稳定用户主体。
	PrincipalUser authdomain.PrincipalKind = "USER"
)

// Type classifies Memory without turning it into a Knowledge or Citation fact.
type Type string

const (
	// TypePreference stores a user preference.
	TypePreference Type = "PREFERENCE"
	// TypeEpisodic stores short-lived contextual Memory and always requires expiry.
	TypeEpisodic Type = "EPISODIC"
	// TypeGoal stores a user goal.
	TypeGoal Type = "GOAL"
	// TypeFeedback stores user-authorized feedback.
	TypeFeedback Type = "FEEDBACK"
)

// Status is the only Memory lifecycle state machine.
type Status string

const (
	// StatusCandidate is a suggestion and is never effective context.
	StatusCandidate Status = "CANDIDATE"
	// StatusActive is confirmed Memory eligible for effective-context filtering.
	StatusActive Status = "ACTIVE"
	// StatusPaused is confirmed Memory intentionally excluded from context.
	StatusPaused Status = "PAUSED"
	// StatusExpired is retained history that is excluded from context.
	StatusExpired Status = "EXPIRED"
	// StatusDeleted is a retained tombstone and is excluded from context.
	StatusDeleted Status = "DELETED"
)

// SourceType records a candidate's bounded, immutable origin category.
type SourceType string

const (
	// SourceUser records a user-originated candidate.
	SourceUser SourceType = "USER"
	// SourceAgent records an agent suggestion; it still requires user confirmation.
	SourceAgent SourceType = "AGENT"
	// SourceInterview records an interview-derived suggestion; it still requires user confirmation.
	SourceInterview SourceType = "INTERVIEW"
)

// AuditAction identifies an append-only Memory lifecycle fact.
type AuditAction string

const (
	// AuditCandidateCreated records candidate persistence.
	AuditCandidateCreated AuditAction = "CANDIDATE_CREATED"
	// AuditConfirmed records the only transition into ACTIVE.
	AuditConfirmed AuditAction = "CONFIRMED"
	// AuditUpdated records a content, scope, or expiry edit.
	AuditUpdated AuditAction = "UPDATED"
	// AuditPaused records an owner pause.
	AuditPaused AuditAction = "PAUSED"
	// AuditResumed records an owner resume.
	AuditResumed AuditAction = "RESUMED"
	// AuditExpired records a maintenance expiry transition.
	AuditExpired AuditAction = "EXPIRED"
	// AuditDeleted records a tombstone transition.
	AuditDeleted AuditAction = "DELETED"
)

// Principal 是 Memory 所有权与确认审计使用的稳定用户主体。
type Principal struct {
	Kind authdomain.PrincipalKind `json:"kind"`
	ID   foundation.ID            `json:"id"`
}

// SingleUserOwner 返回当前部署模型下与凭据轮换无关的稳定用户主体。
func SingleUserOwner() Principal {
	return Principal{Kind: PrincipalUser, ID: SingleUserOwnerID}
}

// OwnerForAuthenticatedPrincipal 将已认证凭据映射为当前单用户的稳定 Memory owner。
func OwnerForAuthenticatedPrincipal(principal authdomain.Principal) (Principal, error) {
	if err := authdomain.ValidatePrincipal(principal); err != nil {
		return Principal{}, invalid("authenticated memory principal is invalid")
	}
	return SingleUserOwner(), nil
}

// Source is an opaque, bounded source reference. It is never a Citation.
type Source struct {
	Type SourceType `json:"type"`
	Ref  string     `json:"ref"`
}

// Memory is the authoritative aggregate. Candidate records are intentionally
// represented by the same aggregate but cannot be effective until confirmed.
type Memory struct {
	ID          foundation.ID   `json:"id"`
	WorkspaceID foundation.ID   `json:"workspace_id"`
	Owner       Principal       `json:"owner"`
	Type        Type            `json:"type"`
	Content     json.RawMessage `json:"content"`
	Source      Source          `json:"source"`
	TaskScopeID *foundation.ID  `json:"task_scope_id,omitempty"`
	Status      Status          `json:"status"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
	ConfirmedAt *time.Time      `json:"confirmed_at,omitempty"`
	ConfirmedBy *Principal      `json:"confirmed_by,omitempty"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// CandidateInput is the validated input used to construct a candidate.
type CandidateInput struct {
	ID          foundation.ID
	WorkspaceID foundation.ID
	Owner       Principal
	Type        Type
	Content     json.RawMessage
	Source      Source
	TaskScopeID *foundation.ID
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

// EditInput is the mutable user-owned payload for a nonterminal Memory record.
type EditInput struct {
	Content     json.RawMessage
	TaskScopeID *foundation.ID
	ExpiresAt   *time.Time
}

// NewCandidate creates a candidate that is intentionally not confirmed or active.
func NewCandidate(input CandidateInput) (Memory, error) {
	content, err := CanonicalContent(input.Content)
	if err != nil {
		return Memory{}, err
	}
	now := canonicalTime(input.CreatedAt)
	memory := Memory{
		ID: input.ID, WorkspaceID: input.WorkspaceID, Owner: input.Owner, Type: input.Type,
		Content: content, Source: input.Source, TaskScopeID: cloneID(input.TaskScopeID),
		Status: StatusCandidate, ExpiresAt: cloneTime(input.ExpiresAt), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidateMemory(memory); err != nil {
		return Memory{}, err
	}
	if memory.ExpiresAt != nil && !memory.ExpiresAt.After(now) {
		return Memory{}, expired("candidate expiry must be in the future")
	}
	return memory, nil
}

// Confirm is the sole transition that turns a candidate into confirmed ACTIVE Memory.
func Confirm(memory Memory, actor Principal, at time.Time) (Memory, error) {
	if err := ValidateMemory(memory); err != nil {
		return Memory{}, err
	}
	if err := ValidatePrincipal(actor); err != nil {
		return Memory{}, err
	}
	if !samePrincipal(memory.Owner, actor) {
		return Memory{}, ownerDenied("memory confirmation requires its owner principal")
	}
	if memory.Status != StatusCandidate {
		return Memory{}, stateConflict("only a candidate can be confirmed")
	}
	now := canonicalTime(at)
	if memory.ExpiresAt != nil && !memory.ExpiresAt.After(now) {
		return Memory{}, expired("expired candidate cannot be confirmed")
	}
	next := CloneMemory(memory)
	next.Status = StatusActive
	next.ConfirmedAt = &now
	next.ConfirmedBy = clonePrincipal(&actor)
	next.Version++
	next.UpdatedAt = now
	if err := ValidateMemory(next); err != nil {
		return Memory{}, err
	}
	return next, nil
}

// Edit updates content, scope, or expiry without changing source provenance.
func Edit(memory Memory, actor Principal, input EditInput, at time.Time) (Memory, error) {
	if err := ValidateMemory(memory); err != nil {
		return Memory{}, err
	}
	if err := ValidatePrincipal(actor); err != nil {
		return Memory{}, err
	}
	if !samePrincipal(memory.Owner, actor) {
		return Memory{}, ownerDenied("memory edit requires its owner principal")
	}
	if memory.Status != StatusCandidate && memory.Status != StatusActive && memory.Status != StatusPaused {
		return Memory{}, stateConflict("terminal memory cannot be edited")
	}
	content, err := CanonicalContent(input.Content)
	if err != nil {
		return Memory{}, err
	}
	now := canonicalTime(at)
	if memory.ExpiresAt != nil && !memory.ExpiresAt.After(now) {
		return Memory{}, expired("expired memory cannot be edited")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return Memory{}, expired("memory expiry must be in the future")
	}
	next := CloneMemory(memory)
	next.Content = content
	next.TaskScopeID = cloneID(input.TaskScopeID)
	next.ExpiresAt = cloneTime(input.ExpiresAt)
	next.Version++
	next.UpdatedAt = now
	if err := ValidateMemory(next); err != nil {
		return Memory{}, err
	}
	return next, nil
}

// Pause excludes confirmed active Memory from future context reads.
func Pause(memory Memory, actor Principal, at time.Time) (Memory, error) {
	return transitionOwned(memory, actor, StatusActive, StatusPaused, at)
}

// Resume re-enables confirmed paused Memory if it has not expired.
func Resume(memory Memory, actor Principal, at time.Time) (Memory, error) {
	next, err := transitionOwned(memory, actor, StatusPaused, StatusActive, at)
	if err != nil {
		return Memory{}, err
	}
	if next.ExpiresAt != nil && !next.ExpiresAt.After(next.UpdatedAt) {
		return Memory{}, expired("expired memory cannot be resumed")
	}
	return next, nil
}

// Delete writes a tombstone while retaining lifecycle and audit history.
func Delete(memory Memory, actor Principal, at time.Time) (Memory, error) {
	if err := ValidateMemory(memory); err != nil {
		return Memory{}, err
	}
	if err := ValidatePrincipal(actor); err != nil {
		return Memory{}, err
	}
	if !samePrincipal(memory.Owner, actor) {
		return Memory{}, ownerDenied("memory deletion requires its owner principal")
	}
	if memory.Status == StatusDeleted {
		return Memory{}, stateConflict("memory is already deleted")
	}
	next := CloneMemory(memory)
	next.Status = StatusDeleted
	next.Version++
	next.UpdatedAt = canonicalTime(at)
	if err := ValidateMemory(next); err != nil {
		return Memory{}, err
	}
	return next, nil
}

// Expire transitions an elapsed candidate, active, or paused record into retained expiry history.
func Expire(memory Memory, at time.Time) (Memory, error) {
	if err := ValidateMemory(memory); err != nil {
		return Memory{}, err
	}
	now := canonicalTime(at)
	if memory.ExpiresAt == nil || memory.ExpiresAt.After(now) {
		return Memory{}, stateConflict("memory is not due for expiry")
	}
	if memory.Status != StatusCandidate && memory.Status != StatusActive && memory.Status != StatusPaused {
		return Memory{}, stateConflict("memory cannot be expired from its current state")
	}
	next := CloneMemory(memory)
	next.Status = StatusExpired
	next.Version++
	next.UpdatedAt = now
	if err := ValidateMemory(next); err != nil {
		return Memory{}, err
	}
	return next, nil
}

// IsEffective returns whether this record is eligible for a task-scoped context read.
func IsEffective(memory Memory, taskScopeID *foundation.ID, at time.Time) bool {
	if ValidateMemory(memory) != nil || memory.Status != StatusActive || memory.ConfirmedAt == nil || memory.ConfirmedBy == nil {
		return false
	}
	now := canonicalTime(at)
	if memory.ExpiresAt != nil && !memory.ExpiresAt.After(now) {
		return false
	}
	if memory.TaskScopeID == nil {
		return true
	}
	return taskScopeID != nil && *memory.TaskScopeID == *taskScopeID
}

// ValidateMemory validates aggregate state independent of a particular command.
func ValidateMemory(memory Memory) error {
	if !validID(memory.ID) || !validID(memory.WorkspaceID) || ValidatePrincipal(memory.Owner) != nil ||
		!validType(memory.Type) || !validStatus(memory.Status) || memory.Version < 1 {
		return invalid("memory identity, owner, type, status, or version is invalid")
	}
	content, err := CanonicalContent(memory.Content)
	if err != nil || !bytes.Equal(content, memory.Content) {
		return invalid("memory content is not canonical")
	}
	if err := ValidateSource(memory.Source); err != nil {
		return err
	}
	if memory.TaskScopeID != nil && !validID(*memory.TaskScopeID) {
		return invalid("memory task scope is invalid")
	}
	if memory.CreatedAt.IsZero() || memory.UpdatedAt.IsZero() || memory.UpdatedAt.Before(memory.CreatedAt) {
		return invalid("memory timestamps are invalid")
	}
	if memory.ExpiresAt != nil && !memory.ExpiresAt.After(memory.CreatedAt) {
		return invalid("memory expiry must be after creation")
	}
	if memory.Type == TypeEpisodic && memory.ExpiresAt == nil {
		return invalid("episodic memory requires expiry")
	}
	confirmed := memory.ConfirmedAt != nil || memory.ConfirmedBy != nil
	if confirmed && (memory.ConfirmedAt == nil || memory.ConfirmedBy == nil ||
		ValidatePrincipal(*memory.ConfirmedBy) != nil || memory.ConfirmedAt.Before(memory.CreatedAt) || memory.ConfirmedAt.After(memory.UpdatedAt)) {
		return invalid("memory confirmation binding is invalid")
	}
	if confirmed && !samePrincipal(memory.Owner, *memory.ConfirmedBy) {
		return invalid("memory confirmation principal must match its owner")
	}
	if memory.Status == StatusCandidate && confirmed {
		return invalid("candidate memory cannot be confirmed")
	}
	if (memory.Status == StatusActive || memory.Status == StatusPaused) && !confirmed {
		return invalid("effective memory must be confirmed")
	}
	return nil
}

// ValidatePrincipal validates the owner or confirmation principal boundary.
func ValidatePrincipal(principal Principal) error {
	if !validID(principal.ID) || principal.Kind != PrincipalUser {
		return invalid("memory principal is invalid")
	}
	return nil
}

// ValidateSource validates the opaque source classification and reference.
func ValidateSource(source Source) error {
	if source.Type != SourceUser && source.Type != SourceAgent && source.Type != SourceInterview ||
		!validText(source.Ref, MaxSourceRefBytes) {
		return invalid("memory source is invalid")
	}
	return nil
}

// ValidateIdempotencyKey validates a bounded opaque command key.
func ValidateIdempotencyKey(value string) error {
	if !validText(value, MaxIdempotencyKeyBytes) {
		return invalid("memory idempotency key is invalid")
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return invalid("memory idempotency key contains control characters")
		}
	}
	return nil
}

// CanonicalContent validates and canonicalizes bounded JSON object content.
func CanonicalContent(raw json.RawMessage) (json.RawMessage, error) {
	object, err := strictjson.DecodeObject[map[string]any](raw, strictjson.Limits{
		MaxDocumentBytes: MaxContentBytes,
		MaxDepth:         8,
		MaxStringBytes:   4096,
		MaxArrayItems:    128,
		MaxObjectFields:  64,
	}, nil)
	if err != nil || len(object) == 0 {
		return nil, invalid("memory content must be a nonempty bounded JSON object")
	}
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > MaxContentBytes {
		return nil, invalid("memory content cannot be canonicalized")
	}
	return json.RawMessage(encoded), nil
}

// CloneMemory returns a deep-enough copy for pure lifecycle transitions.
func CloneMemory(memory Memory) Memory {
	clone := memory
	clone.Content = append(json.RawMessage(nil), memory.Content...)
	clone.TaskScopeID = cloneID(memory.TaskScopeID)
	clone.ExpiresAt = cloneTime(memory.ExpiresAt)
	clone.ConfirmedAt = cloneTime(memory.ConfirmedAt)
	clone.ConfirmedBy = clonePrincipal(memory.ConfirmedBy)
	return clone
}

func transitionOwned(memory Memory, actor Principal, from, to Status, at time.Time) (Memory, error) {
	if err := ValidateMemory(memory); err != nil {
		return Memory{}, err
	}
	if err := ValidatePrincipal(actor); err != nil {
		return Memory{}, err
	}
	if !samePrincipal(memory.Owner, actor) {
		return Memory{}, ownerDenied("memory transition requires its owner principal")
	}
	if memory.Status != from {
		return Memory{}, stateConflict("memory is not in the required lifecycle state")
	}
	next := CloneMemory(memory)
	next.Status = to
	next.Version++
	next.UpdatedAt = canonicalTime(at)
	if err := ValidateMemory(next); err != nil {
		return Memory{}, err
	}
	return next, nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validType(value Type) bool {
	return value == TypePreference || value == TypeEpisodic || value == TypeGoal || value == TypeFeedback
}

func validStatus(value Status) bool {
	return value == StatusCandidate || value == StatusActive || value == StatusPaused || value == StatusExpired || value == StatusDeleted
}

func validText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func samePrincipal(left, right Principal) bool {
	return left.Kind == right.Kind && left.ID == right.ID
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := canonicalTime(*value)
	return &copy
}

func clonePrincipal(value *Principal) *Principal {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func canonicalTime(value time.Time) time.Time { return value.UTC().Truncate(time.Microsecond) }
