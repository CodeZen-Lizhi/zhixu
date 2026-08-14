package localmodelruntime

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// LifecycleStore is the narrow persistence seam shared by the supervisor and
// application reconciler. Implementations must use PostgreSQL time for all
// freshness/lease decisions and must never perform process or network I/O while
// holding a database transaction.
type LifecycleStore interface {
	ManagerStore
	DemandStore
	OperationStore
	HoldStore
	RuntimeStore
}

// ActivationPreparationReader exposes the read-only gate used by the model
// settings coordinator before it probes a local target.
type ActivationPreparationReader interface {
	ReadActivationOperation(context.Context, int64) (OperationRecord, error)
}

// TxStore exposes activation facts that must be committed with an owning
// model-settings transaction. The transaction is supplied by the caller and
// is never committed or rolled back by these methods.
type TxStore interface {
	SeedActivationPreparation(context.Context, ActivationPreparationCommand) (ActivationPreparation, error)
	ReadOperationByRollout(context.Context, foundation.ID) (OperationRecord, error)
	ReadOperationByTarget(context.Context, int64) (OperationRecord, error)
	CompleteActivationPreparation(context.Context, foundation.ID, string, bool) (OperationRecord, error)
}

// TestPreparationStore is the optional application-side seam for durable
// connection-test preparation. It is separate from LifecycleStore so narrow
// in-memory supervisor fakes do not need to implement API-only behavior.
type TestPreparationStore interface {
	SeedTestPreparation(context.Context, TestPreparationCommand) (TestPreparation, error)
	ReadTestOperation(context.Context, foundation.ID) (OperationRecord, error)
	ClaimTestProbe(context.Context, TestProbeClaimCommand) (OperationRecord, bool, error)
	CompleteTestProbe(context.Context, TestProbeCompletionCommand) (OperationRecord, error)
}

// TxLifecycle is the unbound form used by callers that own the transaction.
type TxLifecycle interface {
	SeedActivationPreparation(context.Context, pgx.Tx, ActivationPreparationCommand) (ActivationPreparation, error)
	ReadOperationByRollout(context.Context, pgx.Tx, foundation.ID) (OperationRecord, error)
	ReadOperationByTarget(context.Context, pgx.Tx, int64) (OperationRecord, error)
	CompleteActivationPreparation(context.Context, pgx.Tx, foundation.ID, string, bool) (OperationRecord, error)
}

var _ TxLifecycle = (*PostgresStore)(nil)

// WithTx binds lifecycle operations to an existing PostgreSQL transaction.
func (store *PostgresStore) WithTx(tx pgx.Tx) TxStore { return &txStore{tx: tx} }

// ManagerStore owns the singleton supervisor lease.
type ManagerStore interface {
	ClaimManager(context.Context, ManagerClaimCommand) (ManagerLease, error)
	HeartbeatManager(context.Context, ManagerHeartbeatCommand) (ManagerLease, error)
}

// DemandStore reads and publishes the effective local model demand. ReadDemand
// is one repeatable-read projection; it computes the authoritative union from
// active/live rollout settings, fresh holds, and nonterminal operations. The
// manager then PublishDemand-s the resulting hash/model set under its current
// owner+requirement-version fence before starting or stopping external work.
type DemandStore interface {
	ReadDemand(context.Context) (DemandSnapshot, error)
	PublishDemand(context.Context, DemandCASCommand) (RuntimeRecord, error)
	LoadEffectiveIntent(context.Context, IntentReadCommand) (IntentSnapshot, error)
}

// OperationStore persists resumable model preparation and recovery work.
type OperationStore interface {
	SeedActiveRecovery(context.Context, ActiveRecoveryCommand) (OperationRecord, error)
	ActiveRecoveryCurrent(context.Context, ActiveRecoveryCheckCommand) (bool, error)
	ClaimOperation(context.Context, OperationClaimCommand) (OperationRecord, error)
	BeginPullAttempt(context.Context, PullAttemptCommand) (PullAttemptResult, error)
	RecordOperationProgress(context.Context, OperationProgressCommand) (OperationRecord, error)
	CompleteOperation(context.Context, OperationTerminalCommand) (OperationRecord, error)
	SweepExpiredOperations(context.Context, OperationExpirySweepCommand) (int64, error)
}

// HoldStore maintains generation/test/preparation demand references.
type HoldStore interface {
	AcquireHold(context.Context, HoldAcquireCommand) (HoldRecord, error)
	RenewHold(context.Context, HoldRenewCommand) (HoldRecord, error)
	ReleaseHold(context.Context, HoldReleaseCommand) (HoldRecord, error)
}

// RuntimeStore applies the supervisor serve phase with owner/epoch/version CAS.
type RuntimeStore interface {
	CompareAndSetRuntimePhase(context.Context, RuntimePhaseCommand) (RuntimeRecord, error)
}
