package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

func (s *GORMSynthesisManuscriptSourceReviewStore) origin(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, row sourceReviewRow) (app.SynthesisGenerationInput, app.SynthesisGenerationResult, error) {
	var input app.SynthesisGenerationInput
	var generation app.SynthesisGenerationResult
	var eligible bool
	err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM organizing.synthesis_processing p JOIN organizing.synthesis_execution e ON e.workflow_run_id=p.workflow_run_id AND e.processing_id=p.id JOIN workflow.run r ON r.id=p.workflow_run_id WHERE p.workspace_id=? AND p.id=? AND p.workflow_run_id=? AND p.status='RECOVERY_REQUIRED' AND p.error_code='SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED' AND e.applied_result IS NULL AND NOT e.apply_recovery AND r.status='failed' AND NOT EXISTS(SELECT 1 FROM organizing.synthesis_apply_receipt a WHERE a.workspace_id=p.workspace_id AND a.processing_id=p.id) AND NOT EXISTS(SELECT 1 FROM agent.model_run m WHERE m.workflow_run_id=p.workflow_run_id AND m.status='RUNNING'))`, row.WorkspaceID, row.OriginProcessingID, row.OriginWorkflowRunID).Scan(&eligible).Error
	if err != nil {
		return input, generation, err
	}
	if !eligible {
		return input, generation, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ORIGIN_INVALID")
	}
	loaded, err := s.runtime.dependencies.Executions.LoadSynthesisExecution(ctx, foundation.ID(row.WorkspaceID), foundation.ID(row.OriginProcessingID), foundation.ID(row.OriginWorkflowRunID))
	if err != nil {
		return input, generation, err
	}
	if loaded.Input == nil || loaded.Generation == nil || loaded.Generation.Generation == nil || loaded.Semantic == nil || loaded.Semantic.Semantic == nil || !loaded.Semantic.Semantic.Accepted {
		return input, generation, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ORIGIN_INVALID")
	}
	frozen := loaded.Input
	input = app.SynthesisGenerationInput{BodyRefresh: frozen.BodyRefresh, Goal: frozen.Goal, ProcessingID: frozen.ProcessingID, SourceEvent: frozen.SourceEvent, WorkflowRunID: frozen.WorkflowRunID, NodeRunID: loaded.Generation.NodeRunID, NodeAttemptID: loaded.Generation.NodeAttemptID, RequestHash: frozen.RequestHash, Notes: []app.SynthesisGenerationNote{}, Sources: []app.SynthesisSourceExcerpt{}}
	for _, n := range frozen.Notes {
		revision, e := loadSynthesisRevision(tx, foundation.ID(row.WorkspaceID), n.Note.ID, n.RevisionID)
		if e != nil {
			return input, generation, e
		}
		if revision.Hash != n.RevisionHash {
			return input, generation, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ORIGIN_INVALID")
		}
		input.Notes = append(input.Notes, app.SynthesisGenerationNote{PublicationID: n.PublicationID, Note: n.Note, Revision: revision, Supplements: n.Supplements, Anchor: n.Anchor})
	}
	for _, ref := range frozen.Sources {
		view, e := s.runtime.dependencies.Service.Sources.OpenSynthesisSource(ctx, ref)
		if e != nil {
			return input, generation, e
		}
		if view.Reference != ref || view.Availability != domain.MaterialAvailable || view.Validate(ref.Source.WorkspaceID) != nil {
			return input, generation, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_SOURCE")
		}
		input.Sources = append(input.Sources, app.SynthesisSourceExcerpt{Reference: ref, Text: view.Text})
	}
	generation = *loaded.Generation.Generation
	if err = s.runtime.dependencies.Models.VerifySynthesisManuscriptHistoryScoped(ctx, scope, input, generation); err != nil {
		return input, generation, err
	}
	return input, generation, nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) current(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, row sourceReviewRow) (app.SynthesisSourceReviewInput, error) {
	var out app.SynthesisSourceReviewInput
	root, err := s.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, foundation.ID(row.WorkspaceID))
	if err != nil {
		return out, err
	}
	input, generation, err := s.origin(ctx, scope, tx, row)
	if err != nil {
		return out, err
	}
	obligations, ids, err := app.SourceReviewObligations(input, generation)
	if err != nil {
		return out, err
	}
	out.Snapshot = app.SynthesisSourceReviewSnapshot{Targets: []app.SynthesisSourceReviewTarget{}, Obligations: obligations}
	out.Sources = input.Sources
	refs := []domain.SynthesisSourceRef{}
	for _, v := range input.Sources {
		refs = append(refs, v.Reference)
	}
	if err = s.runtime.dependencies.Candidates.dependencies.Sources.VerifySynthesisSourcesScoped(ctx, scope, foundation.ID(row.WorkspaceID), refs); err != nil {
		return out, err
	}
	for i, id := range ids {
		var frozen app.SynthesisGenerationNote
		for _, n := range input.Notes {
			if n.Note.ID == id {
				frozen = n
			}
		}
		note, e := loadSynthesisNote(tx, foundation.ID(row.WorkspaceID), id, true)
		if e != nil {
			return out, e
		}
		if row.AttemptNo > 1 {
			var a anchorModel
			err := tx.Where("workspace_id=? AND note_id=?", row.WorkspaceID, string(id)).Take(&a).Error
			if err != nil && !gormNoRows(err) {
				return out, err
			}
			frozen.Anchor = nil
			if err == nil {
				anchor, err := readAnchor(tx, note.WorkspaceID, foundation.ID(a.ID), true)
				if err != nil {
					return out, err
				}
				allowed, err := readAcceptedAnchorRefs(tx, note.WorkspaceID, id, input.SourceEvent.Source)
				if err != nil {
					return out, err
				}
				frozen.Anchor = &app.SynthesisAnchorBinding{AnchorID: anchor.ID, ScopeVersion: anchor.ScopeVersion, Scope: anchor.Scope, AllowedSources: allowed}
			}
		}
		// 准入仍以精确的已批准范围为准；冻结范围为 nil 时，须在笔记锁内确认锚点不存在，也要防范并发创建锚点。
		if e = s.runtime.dependencies.Candidates.dependencies.Anchors.VerifySynthesisAnchorAdmissionScoped(ctx, scope, note.WorkspaceID, id, input.SourceEvent.Source, frozen.Anchor); e != nil {
			return out, e
		}
		if frozen.Anchor != nil {
			for _, obligation := range obligations {
				if obligation.NoteLabel != fmt.Sprintf("N%03d", i+1) {
					continue
				}
				permitted := false
				for _, ref := range frozen.Anchor.AllowedSources {
					if ref == obligation.Source {
						permitted = true
						break
					}
				}
				if !permitted {
					return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_SCOPE")
				}
			}
		}
		latest, e := loadSynthesisRevision(tx, note.WorkspaceID, id, note.CurrentRevisionID)
		if e != nil {
			return out, e
		}
		if latest.Manuscript == nil {
			return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_TEXT")
		}
		owner, e := s.runtime.dependencies.Candidates.dependencies.Authoring.ReadGeneratedDocumentScoped(ctx, scope, note.WorkspaceID, note.DocumentID, id)
		if e != nil {
			return out, e
		}
		if owner.Revision.ID != latest.ArticleRevisionID || owner.Revision.ContentHash != latest.ContentHash || owner.Revision.CreatedByType != "AGENT" || owner.Revision.RevisionNo != int(latest.ArticleRevisionNo) {
			return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_TEXT")
		}
		if e = verifyManuscriptPendingState(tx, note.WorkspaceID, note.DocumentID, latest.ArticleRevisionID, true); e != nil {
			return out, e
		}
		authority := app.SynthesisManuscriptAuthority{DocumentID: note.DocumentID, DocumentVersion: owner.Document.Version, NoteVersion: note.Version, LatestArticleID: latest.ArticleRevisionID, TargetPath: owner.Document.CanonicalPath, RootGrantID: root.GrantID, RootFingerprint: root.Fingerprint, WorkspaceBindingVersion: root.BindingVersion}
		if frozen.Anchor != nil {
			authority.Anchor = *frozen.Anchor
		}
		target := app.SynthesisSourceReviewTarget{Label: fmt.Sprintf("N%03d", i+1), NoteID: id, BaseRevisionID: latest.ID, ProjectionHash: latest.Hash, TargetKind: "REVISION", Authority: authority, Anchor: frozen.Anchor, FullContent: latest.Manuscript.FullContent}
		var publication struct{ RevisionID, PublicationID, ProposalCommitID, GitCommit string }
		q := tx.Raw(`SELECT r.id AS revision_id,b.id AS publication_id,pc.id AS proposal_commit_id,b.git_commit FROM core.document d JOIN organizing.synthesis_revision r ON r.workspace_id=d.workspace_id AND r.document_id=d.id AND r.article_revision_id=d.current_published_revision_id JOIN organizing.synthesis_proven_publication p ON p.workspace_id=r.workspace_id AND p.revision_id=r.id JOIN authoring.document_publication_binding b ON b.id=p.publication_id AND b.workspace_id=p.workspace_id JOIN change_control.proposal_commit pc ON pc.workspace_id=b.workspace_id AND pc.proposal_id=b.proposal_id AND pc.revision_id=b.proposal_revision_id AND pc.result_hash=b.content_hash AND pc.git_commit=b.git_commit WHERE d.workspace_id=? AND d.id=? AND b.status='PUBLISHED' AND b.target_path=d.canonical_path`, row.WorkspaceID, string(note.DocumentID)).Scan(&publication)
		if q.Error != nil {
			return out, q.Error
		}
		if owner.Document.CurrentPublishedRevisionID != "" {
			if q.RowsAffected != 1 {
				return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_TEXT")
			}
			target.PublishedRevisionID = foundation.ID(publication.RevisionID)
			target.Authority.PublishedPublicationID = foundation.ID(publication.PublicationID)
			target.Authority.PublishedProposalCommitID = foundation.ID(publication.ProposalCommitID)
			target.Authority.PublishedGitCommit = publication.GitCommit
			var ancestor bool
			if e = tx.Raw(`WITH RECURSIVE chain AS (SELECT id,parent_revision_id FROM organizing.synthesis_revision WHERE workspace_id=? AND note_id=? AND id=? UNION ALL SELECT r.id,r.parent_revision_id FROM organizing.synthesis_revision r JOIN chain c ON c.parent_revision_id=r.id WHERE r.workspace_id=? AND r.note_id=?) SELECT EXISTS(SELECT 1 FROM chain WHERE id=?)`, row.WorkspaceID, string(id), string(latest.ID), row.WorkspaceID, string(id), publication.RevisionID).Scan(&ancestor).Error; e != nil {
				return out, e
			}
			if !ancestor {
				return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_TEXT")
			}
			data, hash, e := s.runtime.dependencies.Storage.Files.CurrentContent(ctx, note.WorkspaceID, authority.TargetPath, domain.MaxSynthesisManuscriptBytes)
			if e != nil {
				return out, e
			}
			target.FileExists = true
			target.FileHash = hash
			if latest.ArticleRevisionID == owner.Document.CurrentPublishedRevisionID {
				target.FullContent = string(data)
				if hash != latest.ContentHash {
					target.TargetKind = "LOCAL_FILE"
				}
			} else {
				capture, e := s.sourceReviewCandidateCapture(tx, latest)
				if e != nil {
					return out, e
				}
				if !capture.Exists || capture.ContentHash != hash || !reflect.DeepEqual(capture.Bytes, data) {
					return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_MANUSCRIPT_BASELINE")
				}
			}
		} else {
			if q.RowsAffected != 0 {
				return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_TEXT")
			}
			token, e := authoringdomain.ComputeAbsenceToken(note.WorkspaceID, authority.TargetPath)
			if e != nil {
				return out, e
			}
			if e = s.runtime.dependencies.Storage.Files.EnsureTargetAbsent(ctx, note.WorkspaceID, authority.TargetPath, token); e != nil {
				return out, e
			}
			capture, e := s.sourceReviewCandidateCapture(tx, latest)
			if e != nil {
				return out, e
			}
			if capture.Exists {
				return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_MANUSCRIPT_BASELINE")
			}
		}
		target.FullContentHash = sha256Hex([]byte(target.FullContent))
		target.Paragraphs, e = s.mapper.SourceReviewParagraphs(target.Label, target.FullContent)
		if e != nil {
			return out, e
		}
		if len(target.Paragraphs) == 0 {
			return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_NO_PARAGRAPHS")
		}
		out.Snapshot.Targets = append(out.Snapshot.Targets, target)
	}
	return out, nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) sourceReviewCandidateCapture(tx *gorm.DB, r domain.SynthesisRevision) (app.SynthesisManuscriptCapture, error) {
	var capture app.SynthesisManuscriptCapture
	if r.Remerge != nil {
		var previous candidateRemergeAttempt
		found, err := readRemergeEvent(tx, r.WorkspaceID, r.NoteID, "BEGIN", "", r.Remerge.AttemptID, &previous)
		if err != nil {
			return capture, err
		}
		if !found {
			return capture, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_MANUSCRIPT_BASELINE")
		}
		return previous.Capture, validateManuscriptCapture(previous.Capture)
	}
	var raw struct{ Payload []byte }
	q := tx.Raw(`SELECT c.payload FROM organizing.synthesis_revision r JOIN organizing.synthesis_manuscript_receipt mr ON mr.id=r.manuscript_receipt_id AND mr.workspace_id=r.workspace_id JOIN organizing.synthesis_manuscript_attempt a ON a.id=mr.attempt_id AND a.workspace_id=r.workspace_id JOIN organizing.synthesis_manuscript_capture c ON c.id=a.capture_id AND c.workspace_id=r.workspace_id WHERE r.id=? AND r.workspace_id=?`, string(r.ID), string(r.WorkspaceID)).Scan(&raw)
	if q.Error != nil {
		return capture, q.Error
	}
	if q.RowsAffected != 1 || json.Unmarshal(raw.Payload, &capture) != nil {
		return capture, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_STALE_MANUSCRIPT_BASELINE")
	}
	return capture, validateManuscriptCapture(capture)
}
