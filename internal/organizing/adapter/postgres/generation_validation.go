package postgres

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
)

func validateGenerationModelRecord(record agentapp.ModelRunRecord, generation organizingworkflow.GenerationRecord, expectedVersion int64, outputHash string) error {
	if !sameGenerationModelBinding(record.Run, generation) || !generationRuntimeMatches(record.Run, generation.Kind) ||
		record.Run.Status != agentdomain.ModelRunRunning ||
		record.Run.Version != expectedVersion || len(record.Calls) == 0 {
		return generationInconsistent(errors.New("organizing generation model run is incomplete"))
	}
	matchedOutput := false
	for _, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != record.Run.ID || call.Status == agentdomain.ModelCallStarted || call.Status == agentdomain.ModelCallUnknown {
			return generationInconsistent(errors.New("organizing generation model call history is incomplete"))
		}
		if !generationCallMatchesRuntime(call, record.Run) {
			return generationInconsistent(errors.New("organizing generation model call differs from its runtime"))
		}
		if call.Status == agentdomain.ModelCallSucceeded && call.ResponseHash == outputHash {
			matchedOutput = true
		}
	}
	if !matchedOutput {
		return generationInconsistent(errors.New("organizing generation output does not match a successful model response"))
	}
	return nil
}

func generationRuntimeMatches(run agentdomain.ModelRun, kind organizingworkflow.GenerationKind) bool {
	version := organizingworkflow.GenerationRuntimeVersion
	switch kind {
	case organizingworkflow.GenerationOutline:
		return run.Prompt == (agentdomain.PromptRef{ID: organizingworkflow.OutlinePromptID, Version: version}) &&
			run.Schema == (agentdomain.SchemaRef{ID: organizingworkflow.OutlineSchemaID, Version: version}) &&
			run.ReducedSchema == (agentdomain.SchemaRef{ID: organizingworkflow.OutlineReducedSchemaID, Version: version})
	case organizingworkflow.GenerationDocument:
		return run.Prompt == (agentdomain.PromptRef{ID: organizingworkflow.DocumentPromptID, Version: version}) &&
			run.Schema == (agentdomain.SchemaRef{ID: organizingworkflow.DocumentSchemaID, Version: version}) &&
			run.ReducedSchema == (agentdomain.SchemaRef{ID: organizingworkflow.DocumentReducedSchemaID, Version: version})
	default:
		return false
	}
}

func generationCallMatchesRuntime(call agentdomain.ModelCall, run agentdomain.ModelRun) bool {
	if call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt {
		return false
	}
	switch call.Phase {
	case agentdomain.ModelCallInitial, agentdomain.ModelCallRepair:
		return call.Schema == run.Schema
	case agentdomain.ModelCallReduced:
		return call.Schema == run.ReducedSchema
	default:
		return false
	}
}

func generationResultMatches(kind organizingworkflow.GenerationKind, resultType string) bool {
	switch kind {
	case organizingworkflow.GenerationOutline:
		return resultType == agentdomain.ResultTypeOrganizingOutline
	case organizingworkflow.GenerationDocument:
		return resultType == agentdomain.ResultTypeOrganizingDocument
	default:
		return false
	}
}

func sameGenerationModelBinding(run agentdomain.ModelRun, generation organizingworkflow.GenerationRecord) bool {
	return run.ID == generation.ModelRunID && run.WorkspaceID == generation.WorkspaceID &&
		run.WorkflowRunID == generation.WorkflowRunID && run.NodeRunID == generation.NodeRunID &&
		run.NodeAttemptID == generation.NodeAttemptID && sameOptionalRevision(run.ModelSettingsRevision, generation.ModelSettingsRevision)
}

func sameGenerationCreate(left, right organizingworkflow.GenerationRecord) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SnapshotID == right.SnapshotID &&
		left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID &&
		left.NodeAttemptID == right.NodeAttemptID && left.Kind == right.Kind && left.RequestHash == right.RequestHash &&
		sameOptionalRevision(left.ModelSettingsRevision, right.ModelSettingsRevision)
}

func validCompleteGeneration(command organizingworkflow.CompleteGenerationCommand) bool {
	return postgresID(command.WorkspaceID) && postgresID(command.GenerationID) && postgresID(command.ModelRunID) &&
		command.ExpectedVersion > 0 && command.ExpectedModelRunVersion > 0 &&
		(command.ResultType == agentdomain.ResultTypeOrganizingOutline || command.ResultType == agentdomain.ResultTypeOrganizingDocument) &&
		len(command.Output) > 0 && len(command.Output) <= 512*1024 && json.Valid(command.Output) &&
		lowerHash(command.OutputHash) && sha256Bytes(command.Output) == command.OutputHash && !command.CompletedAt.IsZero()
}

func validFailGeneration(command organizingworkflow.FailGenerationCommand) bool {
	return postgresID(command.WorkspaceID) && postgresID(command.GenerationID) && postgresID(command.ModelRunID) &&
		command.ExpectedVersion > 0 && command.ExpectedModelRunVersion > 0 &&
		(command.ModelRunStatus == agentdomain.ModelRunFailed || command.ModelRunStatus == agentdomain.ModelRunUnknown) &&
		stableGenerationError(command.ErrorCode) && !command.CompletedAt.IsZero() &&
		(command.ModelRunStatus != agentdomain.ModelRunUnknown || !command.Retryable)
}

func generationKind(kind organizingworkflow.GenerationKind) bool {
	return kind == organizingworkflow.GenerationOutline || kind == organizingworkflow.GenerationDocument
}

func sameOptionalRevision(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func timePointer(value time.Time) *time.Time { return &value }

func stableGenerationError(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func lowerHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func sha256Bytes(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("%x", digest[:])
}

func postgresID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func generationInvalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "ORGANIZING_GENERATION_INVALID", false, err)
}

func generationNotFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, "ORGANIZING_GENERATION_NOT_FOUND", false, err)
}

func generationConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "ORGANIZING_GENERATION_CONFLICT", false, err)
}

func generationInconsistent(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_RESULT_INVALID", false, err)
}

func generationRecovery(err error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_RECOVERY_REQUIRED", false, err)
}

func generationUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "ORGANIZING_GENERATION_REPOSITORY_UNAVAILABLE", true, err)
}
