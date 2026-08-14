package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	testHoldLease  = 10 * time.Minute
	testReadyWait  = 5 * time.Minute
	testProbeLease = 10 * time.Minute
)

// TestLifecycleAdapter turns a local connection test into a durable demand
// hold. It deliberately does not own a process or expose a runtime control
// endpoint; the resident supervisor observes the same PostgreSQL projection.
type TestLifecycleAdapter struct {
	store localmodelruntime.LifecycleStore
	ids   foundation.IDGenerator
}

var _ modelsettingsapplication.TestLifecycle = (*TestLifecycleAdapter)(nil)

// NewTestLifecycle creates the API-side local test hold adapter.
func NewTestLifecycle(store localmodelruntime.LifecycleStore) (*TestLifecycleAdapter, error) {
	if store == nil {
		return nil, errors.New("managed local model test lifecycle store is unavailable")
	}
	return &TestLifecycleAdapter{store: store, ids: foundation.NewUUIDGenerator(nil)}, nil
}

// BeginTest acquires the exact target model hold and waits for the supervisor
// to publish a matching ready hash before the production probe proceeds.
func (adapter *TestLifecycleAdapter) BeginTest(ctx context.Context, command modelsettingsapplication.TestLifecycleCommand) (modelsettingsapplication.TestLifecycleLease, error) {
	if adapter == nil || adapter.store == nil || ctx == nil {
		return nil, errors.New("managed local model test lifecycle is unavailable")
	}
	requirement, err := testRequirement(command.Settings, command.Target)
	if err != nil {
		return nil, err
	}
	if len(requirement.Models) == 0 {
		return nil, errors.New("managed local model test requirement is empty")
	}
	var lease *testHoldLeaseAdapter
	if preparationStore, ok := adapter.store.(localmodelruntime.TestPreparationStore); ok {
		operationID, holdID, holdOwnerID, idempotencyKey, requestHash, identityErr := testPreparationIdentity(command, requirement)
		if identityErr != nil {
			return nil, identityErr
		}
		preparation, seedErr := preparationStore.SeedTestPreparation(ctx, localmodelruntime.TestPreparationCommand{
			OperationID: operationID, HoldID: holdID, TargetRevision: optionalRevision(command.ExpectedRevision),
			IdempotencyKey: idempotencyKey, RequestHash: requestHash, Requirement: requirement,
			OwnerID: holdOwnerID, OwnerEpoch: 1, LeaseDuration: testHoldLease,
		})
		if seedErr != nil {
			return nil, seedErr
		}
		lease = &testHoldLeaseAdapter{store: adapter.store, preparation: preparationStore, hold: preparation.Hold, operationID: preparation.Operation.OperationID, holdOwnerID: holdOwnerID}
		switch preparation.Operation.Phase {
		case localmodelruntime.OperationPhaseSucceeded:
			lease.replayedSuccess = true
			return lease, nil
		case localmodelruntime.OperationPhaseFailed, localmodelruntime.OperationPhaseSuperseded:
			failure := testPreparationFailure(preparation.Operation)
			return nil, errors.Join(failure, lease.releaseTerminalHold(context.WithoutCancel(ctx)))
		}
		probeOwnerID, ownerErr := adapter.newProbeOwner(holdOwnerID)
		if ownerErr != nil {
			return nil, errors.New("managed local model test probe owner identity is unavailable")
		}
		lease.probeOwnerID = probeOwnerID
		lease.probeOwnerEpoch = 1
	} else {
		ownerID, ownerErr := adapter.ids.New()
		if ownerErr != nil {
			return nil, errors.New("managed local model test owner identity is unavailable")
		}
		holdID, idErr := adapter.ids.New()
		if idErr != nil {
			return nil, errors.New("managed local model test hold identity is unavailable")
		}
		hold, acquireErr := adapter.store.AcquireHold(ctx, localmodelruntime.HoldAcquireCommand{
			HoldID: holdID, Kind: localmodelruntime.HoldKindTest, OwnerID: ownerID, OwnerEpoch: 1,
			Role: "api", InstanceID: &ownerID, Revision: optionalRevision(command.ExpectedRevision),
			Requirement: requirement, LeaseDuration: testHoldLease,
		})
		if acquireErr != nil {
			return nil, acquireErr
		}
		lease = &testHoldLeaseAdapter{store: adapter.store, hold: hold, holdOwnerID: ownerID}
	}
	if err := adapter.waitReady(ctx, requirement, lease); err != nil {
		// Once seeded, the operation is durable demand owned by the supervisor.
		// A disconnected caller stops waiting but must not cancel shared work; a
		// replay with the same idempotency key can join it later.
		lease.releaseAfterWaitFailure()
		return nil, err
	}
	return lease, nil
}

func (adapter *TestLifecycleAdapter) newProbeOwner(holdOwnerID foundation.ID) (foundation.ID, error) {
	for range 4 {
		ownerID, err := adapter.ids.New()
		if err != nil {
			return "", err
		}
		if ownerID != holdOwnerID {
			return ownerID, nil
		}
	}
	return "", errors.New("managed local model test probe owner collides with hold owner")
}

func (adapter *TestLifecycleAdapter) waitReady(ctx context.Context, requirement localmodelruntime.Requirement, lease *testHoldLeaseAdapter) error {
	waitCtx, cancel := context.WithTimeout(ctx, testReadyWait)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		snapshot, err := adapter.store.ReadDemand(waitCtx)
		operationReady := lease.operationID == ""
		var operation localmodelruntime.OperationRecord
		operationFound := false
		if err == nil && lease.operationID != "" {
			for _, candidate := range snapshot.Operations {
				if candidate.OperationID == lease.operationID {
					operationFound = true
					operation = candidate
					operationReady = candidate.Phase == localmodelruntime.OperationPhaseReady || candidate.Phase == localmodelruntime.OperationPhaseProbing
					break
				}
			}
			if !operationFound {
				operation, err = lease.preparation.ReadTestOperation(waitCtx, lease.operationID)
				if err == nil {
					switch operation.Phase {
					case localmodelruntime.OperationPhaseSucceeded:
						lease.replayedSuccess = true
						return nil
					case localmodelruntime.OperationPhaseFailed, localmodelruntime.OperationPhaseSuperseded:
						return testPreparationFailure(operation)
					}
				}
			}
		}
		if err == nil && operationReady && snapshot.Runtime.Phase == localmodelruntime.RuntimePhaseReady {
			if effective, effectiveErr := localmodelruntime.NewRequirement(snapshot.Requirement.Models); effectiveErr == nil &&
				effective.Hash == snapshot.Runtime.ReadyHash && containsRequirement(effective, requirement) {
				if lease.operationID == "" {
					return nil
				}
				claimed, owned, claimErr := lease.preparation.ClaimTestProbe(waitCtx, localmodelruntime.TestProbeClaimCommand{
					OperationID: lease.operationID, OwnerID: lease.probeOwnerID, OwnerEpoch: lease.probeOwnerEpoch,
					ExpectedVersion: operation.Version, LeaseDuration: testProbeLease,
				})
				if claimErr != nil {
					lastErr = claimErr
				} else if owned {
					lease.probeVersion = claimed.Version
					return nil
				} else {
					switch claimed.Phase {
					case localmodelruntime.OperationPhaseSucceeded:
						lease.replayedSuccess = true
						return nil
					case localmodelruntime.OperationPhaseFailed, localmodelruntime.OperationPhaseSuperseded:
						return testPreparationFailure(claimed)
					}
				}
			}
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-waitCtx.Done():
			if lastErr != nil {
				return errors.Join(errors.New("managed local model runtime did not become ready"), lastErr, waitCtx.Err())
			}
			return errors.Join(errors.New("managed local model runtime did not become ready"), waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func containsRequirement(effective, requested localmodelruntime.Requirement) bool {
	for _, wanted := range requested.Models {
		found := false
		for _, available := range effective.Models {
			if wanted == available {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type testHoldLeaseAdapter struct {
	store           localmodelruntime.LifecycleStore
	preparation     localmodelruntime.TestPreparationStore
	hold            localmodelruntime.HoldRecord
	operationID     foundation.ID
	holdOwnerID     foundation.ID
	probeOwnerID    foundation.ID
	probeOwnerEpoch int64
	probeVersion    int64
	once            sync.Once
	err             error
	replayedSuccess bool
}

func (lease *testHoldLeaseAdapter) Close(ctx context.Context) error {
	return lease.Abandon(ctx)
}

func (lease *testHoldLeaseAdapter) releaseAfterWaitFailure() {
	if lease != nil && lease.preparation == nil {
		_ = lease.Close(context.Background())
	}
}

func (lease *testHoldLeaseAdapter) releaseTerminalHold(ctx context.Context) error {
	if lease == nil || lease.store == nil {
		return nil
	}
	_, err := lease.store.ReleaseHold(ctx, localmodelruntime.HoldReleaseCommand{
		HoldID: lease.hold.HoldID, OwnerID: lease.holdOwnerID, OwnerEpoch: lease.hold.OwnerEpoch,
	})
	return err
}

func (lease *testHoldLeaseAdapter) ReplayedSuccess() bool {
	return lease != nil && lease.replayedSuccess
}

// Complete closes the durable test operation with the production probe result.
// It is intentionally optional on the application interface so static/fake
// test lifecycles keep their existing release-only contract.
func (lease *testHoldLeaseAdapter) Complete(ctx context.Context, errorCode string, retryable bool) error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		lease.err = func() error {
			if ctx == nil {
				ctx = context.Background()
			}
			if lease.preparation != nil {
				if lease.replayedSuccess {
					return nil
				}
				_, err := lease.preparation.CompleteTestProbe(ctx, localmodelruntime.TestProbeCompletionCommand{
					OperationID: lease.operationID, OwnerID: lease.probeOwnerID, OwnerEpoch: lease.probeOwnerEpoch,
					ExpectedVersion: lease.probeVersion, ErrorCode: errorCode, Retryable: retryable,
				})
				return err
			}
			_, err := lease.store.ReleaseHold(ctx, localmodelruntime.HoldReleaseCommand{
				HoldID: lease.hold.HoldID, OwnerID: lease.holdOwnerID, OwnerEpoch: 1,
				ExpectedVersion: lease.hold.Version,
			})
			return err
		}()
	})
	return lease.err
}

// Abandon gives up only this request's production-probe claim. Durable
// preparation demand remains live so an exact replay can reclaim the probe.
func (lease *testHoldLeaseAdapter) Abandon(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		lease.err = func() error {
			if ctx == nil {
				ctx = context.Background()
			}
			if lease.preparation == nil {
				_, err := lease.store.ReleaseHold(ctx, localmodelruntime.HoldReleaseCommand{
					HoldID: lease.hold.HoldID, OwnerID: lease.holdOwnerID, OwnerEpoch: 1,
					ExpectedVersion: lease.hold.Version,
				})
				return err
			}
			if lease.replayedSuccess || lease.probeVersion == 0 {
				return nil
			}
			_, err := lease.preparation.CompleteTestProbe(ctx, localmodelruntime.TestProbeCompletionCommand{
				OperationID: lease.operationID, OwnerID: lease.probeOwnerID, OwnerEpoch: lease.probeOwnerEpoch,
				ExpectedVersion: lease.probeVersion, Abandon: true,
			})
			return err
		}()
	})
	return lease.err
}

func testPreparationIdentity(command modelsettingsapplication.TestLifecycleCommand, requirement localmodelruntime.Requirement) (foundation.ID, foundation.ID, foundation.ID, string, string, error) {
	target := string(command.Target)
	revision := strconv.FormatInt(command.ExpectedRevision, 10)
	targetIdentity, err := testTargetIdentity(command.Settings, command.Target)
	if err != nil {
		return "", "", "", "", "", err
	}
	identity := strings.Join([]string{"test", command.IdempotencyKey, target, revision, requirement.Hash, targetIdentity}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	operationID, err := testLifecycleID("operation", digest)
	if err != nil {
		return "", "", "", "", "", err
	}
	holdID, err := testLifecycleID("hold", digest)
	if err != nil {
		return "", "", "", "", "", err
	}
	ownerID, err := testLifecycleID("owner", digest)
	if err != nil {
		return "", "", "", "", "", err
	}
	requestDigest := sha256.Sum256([]byte("model-settings-test/v1\x00" + identity))
	return operationID, holdID, ownerID, command.IdempotencyKey, hex.EncodeToString(requestDigest[:]), nil
}

func testLifecycleID(prefix string, digest [32]byte) (foundation.ID, error) {
	var raw [16]byte
	copy(raw[:], digest[:16])
	switch prefix {
	case "hold":
		for i := range raw {
			raw[i] ^= 0x5a
		}
	case "owner":
		for i := range raw {
			raw[i] ^= 0xa5
		}
	}
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	return foundation.ParseID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
}

func testTargetIdentity(settings modelsettingsdomain.Settings, target modelsettingsapplication.ConnectionTarget) (string, error) {
	switch target {
	case modelsettingsapplication.ConnectionTargetChat:
		chat := settings.Chat
		return strings.Join([]string{
			string(chat.Provider), string(chat.APIStyle), chat.BaseURL, chat.Model, chat.ModelVersion, chat.AdapterVersion,
			strconv.FormatInt(chat.Timeout.Nanoseconds(), 10), strconv.FormatInt(chat.MaxRequestBytes, 10), strconv.FormatInt(chat.MaxResponseBytes, 10),
		}, "\x00"), nil
	case modelsettingsapplication.ConnectionTargetEmbedding:
		embedding := settings.Embedding
		return strings.Join([]string{
			string(embedding.Provider), embedding.BaseURL, embedding.Model, strconv.FormatInt(int64(embedding.Dimensions), 10),
			string(embedding.Normalization), string(embedding.DistanceMetric), strconv.FormatInt(int64(embedding.MaxBatchSize), 10),
			strconv.FormatInt(int64(embedding.MaxInputBytes), 10), strconv.FormatInt(embedding.MaxBatchInputBytes, 10),
			strconv.FormatInt(embedding.Timeout.Nanoseconds(), 10), strconv.FormatInt(embedding.MaxResponseBytes, 10),
		}, "\x00"), nil
	default:
		return "", errors.New("model connection target is invalid")
	}
}

func testPreparationFailure(operation localmodelruntime.OperationRecord) error {
	code := operation.ErrorCode
	if code == "" {
		code = "LOCAL_MODEL_RUNTIME_TEST_FAILED"
	}
	kind := foundation.ErrorNonRetryableFailure
	if operation.Retryable {
		kind = foundation.ErrorRetryableFailure
	}
	if operation.Phase == localmodelruntime.OperationPhaseSuperseded {
		kind = foundation.ErrorVersionConflict
		code = "LOCAL_MODEL_RUNTIME_TEST_SUPERSEDED"
	}
	return foundation.NewError(kind, code, operation.Retryable, errors.New("managed local model test preparation did not succeed"))
}

func testRequirement(settings modelsettingsdomain.Settings, target modelsettingsapplication.ConnectionTarget) (localmodelruntime.Requirement, error) {
	var model string
	switch target {
	case modelsettingsapplication.ConnectionTargetChat:
		if settings.Chat.Provider == modelsettingsdomain.ChatProviderOllama ||
			(settings.Chat.Provider == modelsettingsdomain.ChatProviderOpenAICompatible && settings.Chat.BaseURL == modelsettingsdomain.ManagedOllamaBaseURL) {
			model = settings.Chat.Model
		}
	case modelsettingsapplication.ConnectionTargetEmbedding:
		if settings.Embedding.Provider == modelsettingsdomain.EmbeddingProviderOllama {
			model = settings.Embedding.Model
		}
	default:
		return localmodelruntime.Requirement{}, errors.New("model connection target is invalid")
	}
	return localmodelruntime.NewRequirement([]localmodelruntime.ModelRef{localmodelruntime.ModelRef(model)})
}

func optionalRevision(revision int64) *int64 {
	if revision < 0 {
		return nil
	}
	return &revision
}
