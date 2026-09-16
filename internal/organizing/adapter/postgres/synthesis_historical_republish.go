package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

type synthesisHistoricalRepublish struct {
	runtime *SynthesisManuscriptRuntime
	caller  app.SynthesisManuscriptCaller
}
type historicalRepublishEvent struct {
	ID             string `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string `gorm:"column:workspace_id;type:uuid"`
	NoteID         string `gorm:"column:note_id;type:uuid"`
	AttemptID      string `gorm:"column:attempt_id;type:uuid"`
	Kind           string `gorm:"column:kind"`
	IdempotencyKey string `gorm:"column:idempotency_key"`
	Payload        []byte `gorm:"column:payload;type:bytea"`
	PayloadHash    string `gorm:"column:payload_hash"`
}

func (historicalRepublishEvent) TableName() string {
	return "organizing.synthesis_historical_republish_event"
}

type historicalRepublishAttempt struct {
	ID                   foundation.ID                             `json:"id"`
	Command              app.BeginSynthesisHistoricalRepublish     `json:"command"`
	Selected             domain.SynthesisRevision                  `json:"selected"`
	Latest               domain.SynthesisRevision                  `json:"latest"`
	Capture              app.SynthesisManuscriptCapture            `json:"capture"`
	Scope                json.RawMessage                           `json:"scope"`
	PublishedContent     string                                    `json:"published_content"`
	PublishedContentHash string                                    `json:"published_content_hash"`
	Candidate            string                                    `json:"candidate"`
	Fingerprint          string                                    `json:"fingerprint"`
	Warnings             []app.SynthesisHistoricalRepublishWarning `json:"warnings"`
	CreatedAt            time.Time                                 `json:"created_at"`
}
type historicalRepublishApplied struct {
	Command       app.ApplySynthesisHistoricalRepublish `json:"command"`
	Revision      domain.SynthesisRevision              `json:"revision"`
	Publication   app.SynthesisPublicationCommand       `json:"publication"`
	ReservationID foundation.ID                         `json:"reservation_id"`
	ApplyID       foundation.ID                         `json:"apply_id"`
}

func (r *SynthesisManuscriptRuntime) HistoricalRepublish(caller app.SynthesisManuscriptCaller) (app.SynthesisHistoricalRepublishOwner, error) {
	if err := caller.Authorize(); err != nil {
		return nil, err
	}
	return &synthesisHistoricalRepublish{r, caller}, nil
}
func (h *synthesisHistoricalRepublish) within(ctx context.Context, fn func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	return h.runtime.dependencies.Candidates.within(ctx, foundation.TransactionOptions{}, fn)
}
func (h *synthesisHistoricalRepublish) authorize(ctx context.Context, scope foundation.TransactionScope, w foundation.ID) (app.SynthesisManuscriptRoot, error) {
	if err := h.caller.Authorize(); err != nil {
		return app.SynthesisManuscriptRoot{}, err
	}
	if !validID(w) {
		return app.SynthesisManuscriptRoot{}, manuscriptStoreInvalid("invalid workspace")
	}
	return h.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, w)
}
func readHistoricalEvent(tx *gorm.DB, w, n foundation.ID, kind, key string, id foundation.ID, out any) (bool, error) {
	q := tx.Where("workspace_id=? AND note_id=? AND kind=?", string(w), string(n), kind)
	if id != "" {
		q = q.Where("attempt_id=?", string(id))
	} else {
		q = q.Where("idempotency_key=?", key)
	}
	var row historicalRepublishEvent
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.PayloadHash != manuscriptBytesHash(row.Payload) || json.Unmarshal(row.Payload, out) != nil {
		return false, synthesisConsistency("invalid historical republish event")
	}
	return true, nil
}
func (h *synthesisHistoricalRepublish) insert(tx *gorm.DB, id, w, n, a foundation.ID, kind, key string, v any) error {
	raw, err := encodeManuscriptRecord(v)
	if err != nil {
		return err
	}
	return tx.Create(&historicalRepublishEvent{string(id), string(w), string(n), string(a), kind, key, raw, manuscriptBytesHash(raw)}).Error
}
func historicalScope(tx *gorm.DB, w, n foundation.ID) (json.RawMessage, error) {
	var row struct{ Scope string }
	err := tx.Raw(`SELECT COALESCE((SELECT jsonb_build_object('id',a.id,'scope_version',a.scope_version,'scope',s.scope)::text FROM organizing.knowledge_anchor a JOIN organizing.anchor_scope_revision s ON s.anchor_id=a.id AND s.workspace_id=a.workspace_id AND s.version=a.scope_version WHERE a.workspace_id=? AND a.note_id=?),'null') AS scope`, string(w), string(n)).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, []byte(row.Scope)); err != nil {
		return nil, synthesisConsistency("invalid anchor scope snapshot")
	}
	return json.RawMessage(compact.Bytes()), nil
}
func (h *synthesisHistoricalRepublish) target(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, w, n, selectedID foundation.ID) (app.SynthesisHistoricalRepublishTarget, domain.SynthesisRevision, domain.SynthesisRevision, authoringapp.GeneratedDocumentState, error) {
	var t app.SynthesisHistoricalRepublishTarget
	var selected, latest domain.SynthesisRevision
	var owner authoringapp.GeneratedDocumentState
	note, err := loadSynthesisNote(tx, w, n, true)
	if err != nil {
		return t, selected, latest, owner, err
	}
	if note.Status == domain.SynthesisGenerating || note.Status == domain.SynthesisQueued || note.Status == domain.SynthesisRecoveryRequired {
		return t, selected, latest, owner, manuscriptStoreConflict("note has unfinished generation or recovery")
	}
	selected, err = loadSynthesisRevision(tx, w, n, selectedID)
	if err != nil {
		return t, selected, latest, owner, err
	}
	latest, err = loadSynthesisRevision(tx, w, n, note.CurrentRevisionID)
	if err != nil {
		return t, selected, latest, owner, err
	}
	var proven bool
	if err = tx.Raw(`SELECT organizing.synthesis_historical_revision_proven(?, ?, ?)`, string(w), string(n), string(selectedID)).Scan(&proven).Error; err != nil {
		return t, selected, latest, owner, err
	}
	if !proven {
		return t, selected, latest, owner, synthesisConsistency("selected revision has no complete immutable generation or human-copy proof")
	}
	owner, err = h.runtime.dependencies.Candidates.dependencies.Authoring.ReadGeneratedDocumentScoped(ctx, scope, w, note.DocumentID, n)
	if err != nil {
		return t, selected, latest, owner, err
	}
	if owner.Revision.ID != latest.ArticleRevisionID || owner.Revision.ContentHash != latest.ContentHash {
		return t, selected, latest, owner, manuscriptStoreConflict("latest generated owner changed")
	}
	if err = verifyManuscriptPendingState(tx, w, note.DocumentID, latest.ArticleRevisionID, true); err != nil {
		return t, selected, latest, owner, err
	}
	t = app.SynthesisHistoricalRepublishTarget{WorkspaceID: w, NoteID: n, SelectedRevisionID: selected.ID, SelectedProjectionHash: selected.Hash, ExpectedRevisionID: latest.ID, ExpectedNoteVersion: note.Version, ExpectedDocumentID: note.DocumentID, ExpectedDocumentVersion: owner.Document.Version, ExpectedPublishedRevisionID: owner.Document.CurrentPublishedRevisionID}
	var pub struct{ PublicationID, ProposalCommitID string }
	q := tx.Raw(`SELECT publication_id::text,proposal_commit_id::text FROM organizing.synthesis_proven_publication WHERE workspace_id=? AND note_id=? AND revision_id=?`, string(w), string(n), string(selectedID)).Scan(&pub)
	if q.Error != nil {
		return t, selected, latest, owner, q.Error
	}
	t.SelectedPublicationID = foundation.ID(pub.PublicationID)
	t.SelectedProposalCommitID = foundation.ID(pub.ProposalCommitID)
	if owner.Publication != nil {
		b := owner.Publication
		t.ExpectedPublicationID = b.ID
		t.ExpectedProposalID = b.ProposalID
		t.ExpectedProposalRevisionID = b.ProposalRevisionID
		t.ExpectedProposalVersion = owner.ProposalVersion
	}
	if latest.ArticleRevisionID != owner.Document.CurrentPublishedRevisionID {
		if owner.Publication == nil {
			return t, selected, latest, owner, manuscriptStoreConflict("latest candidate has no reviewable publication")
		}
		t.RequiresRetirement = true
	} else if owner.Publication == nil || owner.Publication.Status != authoringdomain.PublicationPublished {
		return t, selected, latest, owner, manuscriptStoreConflict("current publication is not complete")
	}
	return t, selected, latest, owner, nil
}
func (h *synthesisHistoricalRepublish) Target(ctx context.Context, w, n, selected foundation.ID) (app.SynthesisHistoricalRepublishTarget, error) {
	var out app.SynthesisHistoricalRepublishTarget
	if !validID(n) || !validID(selected) {
		return out, manuscriptStoreInvalid("invalid historical target")
	}
	err := h.within(ctx, func(ctx context.Context, s foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, s, w)
		if err != nil {
			return err
		}
		t, _, _, owner, err := h.target(ctx, s, tx, w, n, selected)
		if err != nil {
			return err
		}
		_, err = h.capture(ctx, w, owner.Document.CanonicalPath, root, n, owner.Document.CurrentPublishedRevisionID != "")
		if err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return app.SynthesisHistoricalRepublishTarget{}, err
	}
	return out, nil
}
func (h *synthesisHistoricalRepublish) capture(ctx context.Context, w foundation.ID, path string, root app.SynthesisManuscriptRoot, id foundation.ID, exists bool) (app.SynthesisManuscriptCapture, error) {
	c := app.SynthesisManuscriptCapture{ID: id, WorkspaceID: w, TargetPath: path, RootGrantID: root.GrantID, RootFingerprint: root.Fingerprint, WorkspaceBindingVersion: root.BindingVersion, Exists: exists, Bytes: []byte{}, CreatedAt: canonicalTime(h.runtime.dependencies.Storage.Clock.Now())}
	var err error
	if !exists {
		c.AbsenceToken, err = authoringdomain.ComputeAbsenceToken(w, path)
		if err != nil {
			return c, err
		}
		if err = h.runtime.dependencies.Storage.Files.EnsureTargetAbsent(ctx, w, path, c.AbsenceToken); err != nil {
			return c, err
		}
		return c, validateManuscriptCapture(c)
	}
	c.Bytes, c.ContentHash, err = h.runtime.dependencies.Storage.Files.CurrentContent(ctx, w, path, domain.MaxSynthesisManuscriptBytes)
	if err != nil {
		return c, err
	}
	return c, validateManuscriptCapture(c)
}
func historicalRoot(a historicalRepublishAttempt, r app.SynthesisManuscriptRoot) error {
	if a.Capture.RootGrantID != r.GrantID || a.Capture.RootFingerprint != r.Fingerprint || a.Capture.WorkspaceBindingVersion != r.BindingVersion {
		return manuscriptStoreConflict("historical republish root changed")
	}
	return nil
}
func (h *synthesisHistoricalRepublish) verifyFile(ctx context.Context, a historicalRepublishAttempt) error {
	if !a.Capture.Exists {
		return h.runtime.dependencies.Storage.Files.EnsureTargetAbsent(ctx, a.Command.WorkspaceID, a.Capture.TargetPath, a.Capture.AbsenceToken)
	}
	bytes, hash, err := h.runtime.dependencies.Storage.Files.CurrentContent(ctx, a.Command.WorkspaceID, a.Capture.TargetPath, domain.MaxSynthesisManuscriptBytes)
	if err != nil {
		return err
	}
	if hash != a.Capture.ContentHash || string(bytes) != string(a.Capture.Bytes) {
		return manuscriptStoreConflict("file changed since historical preview")
	}
	return nil
}
func (h *synthesisHistoricalRepublish) project(tx *gorm.DB, a historicalRepublishAttempt, replayed bool) (app.SynthesisHistoricalRepublishReview, error) {
	out := app.SynthesisHistoricalRepublishReview{CurrentScope: a.Scope, WorkspaceID: a.Command.WorkspaceID, NoteID: a.Command.NoteID, AttemptID: a.ID, State: "READY", Target: a.Command.SynthesisHistoricalRepublishTarget, Candidate: a.Candidate, CurrentContent: string(a.Capture.Bytes), PublishedContent: a.PublishedContent, PreviewFingerprint: a.Fingerprint, Warnings: a.Warnings, Replayed: replayed}
	var applied historicalRepublishApplied
	found, err := readHistoricalEvent(tx, a.Command.WorkspaceID, a.Command.NoteID, "APPLY", "", a.ID, &applied)
	if err != nil || !found {
		return out, err
	}
	out.State = "APPLIED"
	out.Result = &app.SynthesisCandidateRemergeResult{RevisionID: applied.Revision.ID, ArticleRevisionID: applied.Revision.ArticleRevisionID}
	var b struct{ ID, ProposalID, ProposalRevisionID string }
	res := tx.Raw(`SELECT id::text,proposal_id::text,proposal_revision_id::text FROM authoring.document_publication_binding WHERE workspace_id=? AND document_id=? AND article_revision_id=?`, string(a.Command.WorkspaceID), string(a.Command.ExpectedDocumentID), string(applied.Revision.ArticleRevisionID)).Scan(&b)
	if res.Error != nil {
		return out, res.Error
	}
	if res.RowsAffected == 1 {
		out.Result.PublicationID = foundation.ID(b.ID)
		out.Result.ProposalID = foundation.ID(b.ProposalID)
		out.Result.ProposalRevisionID = foundation.ID(b.ProposalRevisionID)
	}
	return out, nil
}
func (h *synthesisHistoricalRepublish) Begin(ctx context.Context, c app.BeginSynthesisHistoricalRepublish) (app.SynthesisHistoricalRepublishReview, error) {
	var out app.SynthesisHistoricalRepublishReview
	if !validID(c.WorkspaceID) || !validID(c.NoteID) || !validID(c.SelectedRevisionID) || !validRemergeKey(c.IdempotencyKey) {
		return out, manuscriptStoreInvalid("invalid historical begin")
	}
	err := h.within(ctx, func(ctx context.Context, s foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, s, c.WorkspaceID)
		if err != nil {
			return err
		}
		if _, err = loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, true); err != nil {
			return err
		}
		var prior historicalRepublishAttempt
		found, err := readHistoricalEvent(tx, c.WorkspaceID, c.NoteID, "BEGIN", c.IdempotencyKey, "", &prior)
		if err != nil {
			return err
		}
		if found {
			if !reflect.DeepEqual(prior.Command, c) {
				return manuscriptStoreConflict("historical begin command differs")
			}
			if err = historicalRoot(prior, root); err != nil {
				return err
			}
			out, err = h.project(tx, prior, true)
			return err
		}
		t, selected, latest, owner, err := h.target(ctx, s, tx, c.WorkspaceID, c.NoteID, c.SelectedRevisionID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(t, c.SynthesisHistoricalRepublishTarget) {
			return manuscriptStoreConflict("historical target changed")
		}
		id, err := h.runtime.dependencies.Storage.IDs.New()
		if err != nil {
			return err
		}
		captureID, err := h.runtime.dependencies.Storage.IDs.New()
		if err != nil {
			return err
		}
		capture, err := h.capture(ctx, c.WorkspaceID, owner.Document.CanonicalPath, root, captureID, owner.Document.CurrentPublishedRevisionID != "")
		if err != nil {
			return err
		}
		scope, err := historicalScope(tx, c.WorkspaceID, c.NoteID)
		if err != nil {
			return err
		}
		candidate, err := selected.Content()
		if err != nil {
			return err
		}
		var published struct{ Content, ContentHash string }
		if c.ExpectedPublishedRevisionID != "" {
			q := tx.Raw(`SELECT content,content_hash FROM core.article_revision WHERE workspace_id=? AND document_id=? AND id=? AND status='PUBLISHED'`, string(c.WorkspaceID), string(c.ExpectedDocumentID), string(c.ExpectedPublishedRevisionID)).Scan(&published)
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return manuscriptStoreConflict("published baseline missing")
			}
		}
		a := historicalRepublishAttempt{ID: id, Command: c, Selected: selected, Latest: latest, Capture: capture, Scope: scope, PublishedContent: published.Content, PublishedContentHash: published.ContentHash, Candidate: candidate, CreatedAt: capture.CreatedAt, Warnings: []app.SynthesisHistoricalRepublishWarning{{Code: "HISTORICAL_CONTENT_NOT_REVALIDATED_AGAINST_CURRENT_SCOPE"}}}
		var unavailable []struct{ SourceID, SourceVersionID, Code string }
		if err = tx.Raw(`SELECT DISTINCT rs.source_id::text,rs.source_version_id::text,warning.code FROM organizing.synthesis_revision_source rs JOIN core.source s ON s.id=rs.source_id AND s.workspace_id=rs.workspace_id JOIN core.source_version sv ON sv.id=rs.source_version_id AND sv.workspace_id=rs.workspace_id CROSS JOIN LATERAL (SELECT 'SOURCE_REMOVED' AS code WHERE s.removed_at IS NOT NULL UNION ALL SELECT 'SOURCE_QUARANTINED' WHERE sv.security_status='quarantined' OR EXISTS(SELECT 1 FROM ingestion.attempt ia WHERE ia.workspace_id=rs.workspace_id AND ia.source_version_id=rs.source_version_id AND ia.security_status='quarantined')) warning WHERE rs.workspace_id=? AND rs.revision_id=? ORDER BY rs.source_id::text,rs.source_version_id::text,warning.code`, string(c.WorkspaceID), string(selected.ID)).Scan(&unavailable).Error; err != nil {
			return err
		}
		for _, v := range unavailable {
			a.Warnings = append(a.Warnings, app.SynthesisHistoricalRepublishWarning{Code: v.Code, SourceID: foundation.ID(v.SourceID), SourceVersionID: foundation.ID(v.SourceVersionID)})
		}
		raw, err := encodeManuscriptRecord(a)
		if err != nil {
			return err
		}
		a.Fingerprint = manuscriptBytesHash(raw)
		raw, err = encodeManuscriptRecord(capture)
		if err != nil {
			return err
		}
		if err = tx.Create(&manuscriptCaptureRow{ID: string(capture.ID), WorkspaceID: string(c.WorkspaceID), Payload: raw, PayloadHash: manuscriptBytesHash(raw)}).Error; err != nil {
			return err
		}
		if err = h.verifyFile(ctx, a); err != nil {
			return err
		}
		if err = h.insert(tx, id, c.WorkspaceID, c.NoteID, id, "BEGIN", c.IdempotencyKey, a); err != nil {
			return err
		}
		out, err = h.project(tx, a, false)
		return err
	})
	if err != nil {
		return app.SynthesisHistoricalRepublishReview{}, err
	}
	return out, nil
}
func (h *synthesisHistoricalRepublish) Read(ctx context.Context, c app.ReadSynthesisHistoricalRepublish) (app.SynthesisHistoricalRepublishReview, error) {
	var out app.SynthesisHistoricalRepublishReview
	if !validID(c.NoteID) || (c.AttemptID == "" && !validRemergeKey(c.IdempotencyKey)) || (c.AttemptID != "" && !validID(c.AttemptID)) {
		return out, manuscriptStoreInvalid("invalid historical read")
	}
	err := h.within(ctx, func(ctx context.Context, s foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, s, c.WorkspaceID)
		if err != nil {
			return err
		}
		var a historicalRepublishAttempt
		found, err := readHistoricalEvent(tx, c.WorkspaceID, c.NoteID, "BEGIN", c.IdempotencyKey, c.AttemptID, &a)
		if err != nil {
			return err
		}
		if !found {
			return synthesisNotFound()
		}
		if c.IdempotencyKey != "" && a.Command.IdempotencyKey != c.IdempotencyKey {
			return manuscriptStoreConflict("historical read identities differ")
		}
		if err = historicalRoot(a, root); err != nil {
			return err
		}
		out, err = h.project(tx, a, true)
		return err
	})
	if err != nil {
		return app.SynthesisHistoricalRepublishReview{}, err
	}
	return out, nil
}
