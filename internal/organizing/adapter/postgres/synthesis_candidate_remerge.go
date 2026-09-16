package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

type synthesisCandidateRemerge struct {
	runtime *SynthesisManuscriptRuntime
	caller  app.SynthesisManuscriptCaller
}

type candidateRemergeEvent struct {
	ID             string `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string `gorm:"column:workspace_id;type:uuid"`
	NoteID         string `gorm:"column:note_id;type:uuid"`
	AttemptID      string `gorm:"column:attempt_id;type:uuid"`
	Kind           string `gorm:"column:kind"`
	IdempotencyKey string `gorm:"column:idempotency_key"`
	Payload        []byte `gorm:"column:payload;type:bytea"`
	PayloadHash    string `gorm:"column:payload_hash"`
}

func (candidateRemergeEvent) TableName() string {
	return "organizing.synthesis_candidate_remerge_event"
}

type candidateRemergeAttempt struct {
	ID                   foundation.ID                       `json:"id"`
	Command              app.BeginSynthesisCandidateRemerge  `json:"command"`
	Original             domain.SynthesisRevision            `json:"original"`
	ReceiptID            foundation.ID                       `json:"receipt_id"`
	BaseCapture          app.SynthesisManuscriptCapture      `json:"base_capture"`
	Capture              app.SynthesisManuscriptCapture      `json:"capture"`
	PublishedRevisionID  foundation.ID                       `json:"published_revision_id"`
	PublishedContentHash string                              `json:"published_content_hash"`
	Preview              app.SynthesisManuscriptMergePreview `json:"preview"`
	CreatedAt            time.Time                           `json:"created_at"`
}

type candidateRemergeApplied struct {
	Command       app.ApplySynthesisCandidateRemerge `json:"command"`
	Revision      domain.SynthesisRevision           `json:"revision"`
	Publication   app.SynthesisPublicationCommand    `json:"publication"`
	ReservationID foundation.ID                      `json:"reservation_id"`
}

func (r *SynthesisManuscriptRuntime) CandidateRemerge(caller app.SynthesisManuscriptCaller) (app.SynthesisCandidateRemergeOwner, error) {
	if err := caller.Authorize(); err != nil {
		return nil, err
	}
	return &synthesisCandidateRemerge{r, caller}, nil
}
func (h *synthesisCandidateRemerge) authorize(ctx context.Context, scope foundation.TransactionScope, workspace foundation.ID) (app.SynthesisManuscriptRoot, error) {
	if err := h.caller.Authorize(); err != nil {
		return app.SynthesisManuscriptRoot{}, err
	}
	if !validID(workspace) {
		return app.SynthesisManuscriptRoot{}, manuscriptStoreInvalid("invalid workspace")
	}
	return h.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, workspace)
}
func validRemergeKey(s string) bool { return s != "" && len(s) <= 200 && strings.TrimSpace(s) == s }
func validRemergeBegin(c app.BeginSynthesisCandidateRemerge) bool {
	for _, id := range []foundation.ID{c.WorkspaceID, c.NoteID, c.ExpectedRevisionID, c.ExpectedDocumentID, c.ExpectedPublicationID, c.ExpectedProposalID, c.ExpectedProposalRevisionID} {
		if !validID(id) {
			return false
		}
	}
	return c.ExpectedNoteVersion > 0 && c.ExpectedDocumentVersion > 0 && c.ExpectedProposalVersion > 0 && validRemergeKey(c.IdempotencyKey)
}
func (h *synthesisCandidateRemerge) within(ctx context.Context, fn func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	return h.runtime.dependencies.Candidates.within(ctx, foundation.TransactionOptions{}, fn)
}
func (h *synthesisCandidateRemerge) insert(tx *gorm.DB, id, workspace, note, attempt foundation.ID, kind, key string, value any) error {
	raw, err := encodeManuscriptRecord(value)
	if err != nil {
		return err
	}
	return tx.Create(&candidateRemergeEvent{ID: string(id), WorkspaceID: string(workspace), NoteID: string(note), AttemptID: string(attempt), Kind: kind, IdempotencyKey: key, Payload: raw, PayloadHash: manuscriptBytesHash(raw)}).Error
}
func readRemergeEvent(tx *gorm.DB, workspace, note foundation.ID, kind string, key string, id foundation.ID, out any) (bool, error) {
	q := tx.Where("workspace_id=? AND note_id=? AND kind=?", string(workspace), string(note), kind)
	if id != "" {
		q = q.Where("attempt_id=?", string(id))
	} else {
		q = q.Where("idempotency_key=?", key)
	}
	var row candidateRemergeEvent
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.PayloadHash != manuscriptBytesHash(row.Payload) || json.Unmarshal(row.Payload, out) != nil {
		return false, synthesisConsistency("invalid candidate remerge event")
	}
	return true, nil
}
func (h *synthesisCandidateRemerge) current(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, c app.BeginSynthesisCandidateRemerge) (domain.SynthesisRevision, authoringapp.GeneratedDocumentState, error) {
	var empty domain.SynthesisRevision
	var owner authoringapp.GeneratedDocumentState
	note, err := loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, true)
	if err != nil {
		return empty, owner, err
	}
	if note.CurrentRevisionID != c.ExpectedRevisionID || note.Version != c.ExpectedNoteVersion || note.DocumentID != c.ExpectedDocumentID {
		return empty, owner, manuscriptStoreConflict("candidate owner changed")
	}
	revision, err := loadSynthesisRevision(tx, c.WorkspaceID, c.NoteID, note.CurrentRevisionID)
	if err != nil {
		return empty, owner, err
	}
	if revision.HistoricalRepublish != nil {
		return empty, owner, manuscriptStoreInvalid("historical candidate requires a new exact historical preview")
	}
	if revision.Manuscript == nil {
		return empty, owner, manuscriptStoreInvalid("candidate has no captured manuscript")
	}
	owner, err = h.runtime.dependencies.Candidates.dependencies.Authoring.ReadGeneratedDocumentScoped(ctx, scope, c.WorkspaceID, c.ExpectedDocumentID, c.NoteID)
	if err != nil {
		return empty, owner, err
	}
	if owner.Document.Version != c.ExpectedDocumentVersion || owner.Revision.ID != revision.ArticleRevisionID || owner.Revision.ContentHash != revision.ContentHash || owner.Publication == nil || owner.Publication.ID != c.ExpectedPublicationID || (owner.Publication.Status != authoringdomain.PublicationPending && !(owner.Publication.Status == authoringdomain.PublicationClosed && owner.Publication.ErrorCode == "AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION")) || owner.Publication.ProposalID != c.ExpectedProposalID || owner.Publication.ProposalRevisionID != c.ExpectedProposalRevisionID || owner.ProposalVersion != c.ExpectedProposalVersion {
		return empty, owner, manuscriptStoreConflict("candidate publication binding changed")
	}
	if err = verifyManuscriptPendingState(tx, c.WorkspaceID, c.ExpectedDocumentID, revision.ArticleRevisionID, true); err != nil {
		return empty, owner, err
	}
	return revision, owner, nil
}
func (h *synthesisCandidateRemerge) Begin(ctx context.Context, c app.BeginSynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	var out app.SynthesisCandidateRemergeReview
	if !validRemergeBegin(c) {
		return out, manuscriptStoreInvalid("invalid candidate remerge command")
	}
	err := h.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, scope, c.WorkspaceID)
		if err != nil {
			return err
		}
		// 将同一笔记的开始操作串行化，并在幂等恢复前验证作用域权限。
		if _, err = loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, true); err != nil {
			return err
		}
		var prior candidateRemergeAttempt
		found, err := readRemergeEvent(tx, c.WorkspaceID, c.NoteID, "BEGIN", c.IdempotencyKey, "", &prior)
		if err != nil {
			return err
		}
		if found {
			if !reflect.DeepEqual(prior.Command, c) {
				return synthesisConflict("candidate remerge idempotency binding differs")
			}
			if err = verifyRemergeRoot(prior, root); err != nil {
				return err
			}
			out, err = h.project(tx, prior, true)
			return err
		}
		revision, owner, err := h.current(ctx, scope, tx, c)
		if err != nil {
			return err
		}
		receiptID, base, err := candidateRemergeBase(tx, c.WorkspaceID, c.NoteID, revision)
		if err != nil {
			return err
		}
		if !base.Exists || owner.Document.CurrentPublishedRevisionID == "" {
			return manuscriptStoreConflict("remerge requires an existing published file")
		}
		id, err := h.runtime.dependencies.Storage.IDs.New()
		if err != nil {
			return err
		}
		captureID, err := h.runtime.dependencies.Storage.IDs.New()
		if err != nil {
			return err
		}
		at := canonicalTime(h.runtime.dependencies.Storage.Clock.Now())
		capture := app.SynthesisManuscriptCapture{ID: captureID, WorkspaceID: c.WorkspaceID, TargetPath: owner.Document.CanonicalPath, RootGrantID: root.GrantID, RootFingerprint: root.Fingerprint, WorkspaceBindingVersion: root.BindingVersion, Exists: true, CreatedAt: at}
		capture.Bytes, capture.ContentHash, err = h.runtime.dependencies.Storage.Files.CurrentContent(ctx, c.WorkspaceID, capture.TargetPath, domain.MaxSynthesisManuscriptBytes)
		if err != nil {
			return err
		}
		if err = validateManuscriptCapture(capture); err != nil {
			return err
		}
		var published struct{ ContentHash string }
		res := tx.Raw(`SELECT content_hash FROM core.article_revision WHERE workspace_id=? AND document_id=? AND id=? AND status='PUBLISHED'`, string(c.WorkspaceID), string(c.ExpectedDocumentID), string(owner.Document.CurrentPublishedRevisionID)).Scan(&published)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return manuscriptStoreConflict("published baseline missing")
		}
		preview, err := app.PreviewSynthesisCandidateRemerge(ctx, revision, string(base.Bytes), string(capture.Bytes), h.runtime.dependencies.Storage.Merge, h.runtime.dependencies.Storage.Mapper, nil)
		if err != nil {
			return err
		}
		attempt := candidateRemergeAttempt{ID: id, Command: c, Original: revision, ReceiptID: receiptID, BaseCapture: base, Capture: capture, PublishedRevisionID: owner.Document.CurrentPublishedRevisionID, PublishedContentHash: published.ContentHash, Preview: preview, CreatedAt: at}
		if err = h.verifyFile(ctx, attempt); err != nil {
			return err
		}
		raw, err := encodeManuscriptRecord(capture)
		if err != nil {
			return err
		}
		if err = tx.Create(&manuscriptCaptureRow{ID: string(capture.ID), WorkspaceID: string(c.WorkspaceID), Payload: raw, PayloadHash: manuscriptBytesHash(raw)}).Error; err != nil {
			return err
		}
		if err = h.insert(tx, id, c.WorkspaceID, c.NoteID, id, "BEGIN", c.IdempotencyKey, attempt); err != nil {
			return err
		}
		out, err = h.project(tx, attempt, false)
		return err
	})
	return out, err
}
func verifyRemergeRoot(a candidateRemergeAttempt, r app.SynthesisManuscriptRoot) error {
	if a.Capture.RootGrantID != r.GrantID || a.Capture.RootFingerprint != r.Fingerprint || a.Capture.WorkspaceBindingVersion != r.BindingVersion {
		return manuscriptStoreConflict("candidate root changed")
	}
	return nil
}
func (h *synthesisCandidateRemerge) verifyFile(ctx context.Context, a candidateRemergeAttempt) error {
	bytes, hash, err := h.runtime.dependencies.Storage.Files.CurrentContent(ctx, a.Command.WorkspaceID, a.Capture.TargetPath, domain.MaxSynthesisManuscriptBytes)
	if err != nil {
		return err
	}
	if hash != a.Capture.ContentHash || string(bytes) != string(a.Capture.Bytes) {
		return manuscriptStoreConflict("candidate file changed since preview")
	}
	return nil
}
func (h *synthesisCandidateRemerge) project(tx *gorm.DB, a candidateRemergeAttempt, replayed bool) (app.SynthesisCandidateRemergeReview, error) {
	out := app.SynthesisCandidateRemergeReview{WorkspaceID: a.Command.WorkspaceID, NoteID: a.Command.NoteID, AttemptID: a.ID, State: "READY", PreviewFingerprint: a.Preview.Fingerprint, Review: a.Preview.Review, Replayed: replayed}
	if out.Review != nil {
		out.State = "CONFLICTS"
	} else if a.Preview.Manuscript != nil {
		out.Candidate = a.Preview.Manuscript.FullContent
	} else {
		return app.SynthesisCandidateRemergeReview{}, synthesisConsistency("candidate remerge preview has no manuscript")
	}
	var applied candidateRemergeApplied
	found, err := readRemergeEvent(tx, a.Command.WorkspaceID, a.Command.NoteID, "APPLY", "", a.ID, &applied)
	if err != nil {
		return out, err
	}
	if !found {
		return out, nil
	}
	out.State = "APPLIED"
	out.Candidate = ""
	out.Review = nil
	out.Result = &app.SynthesisCandidateRemergeResult{RevisionID: applied.Revision.ID, ArticleRevisionID: applied.Revision.ArticleRevisionID}
	var b struct {
		ID                 string
		ProposalID         string
		ProposalRevisionID string
	}
	q := tx.Raw(`SELECT id,proposal_id,proposal_revision_id FROM authoring.document_publication_binding WHERE workspace_id=? AND document_id=? AND article_revision_id=?`, string(a.Command.WorkspaceID), string(a.Command.ExpectedDocumentID), string(applied.Revision.ArticleRevisionID)).Scan(&b)
	if q.Error != nil {
		return out, q.Error
	}
	if q.RowsAffected == 1 {
		out.Result.PublicationID = foundation.ID(b.ID)
		out.Result.ProposalID = foundation.ID(b.ProposalID)
		out.Result.ProposalRevisionID = foundation.ID(b.ProposalRevisionID)
	}
	return out, nil
}
func (h *synthesisCandidateRemerge) Read(ctx context.Context, c app.ReadSynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	var out app.SynthesisCandidateRemergeReview
	if !validID(c.NoteID) || !validRemergeKey(c.IdempotencyKey) {
		return out, manuscriptStoreInvalid("invalid remerge read")
	}
	err := h.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, scope, c.WorkspaceID)
		if err != nil {
			return err
		}
		if _, err = loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, false); err != nil {
			return err
		}
		var a candidateRemergeAttempt
		found, err := readRemergeEvent(tx, c.WorkspaceID, c.NoteID, "BEGIN", c.IdempotencyKey, "", &a)
		if err != nil {
			return err
		}
		if !found {
			return synthesisNotFound()
		}
		if err = verifyRemergeRoot(a, root); err != nil {
			return err
		}
		out, err = h.project(tx, a, true)
		return err
	})
	return out, err
}

func candidateRemergeBase(tx *gorm.DB, workspace, note foundation.ID, revision domain.SynthesisRevision) (foundation.ID, app.SynthesisManuscriptCapture, error) {
	var base app.SynthesisManuscriptCapture
	var row struct {
		ReceiptID string
		Capture   []byte
		Status    string
	}
	result := tx.Raw(`SELECT r.manuscript_receipt_id::text AS receipt_id,cp.payload AS capture,p.status FROM organizing.synthesis_revision r JOIN organizing.synthesis_manuscript_receipt mr ON mr.id=r.manuscript_receipt_id AND mr.workspace_id=r.workspace_id JOIN organizing.synthesis_manuscript_attempt a ON a.id=mr.attempt_id AND a.workspace_id=r.workspace_id JOIN organizing.synthesis_manuscript_capture cp ON cp.id=a.capture_id AND cp.workspace_id=r.workspace_id JOIN organizing.synthesis_processing p ON p.id=(convert_from(a.payload,'UTF8')::jsonb->'command'->>'processing_id')::uuid AND p.workspace_id=r.workspace_id WHERE r.id=? AND r.workspace_id=?`, string(revision.ID), string(workspace)).Scan(&row)
	if result.Error != nil {
		return "", base, result.Error
	}
	if result.RowsAffected != 1 || row.Status != "SUCCEEDED" {
		return "", base, manuscriptStoreConflict("original candidate processing is not complete")
	}
	if json.Unmarshal(row.Capture, &base) != nil || validateManuscriptCapture(base) != nil {
		return "", base, synthesisConsistency("original file capture invalid")
	}
	// 再次合并时，共同祖先取自已重合并候选自己的捕获快照，不使用更早的模型回执。
	if revision.Remerge != nil {
		var previous candidateRemergeAttempt
		found, e := readRemergeEvent(tx, workspace, note, "BEGIN", "", revision.Remerge.AttemptID, &previous)
		if e != nil {
			return "", base, e
		}
		if !found {
			return "", base, synthesisConsistency("missing remerge parent")
		}
		base = previous.Capture
	}
	return foundation.ID(row.ReceiptID), base, nil
}
