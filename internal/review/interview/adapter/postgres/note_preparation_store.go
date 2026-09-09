package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	interviewworkflow "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/workflow"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type NotePreparationDependencies struct {
	IDs       foundation.IDGenerator
	Clock     foundation.Clock
	ModelRuns agentapp.ScopedModelRunStore
	Fence     workflowapp.ScopedToolExecutionPolicySnapshot
	Bindings  workflowapp.ScopedRuntimeBindingReader
}

type GORMNotePreparationRepository struct {
	database     *gorm.DB
	uow          foundation.UnitOfWork
	dependencies NotePreparationDependencies
	definition   workflowdomain.RegisteredDefinition
	mu           sync.RWMutex
	starter      workflowapp.ScopedRuntimeStarter
}

func NewGORMNotePreparationRepository(pool *platformpostgres.Pool, dependencies NotePreparationDependencies) (*GORMNotePreparationRepository, error) {
	for _, port := range []any{pool, dependencies.IDs, dependencies.Clock, dependencies.ModelRuns, dependencies.Fence, dependencies.Bindings} {
		if nilGORMInterviewDependency(port) {
			return nil, gormInterviewUnavailable(errors.New("note preparation dependencies are unavailable"))
		}
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	definition, err := interviewworkflow.NotePreparationDefinition()
	if err != nil {
		return nil, err
	}
	return &GORMNotePreparationRepository{database: database, uow: uow, dependencies: dependencies, definition: definition}, nil
}

// BindWorkflowStarter closes the composition cycle once, before serving API
// traffic. All participants must have been constructed from the same Pool.
func (repository *GORMNotePreparationRepository) BindWorkflowStarter(starter workflowapp.ScopedRuntimeStarter) error {
	if repository == nil || nilGORMInterviewDependency(starter) {
		return gormInterviewUnavailable(errors.New("note preparation starter is unavailable"))
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.starter != nil {
		return notePreparationConflict("note preparation starter is already bound")
	}
	repository.starter = starter
	return nil
}

func (repository *GORMNotePreparationRepository) within(ctx context.Context, fn func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	if repository == nil || ctx == nil || repository.database == nil || repository.uow == nil {
		return gormInterviewUnavailable(errors.New("note preparation repository is unavailable"))
	}
	err := repository.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return fn(ctx, scope, tx.WithContext(ctx))
	})
	return gormInterviewClassify(ctx, err, interviewapp.ErrorCodeNotePlanUnavailable)
}

func (repository *GORMNotePreparationRepository) FindNotePreparationReplay(ctx context.Context, workspaceID foundation.ID, key, hash string) (interviewapp.NotePreparationResult, bool, error) {
	row, found, err := findNotePreparation(repository.database.WithContext(ctx), workspaceID, key)
	if err != nil || !found {
		return interviewapp.NotePreparationResult{}, found, err
	}
	if row.RequestHash != hash {
		return interviewapp.NotePreparationResult{}, false, notePreparationConflict("note preparation key was reused with different input")
	}
	preparation, err := row.value()
	return interviewapp.NotePreparationResult{Preparation: preparation, Replayed: true}, true, err
}

func (repository *GORMNotePreparationRepository) CreateNotePreparation(ctx context.Context, requested interviewapp.NotePreparation) (interviewapp.NotePreparationResult, error) {
	repository.mu.RLock()
	starter := repository.starter
	repository.mu.RUnlock()
	if nilGORMInterviewDependency(starter) {
		return interviewapp.NotePreparationResult{}, gormInterviewUnavailable(errors.New("note preparation Workflow starter is unavailable"))
	}
	var result interviewapp.NotePreparationResult
	err := repository.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		// Serialize the exact workspace/key before generating a Workflow run.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "note-interview:"+string(requested.WorkspaceID)+":"+requested.IdempotencyKey).Error; err != nil {
			return err
		}
		row, found, err := findNotePreparation(tx, requested.WorkspaceID, requested.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if row.RequestHash != requested.RequestHash {
				return notePreparationConflict("note preparation key was reused with different input")
			}
			result.Preparation, err = row.value()
			result.Replayed = true
			return err
		}
		if requested.RetryOf != nil {
			previous, err := loadNotePreparation(tx, requested.WorkspaceID, *requested.RetryOf, true)
			if err != nil {
				return err
			}
			old, err := previous.value()
			if err != nil {
				return err
			}
			if !old.RetryAllowed() || old.NoteRevision != requested.NoteRevision || old.Options != requested.Options {
				return notePreparationConflict("note preparation retry changed its frozen request")
			}
			var count int64
			if err := tx.Model(&notePreparationModel{}).Where("workspace_id=? AND retry_of=?", string(requested.WorkspaceID), string(*requested.RetryOf)).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return notePreparationConflict("note preparation already has an explicit retry")
			}
		}
		input, err := json.Marshal(interviewworkflow.NotePreparationInput{PreparationID: requested.ID})
		if err != nil {
			return err
		}
		start, err := workflowapp.BuildRuntimeStartRequest(repository.dependencies.IDs, repository.dependencies.Clock, requested.WorkspaceID, "note-interview:"+string(requested.ID), input, repository.definition)
		if err != nil {
			return err
		}
		started, err := starter.StartScoped(ctx, scope, start)
		if err != nil {
			return err
		}
		requested.WorkflowRunID, requested.NodeRunID = started.Run.ID, started.FirstNode.ID
		requested.CreatedAt = requested.CreatedAt.UTC().Truncate(time.Microsecond)
		requested.UpdatedAt = requested.CreatedAt
		if err := requested.Validate(); err != nil {
			return err
		}
		if err := repository.checkBinding(ctx, scope, requested, ""); err != nil {
			return err
		}
		row, err = notePreparationRow(requested)
		if err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		result.Preparation = requested
		return nil
	})
	if err != nil {
		// The failed call may have lost a successful commit response.
		replay, found, lookupErr := repository.FindNotePreparationReplay(ctx, requested.WorkspaceID, requested.IdempotencyKey, requested.RequestHash)
		if lookupErr == nil && found {
			return replay, nil
		}
		return result, err
	}
	return result, nil
}

func (repository *GORMNotePreparationRepository) GetNotePreparation(ctx context.Context, workspaceID, id foundation.ID) (interviewapp.NotePreparation, error) {
	row, err := loadNotePreparation(repository.database.WithContext(ctx), workspaceID, id, false)
	if err != nil {
		return interviewapp.NotePreparation{}, err
	}
	return row.value()
}

func (repository *GORMNotePreparationRepository) ListNotePreparations(ctx context.Context, workspaceID, noteID foundation.ID) ([]interviewapp.NotePreparation, error) {
	var rows []notePreparationModel
	if err := repository.database.WithContext(ctx).Where("workspace_id=? AND note_id=?", string(workspaceID), string(noteID)).Order("created_at DESC,id DESC").Limit(20).Find(&rows).Error; err != nil {
		return nil, gormInterviewClassify(ctx, err, interviewapp.ErrorCodeNotePlanUnavailable)
	}
	values := make([]interviewapp.NotePreparation, 0, len(rows))
	for _, row := range rows {
		value, err := row.value()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func findNotePreparation(tx *gorm.DB, workspaceID foundation.ID, key string) (notePreparationModel, bool, error) {
	var row notePreparationModel
	err := tx.Where("workspace_id=? AND idempotency_key=?", string(workspaceID), key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	return row, err == nil, err
}
func loadNotePreparation(tx *gorm.DB, workspaceID, id foundation.ID, lock bool) (notePreparationModel, error) {
	var row notePreparationModel
	query := tx.Where("workspace_id=? AND id=?", string(workspaceID), string(id))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, domain.NotFoundError(interviewapp.ErrorCodeNotePreparationNotFound, "note interview preparation was not found")
	}
	return row, err
}

func (repository *GORMNotePreparationRepository) checkBinding(ctx context.Context, scope foundation.TransactionScope, preparation interviewapp.NotePreparation, attemptID foundation.ID) error {
	binding, err := repository.dependencies.Bindings.LoadRuntimeBindingScoped(ctx, scope, workflowapp.ScopedRuntimeBindingQuery{WorkspaceID: preparation.WorkspaceID, WorkflowRunID: preparation.WorkflowRunID, NodeRunID: preparation.NodeRunID, NodeAttemptID: attemptID})
	if err != nil {
		return err
	}
	input, err := interviewworkflow.DecodeNotePreparationInput(binding.RunInput)
	nodeInput, nodeErr := interviewworkflow.DecodeNotePreparationInput(binding.NodeInput)
	graph, graphErr := workflowapp.DecodeCanonicalGraph(binding.DefinitionGraph)
	hash, hashErr := workflowapp.ComputeCanonicalGraphHash(graph)
	if err != nil || nodeErr != nil || input != nodeInput || input.PreparationID != preparation.ID || binding.DefinitionKey != repository.definition.Key || binding.DefinitionVersion != repository.definition.Version ||
		graphErr != nil || hashErr != nil || hash != repository.definition.GraphHash || binding.NodeKey != interviewapp.NotePreparationNodeKey || binding.NodeType != interviewapp.NotePreparationNodeKind ||
		binding.InputSchemaVersion != interviewapp.NotePreparationSchemaVersion || binding.OutputSchemaVersion != interviewapp.NotePreparationSchemaVersion || attemptID != "" && !binding.AttemptFound {
		return notePreparationConflict("note preparation Workflow binding is invalid")
	}
	return nil
}

func (repository *GORMNotePreparationRepository) lockLive(ctx context.Context, scope foundation.TransactionScope, p interviewapp.NotePreparation, attemptID foundation.ID) error {
	facts, found, err := repository.dependencies.Fence.LockToolExecutionPolicyScoped(ctx, scope, workflowapp.ToolExecutionPolicySnapshotRequest{WorkspaceID: p.WorkspaceID, WorkflowRunID: p.WorkflowRunID, NodeRunID: p.NodeRunID, NodeAttemptID: attemptID})
	if err != nil {
		return err
	}
	if !found || facts.DefinitionKey != repository.definition.Key || facts.DefinitionVersion != repository.definition.Version || facts.WorkflowStatus != workflowdomain.RunStatusRunning || facts.PauseRequested || facts.CancelRequested ||
		facts.NodeStatus != workflowdomain.NodeStatusRunning || facts.AttemptStatus != workflowdomain.AttemptStatusRunning || !facts.NodeLeaseOwnerSet || !facts.AttemptLeaseOwnerSet ||
		facts.NodeLeaseOwner != facts.AttemptLeaseOwner || !facts.NodeLeaseUntilSet || !facts.AttemptLeaseUntilSet || facts.NodeAttempt != facts.AttemptNo ||
		facts.DatabaseNow.IsZero() || !facts.DatabaseNow.Before(facts.NodeLeaseUntil) || !facts.DatabaseNow.Before(facts.AttemptLeaseUntil) {
		return notePreparationConflict("note preparation execution lease is no longer current")
	}
	return repository.checkBinding(ctx, scope, p, attemptID)
}

func notePreparationConflict(message string) error {
	return domain.ConflictError(interviewapp.ErrorCodeNotePreparationConflict, message)
}

var _ interviewapp.NoteGenerationStore = (*GORMNotePreparationRepository)(nil)
