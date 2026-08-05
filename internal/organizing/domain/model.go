// Package domain defines mutable Suggested Material Set and immutable Workflow input facts.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// DraftStatus 表示 Suggested Material Set 是否仍可编辑。
type DraftStatus string

const (
	// DraftEditing 表示用户仍可修改 intent 与材料。
	DraftEditing DraftStatus = "EDITING"
	// DraftConfirmed 表示用户已显式确认且 Snapshot 已冻结。
	DraftConfirmed DraftStatus = "CONFIRMED"
)

// Draft 是服务端拥有的可恢复 Suggested Material Set。
type Draft struct {
	ID                  foundation.ID   `json:"id"`
	WorkspaceID         foundation.ID   `json:"workspace_id"`
	Intent              string          `json:"intent"`
	TemplateRevisionID  foundation.ID   `json:"template_revision_id,omitempty"`
	Status              DraftStatus     `json:"status"`
	Materials           []DraftMaterial `json:"materials"`
	ConfirmedSnapshotID foundation.ID   `json:"confirmed_snapshot_id,omitempty"`
	Version             int64           `json:"version"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// DraftMaterial 是 Draft 内尚未授权生成的候选。
type DraftMaterial struct {
	ID           foundation.ID          `json:"id"`
	DraftID      foundation.ID          `json:"draft_id"`
	Ref          MaterialRef            `json:"reference"`
	Title        string                 `json:"title"`
	Reasons      []SuggestionReasonCode `json:"reasons"`
	Origin       MaterialOrigin         `json:"origin"`
	Availability MaterialAvailability   `json:"availability"`
	Score        float64                `json:"score"`
	Selected     bool                   `json:"selected"`
	Position     int                    `json:"position"`
	CreatedAt    time.Time              `json:"created_at"`
}

// Snapshot 是确认后不可改写的 Workflow input identity。
type Snapshot struct {
	ID                 foundation.ID `json:"id"`
	WorkspaceID        foundation.ID `json:"workspace_id"`
	DraftID            foundation.ID `json:"draft_id"`
	DraftVersion       int64         `json:"draft_version"`
	TemplateID         foundation.ID `json:"template_id"`
	TemplateRevisionID foundation.ID `json:"template_revision_id"`
	TemplateHash       string        `json:"template_hash"`
	Intent             string        `json:"intent"`
	Materials          []MaterialRef `json:"materials"`
	Hash               string        `json:"hash"`
	CreatedAt          time.Time     `json:"created_at"`
}

// RunBinding 是 Snapshot 与 Workflow Run 的 immutable binding。
type RunBinding struct {
	ID                foundation.ID `json:"id"`
	WorkspaceID       foundation.ID `json:"workspace_id"`
	SnapshotID        foundation.ID `json:"snapshot_id"`
	WorkflowRunID     foundation.ID `json:"workflow_run_id"`
	DefinitionKey     string        `json:"definition_key"`
	DefinitionVersion int64         `json:"definition_version"`
	CreatedAt         time.Time     `json:"created_at"`
}

// RunResult 是 Workflow 完成后绑定到 Organizing Run 的不可变结果事实。
type RunResult struct {
	ID            foundation.ID `json:"id"`
	WorkspaceID   foundation.ID `json:"workspace_id"`
	RunBindingID  foundation.ID `json:"run_binding_id"`
	SnapshotID    foundation.ID `json:"snapshot_id"`
	WorkflowRunID foundation.ID `json:"workflow_run_id"`
	NodeRunID     foundation.ID `json:"node_run_id"`
	Kind          ResultKind    `json:"kind"`
	ResultRef     foundation.ID `json:"result_ref"`
	ResultHash    string        `json:"result_hash"`
	CreatedAt     time.Time     `json:"created_at"`
}

// NewDraft 创建 version 1 的可恢复整理草稿。
func NewDraft(id, workspaceID foundation.ID, intent string, createdAt time.Time) (Draft, error) {
	createdAt = canonicalTime(createdAt)
	intent, ok := canonicalText(intent, 4096, false)
	if !ok {
		return Draft{}, invalid(ErrorCodeDraftInvalid, "organizing intent is invalid")
	}
	draft := Draft{ID: id, WorkspaceID: workspaceID, Intent: intent, Status: DraftEditing, Materials: []DraftMaterial{}, Version: 1, CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := draft.Validate(); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

// Validate 检查 Draft 身份、版本、状态和材料集合。
func (draft Draft) Validate() error {
	intent, intentOK := canonicalText(draft.Intent, 4096, false)
	templateRevisionOK := draft.TemplateRevisionID == "" || validID(draft.TemplateRevisionID)
	statusOK := (draft.Status == DraftEditing && draft.ConfirmedSnapshotID == "") || (draft.Status == DraftConfirmed && validID(draft.ConfirmedSnapshotID))
	if !validID(draft.ID) || !validID(draft.WorkspaceID) || !intentOK || intent != draft.Intent || !templateRevisionOK || !statusOK ||
		draft.Version < 1 || draft.CreatedAt.IsZero() || canonicalTime(draft.CreatedAt) != draft.CreatedAt ||
		canonicalTime(draft.UpdatedAt) != draft.UpdatedAt || draft.UpdatedAt.Before(draft.CreatedAt) || len(draft.Materials) > MaxDraftMaterials {
		return invalid(ErrorCodeDraftInvalid, "organizing draft fields are invalid")
	}
	seen := make(map[string]struct{}, len(draft.Materials))
	for index, material := range draft.Materials {
		if err := material.Validate(); err != nil || material.DraftID != draft.ID || material.Position != index {
			return invalid(ErrorCodeDraftInvalid, "organizing draft material binding is invalid")
		}
		identity, _ := material.Ref.IdentityKey()
		if _, duplicate := seen[identity]; duplicate {
			return invalid(ErrorCodeDraftInvalid, "organizing draft material is duplicated")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

// Validate 检查候选材料展示与 owner 解析结果。
func (material DraftMaterial) Validate() error {
	title, titleOK := canonicalText(material.Title, 512, false)
	if !validID(material.ID) || !validID(material.DraftID) || !titleOK || title != material.Title ||
		len(material.Reasons) < 1 || len(material.Reasons) > 8 || !material.Origin.Valid() || !material.Availability.Valid() ||
		material.Score < 0 || material.Score > 1 || material.Position < 0 || material.CreatedAt.IsZero() || canonicalTime(material.CreatedAt) != material.CreatedAt {
		return invalid(ErrorCodeMaterialInvalid, "draft material fields are invalid")
	}
	if err := material.Ref.Validate(); err != nil {
		return err
	}
	seen := make(map[SuggestionReasonCode]struct{}, len(material.Reasons))
	for _, reason := range material.Reasons {
		if !reason.Valid() {
			return invalid(ErrorCodeMaterialInvalid, "draft material reason is invalid")
		}
		if _, duplicate := seen[reason]; duplicate {
			return invalid(ErrorCodeMaterialInvalid, "draft material reason is duplicated")
		}
		seen[reason] = struct{}{}
	}
	return nil
}

// ReplaceMaterials applies a full expected-version replacement without granting generation authorization.
func ReplaceMaterials(current Draft, expectedVersion int64, materials []DraftMaterial, updatedAt time.Time) (Draft, error) {
	if err := current.Validate(); err != nil {
		return Draft{}, err
	}
	if current.Status != DraftEditing || current.Version != expectedVersion {
		return Draft{}, conflict("organizing draft expected version is stale")
	}
	if len(materials) > MaxDraftMaterials {
		return Draft{}, invalid(ErrorCodeMaterialInvalid, "too many organizing draft materials")
	}
	next := current
	next.Materials = make([]DraftMaterial, len(materials))
	for index, material := range materials {
		material.DraftID = current.ID
		material.Position = index
		if material.CreatedAt.IsZero() {
			material.CreatedAt = canonicalTime(updatedAt)
		}
		canonicalRef, err := CanonicalMaterialRef(material.Ref)
		if err != nil {
			return Draft{}, err
		}
		material.Ref = canonicalRef
		next.Materials[index] = material
	}
	next.Version++
	next.UpdatedAt = canonicalTime(updatedAt)
	if next.UpdatedAt.Before(current.UpdatedAt) {
		return Draft{}, invalid(ErrorCodeDraftInvalid, "organizing draft update time is invalid")
	}
	if err := next.Validate(); err != nil {
		return Draft{}, err
	}
	return next, nil
}

// UpdateDraft applies one intent and Template Revision expected-version transition.
func UpdateDraft(current Draft, expectedVersion int64, intent string, templateRevisionID foundation.ID, updatedAt time.Time) (Draft, error) {
	if err := current.Validate(); err != nil {
		return Draft{}, err
	}
	if current.Status != DraftEditing || current.Version != expectedVersion {
		return Draft{}, conflict("organizing draft expected version is stale")
	}
	intent, ok := canonicalText(intent, 4096, false)
	if !ok || !validID(templateRevisionID) {
		return Draft{}, invalid(ErrorCodeDraftInvalid, "organizing intent or template revision is invalid")
	}
	next := current
	next.Intent = intent
	next.TemplateRevisionID = templateRevisionID
	next.Version++
	next.UpdatedAt = canonicalTime(updatedAt)
	if next.UpdatedAt.Before(current.UpdatedAt) {
		return Draft{}, invalid(ErrorCodeDraftInvalid, "organizing draft update time is invalid")
	}
	if err := next.Validate(); err != nil {
		return Draft{}, err
	}
	return next, nil
}

// Confirm closes a Draft around an exact Template Revision and ordered material Snapshot.
func Confirm(current Draft, expectedVersion int64, snapshotID, templateID, templateRevisionID foundation.ID, templateHash string, materials []MaterialRef, confirmedAt time.Time) (Draft, Snapshot, error) {
	if err := current.Validate(); err != nil {
		return Draft{}, Snapshot{}, err
	}
	if current.Status != DraftEditing || current.Version != expectedVersion {
		return Draft{}, Snapshot{}, conflict("organizing draft cannot be confirmed from the requested version")
	}
	if current.TemplateRevisionID == "" || current.TemplateRevisionID != templateRevisionID {
		return Draft{}, Snapshot{}, conflict("organizing draft template revision changed before confirmation")
	}
	if !validID(snapshotID) || !validID(templateID) || !validID(templateRevisionID) || !isHash(templateHash) || len(materials) < 1 || len(materials) > MaxSnapshotMaterials {
		return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "snapshot identity or material count is invalid")
	}
	selected := make(map[string]MaterialRef, len(current.Materials))
	selectedCollections := make(map[foundation.ID]struct{})
	for _, material := range current.Materials {
		if !material.Selected {
			continue
		}
		if material.Availability != MaterialAvailable {
			return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "selected snapshot material is no longer available")
		}
		canonical, err := CanonicalMaterialRef(material.Ref)
		if err != nil {
			return Draft{}, Snapshot{}, err
		}
		identity, _ := canonical.IdentityKey()
		selected[identity] = canonical
		if canonical.Kind == MaterialSmartCollection {
			selectedCollections[canonical.CollectionID] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "snapshot requires selected draft materials")
	}
	canonicalMaterials := make([]MaterialRef, len(materials))
	seen := make(map[string]struct{}, len(materials))
	presentSelected := make(map[string]struct{}, len(selected))
	for index, material := range materials {
		canonical, err := CanonicalMaterialRef(material)
		if err != nil {
			return Draft{}, Snapshot{}, err
		}
		identity, _ := canonical.IdentityKey()
		if _, duplicate := seen[identity]; duplicate {
			return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "snapshot material identity is duplicated")
		}
		if canonical.OriginCollectionID != "" {
			if _, allowed := selectedCollections[canonical.OriginCollectionID]; !allowed {
				return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "snapshot collection member has no selected origin")
			}
		} else {
			expected, allowed := selected[identity]
			if !allowed {
				return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "snapshot contains an unselected material")
			}
			if !sameMaterialRef(expected, canonical) {
				return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "snapshot material version is stale")
			}
			presentSelected[identity] = struct{}{}
		}
		seen[identity] = struct{}{}
		canonicalMaterials[index] = canonical
	}
	if len(presentSelected) != len(selected) {
		return Draft{}, Snapshot{}, invalid(ErrorCodeSnapshotInvalid, "snapshot omits a selected material")
	}
	snapshot := Snapshot{ID: snapshotID, WorkspaceID: current.WorkspaceID, DraftID: current.ID, DraftVersion: current.Version,
		TemplateID: templateID, TemplateRevisionID: templateRevisionID, TemplateHash: templateHash, Intent: current.Intent,
		Materials: canonicalMaterials, CreatedAt: canonicalTime(confirmedAt)}
	hash, err := ComputeSnapshotHash(snapshot)
	if err != nil {
		return Draft{}, Snapshot{}, err
	}
	snapshot.Hash = hash
	confirmed := current
	confirmed.Status = DraftConfirmed
	confirmed.ConfirmedSnapshotID = snapshot.ID
	confirmed.Version++
	confirmed.UpdatedAt = snapshot.CreatedAt
	if err := confirmed.Validate(); err != nil {
		return Draft{}, Snapshot{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return Draft{}, Snapshot{}, err
	}
	return confirmed, snapshot, nil
}

func sameMaterialRef(left, right MaterialRef) bool {
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftEncoded) == string(rightEncoded)
}

// Validate checks an immutable Workflow input Snapshot and its canonical digest.
func (snapshot Snapshot) Validate() error {
	intent, intentOK := canonicalText(snapshot.Intent, 4096, false)
	if !validID(snapshot.ID) || !validID(snapshot.WorkspaceID) || !validID(snapshot.DraftID) || snapshot.DraftVersion < 1 ||
		!validID(snapshot.TemplateID) || !validID(snapshot.TemplateRevisionID) || !isHash(snapshot.TemplateHash) ||
		!intentOK || intent != snapshot.Intent || len(snapshot.Materials) < 1 || len(snapshot.Materials) > MaxSnapshotMaterials ||
		!isHash(snapshot.Hash) || snapshot.CreatedAt.IsZero() || canonicalTime(snapshot.CreatedAt) != snapshot.CreatedAt {
		return invalid(ErrorCodeSnapshotInvalid, "workflow input snapshot fields are invalid")
	}
	computed, err := computeSnapshotHash(snapshot)
	if err != nil || computed != snapshot.Hash {
		return invalid(ErrorCodeSnapshotInvalid, "workflow input snapshot hash is invalid")
	}
	return nil
}

// ComputeSnapshotHash returns a stable digest and ignores the Hash field itself.
func ComputeSnapshotHash(snapshot Snapshot) (string, error) {
	return computeSnapshotHash(snapshot)
}

func computeSnapshotHash(snapshot Snapshot) (string, error) {
	if !validID(snapshot.WorkspaceID) || !validID(snapshot.DraftID) || snapshot.DraftVersion < 1 || !validID(snapshot.TemplateID) ||
		!validID(snapshot.TemplateRevisionID) || !isHash(snapshot.TemplateHash) || len(snapshot.Materials) < 1 || len(snapshot.Materials) > MaxSnapshotMaterials {
		return "", invalid(ErrorCodeSnapshotInvalid, "snapshot hash input is invalid")
	}
	materials := make([]MaterialRef, len(snapshot.Materials))
	for index, material := range snapshot.Materials {
		canonical, err := CanonicalMaterialRef(material)
		if err != nil {
			return "", err
		}
		materials[index] = canonical
	}
	encoded, err := json.Marshal(struct {
		Schema             string        `json:"schema"`
		WorkspaceID        foundation.ID `json:"workspace_id"`
		DraftID            foundation.ID `json:"draft_id"`
		DraftVersion       int64         `json:"draft_version"`
		TemplateID         foundation.ID `json:"template_id"`
		TemplateRevisionID foundation.ID `json:"template_revision_id"`
		TemplateHash       string        `json:"template_hash"`
		Intent             string        `json:"intent"`
		Materials          []MaterialRef `json:"materials"`
		CreatedAt          time.Time     `json:"created_at"`
	}{"organizing-snapshot/v1", snapshot.WorkspaceID, snapshot.DraftID, snapshot.DraftVersion, snapshot.TemplateID,
		snapshot.TemplateRevisionID, snapshot.TemplateHash, snapshot.Intent, materials, canonicalTime(snapshot.CreatedAt)})
	if err != nil {
		return "", invalid(ErrorCodeSnapshotInvalid, "snapshot hash encoding failed")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Validate checks immutable Run binding identity.
func (binding RunBinding) Validate() error {
	definition, ok := canonicalText(binding.DefinitionKey, 128, false)
	if !validID(binding.ID) || !validID(binding.WorkspaceID) || !validID(binding.SnapshotID) || !validID(binding.WorkflowRunID) ||
		!ok || definition != binding.DefinitionKey || binding.DefinitionVersion < 1 || binding.CreatedAt.IsZero() || canonicalTime(binding.CreatedAt) != binding.CreatedAt {
		return invalid(ErrorCodeSnapshotInvalid, "organizing run binding fields are invalid")
	}
	return nil
}

// Validate checks an immutable Workflow result binding.
func (result RunResult) Validate() error {
	if !validID(result.ID) || !validID(result.WorkspaceID) || !validID(result.RunBindingID) ||
		!validID(result.SnapshotID) || !validID(result.WorkflowRunID) || !validID(result.NodeRunID) ||
		(result.Kind != ResultArtifact && result.Kind != ResultMergeProposal) || !validID(result.ResultRef) || !isHash(result.ResultHash) ||
		result.CreatedAt.IsZero() || canonicalTime(result.CreatedAt) != result.CreatedAt {
		return invalid(ErrorCodeSnapshotInvalid, "organizing run result fields are invalid")
	}
	return nil
}

// Valid reports whether a reason is registered.
func (reason SuggestionReasonCode) Valid() bool {
	return reason == ReasonHybridMatch || reason == ReasonProfileMatch || reason == ReasonAliasMatch || reason == ReasonFormalKnowledge || reason == ReasonCollectionMember || reason == ReasonUserAdded
}

// Valid reports whether a material origin is registered.
func (origin MaterialOrigin) Valid() bool {
	return origin == MaterialOriginSuggested || origin == MaterialOriginUser
}

// Valid reports whether an availability state is registered.
func (availability MaterialAvailability) Valid() bool {
	return availability == MaterialAvailable || availability == MaterialStale || availability == MaterialUnavailable
}
