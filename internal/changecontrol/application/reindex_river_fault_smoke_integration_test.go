//go:build integration

package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/postgres"
	ingestionworkspace "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/adapter/workspace"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	platformparser "github.com/CodeZen-Lizhi/zhixu/internal/platform/parser"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	reindexriver "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/river"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	reindexCredentialCanary   = "REINDEX_CREDENTIAL_CANARY_7A2F"
	reindexDSNCanary          = "postgres://REINDEX_DSN_CANARY_9C4D"
	reindexEmbeddingKeyCanary = "REINDEX_EMBEDDING_KEY_CANARY_4E8B"
)

func runReindexRiverFaultSmoke(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceRepository *workspacepostgres.Repository,
	committedGit *gitcli.WritebackClient,
	insertClient *workflowriver.Client,
	workspaceID foundation.ID,
	executionID foundation.ID,
	root string,
	targetPath string,
	approvedContent string,
) {
	t.Helper()
	driftContent := []byte("# Approval River Smoke\n\nworktree drift after committed writeback\n")
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(targetPath)), driftContent, 0o644); err != nil {
		t.Fatal(err)
	}

	ids := foundation.NewUUIDGenerator(nil)
	clock := foundation.SystemClock{}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	workspaceService := workspaceapplication.NewService(workspaceapplication.Dependencies{
		Repository: workspaceRepository, CommittedFiles: files, CommittedGit: committedGit, IDs: ids, Clock: clock,
	})
	ingestionRepository, err := ingestionpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	sourceReader, err := ingestionworkspace.NewReader(workspaceRepository, files)
	if err != nil {
		t.Fatal(err)
	}
	ingestionService, err := ingestionapplication.NewService(ingestionapplication.Dependencies{
		Repository: ingestionRepository, Sources: sourceReader,
		Parsers: platformparser.NewRegistry(platformparser.Options{MaxBytes: filesystem.DefaultMaxBytes}),
		IDs:     ids, Clock: clock,
		ContentPolicy: ingestionapplication.ContentPolicy{MaxBytes: ingestionapplication.DefaultContentMaxBytes},
		ChunkOptions: ingestiondomain.ChunkOptions{
			StrategyVersion: "structure-v1", SchemaVersion: platformparser.ParseSchemaVersion,
			SoftMaxBytes: ingestiondomain.DefaultChunkSoftMaxBytes,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	retrievalRepository, err := retrievalpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	retrievalService, err := retrievalapplication.NewService(retrievalapplication.Dependencies{Store: retrievalRepository, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	regressionService, err := retrievalapplication.NewRegressionService(retrievalRepository)
	if err != nil {
		t.Fatal(err)
	}
	embedder, closeEmbedder, providerCalls := newReindexSmokeEmbedder(t)
	defer closeEmbedder()
	contract := embedder.Contract()
	registered, err := retrievalService.RegisterEmbedding(ctx, retrievalapplication.RegisterEmbeddingRequest{
		Provider: contract.Provider, AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion,
		Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization,
		DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	vectorBuilder, err := retrievalapplication.NewVectorBuilder(retrievalapplication.VectorBuilderDependencies{
		Store: retrievalRepository, Embedder: embedder, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	faultVectors := &reindexVectorResponseLoss{builder: vectorBuilder, lose: true}
	fusion, err := retrievaldomain.CanonicalRRFConfig(retrievaldomain.RRFConfig{
		SchemaVersion: 1, Method: retrievaldomain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 20, VectorCandidateLimit: 20, FusedCandidateLimit: 10, RerankCandidateLimit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	processorOptions := retrievalapplication.DefaultFTSOnlyProcessorOptions(100 * time.Millisecond)
	embeddingVersionID := registered.EmbeddingVersion.ID
	processorOptions.EmbeddingVersionID = &embeddingVersionID
	processorOptions.FusionConfig = fusion
	deliveryRepository, err := retrievalpostgres.NewDeliveryRepository(pool, ids)
	if err != nil {
		t.Fatal(err)
	}
	deliveryRuntime, err := retrievalapplication.NewDeliveryRuntime(deliveryRepository)
	if err != nil {
		t.Fatal(err)
	}
	faultRuntime := newReindexCheckpointResponseLossRuntime(deliveryRuntime)
	faultRetrieval := &reindexReadyResponseLossRetrieval{Service: retrievalService, lose: true}
	processor, err := retrievalapplication.NewProcessor(retrievalapplication.ProcessorDependencies{
		Contexts: deliveryRepository, Capture: workspaceService, Ingestion: ingestionService,
		Retrieval: faultRetrieval, Vectors: faultVectors, Regression: regressionService,
	}, processorOptions)
	if err != nil {
		t.Fatal(err)
	}
	completionService, err := retrievalapplication.NewCompletionService(retrievalRepository, ids, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	faultCompletion := &reindexCompletionResponseLoss{service: completionService, lose: true}

	reindexInserter, err := reindexriver.NewInserter(insertClient)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := retrievalpostgres.NewDispatcher(pool, ids, reindexInserter)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}

	var deliveryID foundation.ID
	var jobID int64
	if err := pool.QueryRow(ctx, `SELECT delivery.id::text,job.id
		FROM retrieval.reindex_delivery delivery
		JOIN workflow.river_job job ON job.kind=$2 AND job.args->>'delivery_id'=delivery.id::text
		WHERE delivery.writeback_execution_id=$1`, string(executionID), reindexriver.JobKind).Scan(&deliveryID, &jobID); err != nil {
		t.Fatal(err)
	}
	var logs lockedReindexSmokeBuffer
	workerClients := make([]*workflowriver.Client, 0, 2)
	for _, owner := range []string{"reindex-smoke-a", "reindex-smoke-b"} {
		worker := newReindexFaultSmokeWorker(t, faultRuntime, processor, faultCompletion, owner)
		workers := workflowriver.NewWorkers()
		if err := reindexriver.AddWorkerSafely(workers, worker); err != nil {
			t.Fatal(err)
		}
		options := workflowriver.DefaultOptions()
		options.Queue = insertClient.Queue()
		options.MaxWorkers = 1
		options.JobTimeout = 20 * time.Second
		options.RescueStuckJobsAfter = 30 * time.Second
		options.SoftStopTimeout = 5 * time.Second
		options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		client, err := workflowriver.NewClientWithOptions(pool, workers, options)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Start(ctx); err != nil {
			t.Fatal(err)
		}
		workerClients = append(workerClients, client)
	}
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, client := range workerClients {
			if err := client.Stop(stopContext); err != nil {
				t.Errorf("stop reindex worker: %v", err)
			}
		}
	}()

	faultCodes := []string{
		"REINDEX_CHECKPOINT_RESPONSE_LOST",
		"REINDEX_CHECKPOINT_RESPONSE_LOST",
		"REINDEX_CHECKPOINT_RESPONSE_LOST",
		"REINDEX_VECTOR_RESPONSE_LOST",
		"REINDEX_CHECKPOINT_RESPONSE_LOST",
		"REINDEX_READY_RESPONSE_LOST",
		"REINDEX_COMPLETION_RESPONSE_LOST",
	}
	for index, code := range faultCodes {
		waitForReindexRiverRetry(t, ctx, pool, deliveryID, jobID, index+1, code)
	}
	if pending := faultRuntime.pendingStages(); len(pending) != 0 || faultVectors.pending() || faultRetrieval.pending() || faultCompletion.pending() {
		t.Fatalf("reindex faults were not all exercised: stages=%v vector=%v ready=%v completion=%v", pending, faultVectors.pending(), faultRetrieval.pending(), faultCompletion.pending())
	}

	waitContext, cancelWait := context.WithTimeout(ctx, 30*time.Second)
	defer cancelWait()
	waitForReindexCompletion(t, waitContext, pool, deliveryID, jobID)
	assertReindexFaultSmokePayloadsClean(t, ctx, pool, deliveryID, jobID, logs.String(), root, targetPath, approvedContent)
	assertReindexFaultSmokeFacts(t, ctx, pool, workspaceID, executionID, deliveryID, root, targetPath, approvedContent, driftContent)
	assertHybridReindexAndSearchSmoke(t, ctx, pool, embedder, workspaceID, deliveryID, registered.EmbeddingVersion.ID, providerCalls)
}

func newReindexFaultSmokeWorker(
	t *testing.T,
	runtime *reindexCheckpointResponseLossRuntime,
	processor retrievalapplication.ProcessorRunner,
	completion *reindexCompletionResponseLoss,
	owner string,
) *reindexriver.Worker {
	t.Helper()
	worker, err := reindexriver.NewWorker(runtime, processor, completion, reindexriver.WorkerOptions{
		Owner: owner, LeaseDuration: 750 * time.Millisecond, HeartbeatInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

type reindexCheckpointResponseLossRuntime struct {
	*retrievalapplication.DeliveryRuntime
	mu        sync.Mutex
	remaining map[retrievaldomain.DeliveryCheckpointStage]bool
}

type reindexVectorResponseLoss struct {
	builder *retrievalapplication.VectorBuilder
	mu      sync.Mutex
	lose    bool
}

func (vectors *reindexVectorResponseLoss) BuildNextVectorBatch(ctx context.Context, request retrievalapplication.BuildNextVectorBatchRequest) (retrievalapplication.BuildNextVectorBatchResult, error) {
	result, err := vectors.builder.BuildNextVectorBatch(ctx, request)
	if err != nil {
		return result, err
	}
	vectors.mu.Lock()
	defer vectors.mu.Unlock()
	if vectors.lose && result.ProcessedCount > 0 {
		vectors.lose = false
		return result, errors.New("REINDEX_VECTOR_RESPONSE_LOST: durable vector batch committed before response was lost")
	}
	return result, nil
}

func (vectors *reindexVectorResponseLoss) pending() bool {
	vectors.mu.Lock()
	defer vectors.mu.Unlock()
	return vectors.lose
}

func newReindexCheckpointResponseLossRuntime(runtime *retrievalapplication.DeliveryRuntime) *reindexCheckpointResponseLossRuntime {
	return &reindexCheckpointResponseLossRuntime{
		DeliveryRuntime: runtime,
		remaining: map[retrievaldomain.DeliveryCheckpointStage]bool{
			retrievaldomain.DeliveryCheckpointSourceCaptured:   true,
			retrievaldomain.DeliveryCheckpointIngested:         true,
			retrievaldomain.DeliveryCheckpointIndexBuilding:    true,
			retrievaldomain.DeliveryCheckpointRegressionPassed: true,
		},
	}
}

func (runtime *reindexCheckpointResponseLossRuntime) Checkpoint(ctx context.Context, command retrievalapplication.DeliveryCheckpointCommand) (retrievalapplication.DeliveryMutationResult, error) {
	result, err := runtime.DeliveryRuntime.Checkpoint(ctx, command)
	if err != nil {
		return result, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.remaining[command.Checkpoint.Stage] {
		delete(runtime.remaining, command.Checkpoint.Stage)
		return result, reindexSmokeResponseLost("REINDEX_CHECKPOINT_RESPONSE_LOST")
	}
	return result, nil
}

func (runtime *reindexCheckpointResponseLossRuntime) pendingStages() []retrievaldomain.DeliveryCheckpointStage {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	result := make([]retrievaldomain.DeliveryCheckpointStage, 0, len(runtime.remaining))
	for stage := range runtime.remaining {
		result = append(result, stage)
	}
	return result
}

type reindexReadyResponseLossRetrieval struct {
	*retrievalapplication.Service
	mu   sync.Mutex
	lose bool
}

func (retrieval *reindexReadyResponseLossRetrieval) Ready(ctx context.Context, request retrievalapplication.TransitionRequest) (retrievaldomain.IndexVersion, error) {
	result, err := retrieval.Service.Ready(ctx, request)
	if err != nil {
		return result, err
	}
	retrieval.mu.Lock()
	defer retrieval.mu.Unlock()
	if retrieval.lose {
		retrieval.lose = false
		return result, reindexSmokeResponseLost("REINDEX_READY_RESPONSE_LOST")
	}
	return result, nil
}

func (retrieval *reindexReadyResponseLossRetrieval) pending() bool {
	retrieval.mu.Lock()
	defer retrieval.mu.Unlock()
	return retrieval.lose
}

type reindexCompletionResponseLoss struct {
	service *retrievalapplication.CompletionService
	mu      sync.Mutex
	lose    bool
}

func (completion *reindexCompletionResponseLoss) Complete(ctx context.Context, request retrievalapplication.CompleteReindexRequest) (retrievaldomain.CompleteReindexResult, error) {
	result, err := completion.service.Complete(ctx, request)
	if err != nil {
		return result, err
	}
	completion.mu.Lock()
	defer completion.mu.Unlock()
	if completion.lose {
		completion.lose = false
		return result, reindexSmokeResponseLost("REINDEX_COMPLETION_RESPONSE_LOST")
	}
	return result, nil
}

func (completion *reindexCompletionResponseLoss) pending() bool {
	completion.mu.Lock()
	defer completion.mu.Unlock()
	return completion.lose
}

func waitForReindexLeaseExpiry(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deliveryID foundation.ID) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		err := pool.QueryRow(ctx, `SELECT clock_timestamp() >= attempt.lease_until
			FROM retrieval.reindex_delivery delivery
			JOIN retrieval.reindex_delivery_attempt attempt ON attempt.id=delivery.current_attempt_id
			WHERE delivery.id=$1`, string(deliveryID)).Scan(&expired)
		if err == nil && expired {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("delivery lease did not expire: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForReindexRiverRetry(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deliveryID foundation.ID, jobID int64, attempt int, code string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	var riverErrors, jobState string
	var riverAttempt int
	for {
		err := pool.QueryRow(ctx, `SELECT state,attempt,errors::text FROM workflow.river_job WHERE id=$1`, jobID).Scan(&jobState, &riverAttempt, &riverErrors)
		if err == nil && (jobState == "available" || jobState == "retryable") && riverAttempt == attempt && strings.Contains(riverErrors, code) {
			tag, updateErr := pool.Exec(ctx, `UPDATE workflow.river_job
				SET state='retryable',scheduled_at=clock_timestamp()+interval '1 minute'
				WHERE id=$1 AND state IN ('available','retryable') AND attempt=$2`, jobID, attempt)
			if updateErr != nil {
				t.Fatal(updateErr)
			}
			if tag.RowsAffected() == 1 {
				break
			}
		}
		if ctx.Err() != nil {
			failReindexRiverRetryWait(t, pool, deliveryID, jobID, attempt, code, ctx.Err())
		}
		select {
		case <-ctx.Done():
			failReindexRiverRetryWait(t, pool, deliveryID, jobID, attempt, code, ctx.Err())
		case <-deadline.C:
			failReindexRiverRetryWait(t, pool, deliveryID, jobID, attempt, code, errors.New("River retry observation timed out"))
		case <-ticker.C:
		}
	}
	assertReindexTextClean(t, "River errors", riverErrors, reindexCredentialCanary, reindexDSNCanary, reindexEmbeddingKeyCanary)

	var deliveryStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM retrieval.reindex_delivery WHERE id=$1`, string(deliveryID)).Scan(&deliveryStatus); err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != string(retrievaldomain.DeliveryStatusSucceeded) {
		waitForReindexLeaseExpiry(t, ctx, pool, deliveryID)
	}
	tag, err := pool.Exec(ctx, `UPDATE workflow.river_job
		SET state='available',scheduled_at=clock_timestamp()
		WHERE id=$1 AND state='retryable' AND attempt=$2`, jobID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("River retry attempt %d could not be released", attempt)
	}
}

func failReindexRiverRetryWait(t *testing.T, pool *pgxpool.Pool, deliveryID foundation.ID, jobID int64, attempt int, code string, waitErr error) {
	t.Helper()
	var jobState, riverErrors, deliveryStatus string
	var riverAttempt, deliveryAttempt int
	_ = pool.QueryRow(context.Background(), `SELECT state,attempt,errors::text FROM workflow.river_job WHERE id=$1`, jobID).Scan(&jobState, &riverAttempt, &riverErrors)
	_ = pool.QueryRow(context.Background(), `SELECT status,attempt_no FROM retrieval.reindex_delivery WHERE id=$1`, string(deliveryID)).Scan(&deliveryStatus, &deliveryAttempt)
	t.Fatalf("River retry attempt %d code=%s was not recorded: job_state=%s river_attempt=%d delivery=%s delivery_attempt=%d errors=%s err=%v",
		attempt, code, jobState, riverAttempt, deliveryStatus, deliveryAttempt, riverErrors, waitErr)
}

func assertReindexFaultSmokePayloadsClean(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deliveryID foundation.ID, jobID int64, logs, root, targetPath, approvedContent string) {
	t.Helper()
	var riverPayload, deliveryPayload, attemptPayload string
	if err := pool.QueryRow(ctx, `SELECT concat_ws(' ',args::text,metadata::text,errors::text)
		FROM workflow.river_job WHERE id=$1`, jobID).Scan(&riverPayload); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT concat_ws(' ',consumer_name,status,COALESCE(error_code,''),COALESCE(error_summary,''))
		FROM retrieval.reindex_delivery WHERE id=$1`, string(deliveryID)).Scan(&deliveryPayload); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(string_agg(concat_ws(' ',delivery_key,lease_owner,status,COALESCE(error_code,''),COALESCE(error_summary,'')), ' ' ORDER BY attempt_no),'')
		FROM retrieval.reindex_delivery_attempt WHERE delivery_id=$1`, string(deliveryID)).Scan(&attemptPayload); err != nil {
		t.Fatal(err)
	}
	for label, payload := range map[string]string{
		"River payload": riverPayload,
		"Delivery":      deliveryPayload,
		"Attempt":       attemptPayload,
		"logs":          logs,
	} {
		assertReindexTextClean(t, label, payload, root, targetPath, approvedContent, reindexCredentialCanary, reindexDSNCanary, reindexEmbeddingKeyCanary)
	}
}

func assertReindexTextClean(t *testing.T, label, payload string, forbidden ...string) {
	t.Helper()
	lower := strings.ToLower(payload)
	for _, value := range forbidden {
		if value != "" && strings.Contains(lower, strings.ToLower(value)) {
			t.Fatalf("%s leaked %q: %s", label, value, payload)
		}
	}
}

type lockedReindexSmokeBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedReindexSmokeBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *lockedReindexSmokeBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func waitForReindexCompletion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deliveryID foundation.ID, jobID int64) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var deliveryStatus, jobState string
		err := pool.QueryRow(ctx, `SELECT delivery.status,job.state
			FROM retrieval.reindex_delivery delivery
			JOIN workflow.river_job job ON job.id=$2
			WHERE delivery.id=$1`, string(deliveryID), jobID).Scan(&deliveryStatus, &jobState)
		if err == nil && deliveryStatus == string(retrievaldomain.DeliveryStatusSucceeded) && jobState == "completed" {
			return
		}
		if err != nil {
			if ctx.Err() != nil {
				failReindexCompletionWait(t, pool, deliveryID, jobID, deliveryStatus, jobState, ctx.Err())
			}
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			failReindexCompletionWait(t, pool, deliveryID, jobID, deliveryStatus, jobState, ctx.Err())
		case <-ticker.C:
		}
	}
}

func failReindexCompletionWait(t *testing.T, pool *pgxpool.Pool, deliveryID foundation.ID, jobID int64, deliveryStatus, jobState string, waitErr error) {
	t.Helper()
	var jobErrors string
	var attemptStatus, leaseOwner string
	var leaseUntil time.Time
	_ = pool.QueryRow(context.Background(), `SELECT errors::text FROM workflow.river_job WHERE id=$1`, jobID).Scan(&jobErrors)
	_ = pool.QueryRow(context.Background(), `SELECT attempt.status,attempt.lease_owner,attempt.lease_until
		FROM retrieval.reindex_delivery delivery
		JOIN retrieval.reindex_delivery_attempt attempt ON attempt.id=delivery.current_attempt_id
		WHERE delivery.id=$1`, string(deliveryID)).Scan(&attemptStatus, &leaseOwner, &leaseUntil)
	t.Fatalf("reindex did not complete: delivery=%s job=%s attempt=%s owner=%s lease_until=%s errors=%s err=%v",
		deliveryStatus, jobState, attemptStatus, leaseOwner, leaseUntil.UTC().Format(time.RFC3339Nano), jobErrors, waitErr)
}

func assertReindexFaultSmokeFacts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID foundation.ID,
	executionID foundation.ID,
	deliveryID foundation.ID,
	root string,
	targetPath string,
	approvedContent string,
	driftContent []byte,
) {
	t.Helper()
	var deliveryStatus, executionStatus, proposalStatus, indexStatus, resultHash string
	var attemptNo, activationCount, activeCount, sourceCount, chunkCount int
	err := pool.QueryRow(ctx, `SELECT delivery.status,delivery.attempt_no,execution.status,proposal.status,index_version.status,execution.result_hash,
		(SELECT count(*) FROM retrieval.index_activation activation WHERE activation.workspace_id=$1),
		(SELECT count(*) FROM retrieval.index_version active WHERE active.workspace_id=$1 AND active.status='active'),
		(SELECT count(*) FROM retrieval.index_manifest_source source_manifest WHERE source_manifest.index_version_id=delivery.index_version_id AND source_manifest.selection_status='included'),
		(SELECT count(*) FROM retrieval.index_manifest_chunk chunk_manifest WHERE chunk_manifest.index_version_id=delivery.index_version_id)
		FROM retrieval.reindex_delivery delivery
		JOIN change_control.writeback_execution execution ON execution.id=delivery.writeback_execution_id
		JOIN change_control.proposal proposal ON proposal.id=execution.proposal_id
		JOIN retrieval.index_version index_version ON index_version.id=delivery.index_version_id
		WHERE delivery.id=$2 AND execution.id=$3`, string(workspaceID), string(deliveryID), string(executionID)).Scan(
		&deliveryStatus, &attemptNo, &executionStatus, &proposalStatus, &indexStatus, &resultHash,
		&activationCount, &activeCount, &sourceCount, &chunkCount,
	)
	if err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != string(retrievaldomain.DeliveryStatusSucceeded) || executionStatus != "completed" || proposalStatus != "completed" ||
		indexStatus != string(retrievaldomain.IndexStatusActive) || attemptNo < 7 || activationCount != 1 || activeCount != 1 || sourceCount != 1 || chunkCount < 1 {
		t.Fatalf("delivery=%s attempts=%d execution=%s proposal=%s index=%s activations=%d active=%d sources=%d chunks=%d",
			deliveryStatus, attemptNo, executionStatus, proposalStatus, indexStatus, activationCount, activeCount, sourceCount, chunkCount)
	}
	artifactBytes, err := os.ReadFile(filepath.Join(root, ".knowledge", "sources", resultHash))
	if err != nil {
		t.Fatal(err)
	}
	if string(artifactBytes) != approvedContent {
		t.Fatalf("committed artifact=%q want=%q", artifactBytes, approvedContent)
	}
	worktreeBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(targetPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(worktreeBytes) != string(driftContent) || string(worktreeBytes) == string(artifactBytes) {
		t.Fatalf("worktree=%q artifact=%q", worktreeBytes, artifactBytes)
	}
}

func newReindexSmokeEmbedder(t *testing.T) (retrievalapplication.Embedder, func(), *atomic.Int32) {
	t.Helper()
	const model = "reindex-smoke-embed-v1"
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/embeddings" ||
			request.Header.Get("Authorization") != "Bearer "+reindexEmbeddingKeyCanary {
			http.Error(response, "rejected", http.StatusUnauthorized)
			return
		}
		var payload struct {
			Input          []string `json:"input"`
			Model          string   `json:"model"`
			EncodingFormat string   `json:"encoding_format"`
			Dimensions     int32    `json:"dimensions"`
		}
		decoder := json.NewDecoder(request.Body)
		if decoder.Decode(&payload) != nil || len(payload.Input) == 0 || payload.Model != model ||
			payload.EncodingFormat != "float" || payload.Dimensions != 3 {
			http.Error(response, "invalid", http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, len(payload.Input))
		for index := range payload.Input {
			data[index] = map[string]any{"index": index, "embedding": []float32{1, 0, 0}}
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"model": model, "data": data})
	}))
	embedder, err := platformmodels.NewOpenAICompatibleEmbedder(platformmodels.OpenAIEmbeddingOptions{
		Client: server.Client(), BaseURL: server.URL, APIKey: reindexEmbeddingKeyCanary, Model: model,
		Dimensions: 3, Normalization: retrievaldomain.NormalizationL2, DistanceMetric: retrievaldomain.DistanceCosine,
		MaxBatchSize: 128, MaxInputBytes: 64 * 1024, MaxBatchInputBytes: 8 * 1024 * 1024,
		Timeout: 5 * time.Second, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return embedder, server.Close, &calls
}

func assertHybridReindexAndSearchSmoke(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	embedder retrievalapplication.Embedder,
	workspaceID foundation.ID,
	deliveryID foundation.ID,
	embeddingVersionID foundation.ID,
	providerCalls *atomic.Int32,
) {
	t.Helper()
	var indexID, persistedEmbeddingID foundation.ID
	var degraded, regressionCode string
	var readyVectors, cacheRows int64
	if err := pool.QueryRow(ctx, `SELECT index_version.id::text,index_version.embedding_version_id::text,
		index_version.degraded_capabilities::text,delivery.regression_code,
		(SELECT count(*) FROM retrieval.chunk_projection projection
		 WHERE projection.index_version_id=index_version.id AND projection.vector_status='ready'),
		(SELECT count(*) FROM retrieval.embedding_cache cache
		 WHERE cache.workspace_id=index_version.workspace_id AND cache.embedding_version_id=index_version.embedding_version_id)
		FROM retrieval.reindex_delivery delivery
		JOIN retrieval.index_version index_version ON index_version.id=delivery.index_version_id
		WHERE delivery.id=$1`, string(deliveryID)).Scan(
		&indexID, &persistedEmbeddingID, &degraded, &regressionCode, &readyVectors, &cacheRows,
	); err != nil {
		t.Fatal(err)
	}
	if persistedEmbeddingID != embeddingVersionID || degraded != "[]" ||
		regressionCode != retrievaldomain.SnapshotStructureRegressionV2 || readyVectors < 1 || cacheRows < 1 {
		t.Fatalf("hybrid index=%s embedding=%s degraded=%s regression=%s vectors=%d cache=%d",
			indexID, persistedEmbeddingID, degraded, regressionCode, readyVectors, cacheRows)
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	providerCallsBeforeSearch := providerCalls.Load()
	result, err := searchService.Search(ctx, retrievaldomain.SearchRequest{
		WorkspaceID: workspaceID, Query: "approved", Mode: retrievaldomain.SearchModeHybrid, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexVersionID != indexID || result.EffectiveMode != retrievaldomain.SearchModeHybrid || len(result.Items) == 0 ||
		len(result.Degradations) != 1 || result.Degradations[0].Capability != retrievaldomain.SearchDegradationRerank {
		t.Fatalf("hybrid search result=%#v", result)
	}
	providerCallsAfterSearch := providerCalls.Load()
	if providerCallsBeforeSearch < 1 || providerCallsAfterSearch != providerCallsBeforeSearch+1 {
		t.Fatalf("provider calls before=%d after=%d", providerCallsBeforeSearch, providerCallsAfterSearch)
	}
}

func reindexSmokeResponseLost(code string) error {
	return foundation.NewError(foundation.ErrorRetryableFailure, code, true, errors.New("durable reindex operation committed before response was lost"))
}
