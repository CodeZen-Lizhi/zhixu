// Package workflow adapts durable Capture processing to the shared Workflow runtime.
package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const maxWorkflowRetries = 3

// OutputReceipt is the redacted durable result of Capture processing.
type OutputReceipt struct {
	SchemaVersion int                `json:"schema_version"`
	WorkspaceID   foundation.ID      `json:"workspace_id"`
	CaptureID     foundation.ID      `json:"capture_id"`
	Status        domain.Status      `json:"status"`
	ProfileStatus domain.StageStatus `json:"profile_status"`
	ProfileID     foundation.ID      `json:"profile_id,omitempty"`
}

// RegisteredDefinition returns the frozen single-node Capture processing workflow.
func RegisteredDefinition() (workflowdomain.RegisteredDefinition, error) {
	graph := workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key: captureapp.ProcessingNodeKey, Kind: captureapp.ProcessingNodeKind,
		InputSchemaVersion:  captureapp.ProcessingInputSchemaVersion,
		OutputSchemaVersion: captureapp.ProcessingOutputSchemaVersion,
		RetryPolicy: workflowdomain.RetryPolicy{
			MaxRetries: maxWorkflowRetries, BaseDelay: time.Second, MaxDelay: 30 * time.Second,
		},
		RequiredPermissions: []workflowdomain.Permission{
			workflowdomain.PermissionReadLocal,
			workflowdomain.PermissionWriteProposal,
		},
	}}}
	graphHash, err := workflowapp.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	return workflowdomain.RegisteredDefinition{
		Key: captureapp.ProcessingDefinitionKey, Version: captureapp.ProcessingDefinitionVersion,
		InputSchemaVersion: captureapp.ProcessingInputSchemaVersion, Graph: graph, GraphHash: graphHash,
	}, nil
}

// DecodeInput strictly decodes the minimal Capture processing identity.
func DecodeInput(raw json.RawMessage) (captureapp.ProcessingInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input captureapp.ProcessingInput
	if err := decoder.Decode(&input); err != nil {
		return captureapp.ProcessingInput{}, inputError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return captureapp.ProcessingInput{}, inputError(errors.New("capture workflow input contains trailing JSON"))
	}
	if input.SchemaVersion != captureapp.ProcessingInputSchemaVersion || !validID(input.WorkspaceID) || !validID(input.CaptureID) {
		return captureapp.ProcessingInput{}, inputError(errors.New("capture workflow input is invalid"))
	}
	return input, nil
}

// EncodeOutput validates and encodes a Capture processing receipt.
func EncodeOutput(receipt OutputReceipt) (json.RawMessage, error) {
	if receipt.SchemaVersion != captureapp.ProcessingOutputSchemaVersion || !validID(receipt.WorkspaceID) ||
		!validID(receipt.CaptureID) || !receipt.Status.Valid() || !receipt.ProfileStatus.Valid() ||
		(receipt.ProfileID != "" && !validID(receipt.ProfileID)) {
		return nil, outputError(errors.New("capture workflow output is invalid"))
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, outputError(err)
	}
	return encoded, nil
}

func validateExecution(execution workflowapp.ExecutionContext) error {
	definition, err := RegisteredDefinition()
	if err != nil {
		return err
	}
	if execution.DefinitionVersion != captureapp.ProcessingDefinitionVersion || execution.DefinitionHash != definition.GraphHash ||
		execution.NodeKey != captureapp.ProcessingNodeKey || execution.NodeKind != captureapp.ProcessingNodeKind ||
		execution.InputSchemaVersion != captureapp.ProcessingInputSchemaVersion || execution.AttemptNo < 1 ||
		execution.DispatchNo < 1 || execution.NodeVersion < 1 || strings.TrimSpace(execution.LeaseOwner) == "" ||
		!validID(execution.WorkspaceID) || !validID(execution.DefinitionID) || !validID(execution.RunID) ||
		!validID(execution.NodeRunID) || !validID(execution.NodeAttemptID) {
		return inputError(errors.New("capture workflow execution binding is invalid"))
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func inputError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "CAPTURE_WORKFLOW_INPUT_INVALID", false, cause)
}

func outputError(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "CAPTURE_WORKFLOW_OUTPUT_INVALID", false, cause)
}
