package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SynthesisGeneratedAuthoring interface {
	authoringapp.GeneratedRepository
	authoringapp.GeneratedDocumentReader
	ReconcilePublications(context.Context, authoringapp.ReconcileQuery) (int, error)
}

type SynthesisStoreDependencies struct {
	Authoring SynthesisGeneratedAuthoring
	Retirer   authoringapp.GeneratedProposalRetirer
	Sources   organizingapp.SynthesisSourceFence
	Validated organizingapp.SynthesisValidatedGenerationFence
}

type GORMSynthesisStore struct {
	database     *gorm.DB
	uow          foundation.UnitOfWork
	dependencies SynthesisStoreDependencies
}

var _ organizingapp.SynthesisStore = (*GORMSynthesisStore)(nil)

func NewGORMSynthesisStore(pool *platformpostgres.Pool, dependencies SynthesisStoreDependencies) (*GORMSynthesisStore, error) {
	if pool == nil {
		return nil, synthesisUnavailable(errors.New("synthesis database is unavailable"))
	}
	for _, value := range []any{dependencies.Authoring, dependencies.Retirer, dependencies.Sources, dependencies.Validated} {
		if isNilInterface(value) {
			return nil, synthesisUnavailable(errors.New("synthesis transaction dependencies are required"))
		}
	}
	db, err := pool.GORM()
	if err != nil {
		return nil, synthesisUnavailable(err)
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, synthesisUnavailable(err)
	}
	return &GORMSynthesisStore{database: db, uow: uow, dependencies: dependencies}, nil
}

func (store *GORMSynthesisStore) ready(ctx context.Context, ids ...foundation.ID) error {
	if store == nil || store.database == nil || isNilInterface(store.uow) {
		return synthesisUnavailable(errors.New("synthesis store is unavailable"))
	}
	if ctx == nil {
		return invalid(errors.New("synthesis context is required"))
	}
	if err := ctx.Err(); err != nil {
		return synthesisDBError(ctx, err)
	}
	for _, id := range ids {
		if !validID(id) {
			return invalid(errors.New("synthesis identity is invalid"))
		}
	}
	return nil
}

func (store *GORMSynthesisStore) within(ctx context.Context, options foundation.TransactionOptions, work func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	if err := store.ready(ctx); err != nil {
		return err
	}
	return synthesisDBError(ctx, store.uow.Within(ctx, options, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(ctx, scope, tx.WithContext(ctx))
	}))
}

func (store *GORMSynthesisStore) ApplySynthesisGeneration(ctx context.Context, record organizingapp.SynthesisApplyRecord) (organizingapp.SynthesisApplyResult, error) {
	input, generation := record.Input, record.Generation
	workspaceID := input.SourceEvent.Source.WorkspaceID
	if err := store.ready(ctx, workspaceID); err != nil {
		return organizingapp.SynthesisApplyResult{}, err
	}
	if err := generation.Validate(input); err != nil {
		return organizingapp.SynthesisApplyResult{}, err
	}
	if len(record.Candidates) != len(generation.Notes) || record.AppliedAt.IsZero() {
		return organizingapp.SynthesisApplyResult{}, invalid(errors.New("synthesis candidate allocations are incomplete"))
	}
	seenIDs := make(map[foundation.ID]bool)
	for _, ids := range record.Candidates {
		for _, id := range []foundation.ID{ids.NoteID, ids.DocumentID, ids.RevisionID, ids.ArticleRevisionID, ids.ReservationID} {
			if !validID(id) || seenIDs[id] {
				return organizingapp.SynthesisApplyResult{}, invalid(errors.New("synthesis candidate allocation is invalid"))
			}
			seenIDs[id] = true
		}
	}
	processingKey, err := input.SourceEvent.ProcessingKey()
	if err != nil {
		return organizingapp.SynthesisApplyResult{}, err
	}
	encoded, err := json.Marshal(struct {
		Input      organizingapp.SynthesisGenerationInput
		Generation organizingapp.SynthesisGenerationResult
	}{input, generation})
	if err != nil {
		return organizingapp.SynthesisApplyResult{}, err
	}
	digest := sha256.Sum256(encoded)
	bindingHash := hex.EncodeToString(digest[:])
	// Immutable receipt recovery precedes all current-owner reconciliation. A
	// source, document or proposal changing later cannot invalidate exact replay.
	var committed synthesisApplyModel
	lookup := store.database.WithContext(ctx).Select(synthesisApplyColumns).Where("workspace_id=? AND processing_id=?", string(workspaceID), string(input.ProcessingID)).Take(&committed).Error
	if lookup == nil {
		if !synthesisReceiptMatches(committed, input, generation, processingKey, bindingHash) {
			return organizingapp.SynthesisApplyResult{}, synthesisReplayConflict()
		}
		return committed.result()
	}
	if !gormNoRows(lookup) {
		return organizingapp.SynthesisApplyResult{}, synthesisDBError(ctx, lookup)
	}
	// Reconcile only emitted existing notes through the Authoring owner. This
	// advances already-proven commits before freezing the next Document baseline.
	for _, generated := range generation.Notes {
		if generated.NoteID != "" {
			for _, candidate := range input.Notes {
				if candidate.Note.ID == generated.NoteID {
					if _, err := store.dependencies.Authoring.ReconcilePublications(ctx, authoringapp.ReconcileQuery{WorkspaceID: workspaceID, DocumentID: candidate.Note.DocumentID, Limit: 10, Now: record.AppliedAt}); err != nil {
						return organizingapp.SynthesisApplyResult{}, err
					}
					break
				}
			}
		}
	}
	result := organizingapp.SynthesisApplyResult{ProcessingID: input.ProcessingID, RevisionIDs: []foundation.ID{}, Publications: []organizingapp.SynthesisPublicationCommand{}}
	err = store.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		// A short Workspace lock serializes topic/alias allocation and candidate
		// commits only. Model calls and publication file preflight stay outside it.
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "synthesis:"+string(workspaceID)).Error; err != nil {
			return err
		}
		var receipt synthesisApplyModel
		err := tx.Select(synthesisApplyColumns).Where("workspace_id=? AND processing_id=?", string(workspaceID), string(input.ProcessingID)).Take(&receipt).Error
		if err == nil {
			if !synthesisReceiptMatches(receipt, input, generation, processingKey, bindingHash) {
				return synthesisReplayConflict()
			}
			result, err = receipt.result()
			return err
		}
		if !gormNoRows(err) {
			return err
		}
		if err := store.dependencies.Validated.VerifyValidatedSynthesisGenerationScoped(ctx, scope, input, generation); err != nil {
			return err
		}
		refs := make([]domain.SynthesisSourceRef, len(input.Sources))
		for i, source := range input.Sources {
			refs[i] = source.Reference
		}
		if err := store.dependencies.Sources.VerifySynthesisSourcesScoped(ctx, scope, workspaceID, refs); err != nil {
			return err
		}
		current := make(map[foundation.ID]organizingapp.SynthesisGenerationNote, len(input.Notes))
		for _, candidate := range input.Notes {
			current[candidate.Note.ID] = candidate
		}
		for index, generated := range generation.Notes {
			var base *organizingapp.SynthesisGenerationNote
			if generated.NoteID != "" {
				frozen := current[generated.NoteID]
				stored, err := loadSynthesisNote(tx, workspaceID, generated.NoteID, true)
				if err != nil {
					return err
				}
				if stored.Version != frozen.Note.Version || stored.CurrentRevisionID != frozen.Revision.ID || stored.DocumentID != frozen.Note.DocumentID || stored.TopicKey != frozen.Note.TopicKey || stored.Title != frozen.Note.Title || !slices.Equal(stored.Aliases, frozen.Note.Aliases) {
					return synthesisConflict("synthesis candidate changed while generation was running")
				}
				revision, err := loadSynthesisRevision(tx, workspaceID, stored.ID, stored.CurrentRevisionID)
				if err != nil {
					return err
				}
				if revision.Hash != frozen.Revision.Hash {
					return synthesisConsistency("frozen synthesis projection changed")
				}
				base = &organizingapp.SynthesisGenerationNote{Note: stored, Revision: revision}
			}
			var items []domain.SynthesisItem
			if base != nil {
				items = base.Revision.Items
			}
			delta, err := domain.ApplySynthesisDelta(workspaceID, items, generated.Delta, refs)
			if err != nil {
				return err
			}
			if !delta.Changed {
				continue
			}
			command, err := store.appendCandidate(ctx, scope, tx, record, index, base, delta.Items)
			if err != nil {
				return err
			}
			result.RevisionIDs = append(result.RevisionIDs, command.RevisionID)
			result.Publications = append(result.Publications, command)
		}
		result.Changed = len(result.RevisionIDs) > 0
		if err := organizingapp.ValidateSynthesisApplyResult(result); err != nil {
			return err
		}
		encodedResult, err := json.Marshal(result)
		if err != nil {
			return err
		}
		receipt = synthesisApplyModel{WorkspaceID: string(workspaceID), ProcessingID: string(input.ProcessingID), ProcessingKey: processingKey, SourceEventID: string(input.SourceEvent.ID), WorkflowRunID: string(input.WorkflowRunID), ModelRunID: string(generation.ModelRunID), RequestHash: input.RequestHash, OutputHash: generation.OutputHash, BindingHash: bindingHash, Changed: result.Changed, Result: organizingJSONB(encodedResult), CreatedAt: canonicalTime(record.AppliedAt)}
		return tx.Create(&receipt).Error
	})
	if err != nil {
		return organizingapp.SynthesisApplyResult{}, err
	}
	return result, nil
}

func synthesisReceiptMatches(receipt synthesisApplyModel, input organizingapp.SynthesisGenerationInput, generation organizingapp.SynthesisGenerationResult, processingKey, bindingHash string) bool {
	return receipt.BindingHash == bindingHash && receipt.ProcessingKey == processingKey && receipt.RequestHash == input.RequestHash && receipt.OutputHash == generation.OutputHash && receipt.ModelRunID == string(generation.ModelRunID) && receipt.WorkflowRunID == string(input.WorkflowRunID) && receipt.SourceEventID == string(input.SourceEvent.ID)
}

func synthesisReplayConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, organizingapp.ErrorCodeSynthesisIdempotencyConflict, false, errors.New("synthesis processing is bound to another generation"))
}

func (store *GORMSynthesisStore) appendCandidate(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, record organizingapp.SynthesisApplyRecord, index int, base *organizingapp.SynthesisGenerationNote, items []domain.SynthesisItem) (organizingapp.SynthesisPublicationCommand, error) {
	input, generated, ids := record.Input, record.Generation.Notes[index], record.Candidates[index]
	workspaceID := input.SourceEvent.Source.WorkspaceID
	at := canonicalTime(record.AppliedAt)
	note := domain.SynthesisNote{ID: ids.NoteID, WorkspaceID: workspaceID, DocumentID: ids.DocumentID, TopicKey: generated.TopicKey, Title: generated.Title, Aliases: append([]string{}, generated.Aliases...), CurrentRevisionID: ids.RevisionID, Version: 1, Status: domain.SynthesisPendingApproval, WorkflowRunID: input.WorkflowRunID, CreatedAt: at, UpdatedAt: at}
	var ownerState authoringapp.GeneratedDocumentState
	if base != nil {
		note = base.Note
		var err error
		ownerState, err = store.dependencies.Authoring.ReadGeneratedDocumentScoped(ctx, scope, workspaceID, note.DocumentID, note.ID)
		if err != nil {
			return organizingapp.SynthesisPublicationCommand{}, err
		}
		if ownerState.Revision.ID != base.Revision.ArticleRevisionID || ownerState.Revision.ContentHash != base.Revision.ContentHash || int64(ownerState.Revision.RevisionNo) != base.Revision.ArticleRevisionNo {
			return organizingapp.SynthesisPublicationCommand{}, synthesisConflict("authoring baseline changed outside synthesis")
		}
		for _, previous := range []time.Time{note.UpdatedAt, ownerState.Document.UpdatedAt} {
			if previous.After(at) {
				at = previous
			}
		}
		if ownerState.Publication != nil && ownerState.Publication.UpdatedAt.After(at) {
			at = ownerState.Publication.UpdatedAt
		}
		if ownerState.Publication != nil {
			switch ownerState.Publication.Status {
			case authoringdomain.PublicationPending:
				request := authoringdomain.GeneratedPublicationRetirementRequest{WorkspaceID: workspaceID, DocumentID: note.DocumentID, ArticleRevisionID: base.Revision.ArticleRevisionID, PublicationID: ownerState.Publication.ID, OriginKind: authoringdomain.GeneratedOriginSynthesisNote, OriginID: note.ID, OriginRevisionID: base.Revision.ID, ProjectionHash: base.Revision.Hash, ExpectedDocumentVersion: ownerState.Document.Version, ExpectedProposalVersion: ownerState.ProposalVersion}
				hash, err := authoringdomain.ComputeGeneratedPublicationRetirementRequestHash(request)
				if err != nil {
					return organizingapp.SynthesisPublicationCommand{}, err
				}
				if _, err := store.dependencies.Authoring.RetireGeneratedPublicationScoped(ctx, scope, authoringapp.RetireGeneratedPublicationRecord{Request: request, RequestHash: hash, IdempotencyKey: "synthesis-retire:" + string(input.ProcessingID) + ":" + string(note.ID), RetiredAt: at}, store.dependencies.Retirer); err != nil {
					return organizingapp.SynthesisPublicationCommand{}, err
				}
			case authoringdomain.PublicationRecoveryRequired:
				return organizingapp.SynthesisPublicationCommand{}, foundation.NewError(foundation.ErrorManualRecoveryRequired, "SYNTHESIS_PUBLICATION_RECOVERY_REQUIRED", false, errors.New("synthesis publication requires recovery"))
			}
		}
		note.CurrentRevisionID = ids.RevisionID
		note.Version++
		note.Status = domain.SynthesisPendingApproval
		note.Failure = nil
		note.WorkflowRunID = input.WorkflowRunID
		note.UpdatedAt = at
	} else {
		row := synthesisNoteModel{ID: string(note.ID), WorkspaceID: string(workspaceID), DocumentID: string(note.DocumentID), TopicKey: note.TopicKey, Title: note.Title, Aliases: pq.StringArray(note.Aliases), CurrentRevisionID: string(note.CurrentRevisionID), Version: note.Version, Status: string(note.Status), WorkflowRunID: synthesisIDPointer(note.WorkflowRunID), CreatedAt: note.CreatedAt, UpdatedAt: note.UpdatedAt}
		if err := tx.Create(&row).Error; err != nil {
			return organizingapp.SynthesisPublicationCommand{}, err
		}
		keys := make([]synthesisTopicModel, 0, len(note.Aliases)+1)
		for _, key := range append([]string{note.TopicKey}, note.Aliases...) {
			keys = append(keys, synthesisTopicModel{WorkspaceID: string(workspaceID), TopicKey: key, NoteID: string(note.ID)})
		}
		if err := tx.Create(&keys).Error; err != nil {
			return organizingapp.SynthesisPublicationCommand{}, err
		}
	}
	revision := domain.SynthesisRevision{ID: ids.RevisionID, WorkspaceID: workspaceID, NoteID: note.ID, DocumentID: note.DocumentID, ArticleRevisionID: ids.ArticleRevisionID, RevisionNo: 1, ArticleRevisionNo: 1, Title: note.Title, RendererVersion: domain.SynthesisRendererVersion, Items: items, Delta: generated.Delta, SourceEventID: input.SourceEvent.ID, WorkflowRunID: input.WorkflowRunID, ModelRunID: record.Generation.ModelRunID, CreatedAt: at}
	if base != nil {
		revision.ParentRevisionID = base.Revision.ID
		revision.RevisionNo = base.Revision.RevisionNo + 1
		revision.ArticleRevisionNo = base.Revision.ArticleRevisionNo + 1
	}
	content, err := domain.RenderSynthesisMarkdown(workspaceID, note.ID, note.Title, items)
	if err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	revision.ContentHash = authoringdomain.ComputeContentHash(content)
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	target, err := authoringdomain.DefaultGeneratedTargetPath(note.ID)
	if err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	request := authoringdomain.GeneratedRevisionRequest{WorkspaceID: workspaceID, DocumentID: note.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, OriginKind: authoringdomain.GeneratedOriginSynthesisNote, OriginID: note.ID, OriginRevisionID: revision.ID, ProjectionHash: revision.Hash, ExpectedDocumentVersion: ownerState.Document.Version, RevisionNo: int(revision.ArticleRevisionNo), Title: note.Title, TargetPath: target, Content: content}
	if base != nil {
		request.ParentRevisionID = ownerState.Revision.ID
		request.TargetPath = ownerState.Document.CanonicalPath
	}
	hash, err := authoringdomain.ComputeGeneratedRevisionRequestHash(request)
	if err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	appended, err := store.dependencies.Authoring.AppendGeneratedRevisionScoped(ctx, scope, authoringapp.GeneratedRevisionRecord{Request: request, RequestHash: hash, IdempotencyKey: "synthesis-revision:" + string(revision.ID), CreatedAt: at})
	if err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	if appended.Revision.ID != revision.ArticleRevisionID || appended.Revision.ContentHash != revision.ContentHash || appended.Document.ID != note.DocumentID {
		return organizingapp.SynthesisPublicationCommand{}, synthesisConsistency("authoring generation returned another candidate")
	}
	row, err := synthesisRevisionRecord(revision)
	if err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	if err := tx.Create(&row).Error; err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	if err := insertSynthesisRevisionSources(tx, revision); err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	if base != nil {
		changed := tx.Model(&synthesisNoteModel{}).Where("id=? AND workspace_id=? AND version=? AND current_revision_id=?", string(note.ID), string(workspaceID), base.Note.Version, string(base.Revision.ID)).Updates(map[string]any{"current_revision_id": string(note.CurrentRevisionID), "version": note.Version, "status": string(note.Status), "workflow_run_id": string(note.WorkflowRunID), "failure": nil, "updated_at": note.UpdatedAt})
		if changed.Error != nil {
			return organizingapp.SynthesisPublicationCommand{}, changed.Error
		}
		if changed.RowsAffected != 1 {
			return organizingapp.SynthesisPublicationCommand{}, synthesisConflict("synthesis candidate CAS changed")
		}
	}
	command := organizingapp.SynthesisPublicationCommand{NoteID: note.ID, RevisionID: revision.ID, DocumentID: note.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, ContentHash: revision.ContentHash, IdempotencyKey: "synthesis-publish:" + string(revision.ID)}
	publishHash, err := authoringdomain.ComputePublishRequestHash(workspaceID, note.DocumentID, revision.ArticleRevisionID)
	if err != nil {
		return organizingapp.SynthesisPublicationCommand{}, err
	}
	_, err = store.dependencies.Authoring.ReservePublicationScoped(ctx, scope, authoringapp.ReservePublicationRecord{Binding: authoringapp.PublishBinding{WorkspaceID: workspaceID, DocumentID: note.DocumentID, RevisionID: revision.ArticleRevisionID, IdempotencyKey: command.IdempotencyKey, RequestHash: publishHash}, ReservationID: ids.ReservationID, ReservedAt: at})
	return command, err
}

func insertSynthesisRevisionSources(tx *gorm.DB, revision domain.SynthesisRevision) error {
	seen := map[string]bool{}
	rows := []synthesisSourceModel{}
	for _, item := range revision.Items {
		for _, ref := range item.SourceReferences() {
			key, err := ref.IdentityKey()
			if err != nil {
				return err
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, synthesisSourceModel{WorkspaceID: string(revision.WorkspaceID), RevisionID: string(revision.ID), NoteID: string(revision.NoteID), SourceID: string(ref.Source.SourceID), SourceVersionID: string(ref.Source.SourceVersionID), ContentArtifactID: string(ref.Source.ContentArtifactID), ParseProjectionID: string(ref.Source.ParseProjectionID), SourceSpanID: string(ref.SourceSpanID), ContentHash: ref.Source.ContentHash, ExcerptHash: ref.ExcerptHash, Title: ref.Title})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.CreateInBatches(&rows, domain.MaxSynthesisSources).Error
}

func loadSynthesisNote(tx *gorm.DB, workspaceID, noteID foundation.ID, lock bool) (domain.SynthesisNote, error) {
	query := tx.Select(synthesisNoteColumns).Where("workspace_id=? AND id=?", string(workspaceID), string(noteID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row synthesisNoteModel
	err := query.Take(&row).Error
	if gormNoRows(err) {
		return domain.SynthesisNote{}, synthesisNotFound()
	}
	if err != nil {
		return domain.SynthesisNote{}, err
	}
	return row.domain()
}
func loadSynthesisRevision(tx *gorm.DB, workspaceID, noteID, revisionID foundation.ID) (domain.SynthesisRevision, error) {
	var row synthesisRevisionModel
	err := tx.Select(synthesisRevisionColumns).Where("workspace_id=? AND note_id=? AND id=?", string(workspaceID), string(noteID), string(revisionID)).Take(&row).Error
	if gormNoRows(err) {
		return domain.SynthesisRevision{}, synthesisNotFound()
	}
	if err != nil {
		return domain.SynthesisRevision{}, err
	}
	return row.domain()
}

func (store *GORMSynthesisStore) LookupSynthesisApplyResult(ctx context.Context, workspaceID, processingID foundation.ID) (organizingapp.SynthesisApplyResult, bool, error) {
	if err := store.ready(ctx, workspaceID, processingID); err != nil {
		return organizingapp.SynthesisApplyResult{}, false, err
	}
	return lookupSynthesisApplyResult(ctx, store.database, workspaceID, processingID)
}

// LookupSynthesisApplyResultScoped keeps retry decisions on the caller's
// connection while it holds the processing lock in the same Unit of Work.
func (store *GORMSynthesisStore) LookupSynthesisApplyResultScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID, processingID foundation.ID) (organizingapp.SynthesisApplyResult, bool, error) {
	if err := store.ready(ctx, workspaceID, processingID); err != nil {
		return organizingapp.SynthesisApplyResult{}, false, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return organizingapp.SynthesisApplyResult{}, false, err
	}
	return lookupSynthesisApplyResult(ctx, tx, workspaceID, processingID)
}

func lookupSynthesisApplyResult(ctx context.Context, tx *gorm.DB, workspaceID, processingID foundation.ID) (organizingapp.SynthesisApplyResult, bool, error) {
	var model synthesisApplyModel
	err := tx.WithContext(ctx).Select(synthesisApplyColumns).Where("workspace_id=? AND processing_id=?", string(workspaceID), string(processingID)).Take(&model).Error
	if gormNoRows(err) {
		return organizingapp.SynthesisApplyResult{}, false, nil
	}
	if err != nil {
		return organizingapp.SynthesisApplyResult{}, false, synthesisDBError(ctx, err)
	}
	result, err := model.result()
	return result, true, err
}

func synthesisIDPointer(value foundation.ID) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}
func synthesisUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, organizingapp.ErrorCodeSynthesisUnavailable, true, err)
}
func synthesisDBError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, organizingapp.ErrorCodeSynthesisUnavailable, errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Join(err, ctx.Err(), context.Cause(ctx)))
	}
	switch platformpostgres.SQLState(err) {
	case "23505":
		return synthesisConflict("synthesis identity or topic is already bound")
	case "23502", "23503", "23514", "22P02", "55000":
		return foundation.NewError(foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeSynthesisConsistency, false, err)
	}
	return synthesisUnavailable(err)
}
