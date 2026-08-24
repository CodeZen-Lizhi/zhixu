package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

// GORMSectionGenerationRepository coordinates Artifact, Workflow, and Agent in
// one platform-owned transaction scope.
type GORMSectionGenerationRepository struct {
	database *gorm.DB
	uow      foundation.UnitOfWork
	runtime  workflowapplication.ScopedRuntimeStarter
	binding  workflowapplication.ScopedRuntimeBindingReader
	agent    agentapplication.ScopedModelRunStore
	evidence artifactapplication.CitationVerifier
	ids      foundation.IDGenerator
	clock    foundation.Clock
	profile  agentdomain.ModelProfileRef
}

// NewGORMSectionGenerationRepository constructs the staged scoped generation
// coordinator. All database-owning dependencies must derive from the same Pool.
func NewGORMSectionGenerationRepository(
	pool *platformpostgres.Pool,
	runtime workflowapplication.ScopedRuntimeStarter,
	binding workflowapplication.ScopedRuntimeBindingReader,
	agent agentapplication.ScopedModelRunStore,
	evidence artifactapplication.CitationVerifier,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	profile agentdomain.ModelProfileRef,
) (*GORMSectionGenerationRepository, error) {
	if pool == nil || nilGenerationDependency(runtime) || nilGenerationDependency(binding) || nilGenerationDependency(agent) ||
		nilGenerationDependency(evidence) || nilGenerationDependency(ids) || nilGenerationDependency(clock) || profile.Validate() != nil {
		return nil, unavailable(errors.New("artifact section generation dependencies are incomplete"))
	}
	database, err := pool.GORM()
	if err != nil || !validGORMArtifactDatabase(database) {
		return nil, unavailable(errors.New("artifact section generation database is unavailable"))
	}
	uow, err := pool.UnitOfWork()
	if err != nil || nilGenerationDependency(uow) {
		return nil, unavailable(errors.New("artifact section generation unit of work is unavailable"))
	}
	return &GORMSectionGenerationRepository{
		database: database, uow: uow, runtime: runtime, binding: binding, agent: agent,
		evidence: evidence, ids: ids, clock: clock, profile: profile,
	}, nil
}

func (repository *GORMSectionGenerationRepository) available() bool {
	return repository != nil && validGORMArtifactDatabase(repository.database) &&
		!nilGenerationDependency(repository.uow) && !nilGenerationDependency(repository.runtime) &&
		!nilGenerationDependency(repository.binding) && !nilGenerationDependency(repository.agent) &&
		!nilGenerationDependency(repository.evidence) && !nilGenerationDependency(repository.ids) &&
		!nilGenerationDependency(repository.clock) && repository.profile.Validate() == nil
}

func (repository *GORMSectionGenerationRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	if !repository.available() {
		return generationCapabilityError(errors.New("artifact section generation repository is unavailable"))
	}
	if ctx == nil {
		return generationInputError(errors.New("artifact generation context is nil"))
	}
	return repository.uow.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return generationCapabilityError(err)
		}
		return work(callbackCtx, scope, tx.WithContext(callbackCtx))
	})
}

// StartSectionGeneration atomically freezes an approved section, starts its
// Workflow, and records the generation binding.
func (repository *GORMSectionGenerationRepository) StartSectionGeneration(
	ctx context.Context,
	command artifactapplication.StartSectionGenerationCommand,
) (artifactapplication.StartSectionGenerationResult, error) {
	if !repository.available() {
		return artifactapplication.StartSectionGenerationResult{}, unavailable(errors.New("artifact section generation repository is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := artifactapplication.ValidateStartSectionGenerationCommand(command); err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	requestHash, err := artifactapplication.ComputeSectionGenerationRequestHash(command)
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	var result artifactapplication.StartSectionGenerationResult
	callbackSucceeded := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := gormLockCommand(callbackCtx, tx, command.WorkspaceID, command.IdempotencyKey); err != nil {
			return classifyGORM(callbackCtx, err, "ARTIFACT_GENERATION_START_FAILED")
		}
		existing, found, err := gormLoadGenerationByKey(callbackCtx, tx, command.WorkspaceID, command.IdempotencyKey, true)
		if err != nil {
			return err
		}
		if found {
			if !sameGenerationStartRequest(existing, command, requestHash) {
				return idempotencyConflict(errors.New("artifact generation idempotency key is bound to another request"))
			}
			encodedInput, err := gormGenerationWorkflowInput(existing)
			if err != nil {
				return err
			}
			if err := repository.validateWorkflowBindingScoped(callbackCtx, scope, existing, "", encodedInput); err != nil {
				return err
			}
			result = artifactapplication.StartSectionGenerationResult{Generation: existing, Replayed: true}
			callbackSucceeded = true
			return nil
		}
		current, err := gormLoadState(callbackCtx, tx, command.WorkspaceID, command.ArtifactID, true, false)
		if gormNoRows(err) {
			return notFound(err)
		}
		if err != nil {
			return err
		}
		if _, err := artifactapplication.ValidateSectionGenerationSource(current, command.ExpectedVersion, command.SectionKey); err != nil {
			return err
		}
		if _, found, err := gormLoadActiveGeneration(callbackCtx, tx, command.WorkspaceID, command.ArtifactID, command.SectionKey); err != nil {
			return err
		} else if found {
			return idempotencyConflict(errors.New("artifact section already has an active generation workflow"))
		}
		input := artifactworkflow.Input{
			SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: current.Artifact.ID,
			RevisionID: current.Revision.ID, RevisionNo: current.Revision.RevisionNo,
			ArtifactVersion: current.Artifact.Version, SectionKey: command.SectionKey,
		}
		encodedInput, err := artifactworkflow.EncodeInput(input)
		if err != nil {
			return generationContextError(foundation.ErrorConsistencyViolation, err)
		}
		workflowRequest, err := workflowapplication.BuildRuntimeStartRequest(
			repository.ids, repository.clock, command.WorkspaceID,
			sectionGenerationWorkflowKey(command.WorkspaceID, command.IdempotencyKey), encodedInput,
			artifactworkflow.RegisteredDefinition(),
		)
		if err != nil {
			return err
		}
		generationID, err := repository.ids.New()
		if err != nil {
			return err
		}
		started, err := repository.runtime.StartScoped(callbackCtx, scope, workflowRequest)
		if err != nil {
			return err
		}
		if started.Replayed || !sameStartedWorkflow(started, command.WorkspaceID, encodedInput) {
			return inconsistent(errors.New("artifact generation resolved to an unexpected workflow start"))
		}
		generation := artifactapplication.SectionGeneration{
			ID: generationID, WorkspaceID: command.WorkspaceID, ArtifactID: command.ArtifactID,
			SourceRevisionID: current.Revision.ID, SourceRevisionNo: current.Revision.RevisionNo,
			SourceArtifactVersion: current.Artifact.Version, SectionKey: command.SectionKey,
			IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash,
			WorkflowRunID: started.Run.ID, NodeRunID: started.FirstNode.ID,
			Status: artifactapplication.SectionGenerationPending, Version: 1,
			CreatedAt: started.Run.CreatedAt.UTC(), UpdatedAt: started.Run.CreatedAt.UTC(),
		}
		if err := artifactapplication.ValidateSectionGeneration(generation); err != nil {
			return inconsistent(fmt.Errorf("artifact generation candidate is invalid: %w", err))
		}
		if err := gormInsertSectionGeneration(callbackCtx, tx, generation); err != nil {
			return err
		}
		result = artifactapplication.StartSectionGenerationResult{Generation: generation}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		recovered, found, recoveryErr := repository.recoverCommittedGeneration(ctx, command, requestHash)
		if recoveryErr != nil {
			return artifactapplication.StartSectionGenerationResult{}, errors.Join(classifyGORM(ctx, err, "ARTIFACT_GENERATION_START_COMMIT_FAILED"), recoveryErr)
		}
		if found {
			return artifactapplication.StartSectionGenerationResult{Generation: recovered, Replayed: result.Replayed}, nil
		}
	}
	return artifactapplication.StartSectionGenerationResult{}, classifyGORM(ctx, err, "ARTIFACT_GENERATION_START_FAILED")
}

// LoadGenerationContext reads frozen and current Artifact state in one
// repeatable-read, read-only snapshot.
func (repository *GORMSectionGenerationRepository) LoadGenerationContext(
	ctx context.Context,
	query artifactworkflow.GenerationContextQuery,
) (artifactworkflow.GenerationContext, error) {
	if !repository.available() {
		return artifactworkflow.GenerationContext{}, generationCapabilityError(errors.New("artifact section generation repository is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	encodedInput, err := validateGenerationContextQuery(query)
	if err != nil {
		return artifactworkflow.GenerationContext{}, err
	}
	var result artifactworkflow.GenerationContext
	err = repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		generation, found, err := gormLoadGenerationByWorkflow(callbackCtx, tx, query.WorkspaceID, query.WorkflowRunID, query.NodeRunID, false)
		if err != nil {
			return err
		}
		if !found {
			return generationContextError(foundation.ErrorNotFound, errors.New("artifact generation workflow binding was not found"))
		}
		if generation.Status != artifactapplication.SectionGenerationPending || !generationMatchesInput(generation, query.Input) {
			return generationContextError(foundation.ErrorVersionConflict, errors.New("artifact generation is not pending for the frozen input"))
		}
		if err := repository.validateWorkflowBindingScoped(callbackCtx, scope, generation, "", encodedInput); err != nil {
			return err
		}
		current, err := gormLoadState(callbackCtx, tx, query.WorkspaceID, generation.ArtifactID, false, false)
		if gormNoRows(err) {
			return generationContextError(foundation.ErrorNotFound, err)
		}
		if err != nil {
			return err
		}
		source, err := gormLoadRevision(callbackCtx, tx, query.WorkspaceID, generation.ArtifactID, generation.SourceRevisionID)
		if gormNoRows(err) {
			return generationContextError(foundation.ErrorConsistencyViolation, err)
		}
		if err != nil {
			return err
		}
		if _, err := artifactapplication.ValidateSectionGenerationRebase(source, current, generation.SectionKey); err != nil {
			return generationContextError(foundation.ErrorVersionConflict, err)
		}
		result = artifactworkflow.GenerationContext{
			Current:        artifactapplication.State{Artifact: artifactdomain.CloneArtifact(current.Artifact), Revision: artifactdomain.CloneRevision(current.Revision)},
			SourceRevision: artifactdomain.CloneRevision(source), ProfileRef: repository.profile,
		}
		return nil
	})
	if err != nil {
		return artifactworkflow.GenerationContext{}, generationCapabilityOrExisting(ctx, err)
	}
	return result, nil
}

// Lookup recovers a committed terminal receipt before provider execution.
func (repository *GORMSectionGenerationRepository) Lookup(
	ctx context.Context,
	lookup artifactworkflow.FinalizationLookup,
) (artifactworkflow.OutputReceipt, bool, error) {
	if !repository.available() {
		return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(errors.New("artifact section generation repository is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	encodedInput, err := validateFinalizationLookup(lookup)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	var receipt artifactworkflow.OutputReceipt
	var replayed bool
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		generation, found, err := gormLoadGenerationByWorkflow(callbackCtx, tx, lookup.WorkspaceID, lookup.WorkflowRunID, lookup.NodeRunID, false)
		if err != nil {
			return err
		}
		if !found || !generationMatchesInput(generation, lookup.Input) {
			return generationContextError(foundation.ErrorNotFound, errors.New("artifact generation finalization binding was not found"))
		}
		if err := repository.validateWorkflowBindingScoped(callbackCtx, scope, generation, lookup.NodeAttemptID, encodedInput); err != nil {
			return err
		}
		switch generation.Status {
		case artifactapplication.SectionGenerationPending:
			run, found, err := repository.agent.GetModelRunByAttemptScoped(callbackCtx, scope, lookup.WorkspaceID, lookup.NodeAttemptID, false)
			if err != nil {
				return err
			}
			if found && (run.WorkflowRunID != lookup.WorkflowRunID || run.NodeRunID != lookup.NodeRunID || run.NodeAttemptID != lookup.NodeAttemptID || run.Status != agentdomain.ModelRunRunning) {
				return generationOutputError(foundation.ErrorConsistencyViolation, errors.New("pending artifact generation has a split model run binding"))
			}
			return nil
		case artifactapplication.SectionGenerationRecoveryRequired:
			return generationFinalizationUnknown(errors.New("artifact generation requires explicit recovery"))
		case artifactapplication.SectionGenerationFailed, artifactapplication.SectionGenerationCancelled:
			return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation is terminal without an output receipt"))
		case artifactapplication.SectionGenerationCompleted:
		default:
			return generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation has an unsupported status"))
		}
		if generation.ModelRunID == nil {
			return generationOutputError(foundation.ErrorConsistencyViolation, errors.New("completed artifact generation has no model run"))
		}
		record, err := repository.agent.GetModelRunRecordScoped(callbackCtx, scope, lookup.WorkspaceID, *generation.ModelRunID, false)
		if err != nil {
			return err
		}
		if _, err := repository.validateModelRunRecord(record, generation, "", true); err != nil {
			return err
		}
		receipt, _, err = gormLoadCompletedGenerationReceipt(callbackCtx, tx, generation, lookup.Input)
		if err == nil {
			replayed = true
		}
		return err
	})
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationCapabilityOrExisting(ctx, err)
	}
	return receipt, replayed, nil
}

// Finalize atomically writes the Artifact revision, generation receipt, and
// Agent model-run terminal state.
func (repository *GORMSectionGenerationRepository) Finalize(
	ctx context.Context,
	command artifactworkflow.FinalizeSectionCommand,
) (artifactworkflow.OutputReceipt, bool, error) {
	if !repository.available() {
		return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(errors.New("artifact section generation repository is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	encodedInput, err := validateFinalizeSectionCommand(command)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	preverifiedSection, preverified, err := repository.preverifySectionProposal(ctx, command, encodedInput)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	var receipt artifactworkflow.OutputReceipt
	var replayed bool
	callbackSucceeded := false
	newCompletion := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		generation, found, err := gormLoadGenerationByWorkflow(callbackCtx, tx, command.WorkspaceID, command.WorkflowRunID, command.NodeRunID, true)
		if err != nil {
			return err
		}
		if !found || !generationMatchesInput(generation, command.Input) {
			return generationContextError(foundation.ErrorNotFound, errors.New("artifact generation finalization binding was not found"))
		}
		if err := repository.validateWorkflowBindingScoped(callbackCtx, scope, generation, command.NodeAttemptID, encodedInput); err != nil {
			return err
		}
		switch generation.Status {
		case artifactapplication.SectionGenerationRecoveryRequired:
			return generationFinalizationUnknown(errors.New("artifact generation requires explicit recovery"))
		case artifactapplication.SectionGenerationFailed, artifactapplication.SectionGenerationCancelled:
			return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation is already terminal without an output receipt"))
		case artifactapplication.SectionGenerationPending, artifactapplication.SectionGenerationCompleted:
		default:
			return generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation has an unsupported status"))
		}
		current, err := gormLoadState(callbackCtx, tx, command.WorkspaceID, generation.ArtifactID, true, false)
		if gormNoRows(err) {
			return generationContextError(foundation.ErrorConsistencyViolation, err)
		}
		if err != nil {
			return err
		}
		runID := command.ModelRunID
		if generation.Status == artifactapplication.SectionGenerationCompleted {
			if generation.ModelRunID == nil {
				return generationOutputError(foundation.ErrorConsistencyViolation, errors.New("completed artifact generation has no model run"))
			}
			runID = *generation.ModelRunID
		}
		record, err := repository.agent.GetModelRunRecordScoped(callbackCtx, scope, command.WorkspaceID, runID, true)
		if err != nil {
			return err
		}
		successfulCall, err := repository.validateModelRunRecord(record, generation, command.NodeAttemptID, generation.Status == artifactapplication.SectionGenerationCompleted)
		if err != nil {
			return err
		}
		if generation.Status == artifactapplication.SectionGenerationCompleted {
			if command.ModelRunID != runID {
				return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation already completed with another model run"))
			}
			var recorded artifactdomain.Revision
			receipt, recorded, err = gormLoadCompletedGenerationReceipt(callbackCtx, tx, generation, command.Input)
			if err != nil {
				return err
			}
			persistedSection, found := revisionSectionByKey(recorded, generation.SectionKey)
			if !found || validateCompletedSectionProposal(command.WorkspaceID, record.Run, successfulCall, command.Proposal, persistedSection) != nil ||
				recorded.CreatedBy != artifactdomain.CreatorAgent || recorded.Metadata == nil || !reflect.DeepEqual(*recorded.Metadata, command.Proposal.Metadata) {
				return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation terminal proposal differs"))
			}
			replayed = true
			callbackSucceeded = true
			return nil
		}
		if !preverified {
			return generationOutputError(foundation.ErrorConsistencyViolation, errors.New("pending artifact generation has no preverified section"))
		}
		if command.ModelRunID != record.Run.ID || command.ExpectedModelRunVersion != record.Run.Version || record.Run.Status != agentdomain.ModelRunRunning || record.Run.NodeAttemptID != command.NodeAttemptID {
			return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation model run version or attempt is stale"))
		}
		source, err := gormLoadRevision(callbackCtx, tx, command.WorkspaceID, generation.ArtifactID, generation.SourceRevisionID)
		if gormNoRows(err) {
			return generationContextError(foundation.ErrorConsistencyViolation, err)
		}
		if err != nil {
			return err
		}
		target, err := artifactapplication.ValidateSectionGenerationRebase(source, current, generation.SectionKey)
		if err != nil {
			return generationContextError(foundation.ErrorVersionConflict, err)
		}
		if command.Proposal.SectionKey != target.Key || command.Proposal.Title != target.Title {
			return generationOutputError(foundation.ErrorInvalidInput, errors.New("artifact section proposal changed the frozen section identity"))
		}
		section, err := sectionFromVerifiedProposal(command.WorkspaceID, record.Run, successfulCall, command.Proposal, preverifiedSection.Citations)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(section, preverifiedSection) {
			return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact section proposal changed after evidence verification"))
		}
		now := repository.finalizationTime(generation, current, record, successfulCall)
		revisionID, err := repository.ids.New()
		if err != nil {
			return err
		}
		metadata := command.Proposal.Metadata
		artifact, revision, err := artifactdomain.RecordSection(current.Artifact, current.Revision, revisionID, section, artifactdomain.CreatorAgent, &metadata, now)
		if err != nil {
			return generationOutputError(foundation.ErrorInvalidInput, err)
		}
		if err := gormInsertRevision(callbackCtx, tx, command.WorkspaceID, revision); err != nil {
			return err
		}
		if err := gormUpdateGeneratedArtifact(callbackCtx, tx, current, artifact); err != nil {
			return err
		}
		completed := completeSectionGeneration(generation, record.Run.ID, current.Revision.ID, revision, artifact.Version, now)
		if err := artifactapplication.ValidateSectionGeneration(completed); err != nil {
			return generationOutputError(foundation.ErrorConsistencyViolation, err)
		}
		if err := gormUpdateCompletedGeneration(callbackCtx, tx, generation, completed); err != nil {
			return err
		}
		terminalRun := record.Run
		terminalRun.Status = agentdomain.ModelRunSucceeded
		terminalRun.FinalResultType = agentdomain.ResultTypeArtifactSection
		terminalRun.Version++
		terminalRun.UpdatedAt = now
		terminalRun.CompletedAt = &now
		if _, replayedRun, err := repository.agent.FinalizeModelRunScoped(callbackCtx, scope, agentapplication.FinalizeModelRunCommand{ExpectedVersion: command.ExpectedModelRunVersion, Run: terminalRun}); err != nil || replayedRun {
			if err == nil {
				err = errors.New("artifact model run unexpectedly replayed while generation was pending")
			}
			return generationOutputError(foundation.ErrorConsistencyViolation, err)
		}
		receipt = outputReceiptFromCompletion(completed, revision)
		if err := validateGenerationReceipt(receipt, command.Input); err != nil {
			return err
		}
		newCompletion = true
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return receipt, replayed, nil
	}
	if callbackSucceeded && newCompletion {
		return artifactworkflow.OutputReceipt{}, false, generationFinalizationUnknown(err)
	}
	return artifactworkflow.OutputReceipt{}, false, generationCapabilityOrExisting(ctx, err)
}

func (repository *GORMSectionGenerationRepository) preverifySectionProposal(
	ctx context.Context,
	command artifactworkflow.FinalizeSectionCommand,
	encodedInput json.RawMessage,
) (artifactdomain.Section, bool, error) {
	var record agentapplication.ModelRunRecord
	var successfulCall agentdomain.ModelCall
	pending := false
	err := repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		generation, found, err := gormLoadGenerationByWorkflow(callbackCtx, tx, command.WorkspaceID, command.WorkflowRunID, command.NodeRunID, false)
		if err != nil {
			return err
		}
		if !found || !generationMatchesInput(generation, command.Input) {
			return generationContextError(foundation.ErrorNotFound, errors.New("artifact generation finalization binding was not found"))
		}
		if err := repository.validateWorkflowBindingScoped(callbackCtx, scope, generation, command.NodeAttemptID, encodedInput); err != nil {
			return err
		}
		if generation.Status != artifactapplication.SectionGenerationPending {
			return nil
		}
		record, err = repository.agent.GetModelRunRecordScoped(callbackCtx, scope, command.WorkspaceID, command.ModelRunID, false)
		if err != nil {
			return err
		}
		successfulCall, err = repository.validateModelRunRecord(record, generation, command.NodeAttemptID, false)
		if err != nil {
			return err
		}
		if command.ModelRunID != record.Run.ID || command.ExpectedModelRunVersion != record.Run.Version || record.Run.Status != agentdomain.ModelRunRunning || record.Run.NodeAttemptID != command.NodeAttemptID {
			return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation model run version or attempt is stale"))
		}
		if command.Proposal.SectionKey != generation.SectionKey {
			return generationOutputError(foundation.ErrorInvalidInput, errors.New("artifact section proposal changed the frozen section key"))
		}
		pending = true
		return nil
	})
	if err != nil || !pending {
		return artifactdomain.Section{}, false, generationCapabilityOrExisting(ctx, err)
	}
	section, err := repository.verifySectionProposal(ctx, command.WorkspaceID, record.Run, successfulCall, command.Proposal)
	if err != nil {
		return artifactdomain.Section{}, false, err
	}
	return section, true, nil
}

func (repository *GORMSectionGenerationRepository) verifySectionProposal(ctx context.Context, workspaceID foundation.ID, run agentdomain.ModelRun, call agentdomain.ModelCall, proposal artifactworkflow.SectionProposal) (artifactdomain.Section, error) {
	if err := validateProposalRuntime(run, call, proposal); err != nil {
		return artifactdomain.Section{}, err
	}
	inputs, err := proposalCitationInputs(workspaceID, run, proposal)
	if err != nil {
		return artifactdomain.Section{}, err
	}
	verified := make([]artifactdomain.Citation, 0)
	if len(inputs) > 0 {
		verified, err = repository.evidence.VerifyCitations(ctx, workspaceID, inputs)
		if err != nil {
			return artifactdomain.Section{}, generationEvidenceVerificationError(err)
		}
	}
	return sectionFromVerifiedProposal(workspaceID, run, call, proposal, verified)
}

func (repository *GORMSectionGenerationRepository) validateWorkflowBindingScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	generation artifactapplication.SectionGeneration,
	attemptID foundation.ID,
	expectedInput json.RawMessage,
) error {
	snapshot, err := repository.binding.LoadRuntimeBindingScoped(ctx, scope, workflowapplication.ScopedRuntimeBindingQuery{
		WorkspaceID: generation.WorkspaceID, WorkflowRunID: generation.WorkflowRunID,
		NodeRunID: generation.NodeRunID, NodeAttemptID: attemptID,
	})
	if err != nil {
		return mapGenerationWorkflowBindingError(err)
	}
	registered := artifactworkflow.RegisteredDefinition()
	if snapshot.DefinitionKey != registered.Key || snapshot.DefinitionVersion != registered.Version || snapshot.NodeKey != artifactworkflow.NodeKey ||
		snapshot.NodeType != artifactworkflow.NodeKind || snapshot.InputSchemaVersion != artifactworkflow.InputSchemaVersion ||
		snapshot.OutputSchemaVersion != artifactworkflow.OutputSchemaVersion || !sameGenerationJSON(snapshot.RunInput, expectedInput) ||
		!sameGenerationJSON(snapshot.NodeInput, expectedInput) || (attemptID != "" && !snapshot.AttemptFound) {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation workflow identity drifted"))
	}
	var graph workflowdomain.CanonicalGraph
	if err := decodeGenerationJSON(snapshot.DefinitionGraph, &graph); err != nil || !reflect.DeepEqual(graph, registered.Graph) {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation workflow definition drifted"))
	}
	return nil
}

func mapGenerationWorkflowBindingError(err error) error {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return generationCapabilityError(err)
	}
	switch classified.Kind {
	case foundation.ErrorNotFound, foundation.ErrorInvalidInput, foundation.ErrorConsistencyViolation:
		return generationContextError(foundation.ErrorConsistencyViolation, err)
	default:
		return generationCapabilityError(err)
	}
}

func (repository *GORMSectionGenerationRepository) validateModelRunRecord(record agentapplication.ModelRunRecord, generation artifactapplication.SectionGeneration, expectedAttemptID foundation.ID, terminal bool) (agentdomain.ModelCall, error) {
	run := record.Run
	if run.WorkspaceID != generation.WorkspaceID || run.WorkflowRunID != generation.WorkflowRunID || run.NodeRunID != generation.NodeRunID ||
		run.Profile != repository.profile || run.Prompt != artifactworkflow.PromptRef() || run.ReducedSchema != artifactworkflow.ReducedSchemaRef() ||
		(run.Schema != artifactworkflow.SchemaRef() && run.Schema != artifactworkflow.ReducedSchemaRef()) {
		return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact model run references drifted"))
	}
	if expectedAttemptID != "" && run.NodeAttemptID != expectedAttemptID {
		return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact model run belongs to another node attempt"))
	}
	if terminal {
		if generation.ModelRunID == nil || run.ID != *generation.ModelRunID || run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != agentdomain.ResultTypeArtifactSection || run.FinalErrorCode != "" {
			return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact model run terminal binding is invalid"))
		}
	} else if run.Status != agentdomain.ModelRunRunning || run.FinalResultType != "" || run.FinalErrorCode != "" {
		return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact model run is not running"))
	}
	var successful agentdomain.ModelCall
	for index, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != run.ID || call.CallNo != index+1 || call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt ||
			(call.Schema != run.Schema && call.Schema != run.ReducedSchema) || call.Status == agentdomain.ModelCallStarted {
			return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact model call history is invalid"))
		}
		if call.Status == agentdomain.ModelCallSucceeded {
			successful = call
		}
	}
	if successful.ID == "" || successful.CallNo != len(record.Calls) {
		return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact model run has no final successful call"))
	}
	return successful, nil
}

func (repository *GORMSectionGenerationRepository) finalizationTime(generation artifactapplication.SectionGeneration, current artifactapplication.State, record agentapplication.ModelRunRecord, call agentdomain.ModelCall) time.Time {
	now := repository.clock.Now().UTC()
	floors := []time.Time{generation.CreatedAt, generation.UpdatedAt, current.Artifact.CreatedAt, current.Artifact.UpdatedAt, current.Revision.CreatedAt, record.Run.CreatedAt, record.Run.UpdatedAt, call.StartedAt}
	if call.CompletedAt != nil {
		floors = append(floors, *call.CompletedAt)
	}
	for _, floor := range floors {
		if now.Before(floor.UTC()) {
			now = floor.UTC()
		}
	}
	return now
}

func (repository *GORMSectionGenerationRepository) recoverCommittedGeneration(ctx context.Context, command artifactapplication.StartSectionGenerationCommand, requestHash string) (artifactapplication.SectionGeneration, bool, error) {
	var recovered artifactapplication.SectionGeneration
	var found bool
	err := repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(callbackCtx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		generation, exists, err := gormLoadGenerationByKey(callbackCtx, tx, command.WorkspaceID, command.IdempotencyKey, false)
		if err != nil || !exists {
			return err
		}
		if !sameGenerationStartRequest(generation, command, requestHash) {
			return inconsistent(errors.New("recovered artifact generation differs from the committed request"))
		}
		encodedInput, err := gormGenerationWorkflowInput(generation)
		if err != nil {
			return err
		}
		if err := repository.validateWorkflowBindingScoped(callbackCtx, scope, generation, "", encodedInput); err != nil {
			return err
		}
		recovered = generation
		found = true
		return nil
	})
	if err != nil {
		return artifactapplication.SectionGeneration{}, false, generationCapabilityOrExisting(ctx, err)
	}
	return recovered, found, nil
}

func gormGenerationWorkflowInput(generation artifactapplication.SectionGeneration) (json.RawMessage, error) {
	encoded, err := artifactworkflow.EncodeInput(artifactworkflow.Input{
		SchemaVersion: artifactworkflow.InputSchemaVersion,
		ArtifactID:    generation.ArtifactID, RevisionID: generation.SourceRevisionID,
		RevisionNo: generation.SourceRevisionNo, ArtifactVersion: generation.SourceArtifactVersion,
		SectionKey: generation.SectionKey,
	})
	if err != nil {
		return nil, generationContextError(foundation.ErrorConsistencyViolation, fmt.Errorf("encode artifact generation durable input: %w", err))
	}
	return encoded, nil
}

func gormLoadGenerationByKey(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, key string, lock bool) (artifactapplication.SectionGeneration, bool, error) {
	query := `SELECT ` + sectionGenerationColumns + ` FROM learning.artifact_section_generation WHERE workspace_id=?::uuid AND idempotency_key=?`
	return gormLoadGeneration(ctx, db, query, lock, string(workspaceID), key)
}

func gormLoadGenerationByWorkflow(ctx context.Context, db *gorm.DB, workspaceID, workflowRunID, nodeRunID foundation.ID, lock bool) (artifactapplication.SectionGeneration, bool, error) {
	query := `SELECT ` + sectionGenerationColumns + ` FROM learning.artifact_section_generation WHERE workspace_id=?::uuid AND workflow_run_id=?::uuid AND node_run_id=?::uuid`
	return gormLoadGeneration(ctx, db, query, lock, string(workspaceID), string(workflowRunID), string(nodeRunID))
}

func gormLoadActiveGeneration(ctx context.Context, db *gorm.DB, workspaceID, artifactID foundation.ID, sectionKey string) (artifactapplication.SectionGeneration, bool, error) {
	query := `SELECT ` + sectionGenerationColumns + ` FROM learning.artifact_section_generation WHERE workspace_id=?::uuid AND artifact_id=?::uuid AND section_key=? AND status IN ('PENDING','RECOVERY_REQUIRED')`
	return gormLoadGeneration(ctx, db, query, false, string(workspaceID), string(artifactID), sectionKey)
}

func gormLoadGeneration(ctx context.Context, db *gorm.DB, query string, lock bool, args ...any) (artifactapplication.SectionGeneration, bool, error) {
	if lock {
		query += ` FOR UPDATE`
	}
	row, err := gormRow(db.WithContext(ctx), query, args...)
	if err != nil {
		return artifactapplication.SectionGeneration{}, false, classifyGORM(ctx, err, "ARTIFACT_GENERATION_QUERY_FAILED")
	}
	generation, err := scanSectionGeneration(row)
	if gormNoRows(err) {
		return artifactapplication.SectionGeneration{}, false, nil
	}
	if err != nil {
		return artifactapplication.SectionGeneration{}, false, classifyGORM(ctx, err, "ARTIFACT_GENERATION_QUERY_FAILED")
	}
	return generation, true, nil
}

func gormInsertSectionGeneration(ctx context.Context, tx *gorm.DB, generation artifactapplication.SectionGeneration) error {
	return classifyGORM(ctx, tx.WithContext(ctx).Exec(`INSERT INTO learning.artifact_section_generation(
		id,workspace_id,artifact_id,source_revision_id,source_revision_no,source_artifact_version,
		section_key,idempotency_key,request_hash,workflow_run_id,node_run_id,status,version,created_at,updated_at
	) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?,?,?,?,?,?::uuid,?::uuid,?,?,?::timestamptz,?::timestamptz)`,
		string(generation.ID), string(generation.WorkspaceID), string(generation.ArtifactID), string(generation.SourceRevisionID),
		generation.SourceRevisionNo, generation.SourceArtifactVersion, generation.SectionKey, generation.IdempotencyKey,
		generation.RequestHash, string(generation.WorkflowRunID), string(generation.NodeRunID), string(generation.Status),
		generation.Version, generation.CreatedAt.UTC(), generation.UpdatedAt.UTC()).Error, "ARTIFACT_GENERATION_INSERT_FAILED")
}

func gormLoadRevision(ctx context.Context, db *gorm.DB, workspaceID, artifactID, revisionID foundation.ID) (artifactdomain.Revision, error) {
	row, err := gormRow(db.WithContext(ctx), revisionSelect+` WHERE r.workspace_id=?::uuid AND r.artifact_id=?::uuid AND r.id=?::uuid AND r.domain_schema_version IN ('artifact-revision/v1','artifact-revision/v2')`, string(workspaceID), string(artifactID), string(revisionID))
	if err != nil {
		return artifactdomain.Revision{}, classifyGORM(ctx, err, "ARTIFACT_GENERATION_REVISION_QUERY_FAILED")
	}
	revision, err := scanRevision(row)
	if err != nil {
		return artifactdomain.Revision{}, err
	}
	if err := gormValidateRevisionDocumentSources(ctx, db, workspaceID, revision); err != nil {
		return artifactdomain.Revision{}, err
	}
	return revision, nil
}

func gormUpdateGeneratedArtifact(ctx context.Context, tx *gorm.DB, current artifactapplication.State, artifact artifactdomain.Artifact) error {
	result := tx.WithContext(ctx).Exec(`UPDATE learning.artifact SET status=?,source_coverage=?::jsonb,current_revision_id=?::uuid,version=?,updated_at=?::timestamptz WHERE id=?::uuid AND workspace_id=?::uuid AND domain_schema_version=? AND version=? AND current_revision_id=?::uuid`,
		string(artifact.Status), string(marshalJSON(artifact.SourceCoverage)), string(artifact.CurrentRevisionID), artifact.Version, artifact.UpdatedAt.UTC(),
		string(current.Artifact.ID), string(current.Artifact.WorkspaceID), artifactSchemaVersion, current.Artifact.Version, string(current.Revision.ID))
	if result.Error != nil {
		return classifyGORM(ctx, result.Error, "ARTIFACT_GENERATION_UPDATE_FAILED")
	}
	if result.RowsAffected != 1 {
		return versionConflict(errors.New("artifact generation compare-and-swap did not update the artifact"))
	}
	return nil
}

func gormUpdateCompletedGeneration(ctx context.Context, tx *gorm.DB, pending, completed artifactapplication.SectionGeneration) error {
	result := tx.WithContext(ctx).Exec(`UPDATE learning.artifact_section_generation SET status=?,model_run_id=?::uuid,recorded_base_revision_id=?::uuid,recorded_revision_id=?::uuid,recorded_artifact_version=?,content_hash=?,version=?,updated_at=?::timestamptz,completed_at=?::timestamptz,terminal_at=?::timestamptz WHERE id=?::uuid AND workspace_id=?::uuid AND status='PENDING' AND version=?`,
		string(completed.Status), string(*completed.ModelRunID), string(*completed.RecordedBaseRevisionID), string(*completed.RecordedRevisionID), *completed.RecordedArtifactVersion,
		completed.ContentHash, completed.Version, completed.UpdatedAt.UTC(), completed.UpdatedAt.UTC(), completed.UpdatedAt.UTC(),
		string(pending.ID), string(pending.WorkspaceID), pending.Version)
	if result.Error != nil {
		return classifyGORM(ctx, result.Error, "ARTIFACT_GENERATION_COMPLETION_FAILED")
	}
	if result.RowsAffected != 1 {
		return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation completion compare-and-swap failed"))
	}
	return nil
}

func gormLoadCompletedGenerationReceipt(ctx context.Context, db *gorm.DB, generation artifactapplication.SectionGeneration, input artifactworkflow.Input) (artifactworkflow.OutputReceipt, artifactdomain.Revision, error) {
	if generation.Status != artifactapplication.SectionGenerationCompleted || generation.RecordedBaseRevisionID == nil || generation.RecordedRevisionID == nil || generation.RecordedArtifactVersion == nil || generation.ModelRunID == nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation completion is incomplete"))
	}
	base, err := gormLoadRevision(ctx, db, generation.WorkspaceID, generation.ArtifactID, *generation.RecordedBaseRevisionID)
	if gormNoRows(err) {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation base revision is missing"))
	}
	if err != nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, err
	}
	recorded, err := gormLoadRevision(ctx, db, generation.WorkspaceID, generation.ArtifactID, *generation.RecordedRevisionID)
	if gormNoRows(err) {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation recorded revision is missing"))
	}
	if err != nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, err
	}
	if recorded.RevisionNo != base.RevisionNo+1 || recorded.ContentHash != generation.ContentHash || recorded.ArtifactID != generation.ArtifactID || base.ArtifactID != generation.ArtifactID {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation recorded revision binding is invalid"))
	}
	receipt := outputReceiptFromCompletion(generation, recorded)
	if err := validateGenerationReceipt(receipt, input); err != nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, err
	}
	return receipt, recorded, nil
}

func generationCapabilityOrExisting(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := gormArtifactContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, artifactworkflow.ErrorCodeCapabilityUnavailable, false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, artifactworkflow.ErrorCodeCapabilityUnavailable, true, cause)
	}
	return generationCapabilityError(err)
}

var _ artifactapplication.SectionGenerationStarter = (*GORMSectionGenerationRepository)(nil)
var _ artifactworkflow.ContextLoader = (*GORMSectionGenerationRepository)(nil)
var _ artifactworkflow.Finalizer = (*GORMSectionGenerationRepository)(nil)
