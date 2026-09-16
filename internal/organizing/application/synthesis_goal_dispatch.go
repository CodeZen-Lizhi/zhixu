package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

type GoalSelectionPreparation struct {
	WorkspaceID, RequestID, BatchID foundation.ID
	BatchNo                         int64
}

type GoalSelectionDispatchStore interface {
	ListUnpreparedGoalSelectionBatches(context.Context, foundation.ID, int) ([]GoalSelectionPreparation, error)
	PrepareGoalSelections(context.Context, foundation.ID, foundation.ID, int64) ([]SynthesisGoalSelection, error)
	DeferGoalSelectionPreparation(context.Context, GoalSelectionPreparation, string) error
	ReconcileGoalSelectionWorkflows(context.Context, foundation.ID, int) (int, error)
	ClaimPendingGoalSelectionScoped(context.Context, foundation.TransactionScope, foundation.ID) (SynthesisGoalSelection, bool, error)
	BindGoalSelectionWorkflowScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, foundation.ID) error
	GetGoalSelection(context.Context, foundation.ID, foundation.ID) (SynthesisGoalSelection, error)
	FailScheduledGoalSelection(context.Context, foundation.ID, foundation.ID, foundation.ID, string, bool) (SynthesisGoalSelection, error)
}

type GoalSelectionStartInput struct {
	SelectionID     foundation.ID `json:"selection_id"`
	ExpectedVersion int64         `json:"expected_version"`
}

func DecodeGoalSelectionStartInput(raw []byte) (GoalSelectionStartInput, error) {
	input, err := strictjson.DecodeObject[GoalSelectionStartInput](raw, strictjson.Limits{MaxDocumentBytes: 1024, MaxDepth: 2, MaxStringBytes: 128, MaxArrayItems: 1, MaxObjectFields: 2}, nil)
	if err != nil || !validID(input.SelectionID) || input.ExpectedVersion < 1 {
		return GoalSelectionStartInput{}, goalSelectionInvalid()
	}
	return input, nil
}

type RetryGoalSelectionCommand struct {
	WorkspaceID, SelectionID foundation.ID
	ExpectedVersion          int64
	IdempotencyKey           string
}
