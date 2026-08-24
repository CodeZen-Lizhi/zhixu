package application

import (
	"context"
	"encoding/json"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedRuntimeBindingQuery identifies one Workspace-bound durable runtime
// binding. NodeAttemptID is optional; when present the reader reports whether
// that attempt belongs to the requested Node Run.
type ScopedRuntimeBindingQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
}

// ScopedRuntimeBindingSnapshot is the raw Workflow-owned runtime snapshot.
// JSON values remain opaque to Workflow callers so another owner can apply its
// own strict decoder and schema validation without duplicating Workflow SQL.
type ScopedRuntimeBindingSnapshot struct {
	RunInput            json.RawMessage
	NodeInput           json.RawMessage
	DefinitionKey       string
	DefinitionVersion   int64
	DefinitionGraph     json.RawMessage
	NodeKey             string
	NodeType            string
	InputSchemaVersion  int
	OutputSchemaVersion int
	AttemptFound        bool
}

// ScopedRuntimeBindingReader reads durable Workflow runtime facts in a
// caller-owned transaction scope. Implementations never manage transaction
// lifetime and must fail closed for an invalid or inactive scope.
type ScopedRuntimeBindingReader interface {
	LoadRuntimeBindingScoped(context.Context, foundation.TransactionScope, ScopedRuntimeBindingQuery) (ScopedRuntimeBindingSnapshot, error)
}
