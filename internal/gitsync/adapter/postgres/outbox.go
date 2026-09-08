package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

func validateOutboxLease(lease domain.OutboxLease) error {
	if !validID(lease.ID) || !validID(lease.WorkspaceID) || !validID(lease.RunID) || !validText(lease.Owner, 256) ||
		(lease.Kind != domain.OutboxExecuteRun && lease.Kind != domain.OutboxIndexFollowup) ||
		lease.AttemptCount < 1 || lease.Version < 2 || lease.LeaseUntil.IsZero() {
		return invalid("Git sync outbox lease is invalid")
	}
	return nil
}
