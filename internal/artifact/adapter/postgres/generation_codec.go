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
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const sectionGenerationColumns = `
	id::text,workspace_id::text,artifact_id::text,source_revision_id::text,
	source_revision_no,source_artifact_version,section_key,idempotency_key,request_hash,
	workflow_run_id::text,node_run_id::text,status,model_run_id::text,
	recorded_base_revision_id::text,recorded_revision_id::text,recorded_artifact_version,
	content_hash,failure_class,error_code,error_summary,version,created_at,updated_at,completed_at,terminal_at`

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
