package domain

import (
	"encoding/json"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func (revision SynthesisRevision) Validate() error {
	hash, err := ComputeSynthesisRevisionHash(revision)
	if err != nil {
		return err
	}
	if !isHash(revision.Hash) || hash != revision.Hash {
		return invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision projection hash is invalid")
	}
	return nil
}

// ComputeSynthesisRevisionHash validates the exact rendered ArticleRevision
// content and hashes all immutable projection fields, ignoring only Hash itself.
func ComputeSynthesisRevisionHash(revision SynthesisRevision) (string, error) {
	if !validID(revision.ID) || !validID(revision.WorkspaceID) || !validID(revision.NoteID) || !validID(revision.DocumentID) ||
		!validID(revision.ArticleRevisionID) || revision.RevisionNo < 1 || revision.RevisionNo > MaxSynthesisRevisionNo ||
		revision.ArticleRevisionNo < 1 || revision.ArticleRevisionNo > MaxSynthesisRevisionNo ||
		(revision.RevisionNo == 1 && revision.ParentRevisionID != "") ||
		(revision.RevisionNo > 1 && (!validID(revision.ParentRevisionID) || revision.ParentRevisionID == revision.ID)) ||
		!isHash(revision.ContentHash) ||
		!validID(revision.SourceEventID) || !validID(revision.WorkflowRunID) || !validID(revision.ModelRunID) ||
		revision.CreatedAt.IsZero() || canonicalTime(revision.CreatedAt) != revision.CreatedAt ||
		len(revision.Delta.Operations) == 0 {
		return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision binding is incomplete")
	}
	if err := revision.Delta.Validate(revision.WorkspaceID); err != nil {
		return "", err
	}
	for _, item := range revision.Items {
		if item.BodyReference != nil && item.BodyReference.NoteID == revision.NoteID {
			return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis note cannot include its own body")
		}
	}
	content, err := revision.Content()
	if err != nil {
		return "", err
	}
	if synthesisHash([]byte(content)) != revision.ContentHash {
		return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision does not match its article content hash")
	}
	if h := revision.HistoricalRepublish; h != nil {
		if revision.Remerge != nil || !validID(h.AttemptID) || !validID(h.SelectedRevisionID) || h.SelectedRevisionID == revision.ID || revision.RevisionNo < 2 ||
			((h.SelectedPublicationID == "") != (h.SelectedProposalCommitID == "")) || (h.SelectedPublicationID != "" && (!validID(h.SelectedPublicationID) || !validID(h.SelectedProposalCommitID))) {
			return "", manuscriptInvalid("invalid historical republish provenance")
		}
	}
	if revision.Remerge != nil && (revision.RendererVersion != SynthesisRendererVersionV2 || !validID(revision.Remerge.AttemptID) || revision.Remerge.SourceRevisionID != revision.ParentRevisionID) {
		return "", manuscriptInvalid("invalid remerge provenance")
	}
	schema := SynthesisRevisionSchema
	if revision.RendererVersion == SynthesisRendererVersionV2 {
		schema = SynthesisRevisionSchemaV2
	}
	revision.Hash = ""
	// 旧修订省略可选溯源字段。V1 保留原字段顺序和 Schema，并拒绝包含全文对象；
	// 精确的人工复制可以携带历史溯源，不改变选中版本的渲染器或内容。
	encoded, err := json.Marshal(struct {
		Schema   string            `json:"schema"`
		Revision SynthesisRevision `json:"revision"`
	}{schema, revision})
	if err != nil {
		return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision cannot be encoded")
	}
	return synthesisHash(encoded), nil
}

// SynthesisSnapshotFromRevision deep-copies an exact validated revision. This
// pure conversion makes no claim about publication; the owner reader must prove
// published status before exposing it as interview material.
func SynthesisSnapshotFromRevision(revision SynthesisRevision) (SynthesisNoteSnapshot, error) {
	if err := revision.Validate(); err != nil {
		return SynthesisNoteSnapshot{}, err
	}
	snapshot := SynthesisNoteSnapshot{
		WorkspaceID: revision.WorkspaceID, NoteID: revision.NoteID, RevisionID: revision.ID, RevisionNo: revision.RevisionNo,
		DocumentID: revision.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, ArticleRevisionNo: revision.ArticleRevisionNo,
		ContentHash: revision.ContentHash, ProjectionHash: revision.Hash, Title: revision.Title, RendererVersion: revision.RendererVersion,
		Items: revision.Items, Manuscript: revision.Manuscript,
	}
	if revision.RendererVersion == SynthesisRendererVersion {
		// 同时保留历史 v1 快照的切片规范化规则。
		snapshot.Items = cloneSynthesisItems(revision.Items)
		return snapshot, nil
	}
	// JSON 值复制保留参与哈希的 nil 与空值区别，
	// 包括所有嵌套条目、来源、映射、复核和排除项切片。
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return SynthesisNoteSnapshot{}, manuscriptInvalid("cannot copy synthesis snapshot")
	}
	var copied SynthesisNoteSnapshot
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return SynthesisNoteSnapshot{}, manuscriptInvalid("cannot copy synthesis snapshot")
	}
	return copied, nil
}

func (snapshot SynthesisNoteSnapshot) Validate() error {
	if !validID(snapshot.WorkspaceID) || !validID(snapshot.NoteID) || !validID(snapshot.RevisionID) || !validID(snapshot.DocumentID) ||
		!validID(snapshot.ArticleRevisionID) || snapshot.RevisionNo < 1 || snapshot.RevisionNo > MaxSynthesisRevisionNo ||
		snapshot.ArticleRevisionNo < 1 || snapshot.ArticleRevisionNo > MaxSynthesisRevisionNo || !isHash(snapshot.ContentHash) ||
		!isHash(snapshot.ProjectionHash) {
		return invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis note snapshot binding is incomplete")
	}
	content, err := snapshot.Content()
	if err != nil {
		return err
	}
	if synthesisHash([]byte(content)) != snapshot.ContentHash {
		return invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis note snapshot content changed")
	}
	return nil
}

// Content 返回绑定到版本的精确 Article 内容。它检查封装完整性，
// 不检查解析、来源或回执证明，这些须由所属模块另行验证。
// 它也不验证修订投影 Hash，此检查由 Validate 执行。
func (revision SynthesisRevision) Content() (string, error) {
	return synthesisRevisionMarkdown(revision.WorkspaceID, revision.NoteID, revision.Title, revision.RendererVersion, revision.ContentHash, revision.Items, revision.Manuscript)
}

// Content 不会从可能为空的 Items 重建 v2 全文。
// 与快照 Validate 一样，它不能证明发布或 ProjectionHash。
func (snapshot SynthesisNoteSnapshot) Content() (string, error) {
	return synthesisRevisionMarkdown(snapshot.WorkspaceID, snapshot.NoteID, snapshot.Title, snapshot.RendererVersion, snapshot.ContentHash, snapshot.Items, snapshot.Manuscript)
}

func synthesisRevisionMarkdown(workspaceID, noteID foundation.ID, title, version, contentHash string, items []SynthesisItem, manuscript *SynthesisManuscript) (string, error) {
	var content string
	switch version {
	case SynthesisRendererVersion:
		if manuscript != nil || len(items) == 0 {
			return "", manuscriptInvalid("v1 cannot contain manuscript or empty items")
		}
		var err error
		content, err = RenderSynthesisMarkdown(workspaceID, noteID, title, items)
		if err != nil {
			return "", err
		}
	case SynthesisRendererVersionV2:
		if manuscript == nil {
			return "", manuscriptInvalid("v2 manuscript missing")
		}
		if err := manuscript.ValidateIntegrity(); err != nil {
			return "", err
		}
		if manuscript.Machine.WorkspaceID != workspaceID || manuscript.Machine.NoteID != noteID || !synthesisText(title, MaxSynthesisTitleBytes, false) {
			return "", manuscriptInvalid("v2 manuscript owner or title mismatch")
		}
		// 这里只检查子集内部的一致性。客户端提供的映射不会因通过此检查而变得更可信；
		// 接受任何 v2 修订前，仍必须由所属模块验证解析、来源和回执。
		mapped := make(map[foundation.ID]bool, len(manuscript.Assessment.Mappings))
		for _, m := range manuscript.Assessment.Mappings {
			mapped[m.ItemID] = true
		}
		index := 0
		for _, item := range manuscript.Machine.MachineItems {
			if !mapped[item.ID] {
				continue
			}
			if index >= len(items) || !reflect.DeepEqual(items[index], item) {
				return "", manuscriptInvalid("v2 items differ from mapped machine subset")
			}
			index++
		}
		if index != len(items) {
			return "", manuscriptInvalid("v2 items contain unmapped machine content")
		}
		content = manuscript.FullContent
	default:
		return "", manuscriptInvalid("unsupported synthesis renderer version")
	}
	if synthesisHash([]byte(content)) != contentHash {
		return "", manuscriptInvalid("synthesis content hash mismatch")
	}
	return content, nil
}
