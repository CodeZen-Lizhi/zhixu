package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SynthesisCandidateRemergeTarget 仅包含服务端读取的身份标识，Begin 会重新核验。
type SynthesisCandidateRemergeTarget struct {
	WorkspaceID                foundation.ID `json:"workspace_id"`
	NoteID                     foundation.ID `json:"note_id"`
	ExpectedRevisionID         foundation.ID `json:"expected_revision_id"`
	ExpectedNoteVersion        int64         `json:"expected_note_version"`
	ExpectedDocumentID         foundation.ID `json:"expected_document_id"`
	ExpectedDocumentVersion    int64         `json:"expected_document_version"`
	ExpectedPublicationID      foundation.ID `json:"expected_publication_id"`
	ExpectedProposalID         foundation.ID `json:"expected_proposal_id"`
	ExpectedProposalRevisionID foundation.ID `json:"expected_proposal_revision_id"`
	ExpectedProposalVersion    int64         `json:"expected_proposal_version"`
}

type BeginSynthesisCandidateRemerge struct {
	WorkspaceID                foundation.ID `json:"workspace_id"`
	NoteID                     foundation.ID `json:"note_id"`
	ExpectedRevisionID         foundation.ID `json:"expected_revision_id"`
	ExpectedNoteVersion        int64         `json:"expected_note_version"`
	ExpectedDocumentID         foundation.ID `json:"expected_document_id"`
	ExpectedDocumentVersion    int64         `json:"expected_document_version"`
	ExpectedPublicationID      foundation.ID `json:"expected_publication_id"`
	ExpectedProposalID         foundation.ID `json:"expected_proposal_id"`
	ExpectedProposalRevisionID foundation.ID `json:"expected_proposal_revision_id"`
	ExpectedProposalVersion    int64         `json:"expected_proposal_version"`
	IdempotencyKey             string        `json:"idempotency_key"`
}

type ReadSynthesisCandidateRemerge struct {
	WorkspaceID    foundation.ID `json:"workspace_id"`
	NoteID         foundation.ID `json:"note_id"`
	IdempotencyKey string        `json:"idempotency_key"`
}

type ApplySynthesisCandidateRemerge struct {
	WorkspaceID    foundation.ID                  `json:"workspace_id"`
	NoteID         foundation.ID                  `json:"note_id"`
	AttemptID      foundation.ID                  `json:"attempt_id"`
	IdempotencyKey string                         `json:"idempotency_key"`
	Resolution     *SynthesisManuscriptResolution `json:"resolution,omitempty"`
}

// ResumeSynthesisCandidateRemerge 仅恢复已经应用的发布操作。
type ResumeSynthesisCandidateRemerge struct {
	WorkspaceID foundation.ID `json:"workspace_id"`
	NoteID      foundation.ID `json:"note_id"`
	AttemptID   foundation.ID `json:"attempt_id"`
}

type SynthesisCandidateRemergeResult struct {
	RevisionID         foundation.ID `json:"revision_id"`
	ArticleRevisionID  foundation.ID `json:"article_revision_id"`
	PublicationID      foundation.ID `json:"publication_id,omitempty"`
	ProposalID         foundation.ID `json:"proposal_id,omitempty"`
	ProposalRevisionID foundation.ID `json:"proposal_revision_id,omitempty"`
}

type SynthesisCandidateRemergeReview struct {
	WorkspaceID foundation.ID `json:"workspace_id"`
	NoteID      foundation.ID `json:"note_id"`
	AttemptID   foundation.ID `json:"attempt_id"`
	State       string        `json:"state"`
	// READY 时 Candidate 是精确、完整的合并后全文；其他状态下不存在。
	Candidate          string                           `json:"candidate,omitempty"`
	PreviewFingerprint string                           `json:"preview_fingerprint"`
	Review             *SynthesisManuscriptMergeReview  `json:"review,omitempty"`
	Result             *SynthesisCandidateRemergeResult `json:"result,omitempty"`
	Replayed           bool                             `json:"replayed"`
}

type SynthesisCandidateRemergeOwner interface {
	Target(context.Context, foundation.ID, foundation.ID) (SynthesisCandidateRemergeTarget, error)
	Begin(context.Context, BeginSynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)
	Read(context.Context, ReadSynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)
	Apply(context.Context, ApplySynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)
	Resume(context.Context, ResumeSynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)
}
