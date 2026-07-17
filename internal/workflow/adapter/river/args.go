// Package riveradapter contains the River transport adapter for durable
// workflow node delivery. It deliberately does not own workflow state.
package riveradapter

import (
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// WorkflowSchema is the only PostgreSQL schema used by the River runtime.
	WorkflowSchema = "workflow"
	// NodeJobKind is a persisted, versioned River job kind. Do not rename it.
	NodeJobKind = "workflow_node_run_v1"
	// NodeJobSchemaVersion identifies the JSON contract of NodeJobArgs.
	NodeJobSchemaVersion = 1
)

// NodeJobArgs is the complete persisted payload for a runnable node. The
// node kind and input are loaded from the workflow fact source by the worker;
// they are intentionally not copied into this transport payload.
type NodeJobArgs struct {
	SchemaVersion int           `json:"schema_version"`
	NodeRunID     foundation.ID `json:"node_run_id" river:"unique"`
	DispatchNo    int           `json:"dispatch_no" river:"unique"`
}

// Kind implements river.JobArgs while keeping the stable kind local to this
// adapter package.
func (NodeJobArgs) Kind() string { return NodeJobKind }

// NewNodeJobArgs validates and constructs a node delivery payload.
func NewNodeJobArgs(nodeRunID foundation.ID, dispatchNo int) (NodeJobArgs, error) {
	args := NodeJobArgs{SchemaVersion: NodeJobSchemaVersion, NodeRunID: nodeRunID, DispatchNo: dispatchNo}
	if err := ValidateNodeJobArgs(args); err != nil {
		return NodeJobArgs{}, err
	}
	return args, nil
}

// ValidateNodeJobArgs enforces the persisted job contract at every boundary.
func ValidateNodeJobArgs(args NodeJobArgs) error {
	if args.SchemaVersion != NodeJobSchemaVersion {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_NODE_JOB_SCHEMA_INVALID", errors.New("unsupported node job schema version"))
	}
	parsed, err := foundation.ParseID(string(args.NodeRunID))
	if err != nil || parsed != args.NodeRunID {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_NODE_JOB_NODE_RUN_ID_INVALID", errors.New("node run id must be a canonical UUID"))
	}
	if args.DispatchNo < 1 {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_NODE_JOB_DISPATCH_INVALID", errors.New("dispatch number must be positive"))
	}
	if strings.ContainsAny(string(args.NodeRunID), "\r\n\t/") {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_NODE_JOB_SECRET_OR_PATH_INVALID", errors.New("job identity contains a forbidden separator"))
	}
	return nil
}

func jobError(kind foundation.ErrorKind, code string, cause error) error {
	return foundation.NewError(kind, code, false, cause)
}
