//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceAnalysisModelOperationAuthorizeReconcileAndReplacementUnknownIntegration(t *testing.T) {
	for _, variant := range workspaceAnalysisModelRepositoryIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			pool := platform.DB()
			fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
			repository := variant.open(t, platform)

			created, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
			if err != nil || created.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
				t.Fatalf("first authorization=%#v err=%v", created, err)
			}
			checkpointQuery := application.WorkspaceAnalysisRetrievalPlanCheckpointQuery{
				WorkspaceID: fixture.command.Identity.WorkspaceID, WorkflowRunID: fixture.command.Identity.WorkflowRunID,
				AnalysisRunID: fixture.command.OperationKey.AnalysisRunID, NodeRunID: fixture.command.Identity.NodeRunID,
			}
			checkpoint, found, err := repository.FindWorkspaceAnalysisRetrievalPlanCheckpoint(ctx, checkpointQuery)
			if err != nil || !found || checkpoint.Status != domain.WorkspaceAnalysisOperationStarted ||
				checkpoint.ModelResultID != "" || checkpoint.ResultHash != "" ||
				checkpoint.OperationID != created.OperationID || checkpoint.ModelRunID != created.Run.ID ||
				checkpoint.ModelCallID != created.Call.ID || !sameRetrieval(checkpoint.Retrieval, created.Run.Retrieval) {
				t.Fatalf("started checkpoint=%#v found=%t err=%v", checkpoint, found, err)
			}
			reconciled, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
			if err != nil || reconciled.Disposition != application.WorkspaceAnalysisModelAuthorizationReconcile ||
				reconciled.Run.ID != created.Run.ID || reconciled.Call.ID != created.Call.ID {
				t.Fatalf("same-attempt reconcile=%#v err=%v", reconciled, err)
			}

			advanceWorkspaceAnalysisModelAttemptIntegration(t, ctx, pool)
			replacement := fixture.command
			replacement.Identity.NodeAttemptID = workspaceAnalysisModelIntegrationID(90)
			replacement.Identity.LeaseFence = 2
			replacement.Run.ID = workspaceAnalysisModelIntegrationID(91)
			replacement.Run.NodeAttemptID = replacement.Identity.NodeAttemptID
			replacement.Call.ID = workspaceAnalysisModelIntegrationID(92)
			replacement.Call.ModelRunID = replacement.Run.ID
			reduced, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, replacement)
			if err != nil || reduced.Disposition != application.WorkspaceAnalysisModelAuthorizationTerminateUnknown ||
				reduced.Run.ID != created.Run.ID || reduced.Call.ID != created.Call.ID ||
				reduced.Run.Status != domain.ModelRunUnknown || reduced.Call.Status != domain.ModelCallUnknown {
				t.Fatalf("replacement reduction=%#v err=%v", reduced, err)
			}

			var callStatus, runStatus, operationStatus, reservationStatus, errorCode string
			var settledInput, settledOutput, runReservedInput, runReservedOutput, runSettledInput, runSettledOutput int64
			if err := pool.QueryRow(ctx, `SELECT call.status,model.status,operation.status,reservation.status,operation.error_code,
		reservation.settled_input_tokens,reservation.settled_output_tokens,
		analysis.reserved_input_tokens,analysis.reserved_output_tokens,
		analysis.settled_input_tokens,analysis.settled_output_tokens
		FROM agent.workspace_analysis_operation AS operation
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.workspace_analysis_run AS analysis ON analysis.id=operation.analysis_run_id
		JOIN agent.model_call AS call ON call.id=operation.model_call_id
		JOIN agent.model_run AS model ON model.id=call.model_run_id
		WHERE operation.id=$1`, string(created.OperationID)).Scan(
				&callStatus, &runStatus, &operationStatus, &reservationStatus, &errorCode,
				&settledInput, &settledOutput, &runReservedInput, &runReservedOutput, &runSettledInput, &runSettledOutput,
			); err != nil {
				t.Fatal(err)
			}
			if callStatus != "UNKNOWN" || runStatus != "UNKNOWN" || operationStatus != "UNKNOWN" ||
				reservationStatus != "UNKNOWN_CHARGED" || errorCode != string(domain.WorkspaceAnalysisRunResultUnknown) ||
				settledInput != domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall ||
				settledOutput != domain.WorkspaceAnalysisV1PlanMaxOutputTokens || runReservedInput != 0 || runReservedOutput != 0 ||
				runSettledInput != domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall ||
				runSettledOutput != domain.WorkspaceAnalysisV1PlanMaxOutputTokens {
				t.Fatalf("unknown closure call=%s run=%s operation=%s reservation=%s error=%s settled=(%d,%d) run=(%d,%d,%d,%d)",
					callStatus, runStatus, operationStatus, reservationStatus, errorCode, settledInput, settledOutput,
					runReservedInput, runReservedOutput, runSettledInput, runSettledOutput)
			}
		})
	}
}

type workspaceAnalysisModelRepositoryIntegrationStore interface {
	application.WorkspaceAnalysisModelOperationRepository
	application.WorkspaceAnalysisRetrievalPlanCheckpointReader
	application.WorkspaceAnalysisCandidateAuthorityReader
}

type workspaceAnalysisModelRepositoryIntegrationVariant struct {
	name               string
	open               func(*testing.T, *platformpostgres.Pool) workspaceAnalysisModelRepositoryIntegrationStore
	openWithCommitLoss func(*testing.T, *platformpostgres.Pool) (workspaceAnalysisModelRepositoryIntegrationStore, func())
}

func workspaceAnalysisModelRepositoryIntegrationVariants() []workspaceAnalysisModelRepositoryIntegrationVariant {
	return []workspaceAnalysisModelRepositoryIntegrationVariant{
		{name: "legacy", open: openLegacyWorkspaceAnalysisModelRepositoryIntegration,
			openWithCommitLoss: openLegacyWorkspaceAnalysisModelRepositoryWithCommitLossIntegration},
		{name: "gorm", open: openGORMWorkspaceAnalysisModelRepositoryIntegration,
			openWithCommitLoss: openGORMWorkspaceAnalysisModelRepositoryWithCommitLossIntegration},
	}
}

func openLegacyWorkspaceAnalysisModelRepositoryIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) workspaceAnalysisModelRepositoryIntegrationStore {
	t.Helper()
	repository, err := NewRepository(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func openGORMWorkspaceAnalysisModelRepositoryIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) workspaceAnalysisModelRepositoryIntegrationStore {
	t.Helper()
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(platform)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMWorkspaceAnalysisRepository(platform, fence)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func openLegacyWorkspaceAnalysisModelRepositoryWithCommitLossIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) (workspaceAnalysisModelRepositoryIntegrationStore, func()) {
	t.Helper()
	database := &workspaceAnalysisModelCommitLossDB{DB: platform.DB()}
	repository, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	return repository, func() { database.injectNext.Store(true) }
}

func openGORMWorkspaceAnalysisModelRepositoryWithCommitLossIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) (workspaceAnalysisModelRepositoryIntegrationStore, func()) {
	t.Helper()
	repository := openGORMWorkspaceAnalysisModelRepositoryIntegration(t, platform).(*GORMWorkspaceAnalysisRepository)
	loss := &workspaceAnalysisModelPostCommitErrorUnitOfWork{
		inner: repository.unitOfWork,
		err:   errors.New("injected GORM workspace analysis model commit response loss"),
	}
	repository.unitOfWork = loss
	return repository, func() {
		loss.lost.Store(false)
		loss.armed.Store(true)
	}
}

func testWorkspaceAnalysisModelRepositoryIntegrationVariants(
	t *testing.T,
	test func(*testing.T, *pgxpool.Pool, context.Context, workspaceAnalysisModelRepositoryIntegrationStore),
) {
	t.Helper()
	for _, variant := range workspaceAnalysisModelRepositoryIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			test(t, platform.DB(), ctx, variant.open(t, platform))
		})
	}
}

func testWorkspaceAnalysisModelRepositoryCommitLossIntegrationVariants(
	t *testing.T,
	test func(*testing.T, *pgxpool.Pool, context.Context, workspaceAnalysisModelRepositoryIntegrationStore, func()),
) {
	t.Helper()
	for _, variant := range workspaceAnalysisModelRepositoryIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			repository, armCommitLoss := variant.openWithCommitLoss(t, platform)
			test(t, platform.DB(), ctx, repository, armCommitLoss)
		})
	}
}

func TestWorkspaceAnalysisModelOperationCommitResponseLossRecoveryIntegration(t *testing.T) {
	t.Run("authorization", func(t *testing.T) {
		testWorkspaceAnalysisModelRepositoryCommitLossIntegrationVariants(t, func(
			t *testing.T,
			pool *pgxpool.Pool,
			ctx context.Context,
			repository workspaceAnalysisModelRepositoryIntegrationStore,
			armCommitLoss func(),
		) {
			fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
			armCommitLoss()
			recovered, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
			if err != nil || recovered.Disposition != application.WorkspaceAnalysisModelAuthorizationReconcile ||
				recovered.OperationID != fixture.command.OperationID || recovered.ReservationID != fixture.command.ReservationID ||
				recovered.Run.ID != fixture.command.Run.ID || recovered.Call.ID != fixture.command.Call.ID {
				t.Fatalf("authorization response-loss recovery=%#v err=%v", recovered, err)
			}
		})
	})

	t.Run("call finalization", func(t *testing.T) {
		testWorkspaceAnalysisModelRepositoryCommitLossIntegrationVariants(t, func(
			t *testing.T,
			pool *pgxpool.Pool,
			ctx context.Context,
			repository workspaceAnalysisModelRepositoryIntegrationStore,
			armCommitLoss func(),
		) {
			fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
			authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
			if err != nil {
				t.Fatal(err)
			}
			command := terminalWorkspaceAnalysisModelCallCommand(
				authorized, fixture.command, domain.ModelCallFailed, domain.ModelRunFailed, "", "MODEL_PROVIDER_FAILED",
			)
			armCommitLoss()
			recovered, err := repository.FinalizeWorkspaceAnalysisModelCall(ctx, command)
			if err != nil || !recovered.Replayed || recovered.OperationID != authorized.OperationID ||
				recovered.ReservationID != authorized.ReservationID || recovered.Call.Status != domain.ModelCallFailed ||
				recovered.Run.Status != domain.ModelRunFailed {
				t.Fatalf("call response-loss recovery=%#v err=%v", recovered, err)
			}
		})
	})

	t.Run("candidate finalization", func(t *testing.T) {
		testWorkspaceAnalysisModelRepositoryCommitLossIntegrationVariants(t, func(
			t *testing.T,
			pool *pgxpool.Pool,
			ctx context.Context,
			repository workspaceAnalysisModelRepositoryIntegrationStore,
			armCommitLoss func(),
		) {
			fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
			planAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx,
				successfulWorkspaceAnalysisModelResultCommand(t, fixture, planAuthorization)); err != nil {
				t.Fatal(err)
			}
			seedWorkspaceAnalysisModelEvidencePrefixIntegration(t, ctx, pool)
			authorizationCommand := synthesisWorkspaceAnalysisModelAuthorizationCommand(fixture)
			authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, authorizationCommand)
			if err != nil {
				t.Fatal(err)
			}
			command := successfulWorkspaceAnalysisModelCandidateCommand(t, fixture, authorized, authorizationCommand)
			armCommitLoss()
			recovered, err := repository.FinalizeWorkspaceAnalysisModelCandidate(ctx, command)
			if err != nil || !recovered.Replayed || recovered.Candidate == nil ||
				!sameWorkspaceAnalysisCandidate(*recovered.Candidate, command.Candidate) {
				t.Fatalf("candidate response-loss recovery=%#v err=%v", recovered, err)
			}
		})
	})
}

func TestGORMWorkspaceAnalysisRepositoryRejectsMissingFenceIntegration(t *testing.T) {
	platform, _ := newAgentPlatformIntegrationPool(t)
	if _, err := NewGORMWorkspaceAnalysisRepository(platform, nil); agentErrorCode(err) != application.ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable {
		t.Fatalf("nil fence code=%s err=%v", agentErrorCode(err), err)
	}
	var typedNil *workspaceAnalysisModelNilExecutionFence
	if _, err := NewGORMWorkspaceAnalysisRepository(platform, typedNil); agentErrorCode(err) != application.ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable {
		t.Fatalf("typed nil fence code=%s err=%v", agentErrorCode(err), err)
	}
}

func TestGORMWorkspaceAnalysisModelOperationPreservesCallerCancellationIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	pool := platform.DB()
	fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
	repository := openGORMWorkspaceAnalysisModelRepositoryIntegration(t, platform)

	cancelCause := errors.New("workspace analysis model caller stopped")
	canceledCtx, cancel := context.WithCancelCause(ctx)
	cancel(cancelCause)
	_, err := repository.AuthorizeWorkspaceAnalysisModelCall(canceledCtx, fixture.command)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "AGENT_DATABASE_CANCELLED" ||
		classified.Kind != foundation.ErrorNonRetryableFailure || classified.Retryable ||
		!errors.Is(err, context.Canceled) || !errors.Is(err, cancelCause) {
		t.Fatalf("canceled authorization err=%#v", err)
	}
	assertWorkspaceAnalysisConcurrentModelFacts(t, ctx, pool, fixture.command, false)

	result, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
	if err != nil || result.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
		t.Fatalf("authorization after cancellation=%#v err=%v", result, err)
	}
}

func TestWorkspaceAnalysisCapabilityLifecycleParityIntegration(t *testing.T) {
	for _, variant := range workspaceAnalysisCapabilityIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			repository, requireReady := variant.open(t, platform)
			contract := workspaceAnalysisCapabilityContractIntegration()
			first := workspaceAnalysisCapabilityAdvertisementIntegration(201, contract)
			second := workspaceAnalysisCapabilityAdvertisementIntegration(202, contract)

			created, err := repository.AdvertiseWorkspaceAnalysisWorker(ctx, first)
			if err != nil || created.Version != 1 || created.ReleasedAt != nil ||
				!created.HeartbeatAt.Equal(created.CreatedAt) || !created.HeartbeatAt.Equal(created.UpdatedAt) ||
				created.LeaseUntil.Sub(created.HeartbeatAt) != first.LeaseDuration {
				t.Fatalf("first advertise=%#v err=%v", created, err)
			}
			replayed, err := repository.AdvertiseWorkspaceAnalysisWorker(ctx, first)
			if err != nil || replayed.Version != 2 || replayed.HeartbeatAt.Before(created.HeartbeatAt) {
				t.Fatalf("advertise replay=%#v err=%v", replayed, err)
			}
			heartbeated, err := repository.HeartbeatWorkspaceAnalysisWorker(ctx, first)
			if err != nil || heartbeated.Version != 3 || heartbeated.HeartbeatAt.Before(replayed.HeartbeatAt) ||
				heartbeated.LeaseUntil.Sub(heartbeated.HeartbeatAt) != first.LeaseDuration {
				t.Fatalf("heartbeat=%#v err=%v", heartbeated, err)
			}
			if err := requireReady(ctx, contract); err != nil {
				t.Fatalf("exact ready check: %v", err)
			}
			drifted := contract
			drifted.ConfigRevision++
			if err := requireReady(ctx, drifted); agentErrorCode(err) != application.ErrorCodeWorkspaceAnalysisCapabilityUnavailable {
				t.Fatalf("drifted ready code=%s err=%v", agentErrorCode(err), err)
			}
			if _, err := repository.AdvertiseWorkspaceAnalysisWorker(ctx, second); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.ReleaseWorkspaceAnalysisWorker(ctx, first); err != nil {
				t.Fatal(err)
			}
			if err := requireReady(ctx, contract); err != nil {
				t.Fatalf("second worker did not preserve readiness: %v", err)
			}
			if _, err := repository.ReleaseWorkspaceAnalysisWorker(ctx, second); err != nil {
				t.Fatal(err)
			}
			if err := requireReady(ctx, contract); agentErrorCode(err) != application.ErrorCodeWorkspaceAnalysisCapabilityUnavailable {
				t.Fatalf("released ready code=%s err=%v", agentErrorCode(err), err)
			}
		})
	}
}

type workspaceAnalysisCapabilityIntegrationStore interface {
	application.WorkspaceAnalysisCapabilityLifecyclePort
}

type workspaceAnalysisCapabilityIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool) (
		workspaceAnalysisCapabilityIntegrationStore,
		func(context.Context, application.WorkspaceAnalysisCapabilityContract) error,
	)
}

func workspaceAnalysisCapabilityIntegrationVariants() []workspaceAnalysisCapabilityIntegrationVariant {
	return []workspaceAnalysisCapabilityIntegrationVariant{
		{name: "legacy", open: func(t *testing.T, platform *platformpostgres.Pool) (
			workspaceAnalysisCapabilityIntegrationStore,
			func(context.Context, application.WorkspaceAnalysisCapabilityContract) error,
		) {
			t.Helper()
			repository, err := NewRepository(platform.DB())
			if err != nil {
				t.Fatal(err)
			}
			return repository, func(ctx context.Context, contract application.WorkspaceAnalysisCapabilityContract) error {
				tx, err := platform.DB().Begin(ctx)
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				return repository.RequireWorkspaceAnalysisWorkerReadyTx(ctx, tx, contract)
			}
		}},
		{name: "gorm", open: func(t *testing.T, platform *platformpostgres.Pool) (
			workspaceAnalysisCapabilityIntegrationStore,
			func(context.Context, application.WorkspaceAnalysisCapabilityContract) error,
		) {
			t.Helper()
			repository, err := NewGORMRepository(platform)
			if err != nil {
				t.Fatal(err)
			}
			return repository, func(ctx context.Context, contract application.WorkspaceAnalysisCapabilityContract) error {
				return repository.unitOfWork.Within(ctx, foundation.TransactionOptions{ReadOnly: true}, func(
					callbackCtx context.Context,
					scope foundation.TransactionScope,
				) error {
					return repository.RequireWorkspaceAnalysisWorkerReadyScoped(callbackCtx, scope, contract)
				})
			}
		}},
	}
}

func workspaceAnalysisCapabilityContractIntegration() application.WorkspaceAnalysisCapabilityContract {
	return application.WorkspaceAnalysisCapabilityContract{
		DefinitionKey: "workspace-analysis", DefinitionVersion: 1,
		DefinitionHash: hash64('a'), ToolCatalogHash: hash64('b'),
		PolicyVersion: 1, ConfigRevision: 7,
	}
}

func workspaceAnalysisCapabilityAdvertisementIntegration(
	seed int,
	contract application.WorkspaceAnalysisCapabilityContract,
) application.WorkspaceAnalysisWorkerAdvertisement {
	return application.WorkspaceAnalysisWorkerAdvertisement{
		WorkerInstanceID: workspaceAnalysisModelIntegrationID(seed),
		Contract:         contract,
		LeaseDuration:    application.DefaultWorkspaceAnalysisWorkerCapabilityLease,
	}
}

func TestWorkspaceAnalysisCapabilityCheckedRunStarterAtomicityIntegration(t *testing.T) {
	for _, variant := range workspaceAnalysisRunStarterIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			config, contract, command := seedWorkspaceAnalysisRunStartIntegration(t, ctx, platform.DB())
			harness := variant.open(t, platform, config, contract)
			advertisement := workspaceAnalysisCapabilityAdvertisementIntegration(211, contract)

			if _, err := harness.start(ctx, command, false); agentErrorCode(err) != application.ErrorCodeWorkspaceAnalysisCapabilityUnavailable {
				t.Fatalf("run without capability code=%s err=%v", agentErrorCode(err), err)
			}
			assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 0)
			if _, err := harness.capability.AdvertiseWorkspaceAnalysisWorker(ctx, advertisement); err != nil {
				t.Fatal(err)
			}

			rolledBack, err := harness.start(ctx, command, true)
			if err != nil || rolledBack.ID != workspaceAnalysisRunStartIntegrationID(20) {
				t.Fatalf("rolled-back start=%#v err=%v", rolledBack, err)
			}
			assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 0)

			created, err := harness.start(ctx, command, false)
			if err != nil || created.ID != rolledBack.ID || created.Status != domain.WorkspaceAnalysisRunQueued || created.Version != 1 {
				t.Fatalf("committed start=%#v err=%v", created, err)
			}
			assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 1)
			replay := command
			replay.Replayed = true
			replayed, err := harness.start(ctx, replay, false)
			if err != nil || replayed.ID != created.ID || replayed.DefinitionHash != created.DefinitionHash ||
				replayed.ToolCatalogHash != created.ToolCatalogHash || replayed.ConfigRevision != created.ConfigRevision {
				t.Fatalf("run replay=%#v err=%v", replayed, err)
			}
			loaded, err := harness.loader.LoadWorkspaceAnalysisRunForExecution(ctx, application.WorkspaceAnalysisRunExecutionQuery{
				WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID,
				ConversationID: command.ConversationID, QuestionID: command.QuestionID, AnswerID: command.AnswerID,
			})
			if err != nil || loaded.ID != created.ID {
				t.Fatalf("execution load=%#v err=%v", loaded, err)
			}

			if _, err := harness.capability.ReleaseWorkspaceAnalysisWorker(ctx, advertisement); err != nil {
				t.Fatal(err)
			}
			if _, err := harness.start(ctx, replay, false); agentErrorCode(err) != application.ErrorCodeWorkspaceAnalysisCapabilityUnavailable {
				t.Fatalf("replay after release code=%s err=%v", agentErrorCode(err), err)
			}
			assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 1)
		})
	}
}

type workspaceAnalysisRunStarterIntegrationHarness struct {
	capability workspaceAnalysisCapabilityIntegrationStore
	loader     application.WorkspaceAnalysisRunLoader
	start      func(context.Context, application.WorkspaceAnalysisRunStartCommand, bool) (domain.WorkspaceAnalysisRun, error)
}

type workspaceAnalysisRunStarterIntegrationVariant struct {
	name string
	open func(
		*testing.T,
		*platformpostgres.Pool,
		application.WorkspaceAnalysisRunStartConfig,
		application.WorkspaceAnalysisCapabilityContract,
	) workspaceAnalysisRunStarterIntegrationHarness
}

var errWorkspaceAnalysisRunStartIntegrationRollback = errors.New("rollback workspace analysis run start integration transaction")

func workspaceAnalysisRunStarterIntegrationVariants() []workspaceAnalysisRunStarterIntegrationVariant {
	return []workspaceAnalysisRunStarterIntegrationVariant{
		{name: "legacy", open: func(
			t *testing.T,
			platform *platformpostgres.Pool,
			config application.WorkspaceAnalysisRunStartConfig,
			contract application.WorkspaceAnalysisCapabilityContract,
		) workspaceAnalysisRunStarterIntegrationHarness {
			t.Helper()
			repository, err := NewRepository(platform.DB())
			if err != nil {
				t.Fatal(err)
			}
			service, err := application.NewWorkspaceAnalysisRunService(
				repository,
				workspaceAnalysisRunStartIntegrationIDGenerator{id: workspaceAnalysisRunStartIntegrationID(20)},
				config,
			)
			if err != nil {
				t.Fatal(err)
			}
			starter, err := application.NewWorkspaceAnalysisCapabilityCheckedRunStarter(repository, service, contract)
			if err != nil {
				t.Fatal(err)
			}
			return workspaceAnalysisRunStarterIntegrationHarness{
				capability: repository,
				loader:     repository,
				start: func(ctx context.Context, command application.WorkspaceAnalysisRunStartCommand, rollback bool) (domain.WorkspaceAnalysisRun, error) {
					tx, err := platform.DB().Begin(ctx)
					if err != nil {
						return domain.WorkspaceAnalysisRun{}, err
					}
					defer func() { _ = tx.Rollback(context.Background()) }()
					if _, err := tx.Exec(ctx, workspaceAnalysisRunStartAnswerSQL(false), workspaceAnalysisRunStartAnswerArguments(command)...); err != nil {
						return domain.WorkspaceAnalysisRun{}, err
					}
					run, err := starter.StartWorkspaceAnalysisRunTx(ctx, tx, command)
					if err != nil {
						return domain.WorkspaceAnalysisRun{}, err
					}
					if rollback {
						if err := tx.Rollback(context.Background()); err != nil {
							return domain.WorkspaceAnalysisRun{}, err
						}
						return run, nil
					}
					if err := tx.Commit(ctx); err != nil {
						return domain.WorkspaceAnalysisRun{}, err
					}
					return run, nil
				},
			}
		}},
		{name: "gorm", open: func(
			t *testing.T,
			platform *platformpostgres.Pool,
			config application.WorkspaceAnalysisRunStartConfig,
			contract application.WorkspaceAnalysisCapabilityContract,
		) workspaceAnalysisRunStarterIntegrationHarness {
			t.Helper()
			repository, err := NewGORMRepository(platform)
			if err != nil {
				t.Fatal(err)
			}
			service, err := application.NewScopedWorkspaceAnalysisRunService(
				repository,
				workspaceAnalysisRunStartIntegrationIDGenerator{id: workspaceAnalysisRunStartIntegrationID(20)},
				config,
			)
			if err != nil {
				t.Fatal(err)
			}
			starter, err := application.NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter(repository, service, contract)
			if err != nil {
				t.Fatal(err)
			}
			return workspaceAnalysisRunStarterIntegrationHarness{
				capability: repository,
				loader:     repository,
				start: func(ctx context.Context, command application.WorkspaceAnalysisRunStartCommand, rollback bool) (domain.WorkspaceAnalysisRun, error) {
					var run domain.WorkspaceAnalysisRun
					err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(
						callbackCtx context.Context,
						scope foundation.TransactionScope,
					) error {
						transaction, err := platformpostgres.GORMTransaction(scope)
						if err != nil {
							return err
						}
						if result := transaction.WithContext(callbackCtx).Exec(
							workspaceAnalysisRunStartAnswerSQL(true),
							workspaceAnalysisRunStartAnswerArguments(command)...,
						); result.Error != nil {
							return result.Error
						}
						run, err = starter.StartWorkspaceAnalysisRunScoped(callbackCtx, scope, command)
						if err != nil {
							return err
						}
						if rollback {
							return errWorkspaceAnalysisRunStartIntegrationRollback
						}
						return nil
					})
					if rollback && errors.Is(err, errWorkspaceAnalysisRunStartIntegrationRollback) {
						return run, nil
					}
					return run, err
				},
			}
		}},
	}
}

type workspaceAnalysisRunStartIntegrationIDGenerator struct {
	id foundation.ID
}

func (generator workspaceAnalysisRunStartIntegrationIDGenerator) New() (foundation.ID, error) {
	return generator.id, nil
}

func seedWorkspaceAnalysisRunStartIntegration(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (
	application.WorkspaceAnalysisRunStartConfig,
	application.WorkspaceAnalysisCapabilityContract,
	application.WorkspaceAnalysisRunStartCommand,
) {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID, conversationID := workspaceAnalysisRunStartIntegrationID(1), workspaceAnalysisRunStartIntegrationID(2)
	questionID, definitionID := workspaceAnalysisRunStartIntegrationID(3), workspaceAnalysisRunStartIntegrationID(4)
	workflowRunID, answerID := workspaceAnalysisRunStartIntegrationID(5), workspaceAnalysisRunStartIntegrationID(6)
	queries := []struct {
		statement string
		arguments []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
			VALUES($1,'wa-run-start','/tmp/wa-run-start','/tmp/wa-run-start',clock_timestamp(),'active',clock_timestamp(),clock_timestamp())`, []any{string(workspaceID)}},
		{`INSERT INTO agent.conversation(
			id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash
		) VALUES($1,$2,'open','Workspace Analysis Run Start',1,clock_timestamp(),clock_timestamp(),clock_timestamp(),'wa-run-start',repeat('1',64))`, []any{string(conversationID), string(workspaceID)}},
		{`INSERT INTO agent.question(
			id,workspace_id,conversation_id,ordinal,mode,question_text,scope,answer_depth,output_format,
			context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
		) VALUES($1,$2,$3,1,'workspace_analysis','analyze workspace','{}','standard','markdown',0,repeat('2',64),'wa-run-question',repeat('3',64),clock_timestamp())`, []any{string(questionID), string(workspaceID), string(conversationID)}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES($1,$2,'workspace-analysis',1,'{"nodes":[]}'::jsonb,clock_timestamp())`, []any{string(definitionID), string(workspaceID)}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,clock_timestamp(),clock_timestamp())`, []any{string(workflowRunID), string(workspaceID), string(definitionID)}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.statement, query.arguments...); err != nil {
			t.Fatal(err)
		}
	}
	config := application.WorkspaceAnalysisRunStartConfig{
		DefinitionHash: hash64('a'), ToolCatalogHash: hash64('b'), ConfigRevision: 7,
		SynthesisProfileMaxOutputTokens: int(application.WorkspaceAnalysisV1SynthesisMaxOutputTokens),
		Timeouts: application.WorkspaceAnalysisV1Timeouts{
			PlanModelTimeout: 2 * time.Second, SynthesisModelTimeout: 3 * time.Second, ReviewModelTimeout: 2 * time.Second,
			GitToolTimeout: time.Second, SearchToolTimeout: time.Second, SourceReadToolTimeout: time.Second,
			ValidateCitationToolTimeout: time.Second,
		},
		RuntimeLimits: application.WorkspaceAnalysisRuntimeLimits{
			RiverJobTimeout: time.Minute, LeaseDuration: 30 * time.Second, HeartbeatInterval: 5 * time.Second,
		},
	}
	contract := workspaceAnalysisCapabilityContractIntegration()
	command := application.WorkspaceAnalysisRunStartCommand{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionID: questionID, AnswerID: answerID,
		WorkflowRunID: workflowRunID, CreatedAt: createdAt,
	}
	return config, contract, command
}

func assertWorkspaceAnalysisDispatchCountsIntegration(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	questionID foundation.ID,
	want int,
) {
	t.Helper()
	var answers, runs int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.answer WHERE question_id=$1),
		(SELECT count(*) FROM agent.workspace_analysis_run WHERE question_id=$1)`, string(questionID)).Scan(&answers, &runs); err != nil {
		t.Fatal(err)
	}
	if answers != want || runs != want {
		t.Fatalf("workspace analysis dispatch facts answers=%d runs=%d, want %d each", answers, runs, want)
	}
}

func workspaceAnalysisRunStartAnswerSQL(gorm bool) string {
	if gorm {
		return `INSERT INTO agent.answer(
			id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at
		) SELECT ?,?,?,?,?,'pending',1,clock_timestamp(),clock_timestamp()
		WHERE NOT EXISTS (SELECT 1 FROM agent.answer WHERE id=?::uuid)`
	}
	return `INSERT INTO agent.answer(
		id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at
	) SELECT $1,$2,$3,$4,$5,'pending',1,clock_timestamp(),clock_timestamp()
	WHERE NOT EXISTS (SELECT 1 FROM agent.answer WHERE id=$6::uuid)`
}

func workspaceAnalysisRunStartAnswerArguments(command application.WorkspaceAnalysisRunStartCommand) []any {
	return []any{
		string(command.AnswerID), string(command.WorkspaceID), string(command.ConversationID),
		string(command.QuestionID), string(command.WorkflowRunID), string(command.AnswerID),
	}
}

func workspaceAnalysisRunStartIntegrationID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("86000000-0000-4000-8000-%012d", seed))
}

type workspaceAnalysisModelNilExecutionFence struct{}

func (*workspaceAnalysisModelNilExecutionFence) LockWorkspaceAnalysisExecutionScoped(
	context.Context,
	foundation.TransactionScope,
	workflowapplication.WorkspaceAnalysisExecutionFenceRequest,
) (workflowapplication.WorkspaceAnalysisExecutionFenceSnapshot, bool, error) {
	panic("typed nil execution fence must be rejected by the constructor")
}

func TestWorkspaceAnalysisModelOperationRejectsStaleFenceAndDeadlineIntegration(t *testing.T) {
	tests := []struct {
		name              string
		runAge            time.Duration
		mutate            func(*application.AuthorizeWorkspaceAnalysisModelCallCommand)
		wantCode          string
		wantKind          foundation.ErrorKind
		wantDeadlineCause bool
	}{
		{
			name: "stale fence",
			mutate: func(command *application.AuthorizeWorkspaceAnalysisModelCallCommand) {
				command.Identity.LeaseFence++
			},
			wantCode: application.ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid,
			wantKind: foundation.ErrorVersionConflict,
		},
		{
			name: "deadline exhausted", runAge: 13 * time.Minute,
			wantCode: domain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline,
			wantKind: foundation.ErrorNonRetryableFailure, wantDeadlineCause: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, func(
				t *testing.T,
				pool *pgxpool.Pool,
				ctx context.Context,
				repository workspaceAnalysisModelRepositoryIntegrationStore,
			) {
				fixture := seedWorkspaceAnalysisModelOperationIntegrationWithRunAge(t, ctx, pool, test.runAge)
				if test.mutate != nil {
					test.mutate(&fixture.command)
				}
				_, authorizationErr := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
				var classified *foundation.Error
				if !errors.As(authorizationErr, &classified) || classified.Code != test.wantCode ||
					classified.Kind != test.wantKind || classified.Retryable ||
					errors.Is(authorizationErr, context.DeadlineExceeded) != test.wantDeadlineCause {
					t.Fatalf("authorization err=%#v", authorizationErr)
				}
				var runtimeFacts int64
				if err := pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM agent.model_run WHERE id=$1)+
				(SELECT count(*) FROM agent.model_call WHERE id=$2)+
				(SELECT count(*) FROM agent.workspace_analysis_operation WHERE id=$3)+
				(SELECT count(*) FROM agent.workspace_analysis_budget_reservation WHERE id=$4)`,
					string(fixture.command.Run.ID), string(fixture.command.Call.ID),
					string(fixture.command.OperationID), string(fixture.command.ReservationID),
				).Scan(&runtimeFacts); err != nil {
					t.Fatal(err)
				}
				if runtimeFacts != 0 {
					t.Fatalf("rejected authorization persisted %d runtime facts", runtimeFacts)
				}
			})
		})
	}
}

func TestWorkspaceAnalysisModelOperationSuccessReplayLoadScopeAndCommitRecoveryIntegration(t *testing.T) {
	for _, variant := range workspaceAnalysisModelRepositoryIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			repository, armCommitLoss := variant.openWithCommitLoss(t, platform)
			testWorkspaceAnalysisModelOperationSuccessReplayLoadScopeAndCommitRecoveryIntegration(
				t, platform.DB(), ctx, repository, armCommitLoss,
			)
		})
	}
}

func testWorkspaceAnalysisModelOperationSuccessReplayLoadScopeAndCommitRecoveryIntegration(
	t *testing.T,
	pool *pgxpool.Pool,
	ctx context.Context,
	repository workspaceAnalysisModelRepositoryIntegrationStore,
	armCommitLoss func(),
) {
	fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
	checkpointQuery := application.WorkspaceAnalysisRetrievalPlanCheckpointQuery{
		WorkspaceID: fixture.command.Identity.WorkspaceID, WorkflowRunID: fixture.command.Identity.WorkflowRunID,
		AnalysisRunID: fixture.command.OperationKey.AnalysisRunID, NodeRunID: fixture.command.Identity.NodeRunID,
	}
	if checkpoint, found, err := repository.FindWorkspaceAnalysisRetrievalPlanCheckpoint(ctx, checkpointQuery); err != nil || found {
		t.Fatalf("checkpoint before authorization=%#v found=%t err=%v", checkpoint, found, err)
	}
	authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	command := successfulWorkspaceAnalysisModelResultCommand(t, fixture, authorized)
	armCommitLoss()
	mutation, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx, command)
	if err != nil || !mutation.Replayed || mutation.Result == nil {
		t.Fatalf("commit-loss finalization=%#v err=%v", mutation, err)
	}
	checkpoint, found, err := repository.FindWorkspaceAnalysisRetrievalPlanCheckpoint(ctx, checkpointQuery)
	if err != nil || !found || checkpoint.OperationID != authorized.OperationID || checkpoint.ModelRunID != authorized.Run.ID ||
		checkpoint.ModelCallID != authorized.Call.ID || checkpoint.ModelResultID != command.Result.ID ||
		checkpoint.RequestHash != authorized.Call.RequestHash || checkpoint.ResultHash != command.Result.DocumentHash ||
		checkpoint.Status != domain.WorkspaceAnalysisOperationSucceeded || !sameRetrieval(checkpoint.Retrieval, authorized.Run.Retrieval) {
		t.Fatalf("persisted checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	checkpointCrossWorkspace := checkpointQuery
	checkpointCrossWorkspace.WorkspaceID = workspaceAnalysisModelIntegrationID(99)
	if checkpoint, found, err := repository.FindWorkspaceAnalysisRetrievalPlanCheckpoint(ctx, checkpointCrossWorkspace); err != nil || found {
		t.Fatalf("cross-workspace checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}

	replayed, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Result == nil ||
		!sameWorkspaceAnalysisModelResult(*replayed.Result, command.Result) {
		t.Fatalf("success replay=%#v err=%v", replayed, err)
	}
	query := application.WorkspaceAnalysisModelResultQuery{
		WorkspaceID: command.Result.WorkspaceID, AnalysisRunID: command.Result.AnalysisRunID,
		OperationID: command.Result.OperationID, ModelRunID: command.Result.ModelRunID,
		ModelCallID: command.Result.ModelCallID,
	}
	loaded, err := repository.LoadWorkspaceAnalysisModelResult(ctx, query)
	if err != nil || !sameWorkspaceAnalysisModelResult(loaded, command.Result) {
		t.Fatalf("exact result load=%#v err=%v", loaded, err)
	}
	crossWorkspace := query
	crossWorkspace.WorkspaceID = workspaceAnalysisModelIntegrationID(99)
	if _, err := repository.LoadWorkspaceAnalysisModelResult(ctx, crossWorkspace); agentErrorCode(err) != ErrorCodeRuntimeNotFound {
		t.Fatalf("cross-workspace load code=%s err=%v", agentErrorCode(err), err)
	}
	driftedCall := query
	driftedCall.ModelCallID = workspaceAnalysisModelIntegrationID(98)
	if _, err := repository.LoadWorkspaceAnalysisModelResult(ctx, driftedCall); agentErrorCode(err) != ErrorCodeRuntimeNotFound {
		t.Fatalf("drifted-call load code=%s err=%v", agentErrorCode(err), err)
	}
}

func TestWorkspaceAnalysisModelOperationCandidateReplayAndLoadIntegration(t *testing.T) {
	testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, testWorkspaceAnalysisModelOperationCandidateReplayAndLoadIntegration)
}

func testWorkspaceAnalysisModelOperationCandidateReplayAndLoadIntegration(
	t *testing.T,
	pool *pgxpool.Pool,
	ctx context.Context,
	repository workspaceAnalysisModelRepositoryIntegrationStore,
) {
	fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)

	planAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx,
		successfulWorkspaceAnalysisModelResultCommand(t, fixture, planAuthorization)); err != nil {
		t.Fatal(err)
	}
	seedWorkspaceAnalysisModelEvidencePrefixIntegration(t, ctx, pool)

	authorizationCommand := synthesisWorkspaceAnalysisModelAuthorizationCommand(fixture)
	authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, authorizationCommand)
	if err != nil || authorized.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
		t.Fatalf("synthesis authorization=%#v err=%v", authorized, err)
	}
	command := successfulWorkspaceAnalysisModelCandidateCommand(t, fixture, authorized, authorizationCommand)
	mutation, err := repository.FinalizeWorkspaceAnalysisModelCandidate(ctx, command)
	if err != nil || mutation.Replayed || mutation.Candidate == nil {
		t.Fatalf("candidate finalization=%#v err=%v", mutation, err)
	}
	replayed, err := repository.FinalizeWorkspaceAnalysisModelCandidate(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Candidate == nil ||
		!sameWorkspaceAnalysisCandidate(*replayed.Candidate, command.Candidate) {
		t.Fatalf("candidate replay=%#v err=%v", replayed, err)
	}

	query := application.WorkspaceAnalysisCandidateQuery{
		WorkspaceID: command.Candidate.WorkspaceID, AnalysisRunID: command.Candidate.AnalysisRunID,
		OperationID: command.Candidate.SynthesisOperationID, ModelRunID: command.Candidate.SynthesisModelRunID,
		ModelCallID: command.Call.ID,
	}
	loaded, err := repository.LoadWorkspaceAnalysisCandidate(ctx, query)
	if err != nil || !sameWorkspaceAnalysisCandidate(loaded, command.Candidate) {
		t.Fatalf("exact candidate load=%#v err=%v", loaded, err)
	}
	authorityQuery := application.WorkspaceAnalysisCandidateAuthorityQuery{
		WorkspaceID: command.Candidate.WorkspaceID, WorkflowRunID: fixture.command.Identity.WorkflowRunID,
		AnalysisRunID: command.Candidate.AnalysisRunID, CandidateID: command.Candidate.ID,
		CandidateHash: command.Candidate.DocumentHash,
	}
	authoritative, err := repository.LoadWorkspaceAnalysisCandidateAuthority(ctx, authorityQuery)
	if err != nil || !sameWorkspaceAnalysisCandidate(authoritative, command.Candidate) {
		t.Fatalf("candidate authority=%#v err=%v", authoritative, err)
	}
	driftedWorkflow := authorityQuery
	driftedWorkflow.WorkflowRunID = workspaceAnalysisModelIntegrationID(98)
	if _, err := repository.LoadWorkspaceAnalysisCandidateAuthority(ctx, driftedWorkflow); agentErrorCode(err) != ErrorCodeRuntimeNotFound {
		t.Fatalf("drifted-workflow authority code=%s err=%v", agentErrorCode(err), err)
	}
	crossWorkspace := query
	crossWorkspace.WorkspaceID = workspaceAnalysisModelIntegrationID(99)
	if _, err := repository.LoadWorkspaceAnalysisCandidate(ctx, crossWorkspace); agentErrorCode(err) != ErrorCodeRuntimeNotFound {
		t.Fatalf("cross-workspace candidate load code=%s err=%v", agentErrorCode(err), err)
	}
	driftedCall := query
	driftedCall.ModelCallID = workspaceAnalysisModelIntegrationID(98)
	if _, err := repository.LoadWorkspaceAnalysisCandidate(ctx, driftedCall); agentErrorCode(err) != ErrorCodeRuntimeNotFound {
		t.Fatalf("drifted-call candidate load code=%s err=%v", agentErrorCode(err), err)
	}
}

func TestWorkspaceAnalysisModelOperationReviewPassedFailedAndCandidateBindingIntegration(t *testing.T) {
	for _, passed := range []bool{true, false} {
		name := "failed"
		if passed {
			name = "passed"
		}
		t.Run(name, func(t *testing.T) {
			testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, func(
				t *testing.T,
				pool *pgxpool.Pool,
				ctx context.Context,
				repository workspaceAnalysisModelRepositoryIntegrationStore,
			) {
				fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
				planAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx,
					successfulWorkspaceAnalysisModelResultCommand(t, fixture, planAuthorization)); err != nil {
					t.Fatal(err)
				}
				seedWorkspaceAnalysisModelEvidencePrefixIntegration(t, ctx, pool)
				synthesisAuthorizationCommand := synthesisWorkspaceAnalysisModelAuthorizationCommand(fixture)
				synthesisAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, synthesisAuthorizationCommand)
				if err != nil {
					t.Fatal(err)
				}
				candidateCommand := successfulWorkspaceAnalysisModelCandidateCommand(
					t, fixture, synthesisAuthorization, synthesisAuthorizationCommand,
				)
				candidateMutation, err := repository.FinalizeWorkspaceAnalysisModelCandidate(ctx, candidateCommand)
				if err != nil || candidateMutation.Candidate == nil {
					t.Fatalf("candidate finalization=%#v err=%v", candidateMutation, err)
				}
				seedWorkspaceAnalysisModelCitationValidationIntegration(t, ctx, pool, candidateCommand.Candidate)

				reviewAuthorizationCommand := reviewWorkspaceAnalysisModelAuthorizationCommand(t, fixture, candidateCommand.Candidate)
				reviewAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, reviewAuthorizationCommand)
				if err != nil || reviewAuthorization.Disposition != application.WorkspaceAnalysisModelAuthorizationCreated {
					t.Fatalf("review authorization=%#v err=%v", reviewAuthorization, err)
				}
				command := successfulWorkspaceAnalysisModelReviewCommand(
					t, reviewAuthorization, reviewAuthorizationCommand, candidateCommand.Candidate, passed,
				)
				if passed {
					drifted := command
					drifted.Result.SubjectCandidateHash = strings.Repeat("c", 64)
					if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx, drifted); agentErrorCode(err) != ErrorCodeRuntimeConsistency {
						t.Fatalf("drifted candidate hash code=%s err=%v", agentErrorCode(err), err)
					}
				}
				mutation, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx, command)
				if err != nil || mutation.Replayed || mutation.Result == nil {
					t.Fatalf("review finalization=%#v err=%v", mutation, err)
				}
				replayed, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx, command)
				if err != nil || !replayed.Replayed || replayed.Result == nil ||
					!sameWorkspaceAnalysisModelResult(*replayed.Result, command.Result) {
					t.Fatalf("review replay=%#v err=%v", replayed, err)
				}
				query := application.WorkspaceAnalysisModelResultQuery{
					WorkspaceID: command.Result.WorkspaceID, AnalysisRunID: command.Result.AnalysisRunID,
					OperationID: command.Result.OperationID, ModelRunID: command.Result.ModelRunID,
					ModelCallID: command.Result.ModelCallID,
				}
				loaded, err := repository.LoadWorkspaceAnalysisModelResult(ctx, query)
				if err != nil || !sameWorkspaceAnalysisModelResult(loaded, command.Result) ||
					loaded.SubjectCandidateID == nil || *loaded.SubjectCandidateID != candidateCommand.Candidate.ID ||
					loaded.SubjectCandidateHash != candidateCommand.Candidate.DocumentHash {
					t.Fatalf("exact review load=%#v err=%v", loaded, err)
				}
			})
		})
	}
}

func TestWorkspaceAnalysisModelOperationFailureRefusalAndUnknownClosureIntegration(t *testing.T) {
	tests := []struct {
		name              string
		callStatus        domain.ModelCallStatus
		runStatus         domain.ModelRunStatus
		resultType        string
		errorCode         string
		wantOperation     string
		wantReservation   string
		wantSettledInput  int64
		wantSettledOutput int64
	}{
		{
			name: "failed", callStatus: domain.ModelCallFailed, runStatus: domain.ModelRunFailed,
			errorCode: "MODEL_PROVIDER_FAILED", wantOperation: "FAILED", wantReservation: "SETTLED",
		},
		{
			name: "refused", callStatus: domain.ModelCallSucceeded, runStatus: domain.ModelRunRefused,
			resultType: domain.ResultTypeRefusal, errorCode: string(domain.WorkspaceAnalysisRunModelRefused),
			wantOperation: "FAILED", wantReservation: "SETTLED", wantSettledInput: 8, wantSettledOutput: 4,
		},
		{
			name: "unknown", callStatus: domain.ModelCallUnknown, runStatus: domain.ModelRunUnknown,
			errorCode: string(domain.WorkspaceAnalysisRunResultUnknown), wantOperation: "UNKNOWN", wantReservation: "UNKNOWN_CHARGED",
			wantSettledInput:  domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			wantSettledOutput: domain.WorkspaceAnalysisV1PlanMaxOutputTokens,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, func(
				t *testing.T,
				pool *pgxpool.Pool,
				ctx context.Context,
				repository workspaceAnalysisModelRepositoryIntegrationStore,
			) {
				fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
				authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
				if err != nil {
					t.Fatal(err)
				}
				command := terminalWorkspaceAnalysisModelCallCommand(authorized, fixture.command, test.callStatus,
					test.runStatus, test.resultType, test.errorCode)
				mutation, err := repository.FinalizeWorkspaceAnalysisModelCall(ctx, command)
				if err != nil || mutation.Replayed {
					t.Fatalf("finalization=%#v err=%v", mutation, err)
				}
				replayed, err := repository.FinalizeWorkspaceAnalysisModelCall(ctx, command)
				if err != nil || !replayed.Replayed {
					t.Fatalf("replay=%#v err=%v", replayed, err)
				}
				var operationStatus, reservationStatus string
				var settledInput, settledOutput int64
				if err := pool.QueryRow(ctx, `SELECT operation.status,reservation.status,
				reservation.settled_input_tokens,reservation.settled_output_tokens
				FROM agent.workspace_analysis_operation AS operation
				JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
				WHERE operation.id=$1`, string(authorized.OperationID)).Scan(
					&operationStatus, &reservationStatus, &settledInput, &settledOutput,
				); err != nil {
					t.Fatal(err)
				}
				if operationStatus != test.wantOperation || reservationStatus != test.wantReservation ||
					settledInput != test.wantSettledInput || settledOutput != test.wantSettledOutput {
					t.Fatalf("closure operation=%s reservation=%s usage=(%d,%d)",
						operationStatus, reservationStatus, settledInput, settledOutput)
				}
			})
		})
	}
}

// TestWorkspaceAnalysisModelOperationCancellationSettlesAuthorizedCallIntegration
// proves that a cancel requested after provider authorization closes the durable
// STARTED/RESERVED bundle without permitting a successful result publication.
func TestWorkspaceAnalysisModelOperationCancellationSettlesAuthorizedCallIntegration(t *testing.T) {
	tests := []struct {
		name            string
		callStatus      domain.ModelCallStatus
		runStatus       domain.ModelRunStatus
		wantOperation   string
		wantReservation string
		wantInput       int64
		wantOutput      int64
	}{
		{
			name: "failed", callStatus: domain.ModelCallFailed, runStatus: domain.ModelRunFailed,
			wantOperation: "FAILED", wantReservation: "SETTLED",
		},
		{
			name: "unknown", callStatus: domain.ModelCallUnknown, runStatus: domain.ModelRunUnknown,
			wantOperation: "UNKNOWN", wantReservation: "UNKNOWN_CHARGED",
			wantInput:  domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			wantOutput: domain.WorkspaceAnalysisV1PlanMaxOutputTokens,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, func(
				t *testing.T,
				pool *pgxpool.Pool,
				ctx context.Context,
				repository workspaceAnalysisModelRepositoryIntegrationStore,
			) {
				fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
				authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
				if err != nil {
					t.Fatal(err)
				}
				cancel := newWorkspaceAnalysisConcurrentCancelCoordinator(t, pool)
				if _, err := cancel.Cancel(ctx, workflowapplication.RunControlCommand{
					WorkflowRunID:   fixture.command.Identity.WorkflowRunID,
					ExpectedVersion: 1,
					IdempotencyKey:  "wa-model-cancel-settlement-" + test.name,
				}); err != nil {
					t.Fatalf("persist cancellation: %v", err)
				}

				if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx,
					successfulWorkspaceAnalysisModelResultCommand(t, fixture, authorized)); err == nil {
					t.Fatal("cancelled model call unexpectedly accepted a successful result")
				} else if !application.WorkspaceAnalysisModelCancellationConflict(err) {
					t.Fatalf("successful result cancellation error lost its stable marker: %v", err)
				}
				if _, err := repository.FinalizeWorkspaceAnalysisModelCall(ctx,
					terminalWorkspaceAnalysisModelCallCommand(
						authorized, fixture.command, domain.ModelCallSucceeded, domain.ModelRunRefused,
						domain.ResultTypeRefusal, string(domain.WorkspaceAnalysisRunModelRefused),
					)); err == nil {
					t.Fatal("cancelled model call unexpectedly accepted a refusal")
				}

				command := terminalWorkspaceAnalysisModelCallCommand(
					authorized, fixture.command, test.callStatus, test.runStatus, "", "WORKSPACE_ANALYSIS_CANCELLED",
				)
				mutation, err := repository.FinalizeWorkspaceAnalysisModelCall(ctx, command)
				if err != nil || mutation.Replayed {
					t.Fatalf("cancelled finalization=%#v err=%v", mutation, err)
				}
				replayed, err := repository.FinalizeWorkspaceAnalysisModelCall(ctx, command)
				if err != nil || !replayed.Replayed {
					t.Fatalf("cancelled replay=%#v err=%v", replayed, err)
				}

				var operationStatus, reservationStatus string
				var settledInput, settledOutput, modelResults, candidates int64
				if err := pool.QueryRow(ctx, `SELECT operation.status,reservation.status,
				reservation.settled_input_tokens,reservation.settled_output_tokens,
				(SELECT count(*) FROM agent.workspace_analysis_model_result WHERE operation_id=operation.id),
				(SELECT count(*) FROM agent.workspace_analysis_candidate WHERE synthesis_operation_id=operation.id)
				FROM agent.workspace_analysis_operation AS operation
				JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
				WHERE operation.id=$1`, string(authorized.OperationID)).Scan(
					&operationStatus, &reservationStatus, &settledInput, &settledOutput, &modelResults, &candidates,
				); err != nil {
					t.Fatal(err)
				}
				if operationStatus != test.wantOperation || reservationStatus != test.wantReservation ||
					settledInput != test.wantInput || settledOutput != test.wantOutput || modelResults != 0 || candidates != 0 {
					t.Fatalf("cancelled closure operation=%s reservation=%s usage=(%d,%d) result=%d candidate=%d",
						operationStatus, reservationStatus, settledInput, settledOutput, modelResults, candidates)
				}
			})
		})
	}
}

// TestWorkspaceAnalysisModelOperationCandidateCancellationConflictMarkerIntegration
// proves that a successful Provider response rejected by the cancellation fence
// is reported distinctly, so the Runner can settle the existing Call instead
// of leaving STARTED/RESERVED facts for the cancellation hook.
func TestWorkspaceAnalysisModelOperationCandidateCancellationConflictMarkerIntegration(t *testing.T) {
	testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, testWorkspaceAnalysisModelOperationCandidateCancellationConflictMarkerIntegration)
}

func testWorkspaceAnalysisModelOperationCandidateCancellationConflictMarkerIntegration(
	t *testing.T,
	pool *pgxpool.Pool,
	ctx context.Context,
	repository workspaceAnalysisModelRepositoryIntegrationStore,
) {
	fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
	planAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx,
		successfulWorkspaceAnalysisModelResultCommand(t, fixture, planAuthorization)); err != nil {
		t.Fatal(err)
	}
	seedWorkspaceAnalysisModelEvidencePrefixIntegration(t, ctx, pool)
	synthesisAuthorizationCommand := synthesisWorkspaceAnalysisModelAuthorizationCommand(fixture)
	synthesisAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, synthesisAuthorizationCommand)
	if err != nil {
		t.Fatal(err)
	}
	cancel := newWorkspaceAnalysisConcurrentCancelCoordinator(t, pool)
	if _, err := cancel.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID:   synthesisAuthorizationCommand.Identity.WorkflowRunID,
		ExpectedVersion: 1,
		IdempotencyKey:  "wa-model-candidate-cancel-marker",
	}); err != nil {
		t.Fatalf("persist cancellation: %v", err)
	}

	candidateCommand := successfulWorkspaceAnalysisModelCandidateCommand(
		t, fixture, synthesisAuthorization, synthesisAuthorizationCommand,
	)
	if _, err := repository.FinalizeWorkspaceAnalysisModelCandidate(ctx, candidateCommand); err == nil {
		t.Fatal("cancelled model candidate unexpectedly published")
	} else if !application.WorkspaceAnalysisModelCancellationConflict(err) {
		t.Fatalf("candidate cancellation error lost its stable marker: %v", err)
	}

	var operationStatus, reservationStatus, callStatus string
	if err := pool.QueryRow(ctx, `SELECT operation.status,reservation.status,call.status
		FROM agent.workspace_analysis_operation AS operation
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.model_call AS call ON call.id=operation.model_call_id
		WHERE operation.id=$1`, string(synthesisAuthorization.OperationID)).Scan(
		&operationStatus, &reservationStatus, &callStatus,
	); err != nil {
		t.Fatal(err)
	}
	if operationStatus != "STARTED" || reservationStatus != "RESERVED" || callStatus != "STARTED" {
		t.Fatalf("marker path changed facts before Runner settlement: operation=%s reservation=%s call=%s",
			operationStatus, reservationStatus, callStatus)
	}

	settleCommand := terminalWorkspaceAnalysisModelCallCommand(
		synthesisAuthorization, synthesisAuthorizationCommand,
		domain.ModelCallFailed, domain.ModelRunFailed, "", "WORKSPACE_ANALYSIS_CANCELLED",
	)
	if _, err := repository.FinalizeWorkspaceAnalysisModelCall(ctx, settleCommand); err != nil {
		t.Fatalf("settle cancelled candidate call: %v", err)
	}
	var candidateCount int
	if err := pool.QueryRow(ctx, `SELECT operation.status,reservation.status,call.status,
		(SELECT count(*) FROM agent.workspace_analysis_candidate WHERE analysis_run_id=operation.analysis_run_id)
		FROM agent.workspace_analysis_operation AS operation
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.model_call AS call ON call.id=operation.model_call_id
		WHERE operation.id=$1`, string(synthesisAuthorization.OperationID)).Scan(
		&operationStatus, &reservationStatus, &callStatus, &candidateCount,
	); err != nil {
		t.Fatal(err)
	}
	if operationStatus != "FAILED" || reservationStatus != "SETTLED" || callStatus != "FAILED" || candidateCount != 0 {
		t.Fatalf("cancel settlement operation=%s reservation=%s call=%s candidates=%d",
			operationStatus, reservationStatus, callStatus, candidateCount)
	}
}

func TestWorkspaceAnalysisModelOperationCancellationMarkerRequiresActiveLeaseIntegration(t *testing.T) {
	t.Run("plan result with expired lease", func(t *testing.T) {
		testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, func(
			t *testing.T,
			pool *pgxpool.Pool,
			ctx context.Context,
			repository workspaceAnalysisModelRepositoryIntegrationStore,
		) {
			fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
			authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
			if err != nil {
				t.Fatal(err)
			}
			persistWorkspaceAnalysisModelCancellation(t, ctx, pool, fixture.command.Identity.WorkflowRunID, "wa-model-stale-plan")
			expireWorkspaceAnalysisModelLease(t, ctx, pool, fixture.command.Identity)

			_, err = repository.FinalizeWorkspaceAnalysisModelResult(
				ctx, successfulWorkspaceAnalysisModelResultCommand(t, fixture, authorized),
			)
			assertWorkspaceAnalysisModelStaleFenceIsNotCancellationMarker(t, err)
		})
	})

	t.Run("candidate with expired lease", func(t *testing.T) {
		testWorkspaceAnalysisModelRepositoryIntegrationVariants(t, func(
			t *testing.T,
			pool *pgxpool.Pool,
			ctx context.Context,
			repository workspaceAnalysisModelRepositoryIntegrationStore,
		) {
			fixture := seedWorkspaceAnalysisModelOperationIntegration(t, ctx, pool)
			planAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, fixture.command)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx,
				successfulWorkspaceAnalysisModelResultCommand(t, fixture, planAuthorization)); err != nil {
				t.Fatal(err)
			}
			seedWorkspaceAnalysisModelEvidencePrefixIntegration(t, ctx, pool)
			synthesisCommand := synthesisWorkspaceAnalysisModelAuthorizationCommand(fixture)
			synthesisAuthorization, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, synthesisCommand)
			if err != nil {
				t.Fatal(err)
			}
			persistWorkspaceAnalysisModelCancellation(t, ctx, pool, synthesisCommand.Identity.WorkflowRunID, "wa-model-stale-candidate")
			expireWorkspaceAnalysisModelLease(t, ctx, pool, synthesisCommand.Identity)

			_, err = repository.FinalizeWorkspaceAnalysisModelCandidate(
				ctx,
				successfulWorkspaceAnalysisModelCandidateCommand(t, fixture, synthesisAuthorization, synthesisCommand),
			)
			assertWorkspaceAnalysisModelStaleFenceIsNotCancellationMarker(t, err)
		})
	})
}

func persistWorkspaceAnalysisModelCancellation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workflowRunID foundation.ID,
	idempotencyKey string,
) {
	t.Helper()
	cancel := newWorkspaceAnalysisConcurrentCancelCoordinator(t, pool)
	if _, err := cancel.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: workflowRunID, ExpectedVersion: 1, IdempotencyKey: idempotencyKey,
	}); err != nil {
		t.Fatalf("persist cancellation: %v", err)
	}
}

func expireWorkspaceAnalysisModelLease(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	identity application.WorkspaceAnalysisModelExecutionIdentity,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE workflow.node_attempt
		SET lease_until=clock_timestamp()-interval '1 second'
		WHERE id=$1`, string(identity.NodeAttemptID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow.node_run
		SET lease_until=clock_timestamp()-interval '1 second',updated_at=clock_timestamp()
		WHERE id=$1`, string(identity.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceAnalysisModelStaleFenceIsNotCancellationMarker(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("stale model fence unexpectedly accepted")
	}
	if application.WorkspaceAnalysisModelCancellationConflict(err) {
		t.Fatalf("stale model fence was misclassified as a cancellation conflict: %v", err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != application.ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid {
		t.Fatalf("stale model fence error = %#v", err)
	}
}

type workspaceAnalysisModelIntegrationFixture struct {
	command application.AuthorizeWorkspaceAnalysisModelCallCommand
}

func seedWorkspaceAnalysisModelOperationIntegration(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) workspaceAnalysisModelIntegrationFixture {
	t.Helper()
	return seedWorkspaceAnalysisModelOperationIntegrationWithRunAge(t, ctx, pool, 0)
}

func seedWorkspaceAnalysisModelOperationIntegrationWithRunAge(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runAge time.Duration,
) workspaceAnalysisModelIntegrationFixture {
	t.Helper()
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES ('83000000-0000-4000-8000-000000000001','wa-model-uow','/tmp/wa-model-uow','/tmp/wa-model-uow',now(),'active',now(),now());
INSERT INTO agent.conversation(
    id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash
) VALUES (
    '83000000-0000-4000-8000-000000000002','83000000-0000-4000-8000-000000000001',
    'open','Workspace Analysis Model UoW',1,now(),now(),now(),'wa-model-uow',repeat('a',64)
);
INSERT INTO agent.question(
    id,workspace_id,conversation_id,ordinal,mode,question_text,scope,answer_depth,output_format,
    context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000010','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000002',1,'workspace_analysis','analyze workspace','{}','standard','markdown',
    0,repeat('1',64),'wa-model-question',repeat('2',64),now()
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,$3,$4,$5::jsonb,clock_timestamp())`,
		string(workspaceAnalysisModelIntegrationID(11)), string(workspaceAnalysisModelIntegrationID(1)),
		definition.Key, definition.Version, string(graph),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
VALUES (
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000011','running','{}',1,now(),now()
);
INSERT INTO agent.answer(
    id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000013','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000002','83000000-0000-4000-8000-000000000010',
    '83000000-0000-4000-8000-000000000012','pending',1,now(),now()
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `WITH timing AS (SELECT clock_timestamp()-$10::interval AS at)
INSERT INTO agent.workspace_analysis_run(
    id,workspace_id,conversation_id,question_id,answer_id,workflow_run_id,
    definition_key,definition_version,definition_hash,tool_catalog_hash,policy_version,config_revision,
    deadline_at,plan_model_timeout_ms,synthesis_model_timeout_ms,review_model_timeout_ms,
    git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,validate_citation_tool_timeout_ms,
    durable_completion_margin_ms,max_nodes,max_model_calls,max_tool_calls,max_source_reads,max_tool_concurrency,
    max_input_tokens,max_output_tokens,status,version,created_at,updated_at
) SELECT
    $1,$2,$3,$4,$5,$6,$7,$8,$9,repeat('4',64),1,7,
    at+interval '13 minutes 50 seconds',120000,300000,180000,30000,45000,20000,25000,
    5000,6,3,6,3,1,196608,5376,'queued',1,at,at
FROM timing`, string(workspaceAnalysisModelIntegrationID(14)), string(workspaceAnalysisModelIntegrationID(1)),
		string(workspaceAnalysisModelIntegrationID(2)), string(workspaceAnalysisModelIntegrationID(10)),
		string(workspaceAnalysisModelIntegrationID(13)), string(workspaceAnalysisModelIntegrationID(12)),
		definition.Key, definition.Version, definition.GraphHash,
		fmt.Sprintf("%d milliseconds", runAge.Milliseconds()),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=2,updated_at=clock_timestamp()
		WHERE id=$1 AND status='queued' AND version=1`, string(workspaceAnalysisModelIntegrationID(14))); err != nil {
		t.Fatal(err)
	}
	seedWorkspaceAnalysisModelGitPrefixIntegration(t, ctx, tx)
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
    idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000030','83000000-0000-4000-8000-000000000012',
    'retrieve_evidence','agent.workspace-analysis.retrieve_evidence','running',1,'{}','wa-worker',now()+interval '30 minutes',
    'wa-model-plan',1,1,1,1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000031','83000000-0000-4000-8000-000000000030',
    1,1,0,'wa-model-plan-1','wa-worker',now()+interval '30 minutes','running',now()
);
INSERT INTO retrieval.index_version(
    id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
    source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
    version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000037','83000000-0000-4000-8000-000000000001',
    'simple','v1',repeat('8',64),'{}','wa-model-plan:index',repeat('9',64),0,
    'wa-model-plan-index','building','["vector"]',1,now(),now()
);`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	request := []byte(`{"prompt":"plan"}`)
	at := time.Now().UTC().Truncate(time.Microsecond)
	run := domain.ModelRun{
		ID: workspaceAnalysisModelIntegrationID(34), WorkspaceID: workspaceAnalysisModelIntegrationID(1),
		WorkflowRunID: workspaceAnalysisModelIntegrationID(12), NodeRunID: workspaceAnalysisModelIntegrationID(30),
		NodeAttemptID: workspaceAnalysisModelIntegrationID(31),
		Model:         domain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "model-test", ModelVersion: "2026-08-16"},
		Profile:       domain.ModelProfileRef{ID: "workspace-analysis", Version: "v1"},
		Prompt:        domain.PromptRef{ID: "workspace-analysis-plan", Version: "v1"},
		Schema:        domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1},
		ReducedSchema: domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1},
		Retrieval:     domain.RetrievalRef{IndexVersionID: workspaceAnalysisModelIntegrationID(37)},
		Status:        domain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	call := domain.ModelCall{
		ID: workspaceAnalysisModelIntegrationID(35), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallPlan,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema,
		MaxOutputTokens: int(domain.WorkspaceAnalysisV1PlanMaxOutputTokens), Status: domain.ModelCallStarted,
		RequestHash: workspaceAnalysisModelIntegrationHash(request), RequestBytes: int64(len(request)),
		Version: 1, StartedAt: at,
	}
	return workspaceAnalysisModelIntegrationFixture{command: application.AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: application.WorkspaceAnalysisModelExecutionIdentity{
			WorkspaceID: run.WorkspaceID, DefinitionID: workspaceAnalysisModelIntegrationID(11),
			DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
			WorkflowRunID: run.WorkflowRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
			NodeRunID: run.NodeRunID, NodeAttemptID: run.NodeAttemptID, LeaseOwner: "wa-worker", LeaseFence: 1,
		},
		OperationKey: domain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: workspaceAnalysisModelIntegrationID(14),
			NodeKey:       domain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
			Kind:          domain.WorkspaceAnalysisOperationRetrievalPlan, Ordinal: 1,
		},
		OperationID: workspaceAnalysisModelIntegrationID(32), ReservationID: workspaceAnalysisModelIntegrationID(33),
		Run: run, Call: call, RequestDocument: request,
	}}
}

func seedWorkspaceAnalysisModelGitPrefixIntegration(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000020','83000000-0000-4000-8000-000000000012',
    'inspect_workspace','agent.workspace-analysis.inspect_workspace','running',1,'{}','wa-worker',now()+interval '30 minutes',1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000021','83000000-0000-4000-8000-000000000020',
    1,1,0,'wa-model-git-1','wa-worker',now()+interval '30 minutes','running',now()
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000022','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000020','inspect_workspace','GIT_STATUS',1,'TOOL',repeat('5',64),
    'PENDING',1,now(),now()
);
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES (
    '83000000-0000-4000-8000-000000000023','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000020',
    '83000000-0000-4000-8000-000000000021',1,'ReadGitStatus',2,
    'b5dd1fcca72d5bb41fd9ad3f74006d3706e4184ad39e2a3409b565d1ff896bbd',
    'tool.read_git_status.input',1,'tool.read_git_status.output',1,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('5',64),2,'{}','STARTED',false,now(),0,1
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000021',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000021',
    tool_call_id='83000000-0000-4000-8000-000000000023',version=2,started_at=now(),updated_at=now()
WHERE id='83000000-0000-4000-8000-000000000022';
INSERT INTO agent.workspace_analysis_budget_reservation(
    id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
    reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000024','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000022',
    'TOOL','83000000-0000-4000-8000-000000000023','RESERVED',0,1,0,0,0,now()
);
UPDATE agent.workspace_analysis_run SET reserved_tool_calls=1,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE workflow.tool_call SET
    status='SUCCEEDED',
    response_hash=encode(sha256(convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}','UTF8')),'hex'),
    response_bytes=octet_length(convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}','UTF8')),
    response_summary='{"clean":true}',completed_at=clock_timestamp(),duration_ms=10,version=2
WHERE id='83000000-0000-4000-8000-000000000023';
INSERT INTO workflow.tool_result_receipt(
    id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
    output_schema_id,output_schema_version,definition_hash,persistence_policy,
    max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,created_at
) SELECT
    '83000000-0000-4000-8000-000000000025',id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
    requested_tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,'PERSIST_CANONICAL',
    4096,1024,
    convert_to('{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}','UTF8'),
    response_hash,response_bytes,completed_at+interval '1 millisecond'
FROM workflow.tool_call WHERE id='83000000-0000-4000-8000-000000000023';
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000024';
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=0,settled_tool_calls=1,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',result_id='83000000-0000-4000-8000-000000000025',
    result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000025'),
    version=3,completed_at=terminal.at,updated_at=terminal.at
FROM (SELECT clock_timestamp() AS at) AS terminal
WHERE id='83000000-0000-4000-8000-000000000022';
SET CONSTRAINTS ALL IMMEDIATE;`); err != nil {
		t.Fatal(err)
	}
}

func advanceWorkspaceAnalysisModelAttemptIntegration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET
    status='retry_scheduled',lease_owner=NULL,lease_until=NULL,failure_class='retryable',
    error_code='RECOVERY_REPLAY',ended_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000031';
UPDATE workflow.node_run SET
    attempt=2,lease_owner='wa-worker',lease_until=now()+interval '30 minutes',
    dispatch_no=2,version=version+1,updated_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000030';
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES (
    '83000000-0000-4000-8000-000000000090','83000000-0000-4000-8000-000000000030',
    2,2,1,'wa-model-plan-2','wa-worker',now()+interval '30 minutes','running',now()
);`); err != nil {
		t.Fatal(err)
	}
}

func seedWorkspaceAnalysisModelEvidencePrefixIntegration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
    idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
) VALUES
(
    '83000000-0000-4000-8000-000000000150','83000000-0000-4000-8000-000000000012',
    'read_evidence','agent.workspace-analysis.read_evidence','running',1,'{}','wa-worker',now()+interval '30 minutes',
    'wa-model-read',1,1,1,1,now(),now()
),
(
    '83000000-0000-4000-8000-000000000160','83000000-0000-4000-8000-000000000012',
    'synthesize_answer','agent.workspace-analysis.synthesize_answer','running',1,'{}','wa-worker',now()+interval '30 minutes',
    'wa-model-synthesis',1,1,1,1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES
(
    '83000000-0000-4000-8000-000000000151','83000000-0000-4000-8000-000000000150',
    1,1,0,'wa-model-read-1','wa-worker',now()+interval '30 minutes','running',now()
),
(
    '83000000-0000-4000-8000-000000000161','83000000-0000-4000-8000-000000000160',
    1,1,0,'wa-model-synthesis-1','wa-worker',now()+interval '30 minutes','running',now()
);
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES
(
    '83000000-0000-4000-8000-000000000143','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000030',
    '83000000-0000-4000-8000-000000000031',1,'SearchKnowledge',2,
    'db7180086adb06a18a4be8d1eb80a208fa6385c70f1a71d6d67c4f263fbd1807',
    'tool.search_knowledge.input',2,'tool.search_knowledge.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('5',64),2,'{}','STARTED',false,now(),0,1
),
(
    '83000000-0000-4000-8000-000000000153','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000150',
    '83000000-0000-4000-8000-000000000151',1,'ReadSource',3,
    'd41dabac535261b885e453b64e11fe5bb779838e31a255aea4798e27ead54d32',
    'tool.read_source.input',2,'tool.read_source.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('6',64),2,'{}','STARTED',false,now(),0,1
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES
(
    '83000000-0000-4000-8000-000000000142','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000030','retrieve_evidence','KNOWLEDGE_SEARCH',1,'TOOL',repeat('5',64),
    'PENDING',1,now(),now()
),
(
    '83000000-0000-4000-8000-000000000152','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000150','read_evidence','SOURCE_READ',1,'TOOL',repeat('6',64),
    'PENDING',1,now(),now()
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000031',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000031',
    tool_call_id='83000000-0000-4000-8000-000000000143',version=2,
    started_at=authorized.at,updated_at=authorized.at
FROM (SELECT clock_timestamp() AS at) AS authorized
WHERE id='83000000-0000-4000-8000-000000000142';
INSERT INTO agent.workspace_analysis_budget_reservation(
    id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
    reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000144','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000142',
    'TOOL','83000000-0000-4000-8000-000000000143','RESERVED',0,1,0,0,0,clock_timestamp()
);
UPDATE agent.workspace_analysis_run SET
	reserved_tool_calls=reserved_tool_calls+1,version=version+1,
	updated_at=GREATEST(updated_at,clock_timestamp())
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE workflow.tool_call SET
    status='SUCCEEDED',
    response_hash=encode(sha256(convert_to('{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}]}','UTF8')),'hex'),
    response_bytes=octet_length(convert_to('{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}]}','UTF8')),
    response_summary='{}',completed_at=completed.at,duration_ms=10,version=2
FROM (SELECT clock_timestamp() AS at) AS completed
WHERE id='83000000-0000-4000-8000-000000000143';
INSERT INTO workflow.tool_result_receipt(
    id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
    output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,
    output_document,output_hash,output_bytes,server_binding_schema_id,server_binding_schema_version,
    server_binding_document,server_binding_hash,server_binding_bytes,created_at
) SELECT
    '83000000-0000-4000-8000-000000000145',call.id,call.workspace_id,call.workflow_run_id,call.node_run_id,
    call.node_attempt_id,call.requested_tool_name,call.tool_version,call.output_schema_id,call.output_schema_version,
    call.definition_hash,'PERSIST_CANONICAL',32768,16384,
    convert_to('{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"Evidence"}]}','UTF8'),
    call.response_hash,call.response_bytes,'tool.search_knowledge.private_binding',1,binding.document,
    encode(sha256(binding.document),'hex'),octet_length(binding.document),call.completed_at+interval '1 millisecond'
FROM workflow.tool_call AS call
CROSS JOIN LATERAL (
    SELECT convert_to('{"items":[{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000037","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}],"selected_refs":["E1"]}','UTF8') AS document
) AS binding
WHERE call.id='83000000-0000-4000-8000-000000000143';
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000144';
UPDATE agent.workspace_analysis_run SET
	reserved_tool_calls=reserved_tool_calls-1,settled_tool_calls=settled_tool_calls+1,
	version=version+1,updated_at=GREATEST(updated_at,clock_timestamp())
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',result_id='83000000-0000-4000-8000-000000000145',
    result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000145'),
    version=3,completed_at=completed.at,updated_at=completed.at
FROM (SELECT clock_timestamp() AS at) AS completed
WHERE id='83000000-0000-4000-8000-000000000142';

UPDATE agent.workspace_analysis_operation SET
    status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000151',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000151',
    tool_call_id='83000000-0000-4000-8000-000000000153',version=2,
    started_at=authorized.at,updated_at=authorized.at
FROM (SELECT clock_timestamp() AS at) AS authorized
WHERE id='83000000-0000-4000-8000-000000000152';
INSERT INTO agent.workspace_analysis_budget_reservation(
    id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
    reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000154','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000152',
    'TOOL','83000000-0000-4000-8000-000000000153','RESERVED',0,1,1,0,0,clock_timestamp()
);
UPDATE agent.workspace_analysis_run SET
	reserved_tool_calls=reserved_tool_calls+1,reserved_source_reads=reserved_source_reads+1,
	version=version+1,updated_at=GREATEST(updated_at,clock_timestamp())
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE workflow.tool_call SET
    status='SUCCEEDED',
    response_hash=encode(sha256(convert_to('{"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","excerpt":"Evidence","truncated":false}','UTF8')),'hex'),
    response_bytes=octet_length(convert_to('{"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","excerpt":"Evidence","truncated":false}','UTF8')),
    response_summary='{}',completed_at=completed.at,duration_ms=10,version=2
FROM (SELECT clock_timestamp() AS at) AS completed
WHERE id='83000000-0000-4000-8000-000000000153';
INSERT INTO workflow.tool_result_receipt(
    id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
    output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,
    output_document,output_hash,output_bytes,server_binding_schema_id,server_binding_schema_version,
    server_binding_document,server_binding_hash,server_binding_bytes,created_at
) SELECT
    '83000000-0000-4000-8000-000000000155',call.id,call.workspace_id,call.workflow_run_id,call.node_run_id,
    call.node_attempt_id,call.requested_tool_name,call.tool_version,call.output_schema_id,call.output_schema_version,
    call.definition_hash,'PERSIST_CANONICAL',8192,4096,
    convert_to('{"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","excerpt":"Evidence","truncated":false}','UTF8'),
    call.response_hash,call.response_bytes,'tool.read_source.private_binding',1,binding.document,
    encode(sha256(binding.document),'hex'),octet_length(binding.document),call.completed_at+interval '1 millisecond'
FROM workflow.tool_call AS call
CROSS JOIN LATERAL (
    SELECT convert_to('{"chunk_id":"83000000-0000-4000-8000-000000000138","citation_id":"cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","evidence_ref":"E1","index_version_id":"83000000-0000-4000-8000-000000000037","search_receipt_hash":"' || (SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000145') || '","search_receipt_id":"83000000-0000-4000-8000-000000000145","source_span_id":"83000000-0000-4000-8000-000000000140","source_version_id":"83000000-0000-4000-8000-000000000139"}','UTF8') AS document
) AS binding
WHERE call.id='83000000-0000-4000-8000-000000000153';
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_source_reads=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000154';
UPDATE agent.workspace_analysis_run SET
	reserved_tool_calls=reserved_tool_calls-1,reserved_source_reads=reserved_source_reads-1,
	settled_tool_calls=settled_tool_calls+1,settled_source_reads=settled_source_reads+1,
	version=version+1,updated_at=GREATEST(updated_at,clock_timestamp())
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',result_id='83000000-0000-4000-8000-000000000155',
    result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000155'),
    version=3,completed_at=completed.at,updated_at=completed.at
FROM (SELECT clock_timestamp() AS at) AS completed
WHERE id='83000000-0000-4000-8000-000000000152';
SET CONSTRAINTS ALL IMMEDIATE;`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedWorkspaceAnalysisModelCitationValidationIntegration(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	candidate domain.WorkspaceAnalysisCandidate,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO workflow.node_run(
    id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
    idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
) VALUES
(
    '83000000-0000-4000-8000-000000000170','83000000-0000-4000-8000-000000000012',
    'validate_citations','agent.workspace-analysis.validate_citations','running',1,'{}','wa-worker',now()+interval '30 minutes',
    'wa-model-validation',1,1,1,1,now(),now()
),
(
    '83000000-0000-4000-8000-000000000180','83000000-0000-4000-8000-000000000012',
    'review_publish','agent.workspace-analysis.review_publish','running',1,'{}','wa-worker',now()+interval '30 minutes',
    'wa-model-review',1,1,1,1,now(),now()
);
INSERT INTO workflow.node_attempt(
    id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
) VALUES
(
    '83000000-0000-4000-8000-000000000171','83000000-0000-4000-8000-000000000170',
    1,1,0,'wa-model-validation-1','wa-worker',now()+interval '30 minutes','running',now()
),
(
    '83000000-0000-4000-8000-000000000181','83000000-0000-4000-8000-000000000180',
    1,1,0,'wa-model-review-1','wa-worker',now()+interval '30 minutes','running',now()
);
INSERT INTO workflow.tool_call(
    id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
    requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
    output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
    request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
) VALUES (
    '83000000-0000-4000-8000-000000000173','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000012','83000000-0000-4000-8000-000000000170',
    '83000000-0000-4000-8000-000000000171',1,'ValidateCitation',3,
    'bf5e643c47d57478b46d258eca250dc30e810ee7a0693569f44edaab19037d54',
    'tool.validate_citation.input',2,'tool.validate_citation.output',2,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
    repeat('7',64),2,'{}','STARTED',false,now(),0,1
);
INSERT INTO agent.workspace_analysis_operation(
    id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
    ordinal,call_kind,request_hash,status,version,created_at,updated_at
) VALUES (
    '83000000-0000-4000-8000-000000000172','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000012',
    '83000000-0000-4000-8000-000000000170','validate_citations','CITATION_VALIDATION',1,'TOOL',repeat('7',64),
    'PENDING',1,now(),now()
);
UPDATE agent.workspace_analysis_operation SET
    status='STARTED',first_node_attempt_id='83000000-0000-4000-8000-000000000171',
    latest_node_attempt_id='83000000-0000-4000-8000-000000000171',
    tool_call_id='83000000-0000-4000-8000-000000000173',version=2,
    started_at=authorized.at,updated_at=authorized.at
FROM (SELECT clock_timestamp() AS at) AS authorized
WHERE id='83000000-0000-4000-8000-000000000172';
INSERT INTO agent.workspace_analysis_budget_reservation(
    id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
    reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at
) VALUES (
    '83000000-0000-4000-8000-000000000174','83000000-0000-4000-8000-000000000001',
    '83000000-0000-4000-8000-000000000014','83000000-0000-4000-8000-000000000172',
    'TOOL','83000000-0000-4000-8000-000000000173','RESERVED',0,1,0,0,0,clock_timestamp()
);
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=reserved_tool_calls+1,version=version+1,
    updated_at=GREATEST(updated_at,clock_timestamp())
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE workflow.tool_call SET
    status='SUCCEEDED',
    response_hash=encode(sha256(convert_to('{"results":[{"evidence_ref":"E1","reason_code":"OK","valid":true}]}','UTF8')),'hex'),
    response_bytes=octet_length(convert_to('{"results":[{"evidence_ref":"E1","reason_code":"OK","valid":true}]}','UTF8')),
    response_summary='{}',completed_at=completed.at,duration_ms=10,version=2
FROM (SELECT clock_timestamp() AS at) AS completed
WHERE id='83000000-0000-4000-8000-000000000173';`); err != nil {
		t.Fatal(err)
	}
	bindingDocument, err := json.Marshal(map[string]any{
		"candidate_hash": candidate.DocumentHash,
		"candidate_id":   string(candidate.ID),
		"results": []map[string]any{{
			"chunk_id":          string(workspaceAnalysisModelIntegrationID(138)),
			"citation_id":       "cite-b6f57b12f11d2c1820b082dfada1a971c599ebe5a76c9d0f06d3a6aac498bb23",
			"content_hash":      strings.Repeat("a", 64),
			"evidence_ref":      "E1",
			"index_version_id":  string(workspaceAnalysisModelIntegrationID(37)),
			"source_span_id":    string(workspaceAnalysisModelIntegrationID(140)),
			"source_version_id": string(workspaceAnalysisModelIntegrationID(139)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.tool_result_receipt(
    id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
    output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,
    output_document,output_hash,output_bytes,server_binding_schema_id,server_binding_schema_version,
    server_binding_document,server_binding_hash,server_binding_bytes,created_at
) SELECT
    '83000000-0000-4000-8000-000000000175',call.id,call.workspace_id,call.workflow_run_id,call.node_run_id,
    call.node_attempt_id,call.requested_tool_name,call.tool_version,call.output_schema_id,call.output_schema_version,
    call.definition_hash,'PERSIST_CANONICAL',16384,16384,
    convert_to('{"results":[{"evidence_ref":"E1","reason_code":"OK","valid":true}]}','UTF8'),
    call.response_hash,call.response_bytes,'tool.validate_citation.private_binding',1,convert_to($1,'UTF8'),
    encode(sha256(convert_to($1,'UTF8')),'hex'),octet_length(convert_to($1,'UTF8')),call.completed_at+interval '1 millisecond'
FROM workflow.tool_call AS call
WHERE call.id='83000000-0000-4000-8000-000000000173'`, string(bindingDocument)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE agent.workspace_analysis_budget_reservation SET
    status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp()
WHERE id='83000000-0000-4000-8000-000000000174';
UPDATE agent.workspace_analysis_run SET
    reserved_tool_calls=reserved_tool_calls-1,settled_tool_calls=settled_tool_calls+1,
    version=version+1,updated_at=GREATEST(updated_at,clock_timestamp())
WHERE id='83000000-0000-4000-8000-000000000014';
UPDATE agent.workspace_analysis_operation SET
    status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',result_id='83000000-0000-4000-8000-000000000175',
    result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id='83000000-0000-4000-8000-000000000175'),
    version=3,completed_at=completed.at,updated_at=completed.at
FROM (SELECT clock_timestamp() AS at) AS completed
WHERE id='83000000-0000-4000-8000-000000000172';
SET CONSTRAINTS ALL IMMEDIATE;`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func successfulWorkspaceAnalysisModelResultCommand(
	t *testing.T,
	fixture workspaceAnalysisModelIntegrationFixture,
	authorized application.WorkspaceAnalysisModelAuthorizationResult,
) application.FinalizeWorkspaceAnalysisModelResultCommand {
	t.Helper()
	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	if completedAt.Before(authorized.Call.StartedAt) {
		completedAt = authorized.Call.StartedAt
	}
	plan := domain.WorkspaceAnalysisPlanResult{
		ResultType: domain.ResultTypeWorkspaceAnalysisPlan, SchemaID: domain.WorkspaceAnalysisPlanSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: authorized.Run.ID,
		Payload: domain.RAGQueryPlanPayload{
			Intent: "inspect", Rewrites: []string{"analyze workspace"}, SuggestedScopes: []string{},
		},
	}
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	hash := workspaceAnalysisModelIntegrationHash(document)
	call := authorized.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, hash, int64(len(document))
	call.Usage = domain.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := authorized.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisPlan, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	result := domain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisModelIntegrationID(36), WorkspaceID: fixture.command.Identity.WorkspaceID,
		AnalysisRunID: fixture.command.OperationKey.AnalysisRunID, OperationID: authorized.OperationID,
		NodeAttemptID: authorized.Run.NodeAttemptID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
		OperationKind: fixture.command.OperationKey.Kind, Schema: authorized.Run.Schema,
		Document: document, DocumentHash: hash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	return application.FinalizeWorkspaceAnalysisModelResultCommand{
		Identity: fixture.command.Identity, OperationKey: fixture.command.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: call, Run: run, Result: result,
	}
}

func synthesisWorkspaceAnalysisModelAuthorizationCommand(
	fixture workspaceAnalysisModelIntegrationFixture,
) application.AuthorizeWorkspaceAnalysisModelCallCommand {
	request := []byte(`{"prompt":"synthesize"}`)
	at := time.Now().UTC().Truncate(time.Microsecond)
	run := domain.ModelRun{
		ID: workspaceAnalysisModelIntegrationID(164), WorkspaceID: fixture.command.Identity.WorkspaceID,
		WorkflowRunID: fixture.command.Identity.WorkflowRunID, NodeRunID: workspaceAnalysisModelIntegrationID(160),
		NodeAttemptID: workspaceAnalysisModelIntegrationID(161), Model: fixture.command.Run.Model,
		Profile: fixture.command.Run.Profile, Prompt: domain.PromptRef{ID: "workspace-analysis-answer", Version: "v1"},
		Schema:        domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		ReducedSchema: domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		Retrieval:     fixture.command.Run.Retrieval, Status: domain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	call := domain.ModelCall{
		ID: workspaceAnalysisModelIntegrationID(165), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallAnswer,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema,
		MaxOutputTokens: int(domain.WorkspaceAnalysisV1SynthesisMaxOutputTokens),
		RequestHash:     workspaceAnalysisModelIntegrationHash(request), RequestBytes: int64(len(request)),
		Status: domain.ModelCallStarted, Version: 1, StartedAt: at,
	}
	identity := fixture.command.Identity
	identity.NodeKey = domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer
	identity.NodeRunID = workspaceAnalysisModelIntegrationID(160)
	identity.NodeAttemptID = workspaceAnalysisModelIntegrationID(161)
	return application.AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: identity,
		OperationKey: domain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: fixture.command.OperationKey.AnalysisRunID,
			NodeKey:       domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer,
			Kind:          domain.WorkspaceAnalysisOperationAnswerSynthesis,
			Ordinal:       1,
		},
		OperationID: workspaceAnalysisModelIntegrationID(162), ReservationID: workspaceAnalysisModelIntegrationID(163),
		Run: run, Call: call, RequestDocument: request,
	}
}

func successfulWorkspaceAnalysisModelCandidateCommand(
	t *testing.T,
	fixture workspaceAnalysisModelIntegrationFixture,
	authorized application.WorkspaceAnalysisModelAuthorizationResult,
	authorization application.AuthorizeWorkspaceAnalysisModelCallCommand,
) application.FinalizeWorkspaceAnalysisModelCandidateCommand {
	t.Helper()
	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	if completedAt.Before(authorized.Call.StartedAt) {
		completedAt = authorized.Call.StartedAt
	}
	candidateAt := completedAt.Add(time.Millisecond)
	document, err := json.Marshal(domain.WorkspaceAnalysisCandidateResult{
		ResultType: domain.ResultTypeWorkspaceAnalysisCandidate, SchemaID: domain.WorkspaceAnalysisCandidateSchemaID,
		SchemaVersion: "1", ModelRunRef: authorized.Run.ID,
		Payload: domain.WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: "Workspace is clean.", CitationRefs: []string{"E1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := workspaceAnalysisModelIntegrationHash(document)
	call := authorized.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, hash, int64(len(document))
	call.Usage = domain.TokenUsage{InputTokens: 120, OutputTokens: 80, TotalTokens: 200}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := authorized.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisAnswer, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	candidate := domain.WorkspaceAnalysisCandidate{
		ID: workspaceAnalysisModelIntegrationID(166), WorkspaceID: authorization.Identity.WorkspaceID,
		AnalysisRunID: authorization.OperationKey.AnalysisRunID, AnswerID: workspaceAnalysisModelIntegrationID(13),
		SynthesisOperationID: authorized.OperationID, NodeAttemptID: authorized.Run.NodeAttemptID,
		SynthesisModelRunID: authorized.Run.ID, SchemaID: authorized.Run.Schema.ID, SchemaVersion: 1,
		Document: document, DocumentHash: hash, DocumentBytes: int64(len(document)), CreatedAt: candidateAt,
	}
	return application.FinalizeWorkspaceAnalysisModelCandidateCommand{
		Identity: authorization.Identity, OperationKey: authorization.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: call, Run: run, Candidate: candidate,
	}
}

func reviewWorkspaceAnalysisModelAuthorizationCommand(
	t *testing.T,
	fixture workspaceAnalysisModelIntegrationFixture,
	candidate domain.WorkspaceAnalysisCandidate,
) application.AuthorizeWorkspaceAnalysisModelCallCommand {
	t.Helper()
	request, err := json.Marshal(map[string]string{
		"candidate_hash": candidate.DocumentHash,
		"candidate_id":   string(candidate.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	run := domain.ModelRun{
		ID: workspaceAnalysisModelIntegrationID(184), WorkspaceID: fixture.command.Identity.WorkspaceID,
		WorkflowRunID: fixture.command.Identity.WorkflowRunID, NodeRunID: workspaceAnalysisModelIntegrationID(180),
		NodeAttemptID: workspaceAnalysisModelIntegrationID(181), Model: fixture.command.Run.Model,
		Profile: fixture.command.Run.Profile, Prompt: domain.PromptRef{ID: "faithfulness-review", Version: "v1"},
		Schema:        domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1},
		ReducedSchema: domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1},
		Retrieval:     fixture.command.Run.Retrieval, Status: domain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	call := domain.ModelCall{
		ID: workspaceAnalysisModelIntegrationID(185), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallReview,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema,
		MaxOutputTokens: int(domain.WorkspaceAnalysisV1ReviewMaxOutputTokens),
		RequestHash:     workspaceAnalysisModelIntegrationHash(request), RequestBytes: int64(len(request)),
		Status: domain.ModelCallStarted, Version: 1, StartedAt: at,
	}
	identity := fixture.command.Identity
	identity.NodeKey = domain.WorkspaceAnalysisOperationNodeReviewPublish
	identity.NodeRunID = workspaceAnalysisModelIntegrationID(180)
	identity.NodeAttemptID = workspaceAnalysisModelIntegrationID(181)
	return application.AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: identity,
		OperationKey: domain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: fixture.command.OperationKey.AnalysisRunID,
			NodeKey:       domain.WorkspaceAnalysisOperationNodeReviewPublish,
			Kind:          domain.WorkspaceAnalysisOperationFaithfulnessReview,
			Ordinal:       1,
		},
		OperationID: workspaceAnalysisModelIntegrationID(182), ReservationID: workspaceAnalysisModelIntegrationID(183),
		Run: run, Call: call, RequestDocument: request,
	}
}

func successfulWorkspaceAnalysisModelReviewCommand(
	t *testing.T,
	authorized application.WorkspaceAnalysisModelAuthorizationResult,
	authorization application.AuthorizeWorkspaceAnalysisModelCallCommand,
	candidate domain.WorkspaceAnalysisCandidate,
	passed bool,
) application.FinalizeWorkspaceAnalysisModelResultCommand {
	t.Helper()
	verdict := domain.FaithfulnessUnsupported
	summary := "one factual assertion is unsupported"
	if passed {
		verdict = domain.FaithfulnessSupported
		summary = "all factual assertions are supported"
	}
	candidateResult, err := domain.DecodeWorkspaceAnalysisCandidate(candidate.Document, domain.DefaultDecodeLimits())
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(domain.FaithfulnessReviewResult{
		ResultType: domain.ResultTypeFaithfulnessReview, SchemaID: domain.FaithfulnessReviewSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: authorized.Run.ID,
		Payload: domain.FaithfulnessReviewPayload{
			Passed: passed,
			Items: []domain.FaithfulnessReviewItem{{
				AssertionID: "@answer/conclusion", Verdict: verdict,
				CitationIDs: append([]string(nil), candidateResult.Payload.CitationRefs...), Reason: summary,
			}},
			Summary: summary,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	if completedAt.Before(authorized.Call.StartedAt) {
		completedAt = authorized.Call.StartedAt
	}
	resultAt := completedAt.Add(time.Millisecond)
	hash := workspaceAnalysisModelIntegrationHash(document)
	call := authorized.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, hash, int64(len(document))
	call.Usage = domain.TokenUsage{InputTokens: 50, OutputTokens: 25, TotalTokens: 75}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := authorized.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeFaithfulnessReview, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	candidateID := candidate.ID
	result := domain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisModelIntegrationID(186), WorkspaceID: authorization.Identity.WorkspaceID,
		AnalysisRunID: authorization.OperationKey.AnalysisRunID, OperationID: authorized.OperationID,
		NodeAttemptID: authorized.Run.NodeAttemptID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
		OperationKind: authorization.OperationKey.Kind, Schema: authorized.Run.Schema,
		SubjectCandidateID: &candidateID, SubjectCandidateHash: candidate.DocumentHash,
		Document: document, DocumentHash: hash, DocumentBytes: int64(len(document)), CreatedAt: resultAt,
	}
	return application.FinalizeWorkspaceAnalysisModelResultCommand{
		Identity: authorization.Identity, OperationKey: authorization.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: call, Run: run, Result: result,
	}
}

func terminalWorkspaceAnalysisModelCallCommand(
	authorized application.WorkspaceAnalysisModelAuthorizationResult,
	authorization application.AuthorizeWorkspaceAnalysisModelCallCommand,
	callStatus domain.ModelCallStatus,
	runStatus domain.ModelRunStatus,
	resultType string,
	errorCode string,
) application.FinalizeWorkspaceAnalysisModelCallCommand {
	completedAt := authorized.Call.StartedAt.Add(time.Second)
	call := authorized.Call
	call.Status, call.ErrorCode, call.Version, call.CompletedAt = callStatus, errorCode, 2, &completedAt
	if callStatus == domain.ModelCallSucceeded {
		response := []byte(`{"refusal":true}`)
		call.ErrorCode, call.ResponseHash, call.ResponseBytes = "", workspaceAnalysisModelIntegrationHash(response), int64(len(response))
		call.Usage = domain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}
		call.LatencyMillis = 1000
	}
	run := authorized.Run
	run.Status, run.FinalResultType, run.FinalErrorCode, run.Version = runStatus, resultType, errorCode, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	return application.FinalizeWorkspaceAnalysisModelCallCommand{
		Identity: authorization.Identity, OperationKey: authorization.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: call, Run: run,
	}
}

type workspaceAnalysisModelCommitLossDB struct {
	DB
	injectNext atomic.Bool
}

func (database *workspaceAnalysisModelCommitLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &workspaceAnalysisModelCommitLossTx{Tx: tx, injectNext: &database.injectNext}, nil
}

type workspaceAnalysisModelCommitLossTx struct {
	pgx.Tx
	injectNext *atomic.Bool
}

func (tx *workspaceAnalysisModelCommitLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	if tx.injectNext.CompareAndSwap(true, false) {
		return errors.New("injected workspace analysis model commit response loss")
	}
	return nil
}

type workspaceAnalysisModelPostCommitErrorUnitOfWork struct {
	inner foundation.UnitOfWork
	err   error
	armed atomic.Bool
	lost  atomic.Bool
}

func (unitOfWork *workspaceAnalysisModelPostCommitErrorUnitOfWork) Within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work foundation.TransactionFunc,
) error {
	if err := unitOfWork.inner.Within(ctx, options, work); err != nil {
		return err
	}
	if unitOfWork.armed.Load() && unitOfWork.lost.CompareAndSwap(false, true) {
		return unitOfWork.err
	}
	return nil
}

func workspaceAnalysisModelIntegrationHash(document []byte) string {
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}

func workspaceAnalysisModelIntegrationID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("83000000-0000-4000-8000-%012d", seed))
}
