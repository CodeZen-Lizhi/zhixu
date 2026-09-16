package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisBodyRefreshRequest 汇集同一目标和上游发布的全部已保存 P/C 影响基线。
// 准备阶段必须独立读取目标的最新可编辑版本；ImpactID 只记录溯源，不能作为冻结目标基线。
type SynthesisBodyRefreshRequest struct {
	ID            foundation.ID `json:"id"`
	WorkspaceID   foundation.ID `json:"workspace_id"`
	NoteID        foundation.ID `json:"note_id"`
	PublicationID foundation.ID `json:"publication_id"`
	ImpactID      foundation.ID `json:"impact_id"`
	CreatedAt     time.Time     `json:"created_at"`
}

// MarshalJSON 使冻结输入和模型请求哈希不受数据库会话时区影响，
// 重放时重建的请求也遵守此规则。
func (request SynthesisBodyRefreshRequest) MarshalJSON() ([]byte, error) {
	type wire SynthesisBodyRefreshRequest
	request.CreatedAt = request.CreatedAt.UTC()
	return json.Marshal(wire(request))
}

func (request SynthesisBodyRefreshRequest) Validate() error {
	if !validID(request.ID) || !validID(request.WorkspaceID) || !validID(request.NoteID) || !validID(request.PublicationID) || !validID(request.ImpactID) || request.CreatedAt.IsZero() {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis body refresh request is invalid")
	}
	return nil
}

// GroupKey 不受创建请求的具体发布版或候选版观察影响。
func (request SynthesisBodyRefreshRequest) GroupKey() (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(string(request.WorkspaceID) + ":" + string(request.NoteID) + ":" + string(request.PublicationID)))
	return "synthesis-body-refresh-group:" + hex.EncodeToString(sum[:]), nil
}

type SynthesisBodyRefreshReconciler interface {
	ReconcileSynthesisBodyRefreshRequests(context.Context, int) (int, error)
}

// SynthesisBodyRefreshPreparation 是所属模块的快照，不是模型证明。
// 生成前仍须完成范围准入和来源打开；应用时须在写入事务内核对目标与当前版本。
type SynthesisBodyRefreshPreparation struct {
	Request SynthesisBodyRefreshRequest
	Target  SynthesisGenerationNote
	Items   []SynthesisBodyRefreshItem
}

type SynthesisBodyRefreshItem struct {
	Impact   SynthesisBodyImpact
	Original domain.SynthesisRevision
	Updated  domain.SynthesisRevision
}

type SynthesisBodyRefreshPreparer interface {
	PrepareSynthesisBodyRefresh(context.Context, foundation.ID, foundation.ID) (SynthesisBodyRefreshPreparation, error)
}

type SynthesisBodyRefreshGenerationSeed struct {
	BodyRefreshRequestID foundation.ID
	SourceEvent          domain.SynthesisSourceReady
}

// SynthesisBodyRefreshBinding 只持久保存身份标识；
// 每个模型阶段开始前，会从对应历史修订重新读取不可变条目正文。
type SynthesisBodyRefreshBinding struct {
	Request SynthesisBodyRefreshRequest       `json:"request"`
	Items   []SynthesisBodyRefreshItemBinding `json:"items"`
}

type SynthesisBodyRefreshItemBinding struct {
	ImpactID foundation.ID                 `json:"impact_id"`
	ItemID   foundation.ID                 `json:"item_id"`
	Original domain.SynthesisBodyReference `json:"original"`
	Updated  domain.SynthesisBodyReference `json:"updated"`
}

func (binding *SynthesisBodyRefreshBinding) Validate(workspaceID foundation.ID) error {
	if binding == nil {
		return nil
	}
	if binding.Request.Validate() != nil || binding.Request.WorkspaceID != workspaceID || len(binding.Items) == 0 || len(binding.Items) > domain.MaxSynthesisItems {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis body refresh binding is invalid")
	}
	seen := map[foundation.ID]bool{}
	for _, item := range binding.Items {
		if !validID(item.ImpactID) || !validID(item.ItemID) || seen[item.ItemID] || item.Original.Validate(workspaceID) != nil || item.Updated.Validate(workspaceID) != nil ||
			item.Original.NoteID != item.Updated.NoteID || item.Original.ItemID != item.Updated.ItemID || item.Original.NoteID == binding.Request.NoteID || item.Original.RevisionID == item.Updated.RevisionID || item.Updated.PublicationID != binding.Request.PublicationID {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis body refresh item binding is invalid")
		}
		seen[item.ItemID] = true
	}
	return nil
}

func validateBodyRefreshInput(input SynthesisGenerationInput) error {
	if input.BodyRefresh == nil {
		if len(input.BodyRefreshRevisions) != 0 {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "refresh revisions lack request")
		}
		return nil
	}
	binding := input.BodyRefresh
	workspace := input.SourceEvent.Source.WorkspaceID
	if binding.Validate(workspace) != nil || input.Goal != nil || input.SourceEvent.Fusion != nil || len(input.Notes) != 1 || input.Notes[0].Note.ID != binding.Request.NoteID || input.Notes[0].Anchor == nil {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "refresh input target changed")
	}
	wanted := map[foundation.ID]domain.SynthesisBodyReference{}
	for _, item := range binding.Items {
		wanted[item.Original.RevisionID] = item.Original
		wanted[item.Updated.RevisionID] = item.Updated
	}
	if len(wanted) != len(input.BodyRefreshRevisions) {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "refresh revision set changed")
	}
	seen := map[foundation.ID]bool{}
	for _, revision := range input.BodyRefreshRevisions {
		ref, ok := wanted[revision.ID]
		if !ok || seen[revision.ID] || revision.Validate() != nil || revision.WorkspaceID != workspace || revision.NoteID != ref.NoteID || revision.Hash != ref.ProjectionHash {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "refresh historical revision changed")
		}
		seen[revision.ID] = true
	}
	for _, item := range binding.Items {
		original, updated := bodyRefreshRevisions(input, item)
		if _, err := domain.RefreshSynthesisPublishedItem(workspace, input.Notes[0].Revision.Items, item.ItemID, original, updated, item.Original.PublicationID, item.Updated.PublicationID); err != nil {
			return err
		}
	}
	return nil
}

func bodyRefreshRevisions(input SynthesisGenerationInput, binding SynthesisBodyRefreshItemBinding) (domain.SynthesisRevision, domain.SynthesisRevision) {
	var original, updated domain.SynthesisRevision
	for _, revision := range input.BodyRefreshRevisions {
		if revision.ID == binding.Original.RevisionID {
			original = revision
		}
		if revision.ID == binding.Updated.RevisionID {
			updated = revision
		}
	}
	return original, updated
}

// BodyRefreshOperation 将冻结目标解析为服务端复制的完整条目。
// 模型返回的文本不能成为替换正文。
func BodyRefreshOperation(input SynthesisGenerationInput, target foundation.ID) (domain.SynthesisOperation, error) {
	if input.BodyRefresh == nil {
		return domain.SynthesisOperation{}, invalid(ErrorCodeSynthesisGenerationInvalid, "refresh request is missing")
	}
	for _, binding := range input.BodyRefresh.Items {
		if binding.ItemID != target {
			continue
		}
		_, updated := bodyRefreshRevisions(input, binding)
		op, err := domain.IncludeSynthesisPublishedItem(updated, binding.Updated.PublicationID, binding.Updated.ItemID, target)
		if err != nil {
			return domain.SynthesisOperation{}, err
		}
		op.Kind, op.TargetItemID = domain.SynthesisRefreshItem, target
		return op, op.Validate(input.SourceEvent.Source.WorkspaceID)
	}
	return domain.SynthesisOperation{}, invalid(ErrorCodeSynthesisGenerationInvalid, "refresh target is outside impact group")
}

func SynthesisBodyRefreshPublications(binding *SynthesisBodyRefreshBinding) []SynthesisPublicationBinding {
	var result []SynthesisPublicationBinding
	if binding == nil {
		return result
	}
	seen := map[foundation.ID]bool{}
	for _, item := range binding.Items {
		for _, ref := range []domain.SynthesisBodyReference{item.Original, item.Updated} {
			if seen[ref.RevisionID] {
				continue
			}
			seen[ref.RevisionID] = true
			result = append(result, SynthesisPublicationBinding{WorkspaceID: ref.WorkspaceID, NoteID: ref.NoteID, RevisionID: ref.RevisionID, PublicationID: ref.PublicationID, ProjectionHash: ref.ProjectionHash})
		}
	}
	return result
}
