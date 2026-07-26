package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const maxGenerationSectionKeyBytes = 128

// SectionGenerationStatus is the durable lifecycle of one frozen section workflow.
type SectionGenerationStatus string

const (
	// SectionGenerationPending means a Workflow owns the frozen section but has not published a Revision.
	SectionGenerationPending SectionGenerationStatus = "PENDING"
	// SectionGenerationCompleted means the Artifact Revision and Model Run were finalized atomically.
	SectionGenerationCompleted SectionGenerationStatus = "COMPLETED"
	// SectionGenerationFailed 表示 Workflow 已安全失败，可用新的幂等键重新生成。
	SectionGenerationFailed SectionGenerationStatus = "FAILED"
	// SectionGenerationCancelled 表示 Workflow 已安全取消，可用新的幂等键重新生成。
	SectionGenerationCancelled SectionGenerationStatus = "CANCELLED"
	// SectionGenerationRecoveryRequired 表示模型或终结结果不确定，禁止自动重新生成。
	SectionGenerationRecoveryRequired SectionGenerationStatus = "RECOVERY_REQUIRED"
)

// OwnsSourceSlot 返回该状态是否继续独占冻结来源章节。
func (status SectionGenerationStatus) OwnsSourceSlot() bool {
	return status == SectionGenerationPending || status == SectionGenerationRecoveryRequired
}

// StartSectionGenerationCommand freezes one current Artifact section into a Workflow request.
type StartSectionGenerationCommand struct {
	WorkspaceID     foundation.ID
	ArtifactID      foundation.ID
	ExpectedVersion int64
	SectionKey      string
	IdempotencyKey  string
}

// SectionGeneration is the durable binding between an Artifact Revision and a Workflow Run.
type SectionGeneration struct {
	ID                      foundation.ID
	WorkspaceID             foundation.ID
	ArtifactID              foundation.ID
	SourceRevisionID        foundation.ID
	SourceRevisionNo        int64
	SourceArtifactVersion   int64
	SectionKey              string
	IdempotencyKey          string
	RequestHash             string
	WorkflowRunID           foundation.ID
	NodeRunID               foundation.ID
	Status                  SectionGenerationStatus
	ModelRunID              *foundation.ID
	RecordedBaseRevisionID  *foundation.ID
	RecordedRevisionID      *foundation.ID
	RecordedArtifactVersion *int64
	ContentHash             string
	FailureClass            string
	ErrorCode               string
	ErrorSummary            string
	Version                 int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
	CompletedAt             *time.Time
	TerminalAt              *time.Time
}

// SectionGenerationSnapshot 是 Repository 在同一快照中读取的当前 Revision
// 及其可生成章节的权威 Generation 投影。
type SectionGenerationSnapshot struct {
	State State
	Items []SectionGeneration
}

// SectionGenerationList is the public application read model for refresh recovery.
type SectionGenerationList struct {
	WorkspaceID foundation.ID
	ArtifactID  foundation.ID
	Items       []SectionGeneration
}

// StartSectionGenerationResult returns the authoritative binding and exact replay marker.
type StartSectionGenerationResult struct {
	Generation SectionGeneration
	Replayed   bool
}

// SectionGenerationStarter atomically starts or exactly replays a frozen Artifact Workflow.
type SectionGenerationStarter interface {
	StartSectionGeneration(context.Context, StartSectionGenerationCommand) (StartSectionGenerationResult, error)
}

// ListSectionGenerations 返回当前可生成章节各自唯一的权威 Generation。
func (service *QueryService) ListSectionGenerations(
	ctx context.Context,
	workspaceID, artifactID foundation.ID,
) (SectionGenerationList, error) {
	if service == nil || service.repository == nil {
		return SectionGenerationList{}, unavailable("artifact query service is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || !validID(artifactID) {
		return SectionGenerationList{}, requestInvalid("artifact section generation lookup is invalid")
	}
	snapshot, err := service.repository.ListSectionGenerations(ctx, workspaceID, artifactID)
	if err != nil {
		return SectionGenerationList{}, err
	}
	if err := ValidateSectionGenerationSnapshot(snapshot, workspaceID, artifactID); err != nil {
		return SectionGenerationList{}, err
	}
	result := SectionGenerationList{
		WorkspaceID: workspaceID,
		ArtifactID:  artifactID,
		Items:       make([]SectionGeneration, len(snapshot.Items)),
	}
	copy(result.Items, snapshot.Items)
	if err := ValidateSectionGenerationList(result); err != nil {
		return SectionGenerationList{}, err
	}
	return result, nil
}

// SectionGenerationProjectionKeys 返回需要暴露 Generation 恢复状态的章节键。
// 修订中的 Artifact 允许替换已有章节，因此包含完整大纲；其他状态只包含未记录章节。
func SectionGenerationProjectionKeys(state State) ([]string, error) {
	if err := ValidateState(state); err != nil {
		return nil, resultInconsistent("artifact generation snapshot state is invalid")
	}
	recorded := make(map[string]struct{}, len(state.Revision.Sections))
	for _, section := range state.Revision.Sections {
		recorded[section.Key] = struct{}{}
	}
	keys := make([]string, 0, len(state.Revision.Outline))
	for _, section := range state.Revision.Outline {
		_, exists := recorded[section.Key]
		if state.Artifact.Status == domain.StatusGenerating || !exists {
			keys = append(keys, section.Key)
		}
	}
	return keys, nil
}

// ValidateSectionGenerationSnapshot verifies repository ordering and current
// Revision membership before the read model crosses the application boundary.
func ValidateSectionGenerationSnapshot(
	snapshot SectionGenerationSnapshot,
	workspaceID, artifactID foundation.ID,
) error {
	if err := ValidateState(snapshot.State); err != nil ||
		snapshot.State.Artifact.WorkspaceID != workspaceID || snapshot.State.Artifact.ID != artifactID {
		return resultInconsistent("artifact generation snapshot crossed request binding")
	}
	if snapshot.Items == nil {
		return resultInconsistent("artifact generation snapshot items are nil")
	}
	keys, err := SectionGenerationProjectionKeys(snapshot.State)
	if err != nil {
		return err
	}
	order := make(map[string]int, len(keys))
	for index, key := range keys {
		order[key] = index
	}
	previous := -1
	for _, generation := range snapshot.Items {
		index, exists := order[generation.SectionKey]
		if err := ValidateSectionGeneration(generation); err != nil || !exists || index <= previous ||
			generation.WorkspaceID != workspaceID || generation.ArtifactID != artifactID ||
			generation.Status == SectionGenerationCompleted ||
			generation.SourceRevisionNo > snapshot.State.Revision.RevisionNo ||
			generation.SourceArtifactVersion > snapshot.State.Artifact.Version ||
			(generation.SourceRevisionNo == snapshot.State.Revision.RevisionNo && generation.SourceRevisionID != snapshot.State.Revision.ID) {
			return resultInconsistent("artifact generation snapshot contains an invalid item")
		}
		previous = index
	}
	return nil
}

// ValidateSectionGenerationList verifies the public read model is explicit,
// workspace-bound, non-completed and unique by section.
func ValidateSectionGenerationList(result SectionGenerationList) error {
	if !validID(result.WorkspaceID) || !validID(result.ArtifactID) || result.Items == nil {
		return resultInconsistent("artifact generation list binding is invalid")
	}
	seen := make(map[string]struct{}, len(result.Items))
	for _, generation := range result.Items {
		if err := ValidateSectionGeneration(generation); err != nil ||
			generation.WorkspaceID != result.WorkspaceID || generation.ArtifactID != result.ArtifactID ||
			generation.Status == SectionGenerationCompleted {
			return resultInconsistent("artifact generation list contains an invalid item")
		}
		if _, duplicate := seen[generation.SectionKey]; duplicate {
			return resultInconsistent("artifact generation list contains a duplicate section")
		}
		seen[generation.SectionKey] = struct{}{}
	}
	return nil
}

// ValidateStartSectionGenerationCommand validates only caller-owned command fields.
func ValidateStartSectionGenerationCommand(command StartSectionGenerationCommand) error {
	if !validID(command.WorkspaceID) || !validID(command.ArtifactID) || command.WorkspaceID == command.ArtifactID ||
		command.ExpectedVersion < 1 || !validGenerationSectionKey(command.SectionKey) {
		return requestInvalid("artifact section generation command is invalid")
	}
	if _, err := normalizeIdempotencyKey(command.IdempotencyKey); err != nil {
		return err
	}
	return nil
}

// ValidateSectionGenerationSource proves that a request still targets the current approved outline.
func ValidateSectionGenerationSource(state State, expectedVersion int64, sectionKey string) (domain.OutlineSection, error) {
	if err := ValidateState(state); err != nil {
		return domain.OutlineSection{}, resultInconsistent("artifact generation source state is invalid")
	}
	if expectedVersion < 1 || state.Artifact.Version != expectedVersion {
		return domain.OutlineSection{}, versionConflict("artifact generation expected version is stale")
	}
	if state.Artifact.Status != domain.StatusGenerating {
		return domain.OutlineSection{}, versionConflict("artifact must be generating before a section workflow can start")
	}
	if !validGenerationSectionKey(sectionKey) {
		return domain.OutlineSection{}, requestInvalid("artifact generation section key is invalid")
	}
	for _, section := range state.Revision.Outline {
		if section.Key == sectionKey {
			return section, nil
		}
	}
	return domain.OutlineSection{}, requestInvalid("artifact generation section is not in the approved outline")
}

// ValidateSectionGenerationRebase proves that a frozen source can be applied to the current Revision.
// Other sections may have advanced, but the approved outline and target section must remain unchanged.
func ValidateSectionGenerationRebase(source domain.Revision, current State, sectionKey string) (domain.OutlineSection, error) {
	if err := domain.ValidateRevision(source); err != nil {
		return domain.OutlineSection{}, resultInconsistent("artifact generation source revision is invalid")
	}
	if err := ValidateState(current); err != nil {
		return domain.OutlineSection{}, resultInconsistent("artifact generation current state is invalid")
	}
	if !validGenerationSectionKey(sectionKey) {
		return domain.OutlineSection{}, requestInvalid("artifact generation section key is invalid")
	}
	if source.ArtifactID != current.Artifact.ID || source.RevisionNo > current.Revision.RevisionNo ||
		(source.RevisionNo == current.Revision.RevisionNo && source.ID != current.Revision.ID) ||
		current.Artifact.Status != domain.StatusGenerating {
		return domain.OutlineSection{}, versionConflict("artifact generation source cannot be rebased onto the current revision")
	}
	if !reflect.DeepEqual(source.Outline, current.Revision.Outline) {
		return domain.OutlineSection{}, versionConflict("artifact generation outline changed after workflow start")
	}
	target, found := outlineSectionByKey(source.Outline, sectionKey)
	if !found {
		return domain.OutlineSection{}, requestInvalid("artifact generation section is not in the approved outline")
	}
	sourceSection, sourceFound := artifactSectionByKey(source.Sections, sectionKey)
	currentSection, currentFound := artifactSectionByKey(current.Revision.Sections, sectionKey)
	if sourceFound != currentFound || (sourceFound && !reflect.DeepEqual(sourceSection, currentSection)) {
		return domain.OutlineSection{}, versionConflict("artifact generation target section changed after workflow start")
	}
	return target, nil
}

// ComputeSectionGenerationRequestHash binds the external command to its frozen Artifact identity.
func ComputeSectionGenerationRequestHash(command StartSectionGenerationCommand) (string, error) {
	if err := ValidateStartSectionGenerationCommand(command); err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion   string        `json:"schema_version"`
		WorkspaceID     foundation.ID `json:"workspace_id"`
		ArtifactID      foundation.ID `json:"artifact_id"`
		ExpectedVersion int64         `json:"expected_version"`
		SectionKey      string        `json:"section_key"`
	}{
		SchemaVersion: "artifact-section-generation-request/v1", WorkspaceID: command.WorkspaceID,
		ArtifactID: command.ArtifactID, ExpectedVersion: command.ExpectedVersion, SectionKey: command.SectionKey,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeResultInconsistent, false, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateSectionGeneration validates persisted lifecycle and cross-field bindings.
func ValidateSectionGeneration(generation SectionGeneration) error {
	ids := []foundation.ID{
		generation.ID, generation.WorkspaceID, generation.ArtifactID, generation.SourceRevisionID,
		generation.WorkflowRunID, generation.NodeRunID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids)+2)
	for _, id := range ids {
		if !validID(id) {
			return resultInconsistent("artifact section generation identity is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return resultInconsistent("artifact section generation identity is reused")
		}
		seen[id] = struct{}{}
	}
	if generation.SourceRevisionNo < 1 || generation.SourceArtifactVersion < 1 || generation.Version < 1 ||
		!validGenerationSectionKey(generation.SectionKey) || generation.IdempotencyKey == "" ||
		generation.IdempotencyKey != strings.TrimSpace(generation.IdempotencyKey) || len(generation.IdempotencyKey) > maxIdempotencyKeyBytes ||
		!validHash(generation.RequestHash) || generation.CreatedAt.IsZero() || generation.UpdatedAt.IsZero() ||
		generation.UpdatedAt.Before(generation.CreatedAt) {
		return resultInconsistent("artifact section generation binding is invalid")
	}
	switch generation.Status {
	case SectionGenerationPending:
		if generation.Version != 1 || generation.ModelRunID != nil || generation.RecordedBaseRevisionID != nil || generation.RecordedRevisionID != nil || generation.RecordedArtifactVersion != nil ||
			generation.ContentHash != "" || generation.FailureClass != "" || generation.ErrorCode != "" || generation.ErrorSummary != "" ||
			generation.CompletedAt != nil || generation.TerminalAt != nil {
			return resultInconsistent("pending artifact generation contains terminal facts")
		}
	case SectionGenerationCompleted:
		if generation.Version != 2 || generation.ModelRunID == nil || generation.RecordedBaseRevisionID == nil || generation.RecordedRevisionID == nil || generation.RecordedArtifactVersion == nil ||
			!validID(*generation.ModelRunID) || !validID(*generation.RecordedBaseRevisionID) || !validID(*generation.RecordedRevisionID) ||
			*generation.RecordedArtifactVersion <= generation.SourceArtifactVersion ||
			!validHash(generation.ContentHash) || generation.FailureClass != "" || generation.ErrorCode != "" || generation.ErrorSummary != "" ||
			generation.CompletedAt == nil || generation.CompletedAt.IsZero() || generation.TerminalAt == nil || generation.TerminalAt.IsZero() ||
			!generation.CompletedAt.Equal(generation.UpdatedAt) || !generation.TerminalAt.Equal(generation.UpdatedAt) {
			return resultInconsistent("completed artifact generation is incomplete")
		}
		if *generation.RecordedBaseRevisionID != generation.SourceRevisionID {
			if _, duplicate := seen[*generation.RecordedBaseRevisionID]; duplicate {
				return resultInconsistent("artifact section generation terminal identity is reused")
			}
			seen[*generation.RecordedBaseRevisionID] = struct{}{}
		}
		for _, id := range []foundation.ID{*generation.ModelRunID, *generation.RecordedRevisionID} {
			if _, duplicate := seen[id]; duplicate {
				return resultInconsistent("artifact section generation terminal identity is reused")
			}
			seen[id] = struct{}{}
		}
	case SectionGenerationFailed, SectionGenerationCancelled, SectionGenerationRecoveryRequired:
		if generation.Version != 2 || generation.RecordedBaseRevisionID != nil || generation.RecordedRevisionID != nil || generation.RecordedArtifactVersion != nil ||
			generation.ContentHash != "" || generation.CompletedAt != nil || generation.TerminalAt == nil || generation.TerminalAt.IsZero() ||
			!generation.TerminalAt.Equal(generation.UpdatedAt) ||
			!validSectionGenerationFailure(generation.FailureClass, generation.ErrorCode, generation.ErrorSummary) {
			return resultInconsistent("terminal artifact generation failure facts are invalid")
		}
		if generation.Status == SectionGenerationFailed && generation.FailureClass != "non_retryable" ||
			generation.Status == SectionGenerationCancelled && generation.FailureClass != "cancelled" ||
			generation.Status == SectionGenerationRecoveryRequired && generation.FailureClass != "manual_recovery" {
			return resultInconsistent("terminal artifact generation failure class is invalid")
		}
		if generation.ModelRunID != nil {
			if !validID(*generation.ModelRunID) {
				return resultInconsistent("terminal artifact generation model run is invalid")
			}
			if _, duplicate := seen[*generation.ModelRunID]; duplicate {
				return resultInconsistent("artifact section generation terminal identity is reused")
			}
		}
	default:
		return resultInconsistent("artifact section generation status is invalid")
	}
	return nil
}

func validSectionGenerationFailure(class, code, summary string) bool {
	switch class {
	case "retryable", "non_retryable", "manual_recovery", "lease_lost", "cancelled":
	default:
		return false
	}
	if code == "" || code != strings.TrimSpace(code) || len(code) > 128 || !utf8.ValidString(code) {
		return false
	}
	for _, character := range code {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return summary != "" && summary == strings.TrimSpace(summary) && utf8.ValidString(summary) && utf8.RuneCountInString(summary) <= 256
}

func validGenerationSectionKey(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxGenerationSectionKeyBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func outlineSectionByKey(outline []domain.OutlineSection, key string) (domain.OutlineSection, bool) {
	for _, section := range outline {
		if section.Key == key {
			return section, true
		}
	}
	return domain.OutlineSection{}, false
}

func artifactSectionByKey(sections []domain.Section, key string) (domain.Section, bool) {
	for _, section := range sections {
		if section.Key == key {
			return section, true
		}
	}
	return domain.Section{}, false
}
