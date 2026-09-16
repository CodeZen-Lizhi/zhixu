package domain

import (
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SynthesisBodyReference 记录对一个精确已发布条目的显式包含关系。
// 仅共享原始来源不会建立这种关系。
type SynthesisBodyReference struct {
	WorkspaceID    foundation.ID `json:"workspace_id"`
	NoteID         foundation.ID `json:"note_id"`
	RevisionID     foundation.ID `json:"revision_id"`
	PublicationID  foundation.ID `json:"publication_id"`
	ItemID         foundation.ID `json:"item_id"`
	ProjectionHash string        `json:"projection_hash"`
}

func (reference SynthesisBodyReference) Validate(workspaceID foundation.ID) error {
	if !validID(workspaceID) || reference.WorkspaceID != workspaceID || !validID(reference.NoteID) ||
		!validID(reference.RevisionID) || !validID(reference.PublicationID) || !validID(reference.ItemID) || !isHash(reference.ProjectionHash) {
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis published item reference is invalid")
	}
	return nil
}

// IncludeSynthesisPublishedItem 深复制完整条目，包括适用条件、冲突观点和原始证据。
// PublicationID 是所属模块的绑定，持久化时仍须另行证明；
// 它不是调用方提供的权限，也不能代替独立语义复核。
func IncludeSynthesisPublishedItem(revision SynthesisRevision, publicationID, itemID, newItemID foundation.ID) (SynthesisOperation, error) {
	if revision.Validate() != nil || !validID(publicationID) || !validID(newItemID) || newItemID == itemID {
		return SynthesisOperation{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis published item binding is invalid")
	}
	for _, item := range revision.Items {
		if item.ID != itemID {
			continue
		}
		included := cloneSynthesisItem(item)
		included.ID = newItemID
		included.BodyReference = &SynthesisBodyReference{WorkspaceID: revision.WorkspaceID, NoteID: revision.NoteID,
			RevisionID: revision.ID, PublicationID: publicationID, ItemID: item.ID, ProjectionHash: revision.Hash}
		kind := map[SynthesisItemKind]SynthesisOperationKind{SynthesisFactItem: SynthesisAddFact, SynthesisConflictItem: SynthesisAddConflict, SynthesisGapItem: SynthesisAddGap}[item.Kind]
		operation := SynthesisOperation{Kind: kind, Item: &included}
		return operation, operation.Validate(revision.WorkspaceID)
	}
	return SynthesisOperation{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis published item is missing")
}

// MatchesSynthesisBodyItem 证明精确包含关系，而非仅有语义相似。
// 后续本地补源或缺口解决可以与冻结引用共存；此比较用于增量首次引入该关系时。
func MatchesSynthesisBodyItem(included SynthesisItem, revision SynthesisRevision, publicationID foundation.ID) bool {
	ref := included.BodyReference
	if ref == nil || ref.WorkspaceID != revision.WorkspaceID || ref.NoteID != revision.NoteID ||
		ref.RevisionID != revision.ID || ref.ProjectionHash != revision.Hash || ref.PublicationID != publicationID {
		return false
	}
	operation, err := IncludeSynthesisPublishedItem(revision, publicationID, ref.ItemID, included.ID)
	return err == nil && reflect.DeepEqual(included, *operation.Item)
}
