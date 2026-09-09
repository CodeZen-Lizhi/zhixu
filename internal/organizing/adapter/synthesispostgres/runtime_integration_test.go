//go:build integration

package synthesispostgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringpostgres "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/postgres"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

const runtimeFactOutput = `{"notes":[{"note":"","topic_key":"cache lifetime","title":"Cache lifetime","aliases":[],"operations":[{"op":"ADD_FACT","statement":{"text":"Entries expire after five minutes.","applicability":"","sources":["S001"]}}]}]}`
const runtimeFactReview = `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[{"source":"S001","verdict":"SUPPORTED"}]}]}`
const runtimeSupportOutput = `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":null,"sources":["S001"]}]}]}`
const runtimeNoChangeReview = `{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[]}]}`

func TestSynthesisExecutionSchema(t *testing.T) {
	fixture := newSynthesisRuntimeDatabase(t)
	database, err := fixture.Pool().GORM()
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := database.Raw(`SELECT count(*) FROM information_schema.tables WHERE table_schema='organizing' AND table_name IN ('synthesis_processing','synthesis_execution','synthesis_model_step','synthesis_retry_receipt')`).Scan(&count).Error; err != nil || count != 4 {
		t.Fatalf("execution schema count=%d err=%v", count, err)
	}
}

// The Provider is deterministic. The real Eino scheduler, ModelRun/Call store,
// transaction fences, River delivery, candidates and Proposal owners run here.
func TestSynthesisSourceReadyRunsThroughRiver(t *testing.T) {
	raw := " \n" + runtimeFactOutput + "\n"
	f := newSynthesisRuntimeFixture(t, raw, runtimeFactReview, runtimeSupportOutput, runtimeFactReview, `{"notes":[]}`, runtimeNoChangeReview)
	f.modelStore.loseResponse.Store(true)
	first := f.source(t, "First source: Entries expire after five minutes. RAW-SOURCE-SENTINEL")
	f.dispatch(t, 1)
	processing := f.wait(t, first, organizingapp.SynthesisProcessingSucceeded)
	if f.provider.CallCount() != 2 || len(processing.RevisionIDs) != 1 {
		t.Fatalf("processing=%+v provider calls=%d", processing, f.provider.CallCount())
	}
	execution, err := f.store.LoadSynthesisExecution(t.Context(), f.workspace, processing.ID, processing.WorkflowRunID)
	if err != nil || execution.Generation == nil || execution.Semantic == nil || !execution.Semantic.Semantic.Accepted || !bytes.Equal(execution.Generation.Output, []byte(raw)) {
		t.Fatalf("accepted original bytes or independent review lost: err=%v", err)
	}
	if execution.Generation.ModelRunID == execution.Semantic.ModelRunID || execution.Generation.NodeAttemptID == execution.Semantic.NodeAttemptID {
		t.Fatal("generation and semantic validation shared a recorded attempt")
	}
	for _, step := range []*organizingapp.SynthesisModelStepRecord{execution.Generation, execution.Semantic} {
		record, err := f.modelRuns.GetModelRun(t.Context(), f.workspace, step.ModelRunID)
		if err != nil || record.Run.Status != agentdomain.ModelRunSucceeded || len(record.Calls) != 1 || record.Calls[0].ResponseHash != step.OutputHash {
			t.Fatalf("accepted output lacks recorded ModelCall proof: %+v %v", record, err)
		}
	}
	frozen, _ := json.Marshal(execution.Input)
	if strings.Contains(string(frozen), "RAW-SOURCE-SENTINEL") {
		t.Fatal("source excerpt leaked into frozen Workflow metadata")
	}
	if _, found, err := f.store.LookupReady(t.Context(), runtimeID(), execution.Generation.NodeRunID, organizingapp.SynthesisModelGenerate, execution.Generation.RequestHash); err != nil || found {
		t.Fatalf("cross workspace model lookup found output: found=%t err=%v", found, err)
	}
	notes, err := f.notes.ListCandidates(t.Context(), f.workspace)
	if err != nil || len(notes) != 1 {
		t.Fatalf("candidate notes=%d err=%v", len(notes), err)
	}
	old := notes[0]
	detail, err := f.notes.GetNote(t.Context(), f.workspace, old.Note.ID)
	if err != nil || detail.Publication == nil || detail.PublishedRevision != nil {
		t.Fatalf("candidate publication=%+v err=%v", detail, err)
	}
	oldProposal := detail.Publication.ProposalID
	second := f.source(t, "Second source: Entries expire after five minutes under the same conditions.")
	third := f.source(t, "Third source repeats information already covered.")
	batch, err := f.dispatcher.DispatchBatch(t.Context(), 10)
	if err != nil || batch.Started < 1 || batch.Started > 2 || batch.Started+batch.Waiting != 2 {
		t.Fatalf("two queued sources did not serialize: %+v %v", batch, err)
	}
	f.wait(t, second, organizingapp.SynthesisProcessingSucceeded)
	if batch.Started == 1 {
		f.dispatch(t, 1)
	}
	f.wait(t, third, organizingapp.SynthesisProcessingNoChange)
	updated, err := f.notes.GetNote(t.Context(), f.workspace, old.Note.ID)
	if err != nil || updated.CurrentRevision.RevisionNo != 2 || updated.CurrentRevision.Items[0].ID != old.Revision.Items[0].ID || updated.CurrentRevision.Items[0].Fact.Text != old.Revision.Items[0].Fact.Text || len(updated.CurrentRevision.Items[0].Fact.Sources) != 2 {
		t.Fatalf("continuous update did not preserve the old item: %+v %v", updated, err)
	}
	var proposalStatus string
	if err := f.database.Raw(`SELECT status FROM change_control.proposal WHERE id=?`, string(oldProposal)).Scan(&proposalStatus).Error; err != nil || proposalStatus != string(changecontroldomain.StatusNeedsRevision) {
		t.Fatalf("superseded proposal status=%s err=%v", proposalStatus, err)
	}
	f.count(t, "organizing.synthesis_revision", 2)
	f.count(t, "organizing.synthesis_apply_receipt", 3)
	f.count(t, "change_control.proposal", 2)
	f.count(t, "change_control.proposal_commit", 0)
	f.count(t, "core.source_version", 3)
	f.append(t, first)
	batch, err = f.dispatcher.DispatchBatch(t.Context(), 10)
	if err != nil || batch.Started != 0 || batch.Claimed != 0 || f.provider.CallCount() != 6 {
		t.Fatalf("source replay repeated work: %+v calls=%d err=%v", batch, f.provider.CallCount(), err)
	}
	generatedText, err := organizingdomain.RenderSynthesisMarkdown(f.workspace, updated.Note.ID, updated.Note.Title, updated.CurrentRevision.Items)
	if err != nil {
		t.Fatal(err)
	}
	derived := f.source(t, generatedText)
	batch, err = f.dispatcher.DispatchBatch(t.Context(), 10)
	if err != nil || batch.Skipped != 1 || batch.Started != 0 || f.provider.CallCount() != 6 {
		t.Fatalf("generated-source feedback was not excluded: %+v err=%v", batch, err)
	}
	skipped := f.wait(t, derived, organizingapp.SynthesisProcessingSkipped)
	if skipped.WorkflowRunID != "" || skipped.ModelRunID != "" {
		t.Fatal("excluded source created execution or model work")
	}
}

func TestSynthesisBusyWorkspaceDoesNotBlockOtherSourceIntake(t *testing.T) {
	f := newSynthesisRuntimeFixtureWithWorker(t, false)
	firstWorkspace := f.workspace
	f.source(t, "First Workspace source.")
	waiting := f.source(t, "First Workspace second source waits for its predecessor.")
	f.workspace = runtimeID()
	seedSynthesisRuntimeWorkspace(t, f.database, f.workspace)
	other := f.source(t, "Independent Workspace source can start immediately.")
	result, err := f.dispatcher.DispatchBatch(t.Context(), 10)
	if err != nil || result.Started != 2 || result.Waiting != 1 {
		t.Fatalf("cross Workspace intake=%+v err=%v", result, err)
	}
	var pending int64
	if err := f.database.Model(&processingModel{}).Where("workspace_id IN ? AND status=?", []string{string(firstWorkspace), string(other.WorkspaceID)}, string(organizingapp.SynthesisProcessingPending)).Count(&pending).Error; err != nil || pending != 2 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	var unpublished int64
	if err := f.database.Table("workflow.outbox_event").Where("workspace_id=? AND event_type=? AND payload->>'source_version_id'=? AND published_at IS NULL", string(waiting.WorkspaceID), workflowapp.SourceReadyEventType, string(waiting.SourceVersionID)).Count(&unpublished).Error; err != nil || unpublished != 1 {
		t.Fatalf("waiting event was lost: count=%d err=%v", unpublished, err)
	}
}

func TestSynthesisSemanticRejectionPublishesNothing(t *testing.T) {
	f := newSynthesisRuntimeFixture(t, runtimeFactOutput, strings.ReplaceAll(runtimeFactReview, "SUPPORTED", "UNSUPPORTED"))
	source := f.source(t, "Evidence that does not support the generated claim.")
	f.dispatch(t, 1)
	failed := f.wait(t, source, organizingapp.SynthesisProcessingFailed)
	if failed.Failure == nil || failed.Failure.Code != organizingapp.ErrorCodeSynthesisSemanticRejected || f.provider.CallCount() != 2 {
		t.Fatalf("semantic rejection=%+v calls=%d", failed, f.provider.CallCount())
	}
	f.count(t, "organizing.synthesis_revision", 0)
	f.count(t, "change_control.proposal", 0)
	f.count(t, "core.source_version", 1)
}

func TestSynthesisMissingModelIsVisibleAndExplicitRetryIsIdempotent(t *testing.T) {
	f := newSynthesisRuntimeFixture(t, `{"notes":[]}`, runtimeNoChangeReview)
	f.model.disabled.Store(true)
	source := f.source(t, "Source remains available when Chat has not been configured.")
	f.dispatch(t, 1)
	failed := f.wait(t, source, organizingapp.SynthesisProcessingFailed)
	if failed.Failure == nil || failed.Failure.Code != organizingapp.ErrorCodeSynthesisCapabilityUnavailable || !failed.Failure.Retryable || f.provider.CallCount() != 0 {
		t.Fatalf("missing-model failure=%+v calls=%d", failed, f.provider.CallCount())
	}
	f.model.disabled.Store(false)
	command := organizingapp.RetrySynthesisCommand{WorkspaceID: f.workspace, ProcessingID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "explicit-model-retry"}
	started, err := f.processing.RetryProcessing(t.Context(), command)
	if err != nil || started.Replayed || started.Processing.WorkflowRunID == failed.WorkflowRunID {
		t.Fatalf("explicit retry=%+v err=%v", started, err)
	}
	replay, err := f.processing.RetryProcessing(t.Context(), command)
	if err != nil || !replay.Replayed || replay.Processing.WorkflowRunID != started.Processing.WorkflowRunID {
		t.Fatalf("retry replay=%+v err=%v", replay, err)
	}
	completed := f.wait(t, source, organizingapp.SynthesisProcessingNoChange)
	if completed.WorkflowRunID != started.Processing.WorkflowRunID || f.provider.CallCount() != 2 {
		t.Fatalf("retried processing=%+v calls=%d", completed, f.provider.CallCount())
	}
	command.IdempotencyKey = "new-retry-after-success"
	command.ExpectedVersion = completed.Version
	if _, err := f.processing.RetryProcessing(t.Context(), command); err == nil {
		t.Fatal("completed processing accepted another retry")
	}
	f.count(t, "core.source_version", 1)
	f.count(t, "organizing.synthesis_execution", 2)
	f.count(t, "organizing.synthesis_retry_receipt", 1)
	f.count(t, "organizing.synthesis_revision", 0)
}

func TestSynthesisUnknownModelCommitBlocksPaidReplay(t *testing.T) {
	f := newSynthesisRuntimeFixture(t, runtimeFactOutput)
	f.modelStore.dropCommit.Store(true)
	source := f.source(t, "Entries expire after five minutes.")
	f.dispatch(t, 1)
	failed := f.wait(t, source, organizingapp.SynthesisProcessingRecoveryRequired)
	if failed.Failure == nil || failed.Failure.Retryable || f.provider.CallCount() != 1 {
		t.Fatalf("unknown model result=%+v calls=%d", failed, f.provider.CallCount())
	}
	_, err := f.processing.RetryProcessing(t.Context(), organizingapp.RetrySynthesisCommand{WorkspaceID: f.workspace, ProcessingID: failed.ID, ExpectedVersion: failed.Version, IdempotencyKey: "unsafe-retry"})
	if err == nil || f.provider.CallCount() != 1 {
		t.Fatalf("unknown call was retried: %v calls=%d", err, f.provider.CallCount())
	}
	f.count(t, "organizing.synthesis_revision", 0)
	f.count(t, "organizing.synthesis_execution", 1)
	f.count(t, "core.source_version", 1)
}

func newSynthesisRuntimeDatabase(t *testing.T) *testdb.Fixture {
	t.Helper()
	return testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 12, Migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
		if err := platformmigration.MigrateAtlasToVersion(ctx, pool, 96); err != nil {
			return err
		}
		migrator, err := riveradapter.NewMigrator(pool)
		if err != nil {
			return err
		}
		if err := migrator.Up(ctx); err != nil {
			return err
		}
		return migrator.Validate(ctx)
	}})
}

type synthesisRuntimeFixture struct {
	pool        *platformpostgres.Pool
	database    *gorm.DB
	uow         foundation.UnitOfWork
	workspace   foundation.ID
	store       *Store
	modelRuns   *agentpostgres.GORMRepository
	provider    *agentapp.DeterministicChatModel
	modelStore  *runtimeModelStore
	model       *runtimeSwitchModel
	publisher   *runtimePublisher
	artifacts   *runtimeArtifacts
	notes       *organizingapp.SynthesisService
	dispatcher  *organizingworkflow.SynthesisDispatcher
	processing  *organizingworkflow.SynthesisProcessingService
	outbox      *workflowpostgres.GORMSourceReadyOutbox
	coordinator *workflowapp.RuntimeCoordinator
	executor    *organizingworkflow.SynthesisExecutor
}

func newSynthesisRuntimeFixture(t *testing.T, outputs ...string) *synthesisRuntimeFixture {
	t.Helper()
	return newSynthesisRuntimeFixtureWithWorker(t, true, outputs...)
}

func newSynthesisRuntimeFixtureWithWorker(t *testing.T, startWorker bool, outputs ...string) *synthesisRuntimeFixture {
	t.Helper()
	f := &synthesisRuntimeFixture{pool: newSynthesisRuntimeDatabase(t).Pool(), workspace: runtimeID(), artifacts: &runtimeArtifacts{values: make(map[foundation.ID]retrievalapp.EvidenceArtifact)}}
	var err error
	f.database, err = f.pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	f.uow, err = f.pool.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	seedSynthesisRuntimeWorkspace(t, f.database, f.workspace)
	f.modelRuns, err = agentpostgres.NewGORMRepository(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := workflowpostgres.NewGORMRuntimeBindingReader(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	f.store, err = NewStore(f.pool, Dependencies{ModelRuns: f.modelRuns, WorkflowFence: fence, WorkflowBindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(f.pool, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: f.store})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := workflowpostgres.NewGORMRepository(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := organizingowner.NewGORMSynthesisSourceReader(f.pool, f.artifacts)
	if err != nil {
		t.Fatal(err)
	}
	authoring, err := authoringpostgres.NewGORMRepository(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := changecontrolpostgres.NewGORMRepository(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	retirer, err := authoringchange.NewGeneratedPublicationRetirer(proposals)
	if err != nil {
		t.Fatal(err)
	}
	noteStore, err := organizingpostgres.NewGORMSynthesisStore(f.pool, organizingpostgres.SynthesisStoreDependencies{Authoring: authoring, Retirer: retirer, Sources: sources, Validated: f.store})
	if err != nil {
		t.Fatal(err)
	}
	targets := runtimeTargets{}
	proposalService, err := changecontrolapp.NewService(proposals, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targets, targets)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := authoringchange.NewProposalCreator(proposalService, targets)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := authoringapp.NewService(authoringapp.Dependencies{Repository: authoring, Proposals: creator, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	f.publisher = &runtimePublisher{SynthesisPublicationPublisher: publisher}
	f.notes, err = organizingapp.NewSynthesisService(organizingapp.SynthesisDependencies{Store: noteStore, Sources: sources, Publications: f.publisher, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "synthesis-runtime", ModelVersion: "v1"}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "synthesis-runtime", Version: "v1"}, Model: modelRef, Timeout: time.Second, MaxOutputTokens: 2048}
	catalog := agentapp.NewRuntimeCatalog()
	if err := organizingagent.RegisterSynthesisRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]agentapp.DeterministicChatStep, len(outputs))
	for i, output := range outputs {
		steps[i] = agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: []byte(output), Usage: agentdomain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}}}
	}
	f.provider = agentapp.NewDeterministicChatModel(steps...)
	f.modelStore = &runtimeModelStore{SynthesisModelStore: f.store}
	model, err := organizingagent.NewSynthesisModel(organizingagent.SynthesisModelDependencies{Model: f.provider, Scheduler: scheduler, Catalog: catalog, ModelRuns: f.modelRuns, Store: f.modelStore, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	f.model = &runtimeSwitchModel{ready: model, unavailable: organizingagent.NewUnavailableSynthesisModel()}
	executor, err := organizingworkflow.NewSynthesisExecutor(organizingworkflow.SynthesisExecutorDependencies{Runs: runs, Store: f.store, Candidates: f.notes, Sources: sources, Model: f.model, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	f.executor = executor
	validation, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapp.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range organizingworkflow.SynthesisExecutorNodeKinds() {
		if err := executors.Register(kind, 1, executor); err != nil {
			t.Fatal(err)
		}
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapp.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range organizingworkflow.SynthesisRegisteredDefinitions() {
		if err := definitions.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	resolved, err := definitions.Resolve(organizingworkflow.SynthesisDefinitionKey, 1)
	if err != nil || resolved.GraphHash != f.store.definitionHash {
		t.Fatalf("store fence differs from canonical registry: %v", err)
	}
	f.outbox, err = workflowpostgres.NewGORMSourceReadyOutbox(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatcher, err = organizingworkflow.NewSynthesisDispatcher(organizingworkflow.SynthesisDispatcherDependencies{UnitOfWork: f.uow, Outbox: f.outbox, Sources: sources, Processing: f.store, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	f.processing, err = organizingworkflow.NewSynthesisProcessingService(organizingworkflow.SynthesisProcessingServiceDependencies{UnitOfWork: f.uow, Queries: f.store, Retries: f.store, Applied: noteStore, Starter: runtime, Definitions: definitions, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	f.coordinator = coordinator
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "synthesis-integration", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(f.pool.DB(), workers)
	if err != nil {
		t.Fatal(err)
	}
	if startWorker {
		if err := client.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Stop(ctx); err != nil {
				t.Errorf("stop synthesis River worker: %v", err)
			}
		})
	}
	return f
}

func seedSynthesisRuntimeWorkspace(t *testing.T, database *gorm.DB, workspaceID foundation.ID) {
	t.Helper()
	root := t.TempDir()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := database.Exec(`INSERT INTO core.workspace(id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,status,availability,availability_reason,availability_checked_at,version,created_at,updated_at) VALUES(?,?,?, ?,1,?,?,'inactive','available',NULL,?,1,?,?)`, string(workspaceID), "synthesis runtime", root, runtimeHash(root), root, at, at, at, at).Error; err != nil {
		t.Fatal(err)
	}
}

func (f *synthesisRuntimeFixture) source(t *testing.T, content string) ingestiondomain.SourceReady {
	t.Helper()
	ids := []foundation.ID{runtimeID(), runtimeID(), runtimeID(), runtimeID(), runtimeID(), runtimeID()}
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	hash := runtimeHash(content)
	parserHash := runtimeHash("synthesis fixture parser")
	rows := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES(?,?,'file',?,?,?)`, []any{string(ids[0]), string(f.workspace), "source", string(ids[0]) + ".md", at}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES(?,?,?,?,?,?)`, []any{string(ids[2]), string(f.workspace), hash, len(content), ".knowledge/sources/" + hash, at}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES(?,?,?,?,?,?,'text/markdown',?,'passed',?)`, []any{string(ids[1]), string(ids[0]), string(f.workspace), string(ids[2]), hash, len(content), string(ids[0]) + ".md", at}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at) VALUES(?,?,?,'test-parser','v1',?,'v1',?,?)`, []any{string(ids[3]), string(f.workspace), string(ids[2]), parserHash, hash, at}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES(?,?,?,?)`, []any{string(ids[1]), string(ids[3]), string(f.workspace), at}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,excerpt_hash,evidence_kind,derived_excerpt,parser_version,schema_version,created_at) VALUES(?,?,?,?,'paragraph',1,1,0,?,?,'raw_bytes','','v1','v1',?)`, []any{string(ids[4]), string(f.workspace), string(ids[2]), string(ids[3]), len(content), hash, at}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at) VALUES(?,?,?,?,'chunked','passed','test-parser','v1',?,'v1','v1',?,1,?,?)`, []any{string(ids[5]), string(f.workspace), string(ids[1]), string(ids[3]), parserHash, string(ids[5]), at, at}},
	}
	for _, row := range rows {
		if err := f.database.WithContext(t.Context()).Exec(row.query, row.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.artifacts.mu.Lock()
	f.artifacts.values[ids[1]] = retrievalapp.EvidenceArtifact{WorkspaceID: f.workspace, SourceVersionID: ids[1], ContentArtifactID: ids[2], ContentHash: hash, ByteSize: int64(len(content)), Bytes: []byte(content)}
	f.artifacts.mu.Unlock()
	ready := ingestiondomain.SourceReady{WorkspaceID: f.workspace, SourceID: ids[0], SourceVersionID: ids[1], ContentArtifactID: ids[2], ParseProjectionID: ids[3], ContentHash: hash, IngestionAttemptID: ids[5], OccurredAt: at}
	f.append(t, ready)
	return ready
}

func (f *synthesisRuntimeFixture) append(t *testing.T, ready ingestiondomain.SourceReady) {
	t.Helper()
	if err := f.uow.Within(t.Context(), foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return f.outbox.AppendSourceReadyScoped(ctx, scope, ready)
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *synthesisRuntimeFixture) dispatch(t *testing.T, want int) {
	t.Helper()
	result, err := f.dispatcher.DispatchBatch(t.Context(), 10)
	if err != nil || result.Started != want {
		t.Fatalf("source dispatch=%+v err=%v", result, err)
	}
}

func (f *synthesisRuntimeFixture) wait(t *testing.T, source ingestiondomain.SourceReady, want organizingapp.SynthesisProcessingStatus) organizingapp.SynthesisProcessing {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var row processingModel
		err := f.database.WithContext(ctx).Where("workspace_id=? AND source_version_id=?", string(source.WorkspaceID), string(source.SourceVersionID)).Take(&row).Error
		if err != nil {
			t.Fatal(err)
		}
		processing, err := row.projection()
		if err != nil {
			t.Fatal(err)
		}
		if processing.Status == want {
			return processing
		}
		if processing.Status != organizingapp.SynthesisProcessingPending && processing.Status != organizingapp.SynthesisProcessingRunning {
			t.Fatalf("processing terminal=%s expected=%s failure=%+v", processing.Status, want, processing.Failure)
		}
		select {
		case <-ctx.Done():
			var nodes []struct {
				NodeKey      string
				Status       string
				ErrorCode    string
				ErrorSummary string
			}
			diagnostic := f.database.Raw(`SELECT node_key,status,COALESCE(error_code,'') AS error_code,COALESCE(error_summary,'') AS error_summary FROM workflow.node_run WHERE run_id=? ORDER BY node_key`, string(processing.WorkflowRunID)).Scan(&nodes).Error
			t.Fatalf("synthesis did not finish: %+v nodes=%+v diagnostic=%v", processing, nodes, diagnostic)
		case <-ticker.C:
		}
	}
}

func (f *synthesisRuntimeFixture) count(t *testing.T, table string, expected int64) {
	t.Helper()
	var count int64
	if err := f.database.Table(table).Where("workspace_id=?", string(f.workspace)).Count(&count).Error; err != nil || count != expected {
		t.Fatalf("%s count=%d expected=%d err=%v", table, count, expected, err)
	}
}

type runtimeArtifacts struct {
	mu     sync.Mutex
	values map[foundation.ID]retrievalapp.EvidenceArtifact
}

func (a *runtimeArtifacts) ReadEvidenceArtifact(_ context.Context, workspaceID, versionID foundation.ID) (retrievalapp.EvidenceArtifact, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	value, found := a.values[versionID]
	if !found || value.WorkspaceID != workspaceID {
		return retrievalapp.EvidenceArtifact{}, errors.New("fixture source unavailable")
	}
	value.Bytes = append([]byte(nil), value.Bytes...)
	return value, nil
}

type runtimeTargets struct{}

func (runtimeTargets) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	return "", errors.New("no approved files in this fixture")
}
func (runtimeTargets) EnsureTargetAbsent(context.Context, foundation.ID, string, string) error {
	return nil
}
func (runtimeTargets) CaptureApprovalSnapshot(context.Context, foundation.ID) (changecontroldomain.GitSnapshot, error) {
	return changecontroldomain.GitSnapshot{}, errors.New("fixture does not approve candidates")
}

type runtimeModelStore struct {
	organizingapp.SynthesisModelStore
	loseResponse atomic.Bool
	dropCommit   atomic.Bool
}

type runtimePublisher struct {
	organizingapp.SynthesisPublicationPublisher
	loseResponse atomic.Bool
}

func (publisher *runtimePublisher) PublishArticleRevision(ctx context.Context, command authoringapp.PublishCommand) (authoringapp.PublishResult, error) {
	result, err := publisher.SynthesisPublicationPublisher.PublishArticleRevision(ctx, command)
	if err == nil && publisher.loseResponse.Swap(false) {
		// Force terminal delivery so the test exercises the explicit new-Run
		// recovery branch, after the real Authoring/Proposal transaction commits.
		return authoringapp.PublishResult{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "SYNTHESIS_FIXTURE_RESPONSE_LOST", false, errors.New("publication committed but its response was lost"))
	}
	return result, err
}

func (s *runtimeModelStore) Complete(ctx context.Context, command organizingapp.CompleteSynthesisModelStepCommand) (organizingapp.SynthesisModelStepRecord, bool, error) {
	if s.dropCommit.Load() {
		return organizingapp.SynthesisModelStepRecord{}, false, errors.New("model output commit outcome is unknown")
	}
	record, replayed, err := s.SynthesisModelStore.Complete(ctx, command)
	if err == nil && s.loseResponse.Swap(false) {
		return organizingapp.SynthesisModelStepRecord{}, false, errors.New("accepted output committed but response was lost")
	}
	return record, replayed, err
}

type runtimeSwitchModel struct {
	ready, unavailable organizingworkflow.SynthesisExecutionModel
	disabled           atomic.Bool
}

func (m *runtimeSwitchModel) current() organizingworkflow.SynthesisExecutionModel {
	if m.disabled.Load() {
		return m.unavailable
	}
	return m.ready
}
func (m *runtimeSwitchModel) GenerateSynthesisForExecution(ctx context.Context, execution workflowapp.ExecutionContext, input organizingapp.SynthesisGenerationInput) (organizingapp.SynthesisGenerationResult, error) {
	return m.current().GenerateSynthesisForExecution(ctx, execution, input)
}
func (m *runtimeSwitchModel) ValidateSynthesisSemanticsForExecution(ctx context.Context, execution workflowapp.ExecutionContext, input organizingapp.SynthesisGenerationInput, result organizingapp.SynthesisGenerationResult) (organizingapp.SynthesisSemanticReceipt, error) {
	return m.current().ValidateSynthesisSemanticsForExecution(ctx, execution, input, result)
}

func runtimeID() foundation.ID {
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		panic(err)
	}
	return id
}
func runtimeHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
