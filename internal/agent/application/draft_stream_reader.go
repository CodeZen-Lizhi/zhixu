package application

import "context"

// DraftStreamReader is the HTTP-facing read port for the transient answer
// draft projection. Implementations enforce Workspace binding and return
// current session state from PostgreSQL; callers must never reconstruct a
// draft from Server Events or a published Answer.
type DraftStreamReader interface {
	ReadDraftStream(context.Context, DraftStreamReadQuery) (DraftStreamReadResult, error)
}
