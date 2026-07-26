package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

const sectionGenerationColumns = `
	id::text,workspace_id::text,artifact_id::text,source_revision_id::text,
	source_revision_no,source_artifact_version,section_key,idempotency_key,request_hash,
	workflow_run_id::text,node_run_id::text,status,model_run_id::text,
	recorded_base_revision_id::text,recorded_revision_id::text,recorded_artifact_version,
	content_hash,failure_class,error_code,error_summary,version,created_at,updated_at,completed_at,terminal_at`

// RuntimeStarterTx 是 Artifact generation 启动 Workflow 所需的 caller-owned transaction 边界。
type RuntimeStarterTx interface {
	StartTx(context.Context, pgx.Tx, workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error)
}

// SectionGenerationRepository 原子编排 Artifact、Workflow 与 Agent 的章节生成事实。
type SectionGenerationRepository struct {
	db       DB
	runtime  RuntimeStarterTx
	agent    agentapplication.ModelRunTxFinalizer
	evidence artifactapplication.CitationVerifier
	ids      foundation.IDGenerator
	clock    foundation.Clock
	profile  agentdomain.ModelProfileRef
}

// NewSectionGenerationRepository 构造 PostgreSQL 章节生成协调器。
func NewSectionGenerationRepository(
	db DB,
	runtime RuntimeStarterTx,
	agent agentapplication.ModelRunTxFinalizer,
	evidence artifactapplication.CitationVerifier,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	profile agentdomain.ModelProfileRef,
) (*SectionGenerationRepository, error) {
	if nilGenerationDependency(db) || nilGenerationDependency(runtime) || nilGenerationDependency(agent) ||
		nilGenerationDependency(evidence) || nilGenerationDependency(ids) || nilGenerationDependency(clock) || profile.Validate() != nil {
		return nil, unavailable(errors.New("artifact section generation dependencies are incomplete"))
	}
	return &SectionGenerationRepository{
		db: db, runtime: runtime, agent: agent, evidence: evidence, ids: ids, clock: clock, profile: profile,
	}, nil
}

// StartSectionGeneration 原子冻结当前已审批章节并创建唯一 Workflow Run。
func (repository *SectionGenerationRepository) StartSectionGeneration(
	ctx context.Context,
	command artifactapplication.StartSectionGenerationCommand,
) (artifactapplication.StartSectionGenerationResult, error) {
	if repository == nil || !repository.available() {
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
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(command.WorkspaceID)+":"+command.IdempotencyKey); err != nil {
		return artifactapplication.StartSectionGenerationResult{}, classify(err)
	}
	if existing, found, err := loadSectionGenerationByKey(ctx, tx, command.WorkspaceID, command.IdempotencyKey, true); err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	} else if found {
		if !sameGenerationStartRequest(existing, command, requestHash) {
			return artifactapplication.StartSectionGenerationResult{}, idempotencyConflict(errors.New("artifact generation idempotency key is bound to another request"))
		}
		if err := tx.Commit(ctx); err != nil {
			return artifactapplication.StartSectionGenerationResult{}, classify(err)
		}
		return artifactapplication.StartSectionGenerationResult{Generation: existing, Replayed: true}, nil
	}

	current, err := loadState(ctx, tx, command.WorkspaceID, command.ArtifactID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapplication.StartSectionGenerationResult{}, notFound(err)
	}
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	if _, err := artifactapplication.ValidateSectionGenerationSource(current, command.ExpectedVersion, command.SectionKey); err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	if _, found, err := loadActiveSectionGeneration(ctx, tx, command.WorkspaceID, command.ArtifactID, command.SectionKey); err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	} else if found {
		return artifactapplication.StartSectionGenerationResult{}, idempotencyConflict(errors.New("artifact section already has an active generation workflow"))
	}

	input := artifactworkflow.Input{
		SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: current.Artifact.ID,
		RevisionID: current.Revision.ID, RevisionNo: current.Revision.RevisionNo,
		ArtifactVersion: current.Artifact.Version, SectionKey: command.SectionKey,
	}
	encodedInput, err := artifactworkflow.EncodeInput(input)
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, generationContextError(foundation.ErrorConsistencyViolation, err)
	}
	workflowRequest, err := workflowapplication.BuildRuntimeStartRequest(
		repository.ids, repository.clock, command.WorkspaceID,
		sectionGenerationWorkflowKey(command.WorkspaceID, command.IdempotencyKey), encodedInput,
		artifactworkflow.RegisteredDefinition(),
	)
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	generationID, err := repository.ids.New()
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	started, err := repository.runtime.StartTx(ctx, tx, workflowRequest)
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	if started.Replayed || !sameStartedWorkflow(started, command.WorkspaceID, encodedInput) {
		return artifactapplication.StartSectionGenerationResult{}, inconsistent(errors.New("artifact generation resolved to an unexpected workflow start"))
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
		return artifactapplication.StartSectionGenerationResult{}, inconsistent(fmt.Errorf("artifact generation candidate is invalid: %w", err))
	}
	if err := insertSectionGeneration(ctx, tx, generation); err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	result := artifactapplication.StartSectionGenerationResult{Generation: generation}
	if err := tx.Commit(ctx); err != nil {
		recovered, found, recoveryErr := repository.recoverCommittedGeneration(ctx, command, requestHash)
		if recoveryErr != nil {
			return artifactapplication.StartSectionGenerationResult{}, errors.Join(classify(err), recoveryErr)
		}
		if found {
			return artifactapplication.StartSectionGenerationResult{Generation: recovered}, nil
		}
		return artifactapplication.StartSectionGenerationResult{}, classify(err)
	}
	return result, nil
}

// LoadGenerationContext 从一个一致快照恢复冻结来源 Revision 与当前可 rebase 状态。
func (repository *SectionGenerationRepository) LoadGenerationContext(
	ctx context.Context,
	query artifactworkflow.GenerationContextQuery,
) (artifactworkflow.GenerationContext, error) {
	if repository == nil || !repository.available() {
		return artifactworkflow.GenerationContext{}, generationCapabilityError(errors.New("artifact section generation repository is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	encodedInput, err := validateGenerationContextQuery(query)
	if err != nil {
		return artifactworkflow.GenerationContext{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return artifactworkflow.GenerationContext{}, generationCapabilityError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`); err != nil {
		return artifactworkflow.GenerationContext{}, generationCapabilityError(err)
	}
	generation, found, err := loadSectionGenerationByWorkflow(ctx, tx, query.WorkspaceID, query.WorkflowRunID, query.NodeRunID, false)
	if err != nil {
		return artifactworkflow.GenerationContext{}, err
	}
	if !found {
		return artifactworkflow.GenerationContext{}, generationContextError(foundation.ErrorNotFound, errors.New("artifact generation workflow binding was not found"))
	}
	if generation.Status != artifactapplication.SectionGenerationPending || !generationMatchesInput(generation, query.Input) {
		return artifactworkflow.GenerationContext{}, generationContextError(foundation.ErrorVersionConflict, errors.New("artifact generation is not pending for the frozen input"))
	}
	if err := validatePersistedWorkflowBinding(ctx, tx, generation, encodedInput); err != nil {
		return artifactworkflow.GenerationContext{}, err
	}
	current, err := loadState(ctx, tx, query.WorkspaceID, generation.ArtifactID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactworkflow.GenerationContext{}, generationContextError(foundation.ErrorNotFound, err)
	}
	if err != nil {
		return artifactworkflow.GenerationContext{}, err
	}
	source, err := loadRevision(ctx, tx, query.WorkspaceID, generation.ArtifactID, generation.SourceRevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactworkflow.GenerationContext{}, generationContextError(foundation.ErrorConsistencyViolation, err)
	}
	if err != nil {
		return artifactworkflow.GenerationContext{}, err
	}
	if _, err := artifactapplication.ValidateSectionGenerationRebase(source, current, generation.SectionKey); err != nil {
		return artifactworkflow.GenerationContext{}, generationContextError(foundation.ErrorVersionConflict, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactworkflow.GenerationContext{}, generationCapabilityError(err)
	}
	return artifactworkflow.GenerationContext{
		Current: artifactapplication.State{
			Artifact: artifactdomain.CloneArtifact(current.Artifact), Revision: artifactdomain.CloneRevision(current.Revision),
		},
		SourceRevision: artifactdomain.CloneRevision(source), ProfileRef: repository.profile,
	}, nil
}

// Lookup 在 Provider 调用前恢复已提交终态。
func (repository *SectionGenerationRepository) Lookup(
	ctx context.Context,
	lookup artifactworkflow.FinalizationLookup,
) (artifactworkflow.OutputReceipt, bool, error) {
	if repository == nil || !repository.available() {
		return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(errors.New("artifact section generation repository is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	encodedInput, err := validateFinalizationLookup(lookup)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	generation, found, err := loadSectionGenerationByWorkflow(ctx, tx, lookup.WorkspaceID, lookup.WorkflowRunID, lookup.NodeRunID, false)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if !found || !generationMatchesInput(generation, lookup.Input) {
		return artifactworkflow.OutputReceipt{}, false, generationContextError(foundation.ErrorNotFound, errors.New("artifact generation finalization binding was not found"))
	}
	if err := validatePersistedWorkflowBinding(ctx, tx, generation, encodedInput); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if err := validateNodeAttemptBinding(ctx, tx, generation.NodeRunID, lookup.NodeAttemptID); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	switch generation.Status {
	case artifactapplication.SectionGenerationPending:
		run, runFound, runErr := repository.agent.GetModelRunByAttemptTx(ctx, tx, lookup.WorkspaceID, lookup.NodeAttemptID, false)
		if runErr != nil {
			return artifactworkflow.OutputReceipt{}, false, runErr
		}
		if runFound && (run.WorkflowRunID != lookup.WorkflowRunID || run.NodeRunID != lookup.NodeRunID ||
			run.NodeAttemptID != lookup.NodeAttemptID || run.Status != agentdomain.ModelRunRunning) {
			return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("pending artifact generation has a split model run binding"))
		}
		if err := tx.Commit(ctx); err != nil {
			return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(err)
		}
		return artifactworkflow.OutputReceipt{}, false, nil
	case artifactapplication.SectionGenerationRecoveryRequired:
		return artifactworkflow.OutputReceipt{}, false, generationFinalizationUnknown(errors.New("artifact generation requires explicit recovery"))
	case artifactapplication.SectionGenerationFailed, artifactapplication.SectionGenerationCancelled:
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation is terminal without an output receipt"))
	case artifactapplication.SectionGenerationCompleted:
	default:
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation has an unsupported status"))
	}
	if generation.ModelRunID == nil {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("completed artifact generation has no model run"))
	}
	record, err := repository.agent.GetModelRunRecordTx(ctx, tx, lookup.WorkspaceID, *generation.ModelRunID, false)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if _, err := repository.validateModelRunRecord(record, generation, "", true); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	receipt, _, err := loadCompletedGenerationReceipt(ctx, tx, generation, lookup.Input)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(err)
	}
	return receipt, true, nil
}

// Finalize 原子写入 Artifact Revision、generation receipt 与 Model Run 终态。
func (repository *SectionGenerationRepository) Finalize(
	ctx context.Context,
	command artifactworkflow.FinalizeSectionCommand,
) (artifactworkflow.OutputReceipt, bool, error) {
	if repository == nil || !repository.available() {
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
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// 所有终结入口保持 generation -> artifact -> model_run 的全局锁顺序。
	generation, found, err := loadSectionGenerationByWorkflow(ctx, tx, command.WorkspaceID, command.WorkflowRunID, command.NodeRunID, true)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if !found || !generationMatchesInput(generation, command.Input) {
		return artifactworkflow.OutputReceipt{}, false, generationContextError(foundation.ErrorNotFound, errors.New("artifact generation finalization binding was not found"))
	}
	if err := validatePersistedWorkflowBinding(ctx, tx, generation, encodedInput); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if err := validateNodeAttemptBinding(ctx, tx, generation.NodeRunID, command.NodeAttemptID); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	switch generation.Status {
	case artifactapplication.SectionGenerationRecoveryRequired:
		return artifactworkflow.OutputReceipt{}, false, generationFinalizationUnknown(errors.New("artifact generation requires explicit recovery"))
	case artifactapplication.SectionGenerationFailed, artifactapplication.SectionGenerationCancelled:
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation is already terminal without an output receipt"))
	case artifactapplication.SectionGenerationPending, artifactapplication.SectionGenerationCompleted:
	default:
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation has an unsupported status"))
	}
	current, err := loadState(ctx, tx, command.WorkspaceID, generation.ArtifactID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactworkflow.OutputReceipt{}, false, generationContextError(foundation.ErrorConsistencyViolation, err)
	}
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	runID := command.ModelRunID
	if generation.Status == artifactapplication.SectionGenerationCompleted {
		if generation.ModelRunID == nil {
			return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("completed artifact generation has no model run"))
		}
		runID = *generation.ModelRunID
	}
	record, err := repository.agent.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, runID, true)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	successfulCall, err := repository.validateModelRunRecord(record, generation, command.NodeAttemptID, generation.Status == artifactapplication.SectionGenerationCompleted)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}

	if generation.Status == artifactapplication.SectionGenerationCompleted {
		if command.ModelRunID != runID {
			return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation already completed with another model run"))
		}
		receipt, recorded, err := loadCompletedGenerationReceipt(ctx, tx, generation, command.Input)
		if err != nil {
			return artifactworkflow.OutputReceipt{}, false, err
		}
		persistedSection, sectionFound := revisionSectionByKey(recorded, generation.SectionKey)
		if !sectionFound || validateCompletedSectionProposal(command.WorkspaceID, record.Run, successfulCall, command.Proposal, persistedSection) != nil ||
			recorded.CreatedBy != artifactdomain.CreatorAgent ||
			recorded.Metadata == nil || !reflect.DeepEqual(*recorded.Metadata, command.Proposal.Metadata) {
			return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation terminal proposal differs"))
		}
		if err := tx.Commit(ctx); err != nil {
			return artifactworkflow.OutputReceipt{}, false, generationCapabilityError(err)
		}
		return receipt, true, nil
	}
	if generation.Status != artifactapplication.SectionGenerationPending {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation is not finalizable"))
	}
	if !preverified {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("pending artifact generation has no preverified section"))
	}
	if command.ModelRunID != record.Run.ID || command.ExpectedModelRunVersion != record.Run.Version ||
		record.Run.Status != agentdomain.ModelRunRunning || record.Run.NodeAttemptID != command.NodeAttemptID {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation model run version or attempt is stale"))
	}
	source, err := loadRevision(ctx, tx, command.WorkspaceID, generation.ArtifactID, generation.SourceRevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactworkflow.OutputReceipt{}, false, generationContextError(foundation.ErrorConsistencyViolation, err)
	}
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	target, err := artifactapplication.ValidateSectionGenerationRebase(source, current, generation.SectionKey)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationContextError(foundation.ErrorVersionConflict, err)
	}
	if command.Proposal.SectionKey != target.Key || command.Proposal.Title != target.Title {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorInvalidInput, errors.New("artifact section proposal changed the frozen section identity"))
	}
	section, err := sectionFromVerifiedProposal(command.WorkspaceID, record.Run, successfulCall, command.Proposal, preverifiedSection.Citations)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if !reflect.DeepEqual(section, preverifiedSection) {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact section proposal changed after evidence verification"))
	}
	now := repository.finalizationTime(generation, current, record, successfulCall)
	revisionID, err := repository.ids.New()
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	metadata := command.Proposal.Metadata
	artifact, revision, err := artifactdomain.RecordSection(
		current.Artifact, current.Revision, revisionID, section, artifactdomain.CreatorAgent, &metadata, now,
	)
	if err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorInvalidInput, err)
	}
	if err := insertRevision(ctx, tx, command.WorkspaceID, revision); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if err := updateGeneratedArtifact(ctx, tx, current, artifact); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	completedGeneration := completeSectionGeneration(generation, record.Run.ID, current.Revision.ID, revision, artifact.Version, now)
	if err := artifactapplication.ValidateSectionGeneration(completedGeneration); err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, err)
	}
	if err := updateCompletedSectionGeneration(ctx, tx, generation, completedGeneration); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	terminalRun := record.Run
	terminalRun.Status = agentdomain.ModelRunSucceeded
	terminalRun.FinalResultType = agentdomain.ResultTypeArtifactSection
	terminalRun.Version++
	terminalRun.UpdatedAt = now
	terminalRun.CompletedAt = &now
	if _, replayed, err := repository.agent.FinalizeModelRunTx(ctx, tx, agentapplication.FinalizeModelRunCommand{
		ExpectedVersion: command.ExpectedModelRunVersion, Run: terminalRun,
	}); err != nil || replayed {
		if err == nil {
			err = errors.New("artifact model run unexpectedly replayed while generation was pending")
		}
		return artifactworkflow.OutputReceipt{}, false, generationOutputError(foundation.ErrorConsistencyViolation, err)
	}
	receipt := outputReceiptFromCompletion(completedGeneration, revision)
	if err := validateGenerationReceipt(receipt, command.Input); err != nil {
		return artifactworkflow.OutputReceipt{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactworkflow.OutputReceipt{}, false, generationFinalizationUnknown(err)
	}
	return receipt, false, nil
}

func validateFinalizationLookup(lookup artifactworkflow.FinalizationLookup) (json.RawMessage, error) {
	if !validDistinctGenerationIDs(
		lookup.WorkspaceID, lookup.WorkflowRunID, lookup.NodeRunID, lookup.NodeAttemptID,
		lookup.Input.ArtifactID, lookup.Input.RevisionID,
	) {
		return nil, generationInputError(errors.New("artifact generation finalization identity is invalid"))
	}
	encoded, err := artifactworkflow.EncodeInput(lookup.Input)
	if err != nil {
		return nil, generationInputError(err)
	}
	return encoded, nil
}

func validateFinalizeSectionCommand(command artifactworkflow.FinalizeSectionCommand) (json.RawMessage, error) {
	encoded, err := validateFinalizationLookup(command.FinalizationLookup)
	if err != nil {
		return nil, err
	}
	if !validID(command.ModelRunID) || command.ExpectedModelRunVersion < 1 ||
		command.ModelRunID == command.WorkspaceID || command.ModelRunID == command.WorkflowRunID ||
		command.ModelRunID == command.NodeRunID || command.ModelRunID == command.NodeAttemptID ||
		command.ModelRunID == command.Input.ArtifactID || command.ModelRunID == command.Input.RevisionID {
		return nil, generationInputError(errors.New("artifact generation finalization model run identity is invalid"))
	}
	return encoded, nil
}

func validateNodeAttemptBinding(ctx context.Context, db queryer, nodeRunID, attemptID foundation.ID) error {
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2
	)`, string(attemptID), string(nodeRunID)).Scan(&exists); err != nil {
		return generationCapabilityError(err)
	}
	if !exists {
		return generationContextError(foundation.ErrorNotFound, errors.New("artifact generation node attempt was not found"))
	}
	return nil
}

func (repository *SectionGenerationRepository) validateModelRunRecord(
	record agentapplication.ModelRunRecord,
	generation artifactapplication.SectionGeneration,
	expectedAttemptID foundation.ID,
	terminal bool,
) (agentdomain.ModelCall, error) {
	run := record.Run
	if run.WorkspaceID != generation.WorkspaceID || run.WorkflowRunID != generation.WorkflowRunID ||
		run.NodeRunID != generation.NodeRunID || run.Profile != repository.profile ||
		run.Prompt != artifactworkflow.PromptRef() || run.ReducedSchema != artifactworkflow.ReducedSchemaRef() ||
		(run.Schema != artifactworkflow.SchemaRef() && run.Schema != artifactworkflow.ReducedSchemaRef()) {
		return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact model run references drifted"))
	}
	if expectedAttemptID != "" && run.NodeAttemptID != expectedAttemptID {
		return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact model run belongs to another node attempt"))
	}
	if terminal {
		if generation.ModelRunID == nil || run.ID != *generation.ModelRunID || run.Status != agentdomain.ModelRunSucceeded ||
			run.FinalResultType != agentdomain.ResultTypeArtifactSection || run.FinalErrorCode != "" {
			return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact model run terminal binding is invalid"))
		}
	} else if run.Status != agentdomain.ModelRunRunning || run.FinalResultType != "" || run.FinalErrorCode != "" {
		return agentdomain.ModelCall{}, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact model run is not running"))
	}
	var successful agentdomain.ModelCall
	for index, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != run.ID || call.CallNo != index+1 ||
			call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt ||
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

func (repository *SectionGenerationRepository) preverifySectionProposal(
	ctx context.Context,
	command artifactworkflow.FinalizeSectionCommand,
	encodedInput json.RawMessage,
) (artifactdomain.Section, bool, error) {
	// Citation verification may open immutable files and query other modules. Close
	// this read snapshot before that work; Finalize rechecks every binding under locks.
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return artifactdomain.Section{}, false, generationCapabilityError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	generation, found, err := loadSectionGenerationByWorkflow(
		ctx, tx, command.WorkspaceID, command.WorkflowRunID, command.NodeRunID, false,
	)
	if err != nil {
		return artifactdomain.Section{}, false, err
	}
	if !found || !generationMatchesInput(generation, command.Input) {
		return artifactdomain.Section{}, false, generationContextError(foundation.ErrorNotFound, errors.New("artifact generation finalization binding was not found"))
	}
	if err := validatePersistedWorkflowBinding(ctx, tx, generation, encodedInput); err != nil {
		return artifactdomain.Section{}, false, err
	}
	if err := validateNodeAttemptBinding(ctx, tx, generation.NodeRunID, command.NodeAttemptID); err != nil {
		return artifactdomain.Section{}, false, err
	}
	if generation.Status != artifactapplication.SectionGenerationPending {
		if err := tx.Rollback(ctx); err != nil {
			return artifactdomain.Section{}, false, generationCapabilityError(err)
		}
		return artifactdomain.Section{}, false, nil
	}
	record, err := repository.agent.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, command.ModelRunID, false)
	if err != nil {
		return artifactdomain.Section{}, false, err
	}
	successfulCall, err := repository.validateModelRunRecord(record, generation, command.NodeAttemptID, false)
	if err != nil {
		return artifactdomain.Section{}, false, err
	}
	if command.ModelRunID != record.Run.ID || command.ExpectedModelRunVersion != record.Run.Version ||
		record.Run.Status != agentdomain.ModelRunRunning || record.Run.NodeAttemptID != command.NodeAttemptID {
		return artifactdomain.Section{}, false, generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation model run version or attempt is stale"))
	}
	if command.Proposal.SectionKey != generation.SectionKey {
		return artifactdomain.Section{}, false, generationOutputError(foundation.ErrorInvalidInput, errors.New("artifact section proposal changed the frozen section key"))
	}
	if err := tx.Rollback(ctx); err != nil {
		return artifactdomain.Section{}, false, generationCapabilityError(err)
	}
	section, err := repository.verifySectionProposal(ctx, command.WorkspaceID, record.Run, successfulCall, command.Proposal)
	if err != nil {
		return artifactdomain.Section{}, false, err
	}
	return section, true, nil
}

func (repository *SectionGenerationRepository) verifySectionProposal(
	ctx context.Context,
	workspaceID foundation.ID,
	run agentdomain.ModelRun,
	successfulCall agentdomain.ModelCall,
	proposal artifactworkflow.SectionProposal,
) (artifactdomain.Section, error) {
	if err := validateProposalRuntime(run, successfulCall, proposal); err != nil {
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
	return sectionFromVerifiedProposal(workspaceID, run, successfulCall, proposal, verified)
}

func proposalCitationInputs(
	workspaceID foundation.ID,
	run agentdomain.ModelRun,
	proposal artifactworkflow.SectionProposal,
) ([]artifactapplication.CitationInput, error) {
	inputs := make([]artifactapplication.CitationInput, len(proposal.Citations))
	for index, citation := range proposal.Citations {
		if citation.Validate() != nil || citation.WorkspaceID != workspaceID || citation.IndexVersionID != run.Retrieval.IndexVersionID {
			return nil, generationEvidenceError(errors.New("artifact generation citation differs from the model run retrieval"))
		}
		inputs[index] = artifactapplication.CitationInput{
			IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
			SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		}
	}
	return inputs, nil
}

func sectionFromVerifiedProposal(
	workspaceID foundation.ID,
	run agentdomain.ModelRun,
	successfulCall agentdomain.ModelCall,
	proposal artifactworkflow.SectionProposal,
	verified []artifactdomain.Citation,
) (artifactdomain.Section, error) {
	if err := validateProposalRuntime(run, successfulCall, proposal); err != nil {
		return artifactdomain.Section{}, err
	}
	inputs, err := proposalCitationInputs(workspaceID, run, proposal)
	if err != nil {
		return artifactdomain.Section{}, err
	}
	if len(verified) != len(inputs) {
		return artifactdomain.Section{}, generationEvidenceError(errors.New("artifact citation verifier returned an incomplete result"))
	}
	for index := range verified {
		if verified[index].SourceVersionID != inputs[index].SourceVersionID ||
			verified[index].SourceSpanID != inputs[index].SourceSpanID || !verified[index].Verified {
			return artifactdomain.Section{}, generationEvidenceError(errors.New("artifact citation verifier result drifted"))
		}
	}
	return artifactdomain.Section{
		Key: proposal.SectionKey, Title: proposal.Title, Content: proposal.Content,
		Citations: verified, Coverage: proposal.Coverage,
	}, nil
}

func validateCompletedSectionProposal(
	workspaceID foundation.ID,
	run agentdomain.ModelRun,
	successfulCall agentdomain.ModelCall,
	proposal artifactworkflow.SectionProposal,
	persisted artifactdomain.Section,
) error {
	if validateProposalRuntime(run, successfulCall, proposal) != nil || proposal.SectionKey != persisted.Key ||
		proposal.Title != persisted.Title || proposal.Content != persisted.Content ||
		!reflect.DeepEqual(proposal.Coverage, persisted.Coverage) {
		return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation terminal proposal differs"))
	}
	inputs, err := proposalCitationInputs(workspaceID, run, proposal)
	if err != nil {
		return err
	}
	if len(inputs) != len(persisted.Citations) {
		return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation terminal citations differ"))
	}
	for index, input := range inputs {
		if input.SourceVersionID != persisted.Citations[index].SourceVersionID ||
			input.SourceSpanID != persisted.Citations[index].SourceSpanID || !persisted.Citations[index].Verified {
			return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation terminal citation binding differs"))
		}
	}
	return nil
}

func validateProposalRuntime(
	run agentdomain.ModelRun,
	successfulCall agentdomain.ModelCall,
	proposal artifactworkflow.SectionProposal,
) error {
	if proposal.Metadata.PromptVersion != run.Prompt.Version || proposal.Metadata.ModelVersion != run.Model.ModelVersion ||
		proposal.Metadata.WorkflowDefinitionVersion != generationDefinitionVersion() ||
		proposal.Metadata.SchemaVersion != successfulCall.Schema.Version {
		return generationOutputError(foundation.ErrorInvalidInput, errors.New("artifact generation metadata differs from the executed runtime"))
	}
	return nil
}

func (repository *SectionGenerationRepository) finalizationTime(
	generation artifactapplication.SectionGeneration,
	current artifactapplication.State,
	record agentapplication.ModelRunRecord,
	successfulCall agentdomain.ModelCall,
) time.Time {
	now := repository.clock.Now().UTC()
	floors := []time.Time{
		generation.CreatedAt, generation.UpdatedAt, current.Artifact.CreatedAt, current.Artifact.UpdatedAt,
		current.Revision.CreatedAt, record.Run.CreatedAt, record.Run.UpdatedAt, successfulCall.StartedAt,
	}
	if successfulCall.CompletedAt != nil {
		floors = append(floors, *successfulCall.CompletedAt)
	}
	for _, floor := range floors {
		if now.Before(floor.UTC()) {
			now = floor.UTC()
		}
	}
	return now
}

func updateGeneratedArtifact(ctx context.Context, tx pgx.Tx, current artifactapplication.State, artifact artifactdomain.Artifact) error {
	tag, err := tx.Exec(ctx, `
		UPDATE learning.artifact
		SET status=$1,source_coverage=$2,current_revision_id=$3,version=$4,updated_at=$5
		WHERE id=$6 AND workspace_id=$7 AND domain_schema_version=$8 AND version=$9 AND current_revision_id=$10`,
		string(artifact.Status), marshalJSON(artifact.SourceCoverage), string(artifact.CurrentRevisionID), artifact.Version, artifact.UpdatedAt.UTC(),
		string(current.Artifact.ID), string(current.Artifact.WorkspaceID), artifactSchemaVersion,
		current.Artifact.Version, string(current.Revision.ID),
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return versionConflict(errors.New("artifact generation compare-and-swap did not update the artifact"))
	}
	return nil
}

func completeSectionGeneration(
	generation artifactapplication.SectionGeneration,
	modelRunID, baseRevisionID foundation.ID,
	revision artifactdomain.Revision,
	artifactVersion int64,
	at time.Time,
) artifactapplication.SectionGeneration {
	completed := generation
	completed.Status = artifactapplication.SectionGenerationCompleted
	completed.ModelRunID = &modelRunID
	completed.RecordedBaseRevisionID = &baseRevisionID
	completed.RecordedRevisionID = &revision.ID
	completed.RecordedArtifactVersion = &artifactVersion
	completed.ContentHash = revision.ContentHash
	completed.Version++
	completed.UpdatedAt = at.UTC()
	completedAt := at.UTC()
	completed.CompletedAt = &completedAt
	terminalAt := at.UTC()
	completed.TerminalAt = &terminalAt
	return completed
}

func updateCompletedSectionGeneration(
	ctx context.Context,
	tx pgx.Tx,
	pending, completed artifactapplication.SectionGeneration,
) error {
	tag, err := tx.Exec(ctx, `
			UPDATE learning.artifact_section_generation
			SET status=$1,model_run_id=$2,recorded_base_revision_id=$3,recorded_revision_id=$4,
			    recorded_artifact_version=$5,content_hash=$6,version=$7,updated_at=$8,completed_at=$8,terminal_at=$8
			WHERE id=$9 AND workspace_id=$10 AND status='PENDING' AND version=$11`,
		string(completed.Status), string(*completed.ModelRunID), string(*completed.RecordedBaseRevisionID),
		string(*completed.RecordedRevisionID), *completed.RecordedArtifactVersion, completed.ContentHash,
		completed.Version, completed.UpdatedAt.UTC(), string(pending.ID), string(pending.WorkspaceID), pending.Version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return generationOutputError(foundation.ErrorVersionConflict, errors.New("artifact generation completion compare-and-swap failed"))
	}
	return nil
}

func loadCompletedGenerationReceipt(
	ctx context.Context,
	db queryer,
	generation artifactapplication.SectionGeneration,
	input artifactworkflow.Input,
) (artifactworkflow.OutputReceipt, artifactdomain.Revision, error) {
	if generation.Status != artifactapplication.SectionGenerationCompleted || generation.RecordedBaseRevisionID == nil ||
		generation.RecordedRevisionID == nil || generation.RecordedArtifactVersion == nil || generation.ModelRunID == nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation completion is incomplete"))
	}
	base, err := loadRevision(ctx, db, generation.WorkspaceID, generation.ArtifactID, *generation.RecordedBaseRevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation base revision is missing"))
	}
	if err != nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, err
	}
	recorded, err := loadRevision(ctx, db, generation.WorkspaceID, generation.ArtifactID, *generation.RecordedRevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation recorded revision is missing"))
	}
	if err != nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, err
	}
	if recorded.RevisionNo != base.RevisionNo+1 || recorded.ContentHash != generation.ContentHash ||
		recorded.ArtifactID != generation.ArtifactID || base.ArtifactID != generation.ArtifactID {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, generationOutputError(foundation.ErrorConsistencyViolation, errors.New("artifact generation recorded revision binding is invalid"))
	}
	receipt := outputReceiptFromCompletion(generation, recorded)
	if err := validateGenerationReceipt(receipt, input); err != nil {
		return artifactworkflow.OutputReceipt{}, artifactdomain.Revision{}, err
	}
	return receipt, recorded, nil
}

func outputReceiptFromCompletion(generation artifactapplication.SectionGeneration, revision artifactdomain.Revision) artifactworkflow.OutputReceipt {
	return artifactworkflow.OutputReceipt{
		SchemaVersion: artifactworkflow.OutputSchemaVersion, ArtifactID: generation.ArtifactID,
		BaseRevisionID: *generation.RecordedBaseRevisionID, RevisionID: revision.ID,
		RevisionNo: revision.RevisionNo, ArtifactVersion: *generation.RecordedArtifactVersion,
		SectionKey: generation.SectionKey, ModelRunID: *generation.ModelRunID, ContentHash: generation.ContentHash,
	}
}

func validateGenerationReceipt(receipt artifactworkflow.OutputReceipt, input artifactworkflow.Input) error {
	if _, err := artifactworkflow.EncodeOutputReceipt(receipt); err != nil {
		return generationOutputError(foundation.ErrorConsistencyViolation, err)
	}
	if err := artifactworkflow.ValidateOutputReceiptBinding(receipt, input); err != nil {
		return generationOutputError(foundation.ErrorConsistencyViolation, err)
	}
	return nil
}

func revisionSectionByKey(revision artifactdomain.Revision, key string) (artifactdomain.Section, bool) {
	for _, section := range revision.Sections {
		if section.Key == key {
			return section, true
		}
	}
	return artifactdomain.Section{}, false
}

func (repository *SectionGenerationRepository) recoverCommittedGeneration(
	ctx context.Context,
	command artifactapplication.StartSectionGenerationCommand,
	requestHash string,
) (artifactapplication.SectionGeneration, bool, error) {
	generation, found, err := loadSectionGenerationByKey(ctx, repository.db, command.WorkspaceID, command.IdempotencyKey, false)
	if err != nil || !found {
		return artifactapplication.SectionGeneration{}, found, err
	}
	if !sameGenerationStartRequest(generation, command, requestHash) {
		return artifactapplication.SectionGeneration{}, false, inconsistent(errors.New("recovered artifact generation differs from the committed request"))
	}
	return generation, true, nil
}

func (repository *SectionGenerationRepository) available() bool {
	return !nilGenerationDependency(repository.db) && !nilGenerationDependency(repository.runtime) &&
		!nilGenerationDependency(repository.agent) && !nilGenerationDependency(repository.evidence) &&
		!nilGenerationDependency(repository.ids) && !nilGenerationDependency(repository.clock) && repository.profile.Validate() == nil
}

func nilGenerationDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func sectionGenerationWorkflowKey(workspaceID foundation.ID, key string) string {
	digest := sha256.Sum256([]byte("artifact-section-generation/v1\x00" + string(workspaceID) + "\x00" + key))
	return "artifact-section-generation:" + hex.EncodeToString(digest[:])
}

func sameStartedWorkflow(result workflowapplication.RuntimeStartResult, workspaceID foundation.ID, input json.RawMessage) bool {
	return result.Run.WorkspaceID == workspaceID && result.FirstNode.RunID == result.Run.ID &&
		result.FirstNode.NodeKey == artifactworkflow.NodeKey && result.FirstNode.NodeType == artifactworkflow.NodeKind &&
		result.FirstNode.InputSchemaVersion == artifactworkflow.InputSchemaVersion &&
		result.FirstNode.OutputSchemaVersion == artifactworkflow.OutputSchemaVersion &&
		sameGenerationJSON(result.Run.Input, input) && sameGenerationJSON(result.FirstNode.Input, input)
}

func sameGenerationStartRequest(generation artifactapplication.SectionGeneration, command artifactapplication.StartSectionGenerationCommand, requestHash string) bool {
	return generation.WorkspaceID == command.WorkspaceID && generation.ArtifactID == command.ArtifactID &&
		generation.SourceArtifactVersion == command.ExpectedVersion && generation.SectionKey == command.SectionKey &&
		generation.IdempotencyKey == command.IdempotencyKey && generation.RequestHash == requestHash
}

func generationMatchesInput(generation artifactapplication.SectionGeneration, input artifactworkflow.Input) bool {
	return generation.ArtifactID == input.ArtifactID && generation.SourceRevisionID == input.RevisionID &&
		generation.SourceRevisionNo == input.RevisionNo && generation.SourceArtifactVersion == input.ArtifactVersion &&
		generation.SectionKey == input.SectionKey
}

func validateGenerationContextQuery(query artifactworkflow.GenerationContextQuery) (json.RawMessage, error) {
	if !validDistinctGenerationIDs(query.WorkspaceID, query.WorkflowRunID, query.NodeRunID) {
		return nil, generationContextError(foundation.ErrorInvalidInput, errors.New("artifact generation context identity is invalid"))
	}
	encoded, err := artifactworkflow.EncodeInput(query.Input)
	if err != nil {
		return nil, generationContextError(foundation.ErrorInvalidInput, err)
	}
	return encoded, nil
}

func validatePersistedWorkflowBinding(
	ctx context.Context,
	db queryer,
	generation artifactapplication.SectionGeneration,
	expectedInput json.RawMessage,
) error {
	var runInput, nodeInput, graph []byte
	var definitionKey, nodeKey, nodeType string
	var definitionVersion int64
	var inputSchemaVersion, outputSchemaVersion int
	err := db.QueryRow(ctx, `
		SELECT run.input,node.input,definition.key,definition.version,definition.graph,
		       node.node_key,node.node_type,node.input_schema_version,node.output_schema_version
		FROM workflow.run run
		JOIN workflow.definition definition ON definition.id=run.definition_id AND definition.workspace_id=run.workspace_id
		JOIN workflow.node_run node ON node.run_id=run.id
		WHERE run.workspace_id=$1 AND run.id=$2 AND node.id=$3`,
		string(generation.WorkspaceID), string(generation.WorkflowRunID), string(generation.NodeRunID),
	).Scan(&runInput, &nodeInput, &definitionKey, &definitionVersion, &graph, &nodeKey, &nodeType, &inputSchemaVersion, &outputSchemaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation workflow facts are missing"))
	}
	if err != nil {
		return generationCapabilityError(err)
	}
	registered := artifactworkflow.RegisteredDefinition()
	if definitionKey != registered.Key || definitionVersion != registered.Version || nodeKey != artifactworkflow.NodeKey ||
		nodeType != artifactworkflow.NodeKind || inputSchemaVersion != artifactworkflow.InputSchemaVersion ||
		outputSchemaVersion != artifactworkflow.OutputSchemaVersion || !sameGenerationJSON(runInput, expectedInput) ||
		!sameGenerationJSON(nodeInput, expectedInput) {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation workflow identity drifted"))
	}
	var persistedGraph workflowdomain.CanonicalGraph
	if err := decodeGenerationJSON(graph, &persistedGraph); err != nil || !reflect.DeepEqual(persistedGraph, registered.Graph) {
		return generationContextError(foundation.ErrorConsistencyViolation, errors.New("artifact generation workflow definition drifted"))
	}
	return nil
}

func insertSectionGeneration(ctx context.Context, tx pgx.Tx, generation artifactapplication.SectionGeneration) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO learning.artifact_section_generation(
			id,workspace_id,artifact_id,source_revision_id,source_revision_no,source_artifact_version,
			section_key,idempotency_key,request_hash,workflow_run_id,node_run_id,status,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		string(generation.ID), string(generation.WorkspaceID), string(generation.ArtifactID), string(generation.SourceRevisionID),
		generation.SourceRevisionNo, generation.SourceArtifactVersion, generation.SectionKey, generation.IdempotencyKey,
		generation.RequestHash, string(generation.WorkflowRunID), string(generation.NodeRunID), string(generation.Status),
		generation.Version, generation.CreatedAt.UTC(), generation.UpdatedAt.UTC(),
	)
	if err != nil {
		return classify(err)
	}
	return nil
}

func loadSectionGenerationByKey(ctx context.Context, db queryer, workspaceID foundation.ID, key string, lock bool) (artifactapplication.SectionGeneration, bool, error) {
	return loadSectionGeneration(ctx, db, `workspace_id=$1 AND idempotency_key=$2`, lock, string(workspaceID), key)
}

func loadSectionGenerationByWorkflow(ctx context.Context, db queryer, workspaceID, workflowRunID, nodeRunID foundation.ID, lock bool) (artifactapplication.SectionGeneration, bool, error) {
	return loadSectionGeneration(ctx, db, `workspace_id=$1 AND workflow_run_id=$2 AND node_run_id=$3`, lock,
		string(workspaceID), string(workflowRunID), string(nodeRunID))
}

func loadActiveSectionGeneration(ctx context.Context, db queryer, workspaceID, artifactID foundation.ID, sectionKey string) (artifactapplication.SectionGeneration, bool, error) {
	return loadSectionGeneration(ctx, db, `workspace_id=$1 AND artifact_id=$2 AND section_key=$3 AND status IN ('PENDING','RECOVERY_REQUIRED')`, false,
		string(workspaceID), string(artifactID), sectionKey)
}

func loadSectionGeneration(ctx context.Context, db queryer, predicate string, lock bool, arguments ...any) (artifactapplication.SectionGeneration, bool, error) {
	query := `SELECT ` + sectionGenerationColumns + ` FROM learning.artifact_section_generation WHERE ` + predicate
	if lock {
		query += ` FOR UPDATE`
	}
	generation, err := scanSectionGeneration(db.QueryRow(ctx, query, arguments...))
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapplication.SectionGeneration{}, false, nil
	}
	if err != nil {
		return artifactapplication.SectionGeneration{}, false, classify(err)
	}
	return generation, true, nil
}

func scanSectionGeneration(scanner stateScanner) (artifactapplication.SectionGeneration, error) {
	var generation artifactapplication.SectionGeneration
	var id, workspaceID, artifactID, sourceRevisionID, workflowRunID, nodeRunID string
	var status string
	var modelRunID, baseRevisionID, recordedRevisionID, contentHash *string
	var failureClass, errorCode, errorSummary *string
	if err := scanner.Scan(
		&id, &workspaceID, &artifactID, &sourceRevisionID,
		&generation.SourceRevisionNo, &generation.SourceArtifactVersion, &generation.SectionKey,
		&generation.IdempotencyKey, &generation.RequestHash, &workflowRunID, &nodeRunID, &status,
		&modelRunID, &baseRevisionID, &recordedRevisionID, &generation.RecordedArtifactVersion,
		&contentHash, &failureClass, &errorCode, &errorSummary,
		&generation.Version, &generation.CreatedAt, &generation.UpdatedAt, &generation.CompletedAt, &generation.TerminalAt,
	); err != nil {
		return artifactapplication.SectionGeneration{}, err
	}
	generation.ID, generation.WorkspaceID, generation.ArtifactID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(artifactID)
	generation.SourceRevisionID = foundation.ID(sourceRevisionID)
	generation.WorkflowRunID, generation.NodeRunID = foundation.ID(workflowRunID), foundation.ID(nodeRunID)
	generation.Status = artifactapplication.SectionGenerationStatus(status)
	if modelRunID != nil {
		value := foundation.ID(*modelRunID)
		generation.ModelRunID = &value
	}
	if baseRevisionID != nil {
		value := foundation.ID(*baseRevisionID)
		generation.RecordedBaseRevisionID = &value
	}
	if recordedRevisionID != nil {
		value := foundation.ID(*recordedRevisionID)
		generation.RecordedRevisionID = &value
	}
	if contentHash != nil {
		generation.ContentHash = *contentHash
	}
	if failureClass != nil {
		generation.FailureClass = *failureClass
	}
	if errorCode != nil {
		generation.ErrorCode = *errorCode
	}
	if errorSummary != nil {
		generation.ErrorSummary = *errorSummary
	}
	generation.CreatedAt = generation.CreatedAt.UTC()
	generation.UpdatedAt = generation.UpdatedAt.UTC()
	if generation.CompletedAt != nil {
		value := generation.CompletedAt.UTC()
		generation.CompletedAt = &value
	}
	if generation.TerminalAt != nil {
		value := generation.TerminalAt.UTC()
		generation.TerminalAt = &value
	}
	if err := artifactapplication.ValidateSectionGeneration(generation); err != nil {
		return artifactapplication.SectionGeneration{}, inconsistent(fmt.Errorf("decode artifact section generation: %w", err))
	}
	return generation, nil
}

func sameGenerationJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if decodeGenerationJSON(left, &leftValue) != nil || decodeGenerationJSON(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func decodeGenerationJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validDistinctGenerationIDs(values ...foundation.ID) bool {
	seen := make(map[foundation.ID]struct{}, len(values))
	for _, value := range values {
		if !validID(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func generationCapabilityError(err error) error {
	if err == nil {
		err = errors.New("artifact generation dependency is unavailable")
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, artifactworkflow.ErrorCodeCapabilityUnavailable, true, err)
}

func generationInputError(err error) error {
	if err == nil {
		err = errors.New("artifact generation input is invalid")
	}
	return foundation.NewError(foundation.ErrorInvalidInput, artifactworkflow.ErrorCodeInputInvalid, false, err)
}

func generationContextError(kind foundation.ErrorKind, err error) error {
	if err == nil {
		err = errors.New("artifact generation context is invalid")
	}
	return foundation.NewError(kind, artifactworkflow.ErrorCodeContextInvalid, false, err)
}

func generationOutputError(kind foundation.ErrorKind, err error) error {
	if err == nil {
		err = errors.New("artifact generation output is invalid")
	}
	return foundation.NewError(kind, artifactworkflow.ErrorCodeOutputInvalid, false, err)
}

func generationEvidenceError(err error) error {
	if err == nil {
		err = errors.New("artifact generation evidence is invalid")
	}
	return foundation.NewError(foundation.ErrorInvalidInput, artifactworkflow.ErrorCodeEvidenceInvalid, false, err)
}

func generationEvidenceVerificationError(err error) error {
	if err == nil {
		err = errors.New("artifact generation evidence verification failed")
	}
	kind := foundation.ErrorConsistencyViolation
	retryable := false
	var classified *foundation.Error
	if errors.As(err, &classified) {
		kind = classified.Kind
		retryable = classified.Retryable
	} else if errors.Is(err, context.Canceled) {
		kind = foundation.ErrorNonRetryableFailure
	} else if errors.Is(err, context.DeadlineExceeded) {
		kind = foundation.ErrorRetryableFailure
		retryable = true
	}
	return foundation.NewError(kind, artifactworkflow.ErrorCodeEvidenceInvalid, retryable, err)
}

func generationFinalizationUnknown(err error) error {
	if err == nil {
		err = errors.New("artifact generation finalization result is unknown")
	}
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, artifactworkflow.ErrorCodeFinalizationUnknown, false, err)
}

func generationDefinitionVersion() string {
	return strconv.FormatInt(artifactworkflow.DefinitionVersion, 10)
}

var _ artifactapplication.SectionGenerationStarter = (*SectionGenerationRepository)(nil)
var _ artifactworkflow.ContextLoader = (*SectionGenerationRepository)(nil)
var _ artifactworkflow.Finalizer = (*SectionGenerationRepository)(nil)
