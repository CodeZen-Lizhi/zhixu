package postgres

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMSynthesisManuscriptReviewStore struct {
	store     *GORMSynthesisManuscriptStore
	authority app.SynthesisManuscriptHumanAuthority
}

func NewGORMSynthesisManuscriptReviewStore(store *GORMSynthesisManuscriptStore, authority app.SynthesisManuscriptHumanAuthority) (*GORMSynthesisManuscriptReviewStore, error) {
	if store == nil || isNilInterface(authority) {
		return nil, manuscriptStoreInvalid("human review owner dependencies are required")
	}
	return &GORMSynthesisManuscriptReviewStore{store: store, authority: authority}, nil
}

var _ app.SynthesisManuscriptReviewStore = (*GORMSynthesisManuscriptReviewStore)(nil)

type manuscriptDecisionRow struct {
	ID             string `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string `gorm:"column:workspace_id;type:uuid"`
	AttemptID      string `gorm:"column:attempt_id;type:uuid"`
	Sequence       int    `gorm:"column:sequence"`
	IdempotencyKey string `gorm:"column:idempotency_key"`
	Payload        []byte `gorm:"column:payload;type:bytea"`
	PayloadHash    string `gorm:"column:payload_hash"`
}

func (manuscriptDecisionRow) TableName() string {
	return "organizing.synthesis_manuscript_review_decision"
}

func validHumanBinding(b app.SynthesisManuscriptHumanBinding) bool {
	return validID(b.WorkspaceID) && validID(b.ProcessingID) && validID(b.HumanTaskID) && validID(b.RunID) && validID(b.NodeRunID) && b.TargetVersion > 0
}
func reviewAttemptBinding(b app.SynthesisManuscriptHumanBinding, a app.SynthesisManuscriptAttempt) bool {
	return b.WorkspaceID == a.Command.WorkspaceID && b.ProcessingID == a.Command.ProcessingID && b.RunID == a.Prepared.GenerationInput.WorkflowRunID
}

func (r *GORMSynthesisManuscriptReviewStore) Decide(ctx context.Context, c app.DecideSynthesisManuscript) (app.SynthesisManuscriptStageDecision, error) {
	var out app.SynthesisManuscriptStageDecision
	if !validHumanBinding(c.Binding) || !validID(c.NoteID) || !validID(c.AttemptID) || !validID(c.CaptureID) || c.IdempotencyKey == "" || len(c.IdempotencyKey) > 200 || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || strings.ContainsRune(c.IdempotencyKey, 0) {
		return out, manuscriptStoreInvalid("invalid review decision identity")
	}
	s := r.store
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := r.authority.AuthorizeSynthesisManuscriptHumanScoped(ctx, scope, c.Binding); err != nil {
			return err
		}
		// 任务锁先于尝试锁；不同笔记使用同一任务级幂等键时，也以任务锁串行化。
		if err := lockManuscriptHuman(ctx, tx, c.Binding, false); err != nil {
			return err
		}
		var locked manuscriptAttemptRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(c.Binding.WorkspaceID), string(c.AttemptID)).Take(&locked).Error; err != nil {
			return manuscriptReadError(err)
		}
		a, capture, err := s.readAttempt(ctx, tx, c.Binding.WorkspaceID, c.AttemptID)
		if err != nil {
			return err
		}
		if !reviewAttemptBinding(c.Binding, a) || a.Command.NoteID != c.NoteID || a.CaptureID != c.CaptureID {
			return manuscriptStoreConflict("review attempt binding changed")
		}
		var prior manuscriptDecisionRow
		err = tx.Where("workspace_id=? AND idempotency_key=?", string(c.Binding.WorkspaceID), c.IdempotencyKey).Take(&prior).Error
		if err == nil {
			if err = decodeManuscriptRecord(prior.Payload, prior.PayloadHash, &out); err != nil {
				return err
			}
			if !reflect.DeepEqual(out.Command, c) {
				return manuscriptStoreConflict("decision key bound to another input")
			}
			ledger, err := s.readDecisions(ctx, tx, a)
			if err != nil {
				return err
			}
			if out.Sequence < 1 || out.Sequence > len(ledger) || !reflect.DeepEqual(out, ledger[out.Sequence-1]) {
				return manuscriptStoreConflict("decision recovery differs from ledger")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err = lockManuscriptHuman(ctx, tx, c.Binding, true); err != nil {
			return err
		}
		authority, err := r.authority.VerifyPendingSynthesisManuscriptHumanScoped(ctx, scope, c.Binding, a.Prepared)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(authority, a.Prepared.Authority) {
			return manuscriptStoreConflict("human review owner changed")
		}
		if err = r.verifyFile(ctx, capture); err != nil {
			return err
		}
		ledger, err := s.readDecisions(ctx, tx, a)
		if err != nil {
			return err
		}
		decisions := make([]app.SynthesisManuscriptResolution, 0, len(ledger)+1)
		for _, d := range ledger {
			if d.Command.Binding != c.Binding {
				return manuscriptStoreConflict("attempt belongs to another human task")
			}
			decisions = append(decisions, d.Command.Resolution)
		}
		decisions = append(decisions, c.Resolution)
		resolved, err := app.ResolveSynthesisManuscript(ctx, a.Prepared.MergeInput, decisions, s.dependencies.Merge, s.dependencies.Mapper)
		if err != nil {
			return err
		}
		id, err := s.dependencies.IDs.New()
		if err != nil {
			return err
		}
		out = app.SynthesisManuscriptStageDecision{ID: id, Command: c, Sequence: len(ledger) + 1, Result: resolved, CreatedAt: s.now()}
		if len(ledger) > 0 {
			out.PreviousHash = ledger[len(ledger)-1].Hash
		}
		out.Hash, err = manuscriptValueHash(out)
		if err != nil {
			return err
		}
		payload, err := encodeManuscriptRecord(out)
		if err != nil {
			return err
		}
		row := manuscriptDecisionRow{ID: string(id), WorkspaceID: string(c.Binding.WorkspaceID), AttemptID: string(a.ID), Sequence: out.Sequence, IdempotencyKey: c.IdempotencyKey, Payload: payload, PayloadHash: manuscriptBytesHash(payload)}
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		if resolved.Preview.Review != nil {
			return nil
		}
		if resolved.Preview.Manuscript == nil {
			return manuscriptStoreConflict("resolved preview has no manuscript")
		}
		receiptID, err := s.dependencies.IDs.New()
		if err != nil {
			return err
		}
		receipt := app.SynthesisManuscriptReceipt{ID: receiptID, WorkspaceID: c.Binding.WorkspaceID, AttemptID: a.ID, AttemptHash: a.Hash, Manuscript: *resolved.Preview.Manuscript, CreatedAt: s.now(), Review: &app.SynthesisManuscriptReceiptReview{Version: app.SynthesisManuscriptReviewVersion, Decisions: append(ledger, out)}}
		receipt.Hash, err = manuscriptValueHash(receipt)
		if err != nil {
			return err
		}
		payload, err = encodeManuscriptRecord(receipt)
		if err != nil {
			return err
		}
		return tx.Create(&manuscriptReceiptRow{ID: string(receipt.ID), WorkspaceID: string(receipt.WorkspaceID), AttemptID: string(a.ID), Payload: payload, PayloadHash: manuscriptBytesHash(payload)}).Error
	})
	if err != nil {
		return app.SynthesisManuscriptStageDecision{}, err
	}
	return out, nil
}

func (r *GORMSynthesisManuscriptReviewStore) verifyFile(ctx context.Context, c app.SynthesisManuscriptCapture) error {
	if !c.Exists {
		return r.store.dependencies.Files.EnsureTargetAbsent(ctx, c.WorkspaceID, c.TargetPath, c.AbsenceToken)
	}
	content, hash, err := r.store.dependencies.Files.CurrentContent(ctx, c.WorkspaceID, c.TargetPath, domain.MaxSynthesisManuscriptBytes)
	if err != nil {
		return err
	}
	if hash != c.ContentHash || !bytes.Equal(content, c.Bytes) {
		return manuscriptStoreConflict("captured file changed")
	}
	return nil
}

// 读取真实 Workflow 行仅构成结构证明；已认证能力及根目录、所属模块校验仍由必需的 HumanAuthority 端口负责。
func lockManuscriptHuman(ctx context.Context, tx *gorm.DB, b app.SynthesisManuscriptHumanBinding, pending bool) error {
	var row struct {
		Status        string
		TargetVersion int64
		RunID         string
		NodeRunID     string
		WorkspaceID   string
		NodeStatus    string
		Unexpired     bool
	}
	err := tx.WithContext(ctx).Raw(`SELECT h.status,h.target_version,h.run_id,h.node_run_id,r.workspace_id,n.status AS node_status,(h.expires_at IS NULL OR h.expires_at>CURRENT_TIMESTAMP) AS unexpired FROM workflow.human_task h JOIN workflow.run r ON r.id=h.run_id JOIN workflow.node_run n ON n.id=h.node_run_id AND n.run_id=r.id WHERE h.id=? FOR UPDATE OF h`, string(b.HumanTaskID)).Scan(&row).Error
	if err != nil {
		return err
	}
	if row.WorkspaceID != string(b.WorkspaceID) || row.RunID != string(b.RunID) || row.NodeRunID != string(b.NodeRunID) || row.TargetVersion != b.TargetVersion || (row.Status != "pending" && row.Status != "submitted") || (pending && (row.Status != "pending" || row.NodeStatus != "waiting_for_human" || !row.Unexpired)) {
		return manuscriptStoreConflict("actual human task is unavailable or changed")
	}
	return nil
}

func (s *GORMSynthesisManuscriptStore) readDecisions(ctx context.Context, tx *gorm.DB, a app.SynthesisManuscriptAttempt) ([]app.SynthesisManuscriptStageDecision, error) {
	var rows []manuscriptDecisionRow
	if err := tx.WithContext(ctx).Where("workspace_id=? AND attempt_id=?", string(a.Command.WorkspaceID), string(a.ID)).Order("sequence ASC").Limit(3).Find(&rows).Error; err != nil {
		return nil, err
	}
	ledger := make([]app.SynthesisManuscriptStageDecision, 0, len(rows))
	for _, row := range rows {
		var d app.SynthesisManuscriptStageDecision
		if err := decodeManuscriptRecord(row.Payload, row.PayloadHash, &d); err != nil {
			return nil, err
		}
		if string(d.ID) != row.ID || d.Sequence != row.Sequence || d.Command.IdempotencyKey != row.IdempotencyKey {
			return nil, manuscriptStoreConflict("decision row identity changed")
		}
		ledger = append(ledger, d)
	}
	_, err := s.replayDecisionLedger(ctx, a, ledger)
	return ledger, err
}

func (s *GORMSynthesisManuscriptStore) replayDecisionLedger(ctx context.Context, a app.SynthesisManuscriptAttempt, ledger []app.SynthesisManuscriptStageDecision) (app.SynthesisManuscriptMergePreview, error) {
	preview := a.Preview
	if len(ledger) > 2 {
		return preview, manuscriptStoreConflict("too many review stages")
	}
	decisions := []app.SynthesisManuscriptResolution{}
	previous := ""
	for i, d := range ledger {
		unhashed := d
		unhashed.Hash = ""
		hash, err := manuscriptValueHash(unhashed)
		if err != nil || hash != d.Hash || !validID(d.ID) || d.Sequence != i+1 || d.PreviousHash != previous || !validHumanBinding(d.Command.Binding) || !reviewAttemptBinding(d.Command.Binding, a) || d.Command.AttemptID != a.ID || d.Command.CaptureID != a.CaptureID || d.Command.NoteID != a.Command.NoteID || d.CreatedAt.Before(a.CreatedAt) || (i > 0 && d.Command.Binding != ledger[0].Command.Binding) {
			return preview, manuscriptStoreConflict("stored decision binding is invalid")
		}
		decisions = append(decisions, d.Command.Resolution)
		resolved, err := app.ResolveSynthesisManuscript(ctx, a.Prepared.MergeInput, decisions, s.dependencies.Merge, s.dependencies.Mapper)
		if err != nil {
			return preview, err
		}
		if !reflect.DeepEqual(resolved, d.Result) {
			return preview, manuscriptStoreConflict("decision result differs from replay")
		}
		preview = resolved.Preview
		previous = d.Hash
	}
	return preview, nil
}

func (r *GORMSynthesisManuscriptReviewStore) ReadManifest(ctx context.Context, b app.SynthesisManuscriptHumanBinding) (app.SynthesisManuscriptReviewManifest, error) {
	out := app.SynthesisManuscriptReviewManifest{Binding: b, Targets: []app.SynthesisManuscriptReviewTarget{}}
	if !validHumanBinding(b) {
		return out, manuscriptStoreInvalid("invalid human review binding")
	}
	s := r.store
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := r.authority.AuthorizeSynthesisManuscriptHumanScoped(ctx, scope, b); err != nil {
			return err
		}
		if err := lockManuscriptHuman(ctx, tx, b, false); err != nil {
			return err
		}
		var rows []manuscriptAttemptRow
		if err := tx.Where("workspace_id=? AND convert_from(payload,'UTF8')::jsonb->'command'->>'processing_id'=? AND convert_from(payload,'UTF8')::jsonb->'prepared'->'generation_input'->>'WorkflowRunID'=?", string(b.WorkspaceID), string(b.ProcessingID), string(b.RunID)).Order("id ASC").Limit(app.MaxSynthesisManuscriptReviewTargets + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 || len(rows) > app.MaxSynthesisManuscriptReviewTargets {
			return manuscriptStoreConflict("review target set unavailable or exceeds bound")
		}
		expected := map[foundation.ID]bool{}
		seen := map[foundation.ID]bool{}
		var first app.SynthesisManuscriptPrepared
		completed := 0
		hasReview := false
		for i, row := range rows {
			a, _, err := s.readAttempt(ctx, tx, b.WorkspaceID, foundation.ID(row.ID))
			if err != nil {
				return err
			}
			if !reviewAttemptBinding(b, a) || seen[a.Command.NoteID] {
				return manuscriptStoreConflict("ambiguous review target set")
			}
			if i == 0 {
				// 预期集合由完整冻结的生成结果定义，而不是由下方查到的行定义；缺失的准备记录不能因此被忽略。
				first = a.Prepared
				notes, err := manuscriptChangedNotes(first.GenerationInput, first.Generation)
				if err != nil {
					return err
				}
				for _, note := range notes {
					expected[note] = true
				}
			} else if !reflect.DeepEqual(first.GenerationInput, a.Prepared.GenerationInput) || !reflect.DeepEqual(first.Generation, a.Prepared.Generation) {
				return manuscriptStoreConflict("review generations differ")
			}
			if !expected[a.Command.NoteID] {
				return manuscriptStoreConflict("unexpected review target")
			}
			seen[a.Command.NoteID] = true
			hasReview = hasReview || a.Preview.Review != nil
			ledger, err := s.readDecisions(ctx, tx, a)
			if err != nil {
				return err
			}
			preview := a.Preview
			if len(ledger) > 0 {
				if ledger[0].Command.Binding != b {
					return manuscriptStoreConflict("review belongs to another task")
				}
				preview = ledger[len(ledger)-1].Result.Preview
			}
			target := app.SynthesisManuscriptReviewTarget{Attempt: a, Preview: preview}
			var rr manuscriptReceiptRow
			err = tx.Where("workspace_id=? AND attempt_id=?", string(b.WorkspaceID), string(a.ID)).Take(&rr).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 保持目标可见，但不将其计为已完成。
			} else if err != nil {
				return err
			} else {
				receipt, err := s.decodeReceipt(rr, a)
				if err != nil {
					return err
				}
				target.Receipt = &receipt
				completed++
			}
			out.Targets = append(out.Targets, target)
		}
		out.Ready = hasReview && len(expected) > 0 && len(seen) == len(expected) && completed == len(expected)
		return nil
	})
	if err != nil {
		return app.SynthesisManuscriptReviewManifest{}, err
	}
	return out, nil
}
