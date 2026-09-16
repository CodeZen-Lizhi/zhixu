package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

type sourceReviewEvidenceRow struct {
	ID, WorkspaceID, ReviewID, NoteID, BaseRevisionID, TargetHash, FullContentHash, Obligation, Paragraph          string
	StartByte, EndByte                                                                                             int
	ParagraphHash                                                                                                  string
	SourceID, SourceVersionID, ContentArtifactID, ParseProjectionID, SourceSpanID, ContentHash, ExcerptHash, Title string
	CreatedAt                                                                                                      time.Time `gorm:"autoCreateTime:false"`
}

func (sourceReviewEvidenceRow) TableName() string {
	return "organizing.synthesis_manuscript_source_evidence"
}
func (s *GORMSynthesisManuscriptSourceReviewStore) ApplySourceReview(ctx context.Context, e workflowapp.ExecutionContext, id foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := lockSourceReviewExecutionRow(tx, e, id)
		if err != nil {
			return err
		}
		if e.NodeKind != app.SynthesisSourceReviewApply && e.NodeKind != app.SynthesisSourceReviewRecover {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
		}
		var recoveredReceipt string
		if err = tx.Raw("SELECT receipt_hash FROM organizing.synthesis_manuscript_source_review_recovery_receipt WHERE workspace_id=? AND review_id=?", row.WorkspaceID, row.ID).Scan(&recoveredReceipt).Error; err != nil {
			return err
		}
		if row.Status == "SUCCEEDED" || recoveredReceipt != "" {
			_, err = s.verifyModel(ctx, scope, row, row.Output, true)
			return err
		}
		if err = s.live(ctx, scope, tx, e, row); err != nil {
			return err
		}
		if row.Status == "REJECTED" {
			return nil
		}
		if row.Status != "REVIEWED" && !(row.Status == "STALE" && e.NodeKind == app.SynthesisSourceReviewRecover) {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_NOT_REVIEWED")
		}
		if _, err = s.verifyModel(ctx, scope, row, row.Output, true); err != nil {
			return err
		}
		input, err := s.recheck(ctx, scope, tx, row)
		if err != nil {
			return err
		}
		checks, accepted, err := app.BindSourceReviewOutput(row.Output, input.Snapshot)
		if err != nil {
			return err
		}
		if !accepted {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNSUPPORTED")
		}
		type proof struct{ ID, TargetHash, Paragraph, SourceKey string }
		receipt := []proof{}
		createdEvidence := make(map[string]bool)
		for i, check := range checks.Checks {
			o := input.Snapshot.Obligations[i]
			for _, label := range check.Targets {
				var t app.SynthesisSourceReviewTarget
				var p app.SynthesisSourceReviewParagraph
				for _, target := range input.Snapshot.Targets {
					for _, part := range target.Paragraphs {
						if part.Label == label {
							t = target
							p = part
						}
					}
				}
				if p.Label == "" || t.Label != o.NoteLabel {
					return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_TARGET_INVALID")
				}
				raw, err := json.Marshal(t)
				if err != nil {
					return err
				}
				targetHash := sha256Hex(raw)
				id, err := s.runtime.dependencies.Storage.IDs.New()
				if err != nil {
					return err
				}
				ref := o.Source
				v := ref.Source
				evidence := sourceReviewEvidenceRow{ID: string(id), WorkspaceID: row.WorkspaceID, ReviewID: row.ID, NoteID: string(t.NoteID), BaseRevisionID: string(t.BaseRevisionID), TargetHash: targetHash, FullContentHash: t.FullContentHash, Obligation: o.Label, Paragraph: label, StartByte: p.StartByte, EndByte: p.EndByte, ParagraphHash: p.Hash, SourceID: string(v.SourceID), SourceVersionID: string(v.SourceVersionID), ContentArtifactID: string(v.ContentArtifactID), ParseProjectionID: string(v.ParseProjectionID), SourceSpanID: string(ref.SourceSpanID), ContentHash: v.ContentHash, ExcerptHash: ref.ExcerptHash, Title: ref.Title, CreatedAt: canonicalTime(s.runtime.dependencies.Storage.Clock.Now())}
				var existing sourceReviewEvidenceRow
				lookup := tx.Where("workspace_id=? AND note_id=? AND target_hash=? AND paragraph=? AND source_version_id=? AND parse_projection_id=? AND source_span_id=?", evidence.WorkspaceID, evidence.NoteID, evidence.TargetHash, evidence.Paragraph, evidence.SourceVersionID, evidence.ParseProjectionID, evidence.SourceSpanID).Take(&existing)
				if lookup.Error == nil {
					if existing.BaseRevisionID != evidence.BaseRevisionID || existing.FullContentHash != evidence.FullContentHash || existing.StartByte != evidence.StartByte || existing.EndByte != evidence.EndByte || existing.ParagraphHash != evidence.ParagraphHash || existing.SourceID != evidence.SourceID || existing.ContentArtifactID != evidence.ContentArtifactID || existing.ContentHash != evidence.ContentHash || existing.ExcerptHash != evidence.ExcerptHash || existing.Title != evidence.Title {
						return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_PROOF_CONFLICT")
					}
					proofRow, err := readSourceReview(tx, e.WorkspaceID, foundation.ID(existing.ReviewID), false)
					if err != nil {
						return err
					}
					var recoveryProof int64
					if proofRow.Status != "SUCCEEDED" && !createdEvidence[existing.ID] {
						if err = tx.Table("organizing.synthesis_manuscript_source_review_recovery_receipt").Where("review_id=? AND workspace_id=?", proofRow.ID, proofRow.WorkspaceID).Count(&recoveryProof).Error; err != nil {
							return err
						}
						if recoveryProof != 1 {
							return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_MODEL_PROOF_INVALID")
						}
					}
					if _, err = s.verifyModel(ctx, scope, proofRow, proofRow.Output, true); err != nil {
						return err
					}
					evidence = existing
				} else if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
					if err = tx.Create(&evidence).Error; err != nil {
						return err
					}
					createdEvidence[evidence.ID] = true
				} else {
					return lookup.Error
				}
				if err = tx.Exec("INSERT INTO organizing.synthesis_manuscript_source_review_result(workspace_id,review_id,obligation,paragraph,evidence_id,proof_review_id) VALUES(?,?,?,?,?,?)", row.WorkspaceID, row.ID, o.Label, label, evidence.ID, evidence.ReviewID).Error; err != nil {
					return err
				}
				key, _ := ref.IdentityKey()
				receipt = append(receipt, proof{evidence.ID, targetHash, label, key})
			}
		}
		if len(receipt) == 0 {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OBLIGATIONS_INCOMPLETE")
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		now := canonicalTime(s.runtime.dependencies.Storage.Clock.Now())
		if e.NodeKind == app.SynthesisSourceReviewRecover {
			if err = tx.Exec("INSERT INTO organizing.synthesis_manuscript_source_review_recovery_receipt(review_id,workspace_id,workflow_run_id,receipt_hash,created_at) VALUES(?,?,?,?,?)", row.ID, row.WorkspaceID, string(e.RunID), sha256Hex(raw), now).Error; err != nil {
				return err
			}
			if row.Status == "STALE" {
				return nil
			}
		}
		return s.update(tx, row, map[string]any{"status": "SUCCEEDED", "receipt_hash": sha256Hex(raw), "completed_at": now})
	})
	if err != nil {
		return app.SynthesisManuscriptSourceReview{}, err
	}
	return s.GetSourceReview(ctx, e.WorkspaceID, id)
}
func (s *GORMSynthesisManuscriptSourceReviewStore) ListSourceReviews(ctx context.Context, w, processing foundation.ID) ([]app.SynthesisManuscriptSourceReview, error) {
	if !validID(w) || !validID(processing) {
		return nil, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ID_INVALID")
	}
	var rows []sourceReviewRow
	err := s.runtime.dependencies.Candidates.database.WithContext(ctx).Where("workspace_id=? AND origin_processing_id=?", string(w), string(processing)).Order("created_at,id").Limit(10).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := []app.SynthesisManuscriptSourceReview{}
	for _, r := range rows {
		v, e := r.project()
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) SourceReviewView(ctx context.Context, w, id foundation.ID) (app.SynthesisSourceReviewView, error) {
	var out app.SynthesisSourceReviewView
	err := s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if _, err := s.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, w); err != nil {
			return err
		}
		row, err := readSourceReview(tx, w, id, false)
		if err != nil {
			return err
		}
		v, err := row.project()
		if err != nil {
			return err
		}
		out = app.SynthesisSourceReviewView{ID: v.ID, WorkspaceID: w, OriginProcessingID: v.OriginProcessingID, OriginWorkflowRunID: v.OriginWorkflowRunID, WorkflowRunID: v.WorkflowRunID, AttemptNo: v.AttemptNo, SupersedesID: v.SupersedesID, Version: v.Version, Status: v.Status, EffectiveStatus: "NOT_COMPLETED", Retryable: v.Retryable, Failure: v.ErrorCode, CreatedAt: v.CreatedAt, CompletedAt: v.CompletedAt, Targets: []app.SynthesisSourceReviewTargetView{}, ReceiptHash: v.ReceiptHash}
		if err = s.sourceReviewActions(ctx, scope, tx, row, &out); err != nil {
			return err
		}
		if v.Snapshot == nil {
			return nil
		}
		out.ObligationCount = len(v.Snapshot.Obligations)
		checks, _, err := app.BindSourceReviewOutput(v.Output, *v.Snapshot)
		if len(v.Output) > 0 && err != nil {
			return err
		}
		for _, check := range checks.Checks {
			if check.Verdict == "SUPPORTED" {
				out.SupportedCount++
			}
		}
		if row.Status == "SUCCEEDED" || out.ReceiptHash != "" {
			if _, err = s.verifyModel(ctx, scope, row, row.Output, true); err != nil {
				return err
			}
			if _, err = s.recheck(ctx, scope, tx, row); err != nil {
				out.EffectiveStatus = "STALE"
				var classified *foundation.Error
				if errors.As(err, &classified) {
					out.EffectiveStatus = classified.Code
				}
			} else {
				out.EffectiveStatus = "CURRENT"
				out.Completed = true
			}
		}
		var evidence []sourceReviewEvidenceRow
		if err = tx.Table("organizing.synthesis_manuscript_source_evidence e").Select("e.id,e.workspace_id,e.review_id,e.note_id,e.base_revision_id,e.target_hash,e.full_content_hash,m.obligation,m.paragraph,e.start_byte,e.end_byte,e.paragraph_hash,e.source_id,e.source_version_id,e.content_artifact_id,e.parse_projection_id,e.source_span_id,e.content_hash,e.excerpt_hash,e.title,e.created_at").Joins("JOIN organizing.synthesis_manuscript_source_review_result m ON m.evidence_id=e.id AND m.workspace_id=e.workspace_id").Where("m.workspace_id=? AND m.review_id=?", row.WorkspaceID, row.ID).Order("m.obligation,m.paragraph").Scan(&evidence).Error; err != nil {
			return err
		}
		for _, target := range v.Snapshot.Targets {
			t := app.SynthesisSourceReviewTargetView{NoteID: target.NoteID, BaseRevisionID: target.BaseRevisionID, TargetKind: target.TargetKind, FullContentHash: target.FullContentHash, FullContent: target.FullContent, Paragraphs: target.Paragraphs, Evidence: []app.SynthesisSourceReviewEvidenceView{}}
			positions := make(map[string]int)
			for _, r := range evidence {
				if r.NoteID != string(t.NoteID) {
					continue
				}
				if position, found := positions[r.ID]; found {
					t.Evidence[position].Obligations = append(t.Evidence[position].Obligations, r.Obligation)
					continue
				}
				ref := domain.SynthesisSourceRef{Source: domain.SynthesisSourceVersion{WorkspaceID: foundation.ID(r.WorkspaceID), SourceID: foundation.ID(r.SourceID), SourceVersionID: foundation.ID(r.SourceVersionID), ContentArtifactID: foundation.ID(r.ContentArtifactID), ParseProjectionID: foundation.ID(r.ParseProjectionID), ContentHash: r.ContentHash}, SourceSpanID: foundation.ID(r.SourceSpanID), ExcerptHash: r.ExcerptHash, Title: r.Title}
				positions[r.ID] = len(t.Evidence)
				t.Evidence = append(t.Evidence, app.SynthesisSourceReviewEvidenceView{ID: foundation.ID(r.ID), Obligations: []string{r.Obligation}, Paragraph: r.Paragraph, Source: ref})
			}
			out.Targets = append(out.Targets, t)
		}
		return nil
	})
	return out, err
}
