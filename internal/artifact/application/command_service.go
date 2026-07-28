package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// NewCommandService constructs the durable Artifact command service.
func NewCommandService(dependencies Dependencies) (*CommandService, error) {
	if dependencies.Repository == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, unavailable("artifact repository, id generator, and clock are required")
	}
	return &CommandService{dependencies: dependencies}, nil
}

// NewQueryService constructs a read-only Artifact query service.
func NewQueryService(repository Repository) (*QueryService, error) {
	if repository == nil {
		return nil, unavailable("artifact repository is required")
	}
	return &QueryService{repository: repository}, nil
}

// Plan creates a new isolated Artifact and its initial immutable Revision.
func (service *CommandService) Plan(ctx context.Context, command PlanCommand) (CommandResult, error) {
	if err := service.available(ctx); err != nil {
		return CommandResult{}, err
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return CommandResult{}, err
	}
	if !validID(command.WorkspaceID) {
		return CommandResult{}, requestInvalid("artifact workspace is invalid")
	}
	visibilityHold := cloneVisibilityHold(command.VisibilityHold)
	if err := ValidateVisibilityHoldBinding(visibilityHold, command.Type, key); err != nil {
		return CommandResult{}, err
	}
	hash, err := requestHash(CommandPlan, command.WorkspaceID, "", 0, planRequest{
		Type: command.Type, Title: command.Title, ScopeDefinition: command.ScopeDefinition,
		VisibilityHold: visibilityHold,
	})
	if err != nil {
		return CommandResult{}, err
	}
	lookup := CommandBinding{WorkspaceID: command.WorkspaceID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandPlan}
	if replay, found, err := service.replay(ctx, lookup, 1); err != nil {
		return CommandResult{}, err
	} else if found {
		return replay, nil
	}
	artifactID, err := service.dependencies.IDs.New()
	if err != nil {
		return CommandResult{}, err
	}
	revisionID, err := service.dependencies.IDs.New()
	if err != nil {
		return CommandResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CommandResult{}, err
	}
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{
		ArtifactID: artifactID, InitialRevisionID: revisionID, WorkspaceID: command.WorkspaceID,
		Type: command.Type, Title: command.Title, ScopeDefinition: command.ScopeDefinition, CreatedAt: now,
	})
	if err != nil {
		return CommandResult{}, err
	}
	state := State{Artifact: artifact, Revision: revision}
	result, err := service.dependencies.Repository.Create(ctx, CreateRecord{
		Binding:        CommandBinding{WorkspaceID: command.WorkspaceID, ArtifactID: artifact.ID, IdempotencyKey: key, RequestHash: hash, CommandType: CommandPlan},
		State:          state,
		VisibilityHold: visibilityHold,
	})
	if err != nil {
		return CommandResult{}, err
	}
	if err := validateCommandResult(result, command.WorkspaceID, "", 1, hash, CommandPlan); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

// SubmitOutline creates a new immutable outline Revision and enters review.
func (service *CommandService) SubmitOutline(ctx context.Context, command SubmitOutlinePersistentCommand) (CommandResult, error) {
	binding, replay, err := service.prepareTransition(ctx, CommandSubmitOutline, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, command.IdempotencyKey, submitOutlineRequest{Outline: command.Outline})
	if err != nil {
		return CommandResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	current, err := service.current(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	nextID, now, err := service.newIDAndTime()
	if err != nil {
		return CommandResult{}, err
	}
	artifact, revision, err := domain.SubmitOutline(current.Artifact, current.Revision, nextID, command.Outline, now)
	if err != nil {
		return CommandResult{}, err
	}
	return service.transition(ctx, binding, current, State{Artifact: artifact, Revision: revision}, true, nil, nil)
}

// ApproveOutline freezes an approved outline Revision and enables sections.
func (service *CommandService) ApproveOutline(ctx context.Context, command RevisionPersistentCommand) (CommandResult, error) {
	binding, replay, err := service.prepareTransition(ctx, CommandApproveOutline, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, command.IdempotencyKey, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	current, err := service.current(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	nextID, now, err := service.newIDAndTime()
	if err != nil {
		return CommandResult{}, err
	}
	artifact, revision, err := domain.ApproveOutline(current.Artifact, current.Revision, nextID, now)
	if err != nil {
		return CommandResult{}, err
	}
	return service.transition(ctx, binding, current, State{Artifact: artifact, Revision: revision}, true, nil, nil)
}

// StartRevision returns an approved or exported Artifact to generation.
func (service *CommandService) StartRevision(ctx context.Context, command RevisionPersistentCommand) (CommandResult, error) {
	binding, replay, err := service.prepareTransition(ctx, CommandStartRevision, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, command.IdempotencyKey, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	current, err := service.current(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CommandResult{}, err
	}
	artifact, err := domain.StartRevision(current.Artifact, current.Revision, now)
	if err != nil {
		return CommandResult{}, err
	}
	return service.transition(ctx, binding, current, State{Artifact: artifact, Revision: domain.CloneRevision(current.Revision)}, false, nil, nil)
}

// RecordSection reconstructs citations server-side before adding an immutable revision.
func (service *CommandService) RecordSection(ctx context.Context, command RecordSectionPersistentCommand) (CommandResult, error) {
	payload := recordSectionInputRequest{Section: command.Section, Creator: command.Creator, Metadata: command.Metadata}
	binding, replay, err := service.prepareTransition(ctx, CommandRecordSection, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, command.IdempotencyKey, payload)
	if err != nil {
		return CommandResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	current, err := service.current(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	if command.Creator != domain.CreatorHuman && command.Creator != domain.CreatorAgent {
		return CommandResult{}, requestInvalid("artifact section creator is invalid")
	}
	section, err := service.verifySection(ctx, command.WorkspaceID, command.Section)
	if err != nil {
		return CommandResult{}, err
	}
	nextID, now, err := service.newIDAndTime()
	if err != nil {
		return CommandResult{}, err
	}
	artifact, revision, err := domain.RecordSection(current.Artifact, current.Revision, nextID, section, command.Creator, command.Metadata, now)
	if err != nil {
		return CommandResult{}, err
	}
	return service.transition(ctx, binding, current, State{Artifact: artifact, Revision: revision}, true, nil, nil)
}

// ApproveDraft approves a complete Artifact Draft without formal knowledge writes.
func (service *CommandService) ApproveDraft(ctx context.Context, command RevisionPersistentCommand) (CommandResult, error) {
	binding, replay, err := service.prepareTransition(ctx, CommandApproveDraft, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, command.IdempotencyKey, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	current, err := service.current(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CommandResult{}, err
	}
	artifact, err := domain.ApproveDraft(current.Artifact, current.Revision, now)
	if err != nil {
		return CommandResult{}, err
	}
	return service.transition(ctx, binding, current, State{Artifact: artifact, Revision: domain.CloneRevision(current.Revision)}, false, nil, nil)
}

// ExportMarkdown writes a managed snapshot then durably records its binding.
func (service *CommandService) ExportMarkdown(ctx context.Context, command RevisionPersistentCommand) (CommandResult, error) {
	binding, replay, err := service.prepareTransition(ctx, CommandExportMarkdown, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, command.IdempotencyKey, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	current, err := service.probeExternalTransition(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CommandResult{}, err
	}
	if _, err := domain.MarkExported(current.Artifact, current.Revision, now); err != nil {
		return CommandResult{}, err
	}
	if service.dependencies.Exporter == nil {
		return CommandResult{}, unavailable("artifact markdown exporter is unavailable")
	}
	current, err = service.reserveExternalTransition(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	artifact, err := domain.MarkExported(current.Artifact, current.Revision, now)
	if err != nil {
		return CommandResult{}, err
	}
	exportID, err := exportIDForRevision(current.Artifact.ID, current.Revision.ID)
	if err != nil {
		return CommandResult{}, err
	}
	next := State{Artifact: artifact, Revision: domain.CloneRevision(current.Revision)}
	managed, err := service.dependencies.Exporter.Export(ctx, ExportSnapshot{ExportID: exportID, State: next})
	if err != nil {
		return CommandResult{}, err
	}
	export, err := buildExportRecord(exportID, next, managed, now)
	if err != nil {
		return CommandResult{}, err
	}
	return service.transition(ctx, binding, current, next, false, &export, nil)
}

// Publish creates only an idempotent Change Control Proposal for the frozen revision.
func (service *CommandService) Publish(ctx context.Context, command RevisionPersistentCommand) (CommandResult, error) {
	binding, replay, err := service.prepareTransition(ctx, CommandPublish, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, command.IdempotencyKey, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	current, err := service.probeExternalTransition(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CommandResult{}, err
	}
	if _, _, err := domain.CreatePublicationRequest(current.Artifact, current.Revision, now); err != nil {
		return CommandResult{}, err
	}
	if service.dependencies.Publisher == nil {
		return CommandResult{}, unavailable("artifact publication creator is unavailable")
	}
	current, err = service.reserveExternalTransition(ctx, binding)
	if err != nil {
		return CommandResult{}, err
	}
	artifact, request, err := domain.CreatePublicationRequest(current.Artifact, current.Revision, now)
	if err != nil {
		return CommandResult{}, err
	}
	proposalID, err := service.dependencies.Publisher.CreateArtifactPublication(ctx, request, binding.IdempotencyKey)
	if err != nil {
		return CommandResult{}, err
	}
	if !validID(proposalID) {
		return CommandResult{}, resultInconsistent("publication creator returned an invalid proposal id")
	}
	publication := PublicationRecord{
		WorkspaceID: request.WorkspaceID, ArtifactID: request.ArtifactID, RevisionID: request.RevisionID,
		ArtifactVersion: request.ArtifactVersion, RevisionNo: request.RevisionNo, ContentHash: request.ContentHash,
		ProposalID: proposalID, IdempotencyKey: binding.IdempotencyKey, CreatedAt: now,
	}
	return service.transition(ctx, binding, current,
		State{Artifact: artifact, Revision: domain.CloneRevision(current.Revision)}, false, nil, &publication)
}

func (service *CommandService) transition(
	ctx context.Context,
	binding CommandBinding,
	current State,
	next State,
	newRevision bool,
	export *ExportRecord,
	publication *PublicationRecord,
) (CommandResult, error) {
	result, err := service.dependencies.Repository.Transition(ctx, TransitionRecord{
		Binding:           binding,
		CurrentRevisionID: current.Revision.ID,
		State:             next,
		NewRevision:       newRevision,
		Export:            export,
		Publication:       publication,
	})
	if err != nil {
		return CommandResult{}, err
	}
	if err := validateCommandResult(result, current.Artifact.WorkspaceID, current.Artifact.ID, binding.ExpectedVersion+1, binding.RequestHash, binding.CommandType); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func (service *CommandService) prepareTransition(
	ctx context.Context,
	commandType CommandType,
	workspaceID, artifactID foundation.ID,
	expectedVersion int64,
	idempotencyKey string,
	payload any,
) (CommandBinding, *CommandResult, error) {
	if err := service.available(ctx); err != nil {
		return CommandBinding{}, nil, err
	}
	key, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return CommandBinding{}, nil, err
	}
	if !validID(workspaceID) || !validID(artifactID) || expectedVersion < 1 {
		return CommandBinding{}, nil, requestInvalid("artifact transition identity or version is invalid")
	}
	hash, err := requestHash(commandType, workspaceID, artifactID, expectedVersion, payload)
	if err != nil {
		return CommandBinding{}, nil, err
	}
	binding := CommandBinding{
		WorkspaceID: workspaceID, ArtifactID: artifactID, IdempotencyKey: key,
		RequestHash: hash, CommandType: commandType, ExpectedVersion: expectedVersion,
	}
	if replay, found, err := service.replay(ctx, binding, expectedVersion+1); err != nil {
		return CommandBinding{}, nil, err
	} else if found {
		return binding, &replay, nil
	}
	return binding, nil, nil
}

func (service *CommandService) replay(ctx context.Context, binding CommandBinding, version int64) (CommandResult, bool, error) {
	result, found, err := service.dependencies.Repository.FindCommand(ctx, binding)
	if err != nil || !found {
		return CommandResult{}, found, err
	}
	if !result.Replayed {
		return CommandResult{}, false, resultInconsistent("artifact command lookup did not mark a replay")
	}
	if err := validateCommandResult(result, binding.WorkspaceID, binding.ArtifactID, version, binding.RequestHash, binding.CommandType); err != nil {
		return CommandResult{}, false, err
	}
	return result, true, nil
}

func (service *CommandService) current(ctx context.Context, binding CommandBinding) (State, error) {
	state, err := service.dependencies.Repository.GetCommandState(ctx, binding.WorkspaceID, binding.ArtifactID)
	if err != nil {
		return State{}, err
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	if state.Artifact.WorkspaceID != binding.WorkspaceID || state.Artifact.ID != binding.ArtifactID {
		return State{}, resultInconsistent("artifact repository returned a cross-workspace state")
	}
	if state.Artifact.Version != binding.ExpectedVersion {
		return State{}, versionConflict("artifact expected version is stale")
	}
	return state, nil
}

func (service *CommandService) reserveExternalTransition(ctx context.Context, binding CommandBinding) (State, error) {
	state, err := service.dependencies.Repository.ReserveExternalTransition(ctx, binding)
	if err != nil {
		return State{}, err
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	if state.Artifact.WorkspaceID != binding.WorkspaceID || state.Artifact.ID != binding.ArtifactID || state.Artifact.Version != binding.ExpectedVersion {
		return State{}, resultInconsistent("artifact external transition reservation returned an invalid state")
	}
	return state, nil
}

func (service *CommandService) probeExternalTransition(ctx context.Context, binding CommandBinding) (State, error) {
	state, err := service.dependencies.Repository.ProbeExternalTransition(ctx, binding)
	if err != nil {
		return State{}, err
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	if state.Artifact.WorkspaceID != binding.WorkspaceID || state.Artifact.ID != binding.ArtifactID || state.Artifact.Version != binding.ExpectedVersion {
		return State{}, resultInconsistent("artifact external transition probe returned an invalid state")
	}
	return state, nil
}

func (service *CommandService) verifySection(ctx context.Context, workspaceID foundation.ID, input SectionInput) (domain.Section, error) {
	if input.Citations == nil {
		return domain.Section{}, evidenceInvalid("section citation identities must be explicit")
	}
	var citations []domain.Citation
	if len(input.Citations) > 0 {
		if service.dependencies.Evidence == nil {
			return domain.Section{}, unavailable("artifact citation verifier is unavailable")
		}
		verified, err := service.dependencies.Evidence.VerifyCitations(ctx, workspaceID, input.Citations)
		if err != nil {
			return domain.Section{}, err
		}
		if len(verified) != len(input.Citations) {
			return domain.Section{}, resultInconsistent("citation verifier returned an incomplete result")
		}
		citations = verified
	} else {
		citations = []domain.Citation{}
	}
	return domain.Section{Key: input.Key, Title: input.Title, Content: input.Content, Citations: citations, Coverage: input.Coverage}, nil
}

func (service *CommandService) available(ctx context.Context) error {
	if service == nil || service.dependencies.Repository == nil || service.dependencies.IDs == nil || service.dependencies.Clock == nil {
		return unavailable("artifact command service is unavailable")
	}
	if ctx == nil {
		return requestInvalid("context is nil")
	}
	return nil
}

func (service *CommandService) now() (time.Time, error) {
	now := service.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return time.Time{}, resultInconsistent("artifact clock returned zero time")
	}
	return now, nil
}

func (service *CommandService) newIDAndTime() (foundation.ID, time.Time, error) {
	id, err := service.dependencies.IDs.New()
	if err != nil {
		return "", time.Time{}, err
	}
	now, err := service.now()
	return id, now, err
}

// Get returns the current Artifact revision scoped to one Workspace.
func (service *QueryService) Get(ctx context.Context, workspaceID, artifactID foundation.ID) (State, error) {
	if service == nil || service.repository == nil {
		return State{}, unavailable("artifact query service is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || !validID(artifactID) {
		return State{}, requestInvalid("artifact lookup is invalid")
	}
	state, err := service.repository.Get(ctx, workspaceID, artifactID)
	if err != nil {
		return State{}, err
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	if state.Artifact.WorkspaceID != workspaceID || state.Artifact.ID != artifactID {
		return State{}, resultInconsistent("artifact repository returned a cross-workspace state")
	}
	return state, nil
}

// List returns a bounded stable Artifact page.
func (service *QueryService) List(ctx context.Context, query ListQuery) (ArtifactPage, error) {
	if service == nil || service.repository == nil {
		return ArtifactPage{}, unavailable("artifact query service is unavailable")
	}
	if ctx == nil || !validID(query.WorkspaceID) {
		return ArtifactPage{}, requestInvalid("artifact list workspace is invalid")
	}
	if query.Limit == 0 {
		query.Limit = defaultListLimit
	}
	if query.Limit < 1 || query.Limit > maxListLimit {
		return ArtifactPage{}, requestInvalid("artifact list limit is invalid")
	}
	if query.After != nil && (query.After.UpdatedAt.IsZero() || !validID(query.After.ID)) {
		return ArtifactPage{}, requestInvalid("artifact list cursor is invalid")
	}
	page, err := service.repository.List(ctx, query)
	if err != nil {
		return ArtifactPage{}, err
	}
	if len(page.Items) > query.Limit {
		return ArtifactPage{}, resultInconsistent("artifact list exceeds requested limit")
	}
	for index, state := range page.Items {
		if err := validateState(state); err != nil || state.Artifact.WorkspaceID != query.WorkspaceID {
			return ArtifactPage{}, resultInconsistent("artifact list contains an invalid state")
		}
		if index > 0 && !artifactOrderBefore(page.Items[index-1].Artifact, state.Artifact) {
			return ArtifactPage{}, resultInconsistent("artifact list order is invalid")
		}
	}
	if page.Next != nil {
		if len(page.Items) == 0 || page.Next.UpdatedAt.IsZero() || !validID(page.Next.ID) {
			return ArtifactPage{}, resultInconsistent("artifact list next cursor is invalid")
		}
		last := page.Items[len(page.Items)-1].Artifact
		if page.Next.UpdatedAt.UTC() != last.UpdatedAt.UTC() || page.Next.ID != last.ID {
			return ArtifactPage{}, resultInconsistent("artifact list next cursor is not bound to the last item")
		}
	}
	return page, nil
}

// GetExport returns a managed Markdown output record scoped to its Artifact.
func (service *QueryService) GetExport(ctx context.Context, workspaceID, artifactID, exportID foundation.ID) (ExportRecord, error) {
	if service == nil || service.repository == nil {
		return ExportRecord{}, unavailable("artifact query service is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || !validID(artifactID) || !validID(exportID) {
		return ExportRecord{}, requestInvalid("artifact export lookup is invalid")
	}
	record, err := service.repository.GetExport(ctx, workspaceID, artifactID, exportID)
	if err != nil {
		return ExportRecord{}, err
	}
	if err := validateExportRecord(record); err != nil || record.WorkspaceID != workspaceID || record.ArtifactID != artifactID || record.ID != exportID {
		return ExportRecord{}, resultInconsistent("artifact export repository result is invalid")
	}
	return record, nil
}

func validateCommandResult(result CommandResult, workspaceID, artifactID foundation.ID, version int64, hash string, commandType CommandType) error {
	if err := validateState(result.State); err != nil {
		return err
	}
	if result.State.Artifact.WorkspaceID != workspaceID || (artifactID != "" && result.State.Artifact.ID != artifactID) ||
		result.CommandVersion != version || result.State.Artifact.Version != version || result.RequestHash != hash || result.CommandType != commandType {
		return resultInconsistent("artifact command result is not request-bound")
	}
	if result.Export != nil && validateExportRecord(*result.Export) != nil {
		return resultInconsistent("artifact command result export is invalid")
	}
	if result.Publication != nil && validatePublicationRecord(*result.Publication) != nil {
		return resultInconsistent("artifact command result publication is invalid")
	}
	return nil
}

func validateState(state State) error {
	if err := domain.ValidateArtifact(state.Artifact); err != nil {
		return err
	}
	if err := domain.ValidateRevision(state.Revision); err != nil {
		return err
	}
	if state.Artifact.ID != state.Revision.ArtifactID || state.Artifact.CurrentRevisionID != state.Revision.ID {
		return resultInconsistent("artifact current revision binding is invalid")
	}
	return nil
}

// ValidateState verifies a persisted current Artifact/Revision pair.
func ValidateState(state State) error { return validateState(state) }

func buildExportRecord(id foundation.ID, state State, managed ManagedExport, at time.Time) (ExportRecord, error) {
	record := ExportRecord{
		ID: id, WorkspaceID: state.Artifact.WorkspaceID, ArtifactID: state.Artifact.ID, RevisionID: state.Revision.ID,
		ArtifactVersion: state.Artifact.Version, RevisionNo: state.Revision.RevisionNo, RevisionHash: state.Revision.ContentHash,
		OutputPath: managed.OutputPath, OutputHash: managed.OutputHash, OutputSize: managed.OutputSize, ExportedAt: at.UTC(),
	}
	if err := validateExportRecord(record); err != nil {
		return ExportRecord{}, err
	}
	return record, nil
}

func validateExportRecord(record ExportRecord) error {
	if !validID(record.ID) || !validID(record.WorkspaceID) || !validID(record.ArtifactID) || !validID(record.RevisionID) ||
		record.ArtifactVersion < 1 || record.RevisionNo < 1 || !validHash(record.RevisionHash) || !validHash(record.OutputHash) ||
		record.OutputSize < 0 || record.ExportedAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeExportInvalid, false, errors.New("artifact export record is invalid"))
	}
	wantPath := fmt.Sprintf(".knowledge/exports/artifacts/%s/%s.md", record.ArtifactID, record.ID)
	if record.OutputPath != wantPath {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeExportInvalid, false, errors.New("artifact export path is not managed"))
	}
	return nil
}

// ValidateExportRecord verifies a managed export binding before persistence.
func ValidateExportRecord(record ExportRecord) error { return validateExportRecord(record) }

func validatePublicationRecord(record PublicationRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.ArtifactID) || !validID(record.RevisionID) || !validID(record.ProposalID) ||
		record.ArtifactVersion < 1 || record.RevisionNo < 1 || !validHash(record.ContentHash) || record.CreatedAt.IsZero() {
		return resultInconsistent("artifact publication record is invalid")
	}
	if _, err := normalizeIdempotencyKey(record.IdempotencyKey); err != nil {
		return resultInconsistent("artifact publication idempotency key is invalid")
	}
	return nil
}

// ValidatePublicationRecord verifies an immutable Proposal binding.
func ValidatePublicationRecord(record PublicationRecord) error {
	return validatePublicationRecord(record)
}

func normalizeIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxIdempotencyKeyBytes || strings.ContainsAny(value, "\r\n") {
		return "", requestInvalid("artifact idempotency key is invalid")
	}
	return value, nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ValidateVisibilityHold validates the bounded workflows allowed to hide an
// Artifact before its owning transaction commits. A nil hold is ordinary.
func ValidateVisibilityHold(hold *VisibilityHold) error {
	if hold == nil {
		return nil
	}
	validRole := hold.OwnerType == VisibilityHoldOwnerInterviewComplete &&
		(hold.OwnerRole == VisibilityHoldRoleReport || hold.OwnerRole == VisibilityHoldRolePath)
	validRole = validRole || hold.OwnerType == VisibilityHoldOwnerLearningPathCreate && hold.OwnerRole == VisibilityHoldRolePath
	if !validID(hold.OwnerID) || !validRole || !validHash(hold.AttemptDigest) {
		return requestInvalid("artifact visibility hold is invalid")
	}
	return nil
}

// ValidateVisibilityHoldBinding 把 hold 绑定到固定 Artifact 类型、attempt
// digest 和 PLAN stage key；普通无 hold 的 Artifact 不受影响。
func ValidateVisibilityHoldBinding(hold *VisibilityHold, artifactType, idempotencyKey string) error {
	if err := ValidateVisibilityHold(hold); err != nil || hold == nil {
		return err
	}
	wantType := "LEARNING_PATH"
	wantKey := fmt.Sprintf("lp1:review:%s:%s:%s:p", hold.OwnerID, wantType, hold.AttemptDigest)
	if hold.OwnerType == VisibilityHoldOwnerInterviewComplete {
		wantType = "INTERVIEW_DOC"
		if hold.OwnerRole == VisibilityHoldRolePath {
			wantType = "LEARNING_PATH"
		}
		wantKey = fmt.Sprintf("iv1:%s:%s:%s:p", hold.OwnerID, wantType, hold.AttemptDigest)
	}
	if artifactType != wantType || idempotencyKey != wantKey {
		return requestInvalid("artifact visibility hold binding is invalid")
	}
	return nil
}

func cloneVisibilityHold(hold *VisibilityHold) *VisibilityHold {
	if hold == nil {
		return nil
	}
	copyValue := *hold
	return &copyValue
}

// exportIDForRevision derives one managed export identity for an immutable revision.
// Replays and concurrent requests therefore write the same controlled path instead
// of leaving a second untracked file before a losing CAS can return its conflict.
func exportIDForRevision(artifactID, revisionID foundation.ID) (foundation.ID, error) {
	if !validID(artifactID) || !validID(revisionID) || artifactID == revisionID {
		return "", requestInvalid("artifact export binding is invalid")
	}
	digest := sha256.Sum256([]byte("artifact-export/v1\x00" + string(artifactID) + "\x00" + string(revisionID)))
	value := digest[:16]
	value[6] = value[6]&0x0f | 0x50
	value[8] = value[8]&0x3f | 0x80
	hexValue := hex.EncodeToString(value)
	parsed, err := foundation.ParseID(fmt.Sprintf("%s-%s-%s-%s-%s", hexValue[:8], hexValue[8:12], hexValue[12:16], hexValue[16:20], hexValue[20:]))
	if err != nil {
		return "", resultInconsistent("artifact export identity could not be derived")
	}
	return parsed, nil
}

func requestHash(commandType CommandType, workspaceID, artifactID foundation.ID, expectedVersion int64, payload any) (string, error) {
	encoded, err := json.Marshal(struct {
		SchemaVersion   string        `json:"schema_version"`
		CommandType     CommandType   `json:"command_type"`
		WorkspaceID     foundation.ID `json:"workspace_id"`
		ArtifactID      foundation.ID `json:"artifact_id,omitempty"`
		ExpectedVersion int64         `json:"expected_version,omitempty"`
		Payload         any           `json:"payload,omitempty"`
	}{
		SchemaVersion: "artifact-command/v1", CommandType: commandType, WorkspaceID: workspaceID,
		ArtifactID: artifactID, ExpectedVersion: expectedVersion, Payload: payload,
	})
	if err != nil {
		return "", requestInvalid("artifact request cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ComputeRequestHash exposes the stable Artifact receipt hash for adapters and tests.
func ComputeRequestHash(commandType CommandType, workspaceID, artifactID foundation.ID, expectedVersion int64, payload any) (string, error) {
	return requestHash(commandType, workspaceID, artifactID, expectedVersion, payload)
}

func artifactOrderBefore(left, right domain.Artifact) bool {
	leftTime, rightTime := left.UpdatedAt.UTC(), right.UpdatedAt.UTC()
	return leftTime.After(rightTime) || (leftTime.Equal(rightTime) && left.ID > right.ID)
}

type planRequest struct {
	Type            string          `json:"type"`
	Title           string          `json:"title"`
	ScopeDefinition string          `json:"scope_definition"`
	VisibilityHold  *VisibilityHold `json:"visibility_hold,omitempty"`
}

// planVisibilityHoldHashRequest 保留 00057 前的 PLAN request hash 形状。
// AttemptDigest 已由精确 stage key 与数据库 hold fence 绑定，不能重复加入
// hash 使迁移前的可恢复 PLAN receipt 失效。
type planVisibilityHoldHashRequest struct {
	OwnerType VisibilityHoldOwnerType `json:"owner_type"`
	OwnerID   foundation.ID           `json:"owner_id"`
	OwnerRole VisibilityHoldOwnerRole `json:"owner_role"`
}

func (request planRequest) MarshalJSON() ([]byte, error) {
	var visibilityHold *planVisibilityHoldHashRequest
	if request.VisibilityHold != nil {
		visibilityHold = &planVisibilityHoldHashRequest{
			OwnerType: request.VisibilityHold.OwnerType,
			OwnerID:   request.VisibilityHold.OwnerID,
			OwnerRole: request.VisibilityHold.OwnerRole,
		}
	}
	return json.Marshal(struct {
		Type            string                         `json:"type"`
		Title           string                         `json:"title"`
		ScopeDefinition string                         `json:"scope_definition"`
		VisibilityHold  *planVisibilityHoldHashRequest `json:"visibility_hold,omitempty"`
	}{
		Type:            request.Type,
		Title:           request.Title,
		ScopeDefinition: request.ScopeDefinition,
		VisibilityHold:  visibilityHold,
	})
}

type submitOutlineRequest struct {
	Outline []domain.OutlineSection `json:"outline"`
}

type recordSectionInputRequest struct {
	Section  SectionInput               `json:"section"`
	Creator  domain.CreatorType         `json:"creator"`
	Metadata *domain.GenerationMetadata `json:"metadata,omitempty"`
}
