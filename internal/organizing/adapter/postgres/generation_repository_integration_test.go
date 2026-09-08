//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGenerationRepositoryEnforcesRuntimeAtEveryPersistenceBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := newGenerationRepositoryIntegrationFixture(t, ctx)

	for _, kind := range []organizingworkflow.GenerationKind{
		organizingworkflow.GenerationOutline,
		organizingworkflow.GenerationDocument,
	} {
		t.Run("bind and replay "+string(kind), func(t *testing.T) {
			generation, run := fixture.prepare(t, kind, nil)
			bound := fixture.bind(t, generation, run)
			if bound.Version != 2 || bound.ModelRunID != run.ID {
				t.Fatalf("bound generation=%#v", bound)
			}
			replayed, err := fixture.generations.BindModelRun(fixture.ctx, organizingworkflow.BindGenerationModelRunCommand{
				WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID,
				ExpectedVersion: generation.Version, ModelRunID: run.ID,
			})
			if err != nil || replayed.ID != generation.ID || replayed.Version != 2 {
				t.Fatalf("replayed generation=%#v err=%v", replayed, err)
			}
		})
	}

	runtimeCases := []struct {
		name   string
		mutate func(*agentdomain.ModelRun)
	}{
		{name: "prompt", mutate: func(run *agentdomain.ModelRun) { run.Prompt.ID = organizingworkflow.DocumentPromptID }},
		{name: "primary schema", mutate: func(run *agentdomain.ModelRun) { run.Schema.ID = organizingworkflow.DocumentSchemaID }},
		{name: "reduced schema", mutate: func(run *agentdomain.ModelRun) { run.ReducedSchema.ID = organizingworkflow.DocumentReducedSchemaID }},
		{name: "runtime version", mutate: func(run *agentdomain.ModelRun) { run.Schema.Version = "v2" }},
	}
	for _, test := range runtimeCases {
		t.Run("bind rejects "+test.name, func(t *testing.T) {
			generation, run := fixture.prepare(t, organizingworkflow.GenerationOutline, test.mutate)
			_, err := fixture.generations.BindModelRun(fixture.ctx, organizingworkflow.BindGenerationModelRunCommand{
				WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID,
				ExpectedVersion: generation.Version, ModelRunID: run.ID,
			})
			assertGenerationConsistencyError(t, err)
		})
	}

	t.Run("bind replay revalidates runtime", func(t *testing.T) {
		generation, run := fixture.prepare(t, organizingworkflow.GenerationOutline, nil)
		fixture.bind(t, generation, run)
		fixture.faultInjectModelRun(t, run.ID, `UPDATE agent.model_run SET prompt_template_id='other-prompt' WHERE id=$1`)
		_, err := fixture.generations.BindModelRun(fixture.ctx, organizingworkflow.BindGenerationModelRunCommand{
			WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID,
			ExpectedVersion: generation.Version, ModelRunID: run.ID,
		})
		assertGenerationConsistencyError(t, err)
	})

	t.Run("complete rejects cross-kind result", func(t *testing.T) {
		generation, run := fixture.prepare(t, organizingworkflow.GenerationOutline, nil)
		bound := fixture.bind(t, generation, run)
		output := []byte(`{}`)
		_, _, err := fixture.generations.Complete(fixture.ctx, organizingworkflow.CompleteGenerationCommand{
			WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID, ExpectedVersion: bound.Version,
			ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
			ResultType: agentdomain.ResultTypeOrganizingDocument, Output: output,
			OutputHash: sha256Bytes(output), CompletedAt: fixture.nextTime(),
		})
		assertGenerationConsistencyError(t, err)
	})

	t.Run("fail revalidates runtime", func(t *testing.T) {
		generation, run := fixture.prepare(t, organizingworkflow.GenerationDocument, nil)
		bound := fixture.bind(t, generation, run)
		fixture.faultInjectModelRun(t, run.ID, `UPDATE agent.model_run SET reduced_schema_id='other-reduced' WHERE id=$1`)
		err := fixture.generations.Fail(fixture.ctx, organizingworkflow.FailGenerationCommand{
			WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID, ExpectedVersion: bound.Version,
			ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
			ModelRunStatus: agentdomain.ModelRunFailed, ErrorCode: "PROVIDER_FAILED", Retryable: true,
			CompletedAt: fixture.nextTime(),
		})
		assertGenerationConsistencyError(t, err)
	})

	t.Run("complete accepts and replays exact runtime", func(t *testing.T) {
		generation, run := fixture.prepare(t, organizingworkflow.GenerationOutline, nil)
		bound := fixture.bind(t, generation, run)
		output := []byte(`{"outline":[]}`)
		fixture.recordSuccessfulCall(t, run, agentdomain.ModelCallInitial, run.Schema, output, nil)
		command := organizingworkflow.CompleteGenerationCommand{
			WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID, ExpectedVersion: bound.Version,
			ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
			ResultType: agentdomain.ResultTypeOrganizingOutline, Output: output,
			OutputHash: sha256Bytes(output), CompletedAt: fixture.nextTime(),
		}
		ready, replayed, err := fixture.generations.Complete(fixture.ctx, command)
		if err != nil || replayed || ready.Status != organizingworkflow.GenerationReady {
			t.Fatalf("complete ready=%#v replayed=%v err=%v", ready, replayed, err)
		}
		replayedRecord, replayed, err := fixture.generations.Complete(fixture.ctx, command)
		if err != nil || !replayed || replayedRecord.ID != ready.ID {
			t.Fatalf("complete replay=%#v replayed=%v err=%v", replayedRecord, replayed, err)
		}
	})

	callCases := []struct {
		name   string
		phase  agentdomain.ModelCallPhase
		schema func(agentdomain.ModelRun) agentdomain.SchemaRef
		mutate func(*agentdomain.ModelCall)
	}{
		{name: "model", phase: agentdomain.ModelCallInitial, schema: primaryGenerationSchema, mutate: func(call *agentdomain.ModelCall) { call.Model.ModelID = "other-model" }},
		{name: "profile", phase: agentdomain.ModelCallInitial, schema: primaryGenerationSchema, mutate: func(call *agentdomain.ModelCall) { call.Profile.ID = "other-profile" }},
		{name: "prompt", phase: agentdomain.ModelCallInitial, schema: primaryGenerationSchema, mutate: func(call *agentdomain.ModelCall) { call.Prompt.ID = "other-prompt" }},
		{name: "initial reduced schema", phase: agentdomain.ModelCallInitial, schema: reducedGenerationSchema},
		{name: "reduced primary schema", phase: agentdomain.ModelCallReduced, schema: primaryGenerationSchema},
		{name: "plan phase", phase: agentdomain.ModelCallPlan, schema: primaryGenerationSchema},
		{name: "review phase", phase: agentdomain.ModelCallReview, schema: primaryGenerationSchema},
	}
	for _, test := range callCases {
		t.Run("complete rejects call "+test.name, func(t *testing.T) {
			generation, run := fixture.prepare(t, organizingworkflow.GenerationOutline, nil)
			bound := fixture.bind(t, generation, run)
			output := []byte(`{"outline":[]}`)
			fixture.recordSuccessfulCall(t, run, test.phase, test.schema(run), output, test.mutate)
			_, _, err := fixture.generations.Complete(fixture.ctx, organizingworkflow.CompleteGenerationCommand{
				WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID, ExpectedVersion: bound.Version,
				ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
				ResultType: agentdomain.ResultTypeOrganizingOutline, Output: output,
				OutputHash: sha256Bytes(output), CompletedAt: fixture.nextTime(),
			})
			assertGenerationConsistencyError(t, err)
		})
	}
}

type generationRepositoryIntegrationFixture struct {
	ctx          context.Context
	pool         *pgxpool.Pool
	agent        *agentpostgres.GORMRepository
	generations  *GORMGenerationRepository
	workspaceID  foundation.ID
	snapshotID   foundation.ID
	workflowRun  foundation.ID
	retrievalID  foundation.ID
	nextSeed     int
	logicalClock time.Time
}

func newGenerationRepositoryIntegrationFixture(t *testing.T, ctx context.Context) *generationRepositoryIntegrationFixture {
	t.Helper()
	platform := newOrganizingIntegrationPlatform(t, ctx)
	pool := platform.DB()
	workspaceID := organizingIntegrationID(2100)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-generation-runtime")
	organizingRepository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := organizingRepository.EnsureBuiltIns(ctx, base); err != nil {
		t.Fatal(err)
	}
	templates, revisions, err := organizingdomain.BuiltInTemplates(base)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := organizingRepository.ConfirmDraft(ctx, seedOrganizingConfirmRecord(
		t, ctx, organizingRepository, workspaceID, templates[0], revisions[0], &organizingIntegrationFence{}, 2200, base,
	))
	if err != nil || confirmed.Replayed {
		t.Fatalf("confirm generation fixture=%#v err=%v", confirmed, err)
	}
	definitionID := organizingIntegrationID(2300)
	workflowRunID := organizingIntegrationID(2301)
	logicalClock := base.Add(10 * time.Minute)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'organizing.topic-article',1,'{}',$3)`, string(definitionID), string(workspaceID), logicalClock); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash
	) VALUES($1,$2,$3,'running',jsonb_build_object('snapshot_id',$4::text),1,$5,$5,$6,$7)`,
		string(workflowRunID), string(workspaceID), string(definitionID), string(confirmed.Snapshot.ID), logicalClock,
		organizingapp.StartIdempotencyKey(confirmed.Snapshot.ID), organizingIntegrationHash("organizing-generation-runtime")); err != nil {
		t.Fatal(err)
	}
	lease, claimed, err := organizingRepository.ClaimStart(ctx, "organizing-generation-runtime", time.Minute)
	if err != nil || !claimed || lease.SnapshotID != confirmed.Snapshot.ID {
		t.Fatalf("claim generation start lease=%#v claimed=%v err=%v", lease, claimed, err)
	}
	if _, replayed, err := organizingRepository.CompleteStart(ctx, organizingapp.CompleteStartRecord{
		Lease: lease, BindingID: organizingIntegrationID(2303), WorkflowRunID: workflowRunID,
		DefinitionKey: "organizing.topic-article", DefinitionVersion: 1, StartedAt: time.Now().UTC(),
	}); err != nil || replayed {
		t.Fatalf("complete generation start replayed=%v err=%v", replayed, err)
	}
	retrievalID := organizingIntegrationID(2302)
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,
		degraded_capabilities,version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',$3,'{}','organizing-generation-runtime',$4,0,
		'organizing-generation-runtime','building','["vector"]',1,$5,$5)`,
		string(retrievalID), string(workspaceID), organizingIntegrationHash("organizing-generation-tokenizer"),
		organizingIntegrationHash("organizing-generation-manifest"), logicalClock); err != nil {
		t.Fatal(err)
	}
	agentRepository, err := agentpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	generationRepository, err := NewGORMGenerationRepository(platform, agentRepository)
	if err != nil {
		t.Fatal(err)
	}
	return &generationRepositoryIntegrationFixture{
		ctx: ctx, pool: pool, agent: agentRepository, generations: generationRepository,
		workspaceID: workspaceID, snapshotID: confirmed.Snapshot.ID, workflowRun: workflowRunID,
		retrievalID: retrievalID, nextSeed: 3000, logicalClock: logicalClock,
	}
}

func (fixture *generationRepositoryIntegrationFixture) prepare(t *testing.T, kind organizingworkflow.GenerationKind,
	mutate func(*agentdomain.ModelRun),
) (organizingworkflow.GenerationRecord, agentdomain.ModelRun) {
	t.Helper()
	nodeRunID := fixture.nextID()
	nodeAttemptID := fixture.nextID()
	generationID := fixture.nextID()
	modelRunID := fixture.nextID()
	at := fixture.nextTime()
	seedOrganizingRunningNodeAttempt(t, fixture.ctx, fixture.pool, fixture.workflowRun, nodeRunID, nodeAttemptID,
		fmt.Sprintf("generation-runtime-%d", fixture.nextSeed), at.Add(time.Hour), at)
	record := organizingworkflow.GenerationRecord{
		ID: generationID, WorkspaceID: fixture.workspaceID, SnapshotID: fixture.snapshotID,
		WorkflowRunID: fixture.workflowRun, NodeRunID: nodeRunID, NodeAttemptID: nodeAttemptID,
		Kind: kind, RequestHash: organizingIntegrationHash(string(generationID)),
		Status: organizingworkflow.GenerationRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	prepared, replayed, err := fixture.generations.Prepare(fixture.ctx, organizingworkflow.PrepareGenerationCommand{Record: record})
	if err != nil || replayed {
		var cause error
		if classified, ok := err.(*foundation.Error); ok {
			cause = classified.Cause
		}
		t.Fatalf("prepare generation=%#v replayed=%v err=%v cause=%v", prepared, replayed, err, cause)
	}
	prompt, schema, reduced := generationRuntimeRefs(kind)
	run := agentdomain.ModelRun{
		ID: modelRunID, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.workflowRun,
		NodeRunID: nodeRunID, NodeAttemptID: nodeAttemptID,
		Model:   agentdomain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"},
		Profile: agentdomain.ModelProfileRef{ID: "default", Version: "v1"}, Prompt: prompt,
		Schema: schema, ReducedSchema: reduced,
		Retrieval: agentdomain.RetrievalRef{IndexVersionID: fixture.retrievalID},
		Status:    agentdomain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	if mutate != nil {
		mutate(&run)
	}
	created, modelReplayed, err := fixture.agent.CreateModelRun(fixture.ctx, run)
	if err != nil || modelReplayed || created.ID != run.ID {
		t.Fatalf("create model run=%#v replayed=%v err=%v", created, modelReplayed, err)
	}
	return prepared, run
}

func (fixture *generationRepositoryIntegrationFixture) bind(t *testing.T,
	generation organizingworkflow.GenerationRecord, run agentdomain.ModelRun,
) organizingworkflow.GenerationRecord {
	t.Helper()
	bound, err := fixture.generations.BindModelRun(fixture.ctx, organizingworkflow.BindGenerationModelRunCommand{
		WorkspaceID: generation.WorkspaceID, GenerationID: generation.ID,
		ExpectedVersion: generation.Version, ModelRunID: run.ID,
	})
	if err != nil {
		t.Fatalf("bind model run: %v", err)
	}
	return bound
}

func (fixture *generationRepositoryIntegrationFixture) recordSuccessfulCall(t *testing.T, run agentdomain.ModelRun,
	phase agentdomain.ModelCallPhase, schema agentdomain.SchemaRef, output []byte, mutate func(*agentdomain.ModelCall),
) {
	t.Helper()
	callNo := 1
	if phase == agentdomain.ModelCallReduced {
		fixture.recordSuccessfulModelCall(t, run, 1, agentdomain.ModelCallInitial, run.Schema, []byte(`{"attempt":1}`), nil)
		fixture.recordSuccessfulModelCall(t, run, 2, agentdomain.ModelCallRepair, run.Schema, []byte(`{"attempt":2}`), nil)
		callNo = 3
	} else if phase == agentdomain.ModelCallReview {
		fixture.recordSuccessfulModelCall(t, run, 1, agentdomain.ModelCallInitial, run.Schema, []byte(`{"attempt":1}`), nil)
		callNo = 2
	}
	fixture.recordSuccessfulModelCall(t, run, callNo, phase, schema, output, mutate)
}

func (fixture *generationRepositoryIntegrationFixture) recordSuccessfulModelCall(t *testing.T, run agentdomain.ModelRun,
	callNo int, phase agentdomain.ModelCallPhase, schema agentdomain.SchemaRef, output []byte, mutate func(*agentdomain.ModelCall),
) {
	t.Helper()
	startedAt := fixture.nextTime()
	call := agentdomain.ModelCall{
		ID: fixture.nextID(), ModelRunID: run.ID, CallNo: callNo, Phase: phase,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: schema, MaxOutputTokens: 128,
		Status: agentdomain.ModelCallStarted, RequestHash: organizingIntegrationHash("request-" + string(run.ID)),
		RequestBytes: 64, Version: 1, StartedAt: startedAt,
	}
	if mutate != nil {
		mutate(&call)
	}
	if _, replayed, err := fixture.agent.StartModelCall(fixture.ctx, run.WorkspaceID, call); err != nil || replayed {
		t.Fatalf("start model call replayed=%v err=%v", replayed, err)
	}
	completedAt := fixture.nextTime()
	call.Status = agentdomain.ModelCallSucceeded
	call.ResponseHash = sha256Bytes(output)
	call.ResponseBytes = int64(len(output))
	call.Usage = agentdomain.TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
	call.LatencyMillis = 1
	call.Version = 2
	call.CompletedAt = &completedAt
	if _, replayed, err := fixture.agent.CompleteModelCall(fixture.ctx, agentapp.CompleteModelCallCommand{
		WorkspaceID: run.WorkspaceID, ExpectedVersion: 1, Call: call,
	}); err != nil || replayed {
		t.Fatalf("complete model call replayed=%v err=%v", replayed, err)
	}
}

func (fixture *generationRepositoryIntegrationFixture) faultInjectModelRun(t *testing.T, runID foundation.ID, query string) {
	t.Helper()
	connection, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	tx, err := connection.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(fixture.ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(fixture.ctx, query, string(runID)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
}

func (fixture *generationRepositoryIntegrationFixture) nextID() foundation.ID {
	fixture.nextSeed++
	return organizingIntegrationID(fixture.nextSeed)
}

func (fixture *generationRepositoryIntegrationFixture) nextTime() time.Time {
	fixture.logicalClock = fixture.logicalClock.Add(time.Millisecond)
	return fixture.logicalClock
}

func generationRuntimeRefs(kind organizingworkflow.GenerationKind) (agentdomain.PromptRef, agentdomain.SchemaRef, agentdomain.SchemaRef) {
	version := organizingworkflow.GenerationRuntimeVersion
	if kind == organizingworkflow.GenerationDocument {
		return agentdomain.PromptRef{ID: organizingworkflow.DocumentPromptID, Version: version},
			agentdomain.SchemaRef{ID: organizingworkflow.DocumentSchemaID, Version: version},
			agentdomain.SchemaRef{ID: organizingworkflow.DocumentReducedSchemaID, Version: version}
	}
	return agentdomain.PromptRef{ID: organizingworkflow.OutlinePromptID, Version: version},
		agentdomain.SchemaRef{ID: organizingworkflow.OutlineSchemaID, Version: version},
		agentdomain.SchemaRef{ID: organizingworkflow.OutlineReducedSchemaID, Version: version}
}

func primaryGenerationSchema(run agentdomain.ModelRun) agentdomain.SchemaRef { return run.Schema }

func reducedGenerationSchema(run agentdomain.ModelRun) agentdomain.SchemaRef {
	return run.ReducedSchema
}

func assertGenerationConsistencyError(t *testing.T, err error) {
	t.Helper()
	if !organizingIntegrationError(err, foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_RESULT_INVALID") {
		t.Fatalf("expected organizing generation consistency error, got %v", err)
	}
}
