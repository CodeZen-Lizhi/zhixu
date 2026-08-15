package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ProposalRevisionMergeAlgorithm is the stable merge contract advertised by
	// the API. Adapter implementations may change internally only by changing
	// this version.
	ProposalRevisionMergeAlgorithm        = "git-merge-file"
	ProposalRevisionMergeAlgorithmVersion = "diff3/myers/marker32/v1"
	ProposalRevisionMaxBytes              = 1 << 20
	ProposalRevisionMetadataMaxBytes      = 64 << 10
	proposalRevisionMergeMarkerSize       = 32
	// ProposalRevisionBaseSnapshotSchemaVersion 固定基线快照的持久化契约。
	ProposalRevisionBaseSnapshotSchemaVersion = "proposal-base-snapshot/v1"
)

var (
	ErrProposalRevisionNotEditable        = errors.New("proposal revision is not editable")
	ErrProposalRevisionInputTooLarge      = errors.New("proposal revision input is too large")
	ErrProposalRevisionConflictUnresolved = errors.New("proposal revision has unresolved conflicts")
	ErrProposalRevisionBindingMismatch    = errors.New("proposal revision binding mismatch")
	ErrProposalRevisionStale              = errors.New("proposal revision is stale")
)

// ProposalRevisionCapabilityReason explains whether the current Revision can
// enter the revision workbench. The value is part of the public API contract.
type ProposalRevisionCapabilityReason string

const (
	ProposalRevisionAvailable          ProposalRevisionCapabilityReason = "AVAILABLE"
	ProposalRevisionUnsupportedType    ProposalRevisionCapabilityReason = "PROPOSAL_REVISION_UNSUPPORTED_TYPE"
	ProposalRevisionUnsupportedMode    ProposalRevisionCapabilityReason = "PROPOSAL_REVISION_UNSUPPORTED_MODE"
	ProposalRevisionStatusNotEditable  ProposalRevisionCapabilityReason = "PROPOSAL_REVISION_STATUS_NOT_EDITABLE"
	ProposalRevisionCapabilityStale    ProposalRevisionCapabilityReason = "PROPOSAL_REVISION_STALE"
	ProposalRevisionWorkflowActive     ProposalRevisionCapabilityReason = "PROPOSAL_REVISION_WORKFLOW_ACTIVE"
	ProposalRevisionSideEffectStarted  ProposalRevisionCapabilityReason = "PROPOSAL_REVISION_SIDE_EFFECT_STARTED"
	ProposalRevisionCapabilityTooLarge ProposalRevisionCapabilityReason = "PROPOSAL_REVISION_INPUT_TOO_LARGE"
	ProposalRevisionEngineUnavailable  ProposalRevisionCapabilityReason = "PROPOSAL_MERGE_ENGINE_UNAVAILABLE"
)

// ProposalRevisionCapability is a bounded read projection. AppendRevision
// still repeats every authorization and side-effect fence transactionally.
type ProposalRevisionCapability struct {
	Editable bool
	Reason   ProposalRevisionCapabilityReason
}

// ProposalRevisionCapabilityFacts contains only the current aggregate facts
// needed to decide whether the workbench entry point should be offered.
type ProposalRevisionCapabilityFacts struct {
	ProposalType        ProposalType
	ProposalStatus      ProposalStatus
	TargetMode          TargetMode
	CurrentRevisionID   foundation.ID
	RevisionID          foundation.ID
	RevisionContentSize int
	WorkflowRunID       *foundation.ID
	WorkflowRunStatus   string
}

// EvaluateProposalRevisionCapability keeps list and detail projections on one
// rule. A terminal failed/cancelled run may be superseded; succeeded runs have
// crossed the side-effect boundary, while every other bound status is active.
func EvaluateProposalRevisionCapability(facts ProposalRevisionCapabilityFacts) ProposalRevisionCapability {
	capability := ProposalRevisionCapability{Editable: true, Reason: ProposalRevisionAvailable}
	switch {
	case NormalizeProposalType(facts.ProposalType) != ProposalTypeFilePatch:
		capability.Editable, capability.Reason = false, ProposalRevisionUnsupportedType
	case NormalizeTargetMode(facts.TargetMode) != TargetModeReplace:
		capability.Editable, capability.Reason = false, ProposalRevisionUnsupportedMode
	case facts.ProposalStatus != StatusReady && facts.ProposalStatus != StatusNeedsRevision:
		capability.Editable, capability.Reason = false, ProposalRevisionStatusNotEditable
	case facts.CurrentRevisionID == "" || facts.RevisionID == "" || facts.CurrentRevisionID != facts.RevisionID:
		capability.Editable, capability.Reason = false, ProposalRevisionCapabilityStale
	case facts.ProposalStatus == StatusNeedsRevision && facts.WorkflowRunID != nil && facts.WorkflowRunStatus == "succeeded":
		capability.Editable, capability.Reason = false, ProposalRevisionSideEffectStarted
	case facts.ProposalStatus == StatusNeedsRevision && facts.WorkflowRunID != nil && facts.WorkflowRunStatus != "failed" && facts.WorkflowRunStatus != "cancelled":
		capability.Editable, capability.Reason = false, ProposalRevisionWorkflowActive
	case facts.RevisionContentSize > ProposalRevisionMaxBytes:
		capability.Editable, capability.Reason = false, ProposalRevisionCapabilityTooLarge
	}
	return capability
}

// GateProposalRevisionMergeEngine preserves the more specific domain reason
// and only hides an otherwise available workbench when the runtime probe did
// not install a merge engine.
func GateProposalRevisionMergeEngine(capability ProposalRevisionCapability, available bool) ProposalRevisionCapability {
	if available || !capability.Editable {
		return capability
	}
	return ProposalRevisionCapability{Editable: false, Reason: ProposalRevisionEngineUnavailable}
}

// RevisionBaseSnapshot preserves the exact bytes observed when a revision was
// created. It is intentionally separate from Revision so legacy rows can be
// represented without inventing a base document.
type RevisionBaseSnapshot struct {
	ProposalID    foundation.ID
	RevisionID    foundation.ID
	BaseHash      string
	Content       string
	ByteSize      int
	SchemaVersion string
	CreatedAt     time.Time
}

// RevisionLineage identifies the direct predecessor of an appended revision.
type RevisionLineage struct {
	ProposalID       foundation.ID
	RevisionID       foundation.ID
	SourceRevisionID foundation.ID
	SourceChangeHash string
	Kind             RevisionLineageKind
	MergeAlgorithm   string
	MergeVersion     string
	MergeFingerprint string
	CreatedAt        time.Time
}

type RevisionLineageKind string

const (
	RevisionLineageDirectEdit    RevisionLineageKind = "DIRECT_EDIT"
	RevisionLineageThreeWayMerge RevisionLineageKind = "THREE_WAY_MERGE"
)

// RevisionMergeInput is the server-owned binding used to generate a preview.
// The client supplies identity and expected versions, never the three texts.
type RevisionMergeInput struct {
	WorkspaceID             foundation.ID
	ProposalID              foundation.ID
	SourceRevisionID        foundation.ID
	SourceChangeHash        string
	ExpectedProposalVersion int64
	ExpectedCurrentHash     string
}

// RevisionConflict is a bounded, structured conflict identity. Conflict IDs
// are stable for a given preview and are not parsed from marker text.
type RevisionConflict struct {
	// Ordinal 是合并引擎返回的稳定冲突序号。
	Ordinal  int
	ID       string
	Base     string
	Current  string
	Proposed string
}

// RevisionMergePreview contains the deterministic server result. Text is not
// persisted until AppendProposalRevision succeeds.
type RevisionMergePreview struct {
	ProposalID       foundation.ID
	SourceRevisionID foundation.ID
	ProposalVersion  int64
	BaseHash         string
	CurrentHash      string
	ProposedHash     string
	Candidate        string
	Conflicts        []RevisionConflict
	Algorithm        string
	AlgorithmVersion string
	Fingerprint      string
}

// AppendProposalRevision is the sole domain command for adding a file patch
// revision. New content is complete UTF-8 text, not a patch fragment.
type AppendProposalRevision struct {
	WorkspaceID             foundation.ID
	ProposalID              foundation.ID
	IdempotencyKey          string
	RequestHash             string
	ExpectedProposalVersion int64
	SourceRevisionID        foundation.ID
	SourceChangeHash        string
	ExpectedCurrentHash     string
	// CurrentContent is the exact server-read bytes used as the new base. It is
	// supplied by the application after reading the authorized Workspace.
	CurrentContent        string
	PreviewFingerprint    string
	MergeAlgorithm        string
	MergeAlgorithmVersion string
	FinalContent          string
	EvidenceSummary       string
	Risk                  string
	RollbackPlan          string
	ResolvedConflictIDs   []string
	NewRevisionID         foundation.ID
	CreatedAt             time.Time
}

// AppendProposalRevisionResult is replay-safe: Replayed is true when the
// receipt already existed and Revision is the exact prior result.
type AppendProposalRevisionResult struct {
	Proposal Proposal
	Revision Revision
	Replayed bool
}

// RevisionCommandReceiptLookup 只包含在读取可变 Proposal 或 Workspace 前
// 查询 append 回执所需的不可变请求身份。
type RevisionCommandReceiptLookup struct {
	WorkspaceID    foundation.ID
	ProposalID     foundation.ID
	IdempotencyKey string
	RequestHash    string
}

// ProposalRevisionRepository is an optional extension so existing Repository
// fakes and adapters remain source-compatible during rollout.
type ProposalRevisionRepository interface {
	Repository
	LookupRevisionCommandReceipt(context.Context, RevisionCommandReceiptLookup) (AppendProposalRevisionResult, bool, error)
	GetRevisionBaseSnapshot(context.Context, foundation.ID, foundation.ID) (RevisionBaseSnapshot, bool, error)
	AppendRevision(context.Context, AppendProposalRevision) (AppendProposalRevisionResult, error)
}

// ValidateRevisionMergeInput checks the immutable identity portion of preview
// and append commands. Workspace content and hashes are checked by the server
// at the transaction boundary.
func ValidateRevisionMergeInput(input RevisionMergeInput) error {
	if input.WorkspaceID == "" || input.ProposalID == "" || input.SourceRevisionID == "" || input.ExpectedProposalVersion <= 0 {
		return ErrProposalRevisionBindingMismatch
	}
	if !ValidHash(strings.ToLower(input.SourceChangeHash)) || input.SourceChangeHash != strings.ToLower(input.SourceChangeHash) {
		return ErrProposalRevisionBindingMismatch
	}
	if input.ExpectedCurrentHash != "" && (!ValidHash(input.ExpectedCurrentHash) || input.ExpectedCurrentHash != strings.ToLower(input.ExpectedCurrentHash)) {
		return ErrProposalRevisionBindingMismatch
	}
	return nil
}

// ValidateRevisionCommandReceiptLookup 校验回执查询的完整作用域和请求哈希。
func ValidateRevisionCommandReceiptLookup(input RevisionCommandReceiptLookup) error {
	if input.WorkspaceID == "" || input.ProposalID == "" || strings.TrimSpace(input.IdempotencyKey) == "" ||
		strings.TrimSpace(input.IdempotencyKey) != input.IdempotencyKey || len(input.IdempotencyKey) > 128 ||
		strings.ContainsAny(input.IdempotencyKey, "\r\n") || !ValidHash(input.RequestHash) || input.RequestHash != strings.ToLower(input.RequestHash) {
		return ErrProposalRevisionBindingMismatch
	}
	return nil
}

// ValidateRevisionBaseSnapshot 校验快照身份、精确字节哈希和版本。
func ValidateRevisionBaseSnapshot(snapshot RevisionBaseSnapshot) error {
	if snapshot.ProposalID == "" || snapshot.RevisionID == "" || snapshot.BaseHash != strings.ToLower(snapshot.BaseHash) ||
		!ValidHash(snapshot.BaseHash) || snapshot.ByteSize != len([]byte(snapshot.Content)) || snapshot.ByteSize > ProposalRevisionMaxBytes ||
		!utf8.ValidString(snapshot.Content) || strings.ContainsRune(snapshot.Content, '\x00') ||
		snapshot.SchemaVersion != ProposalRevisionBaseSnapshotSchemaVersion || snapshot.CreatedAt.IsZero() || RawContentHash(snapshot.Content) != snapshot.BaseHash {
		return ErrProposalRevisionBindingMismatch
	}
	return nil
}

// ValidateAppendProposalRevisionRequest 校验可在任何状态或外部读取前确定的 append 请求事实。
func ValidateAppendProposalRevisionRequest(command AppendProposalRevision) error {
	if command.WorkspaceID == "" || command.ProposalID == "" || command.SourceRevisionID == "" || command.ExpectedProposalVersion <= 0 {
		return ErrProposalRevisionBindingMismatch
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" || strings.TrimSpace(command.IdempotencyKey) != command.IdempotencyKey ||
		len(command.IdempotencyKey) > 128 || strings.ContainsAny(command.IdempotencyKey, "\r\n") {
		return ErrProposalRevisionBindingMismatch
	}
	if !ValidHash(command.SourceChangeHash) || command.SourceChangeHash != strings.ToLower(command.SourceChangeHash) ||
		!ValidHash(command.ExpectedCurrentHash) || command.ExpectedCurrentHash != strings.ToLower(command.ExpectedCurrentHash) {
		return ErrProposalRevisionBindingMismatch
	}
	if !ValidHash(command.PreviewFingerprint) || command.PreviewFingerprint != strings.ToLower(command.PreviewFingerprint) ||
		command.MergeAlgorithm != ProposalRevisionMergeAlgorithm || command.MergeAlgorithmVersion != ProposalRevisionMergeAlgorithmVersion {
		return ErrProposalRevisionBindingMismatch
	}
	if len([]byte(command.FinalContent)) > ProposalRevisionMaxBytes {
		return ErrProposalRevisionInputTooLarge
	}
	for _, value := range []string{command.EvidenceSummary, command.Risk, command.RollbackPlan} {
		if len([]byte(value)) > ProposalRevisionMetadataMaxBytes {
			return ErrProposalRevisionInputTooLarge
		}
		if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return ErrWritebackInvalidInput
		}
	}
	if strings.TrimSpace(command.FinalContent) == "" || !utf8.ValidString(command.FinalContent) ||
		strings.ContainsRune(command.FinalContent, '\x00') || ContainsProposalRevisionMergeMarker(command.FinalContent) {
		return ErrWritebackInvalidInput
	}
	if len(command.ResolvedConflictIDs) > 1024 {
		return ErrProposalRevisionInputTooLarge
	}
	seenConflictIDs := make(map[string]struct{}, len(command.ResolvedConflictIDs))
	for _, conflictID := range command.ResolvedConflictIDs {
		if !ValidHash(conflictID) || conflictID != strings.ToLower(conflictID) {
			return ErrProposalRevisionBindingMismatch
		}
		if _, exists := seenConflictIDs[conflictID]; exists {
			return ErrProposalRevisionBindingMismatch
		}
		seenConflictIDs[conflictID] = struct{}{}
	}
	return nil
}

// ValidateAppendProposalRevision enforces limits and conflict acknowledgement
// without trusting a client-provided merge candidate or target identity.
func ValidateAppendProposalRevision(command AppendProposalRevision) error {
	if err := ValidateAppendProposalRevisionRequest(command); err != nil {
		return err
	}
	if len([]byte(command.CurrentContent)) > ProposalRevisionMaxBytes {
		return ErrProposalRevisionInputTooLarge
	}
	if !utf8.ValidString(command.CurrentContent) || strings.ContainsRune(command.CurrentContent, '\x00') {
		return ErrWritebackInvalidInput
	}
	if command.NewRevisionID == "" || command.CreatedAt.IsZero() {
		return ErrProposalRevisionBindingMismatch
	}
	if RawContentHash(command.CurrentContent) != command.ExpectedCurrentHash {
		return ErrProposalRevisionStale
	}
	return nil
}

// ContainsProposalRevisionMergeMarker reports whether text contains one of
// Git's complete fixed merge marker lines. Marker-like text remains valid.
// It protects every append caller from persisting an unresolved document.
func ContainsProposalRevisionMergeMarker(content string) bool {
	currentMarker := strings.Repeat("<", proposalRevisionMergeMarkerSize) + " CURRENT"
	baseMarker := strings.Repeat("|", proposalRevisionMergeMarkerSize) + " BASE"
	separatorMarker := strings.Repeat("=", proposalRevisionMergeMarkerSize)
	proposedMarker := strings.Repeat(">", proposalRevisionMergeMarkerSize) + " PROPOSED"
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == currentMarker || line == baseMarker || line == separatorMarker || line == proposedMarker {
			return true
		}
	}
	return false
}

// RawContentHash binds exact UTF-8 bytes; unlike ComputeChangeHash it does not
// normalize CRLF, so idempotency cannot collide on line-ending-only edits.
func RawContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ComputeRevisionRequestHash binds the exact caller-visible append payload.
// ExpectedCurrentHash already commits to the server-read raw base bytes, so
// replay lookup can run without reading the Workspace again.
func ComputeRevisionRequestHash(command AppendProposalRevision) string {
	var canonical strings.Builder
	canonical.WriteString("proposal-revision-append/v1\n")
	appendRevisionRequestHashField(&canonical, "workspace", string(command.WorkspaceID))
	appendRevisionRequestHashField(&canonical, "proposal", string(command.ProposalID))
	appendRevisionRequestHashField(&canonical, "key", command.IdempotencyKey)
	appendRevisionRequestHashField(&canonical, "version", strconv.FormatInt(command.ExpectedProposalVersion, 10))
	appendRevisionRequestHashField(&canonical, "source", string(command.SourceRevisionID))
	appendRevisionRequestHashField(&canonical, "source-change", command.SourceChangeHash)
	appendRevisionRequestHashField(&canonical, "current", command.ExpectedCurrentHash)
	appendRevisionRequestHashField(&canonical, "preview", command.PreviewFingerprint)
	appendRevisionRequestHashField(&canonical, "algorithm", command.MergeAlgorithm)
	appendRevisionRequestHashField(&canonical, "algorithm-version", command.MergeAlgorithmVersion)
	appendRevisionRequestHashField(&canonical, "content", RawContentHash(command.FinalContent))
	appendRevisionRequestHashField(&canonical, "evidence", command.EvidenceSummary)
	appendRevisionRequestHashField(&canonical, "risk", command.Risk)
	appendRevisionRequestHashField(&canonical, "rollback", command.RollbackPlan)
	appendRevisionRequestHashField(&canonical, "conflict-count", strconv.Itoa(len(command.ResolvedConflictIDs)))
	for _, conflictID := range command.ResolvedConflictIDs {
		appendRevisionRequestHashField(&canonical, "conflict", conflictID)
	}
	return RawContentHash(canonical.String())
}

func appendRevisionRequestHashField(canonical *strings.Builder, name, value string) {
	canonical.WriteString(name)
	canonical.WriteByte(':')
	canonical.WriteString(strconv.Itoa(len([]byte(value))))
	canonical.WriteByte(':')
	canonical.WriteString(value)
	canonical.WriteByte('\n')
}
