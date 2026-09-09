package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const NoteInterviewPromptID = "interview.synthesis-note-plan"

type NoteInterviewGenerator interface {
	GenerateNoteInterview(context.Context, workflowapp.ExecutionContext, NotePreparation) (NotePreparation, error)
}

func NoteModelPromptRef() agentdomain.PromptRef {
	return agentdomain.PromptRef{ID: NoteInterviewPromptID, Version: "1"}
}

func NoteModelSchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: agentdomain.SynthesisNoteInterviewPlanSchemaID, Version: "1"}
}

func NoteOutputHash(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// ValidateNoteModelBinding checks the immutable recorded runtime, independently
// of whether generation has already reached its terminal state.
func ValidateNoteModelBinding(preparation NotePreparation, run agentdomain.ModelRun) error {
	if agentdomain.ValidateModelRun(run) != nil || run.WorkspaceID != preparation.WorkspaceID || run.WorkflowRunID != preparation.WorkflowRunID ||
		run.NodeRunID != preparation.NodeRunID || run.NodeAttemptID != preparation.NodeAttemptID ||
		!reflect.DeepEqual(run.ModelSettingsRevision, preparation.ModelSettingsRevision) ||
		run.Prompt != NoteModelPromptRef() || run.Schema != NoteModelSchemaRef() || run.ReducedSchema != NoteModelSchemaRef() ||
		run.Retrieval.IsBound() || run.MemoryContext.IsBound() || preparation.ModelRunID != "" && run.ID != preparation.ModelRunID {
		return domain.InvalidError(ErrorCodeNotePlanInvalid, "note interview model run changed its frozen binding")
	}
	return nil
}

// ValidateNoteModelOutput requires real, journaled Provider responses. A
// deterministic fallback or an unjournaled JSON document cannot start a session.
func ValidateNoteModelOutput(preparation NotePreparation, record agentapp.ModelRunRecord, raw []byte) error {
	if ValidateNoteModelBinding(preparation, record.Run) != nil || len(record.Calls) < 1 || len(record.Calls) > agentapp.StructuredCallLimit {
		return domain.InvalidError(ErrorCodeNotePlanInvalid, "note interview model call proof is missing")
	}
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	for index, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != record.Run.ID ||
			call.CallNo != index+1 || call.Phase != phases[index] || call.Status != agentdomain.ModelCallSucceeded ||
			call.Model != record.Run.Model || call.Profile != record.Run.Profile || call.Prompt != record.Run.Prompt || call.Schema != record.Run.Schema {
			return domain.InvalidError(ErrorCodeNotePlanInvalid, "note interview model call proof is invalid")
		}
	}
	last := record.Calls[len(record.Calls)-1]
	if last.ResponseHash != NoteOutputHash(raw) || last.ResponseBytes != int64(len(raw)) {
		return domain.InvalidError(ErrorCodeNotePlanInvalid, "note interview plan differs from the accepted model response")
	}
	_, err := DecodeNotePlan(raw, preparation)
	return err
}
