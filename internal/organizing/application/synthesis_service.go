package application

import (
	"context"
	"errors"
	"reflect"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	ErrorCodeSynthesisUnavailable         = "SYNTHESIS_UNAVAILABLE"
	ErrorCodeSynthesisNotFound            = "SYNTHESIS_NOTE_NOT_FOUND"
	ErrorCodeSynthesisIdempotencyConflict = "SYNTHESIS_IDEMPOTENCY_CONFLICT"
	ErrorCodeSynthesisConsistency         = "SYNTHESIS_CONSISTENCY_VIOLATION"
)

// SynthesisService exposes generated candidates and governed publication. It
// does not select tools, call models, or own Source processing state.
type SynthesisService struct{ dependencies SynthesisDependencies }

func NewSynthesisService(dependencies SynthesisDependencies) (*SynthesisService, error) {
	for _, dependency := range []any{dependencies.Store, dependencies.Sources, dependencies.Publications, dependencies.IDs, dependencies.Clock} {
		if nilSynthesisDependency(dependency) {
			return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("synthesis service dependencies are required"))
		}
	}
	return &SynthesisService{dependencies: dependencies}, nil
}

func (service *SynthesisService) ready(ctx context.Context, ids ...foundation.ID) error {
	if service == nil || nilSynthesisDependency(service.dependencies.Store) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("synthesis service is unavailable"))
	}
	if ctx == nil {
		return invalid(domain.ErrorCodeSynthesisInvalid, "synthesis context is required")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	for _, id := range ids {
		if !validID(id) {
			return invalid(domain.ErrorCodeSynthesisInvalid, "synthesis identity is invalid")
		}
	}
	return nil
}

func (service *SynthesisService) ListCandidates(ctx context.Context, workspaceID foundation.ID) ([]SynthesisGenerationNote, error) {
	if err := service.ready(ctx, workspaceID); err != nil {
		return nil, err
	}
	return service.dependencies.Store.ListSynthesisCandidates(ctx, workspaceID)
}

func (service *SynthesisService) ApplyGeneration(ctx context.Context, input SynthesisGenerationInput, generation SynthesisGenerationResult) (SynthesisApplyResult, error) {
	if err := service.ready(ctx, input.SourceEvent.Source.WorkspaceID); err != nil {
		return SynthesisApplyResult{}, err
	}
	if err := generation.Validate(input); err != nil {
		return SynthesisApplyResult{}, err
	}
	allocations := make([]SynthesisCandidateIDs, len(generation.Notes))
	for index := range allocations {
		allocation := &allocations[index]
		for _, target := range []*foundation.ID{&allocation.NoteID, &allocation.DocumentID, &allocation.RevisionID, &allocation.ArticleRevisionID, &allocation.ReservationID} {
			id, err := service.dependencies.IDs.New()
			if err != nil {
				return SynthesisApplyResult{}, err
			}
			if !validID(id) {
				return SynthesisApplyResult{}, invalid(domain.ErrorCodeSynthesisInvalid, "synthesis ID generator returned an invalid identity")
			}
			*target = id
		}
	}
	result, err := service.dependencies.Store.ApplySynthesisGeneration(ctx, SynthesisApplyRecord{Input: input, Generation: generation, Candidates: allocations, AppliedAt: service.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return result, err
	}
	return result, service.completePublications(ctx, input.SourceEvent.Source.WorkspaceID, result)
}

// RecoverAppliedGeneration restores the post-commit publication window before
// a retry considers opening sources or making another paid model call.
func (service *SynthesisService) RecoverAppliedGeneration(ctx context.Context, workspaceID, processingID foundation.ID) (SynthesisApplyResult, bool, error) {
	if err := service.ready(ctx, workspaceID, processingID); err != nil {
		return SynthesisApplyResult{}, false, err
	}
	result, found, err := service.dependencies.Store.LookupSynthesisApplyResult(ctx, workspaceID, processingID)
	if err != nil || !found {
		return result, found, err
	}
	result.Replayed = true
	return result, true, service.completePublications(ctx, workspaceID, result)
}

func (service *SynthesisService) completePublications(ctx context.Context, workspaceID foundation.ID, result SynthesisApplyResult) error {
	if err := ValidateSynthesisApplyResult(result); err != nil {
		return err
	}
	for _, command := range result.Publications {
		published, err := service.dependencies.Publications.PublishArticleRevision(ctx, authoringapp.PublishCommand{WorkspaceID: workspaceID, DocumentID: command.DocumentID, RevisionID: command.ArticleRevisionID, IdempotencyKey: command.IdempotencyKey})
		if err != nil {
			return err
		}
		binding := published.Publication
		if binding.Validate() != nil || binding.WorkspaceID != workspaceID || binding.DocumentID != command.DocumentID || binding.ArticleRevisionID != command.ArticleRevisionID || binding.ContentHash != command.ContentHash {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisConsistency, false, errors.New("authoring returned another synthesis publication"))
		}
	}
	return nil
}

func (service *SynthesisService) ListNotes(ctx context.Context, query SynthesisListQuery) (SynthesisNotePage, error) {
	if err := service.ready(ctx, query.WorkspaceID); err != nil {
		return SynthesisNotePage{}, err
	}
	if query.Limit == 0 {
		query.Limit = DefaultSynthesisListLimit
	}
	if query.Limit < 1 || query.Limit > MaxSynthesisListLimit || (query.BeforeTime == nil) != (query.BeforeID == "") || query.BeforeTime != nil && (query.BeforeTime.IsZero() || !validID(query.BeforeID)) {
		return SynthesisNotePage{}, invalid(domain.ErrorCodeSynthesisInvalid, "synthesis page is invalid")
	}
	return service.dependencies.Store.ListSynthesisNotes(ctx, query)
}

func (service *SynthesisService) GetNote(ctx context.Context, workspaceID, noteID foundation.ID) (SynthesisNoteDetail, error) {
	if err := service.ready(ctx, workspaceID, noteID); err != nil {
		return SynthesisNoteDetail{}, err
	}
	return service.dependencies.Store.GetSynthesisNote(ctx, workspaceID, noteID)
}

func (service *SynthesisService) GetSynthesisRevision(ctx context.Context, workspaceID, noteID, revisionID foundation.ID) (domain.SynthesisRevision, error) {
	if err := service.ready(ctx, workspaceID, noteID, revisionID); err != nil {
		return domain.SynthesisRevision{}, err
	}
	return service.dependencies.Store.GetSynthesisRevision(ctx, workspaceID, noteID, revisionID)
}

func (service *SynthesisService) ListRevisions(ctx context.Context, query SynthesisRevisionListQuery) (SynthesisRevisionPage, error) {
	if err := service.ready(ctx, query.WorkspaceID, query.NoteID); err != nil {
		return SynthesisRevisionPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = DefaultSynthesisListLimit
	}
	if query.Limit < 1 || query.Limit > MaxSynthesisListLimit || query.BeforeRevisionNo < 0 {
		return SynthesisRevisionPage{}, invalid(domain.ErrorCodeSynthesisInvalid, "synthesis history page is invalid")
	}
	return service.dependencies.Store.ListSynthesisRevisions(ctx, query)
}

func (service *SynthesisService) OpenSource(ctx context.Context, workspaceID, noteID, revisionID foundation.ID, reference domain.SynthesisSourceRef) (SynthesisSourceView, error) {
	revision, err := service.GetSynthesisRevision(ctx, workspaceID, noteID, revisionID)
	if err != nil {
		return SynthesisSourceView{}, err
	}
	if reference.Validate() != nil || reference.Source.WorkspaceID != workspaceID {
		return SynthesisSourceView{}, invalid(domain.ErrorCodeSynthesisSourceInvalid, "synthesis source binding is invalid")
	}
	found := false
	for _, item := range revision.Items {
		for _, stored := range item.SourceReferences() {
			if stored == reference {
				found = true
				break
			}
		}
	}
	if !found {
		return SynthesisSourceView{}, foundation.NewError(foundation.ErrorNotFound, "SYNTHESIS_SOURCE_NOT_FOUND", false, errors.New("source is not part of this synthesis revision"))
	}
	view, err := service.dependencies.Sources.OpenSynthesisSource(ctx, reference)
	if err != nil {
		return view, err
	}
	if view.Reference != reference || view.Validate(workspaceID) != nil {
		return SynthesisSourceView{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisConsistency, false, errors.New("source reader changed the requested historical reference"))
	}
	return view, nil
}

func (service *SynthesisService) ReadPublishedSynthesisNote(ctx context.Context, workspaceID, noteID foundation.ID) (domain.SynthesisNoteSnapshot, error) {
	if err := service.ready(ctx, workspaceID, noteID); err != nil {
		return domain.SynthesisNoteSnapshot{}, err
	}
	return service.dependencies.Store.ReadPublishedSynthesisNote(ctx, workspaceID, noteID)
}

func ValidateSynthesisApplyResult(result SynthesisApplyResult) error {
	if !validID(result.ProcessingID) || result.RevisionIDs == nil || result.Publications == nil || len(result.RevisionIDs) != len(result.Publications) || len(result.RevisionIDs) > MaxSynthesisGeneratedNotes || result.Changed != (len(result.RevisionIDs) > 0) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisConsistency, false, errors.New("synthesis application receipt is invalid"))
	}
	seen := make(map[foundation.ID]bool)
	for index, command := range result.Publications {
		if !validID(command.NoteID) || !validID(command.RevisionID) || !validID(command.DocumentID) || !validID(command.ArticleRevisionID) || command.RevisionID != result.RevisionIDs[index] || !validSynthesisHash(command.ContentHash) || command.IdempotencyKey != "synthesis-publish:"+string(command.RevisionID) || seen[command.RevisionID] {
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisConsistency, false, errors.New("synthesis publication receipt binding is invalid"))
		}
		seen[command.RevisionID] = true
	}
	return nil
}

func nilSynthesisDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
