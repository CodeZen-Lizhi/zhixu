// Package ingestionworkflow adapts the Ingestion application service to a
// durable Workflow node without making Workflow state the ingestion source of
// truth.
package ingestionworkflow

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
)

// Processor is the stable application seam used by a Workflow worker.
type Processor interface {
	Process(context.Context, application.ProcessRequest) (application.ProcessResult, error)
}

// Node executes one deterministic Ingestion attempt. Lease and retry state
// remain owned by Workflow; this node only performs the domain operation.
type Node struct{ processor Processor }

// NewNode validates the worker dependency before registration in a composition root.
func NewNode(processor Processor) (*Node, error) {
	if processor == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_WORKFLOW_NODE_UNAVAILABLE", false, errors.New("ingestion processor is nil"))
	}
	return &Node{processor: processor}, nil
}

// Input identifies one idempotent Workflow node invocation.
type Input struct {
	SourceVersionID foundation.ID
	WorkflowRunID   foundation.ID
	IdempotencyKey  string
	AttemptNumber   int32
}

// Output is a compact, auditable node result; source content is not copied
// into Workflow output.
type Output struct {
	AttemptID     foundation.ID
	AttemptNumber int32
	Status        string
	Security      string
	ProjectionID  foundation.ID
	ChunkCount    int
}

// Execute runs the application service and returns only persisted identities.
func (n *Node) Execute(ctx context.Context, input Input) (Output, error) {
	if n == nil || n.processor == nil {
		return Output{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_WORKFLOW_NODE_UNAVAILABLE", false, errors.New("ingestion node is unavailable"))
	}
	if input.SourceVersionID == "" || input.WorkflowRunID == "" || strings.TrimSpace(input.IdempotencyKey) == "" || input.AttemptNumber <= 0 {
		return Output{}, foundation.NewError(foundation.ErrorInvalidInput, "INGESTION_WORKFLOW_INPUT_INVALID", false, errors.New("workflow ingestion input is incomplete"))
	}
	workflowRunID := input.WorkflowRunID
	result, err := n.processor.Process(ctx, application.ProcessRequest{
		SourceVersionID: input.SourceVersionID, WorkflowRunID: &workflowRunID,
		IdempotencyKey: input.IdempotencyKey, AttemptNumber: input.AttemptNumber,
	})
	if err != nil {
		return Output{}, err
	}
	output := Output{AttemptID: result.Attempt.ID, AttemptNumber: result.Attempt.AttemptNumber, Status: string(result.Attempt.Status), Security: string(result.Attempt.SecurityStatus), ChunkCount: len(result.Projection.Chunks)}
	if result.Attempt.ParseProjectionID != nil {
		output.ProjectionID = *result.Attempt.ParseProjectionID
	}
	return output, nil
}
