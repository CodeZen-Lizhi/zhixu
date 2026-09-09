// Package synthesispostgres persists automatic synthesis execution facts in the
// shared GORM pool. It does not own a scheduler, connection pool or model client.
package synthesispostgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"gorm.io/gorm"
)

type Dependencies struct {
	ModelRuns        agentapp.ScopedModelRunFinalizer
	WorkflowFence    workflowapp.ScopedWorkspaceAnalysisExecutionFence
	WorkflowBindings workflowapp.ScopedRuntimeBindingReader
}

type Store struct {
	database       *gorm.DB
	unitOfWork     foundation.UnitOfWork
	dependencies   Dependencies
	definitionHash string
}

func NewStore(pool *platformpostgres.Pool, dependencies Dependencies) (*Store, error) {
	if pool == nil || nilDependency(dependencies.ModelRuns) || nilDependency(dependencies.WorkflowFence) || nilDependency(dependencies.WorkflowBindings) {
		return nil, unavailable(errors.New("synthesis execution store dependencies are incomplete"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(err)
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(err)
	}
	definitionHash, err := workflowapp.ComputeCanonicalGraphHash(organizingworkflow.SynthesisRegisteredDefinitions()[0].Graph)
	if err != nil {
		return nil, err
	}
	return &Store{database: database, unitOfWork: unitOfWork, dependencies: dependencies, definitionHash: definitionHash}, nil
}

func (store *Store) ready(ctx context.Context) error {
	if store == nil || store.database == nil || nilDependency(store.unitOfWork) || ctx == nil {
		return unavailable(errors.New("synthesis execution store is unavailable"))
	}
	return nil
}

func (store *Store) transaction(ctx context.Context, scope foundation.TransactionScope) (*gorm.DB, error) {
	if err := store.ready(ctx); err != nil {
		return nil, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, unavailable(err)
	}
	return tx.WithContext(ctx), nil
}

func (store *Store) within(ctx context.Context, operation func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	if err := store.ready(ctx); err != nil {
		return err
	}
	err := store.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := store.transaction(ctx, scope)
		if err != nil {
			return err
		}
		return operation(ctx, scope, tx)
	})
	return classify(ctx, err)
}

// lockLive uses the Workflow owner's existing ordered Run/Node/Attempt lock.
// That port is named for its first consumer but contains no analysis-specific
// allowlist. This owner verifies the exact synthesis definition and live lease.
func (store *Store) lockLive(ctx context.Context, scope foundation.TransactionScope, workspaceID, runID, nodeID, attemptID foundation.ID, kind string) error {
	if !validID(workspaceID) || !validID(runID) || !validID(nodeID) || !validID(attemptID) {
		return invalid("synthesis execution identity is invalid")
	}
	snapshot, found, err := store.dependencies.WorkflowFence.LockWorkspaceAnalysisExecutionScoped(ctx, scope,
		workflowapp.WorkspaceAnalysisExecutionFenceRequest{WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, NodeAttemptID: attemptID})
	if err != nil {
		return err
	}
	if !found || snapshot.DefinitionKey != organizingworkflow.SynthesisDefinitionKey || snapshot.DefinitionVersion != organizingworkflow.SynthesisDefinitionVersion ||
		snapshot.NodeKey != kind || snapshot.WorkflowStatus != workflowdomain.RunStatusRunning || snapshot.PauseRequested || snapshot.CancelRequested ||
		snapshot.NodeStatus != workflowdomain.NodeStatusRunning || snapshot.AttemptStatus != workflowdomain.AttemptStatusRunning ||
		!snapshot.NodeLeaseOwnerSet || !snapshot.AttemptLeaseOwnerSet || !snapshot.NodeLeaseUntilSet || !snapshot.AttemptLeaseUntilSet ||
		snapshot.NodeLeaseOwner != snapshot.AttemptLeaseOwner || snapshot.NodeAttempt != snapshot.AttemptNo {
		return conflict("synthesis Workflow execution lease is no longer current")
	}
	graph, err := workflowapp.DecodeCanonicalGraph([]byte(snapshot.DefinitionGraph))
	if err != nil {
		return invalid("synthesis Workflow graph is invalid")
	}
	hash, err := workflowapp.ComputeCanonicalGraphHash(graph)
	if err != nil || hash != store.definitionHash {
		return invalid("synthesis Workflow graph differs from its registered definition")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return err
	}
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return classify(ctx, err)
	}
	if !now.Before(snapshot.NodeLeaseUntil) || !now.Before(snapshot.AttemptLeaseUntil) {
		return conflict("synthesis Workflow execution lease expired")
	}
	return nil
}

func (store *Store) bindLive(ctx context.Context, scope foundation.TransactionScope, processingID foundation.ID, execution workflowapp.ExecutionContext) (*gorm.DB, executionModel, error) {
	if err := store.lockLive(ctx, scope, execution.WorkspaceID, execution.RunID, execution.NodeRunID, execution.NodeAttemptID, execution.NodeKind); err != nil {
		return nil, executionModel{}, err
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return nil, executionModel{}, err
	}
	processing, err := loadProcessing(tx, execution.WorkspaceID, processingID, true)
	if err != nil {
		return nil, executionModel{}, err
	}
	if processing.WorkflowRunID == nil || *processing.WorkflowRunID != string(execution.RunID) || (processing.Status != string(organizingapp.SynthesisProcessingPending) && processing.Status != string(organizingapp.SynthesisProcessingRunning)) {
		return nil, executionModel{}, conflict("synthesis processing no longer owns this Workflow execution")
	}
	row, err := loadExecution(tx, execution.WorkspaceID, processingID, execution.RunID, true)
	return tx, row, err
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}
func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}
func nilDependency(value any) bool {
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
func canonical(value time.Time) time.Time { return value.UTC().Truncate(time.Microsecond) }
func optionalID(value foundation.ID) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}
func idValue(value *string) foundation.ID {
	if value == nil {
		return ""
	}
	return foundation.ID(*value)
}
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func timePointer(value time.Time) *time.Time { value = canonical(value); return &value }
func marshal(value any) (jsonValue, error) {
	raw, err := json.Marshal(value)
	return jsonValue(raw), err
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, organizingworkflow.ErrorCodeSynthesisExecutionInvalid, false, errors.New(message))
}
func conflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, organizingworkflow.ErrorCodeSynthesisExecutionConflict, false, errors.New(message))
}
func recovery(message string) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, organizingapp.ErrorCodeSynthesisModelReplayUnsafe, false, errors.New(message))
}
func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, organizingworkflow.ErrorCodeSynthesisExecutionUnavailable, true, err)
}
func notFound() error {
	return foundation.NewError(foundation.ErrorNotFound, organizingworkflow.ErrorCodeSynthesisProcessingNotFound, false, errors.New("synthesis processing was not found"))
}
func classify(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		err = errors.Join(err, ctx.Err(), context.Cause(ctx))
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, organizingworkflow.ErrorCodeSynthesisExecutionUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, organizingworkflow.ErrorCodeSynthesisExecutionUnavailable, true, err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound()
	}
	switch platformpostgres.SQLState(err) {
	case "40001", "40P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, organizingworkflow.ErrorCodeSynthesisExecutionConflict, true, err)
	case "23505":
		return conflict("synthesis execution identity is already bound")
	case "23503", "23514", "55000":
		return invalid("synthesis persisted binding or transition was rejected")
	}
	return unavailable(err)
}

func processingSourceHash(key string) string { return strings.TrimPrefix(key, "synthesis:") }
