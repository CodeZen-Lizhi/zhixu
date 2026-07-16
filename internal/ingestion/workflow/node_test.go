package ingestionworkflow

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

func TestNodeExecutesWithWorkflowIdentity(t *testing.T) {
	processor := &fakeProcessor{result: application.ProcessResult{Attempt: domain.AttemptRecord{Attempt: domain.Attempt{
		ID: "82000000-0000-4000-8000-000000000001", AttemptNumber: 2, Status: domain.AttemptChunked, SecurityStatus: domain.SecurityPassed,
	}, ParseProjectionID: idPointer("82000000-0000-4000-8000-000000000002")}, Projection: domain.ProjectionResult{Chunks: []domain.CanonicalChunk{{}}}}}
	node, err := NewNode(processor)
	if err != nil {
		t.Fatal(err)
	}
	output, err := node.Execute(context.Background(), Input{SourceVersionID: "82000000-0000-4000-8000-000000000003", WorkflowRunID: "82000000-0000-4000-8000-000000000004", IdempotencyKey: "node-1", AttemptNumber: 2})
	if err != nil || output.Status != string(domain.AttemptChunked) || output.ChunkCount != 1 || processor.request.WorkflowRunID == nil || *processor.request.WorkflowRunID != "82000000-0000-4000-8000-000000000004" {
		t.Fatalf("output/request = %#v / %#v, err=%v", output, processor.request, err)
	}
}

type fakeProcessor struct {
	result  application.ProcessResult
	request application.ProcessRequest
}

func (f *fakeProcessor) Process(_ context.Context, request application.ProcessRequest) (application.ProcessResult, error) {
	f.request = request
	return f.result, nil
}

func idPointer(value string) *foundation.ID {
	id := foundation.ID(value)
	return &id
}
