package application

import (
	"context"
	"encoding/json"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SynthesisHistoricalRepublishTarget 绑定持久保存的选中修订，
// 不绑定内容哈希或 Git 快照。选中发布为空表示它是旧候选。
type SynthesisHistoricalRepublishTarget struct {
	WorkspaceID                 foundation.ID `json:"workspace_id"`
	NoteID                      foundation.ID `json:"note_id"`
	SelectedRevisionID          foundation.ID `json:"selected_revision_id"`
	SelectedProjectionHash      string        `json:"selected_projection_hash"`
	SelectedPublicationID       foundation.ID `json:"selected_publication_id,omitempty"`
	SelectedProposalCommitID    foundation.ID `json:"selected_proposal_commit_id,omitempty"`
	ExpectedRevisionID          foundation.ID `json:"expected_revision_id"`
	ExpectedNoteVersion         int64         `json:"expected_note_version"`
	ExpectedDocumentID          foundation.ID `json:"expected_document_id"`
	ExpectedDocumentVersion     int64         `json:"expected_document_version"`
	ExpectedPublishedRevisionID foundation.ID `json:"expected_published_revision_id,omitempty"`
	ExpectedPublicationID       foundation.ID `json:"expected_publication_id,omitempty"`
	ExpectedProposalID          foundation.ID `json:"expected_proposal_id,omitempty"`
	ExpectedProposalRevisionID  foundation.ID `json:"expected_proposal_revision_id,omitempty"`
	ExpectedProposalVersion     int64         `json:"expected_proposal_version,omitempty"`
	RequiresRetirement          bool          `json:"requires_retirement"`
}

type BeginSynthesisHistoricalRepublish struct {
	SynthesisHistoricalRepublishTarget
	IdempotencyKey string `json:"idempotency_key"`
}

type ReadSynthesisHistoricalRepublish struct {
	WorkspaceID    foundation.ID `json:"workspace_id"`
	NoteID         foundation.ID `json:"note_id"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	AttemptID      foundation.ID `json:"attempt_id,omitempty"`
}

// Apply 只确认 Begin 冻结的精确选中内容，不接受替换文本、来源集合、画像、
// 模型身份或范围修改。
type ApplySynthesisHistoricalRepublish struct {
	WorkspaceID            foundation.ID `json:"workspace_id"`
	NoteID                 foundation.ID `json:"note_id"`
	AttemptID              foundation.ID `json:"attempt_id"`
	IdempotencyKey         string        `json:"idempotency_key"`
	PreviewFingerprint     string        `json:"preview_fingerprint"`
	ConfirmExactRestore    bool          `json:"confirm_exact_restore"`
	RetireCurrentCandidate bool          `json:"retire_current_candidate"`
}

type ResumeSynthesisHistoricalRepublish struct {
	WorkspaceID foundation.ID `json:"workspace_id"`
	NoteID      foundation.ID `json:"note_id"`
	AttemptID   foundation.ID `json:"attempt_id"`
}

type SynthesisHistoricalRepublishWarning struct {
	Code            string        `json:"code"`
	SourceID        foundation.ID `json:"source_id,omitempty"`
	SourceVersionID foundation.ID `json:"source_version_id,omitempty"`
}

// 三份完整文本支持精确的 F→selected 和 P→selected 差异比较。
// APPLIED 状态表示候选，不代表已审批或已发布。
type SynthesisHistoricalRepublishReview struct {
	CurrentScope       json.RawMessage                       `json:"current_scope"`
	WorkspaceID        foundation.ID                         `json:"workspace_id"`
	NoteID             foundation.ID                         `json:"note_id"`
	AttemptID          foundation.ID                         `json:"attempt_id"`
	State              string                                `json:"state"`
	Target             SynthesisHistoricalRepublishTarget    `json:"target"`
	Candidate          string                                `json:"candidate"`
	CurrentContent     string                                `json:"current_content"`
	PublishedContent   string                                `json:"published_content"`
	PreviewFingerprint string                                `json:"preview_fingerprint"`
	Warnings           []SynthesisHistoricalRepublishWarning `json:"warnings"`
	Result             *SynthesisCandidateRemergeResult      `json:"result,omitempty"`
	Replayed           bool                                  `json:"replayed"`
}

type SynthesisHistoricalRepublishOwner interface {
	Target(context.Context, foundation.ID, foundation.ID, foundation.ID) (SynthesisHistoricalRepublishTarget, error)
	Begin(context.Context, BeginSynthesisHistoricalRepublish) (SynthesisHistoricalRepublishReview, error)
	Read(context.Context, ReadSynthesisHistoricalRepublish) (SynthesisHistoricalRepublishReview, error)
	Apply(context.Context, ApplySynthesisHistoricalRepublish) (SynthesisHistoricalRepublishReview, error)
	Resume(context.Context, ResumeSynthesisHistoricalRepublish) (SynthesisHistoricalRepublishReview, error)
}
