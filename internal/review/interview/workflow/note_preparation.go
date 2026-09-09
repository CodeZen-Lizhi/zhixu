// Package workflow runs note preparation within the registered Workflow runtime.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type NotePreparationInput struct {
	PreparationID foundation.ID `json:"preparation_id"`
}
type NotePreparationReceipt struct {
	SchemaVersion int           `json:"schema_version"`
	PreparationID foundation.ID `json:"preparation_id"`
	SessionID     foundation.ID `json:"session_id"`
}

func NotePreparationDefinition() (workflowdomain.RegisteredDefinition, error) {
	definition := workflowdomain.RegisteredDefinition{
		Key: interviewapp.NotePreparationDefinitionKey, Version: interviewapp.NotePreparationDefinitionVersion,
		InputSchemaVersion: interviewapp.NotePreparationSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: interviewapp.NotePreparationNodeKey, Kind: interviewapp.NotePreparationNodeKind,
			InputSchemaVersion: interviewapp.NotePreparationSchemaVersion, OutputSchemaVersion: interviewapp.NotePreparationSchemaVersion,
			RetryPolicy:         workflowdomain.RetryPolicy{},
			RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal},
		}}},
	}
	var err error
	definition.GraphHash, err = workflowapp.ComputeCanonicalGraphHash(definition.Graph)
	return definition, err
}

func RegisterNotePreparationDefinition(registry *workflowapp.DefinitionRegistry) error {
	definition, err := NotePreparationDefinition()
	if err != nil {
		return err
	}
	return registry.Register(definition)
}

func DecodeNotePreparationInput(raw []byte) (NotePreparationInput, error) {
	return strictjson.DecodeObject[NotePreparationInput](raw, strictjson.Limits{MaxDocumentBytes: 256, MaxDepth: 2, MaxStringBytes: 64, MaxObjectFields: 1, MaxArrayItems: 1}, func(value NotePreparationInput) error {
		if id, err := foundation.ParseID(string(value.PreparationID)); err != nil || id != value.PreparationID {
			return domain.InvalidError(interviewapp.ErrorCodeNotePreparationInvalid, "note preparation Workflow input is invalid")
		}
		return nil
	})
}

type NotePreparationRunReader interface {
	GetRun(context.Context, foundation.ID) (workflowdomain.Run, error)
}
type NotePreparationExecutorDependencies struct {
	Runs  NotePreparationRunReader
	Store interviewapp.NoteGenerationStore
	Model interviewapp.NoteInterviewGenerator
	Clock foundation.Clock
}
type NotePreparationExecutor struct {
	dependencies NotePreparationExecutorDependencies
}

func NewNotePreparationExecutor(dependencies NotePreparationExecutorDependencies) (*NotePreparationExecutor, error) {
	if dependencies.Runs == nil || dependencies.Store == nil || dependencies.Model == nil || dependencies.Clock == nil {
		return nil, domain.UnavailableError(interviewapp.ErrorCodeNotePlanUnavailable, "note preparation Workflow dependencies are unavailable")
	}
	return &NotePreparationExecutor{dependencies: dependencies}, nil
}

func (executor *NotePreparationExecutor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	definition, err := NotePreparationDefinition()
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash || execution.NodeKey != interviewapp.NotePreparationNodeKey || execution.NodeKind != interviewapp.NotePreparationNodeKind || execution.InputSchemaVersion != interviewapp.NotePreparationSchemaVersion {
		return workflowapp.ExecutionResult{}, domain.InvalidError(interviewapp.ErrorCodeNotePreparationInvalid, "note preparation execution is not registered")
	}
	run, err := executor.dependencies.Runs.GetRun(ctx, execution.RunID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	input, err := DecodeNotePreparationInput(run.Input)
	if err != nil || run.WorkspaceID != execution.WorkspaceID || run.ID != execution.RunID || run.DefinitionID != execution.DefinitionID || run.IdempotencyKey != "note-interview:"+string(input.PreparationID) {
		return workflowapp.ExecutionResult{}, domain.InvalidError(interviewapp.ErrorCodeNotePreparationInvalid, "note preparation run binding is invalid")
	}
	preparation, err := executor.dependencies.Store.BeginNoteGeneration(ctx, input.PreparationID, execution)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	if preparation.Status != interviewapp.NotePreparationReady {
		preparation, err = executor.dependencies.Model.GenerateNoteInterview(ctx, execution, preparation)
		if err != nil {
			code, retryable, unknown := interviewapp.NoteFailure(err)
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			failure := executor.dependencies.Store.FailNoteGeneration(finalCtx, preparation, code, retryable || !unknown, unknown, executor.dependencies.Clock.Now())
			return workflowapp.ExecutionResult{}, errors.Join(err, failure)
		}
	}
	if preparation.Validate() != nil || preparation.ID != input.PreparationID || preparation.WorkspaceID != execution.WorkspaceID || preparation.Status != interviewapp.NotePreparationReady || preparation.SessionID == nil {
		return workflowapp.ExecutionResult{}, domain.InvalidError(interviewapp.ErrorCodeNotePreparationInvalid, "note preparation produced no durable session")
	}
	raw, err := json.Marshal(NotePreparationReceipt{SchemaVersion: interviewapp.NotePreparationSchemaVersion, PreparationID: preparation.ID, SessionID: *preparation.SessionID})
	return workflowapp.ExecutionResult{Output: raw}, err
}

var _ workflowapp.Executor = (*NotePreparationExecutor)(nil)
