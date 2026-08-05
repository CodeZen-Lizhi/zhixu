package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestVersionedReferencesValidateCanonicalValues(t *testing.T) {
	refs := []interface{ Validate() error }{
		ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "gpt-test", ModelVersion: "2026-07-19"},
		ModelProfileRef{ID: "default", Version: "v1"},
		PromptRef{ID: "rag-answer", Version: "v1"},
		SchemaRef{ID: RAGAnswerSchemaID, Version: OutputSchemaVersionV1},
	}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			t.Fatalf("ref=%#v err=%v", ref, err)
		}
	}
	if err := (PromptRef{ID: " prompt", Version: "v1"}).Validate(); errorCode(err) != ErrorCodeReferenceInvalid {
		t.Fatalf("err=%v", err)
	}
	if err := (ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "gpt-test"}).Validate(); errorCode(err) != ErrorCodeReferenceInvalid {
		t.Fatalf("model without version err=%v", err)
	}
}

func TestValidationExhaustedErrorIsStableAndPreservesCause(t *testing.T) {
	cause := errors.New("redacted schema failure")
	err := NewValidationExhaustedError(cause)
	if errorCode(err) != ErrorCodeValidationExhausted || !errors.Is(err, cause) {
		t.Fatalf("err=%v", err)
	}
}

func TestTokenUsageRequiresExactNonOverflowingTotal(t *testing.T) {
	if err := (TokenUsage{InputTokens: 10, OutputTokens: 3, TotalTokens: 13}).Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []TokenUsage{
		{InputTokens: -1},
		{InputTokens: 1, OutputTokens: 2, TotalTokens: 2},
		{InputTokens: math.MaxInt64, OutputTokens: 1, TotalTokens: math.MinInt64},
	}
	for _, usage := range invalid {
		if err := usage.Validate(); errorCode(err) != ErrorCodeModelCallInvalid {
			t.Fatalf("usage=%#v err=%v", usage, err)
		}
	}
}

func TestModelRunValidationAndTerminalTransitions(t *testing.T) {
	run := validModelRun()
	if err := ValidateModelRun(run); err != nil {
		t.Fatalf("validate running run: %v", err)
	}
	for _, status := range []ModelRunStatus{ModelRunSucceeded, ModelRunRefused, ModelRunFailed, ModelRunUnknown} {
		if err := ValidateModelRunTransition(ModelRunRunning, status); err != nil {
			t.Fatalf("status=%s err=%v", status, err)
		}
	}
	if err := ValidateModelRunTransition(ModelRunSucceeded, ModelRunRunning); errorCode(err) != ErrorCodeModelRunTransitionInvalid {
		t.Fatalf("err=%v", err)
	}

	completed := run.UpdatedAt.Add(time.Second)
	run.Status = ModelRunSucceeded
	run.FinalResultType = ResultTypeRAGAnswer
	run.CompletedAt = &completed
	run.UpdatedAt = completed
	if err := ValidateModelRun(run); err != nil {
		t.Fatal(err)
	}
	run.FinalErrorCode = "FAKE_FAILURE"
	if err := ValidateModelRun(run); errorCode(err) != ErrorCodeModelRunInvalid {
		t.Fatalf("successful run with error err=%v", err)
	}
	run.FinalErrorCode = ""
	run.FinalResultType = ""
	if err := ValidateModelRun(run); errorCode(err) != ErrorCodeModelRunInvalid {
		t.Fatalf("successful run without result type err=%v", err)
	}
	run.Status = ModelRunRefused
	run.FinalResultType = ResultTypeRefusal
	run.FinalErrorCode = string(RefusalEvidenceInsufficient)
	if err := ValidateModelRun(run); err != nil {
		t.Fatalf("valid refusal run err=%v", err)
	}
	run.FinalResultType = ResultTypeRAGAnswer
	if err := ValidateModelRun(run); errorCode(err) != ErrorCodeModelRunInvalid {
		t.Fatalf("refusal claiming answer err=%v", err)
	}
}

func TestSuccessfulModelRunAcceptsIndependentToolRequestResultType(t *testing.T) {
	run := validModelRun()
	completed := run.UpdatedAt.Add(time.Second)
	run.Schema = SchemaRef{ID: ToolRequestSchemaID, Version: OutputSchemaVersionV1}
	run.ReducedSchema = run.Schema
	run.Status = ModelRunSucceeded
	run.FinalResultType = ResultTypeToolRequest
	run.CompletedAt = &completed
	run.UpdatedAt = completed
	if err := ValidateModelRun(run); err != nil {
		t.Fatalf("tool request model run rejected: %v", err)
	}
}

func TestSuccessfulModelRunAcceptsArtifactSectionResultType(t *testing.T) {
	run := validModelRun()
	now := run.UpdatedAt.Add(time.Second)
	run.Status = ModelRunSucceeded
	run.FinalResultType = ResultTypeArtifactSection
	run.Version++
	run.UpdatedAt = now
	run.CompletedAt = &now
	if err := ValidateModelRun(run); err != nil {
		t.Fatalf("ValidateModelRun(artifact section) error = %v", err)
	}
}

func TestPlanCallAndClarificationResultRemainAdditive(t *testing.T) {
	call := validStartedCall()
	call.Phase = ModelCallPlan
	if err := ValidateModelCall(call); err != nil {
		t.Fatalf("plan model call rejected: %v", err)
	}

	run := validModelRun()
	completed := run.UpdatedAt.Add(time.Second)
	run.Status = ModelRunSucceeded
	run.FinalResultType = ResultTypeClarification
	run.CompletedAt = &completed
	run.UpdatedAt = completed
	if err := ValidateModelRun(run); err != nil {
		t.Fatalf("clarification model run rejected: %v", err)
	}
}

func TestRAGModelRunMayBindRetrievalWhenItFinalizes(t *testing.T) {
	run := validModelRun()
	run.Schema.Version = OutputSchemaVersionV2
	run.Retrieval = RetrievalRef{}
	if err := ValidateModelRun(run); err != nil {
		t.Fatalf("running rag run without retrieval rejected: %v", err)
	}

	completed := run.UpdatedAt.Add(time.Second)
	run.Status = ModelRunSucceeded
	run.FinalResultType = ResultTypeRAGAnswer
	run.Retrieval = RetrievalRef{IndexVersionID: testIndexVersionID}
	run.CompletedAt = &completed
	run.UpdatedAt = completed
	if err := ValidateModelRun(run); err != nil {
		t.Fatalf("terminal rag answer with retrieval rejected: %v", err)
	}

	run.Retrieval = RetrievalRef{}
	if err := ValidateModelRun(run); errorCode(err) != ErrorCodeModelRunInvalid {
		t.Fatalf("terminal rag answer without retrieval err=%v", err)
	}
}

func TestOnlyRunningRAGModelRunMayOmitRetrieval(t *testing.T) {
	run := validModelRun()
	run.Retrieval = RetrievalRef{}
	run.Schema = SchemaRef{ID: RelationAssessmentSchemaID, Version: OutputSchemaVersionV1}
	if err := ValidateModelRun(run); errorCode(err) != ErrorCodeModelRunInvalid {
		t.Fatalf("relation run without retrieval err=%v", err)
	}
}

func TestOrganizingModelRunMayOmitRetrievalWithoutWeakeningOtherSchemas(t *testing.T) {
	for _, schemaID := range []string{OrganizingOutlineSchemaID, OrganizingDocumentSchemaID} {
		run := validModelRun()
		run.Schema = SchemaRef{ID: schemaID, Version: "v1"}
		run.ReducedSchema = run.Schema
		run.Retrieval = RetrievalRef{}
		if err := ValidateModelRun(run); err != nil {
			t.Fatalf("running organizing run %s rejected: %v", schemaID, err)
		}
		completed := run.UpdatedAt.Add(time.Second)
		run.Status = ModelRunSucceeded
		run.FinalResultType = ResultTypeOrganizingDocument
		if schemaID == OrganizingOutlineSchemaID {
			run.FinalResultType = ResultTypeOrganizingOutline
		}
		run.CompletedAt = &completed
		run.UpdatedAt = completed
		if err := ValidateModelRun(run); err != nil {
			t.Fatalf("terminal organizing run %s rejected: %v", schemaID, err)
		}
	}

	run := validModelRun()
	run.Schema = SchemaRef{ID: RelationAssessmentSchemaID, Version: OutputSchemaVersionV1}
	run.ReducedSchema = run.Schema
	run.Retrieval = RetrievalRef{}
	if err := ValidateModelRun(run); errorCode(err) != ErrorCodeModelRunInvalid {
		t.Fatalf("unrelated schema without retrieval err=%v", err)
	}
}

func TestModelCallValidationPreservesUnknownOutcome(t *testing.T) {
	call := validStartedCall()
	if err := ValidateModelCall(call); err != nil {
		t.Fatalf("validate started call: %v", err)
	}
	for _, status := range []ModelCallStatus{ModelCallSucceeded, ModelCallFailed, ModelCallUnknown} {
		if err := ValidateModelCallTransition(ModelCallStarted, status); err != nil {
			t.Fatalf("status=%s err=%v", status, err)
		}
	}

	completed := call.StartedAt.Add(25 * time.Millisecond)
	call.Status = ModelCallUnknown
	call.CompletedAt = &completed
	call.LatencyMillis = 25
	call.ErrorCode = "MODEL_CALL_OUTCOME_UNKNOWN"
	if err := ValidateModelCall(call); err != nil {
		t.Fatal(err)
	}
	call.ResponseHash = strings.Repeat("b", 64)
	if err := ValidateModelCall(call); errorCode(err) != ErrorCodeModelCallInvalid {
		t.Fatalf("unknown response err=%v", err)
	}
}

func TestModelCallRequiresActualRuntimeReferencesAndOutputLimit(t *testing.T) {
	call := validStartedCall()
	if err := ValidateModelCall(call); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*ModelCall)
	}{
		{name: "model", mutate: func(call *ModelCall) { call.Model.ModelVersion = "" }},
		{name: "profile", mutate: func(call *ModelCall) { call.Profile.Version = "" }},
		{name: "prompt", mutate: func(call *ModelCall) { call.Prompt.Version = "" }},
		{name: "schema", mutate: func(call *ModelCall) { call.Schema.Version = "" }},
		{name: "zero output limit", mutate: func(call *ModelCall) { call.MaxOutputTokens = 0 }},
		{name: "oversized output limit", mutate: func(call *ModelCall) { call.MaxOutputTokens = MaxModelCallOutputTokens + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalidCall := call
			test.mutate(&invalidCall)
			if err := ValidateModelCall(invalidCall); errorCode(err) != ErrorCodeModelCallInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestSuccessfulModelCallRequiresHashUsageAndMetrics(t *testing.T) {
	call := validStartedCall()
	completed := call.StartedAt.Add(15 * time.Millisecond)
	call.Status = ModelCallSucceeded
	call.CompletedAt = &completed
	call.ResponseHash = strings.Repeat("b", 64)
	call.ResponseBytes = 80
	call.Usage = TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	call.LatencyMillis = 15
	if err := ValidateModelCall(call); err != nil {
		t.Fatal(err)
	}
	call.ResponseHash = "BAD"
	if err := ValidateModelCall(call); errorCode(err) != ErrorCodeModelCallInvalid {
		t.Fatalf("err=%v", err)
	}
}

func TestFailedModelCallResponseMetadataIsAtomic(t *testing.T) {
	call := validStartedCall()
	completed := call.StartedAt.Add(time.Millisecond)
	call.Status = ModelCallFailed
	call.CompletedAt = &completed
	call.LatencyMillis = 1
	call.ErrorCode = "MODEL_PROVIDER_REJECTED"
	call.ResponseBytes = 25
	if err := ValidateModelCall(call); errorCode(err) != ErrorCodeModelCallInvalid {
		t.Fatalf("bytes without hash err=%v", err)
	}
	call.ResponseHash = strings.Repeat("b", 64)
	if err := ValidateModelCall(call); err != nil {
		t.Fatalf("failed call with bounded response metadata err=%v", err)
	}
}

func validModelRun() ModelRun {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	return ModelRun{
		ID: testModelRunID, WorkspaceID: testWorkspaceID,
		WorkflowRunID: "20000000-0000-4000-8000-000000000001",
		NodeRunID:     "20000000-0000-4000-8000-000000000002",
		NodeAttemptID: "20000000-0000-4000-8000-000000000003",
		Model:         ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "gpt-test", ModelVersion: "2026-07-19"},
		Profile:       ModelProfileRef{ID: "default", Version: "v1"},
		Prompt:        PromptRef{ID: "rag-answer", Version: "v1"},
		Schema:        SchemaRef{ID: RAGAnswerSchemaID, Version: OutputSchemaVersionV1},
		ReducedSchema: SchemaRef{ID: RefusalSchemaID, Version: OutputSchemaVersionV1},
		Retrieval:     RetrievalRef{IndexVersionID: testIndexVersionID},
		Status:        ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func validStartedCall() ModelCall {
	return ModelCall{
		ID:              foundation.ID("30000000-0000-4000-8000-000000000001"),
		ModelRunID:      testModelRunID,
		CallNo:          1,
		Phase:           ModelCallInitial,
		Model:           ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "gpt-test", ModelVersion: "2026-07-19"},
		Profile:         ModelProfileRef{ID: "default", Version: "v1"},
		Prompt:          PromptRef{ID: "rag-answer", Version: "v1"},
		Schema:          SchemaRef{ID: RAGAnswerSchemaID, Version: OutputSchemaVersionV1},
		MaxOutputTokens: 1024,
		Status:          ModelCallStarted,
		RequestHash:     strings.Repeat("a", 64),
		RequestBytes:    100,
		Usage:           TokenUsage{},
		Version:         1,
		StartedAt:       time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	}
}
