package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// CanonicalJSONHashNodeKind is the stable kind of the side-effect-free test executor.
	CanonicalJSONHashNodeKind = "deterministic.canonical_json_hash"
	// CanonicalJSONHashInputSchemaVersion is the supported deterministic input contract.
	CanonicalJSONHashInputSchemaVersion = 1
	// CanonicalJSONHashOutputSchemaVersion is the emitted deterministic output contract.
	CanonicalJSONHashOutputSchemaVersion = 1
)

// CanonicalJSONHashExecutor computes a SHA-256 digest over normalized JSON.
type CanonicalJSONHashExecutor struct{}

// NewCanonicalJSONHashExecutor constructs the side-effect-free deterministic executor.
func NewCanonicalJSONHashExecutor() CanonicalJSONHashExecutor {
	return CanonicalJSONHashExecutor{}
}

// Execute validates the stable contract and returns a versioned canonical JSON digest.
func (CanonicalJSONHashExecutor) Execute(_ context.Context, execution ExecutionContext) (ExecutionResult, error) {
	if execution.NodeKind != CanonicalJSONHashNodeKind || execution.InputSchemaVersion != CanonicalJSONHashInputSchemaVersion {
		return ExecutionResult{}, registryError(foundation.ErrorNonRetryableFailure, "WORKFLOW_DETERMINISTIC_CONTRACT_UNSUPPORTED", errors.New("deterministic executor contract is not registered"))
	}
	decoder := json.NewDecoder(bytes.NewReader(execution.Input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ExecutionResult{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_DETERMINISTIC_INPUT_INVALID", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("deterministic input contains multiple JSON values")
		}
		return ExecutionResult{}, registryError(foundation.ErrorInvalidInput, "WORKFLOW_DETERMINISTIC_INPUT_INVALID", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return ExecutionResult{}, registryError(foundation.ErrorNonRetryableFailure, "WORKFLOW_DETERMINISTIC_CANONICAL_ENCODING_FAILED", err)
	}
	digest := sha256.Sum256(canonical)
	output, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		SHA256        string `json:"sha256"`
	}{SchemaVersion: CanonicalJSONHashOutputSchemaVersion, SHA256: hex.EncodeToString(digest[:])})
	if err != nil {
		return ExecutionResult{}, registryError(foundation.ErrorNonRetryableFailure, "WORKFLOW_DETERMINISTIC_OUTPUT_ENCODING_FAILED", err)
	}
	return ExecutionResult{Output: output}, nil
}
