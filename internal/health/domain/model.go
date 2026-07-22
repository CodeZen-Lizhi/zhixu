package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	issueIdentitySchemaV1    = "health-issue-identity/v1"
	issueFingerprintSchemaV1 = "health-issue-fingerprint/v1"
	maxIdempotencyKeyBytes   = 128
	maxCursorBytes           = 512

	// DefaultDetectorBatchSize 是 detector 单次读取的默认条数。
	DefaultDetectorBatchSize = 100
	// MaxDetectorBatchSize 是 detector 单次读取的硬上限。
	MaxDetectorBatchSize = 500
	// MaxScanItems 是一次 Health Scan 允许处理的对象数硬上限。
	MaxScanItems int64 = 50_000
	// MaxScheduleItems 是定时 Health Scan 允许配置的对象数硬上限。
	MaxScheduleItems int64 = MaxScanItems
)

// IssueType 是知识健康问题的冻结类型。
type IssueType string

const (
	IssueTypeOrphan            IssueType = "ORPHAN"
	IssueTypeDuplicate         IssueType = "DUPLICATE"
	IssueTypeConflict          IssueType = "CONFLICT"
	IssueTypeStale             IssueType = "STALE"
	IssueTypeMissingSource     IssueType = "MISSING_SOURCE"
	IssueTypeLowConfidence     IssueType = "LOW_CONFIDENCE"
	IssueTypeBrokenReference   IssueType = "BROKEN_REFERENCE"
	IssueTypeIndexError        IssueType = "INDEX_ERROR"
	IssueTypeSupersededUsage   IssueType = "SUPERSEDED_USAGE"
	IssueTypeReviewInvalidated IssueType = "REVIEW_INVALIDATED"
)

// Severity 是 Health Issue 的严重度。
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityHigh     Severity = "HIGH"
	SeverityMedium   Severity = "MEDIUM"
	SeverityLow      Severity = "LOW"
)

// IssueStatus 是 Health Issue 当前可见的受控生命周期状态。
type IssueStatus string

const (
	IssueStatusOpen            IssueStatus = "OPEN"
	IssueStatusAcknowledged    IssueStatus = "ACKNOWLEDGED"
	IssueStatusDeferred        IssueStatus = "DEFERRED"
	IssueStatusProposalCreated IssueStatus = "PROPOSAL_CREATED"
	IssueStatusResolved        IssueStatus = "RESOLVED"
	IssueStatusIgnored         IssueStatus = "IGNORED"
	IssueStatusFalsePositive   IssueStatus = "FALSE_POSITIVE"
	IssueStatusReopened        IssueStatus = "REOPENED"
)

// IssueDecisionAction 是用户对 Health Issue 的受控操作。
type IssueDecisionAction string

const (
	IssueDecisionAcknowledge          IssueDecisionAction = "ACKNOWLEDGE"
	IssueDecisionIgnore               IssueDecisionAction = "IGNORE"
	IssueDecisionFalsePositive        IssueDecisionAction = "FALSE_POSITIVE"
	IssueDecisionDefer                IssueDecisionAction = "DEFER"
	IssueDecisionCreateRepairProposal IssueDecisionAction = "CREATE_REPAIR_PROPOSAL"
)

// ObservationOutcome 表示一次 observation 对现有 Issue 的影响。
type ObservationOutcome string

const (
	ObservationOutcomeUnchanged ObservationOutcome = "UNCHANGED"
	ObservationOutcomeReopened  ObservationOutcome = "REOPENED"
)

// ObjectType 是 Health 领域引用的稳定对象类型。
type ObjectType string

const (
	ObjectTypeTopic         ObjectType = "TOPIC"
	ObjectTypeClaim         ObjectType = "CLAIM"
	ObjectTypeRelation      ObjectType = "RELATION"
	ObjectTypeConflict      ObjectType = "CONFLICT"
	ObjectTypeSourceVersion ObjectType = "SOURCE_VERSION"
	ObjectTypeIndexVersion  ObjectType = "INDEX_VERSION"
)

// ObjectRef 表示一个 Workspace 内的稳定对象引用。
type ObjectRef struct {
	Type ObjectType
	ID   foundation.ID
}

// IssueEvidence 表示可验证的当前证据摘要与引用。
type IssueEvidence struct {
	Ref     ObjectRef
	Hash    string
	Summary string
}

// ObjectVersion 表示 fingerprint 绑定的对象版本快照。
type ObjectVersion struct {
	Ref     ObjectRef
	Version int64
}

// RepairOption 表示 Issue 当前可展示的 typed 修复入口。
type RepairOption struct {
	Code              string
	Title             string
	Available         bool
	UnavailableReason string
}

// ProposalBinding 表示与当前 fingerprint 和目标版本绑定的修复 Proposal。
type ProposalBinding struct {
	ProposalID       foundation.ID
	RepairOptionCode string
	Fingerprint      string
	ObjectVersions   []ObjectVersion
	CreatedAt        time.Time
}

// IssueObservation 表示 detector 在某个时间点产出的当前问题快照。
type IssueObservation struct {
	Type            IssueType
	Target          ObjectRef
	DetectorID      string
	DetectorVersion string
	Severity        Severity
	EvidenceSummary string
	Evidence        []IssueEvidence
	ObjectVersions  []ObjectVersion
}

// Issue 是 Health 模块拥有的稳定问题聚合。
type Issue struct {
	ID              foundation.ID
	WorkspaceID     foundation.ID
	Type            IssueType
	Target          ObjectRef
	DetectorID      string
	DetectorVersion string
	IdentityHash    string
	Fingerprint     string
	Severity        Severity
	EvidenceSummary string
	Evidence        []IssueEvidence
	ObjectVersions  []ObjectVersion
	RepairOptions   []RepairOption
	Status          IssueStatus
	StatusReason    string
	DeferredUntil   *time.Time
	Proposal        *ProposalBinding
	FirstDetectedAt time.Time
	LastDetectedAt  time.Time
	LastVerifiedAt  time.Time
	ResolvedAt      *time.Time
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// IssueDecision 是 Issue 状态变更时的 CAS 输入。
type IssueDecision struct {
	ExpectedVersion  int64
	IdempotencyKey   string
	Action           IssueDecisionAction
	Reason           string
	DeferredUntil    *time.Time
	ProposalID       *foundation.ID
	RepairOptionCode string
}

// FailureSummary 是 scan 或 detector 的稳定失败摘要。
type FailureSummary struct {
	Stage     string
	Code      string
	Retryable bool
}

// ScanScopeType 是 Health Scan 的受控 scope discriminator。
type ScanScopeType string

const (
	ScanScopeTypeWorkspace       ScanScopeType = "WORKSPACE"
	ScanScopeTypeTopic           ScanScopeType = "TOPIC"
	ScanScopeTypeSmartCollection ScanScopeType = "SMART_COLLECTION"
	ScanScopeTypeDirectory       ScanScopeType = "DIRECTORY"
)

// ScanStatus 是 Health Scan 的运行态与终态。
type ScanStatus string

const (
	ScanStatusPending   ScanStatus = "PENDING"
	ScanStatusRunning   ScanStatus = "RUNNING"
	ScanStatusSucceeded ScanStatus = "SUCCEEDED"
	ScanStatusPartial   ScanStatus = "PARTIAL"
	ScanStatusFailed    ScanStatus = "FAILED"
	ScanStatusCancelled ScanStatus = "CANCELLED"
)

// DetectorCoverageStatus 是单 detector coverage 的状态。
type DetectorCoverageStatus string

const (
	DetectorCoverageStatusPending     DetectorCoverageStatus = "PENDING"
	DetectorCoverageStatusRunning     DetectorCoverageStatus = "RUNNING"
	DetectorCoverageStatusSucceeded   DetectorCoverageStatus = "SUCCEEDED"
	DetectorCoverageStatusPartial     DetectorCoverageStatus = "PARTIAL"
	DetectorCoverageStatusFailed      DetectorCoverageStatus = "FAILED"
	DetectorCoverageStatusCancelled   DetectorCoverageStatus = "CANCELLED"
	DetectorCoverageStatusUnavailable DetectorCoverageStatus = "UNAVAILABLE"
)

// ScanScope 描述一次 Health Scan 绑定的稳定对象快照。
type ScanScope struct {
	Type              ScanScopeType
	Ref               foundation.ID
	Version           int64
	SchemaVersion     string
	Hash              string
	ReadModelRevision string
	// ExactCount 是 SMART_COLLECTION start 时由 Collection owner 冻结的精确成员数。
	ExactCount int64
}

// ScanCheckpoint 是持久化分页恢复边界。
type ScanCheckpoint struct {
	Cursor   string
	Page     int64
	LastItem *ObjectRef
}

// ScanCounters 表示 scan 或 detector 的稳定计数器。
type ScanCounters struct {
	Processed int64
	Created   int64
	Reopened  int64
	Resolved  int64
	Unchanged int64
	Failed    int64
}

// DetectorCoverage 表示单 detector 的 coverage、checkpoint 与计数。
type DetectorCoverage struct {
	DetectorID        string
	DetectorVersion   string
	Status            DetectorCoverageStatus
	Checkpoint        ScanCheckpoint
	Counters          ScanCounters
	LastError         *FailureSummary
	UnavailableReason string
}

// Scan 是 Health 模块拥有的 durable 扫描事实。
type Scan struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	WorkflowRunID  foundation.ID
	Scope          ScanScope
	Fingerprint    string
	IdempotencyKey string
	RequestHash    string
	MaxItems       int64
	Status         ScanStatus
	Checkpoint     ScanCheckpoint
	Counters       ScanCounters
	Coverage       []DetectorCoverage
	LastError      *FailureSummary
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

// ScheduleCadence 是 Health 定时扫描的受控节奏。
type ScheduleCadence string

const (
	ScheduleCadenceDisabled ScheduleCadence = "DISABLED"
	ScheduleCadenceDaily    ScheduleCadence = "DAILY"
	ScheduleCadenceWeekly   ScheduleCadence = "WEEKLY"
	ScheduleCadenceCron     ScheduleCadence = "CRON"
)

// Schedule 是 Health 定时扫描的稳定持久化模型。
type Schedule struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	Scope          ScanScope
	Cadence        ScheduleCadence
	CronExpression string
	Timezone       string
	MaxItems       int64
	NextRunAt      *time.Time
	LastRunAt      *time.Time
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ValidateIssueObservation 校验 detector observation 的当前事实。
func ValidateIssueObservation(workspaceID foundation.ID, observation IssueObservation) error {
	if !validID(workspaceID) || !validIssueType(observation.Type) || !validObjectRef(observation.Target) {
		return invalid(ErrorCodeIssueInvalid, "issue observation identity is invalid")
	}
	if observation.Type == IssueTypeReviewInvalidated {
		return invalid(ErrorCodeIssueInvalid, "review-invalidated is unavailable and cannot produce an issue observation")
	}
	detectorID, err := normalizeReference(observation.DetectorID, true)
	if err != nil || detectorID != observation.DetectorID {
		return invalid(ErrorCodeIssueInvalid, "issue detector id is not canonical")
	}
	detectorVersion, err := normalizeReference(observation.DetectorVersion, true)
	if err != nil || detectorVersion != observation.DetectorVersion {
		return invalid(ErrorCodeIssueInvalid, "issue detector version is not canonical")
	}
	if !validSeverity(observation.Severity) {
		return invalid(ErrorCodeIssueInvalid, "issue severity is invalid")
	}
	summary, err := knowledge.NormalizeReason(observation.EvidenceSummary, true)
	if err != nil || summary != observation.EvidenceSummary {
		return invalid(ErrorCodeIssueInvalid, "issue evidence summary is not canonical")
	}
	if len(observation.Evidence) == 0 || len(observation.ObjectVersions) == 0 {
		return invalid(ErrorCodeIssueInvalid, "issue observation must contain evidence and object versions")
	}
	seenEvidence := make(map[string]struct{}, len(observation.Evidence))
	for _, item := range observation.Evidence {
		if err := validateIssueEvidence(item); err != nil {
			return err
		}
		if _, exists := seenEvidence[item.Hash]; exists {
			return invalid(ErrorCodeIssueInvalid, "issue evidence hashes must be unique")
		}
		seenEvidence[item.Hash] = struct{}{}
	}
	seenVersions := make(map[string]struct{}, len(observation.ObjectVersions))
	for _, item := range observation.ObjectVersions {
		if err := validateObjectVersion(item); err != nil {
			return err
		}
		key := objectRefKey(item.Ref)
		if _, exists := seenVersions[key]; exists {
			return invalid(ErrorCodeIssueInvalid, "issue object versions must be unique by reference")
		}
		seenVersions[key] = struct{}{}
	}
	return nil
}

// ComputeIssueIdentityHash 计算绑定 schema、Workspace、问题类型、目标和 detector 的稳定 identity。
func ComputeIssueIdentityHash(workspaceID foundation.ID, observation IssueObservation) (string, error) {
	if err := ValidateIssueObservation(workspaceID, observation); err != nil {
		return "", err
	}
	payload := struct {
		Schema     string    `json:"schema"`
		Workspace  string    `json:"workspace_id"`
		Type       IssueType `json:"type"`
		Target     ObjectRef `json:"target"`
		DetectorID string    `json:"detector_id"`
	}{
		Schema: issueIdentitySchemaV1, Workspace: string(workspaceID), Type: observation.Type, Target: observation.Target, DetectorID: observation.DetectorID,
	}
	return marshalSHA256(payload)
}

// ComputeIssueFingerprint 计算绑定 evidence、对象版本和 detector version 的当前 fingerprint。
func ComputeIssueFingerprint(workspaceID foundation.ID, observation IssueObservation) (string, error) {
	if err := ValidateIssueObservation(workspaceID, observation); err != nil {
		return "", err
	}
	evidenceHashes := make([]string, 0, len(observation.Evidence))
	for _, item := range observation.Evidence {
		evidenceHashes = append(evidenceHashes, item.Hash)
	}
	sort.Strings(evidenceHashes)
	objectVersions := copyObjectVersions(observation.ObjectVersions)
	sort.Slice(objectVersions, func(i, j int) bool {
		left, right := objectRefKey(objectVersions[i].Ref), objectRefKey(objectVersions[j].Ref)
		if left == right {
			return objectVersions[i].Version < objectVersions[j].Version
		}
		return left < right
	})
	payload := struct {
		Schema          string          `json:"schema"`
		Workspace       string          `json:"workspace_id"`
		Type            IssueType       `json:"type"`
		Target          ObjectRef       `json:"target"`
		DetectorID      string          `json:"detector_id"`
		DetectorVersion string          `json:"detector_version"`
		EvidenceHashes  []string        `json:"evidence_hashes"`
		ObjectVersions  []ObjectVersion `json:"object_versions"`
	}{
		Schema: issueFingerprintSchemaV1, Workspace: string(workspaceID), Type: observation.Type, Target: observation.Target,
		DetectorID: observation.DetectorID, DetectorVersion: observation.DetectorVersion,
		EvidenceHashes: evidenceHashes, ObjectVersions: objectVersions,
	}
	return marshalSHA256(payload)
}

// ValidateIssue 校验当前 Issue 聚合及其当前 observation 快照。
func ValidateIssue(issue Issue) error {
	if !validID(issue.ID) || !validID(issue.WorkspaceID) || issue.ID == issue.WorkspaceID || issue.Version < 1 ||
		issue.CreatedAt.IsZero() || issue.UpdatedAt.Before(issue.CreatedAt) || issue.FirstDetectedAt.IsZero() ||
		issue.LastDetectedAt.Before(issue.FirstDetectedAt) || issue.LastVerifiedAt.Before(issue.FirstDetectedAt) || !validIssueStatus(issue.Status) {
		return invalid(ErrorCodeIssueInvalid, "issue identity or lifecycle is invalid")
	}
	observation := IssueObservation{
		Type:            issue.Type,
		Target:          issue.Target,
		DetectorID:      issue.DetectorID,
		DetectorVersion: issue.DetectorVersion,
		Severity:        issue.Severity,
		EvidenceSummary: issue.EvidenceSummary,
		Evidence:        copyIssueEvidence(issue.Evidence),
		ObjectVersions:  copyObjectVersions(issue.ObjectVersions),
	}
	if err := ValidateIssueObservation(issue.WorkspaceID, observation); err != nil {
		return err
	}
	identity, err := ComputeIssueIdentityHash(issue.WorkspaceID, observation)
	if err != nil || identity != issue.IdentityHash {
		return inconsistent(ErrorCodeIssueInvalid, "issue identity hash is inconsistent")
	}
	fingerprint, err := ComputeIssueFingerprint(issue.WorkspaceID, observation)
	if err != nil || fingerprint != issue.Fingerprint {
		return inconsistent(ErrorCodeIssueInvalid, "issue fingerprint is inconsistent")
	}
	if err := validateRepairOptions(issue.RepairOptions); err != nil {
		return err
	}
	switch issue.Status {
	case IssueStatusIgnored, IssueStatusFalsePositive, IssueStatusDeferred:
		reason, normalizeErr := knowledge.NormalizeReason(issue.StatusReason, true)
		if normalizeErr != nil || reason != issue.StatusReason {
			return invalid(ErrorCodeIssueInvalid, "issue status reason is not canonical")
		}
	default:
		if issue.StatusReason != "" {
			return invalid(ErrorCodeIssueInvalid, "issue status reason is only allowed on ignored, false-positive or deferred issues")
		}
	}
	switch issue.Status {
	case IssueStatusDeferred:
		if issue.DeferredUntil == nil || issue.DeferredUntil.IsZero() {
			return invalid(ErrorCodeIssueInvalid, "deferred issue requires a deferred-until time")
		}
		if issue.Proposal != nil {
			return invalid(ErrorCodeIssueInvalid, "deferred issue cannot carry a proposal binding")
		}
	case IssueStatusProposalCreated:
		if issue.DeferredUntil != nil || issue.Proposal == nil {
			return invalid(ErrorCodeIssueInvalid, "proposal-created issue requires only a proposal binding")
		}
		if err := validateProposalBinding(issue.Proposal, issue.Fingerprint, issue.ObjectVersions, issue.RepairOptions); err != nil {
			return err
		}
	default:
		if issue.DeferredUntil != nil || issue.Proposal != nil {
			return invalid(ErrorCodeIssueInvalid, "defer and proposal bindings do not match the issue status")
		}
	}
	if issue.Status == IssueStatusResolved {
		if issue.ResolvedAt == nil || issue.ResolvedAt.Before(issue.FirstDetectedAt) {
			return invalid(ErrorCodeIssueInvalid, "resolved issue requires a valid resolved-at time")
		}
	} else if issue.ResolvedAt != nil {
		return invalid(ErrorCodeIssueInvalid, "only resolved issue can carry resolved-at")
	}
	return nil
}

// ApplyIssueObservation 将新的 detector observation 合并到既有 Issue。
func ApplyIssueObservation(issue Issue, observation IssueObservation, verifiedAt time.Time) (Issue, ObservationOutcome, error) {
	if err := ValidateIssue(issue); err != nil {
		return Issue{}, "", err
	}
	if !validIssueEventTime(issue, verifiedAt) {
		return Issue{}, "", invalid(ErrorCodeIssueInvalid, "issue observation time is zero or moves backwards")
	}
	if err := ValidateIssueObservation(issue.WorkspaceID, observation); err != nil {
		return Issue{}, "", err
	}
	identity, err := ComputeIssueIdentityHash(issue.WorkspaceID, observation)
	if err != nil {
		return Issue{}, "", err
	}
	if identity != issue.IdentityHash {
		return Issue{}, "", invalid(ErrorCodeIssueInvalid, "issue observation does not match the existing identity")
	}
	fingerprint, err := ComputeIssueFingerprint(issue.WorkspaceID, observation)
	if err != nil {
		return Issue{}, "", err
	}
	if fingerprint == issue.Fingerprint {
		if !sameNonFingerprintObservationFields(issue, observation) {
			return Issue{}, "", invalid(ErrorCodeIssueInvalid, "issue observation changes fields not covered by the fingerprint")
		}
		updated := issue
		updated.LastVerifiedAt = verifiedAt
		return updated, ObservationOutcomeUnchanged, nil
	}
	updated := issue
	updated.DetectorVersion = observation.DetectorVersion
	updated.Severity = observation.Severity
	updated.EvidenceSummary = observation.EvidenceSummary
	updated.Evidence = copyIssueEvidence(observation.Evidence)
	updated.ObjectVersions = copyObjectVersions(observation.ObjectVersions)
	updated.LastDetectedAt = verifiedAt
	updated.LastVerifiedAt = verifiedAt
	updated.UpdatedAt = verifiedAt
	updated.Version++
	updated.Fingerprint = fingerprint
	updated.Status = IssueStatusReopened
	updated.StatusReason = ""
	updated.DeferredUntil = nil
	updated.Proposal = nil
	updated.ResolvedAt = nil
	return updated, ObservationOutcomeReopened, nil
}

// sameNonFingerprintObservationFields 检查 fingerprint 未覆盖的当前事实，避免同 fingerprint 静默改写。
func sameNonFingerprintObservationFields(issue Issue, observation IssueObservation) bool {
	if issue.Severity != observation.Severity || issue.EvidenceSummary != observation.EvidenceSummary || len(issue.Evidence) != len(observation.Evidence) {
		return false
	}
	current := copyIssueEvidence(issue.Evidence)
	incoming := copyIssueEvidence(observation.Evidence)
	sort.Slice(current, func(i, j int) bool { return issueEvidenceKey(current[i]) < issueEvidenceKey(current[j]) })
	sort.Slice(incoming, func(i, j int) bool { return issueEvidenceKey(incoming[i]) < issueEvidenceKey(incoming[j]) })
	for index := range current {
		if current[index] != incoming[index] {
			return false
		}
	}
	return true
}

func issueEvidenceKey(value IssueEvidence) string {
	return value.Hash + "\x00" + objectRefKey(value.Ref) + "\x00" + value.Summary
}

// ApplyIssueDecision 校验并应用一个 Issue 决策。
func ApplyIssueDecision(issue Issue, decision IssueDecision, at time.Time) (Issue, error) {
	if err := ValidateIssue(issue); err != nil {
		return Issue{}, err
	}
	if !validIssueEventTime(issue, at) {
		return Issue{}, invalid(ErrorCodeIssueDecisionInvalid, "issue decision time is zero or moves backwards")
	}
	if issue.Version != decision.ExpectedVersion {
		return Issue{}, versionConflict(ErrorCodeIssueTransitionInvalid, "issue expected version does not match current version")
	}
	if err := validateDecision(issue, decision, at); err != nil {
		return Issue{}, err
	}
	nextStatus, err := nextIssueStatus(issue.Status, decision.Action)
	if err != nil {
		return Issue{}, err
	}
	updated := issue
	updated.Status = nextStatus
	updated.StatusReason = ""
	updated.DeferredUntil = nil
	updated.Proposal = nil
	updated.ResolvedAt = nil
	updated.UpdatedAt = at
	updated.Version++
	switch decision.Action {
	case IssueDecisionIgnore, IssueDecisionFalsePositive, IssueDecisionDefer:
		updated.StatusReason = decision.Reason
	}
	switch decision.Action {
	case IssueDecisionDefer:
		updated.DeferredUntil = cloneTime(*decision.DeferredUntil)
	case IssueDecisionCreateRepairProposal:
		updated.Proposal = &ProposalBinding{
			ProposalID:       *decision.ProposalID,
			RepairOptionCode: decision.RepairOptionCode,
			Fingerprint:      issue.Fingerprint,
			ObjectVersions:   copyObjectVersions(issue.ObjectVersions),
			CreatedAt:        at,
		}
	}
	return updated, nil
}

// ResolveIssueFromScan 只在完整 coverage 且本轮 seen set 未包含当前 identity 时自动解决 Issue。
func ResolveIssueFromScan(issue Issue, scan Scan, seenIdentityHashes map[string]struct{}, at time.Time) (Issue, bool, error) {
	if err := ValidateIssue(issue); err != nil {
		return Issue{}, false, err
	}
	if err := ValidateScan(scan); err != nil {
		return Issue{}, false, err
	}
	if seenIdentityHashes == nil {
		return Issue{}, false, invalid(ErrorCodeScanInvalid, "scan seen identity set is required")
	}
	if issue.WorkspaceID != scan.WorkspaceID {
		return Issue{}, false, inconsistent(ErrorCodeIssueInvalid, "issue and scan workspace binding is inconsistent")
	}
	if !validIssueEventTime(issue, at) {
		return Issue{}, false, invalid(ErrorCodeIssueTransitionInvalid, "issue resolution time is zero or moves backwards")
	}
	// 只有仍处于可自动处理的 active 状态才允许完整扫描自动解决。
	// 用户的静默决策（DEFERRED/IGNORED/FALSE_POSITIVE）及已创建 Proposal
	// 必须保持不变，直到新的 fingerprint 通过 observation 显式重开。
	if !isAutoResolvableStatus(issue.Status) || scan.Status != ScanStatusSucceeded || !CoverageComplete(scan.Coverage) {
		return issue, false, nil
	}
	if !coverageContainsDetector(scan.Coverage, issue.DetectorID, DetectorCoverageStatusSucceeded) {
		return issue, false, nil
	}
	if _, seen := seenIdentityHashes[issue.IdentityHash]; seen {
		return issue, false, nil
	}
	updated := issue
	updated.Status = IssueStatusResolved
	updated.StatusReason = ""
	updated.DeferredUntil = nil
	updated.Proposal = nil
	updated.ResolvedAt = cloneTime(at)
	updated.LastVerifiedAt = at
	updated.UpdatedAt = at
	updated.Version++
	return updated, true, nil
}

func isAutoResolvableStatus(status IssueStatus) bool {
	switch status {
	case IssueStatusOpen, IssueStatusAcknowledged, IssueStatusReopened:
		return true
	default:
		return false
	}
}

// ValidateScanScope 校验首版支持的 Workspace、Topic 和 Smart Collection scope。
func ValidateScanScope(scope ScanScope) error {
	return validateScanScope(scope, true)
}

// ValidateScheduleScope 校验 schedule scope；Smart Collection revision 由每次 due scan 重新规划，不写入长期配置。
func ValidateScheduleScope(scope ScanScope) error {
	return validateScanScope(scope, false)
}

func validateScanScope(scope ScanScope, requireReadModelRevision bool) error {
	if scope.Type == ScanScopeTypeDirectory {
		return unavailable(ErrorCodeScanScopeUnavailable, "directory scope is not implemented in this release")
	}
	if scope.Type != ScanScopeTypeWorkspace && scope.Type != ScanScopeTypeTopic && scope.Type != ScanScopeTypeSmartCollection {
		return invalid(ErrorCodeScanInvalid, "scan scope type is invalid")
	}
	schemaVersion, err := normalizeReference(scope.SchemaVersion, true)
	if err != nil || schemaVersion != scope.SchemaVersion || !validID(scope.Ref) || scope.Version < 1 {
		return invalid(ErrorCodeScanInvalid, "scan scope identity or version is invalid")
	}
	if scope.Type == ScanScopeTypeSmartCollection {
		if !validSHA256(scope.Hash) || (requireReadModelRevision && !validSHA256(scope.ReadModelRevision)) {
			return invalid(ErrorCodeScanInvalid, "smart-collection scope requires canonical query and read-model hashes")
		}
		if scope.ExactCount < 0 || scope.ExactCount > MaxScanItems {
			return invalid(ErrorCodeScanInvalid, "smart-collection scope exact count is outside scan bounds")
		}
		if !requireReadModelRevision && (scope.ReadModelRevision != "" || scope.ExactCount != 0) {
			return invalid(ErrorCodeScheduleInvalid, "smart-collection schedule cannot persist a read-model binding")
		}
	} else if scope.Hash != "" || scope.ReadModelRevision != "" || scope.ExactCount != 0 {
		return invalid(ErrorCodeScanInvalid, "only smart-collection scope can carry query and read-model binding")
	}
	return nil
}

// CoverageComplete 判断是否所有请求 detector 都已完整成功覆盖。
func CoverageComplete(coverage []DetectorCoverage) bool {
	if len(coverage) == 0 {
		return false
	}
	for _, item := range coverage {
		if item.Status != DetectorCoverageStatusSucceeded {
			return false
		}
	}
	return true
}

// ValidateScan 校验 Scan、coverage、checkpoint 和终态一致性。
func ValidateScan(scan Scan) error {
	if !validID(scan.ID) || !validID(scan.WorkspaceID) || !validID(scan.WorkflowRunID) || scan.ID == scan.WorkspaceID ||
		scan.Version < 1 || scan.MaxItems < 1 || scan.MaxItems > MaxScanItems || scan.CreatedAt.IsZero() ||
		scan.UpdatedAt.Before(scan.CreatedAt) || !validScanStatus(scan.Status) {
		return invalid(ErrorCodeScanInvalid, "scan identity or lifecycle is invalid")
	}
	if err := ValidateScanScope(scan.Scope); err != nil {
		return err
	}
	if scan.Scope.Type == ScanScopeTypeWorkspace && scan.Scope.Ref != scan.WorkspaceID {
		return inconsistent(ErrorCodeScanInvalid, "workspace scan scope must bind to the same workspace id")
	}
	if !validSHA256(scan.Fingerprint) || !validSHA256(scan.RequestHash) {
		return inconsistent(ErrorCodeScanInvalid, "scan fingerprint or request hash is invalid")
	}
	if err := validateIdempotencyKey(scan.IdempotencyKey); err != nil {
		return invalid(ErrorCodeScanInvalid, "scan idempotency key is invalid")
	}
	if err := validateScanCheckpoint(scan.Checkpoint); err != nil {
		return err
	}
	if err := validateScanCounters(scan.Counters); err != nil {
		return err
	}
	if err := validateFailureSummary(scan.LastError); err != nil {
		return err
	}
	if scan.Status == ScanStatusFailed && scan.LastError == nil {
		return inconsistent(ErrorCodeScanInvalid, "failed scan requires a failure summary")
	}
	if scan.Status != ScanStatusFailed && scan.Status != ScanStatusPartial && scan.LastError != nil {
		return inconsistent(ErrorCodeScanInvalid, "only failed or partial scans can carry a failure summary")
	}
	aggregate := ScanCounters{}
	seenDetectorIDs := make(map[string]struct{}, len(scan.Coverage))
	for _, item := range scan.Coverage {
		if err := validateDetectorCoverage(item); err != nil {
			return err
		}
		if _, exists := seenDetectorIDs[item.DetectorID]; exists {
			return invalid(ErrorCodeScanInvalid, "scan detector coverage ids must be unique")
		}
		seenDetectorIDs[item.DetectorID] = struct{}{}
		aggregate.Processed += item.Counters.Processed
		aggregate.Created += item.Counters.Created
		aggregate.Reopened += item.Counters.Reopened
		aggregate.Resolved += item.Counters.Resolved
		aggregate.Unchanged += item.Counters.Unchanged
		aggregate.Failed += item.Counters.Failed
	}
	if len(scan.Coverage) > 0 {
		if aggregate != scan.Counters {
			return inconsistent(ErrorCodeScanInvalid, "scan counters must equal the sum of detector coverage counters")
		}
	} else if scan.Counters != (ScanCounters{}) {
		return inconsistent(ErrorCodeScanInvalid, "scan counters must be zero before detector coverage exists")
	}
	terminal := scan.Status == ScanStatusSucceeded || scan.Status == ScanStatusPartial || scan.Status == ScanStatusFailed || scan.Status == ScanStatusCancelled
	if terminal != (scan.CompletedAt != nil) {
		return inconsistent(ErrorCodeScanInvalid, "scan completion time does not match its status")
	}
	if scan.CompletedAt != nil && scan.CompletedAt.Before(scan.CreatedAt) {
		return inconsistent(ErrorCodeScanInvalid, "scan completion time is invalid")
	}
	if scan.Status == ScanStatusSucceeded && !CoverageComplete(scan.Coverage) {
		return inconsistent(ErrorCodeScanInvalid, "succeeded scan requires complete detector coverage")
	}
	if scan.Status == ScanStatusPartial && CoverageComplete(scan.Coverage) {
		return inconsistent(ErrorCodeScanInvalid, "partial scan must expose incomplete detector coverage")
	}
	return nil
}

// ValidateSchedule 校验定时扫描配置和 cadence 约束。
func ValidateSchedule(schedule Schedule) error {
	if !validID(schedule.ID) || !validID(schedule.WorkspaceID) || schedule.ID == schedule.WorkspaceID || schedule.Version < 1 ||
		schedule.CreatedAt.IsZero() || schedule.UpdatedAt.Before(schedule.CreatedAt) {
		return invalid(ErrorCodeScheduleInvalid, "schedule identity or lifecycle is invalid")
	}
	if err := ValidateScheduleScope(schedule.Scope); err != nil {
		return err
	}
	if schedule.Scope.Type == ScanScopeTypeDirectory {
		return unavailable(ErrorCodeScanScopeUnavailable, "directory scope is not implemented in this release")
	}
	timezone, err := normalizeReference(schedule.Timezone, true)
	if err != nil || timezone != schedule.Timezone {
		return invalid(ErrorCodeScheduleInvalid, "schedule timezone is not canonical")
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return invalid(ErrorCodeScheduleInvalid, "schedule timezone is not recognized")
	}
	if schedule.MaxItems < 1 || schedule.MaxItems > MaxScheduleItems {
		return invalid(ErrorCodeScheduleInvalid, "schedule max items is outside the allowed range")
	}
	switch schedule.Cadence {
	case ScheduleCadenceDisabled:
		if schedule.CronExpression != "" || schedule.NextRunAt != nil {
			return invalid(ErrorCodeScheduleInvalid, "disabled schedule must not carry cron or next-run state")
		}
	case ScheduleCadenceDaily, ScheduleCadenceWeekly:
		if schedule.CronExpression != "" || schedule.NextRunAt == nil || schedule.NextRunAt.IsZero() {
			return invalid(ErrorCodeScheduleInvalid, "daily or weekly schedule requires next-run time and no cron expression")
		}
	case ScheduleCadenceCron:
		if schedule.NextRunAt == nil || schedule.NextRunAt.IsZero() || !validCronExpression(schedule.CronExpression) {
			return invalid(ErrorCodeScheduleInvalid, "cron schedule requires a validated cron expression and next-run time")
		}
	default:
		return invalid(ErrorCodeScheduleInvalid, "schedule cadence is invalid")
	}
	if schedule.NextRunAt != nil && schedule.LastRunAt != nil && schedule.NextRunAt.Before(*schedule.LastRunAt) {
		return invalid(ErrorCodeScheduleInvalid, "schedule next-run time cannot be earlier than the last-run time")
	}
	return nil
}

// NextScheduleRun 计算 missed-once claim 之后的下一次未来执行时间。
// DAILY/WEEKLY 保留原 next_run_at 的本地墙钟时间；CRON 使用标准五字段、分钟粒度语义。
func NextScheduleRun(schedule Schedule, after time.Time) (*time.Time, error) {
	if err := ValidateSchedule(schedule); err != nil {
		return nil, err
	}
	if after.IsZero() {
		return nil, invalid(ErrorCodeScheduleInvalid, "schedule next-run reference time is zero")
	}
	if schedule.Cadence == ScheduleCadenceDisabled {
		return nil, nil
	}
	location, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return nil, invalid(ErrorCodeScheduleInvalid, "schedule timezone is not recognized")
	}
	current := schedule.NextRunAt.In(location)
	afterLocal := after.In(location)
	switch schedule.Cadence {
	case ScheduleCadenceDaily:
		for !current.After(afterLocal) {
			current = current.AddDate(0, 0, 1)
		}
	case ScheduleCadenceWeekly:
		for !current.After(afterLocal) {
			current = current.AddDate(0, 0, 7)
		}
	case ScheduleCadenceCron:
		candidate := afterLocal.Truncate(time.Minute).Add(time.Minute)
		deadline := candidate.AddDate(5, 0, 0)
		for !candidate.After(deadline) && !cronMatches(schedule.CronExpression, candidate) {
			candidate = candidate.Add(time.Minute)
		}
		if candidate.After(deadline) {
			return nil, invalid(ErrorCodeScheduleInvalid, "schedule cron has no bounded future occurrence")
		}
		current = candidate
	default:
		return nil, invalid(ErrorCodeScheduleInvalid, "schedule cadence is invalid")
	}
	result := current.UTC()
	return &result, nil
}

func cronMatches(expression string, candidate time.Time) bool {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return false
	}
	minute := cronFieldMatches(fields[0], candidate.Minute(), 0)
	hour := cronFieldMatches(fields[1], candidate.Hour(), 0)
	month := cronFieldMatches(fields[3], int(candidate.Month()), 1)
	dayOfMonth := cronFieldMatches(fields[2], candidate.Day(), 1)
	weekday := int(candidate.Weekday())
	dayOfWeek := cronFieldMatches(fields[4], weekday, 0) || (weekday == 0 && cronFieldMatches(fields[4], 7, 0))
	dayMatches := dayOfMonth && dayOfWeek
	if fields[2] != "*" && fields[4] != "*" {
		dayMatches = dayOfMonth || dayOfWeek
	}
	return minute && hour && month && dayMatches
}

func cronFieldMatches(field string, value, minimum int) bool {
	for _, item := range strings.Split(field, ",") {
		parts := strings.Split(item, "/")
		base := parts[0]
		step := 1
		if len(parts) == 2 {
			parsed, err := strconv.Atoi(parts[1])
			if err != nil || parsed < 1 {
				continue
			}
			step = parsed
		}
		start, end := minimum, value
		switch {
		case base == "*":
			end = value
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				continue
			}
			parsedStart, startErr := strconv.Atoi(bounds[0])
			parsedEnd, endErr := strconv.Atoi(bounds[1])
			if startErr != nil || endErr != nil {
				continue
			}
			start, end = parsedStart, parsedEnd
		default:
			parsed, err := strconv.Atoi(base)
			if err != nil {
				continue
			}
			start, end = parsed, parsed
		}
		if value >= start && value <= end && (value-start)%step == 0 {
			return true
		}
	}
	return false
}

func nextIssueStatus(current IssueStatus, action IssueDecisionAction) (IssueStatus, error) {
	allowed := map[IssueStatus]map[IssueDecisionAction]IssueStatus{
		IssueStatusOpen: {
			IssueDecisionAcknowledge:          IssueStatusAcknowledged,
			IssueDecisionIgnore:               IssueStatusIgnored,
			IssueDecisionFalsePositive:        IssueStatusFalsePositive,
			IssueDecisionDefer:                IssueStatusDeferred,
			IssueDecisionCreateRepairProposal: IssueStatusProposalCreated,
		},
		IssueStatusAcknowledged: {
			IssueDecisionIgnore:               IssueStatusIgnored,
			IssueDecisionFalsePositive:        IssueStatusFalsePositive,
			IssueDecisionDefer:                IssueStatusDeferred,
			IssueDecisionCreateRepairProposal: IssueStatusProposalCreated,
		},
		IssueStatusDeferred: {
			IssueDecisionAcknowledge:          IssueStatusAcknowledged,
			IssueDecisionIgnore:               IssueStatusIgnored,
			IssueDecisionFalsePositive:        IssueStatusFalsePositive,
			IssueDecisionCreateRepairProposal: IssueStatusProposalCreated,
		},
		IssueStatusReopened: {
			IssueDecisionAcknowledge:          IssueStatusAcknowledged,
			IssueDecisionIgnore:               IssueStatusIgnored,
			IssueDecisionFalsePositive:        IssueStatusFalsePositive,
			IssueDecisionDefer:                IssueStatusDeferred,
			IssueDecisionCreateRepairProposal: IssueStatusProposalCreated,
		},
		IssueStatusProposalCreated: {
			IssueDecisionAcknowledge:   IssueStatusAcknowledged,
			IssueDecisionIgnore:        IssueStatusIgnored,
			IssueDecisionFalsePositive: IssueStatusFalsePositive,
			IssueDecisionDefer:         IssueStatusDeferred,
		},
	}
	if next, ok := allowed[current][action]; ok {
		return next, nil
	}
	return "", versionConflict(ErrorCodeIssueTransitionInvalid, "issue status transition is not allowed")
}

func validateDecision(issue Issue, decision IssueDecision, at time.Time) error {
	if err := validateIdempotencyKey(decision.IdempotencyKey); err != nil {
		return invalid(ErrorCodeIssueDecisionInvalid, "issue decision idempotency key is invalid")
	}
	switch decision.Action {
	case IssueDecisionAcknowledge:
		if decision.Reason != "" || decision.DeferredUntil != nil || decision.ProposalID != nil || decision.RepairOptionCode != "" {
			return invalid(ErrorCodeIssueDecisionInvalid, "acknowledge does not accept reason, defer or proposal fields")
		}
	case IssueDecisionIgnore, IssueDecisionFalsePositive:
		reason, err := knowledge.NormalizeReason(decision.Reason, true)
		if err != nil || reason != decision.Reason || decision.DeferredUntil != nil || decision.ProposalID != nil || decision.RepairOptionCode != "" {
			return invalid(ErrorCodeIssueDecisionInvalid, "ignore or false-positive decision is invalid")
		}
	case IssueDecisionDefer:
		reason, err := knowledge.NormalizeReason(decision.Reason, true)
		if err != nil || reason != decision.Reason || decision.DeferredUntil == nil || !decision.DeferredUntil.After(at) || decision.ProposalID != nil || decision.RepairOptionCode != "" {
			return invalid(ErrorCodeIssueDecisionInvalid, "defer decision requires canonical reason and future defer time")
		}
	case IssueDecisionCreateRepairProposal:
		if decision.DeferredUntil != nil || decision.Reason != "" || decision.ProposalID == nil || !validID(*decision.ProposalID) {
			return invalid(ErrorCodeIssueDecisionInvalid, "proposal decision requires a stable proposal id")
		}
		code, err := normalizeReference(decision.RepairOptionCode, true)
		if err != nil || code != decision.RepairOptionCode || !repairOptionAvailable(issue.RepairOptions, decision.RepairOptionCode) {
			return invalid(ErrorCodeIssueDecisionInvalid, "proposal decision requires an available repair option")
		}
	default:
		return invalid(ErrorCodeIssueDecisionInvalid, "issue decision action is invalid")
	}
	return nil
}

func validateIssueEvidence(evidence IssueEvidence) error {
	if !validObjectRef(evidence.Ref) || !validSHA256(evidence.Hash) {
		return invalid(ErrorCodeIssueInvalid, "issue evidence reference or hash is invalid")
	}
	summary, err := knowledge.NormalizeReason(evidence.Summary, true)
	if err != nil || summary != evidence.Summary {
		return invalid(ErrorCodeIssueInvalid, "issue evidence summary is not canonical")
	}
	return nil
}

func validateObjectVersion(value ObjectVersion) error {
	if !validObjectRef(value.Ref) || value.Version < 1 {
		return invalid(ErrorCodeIssueInvalid, "issue object version is invalid")
	}
	return nil
}

func validateRepairOptions(options []RepairOption) error {
	seen := make(map[string]struct{}, len(options))
	for _, option := range options {
		code, err := normalizeReference(option.Code, true)
		if err != nil || code != option.Code {
			return invalid(ErrorCodeIssueInvalid, "repair option code is not canonical")
		}
		title, err := knowledge.NormalizeReason(option.Title, true)
		if err != nil || title != option.Title {
			return invalid(ErrorCodeIssueInvalid, "repair option title is not canonical")
		}
		if option.Available {
			if option.UnavailableReason != "" {
				return invalid(ErrorCodeIssueInvalid, "available repair option cannot carry an unavailable reason")
			}
		} else {
			reason, err := knowledge.NormalizeReason(option.UnavailableReason, true)
			if err != nil || reason != option.UnavailableReason {
				return invalid(ErrorCodeIssueInvalid, "unavailable repair option reason is not canonical")
			}
		}
		if _, exists := seen[option.Code]; exists {
			return invalid(ErrorCodeIssueInvalid, "repair option codes must be unique")
		}
		seen[option.Code] = struct{}{}
	}
	return nil
}

func validateProposalBinding(binding *ProposalBinding, fingerprint string, objectVersions []ObjectVersion, options []RepairOption) error {
	if binding == nil || !validID(binding.ProposalID) || binding.CreatedAt.IsZero() || !validSHA256(binding.Fingerprint) {
		return invalid(ErrorCodeIssueInvalid, "proposal binding is invalid")
	}
	code, err := normalizeReference(binding.RepairOptionCode, true)
	if err != nil || code != binding.RepairOptionCode || !repairOptionAvailable(options, binding.RepairOptionCode) {
		return invalid(ErrorCodeIssueInvalid, "proposal binding repair option is invalid")
	}
	if binding.Fingerprint != fingerprint || !sameObjectVersions(binding.ObjectVersions, objectVersions) {
		return inconsistent(ErrorCodeIssueInvalid, "proposal binding must match the current fingerprint and object versions")
	}
	return nil
}

func validateDetectorCoverage(coverage DetectorCoverage) error {
	detectorID, err := normalizeReference(coverage.DetectorID, true)
	if err != nil || detectorID != coverage.DetectorID {
		return invalid(ErrorCodeScanInvalid, "detector coverage id is not canonical")
	}
	detectorVersion, err := normalizeReference(coverage.DetectorVersion, true)
	if err != nil || detectorVersion != coverage.DetectorVersion {
		return invalid(ErrorCodeScanInvalid, "detector coverage version is not canonical")
	}
	if !validDetectorCoverageStatus(coverage.Status) {
		return invalid(ErrorCodeScanInvalid, "detector coverage status is invalid")
	}
	if err := validateScanCheckpoint(coverage.Checkpoint); err != nil {
		return err
	}
	if err := validateScanCounters(coverage.Counters); err != nil {
		return err
	}
	if err := validateFailureSummary(coverage.LastError); err != nil {
		return err
	}
	switch coverage.Status {
	case DetectorCoverageStatusUnavailable:
		reason, err := knowledge.NormalizeReason(coverage.UnavailableReason, true)
		if err != nil || reason != coverage.UnavailableReason || coverage.LastError != nil {
			return invalid(ErrorCodeScanInvalid, "unavailable detector coverage requires a canonical reason")
		}
	case DetectorCoverageStatusFailed, DetectorCoverageStatusPartial:
		if coverage.LastError == nil {
			return inconsistent(ErrorCodeScanInvalid, "failed or partial detector coverage requires a failure summary")
		}
	default:
		if coverage.LastError != nil || coverage.UnavailableReason != "" {
			return invalid(ErrorCodeScanInvalid, "detector coverage status does not accept an error or unavailable reason")
		}
	}
	return nil
}

func validateScanCheckpoint(checkpoint ScanCheckpoint) error {
	if checkpoint.Page < 0 || len(checkpoint.Cursor) > maxCursorBytes {
		return inconsistent(ErrorCodeScanInvalid, "scan checkpoint is invalid")
	}
	if checkpoint.LastItem != nil && !validObjectRef(*checkpoint.LastItem) {
		return inconsistent(ErrorCodeScanInvalid, "scan checkpoint item reference is invalid")
	}
	return nil
}

func validateScanCounters(counters ScanCounters) error {
	if counters.Processed < 0 || counters.Created < 0 || counters.Reopened < 0 || counters.Resolved < 0 || counters.Unchanged < 0 || counters.Failed < 0 {
		return inconsistent(ErrorCodeScanInvalid, "scan counters must be non-negative")
	}
	return nil
}

func validateFailureSummary(summary *FailureSummary) error {
	if summary == nil {
		return nil
	}
	stage, err := normalizeReference(summary.Stage, true)
	if err != nil || stage != summary.Stage {
		return inconsistent(ErrorCodeScanInvalid, "failure stage is not canonical")
	}
	code, err := normalizeReference(summary.Code, true)
	if err != nil || code != summary.Code {
		return inconsistent(ErrorCodeScanInvalid, "failure code is not canonical")
	}
	return nil
}

func repairOptionAvailable(options []RepairOption, code string) bool {
	for _, option := range options {
		if option.Code == code && option.Available {
			return true
		}
	}
	return false
}

func coverageContainsDetector(coverage []DetectorCoverage, detectorID string, status DetectorCoverageStatus) bool {
	for _, item := range coverage {
		if item.DetectorID == detectorID && item.Status == status {
			return true
		}
	}
	return false
}

func sameObjectVersions(left, right []ObjectVersion) bool {
	if len(left) != len(right) {
		return false
	}
	clonedLeft := copyObjectVersions(left)
	clonedRight := copyObjectVersions(right)
	sort.Slice(clonedLeft, func(i, j int) bool { return objectRefKey(clonedLeft[i].Ref) < objectRefKey(clonedLeft[j].Ref) })
	sort.Slice(clonedRight, func(i, j int) bool { return objectRefKey(clonedRight[i].Ref) < objectRefKey(clonedRight[j].Ref) })
	for i := range clonedLeft {
		if clonedLeft[i] != clonedRight[i] {
			return false
		}
	}
	return true
}

func copyIssueEvidence(values []IssueEvidence) []IssueEvidence {
	cloned := make([]IssueEvidence, len(values))
	copy(cloned, values)
	return cloned
}

func copyObjectVersions(values []ObjectVersion) []ObjectVersion {
	cloned := make([]ObjectVersion, len(values))
	copy(cloned, values)
	return cloned
}

func validIssueType(value IssueType) bool {
	switch value {
	case IssueTypeOrphan, IssueTypeDuplicate, IssueTypeConflict, IssueTypeStale, IssueTypeMissingSource,
		IssueTypeLowConfidence, IssueTypeBrokenReference, IssueTypeIndexError, IssueTypeSupersededUsage, IssueTypeReviewInvalidated:
		return true
	default:
		return false
	}
}

func validSeverity(value Severity) bool {
	return value == SeverityCritical || value == SeverityHigh || value == SeverityMedium || value == SeverityLow
}

func validIssueStatus(value IssueStatus) bool {
	switch value {
	case IssueStatusOpen, IssueStatusAcknowledged, IssueStatusDeferred, IssueStatusProposalCreated,
		IssueStatusResolved, IssueStatusIgnored, IssueStatusFalsePositive, IssueStatusReopened:
		return true
	default:
		return false
	}
}

func validObjectRef(value ObjectRef) bool {
	return validObjectType(value.Type) && validID(value.ID)
}

func validObjectType(value ObjectType) bool {
	switch value {
	case ObjectTypeTopic, ObjectTypeClaim, ObjectTypeRelation, ObjectTypeConflict, ObjectTypeSourceVersion, ObjectTypeIndexVersion:
		return true
	default:
		return false
	}
}

func validScanStatus(value ScanStatus) bool {
	switch value {
	case ScanStatusPending, ScanStatusRunning, ScanStatusSucceeded, ScanStatusPartial, ScanStatusFailed, ScanStatusCancelled:
		return true
	default:
		return false
	}
}

func validDetectorCoverageStatus(value DetectorCoverageStatus) bool {
	switch value {
	case DetectorCoverageStatusPending, DetectorCoverageStatusRunning, DetectorCoverageStatusSucceeded,
		DetectorCoverageStatusPartial, DetectorCoverageStatusFailed, DetectorCoverageStatusCancelled, DetectorCoverageStatusUnavailable:
		return true
	default:
		return false
	}
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validCronExpression(value string) bool {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 5 {
		return false
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, field := range fields {
		if !validCronField(field, ranges[index][0], ranges[index][1]) {
			return false
		}
	}
	return true
}

func validCronField(field string, minimum, maximum int) bool {
	for _, item := range strings.Split(field, ",") {
		parts := strings.Split(item, "/")
		if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
			return false
		}
		if len(parts) == 2 {
			step, ok := parseCronNumber(parts[1], 1, maximum-minimum+1)
			if !ok || step < 1 {
				return false
			}
		}
		if parts[0] == "*" {
			continue
		}
		bounds := strings.Split(parts[0], "-")
		switch len(bounds) {
		case 1:
			if _, ok := parseCronNumber(bounds[0], minimum, maximum); !ok {
				return false
			}
		case 2:
			start, startOK := parseCronNumber(bounds[0], minimum, maximum)
			end, endOK := parseCronNumber(bounds[1], minimum, maximum)
			if !startOK || !endOK || start > end {
				return false
			}
		default:
			return false
		}
	}
	return field != ""
}

func parseCronNumber(value string, minimum, maximum int) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed >= minimum && parsed <= maximum
}

func validIssueEventTime(issue Issue, at time.Time) bool {
	return !at.IsZero() && !at.Before(issue.UpdatedAt) && !at.Before(issue.LastVerifiedAt)
}

func normalizeReference(value string, required bool) (string, error) {
	return knowledge.NormalizeReference(value, required)
}

func validateIdempotencyKey(value string) error {
	normalized, err := normalizeReference(value, true)
	if err != nil || normalized != value || len(value) > maxIdempotencyKeyBytes {
		return invalid(ErrorCodeIssueDecisionInvalid, "idempotency key is not canonical")
	}
	return nil
}

func objectRefKey(value ObjectRef) string {
	return string(value.Type) + ":" + string(value.ID)
}

func marshalSHA256(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", inconsistent(ErrorCodeIssueInvalid, "failed to marshal canonical payload")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneTime(value time.Time) *time.Time {
	copyValue := value
	return &copyValue
}
