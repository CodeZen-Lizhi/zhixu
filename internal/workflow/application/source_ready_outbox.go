package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

// SourceReadyEventType identifies the durable parser-completion notification.
const SourceReadyEventType = "ingestion.source.ready"

// SourceReadyOutboxFact contains the first notification for an immutable source
// version/projection pair. EventID remains stable across dispatch retries.
type SourceReadyOutboxFact struct {
	EventID foundation.ID
	Ready   ingestiondomain.SourceReady
}

// ScopedSourceReadyOutbox keeps a claim locked until the caller commits its
// Workflow start (or provenance exclusion) together with Publish. A rollback
// releases the claim without consuming the notification.
type ScopedSourceReadyOutbox interface {
	ClaimSourceReadyScoped(context.Context, foundation.TransactionScope) (SourceReadyOutboxFact, bool, error)
	PublishSourceReadyScoped(context.Context, foundation.TransactionScope, SourceReadyOutboxFact) error
}
