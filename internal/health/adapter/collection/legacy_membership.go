package collection

import (
	"context"

	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/jackc/pgx/v5"
)

// DurableBindingVerifier is the legacy pgx bridge retained for unchanged
// composition roots. Staged GORM callers must use ScopedBindingVerifier.
type DurableBindingVerifier struct{}

// Verify delegates binding validation to the Collection owner without
// duplicating its durable-scan SQL. Final composition removes this bridge
// after legacy Health callers move to the scoped port.
func (DurableBindingVerifier) Verify(ctx context.Context, tx pgx.Tx, binding healthapp.SmartCollectionBinding) error {
	return collectionpostgres.VerifyDurableScanBinding(ctx, tx, toCollectionBinding(binding))
}
