package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const defaultSuggestionLimit = 25

// Dependencies contains Organizing owner stores and bounded cross-module ports.
type Dependencies struct {
	Drafts      DraftRepository
	Templates   TemplateRepository
	Starts      StartRepository
	Materials   MaterialResolver
	Suggestions SuggestionProvider
	Searches    MaterialSearchProvider
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

// SearchMaterials returns a complete, bounded list of owner-backed identities for same-page addition.
func (service *Service) SearchMaterials(ctx context.Context, query MaterialSearchQuery) (MaterialSearchPage, error) {
	if service == nil || validateContext(ctx) != nil || !validID(query.WorkspaceID) || !materialKind(query.Kind) {
		return MaterialSearchPage{}, invalid(ErrorCodeRequestInvalid, "organizing material search request is invalid")
	}
	if service.dependencies.Searches == nil {
		return MaterialSearchPage{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New("organizing material search provider is unavailable"))
	}
	query.Query = strings.TrimSpace(query.Query)
	if query.Limit == 0 {
		query.Limit = DefaultMaterialSearchLimit
	}
	if query.Query == "" || len([]byte(query.Query)) < 2 || !utf8.ValidString(query.Query) || len([]byte(query.Query)) > MaxMaterialSearchQueryBytes ||
		query.Limit < 1 || query.Limit > MaxMaterialSearchLimit || containsControl(query.Query) {
		return MaterialSearchPage{}, invalid(ErrorCodeRequestInvalid, "organizing material search request is invalid")
	}
	items, err := service.dependencies.Searches.SearchMaterials(ctx, query)
	if err != nil {
		return MaterialSearchPage{}, err
	}
	if items == nil || len(items) > query.Limit {
		return MaterialSearchPage{}, inconsistent("material search provider returned an invalid result bound")
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]MaterialSearchHit, len(items))
	for index, item := range items {
		key, ok := materialSearchHitKey(item, query.Kind)
		if !ok {
			return MaterialSearchPage{}, inconsistent("material search provider returned an invalid hit")
		}
		if _, duplicate := seen[key]; duplicate {
			return MaterialSearchPage{}, inconsistent("material search provider returned duplicate identities")
		}
		seen[key] = struct{}{}
		result[index] = item
	}
	return MaterialSearchPage{WorkspaceID: query.WorkspaceID, Query: query.Query, Kind: query.Kind, Items: result}, nil
}

func materialSearchHitKey(hit MaterialSearchHit, kind domain.MaterialKind) (string, bool) {
	if hit.Selector.Kind != kind || !validSelector(hit.Selector) || hit.Title == "" || hit.Title != strings.TrimSpace(hit.Title) ||
		!utf8.ValidString(hit.Title) || len([]byte(hit.Title)) > 512 || containsControl(hit.Title) ||
		(hit.Availability != domain.MaterialAvailable && hit.Availability != domain.MaterialStale && hit.Availability != domain.MaterialUnavailable) {
		return "", false
	}
	switch kind {
	case domain.MaterialSourceVersion:
		return string(kind) + ":" + string(hit.Selector.SourceVersionID), true
	case domain.MaterialDocumentRevision:
		return string(kind) + ":" + string(hit.Selector.DocumentID) + ":" + string(hit.Selector.ArticleRevisionID), true
	case domain.MaterialClaim:
		return string(kind) + ":" + string(hit.Selector.ClaimID), true
	case domain.MaterialSmartCollection:
		return string(kind) + ":" + string(hit.Selector.CollectionID), true
	default:
		return "", false
	}
}

func materialKind(kind domain.MaterialKind) bool {
	return kind == domain.MaterialSourceVersion || kind == domain.MaterialDocumentRevision ||
		kind == domain.MaterialClaim || kind == domain.MaterialSmartCollection
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

// Service coordinates Draft, Snapshot and Template commands.
type Service struct{ dependencies Dependencies }

// NewService creates a fail-closed Organizing service. Optional search and suggestion providers fail closed at their endpoints.
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Drafts == nil || dependencies.Templates == nil || dependencies.Starts == nil ||
		dependencies.Materials == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New("organizing dependencies are incomplete"))
	}
	return &Service{dependencies: dependencies}, nil
}

// CreateDraft creates one recoverable Suggested Material Set.
func (service *Service) CreateDraft(ctx context.Context, command CreateDraftCommand) (DraftResult, error) {
	if err := validateContext(ctx); err != nil || !validID(command.WorkspaceID) {
		return DraftResult{}, invalid(ErrorCodeRequestInvalid, "organizing draft create request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return DraftResult{}, err
	}
	intent := strings.TrimSpace(command.Intent)
	hash, err := requestHash(CommandCreateDraft, command.WorkspaceID, "", 0, struct {
		Intent string `json:"intent"`
	}{intent})
	if err != nil {
		return DraftResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandCreateDraft}
	if replay, found, err := service.dependencies.Drafts.FindDraftCommand(ctx, binding); err != nil || found {
		return validateDraftCommandResult(replay, command.WorkspaceID, true, err)
	}
	id, err := service.dependencies.IDs.New()
	if err != nil {
		return DraftResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return DraftResult{}, err
	}
	draft, err := domain.NewDraft(id, command.WorkspaceID, intent, now)
	if err != nil {
		return DraftResult{}, err
	}
	binding.AggregateID = id
	result, err := service.dependencies.Drafts.CreateDraft(ctx, CreateDraftRecord{Binding: binding, Draft: draft})
	return validateDraftCommandResult(result, command.WorkspaceID, false, err)
}

// GetDraft reloads one Workspace-owned mutable Draft.
func (service *Service) GetDraft(ctx context.Context, workspaceID, draftID foundation.ID) (domain.Draft, error) {
	if err := validateContext(ctx); err != nil || !validID(workspaceID) || !validID(draftID) {
		return domain.Draft{}, invalid(ErrorCodeRequestInvalid, "organizing draft lookup is invalid")
	}
	draft, err := service.dependencies.Drafts.GetDraft(ctx, workspaceID, draftID)
	if err != nil {
		return domain.Draft{}, err
	}
	if draft.WorkspaceID != workspaceID || draft.ID != draftID || draft.Validate() != nil {
		return domain.Draft{}, inconsistent("draft repository returned an invalid binding")
	}
	return draft, nil
}

// GetRunProjection returns the durable dispatch state and any immutable Run facts for one Snapshot.
func (service *Service) GetRunProjection(ctx context.Context, workspaceID, snapshotID foundation.ID) (RunProjection, error) {
	if err := validateContext(ctx); err != nil || !validID(workspaceID) || !validID(snapshotID) {
		return RunProjection{}, invalid(ErrorCodeRequestInvalid, "organizing run projection lookup is invalid")
	}
	projection, err := service.dependencies.Starts.GetRunProjection(ctx, workspaceID, snapshotID)
	if err != nil {
		return RunProjection{}, err
	}
	if projection.WorkspaceID != workspaceID || projection.SnapshotID != snapshotID || !projection.Valid() {
		return RunProjection{}, inconsistent("start repository returned an invalid run projection")
	}
	return projection, nil
}

// UpdateDraft updates the intent and selected Template Revision under expected-version CAS.
func (service *Service) UpdateDraft(ctx context.Context, command UpdateDraftCommand) (DraftResult, error) {
	if err := validateDraftMutation(ctx, command.WorkspaceID, command.DraftID, command.ExpectedVersion); err != nil {
		return DraftResult{}, err
	}
	if !validID(command.TemplateRevisionID) {
		return DraftResult{}, invalid(ErrorCodeRequestInvalid, "organizing draft template revision is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return DraftResult{}, err
	}
	intent := strings.TrimSpace(command.Intent)
	hash, err := requestHash(CommandUpdateDraft, command.WorkspaceID, command.DraftID, command.ExpectedVersion, struct {
		Intent             string        `json:"intent"`
		TemplateRevisionID foundation.ID `json:"template_revision_id"`
	}{intent, command.TemplateRevisionID})
	if err != nil {
		return DraftResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, AggregateID: command.DraftID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandUpdateDraft, ExpectedVersion: command.ExpectedVersion}
	if replay, found, err := service.dependencies.Drafts.FindDraftCommand(ctx, binding); err != nil || found {
		return validateDraftCommandResult(replay, command.WorkspaceID, true, err)
	}
	template, err := service.dependencies.Templates.GetTemplateRevision(ctx, command.WorkspaceID, command.TemplateRevisionID)
	if err != nil {
		return DraftResult{}, err
	}
	if err := validateTemplateDetail(template, command.WorkspaceID); err != nil {
		return DraftResult{}, err
	}
	if template.Revision.ID != command.TemplateRevisionID {
		return DraftResult{}, inconsistent("template repository returned an invalid revision binding")
	}
	now, err := service.now()
	if err != nil {
		return DraftResult{}, err
	}
	result, err := service.dependencies.Drafts.UpdateDraft(ctx, UpdateDraftRecord{
		Binding: binding, Intent: intent, TemplateRevisionID: command.TemplateRevisionID, UpdatedAt: now,
	})
	return validateDraftCommandResult(result, command.WorkspaceID, false, err)
}

// AddMaterial resolves and adds one exact owner-backed material.
func (service *Service) AddMaterial(ctx context.Context, command AddMaterialCommand) (DraftResult, error) {
	if err := validateDraftMutation(ctx, command.WorkspaceID, command.DraftID, command.ExpectedVersion); err != nil || !validSelector(command.Selector) {
		return DraftResult{}, invalid(ErrorCodeRequestInvalid, "organizing material add request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return DraftResult{}, err
	}
	hash, err := requestHash(CommandAddMaterial, command.WorkspaceID, command.DraftID, command.ExpectedVersion, command.Selector)
	if err != nil {
		return DraftResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, AggregateID: command.DraftID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandAddMaterial, ExpectedVersion: command.ExpectedVersion}
	if replay, found, err := service.dependencies.Drafts.FindDraftCommand(ctx, binding); err != nil || found {
		return validateDraftCommandResult(replay, command.WorkspaceID, true, err)
	}
	draft, err := service.GetDraft(ctx, command.WorkspaceID, command.DraftID)
	if err != nil {
		return DraftResult{}, err
	}
	if draft.Version != command.ExpectedVersion || draft.Status != domain.DraftEditing {
		return DraftResult{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, errors.New("organizing draft expected version is stale"))
	}
	candidates, err := service.dependencies.Materials.Resolve(ctx, command.WorkspaceID, []MaterialSelector{command.Selector})
	if err != nil {
		return DraftResult{}, err
	}
	if len(candidates) != 1 {
		return DraftResult{}, inconsistent("material resolver did not return exactly one candidate")
	}
	id, err := service.dependencies.IDs.New()
	if err != nil {
		return DraftResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return DraftResult{}, err
	}
	material, err := materialFromCandidate(id, draft.ID, len(draft.Materials), candidates[0], domain.MaterialOriginUser, now)
	if err != nil {
		return DraftResult{}, err
	}
	materials := append(append([]domain.DraftMaterial(nil), draft.Materials...), material)
	result, err := service.dependencies.Drafts.ReplaceMaterials(ctx, ReplaceMaterialsRecord{Binding: binding, Materials: materials, UpdatedAt: now})
	return validateDraftCommandResult(result, command.WorkspaceID, false, err)
}

// RemoveMaterial removes one Draft-owned candidate.
func (service *Service) RemoveMaterial(ctx context.Context, command RemoveMaterialCommand) (DraftResult, error) {
	if err := validateDraftMutation(ctx, command.WorkspaceID, command.DraftID, command.ExpectedVersion); err != nil || !validID(command.MaterialID) {
		return DraftResult{}, invalid(ErrorCodeRequestInvalid, "organizing material remove request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return DraftResult{}, err
	}
	hash, err := requestHash(CommandRemoveMaterial, command.WorkspaceID, command.DraftID, command.ExpectedVersion, struct {
		MaterialID foundation.ID `json:"material_id"`
	}{command.MaterialID})
	if err != nil {
		return DraftResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, AggregateID: command.DraftID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandRemoveMaterial, ExpectedVersion: command.ExpectedVersion}
	if replay, found, err := service.dependencies.Drafts.FindDraftCommand(ctx, binding); err != nil || found {
		return validateDraftCommandResult(replay, command.WorkspaceID, true, err)
	}
	draft, err := service.GetDraft(ctx, command.WorkspaceID, command.DraftID)
	if err != nil {
		return DraftResult{}, err
	}
	materials := make([]domain.DraftMaterial, 0, len(draft.Materials))
	found := false
	for _, material := range draft.Materials {
		if material.ID == command.MaterialID {
			found = true
			continue
		}
		material.Position = len(materials)
		materials = append(materials, material)
	}
	if !found {
		return DraftResult{}, foundation.NewError(foundation.ErrorNotFound, ErrorCodeNotFound, false, errors.New("organizing material was not found"))
	}
	now, err := service.now()
	if err != nil {
		return DraftResult{}, err
	}
	result, err := service.dependencies.Drafts.ReplaceMaterials(ctx, ReplaceMaterialsRecord{Binding: binding, Materials: materials, UpdatedAt: now})
	return validateDraftCommandResult(result, command.WorkspaceID, false, err)
}

// SetMaterialSelection records the user's explicit authorization for one candidate.
func (service *Service) SetMaterialSelection(ctx context.Context, command SetMaterialSelectionCommand) (DraftResult, error) {
	if err := validateDraftMutation(ctx, command.WorkspaceID, command.DraftID, command.ExpectedVersion); err != nil || !validID(command.MaterialID) {
		return DraftResult{}, invalid(ErrorCodeRequestInvalid, "organizing material selection request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return DraftResult{}, err
	}
	hash, err := requestHash(CommandSetMaterialSelection, command.WorkspaceID, command.DraftID, command.ExpectedVersion, struct {
		MaterialID foundation.ID `json:"material_id"`
		Selected   bool          `json:"selected"`
	}{command.MaterialID, command.Selected})
	if err != nil {
		return DraftResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, AggregateID: command.DraftID, IdempotencyKey: key,
		RequestHash: hash, CommandType: CommandSetMaterialSelection, ExpectedVersion: command.ExpectedVersion}
	if replay, found, err := service.dependencies.Drafts.FindDraftCommand(ctx, binding); err != nil || found {
		return validateDraftCommandResult(replay, command.WorkspaceID, true, err)
	}
	draft, err := service.GetDraft(ctx, command.WorkspaceID, command.DraftID)
	if err != nil {
		return DraftResult{}, err
	}
	materials := append([]domain.DraftMaterial(nil), draft.Materials...)
	found := false
	for index := range materials {
		if materials[index].ID == command.MaterialID {
			materials[index].Selected = command.Selected
			found = true
			break
		}
	}
	if !found {
		return DraftResult{}, foundation.NewError(foundation.ErrorNotFound, ErrorCodeNotFound, false, errors.New("organizing material was not found"))
	}
	now, err := service.now()
	if err != nil {
		return DraftResult{}, err
	}
	result, err := service.dependencies.Drafts.ReplaceMaterials(ctx, ReplaceMaterialsRecord{Binding: binding, Materials: materials, UpdatedAt: now})
	return validateDraftCommandResult(result, command.WorkspaceID, false, err)
}

// Suggest replaces system suggestions but never confirms them.
func (service *Service) Suggest(ctx context.Context, command SuggestCommand) (DraftResult, error) {
	if service.dependencies.Suggestions == nil {
		return DraftResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDependencyUnavailable, true, errors.New("organizing suggestion provider is unavailable"))
	}
	if command.Limit == 0 {
		command.Limit = defaultSuggestionLimit
	}
	if err := validateDraftMutation(ctx, command.WorkspaceID, command.DraftID, command.ExpectedVersion); err != nil || command.Limit < 1 || command.Limit > domain.MaxDraftMaterials {
		return DraftResult{}, invalid(ErrorCodeRequestInvalid, "organizing suggestion request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return DraftResult{}, err
	}
	hash, err := requestHash(CommandReplaceSuggestions, command.WorkspaceID, command.DraftID, command.ExpectedVersion, struct {
		Limit int `json:"limit"`
	}{command.Limit})
	if err != nil {
		return DraftResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, AggregateID: command.DraftID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandReplaceSuggestions, ExpectedVersion: command.ExpectedVersion}
	if replay, found, err := service.dependencies.Drafts.FindDraftCommand(ctx, binding); err != nil || found {
		return validateDraftCommandResult(replay, command.WorkspaceID, true, err)
	}
	draft, err := service.GetDraft(ctx, command.WorkspaceID, command.DraftID)
	if err != nil {
		return DraftResult{}, err
	}
	candidates, err := service.dependencies.Suggestions.Suggest(ctx, SuggestionQuery{WorkspaceID: command.WorkspaceID, Intent: draft.Intent, Limit: command.Limit})
	if err != nil {
		return DraftResult{}, err
	}
	if len(candidates) > command.Limit {
		return DraftResult{}, inconsistent("suggestion provider exceeded the requested bound")
	}
	materials := make([]domain.DraftMaterial, 0, len(draft.Materials)+len(candidates))
	identities := make(map[string]struct{}, len(draft.Materials)+len(candidates))
	for _, current := range draft.Materials {
		if current.Origin == domain.MaterialOriginUser {
			current.Position = len(materials)
			materials = append(materials, current)
			identity, err := current.Ref.IdentityKey()
			if err != nil {
				return DraftResult{}, err
			}
			identities[identity] = struct{}{}
		}
	}
	now, err := service.now()
	if err != nil {
		return DraftResult{}, err
	}
	for _, candidate := range candidates {
		id, err := service.dependencies.IDs.New()
		if err != nil {
			return DraftResult{}, err
		}
		material, err := materialFromCandidate(id, draft.ID, len(materials), candidate, domain.MaterialOriginSuggested, now)
		if err != nil {
			return DraftResult{}, err
		}
		identity, err := material.Ref.IdentityKey()
		if err != nil {
			return DraftResult{}, err
		}
		if _, duplicate := identities[identity]; duplicate {
			continue
		}
		identities[identity] = struct{}{}
		materials = append(materials, material)
	}
	if len(materials) > domain.MaxDraftMaterials {
		return DraftResult{}, invalid(domain.ErrorCodeMaterialInvalid, "suggested material set exceeds the draft bound")
	}
	result, err := service.dependencies.Drafts.ReplaceMaterials(ctx, ReplaceMaterialsRecord{Binding: binding, Materials: materials, UpdatedAt: now})
	return validateDraftCommandResult(result, command.WorkspaceID, false, err)
}

// ConfirmDraft revalidates exact owner facts, then atomically freezes Snapshot and Start Outbox.
func (service *Service) ConfirmDraft(ctx context.Context, command ConfirmCommand) (ConfirmResult, error) {
	if err := validateDraftMutation(ctx, command.WorkspaceID, command.DraftID, command.ExpectedVersion); err != nil || !validID(command.TemplateRevisionID) {
		return ConfirmResult{}, invalid(ErrorCodeRequestInvalid, "organizing confirm request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return ConfirmResult{}, err
	}
	hash, err := requestHash(CommandConfirmDraft, command.WorkspaceID, command.DraftID, command.ExpectedVersion, struct {
		TemplateRevisionID foundation.ID `json:"template_revision_id"`
	}{command.TemplateRevisionID})
	if err != nil {
		return ConfirmResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, AggregateID: command.DraftID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandConfirmDraft, ExpectedVersion: command.ExpectedVersion}
	if replay, found, err := service.dependencies.Drafts.FindConfirmCommand(ctx, binding); err != nil || found {
		return validateConfirmResult(replay, command.WorkspaceID, true, err)
	}
	draft, err := service.GetDraft(ctx, command.WorkspaceID, command.DraftID)
	if err != nil {
		return ConfirmResult{}, err
	}
	if draft.Version != command.ExpectedVersion || draft.Status != domain.DraftEditing {
		return ConfirmResult{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, errors.New("organizing draft expected version is stale"))
	}
	if draft.TemplateRevisionID == "" || draft.TemplateRevisionID != command.TemplateRevisionID {
		return ConfirmResult{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, errors.New("organizing draft template revision changed before confirmation"))
	}
	template, err := service.dependencies.Templates.GetTemplateRevision(ctx, command.WorkspaceID, command.TemplateRevisionID)
	if err != nil {
		return ConfirmResult{}, err
	}
	if err := validateTemplateDetail(template, command.WorkspaceID); err != nil {
		return ConfirmResult{}, err
	}
	if template.Revision.ID != command.TemplateRevisionID {
		return ConfirmResult{}, inconsistent("template repository returned an invalid revision binding")
	}
	selected := make([]domain.MaterialRef, 0, len(draft.Materials))
	for _, material := range draft.Materials {
		if !material.Selected {
			continue
		}
		if material.Availability != domain.MaterialAvailable {
			return ConfirmResult{}, foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeMaterialStale, false, errors.New("selected organizing material is not available"))
		}
		selected = append(selected, material.Ref)
	}
	if len(selected) < 1 {
		return ConfirmResult{}, invalid(domain.ErrorCodeSnapshotInvalid, "at least one selected material is required")
	}
	frozen, err := service.dependencies.Materials.Freeze(ctx, command.WorkspaceID, selected)
	if err != nil {
		return ConfirmResult{}, err
	}
	if err := validateFrozenMaterials(selected, frozen, template.Revision.Declaration.Materials); err != nil {
		return ConfirmResult{}, err
	}
	snapshotID, err := service.dependencies.IDs.New()
	if err != nil {
		return ConfirmResult{}, err
	}
	outboxID, err := service.dependencies.IDs.New()
	if err != nil {
		return ConfirmResult{}, err
	}
	materialIDs := make([]foundation.ID, len(frozen))
	for index := range materialIDs {
		materialIDs[index], err = service.dependencies.IDs.New()
		if err != nil {
			return ConfirmResult{}, err
		}
	}
	now, err := service.now()
	if err != nil {
		return ConfirmResult{}, err
	}
	result, err := service.dependencies.Drafts.ConfirmDraft(ctx, ConfirmRecord{Binding: binding, Fence: service.dependencies.Materials, SnapshotID: snapshotID,
		SnapshotMaterialIDs: materialIDs, OutboxID: outboxID, TemplateID: template.Template.ID,
		TemplateRevisionID: template.Revision.ID, TemplateHash: template.Revision.DeclarationHash,
		FrozenMaterials: frozen, ConfirmedAt: now})
	return validateConfirmResult(result, command.WorkspaceID, false, err)
}

// GetSnapshot returns one immutable Workflow input.
func (service *Service) GetSnapshot(ctx context.Context, workspaceID, snapshotID foundation.ID) (domain.Snapshot, error) {
	if err := validateContext(ctx); err != nil || !validID(workspaceID) || !validID(snapshotID) {
		return domain.Snapshot{}, invalid(ErrorCodeRequestInvalid, "organizing snapshot lookup is invalid")
	}
	snapshot, err := service.dependencies.Drafts.GetSnapshot(ctx, workspaceID, snapshotID)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if snapshot.WorkspaceID != workspaceID || snapshot.ID != snapshotID || snapshot.Validate() != nil {
		return domain.Snapshot{}, inconsistent("snapshot repository returned an invalid binding")
	}
	return snapshot, nil
}

// CreateTemplate creates a Workspace custom template and Revision 1.
func (service *Service) CreateTemplate(ctx context.Context, command CreateTemplateCommand) (TemplateResult, error) {
	if err := validateContext(ctx); err != nil || !validID(command.WorkspaceID) {
		return TemplateResult{}, invalid(ErrorCodeRequestInvalid, "organizing template create request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return TemplateResult{}, err
	}
	declaration, _, err := domain.CanonicalTemplateDeclaration(command.Declaration)
	if err != nil {
		return TemplateResult{}, err
	}
	hash, err := requestHash(CommandCreateTemplate, command.WorkspaceID, "", 0, declaration)
	if err != nil {
		return TemplateResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandCreateTemplate}
	if replay, found, err := service.dependencies.Templates.FindTemplateCommand(ctx, binding); err != nil || found {
		return validateTemplateResult(replay, command.WorkspaceID, true, err)
	}
	templateID, err := service.dependencies.IDs.New()
	if err != nil {
		return TemplateResult{}, err
	}
	revisionID, err := service.dependencies.IDs.New()
	if err != nil {
		return TemplateResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return TemplateResult{}, err
	}
	template, revision, err := domain.NewCustomTemplate(templateID, revisionID, command.WorkspaceID, declaration, now)
	if err != nil {
		return TemplateResult{}, err
	}
	binding.AggregateID = templateID
	result, err := service.dependencies.Templates.CreateTemplate(ctx, CreateTemplateRecord{Binding: binding, Template: template, Revision: revision})
	return validateTemplateResult(result, command.WorkspaceID, false, err)
}

// CloneTemplate clones one readable template declaration into a Workspace custom template.
func (service *Service) CloneTemplate(ctx context.Context, command CloneTemplateCommand) (TemplateResult, error) {
	if err := validateContext(ctx); err != nil || !validID(command.WorkspaceID) || !validID(command.SourceTemplateID) {
		return TemplateResult{}, invalid(ErrorCodeRequestInvalid, "organizing template clone request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return TemplateResult{}, err
	}
	name := strings.TrimSpace(command.Name)
	hash, err := requestHash(CommandCloneTemplate, command.WorkspaceID, command.SourceTemplateID, 0, struct {
		Name string `json:"name"`
	}{name})
	if err != nil {
		return TemplateResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandCloneTemplate}
	if replay, found, err := service.dependencies.Templates.FindTemplateCommand(ctx, binding); err != nil || found {
		return validateTemplateResult(replay, command.WorkspaceID, true, err)
	}
	source, err := service.dependencies.Templates.GetTemplate(ctx, command.WorkspaceID, command.SourceTemplateID)
	if err != nil {
		return TemplateResult{}, err
	}
	templateID, err := service.dependencies.IDs.New()
	if err != nil {
		return TemplateResult{}, err
	}
	revisionID, err := service.dependencies.IDs.New()
	if err != nil {
		return TemplateResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return TemplateResult{}, err
	}
	template, revision, err := domain.CloneBuiltIn(source.Revision, templateID, revisionID, command.WorkspaceID, name, now)
	if err != nil {
		return TemplateResult{}, err
	}
	binding.AggregateID = templateID
	result, err := service.dependencies.Templates.CreateTemplate(ctx, CreateTemplateRecord{Binding: binding, Template: template, Revision: revision})
	return validateTemplateResult(result, command.WorkspaceID, false, err)
}

// ReviseTemplate appends one immutable custom Template Revision.
func (service *Service) ReviseTemplate(ctx context.Context, command ReviseTemplateCommand) (TemplateResult, error) {
	if err := validateContext(ctx); err != nil || !validID(command.WorkspaceID) || !validID(command.TemplateID) || command.ExpectedVersion < 1 {
		return TemplateResult{}, invalid(ErrorCodeRequestInvalid, "organizing template revision request is invalid")
	}
	key, err := normalizeKey(command.IdempotencyKey)
	if err != nil {
		return TemplateResult{}, err
	}
	declaration, _, err := domain.CanonicalTemplateDeclaration(command.Declaration)
	if err != nil {
		return TemplateResult{}, err
	}
	hash, err := requestHash(CommandReviseTemplate, command.WorkspaceID, command.TemplateID, command.ExpectedVersion, declaration)
	if err != nil {
		return TemplateResult{}, err
	}
	binding := CommandBinding{WorkspaceID: command.WorkspaceID, AggregateID: command.TemplateID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandReviseTemplate, ExpectedVersion: command.ExpectedVersion}
	if replay, found, err := service.dependencies.Templates.FindTemplateCommand(ctx, binding); err != nil || found {
		return validateTemplateResult(replay, command.WorkspaceID, true, err)
	}
	current, err := service.dependencies.Templates.GetTemplate(ctx, command.WorkspaceID, command.TemplateID)
	if err != nil {
		return TemplateResult{}, err
	}
	revisionID, err := service.dependencies.IDs.New()
	if err != nil {
		return TemplateResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return TemplateResult{}, err
	}
	template, revision, err := domain.ReviseCustomTemplate(current.Template, command.ExpectedVersion, revisionID, declaration, now)
	if err != nil {
		return TemplateResult{}, err
	}
	result, err := service.dependencies.Templates.ReviseTemplate(ctx, ReviseTemplateRecord{Binding: binding, Template: template, Revision: revision})
	return validateTemplateResult(result, command.WorkspaceID, false, err)
}

// GetTemplate reads one Workspace-visible custom or global built-in template.
func (service *Service) GetTemplate(ctx context.Context, workspaceID, templateID foundation.ID) (TemplateDetail, error) {
	if err := validateContext(ctx); err != nil || !validID(workspaceID) || !validID(templateID) {
		return TemplateDetail{}, invalid(ErrorCodeRequestInvalid, "organizing template lookup is invalid")
	}
	detail, err := service.dependencies.Templates.GetTemplate(ctx, workspaceID, templateID)
	if err != nil {
		return TemplateDetail{}, err
	}
	if err := validateTemplateDetail(detail, workspaceID); err != nil {
		return TemplateDetail{}, err
	}
	return detail, nil
}

// ListTemplates returns global built-ins and Workspace custom templates.
func (service *Service) ListTemplates(ctx context.Context, query TemplateListQuery) (TemplatePage, error) {
	if err := validateContext(ctx); err != nil || !validID(query.WorkspaceID) || (query.Kind != "" && !query.Kind.Valid()) {
		return TemplatePage{}, invalid(ErrorCodeRequestInvalid, "organizing template list request is invalid")
	}
	if query.Limit == 0 {
		query.Limit = 100
	}
	if query.Limit < 1 || query.Limit > 100 {
		return TemplatePage{}, invalid(ErrorCodeRequestInvalid, "organizing template list limit is invalid")
	}
	page, err := service.dependencies.Templates.ListTemplates(ctx, query)
	if err != nil {
		return TemplatePage{}, err
	}
	if len(page.Items) > query.Limit {
		return TemplatePage{}, inconsistent("template repository exceeded the requested bound")
	}
	for _, detail := range page.Items {
		if err := validateTemplateDetail(detail, query.WorkspaceID); err != nil {
			return TemplatePage{}, err
		}
	}
	return page, nil
}

// StartIdempotencyKey is the only Workflow-start key accepted for a Snapshot.
func StartIdempotencyKey(snapshotID foundation.ID) string { return "snapshot:" + string(snapshotID) }

func (service *Service) now() (time.Time, error) {
	now := service.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return time.Time{}, inconsistent("organizing clock returned zero time")
	}
	return now, nil
}

func validateDraftMutation(ctx context.Context, workspaceID, draftID foundation.ID, expectedVersion int64) error {
	if err := validateContext(ctx); err != nil || !validID(workspaceID) || !validID(draftID) || expectedVersion < 1 {
		return invalid(ErrorCodeRequestInvalid, "organizing draft mutation request is invalid")
	}
	return nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return invalid(ErrorCodeRequestInvalid, "context is nil")
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func normalizeKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxIdempotencyKeyBytes || strings.ContainsAny(value, "\r\n\x00") {
		return "", invalid(ErrorCodeIdempotencyInvalid, "organizing idempotency key is invalid")
	}
	return value, nil
}

func validSelector(selector MaterialSelector) bool {
	validSource := validID(selector.SourceVersionID)
	validDocument := validID(selector.DocumentID)
	validRevision := validID(selector.ArticleRevisionID)
	validClaim := validID(selector.ClaimID)
	validCollection := validID(selector.CollectionID)
	switch selector.Kind {
	case domain.MaterialSourceVersion:
		return validSource && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.ClaimID == "" && selector.CollectionID == ""
	case domain.MaterialDocumentRevision:
		return validDocument && validRevision && selector.SourceVersionID == "" && selector.ClaimID == "" && selector.CollectionID == ""
	case domain.MaterialClaim:
		return validClaim && selector.SourceVersionID == "" && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.CollectionID == ""
	case domain.MaterialSmartCollection:
		return validCollection && selector.SourceVersionID == "" && selector.DocumentID == "" && selector.ArticleRevisionID == "" && selector.ClaimID == ""
	default:
		return false
	}
}

func materialFromCandidate(id, draftID foundation.ID, position int, candidate MaterialCandidate, origin domain.MaterialOrigin, createdAt time.Time) (domain.DraftMaterial, error) {
	if !validID(id) || !validID(draftID) || position < 0 || candidate.Availability == "" || !origin.Valid() {
		return domain.DraftMaterial{}, inconsistent("material resolver returned an incomplete candidate")
	}
	reasons := append([]domain.SuggestionReasonCode(nil), candidate.Reasons...)
	if origin == domain.MaterialOriginUser {
		reasons = []domain.SuggestionReasonCode{domain.ReasonUserAdded}
	} else {
		filtered := reasons[:0]
		for _, reason := range reasons {
			if reason != domain.ReasonUserAdded {
				filtered = append(filtered, reason)
			}
		}
		reasons = filtered
		if len(reasons) == 0 {
			return domain.DraftMaterial{}, inconsistent("suggestion provider returned no system reason")
		}
	}
	reference, err := domain.CanonicalMaterialRef(candidate.Reference)
	if err != nil {
		return domain.DraftMaterial{}, err
	}
	title := strings.TrimSpace(candidate.Title)
	material := domain.DraftMaterial{ID: id, DraftID: draftID, Ref: reference, Title: title, Reasons: reasons,
		Origin: origin, Availability: candidate.Availability, Score: candidate.Score, Selected: origin == domain.MaterialOriginUser,
		Position: position, CreatedAt: createdAt}
	if err := material.Validate(); err != nil {
		return domain.DraftMaterial{}, inconsistent("material resolver returned an invalid candidate")
	}
	return material, nil
}

func validateFrozenMaterials(selected, frozen []domain.MaterialRef, policy domain.MaterialPolicy) error {
	if len(frozen) < policy.MinMaterials || len(frozen) > policy.MaxMaterials || len(frozen) > domain.MaxSnapshotMaterials {
		return invalid(domain.ErrorCodeSnapshotInvalid, "frozen material count violates the template policy")
	}
	allowed := make(map[domain.MaterialKind]struct{}, len(policy.AllowedKinds))
	for _, kind := range policy.AllowedKinds {
		allowed[kind] = struct{}{}
	}
	frozenByIdentity := make(map[string]domain.MaterialRef, len(frozen))
	selectedCollections := make(map[foundation.ID]struct{})
	selectedByIdentity := make(map[string]domain.MaterialRef, len(selected))
	for _, original := range selected {
		canonical, err := domain.CanonicalMaterialRef(original)
		if err != nil {
			return err
		}
		identity, _ := canonical.IdentityKey()
		selectedByIdentity[identity] = canonical
		if canonical.Kind == domain.MaterialSmartCollection {
			selectedCollections[canonical.CollectionID] = struct{}{}
		}
	}
	for _, reference := range frozen {
		canonical, err := domain.CanonicalMaterialRef(reference)
		if err != nil {
			return err
		}
		if _, ok := allowed[canonical.Kind]; !ok {
			return invalid(domain.ErrorCodeTemplateInvalid, "frozen material kind is not allowed by the template")
		}
		identity, _ := canonical.IdentityKey()
		if _, duplicate := frozenByIdentity[identity]; duplicate {
			return inconsistent("material resolver returned a duplicate frozen identity")
		}
		if canonical.OriginCollectionID != "" {
			if _, selectedOrigin := selectedCollections[canonical.OriginCollectionID]; !selectedOrigin {
				return inconsistent("material resolver returned a member outside the selected collection set")
			}
		} else if _, selectedDirectly := selectedByIdentity[identity]; !selectedDirectly {
			return inconsistent("material resolver returned an unselected material")
		}
		frozenByIdentity[identity] = canonical
	}
	for identity, canonical := range selectedByIdentity {
		resolved, ok := frozenByIdentity[identity]
		if !ok || !reflect.DeepEqual(canonical, resolved) {
			return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeMaterialStale, false, errors.New("selected material changed during confirmation"))
		}
	}
	return nil
}

func validateDraftCommandResult(result DraftResult, workspaceID foundation.ID, replayExpected bool, err error) (DraftResult, error) {
	if err != nil {
		return DraftResult{}, err
	}
	if result.Draft.WorkspaceID != workspaceID || result.Draft.Validate() != nil || result.Replayed != replayExpected {
		return DraftResult{}, inconsistent("draft repository returned an invalid command result")
	}
	return result, nil
}

func validateConfirmResult(result ConfirmResult, workspaceID foundation.ID, replayExpected bool, err error) (ConfirmResult, error) {
	if err != nil {
		return ConfirmResult{}, err
	}
	if result.Draft.WorkspaceID != workspaceID || result.Snapshot.WorkspaceID != workspaceID || result.Draft.ConfirmedSnapshotID != result.Snapshot.ID ||
		!validID(result.OutboxID) || result.Draft.Validate() != nil || result.Snapshot.Validate() != nil || result.Replayed != replayExpected {
		return ConfirmResult{}, inconsistent("draft repository returned an invalid confirmation result")
	}
	return result, nil
}

func validateTemplateResult(result TemplateResult, workspaceID foundation.ID, replayExpected bool, err error) (TemplateResult, error) {
	if err != nil {
		return TemplateResult{}, err
	}
	if result.Replayed != replayExpected {
		return TemplateResult{}, inconsistent("template repository returned an invalid replay disposition")
	}
	if err := validateTemplateDetail(result.Detail, workspaceID); err != nil {
		return TemplateResult{}, err
	}
	return result, nil
}

func validateTemplateDetail(detail TemplateDetail, workspaceID foundation.ID) error {
	if detail.Template.Validate() != nil || detail.Template.CurrentRevisionID != detail.Revision.ID || detail.Template.ID != detail.Revision.TemplateID ||
		detail.Template.WorkspaceID != detail.Revision.WorkspaceID || detail.Template.Kind != detail.Revision.Declaration.Kind {
		return inconsistent("template repository returned an invalid current revision binding")
	}
	owner := detail.Template.Owner
	if owner == domain.TemplateCustom && detail.Template.WorkspaceID != workspaceID {
		return inconsistent("template repository returned a cross-workspace custom template")
	}
	if detail.Revision.Validate(owner) != nil {
		return inconsistent("template repository returned an invalid revision")
	}
	return nil
}

func requestHash(operation string, workspaceID, aggregateID foundation.ID, expectedVersion int64, payload any) (string, error) {
	encoded, err := json.Marshal(struct {
		Schema          string        `json:"schema"`
		Operation       string        `json:"operation"`
		WorkspaceID     foundation.ID `json:"workspace_id"`
		AggregateID     foundation.ID `json:"aggregate_id,omitempty"`
		ExpectedVersion int64         `json:"expected_version"`
		Payload         any           `json:"payload"`
	}{"organizing-command/v1", operation, workspaceID, aggregateID, expectedVersion, payload})
	if err != nil || !utf8.Valid(encoded) {
		return "", inconsistent("organizing command cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
