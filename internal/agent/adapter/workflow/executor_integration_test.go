//go:build integration

package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentknowledge "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/knowledge"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentretrieval "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/retrieval"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformfilesystem "github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExecutorPersistsRealPostgresModelRunAndCallForWorkflowAttempt(t *testing.T) {
	platform, ctx := newAgentWorkflowIntegrationPool(t)
	pool := platform.DB()
	seedAgentWorkflowRuntime(t, ctx, pool, t.TempDir())
	repository, err := agentpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: relationDocument(t, testModelRunID, knowledgedomain.AssessmentNew),
		Usage: agentdomain.TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
	}})
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorDependencies{
		Model: model, Scheduler: newTrackingEinoStructuredScheduler(t), Catalog: catalog, Repository: repository, Knowledge: workflowKnowledgePort{}, Evidence: workflowEvidenceOpener{},
		IDs:   &workflowIDs{values: []foundation.ID{testModelRunID, testCallID, testCallID2, testCallID3}},
		Clock: &workflowClock{next: time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)}, Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(ctx, testExecution(t, validInput()))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) == 0 {
		t.Fatal("executor returned empty workflow output")
	}
	record, err := repository.GetModelRun(ctx, testWorkspaceID, testModelRunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Run.Status != agentdomain.ModelRunSucceeded || record.Run.WorkflowRunID != testWorkflowRunID ||
		record.Run.NodeRunID != testNodeRunID || record.Run.NodeAttemptID != testAttemptID ||
		record.Run.Retrieval.IndexVersionID != testIndexID || len(record.Calls) != 1 || record.Calls[0].Status != agentdomain.ModelCallSucceeded {
		t.Fatalf("record=%+v", record)
	}
	if record.Calls[0].RequestHash == "" || record.Calls[0].ResponseHash == "" || record.Calls[0].Usage.TotalTokens != 8 {
		t.Fatalf("call=%+v", record.Calls[0])
	}
	var leaked int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent.model_call AS call
		WHERE model_run_id=$1
		  AND to_jsonb(call)::text ~* '(candidate claim|server-opened|bounded ZHIXU Relation Assessment)'`, string(testModelRunID)).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("model call metadata leaked content markers: %d", leaked)
	}
}

func TestExecutorLoadsDisputedExistingClaimAndDisclosureThroughProductionAdapters(t *testing.T) {
	platform, ctx := newAgentWorkflowIntegrationPool(t)
	pool := platform.DB()
	workspaceRoot := t.TempDir()
	seedAgentWorkflowRuntime(t, ctx, pool, workspaceRoot)
	fixture := seedAgentRelationEvidence(t, ctx, pool, workspaceRoot)

	agentRepository, err := agentpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeRepository, err := knowledgepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	eligibility, err := knowledgeapplication.NewEvidenceEligibilityService(knowledgeRepository)
	if err != nil {
		t.Fatal(err)
	}
	formalClaims, err := knowledgeapplication.NewFormalClaimReader(knowledgeRepository)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeAdapter, err := agentknowledge.NewAdapter(eligibility, formalClaims)
	if err != nil {
		t.Fatal(err)
	}

	searchRepository, err := retrievalpostgres.NewGORMSearchRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRepository, err := workspacepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaceRepository, platformfilesystem.Scanner{
		Options: platformfilesystem.ScanOptions{MaxBytes: platformfilesystem.DefaultMaxBytes},
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceReference, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		t.Fatal(err)
	}
	retrievalAdapter, err := agentretrieval.NewAdapter(searchService, evidenceReference)
	if err != nil {
		t.Fatal(err)
	}

	disclosure := agentdomain.RelationConflictDisclosure{
		ClaimID: testExistingID, ConflictIDs: []foundation.ID{fixture.conflictID},
		Applicability: append(json.RawMessage(nil), fixture.applicability.CanonicalJSON...), UpdatedAt: fixture.existingUpdatedAt,
	}
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: relationConflictDocument(t, testModelRunID, disclosure),
		Usage: agentdomain.TokenUsage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
	}})
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorDependencies{
		Model: model, Scheduler: newTrackingEinoStructuredScheduler(t), Catalog: catalog, Repository: agentRepository, Knowledge: knowledgeAdapter, Evidence: retrievalAdapter,
		IDs:   &workflowIDs{values: []foundation.ID{testModelRunID, testCallID, testCallID2, testCallID3}},
		Clock: &workflowClock{next: time.Date(2026, 7, 19, 5, 0, 0, 0, time.UTC)}, Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(ctx, testExecution(t, validInputWithExisting()))
	if err != nil {
		t.Fatal(err)
	}
	var output RelationAssessmentWorkflowOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	assessment, err := agentdomain.DecodeRelationAssessment(output.BusinessJSON, agentdomain.DefaultDecodeLimits())
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Payload.Assessment != knowledgedomain.AssessmentConflict ||
		len(assessment.Payload.ConflictDisclosures) != 1 || assessment.Payload.ConflictDisclosures[0].ClaimID != testExistingID ||
		len(assessment.Payload.ConflictDisclosures[0].ConflictIDs) != 1 || assessment.Payload.ConflictDisclosures[0].ConflictIDs[0] != fixture.conflictID ||
		!assessment.Payload.ConflictDisclosures[0].UpdatedAt.Equal(fixture.existingUpdatedAt) ||
		output.Action.Decision != knowledgedomain.AssessmentDecisionOpenConflict || !output.Action.OpenConflict || output.Action.RelationType != nil {
		t.Fatalf("assessment=%+v action=%+v", assessment.Payload, output.Action)
	}
	calls := model.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls=%d", len(calls))
	}
	modelInput := calls[0].Messages[len(calls[0].Messages)-1].Content
	for _, required := range []string{
		`"statement":"existing claim"`, `"eligibility":"ELIGIBLE_WITH_CONFLICT"`, string(fixture.conflictID),
		fixture.candidateExcerpt, fixture.existingExcerpt,
	} {
		if !strings.Contains(modelInput, required) {
			t.Fatalf("server model input lacks %q: %s", required, modelInput)
		}
	}
	if strings.Contains(modelInput, fixture.workspaceRoot) || strings.Contains(string(result.Output), fixture.workspaceRoot) {
		t.Fatal("workspace root leaked through model or workflow output")
	}
}

func TestRecordingReviewPersistsPostgresCallMetricsWithoutSensitiveBodies(t *testing.T) {
	platform, ctx := newAgentWorkflowIntegrationPool(t)
	pool := platform.DB()
	seedAgentWorkflowRuntime(t, ctx, pool, t.TempDir())
	repository, err := agentpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 19, 6, 0, 0, 0, time.UTC)
	run := agentdomain.ModelRun{
		ID: testModelRunID, WorkspaceID: testWorkspaceID, WorkflowRunID: testWorkflowRunID,
		NodeRunID: testNodeRunID, NodeAttemptID: testAttemptID, Model: testModelRef(),
		Profile: DefaultProfileRef(), Prompt: DefaultPromptRef(),
		Schema:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		ReducedSchema: agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		Retrieval:     agentdomain.RetrievalRef{IndexVersionID: testIndexID}, Status: agentdomain.ModelRunRunning,
		Version: 1, CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	if _, replayed, err := repository.CreateModelRun(ctx, run); err != nil || replayed {
		t.Fatalf("create run replayed=%t err=%v", replayed, err)
	}

	ids := &workflowIDs{values: []foundation.ID{testCallID, testCallID2}}
	clock := &workflowClock{next: startedAt.Add(time.Second)}
	initialProvider := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: []byte(`{"accepted":true}`),
		Usage: agentdomain.TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3},
	}})
	initialRecorder, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: initialProvider, Repository: repository, WorkspaceID: testWorkspaceID, ModelRunID: testModelRunID,
		IDs: ids, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initialRecorder.Chat(ctx, agentapplication.ChatRequest{
		Phase: agentdomain.ModelCallInitial, ProfileRef: DefaultProfileRef(), PromptRef: DefaultPromptRef(),
		SchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		Model:     testModelRef(), Messages: []agentapplication.ChatMessage{
			{Role: agentapplication.MessageRoleSystem, Content: "bounded generation"},
			{Role: agentapplication.MessageRoleUser, Content: "generate structured output"},
		},
		OutputSchema: []byte(`{"type":"object"}`), MaxOutputTokens: 32,
	}); err != nil {
		t.Fatal(err)
	}

	const promptCanary = "PROMPT_CANARY_REVIEW_BODY"
	const sourceCanary = "SOURCE_CANARY_REVIEW_EVIDENCE"
	const rawCanary = "RAW_RESPONSE_CANARY_REVIEW"
	answer := reviewAnswer(sourceCanary)
	review := agentdomain.FaithfulnessReviewResult{
		ResultType: agentdomain.ResultTypeFaithfulnessReview, SchemaID: agentdomain.FaithfulnessReviewSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: agentdomain.FaithfulnessReviewPayload{Passed: true, Summary: rawCanary, Items: []agentdomain.FaithfulnessReviewItem{
			{AssertionID: "assertion-1", Verdict: agentdomain.FaithfulnessSupported, CitationIDs: []string{"review-cite-1"}, Reason: rawCanary},
			{AssertionID: "@answer/conclusion", Verdict: agentdomain.FaithfulnessSupported, CitationIDs: []string{"review-cite-1"}, Reason: rawCanary},
		}},
	}
	reviewRaw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	reviewProvider := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: reviewRaw,
		Usage: agentdomain.TokenUsage{InputTokens: 13, OutputTokens: 8, TotalTokens: 21},
	}})
	reviewRecorder, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: reviewProvider, Repository: repository, WorkspaceID: testWorkspaceID, ModelRunID: testModelRunID,
		StartingCallNo: 2, IDs: ids, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewCatalog := agentapplication.NewRuntimeCatalog()
	reviewPrompt := agentapplication.PromptDefinition{
		Ref:    agentdomain.PromptRef{ID: "faithfulness-review", Version: "v1"},
		System: "review policy " + promptCanary, InitialInstruction: "review assertions " + promptCanary,
		RepairInstruction: "unused repair", ReducedInstruction: "unused reduced",
	}
	reviewSchema := agentapplication.SchemaDefinition{
		Ref:        agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		JSONSchema: []byte(`{"type":"object"}`), Decode: func(raw []byte) (json.RawMessage, error) {
			if _, err := agentdomain.DecodeFaithfulnessReview(raw, agentdomain.DefaultDecodeLimits()); err != nil {
				return nil, err
			}
			return append(json.RawMessage(nil), raw...), nil
		},
	}
	reviewProfile := agentapplication.ModelProfile{
		Ref: DefaultProfileRef(), Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128,
	}
	if err := reviewCatalog.RegisterPrompt(reviewPrompt); err != nil {
		t.Fatal(err)
	}
	if err := reviewCatalog.RegisterSchema(reviewSchema); err != nil {
		t.Fatal(err)
	}
	if err := reviewCatalog.RegisterProfile(reviewProfile); err != nil {
		t.Fatal(err)
	}
	if err := reviewCatalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	reviewer, err := agentapplication.NewStructuredFaithfulnessReviewer(reviewRecorder, reviewCatalog, agentapplication.DefaultRunBudget())
	if err != nil {
		t.Fatal(err)
	}
	reviewResult, err := reviewer.Review(ctx, agentapplication.FaithfulnessReviewRequest{
		ProfileRef: reviewProfile.Ref, PromptRef: reviewPrompt.Ref, SchemaRef: reviewSchema.Ref,
		Answer: answer, Evidence: []agentdomain.Evidence{{
			Citation: answer.Payload.Citations[0], Excerpt: sourceCanary, Eligibility: knowledgedomain.EvidenceEligible,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reviewResult.Review.Payload.Passed {
		t.Fatalf("review=%+v", reviewResult.Review)
	}
	record, err := repository.GetModelRun(ctx, testWorkspaceID, testModelRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Calls) != 2 {
		t.Fatalf("calls=%+v", record.Calls)
	}
	reviewRequests := reviewProvider.Calls()
	if len(reviewRequests) != 1 || reviewRequests[0].Phase != agentdomain.ModelCallReview {
		t.Fatalf("review provider requests=%+v", reviewRequests)
	}
	requestDocument, err := json.Marshal(reviewRequests[0])
	if err != nil {
		t.Fatal(err)
	}
	reviewCall := record.Calls[1]
	if reviewCall.CallNo != 2 || reviewCall.Phase != agentdomain.ModelCallReview || reviewCall.Status != agentdomain.ModelCallSucceeded ||
		reviewCall.Model != testModelRef() || reviewCall.Profile != reviewProfile.Ref || reviewCall.Prompt != reviewPrompt.Ref ||
		reviewCall.Schema != reviewSchema.Ref || reviewCall.MaxOutputTokens != reviewProfile.MaxOutputTokens ||
		reviewCall.RequestHash != testSHA256(requestDocument) || reviewCall.ResponseHash != testSHA256(reviewRaw) || reviewCall.RequestBytes != int64(len(requestDocument)) ||
		reviewCall.ResponseBytes != int64(len(reviewRaw)) || reviewCall.Usage != (agentdomain.TokenUsage{InputTokens: 13, OutputTokens: 8, TotalTokens: 21}) ||
		reviewCall.LatencyMillis != 1 || reviewCall.ErrorCode != "" || reviewCall.Version != 2 || reviewCall.CompletedAt == nil {
		t.Fatalf("review call=%+v", reviewCall)
	}
	var persistedJSON string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(call)::text FROM agent.model_call AS call WHERE model_run_id=$1 AND call_no=2`, string(testModelRunID)).Scan(&persistedJSON); err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{promptCanary, sourceCanary, rawCanary} {
		if strings.Contains(persistedJSON, canary) {
			t.Fatalf("model call metadata leaked %q: %s", canary, persistedJSON)
		}
	}
}

type agentRelationEvidenceFixture struct {
	workspaceRoot     string
	candidateExcerpt  string
	existingExcerpt   string
	conflictID        foundation.ID
	applicability     knowledgedomain.Applicability
	existingUpdatedAt time.Time
}

type agentCitationSeed struct {
	sourceID, artifactID, sourceVersionID foundation.ID
	projectionID, spanID, chunkID         foundation.ID
	relativePath, content                 string
	indexed                               bool
}

func seedAgentRelationEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceRoot string) agentRelationEvidenceFixture {
	t.Helper()
	now := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	candidate := agentCitationSeed{
		sourceID: "82000000-0000-4000-8000-000000000020", artifactID: "82000000-0000-4000-8000-000000000021",
		sourceVersionID: testSourceID, projectionID: "82000000-0000-4000-8000-000000000022",
		spanID: testSpanID, chunkID: testChunkID, relativePath: "docs/candidate.md", content: "candidate approved evidence", indexed: true,
	}
	existing := agentCitationSeed{
		sourceID: "82000000-0000-4000-8000-000000000023", artifactID: "82000000-0000-4000-8000-000000000024",
		sourceVersionID: testSourceID2, projectionID: "82000000-0000-4000-8000-000000000025",
		spanID: testSpanID2, chunkID: testChunkID2, relativePath: "docs/existing.md", content: "existing disputed evidence", indexed: true,
	}
	other := agentCitationSeed{
		sourceID: "82000000-0000-4000-8000-000000000030", artifactID: "82000000-0000-4000-8000-000000000031",
		sourceVersionID: "82000000-0000-4000-8000-000000000032", projectionID: "82000000-0000-4000-8000-000000000033",
		spanID: "82000000-0000-4000-8000-000000000034", relativePath: "docs/other.md", content: "other disputed evidence",
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, seed := range []agentCitationSeed{candidate, existing, other} {
		seedAgentCitationEvidence(t, ctx, tx, workspaceRoot, seed, now)
	}
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	otherClaimID := foundation.ID("82000000-0000-4000-8000-000000000028")
	claims := []knowledgedomain.Claim{
		agentClaim(t, testCandidateID, "candidate claim", applicability, now),
		agentClaim(t, testExistingID, "existing claim", applicability, now),
		agentClaim(t, otherClaimID, "other conflicting claim", applicability, now),
	}
	for _, claim := range claims {
		if _, err := tx.Exec(ctx, `INSERT INTO core.claim(
			id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
			applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,'SUGGESTED','{}',$8,1,$9,$9)`,
			string(claim.ID), string(claim.WorkspaceID), claim.Statement, claim.NormalizedStatement,
			claim.Applicability.CanonicalJSON, claim.Applicability.SchemaVersion, claim.Applicability.Hash, claim.Fingerprint, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	sources := []knowledgedomain.ClaimSource{
		agentClaimSource("82000000-0000-4000-8000-000000000026", testCandidateID, candidate, applicability, now.Add(10*time.Second)),
		agentClaimSource("82000000-0000-4000-8000-000000000027", testExistingID, existing, applicability, now.Add(10*time.Second)),
		agentClaimSource("82000000-0000-4000-8000-000000000035", otherClaimID, other, applicability, now.Add(10*time.Second)),
	}
	for _, source := range sources {
		if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(
			id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(source.ID), string(source.WorkspaceID), string(source.ClaimID),
			string(source.Provenance.SourceVersionID), string(source.Provenance.SourceSpanID), string(source.SupportType), source.Reason, source.EvidenceHash, source.CreatedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
	confirmedAt := now.Add(time.Minute)
	for _, claimID := range []foundation.ID{testCandidateID, testExistingID, otherClaimID} {
		if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$1 WHERE workspace_id=$2 AND id=$3`, confirmedAt, string(testWorkspaceID), string(claimID)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	conflictTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conflictTx.Rollback(ctx) }()
	disputedAt := now.Add(2 * time.Minute)
	for _, claimID := range []foundation.ID{testExistingID, otherClaimID} {
		if _, err := conflictTx.Exec(ctx, `UPDATE core.claim SET status='DISPUTED',version=3,updated_at=$1 WHERE workspace_id=$2 AND id=$3`, disputedAt, string(testWorkspaceID), string(claimID)); err != nil {
			t.Fatal(err)
		}
	}
	conflictID := foundation.ID("82000000-0000-4000-8000-000000000029")
	members := []knowledgedomain.ConflictMember{
		{ConflictID: conflictID, WorkspaceID: testWorkspaceID, ClaimID: testExistingID, Applicability: applicability, ApplicabilityHash: applicability.Hash, PositionSummary: "existing disputed position", CreatedAt: now.Add(3 * time.Minute)},
		{ConflictID: conflictID, WorkspaceID: testWorkspaceID, ClaimID: otherClaimID, Applicability: applicability, ApplicabilityHash: applicability.Hash, PositionSummary: "other disputed position", CreatedAt: now.Add(3 * time.Minute)},
	}
	conflictFingerprint := knowledgedomain.ComputeConflictFingerprint(testWorkspaceID, knowledgedomain.ApplicabilityAssessmentExact, members)
	if _, err := conflictTx.Exec(ctx, `INSERT INTO core.conflict(
		id,workspace_id,status,severity,summary,applicability_assessment,applicability_hash,fingerprint,version,created_at,updated_at
	) VALUES($1,$2,'OPEN','HIGH','formal disputed conflict','EXACT',$3,$4,1,$5,$5)`,
		string(conflictID), string(testWorkspaceID), applicability.Hash, conflictFingerprint, now.Add(3*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if _, err := conflictTx.Exec(ctx, `INSERT INTO core.conflict_member(
			conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,applicability_hash,position_summary,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, string(member.ConflictID), string(member.ClaimID), string(member.WorkspaceID),
			member.Applicability.CanonicalJSON, member.Applicability.SchemaVersion, member.ApplicabilityHash, member.PositionSummary, member.CreatedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := conflictTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return agentRelationEvidenceFixture{
		workspaceRoot: workspaceRoot, candidateExcerpt: candidate.content, existingExcerpt: existing.content,
		conflictID: conflictID, applicability: applicability, existingUpdatedAt: disputedAt,
	}
}

func seedAgentCitationEvidence(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	workspaceRoot string,
	seed agentCitationSeed,
	at time.Time,
) {
	t.Helper()
	content := []byte(seed.content)
	contentHash := testSHA256(content)
	managedLocation := ".knowledge/sources/" + contentHash
	managedPath := filepath.Join(workspaceRoot, filepath.FromSlash(managedLocation))
	if err := os.MkdirAll(filepath.Dir(managedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		  VALUES($1,$2,'file',$3,$4,$5)`, []any{string(seed.sourceID), string(testWorkspaceID), seed.relativePath, seed.relativePath, at}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		  VALUES($1,$2,$3,$4,$5,$6)`, []any{string(seed.artifactID), string(testWorkspaceID), contentHash, int64(len(content)), managedLocation, at}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id)
		  VALUES($1,$2,$3,$4,$5,'text/markdown',$6,'passed',$7,$8)`, []any{string(seed.sourceVersionID), string(seed.sourceID), string(testWorkspaceID), contentHash, int64(len(content)), seed.relativePath, at, string(seed.artifactID)}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at)
		  VALUES($1,$2,$3,'text','text-v1',$4,'schema-v1',$5,$6)`, []any{string(seed.projectionID), string(testWorkspaceID), string(seed.artifactID), testSHA256([]byte(seed.relativePath)), contentHash, at}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		  VALUES($1,$2,$3,$4)`, []any{string(seed.sourceVersionID), string(seed.projectionID), string(testWorkspaceID), at}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
		  VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{}',$6,'text-v1','schema-v1',$7)`, []any{string(seed.spanID), string(testWorkspaceID), string(seed.artifactID), string(seed.projectionID), int64(len(content)), contentHash, at}},
	}
	if seed.indexed {
		statements = append(statements,
			struct {
				sql  string
				args []any
			}{`INSERT INTO ingestion.canonical_chunk(
				id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,
				parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
			) VALUES($1,$2,$3,0,'[]',$4,$5,$6,$7,$7,'text-v1','chunk-v1','schema-v1',false,'active',$8)`,
				[]any{string(seed.chunkID), string(testWorkspaceID), string(seed.projectionID), seed.content, contentHash, string(seed.spanID), int64(len(content)), at}},
			struct {
				sql  string
				args []any
			}{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
			  VALUES($1,$2,$3,$4,0,'text-v1','chunk-v1','schema-v1',$5)`, []any{string(testIndexID), string(seed.chunkID), string(testWorkspaceID), contentHash, at}},
			struct {
				sql  string
				args []any
			}{`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at)
			  VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{string(testIndexID), string(testWorkspaceID), string(seed.sourceID), string(seed.sourceVersionID), string(seed.projectionID), at}},
		)
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func agentClaim(t *testing.T, id foundation.ID, statement string, applicability knowledgedomain.Applicability, at time.Time) knowledgedomain.Claim {
	t.Helper()
	display, normalized, err := knowledgedomain.NormalizeStatement(statement)
	if err != nil {
		t.Fatal(err)
	}
	return knowledgedomain.Claim{
		ID: id, WorkspaceID: testWorkspaceID, Statement: display, NormalizedStatement: normalized,
		Applicability: applicability, Fingerprint: knowledgedomain.ComputeClaimFingerprint(testWorkspaceID, normalized, applicability),
		Status: knowledgedomain.ClaimStatusSuggested, ConfidenceFactors: json.RawMessage(`{}`), Version: 1,
		CreatedAt: at, UpdatedAt: at,
	}
}

func agentClaimSource(id, claimID foundation.ID, seed agentCitationSeed, applicability knowledgedomain.Applicability, at time.Time) knowledgedomain.ClaimSource {
	source := knowledgedomain.ClaimSource{
		ID: id, WorkspaceID: testWorkspaceID, ClaimID: claimID,
		Provenance:  knowledgedomain.ProvenanceRef{WorkspaceID: testWorkspaceID, SourceVersionID: seed.sourceVersionID, SourceSpanID: seed.spanID},
		SupportType: knowledgedomain.ClaimSupportSupports, Reason: "formal supporting source", CreatedAt: at,
	}
	source.EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(source, applicability)
	return source
}

func relationConflictDocument(t *testing.T, runID foundation.ID, disclosure agentdomain.RelationConflictDisclosure) []byte {
	t.Helper()
	document, err := json.Marshal(agentdomain.RelationAssessmentResult{
		ResultType: agentdomain.ResultTypeRelationAssessment, SchemaID: agentdomain.RelationAssessmentSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: runID,
		Payload: agentdomain.RelationAssessmentPayload{
			Assessment: knowledgedomain.AssessmentConflict, CandidateEvidenceRefs: []string{"candidate-1"},
			ExistingEvidenceRefs: []string{"existing-1"}, ConflictDisclosures: []agentdomain.RelationConflictDisclosure{disclosure},
			Applicability: agentdomain.ApplicabilityComparison{Candidate: json.RawMessage(`{}`), Existing: json.RawMessage(`{}`), Summary: "same applicability"},
			Reason:        "formal sources conflict", ConfidenceFactors: []string{"both sides are formally bound"}, UncertaintyReasons: []string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func reviewAnswer(sourceCanary string) agentdomain.RAGAnswerResult {
	citation := testCitation("review-cite-1", testChunkID, testSourceID, testSpanID)
	return agentdomain.RAGAnswerResult{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: agentdomain.RAGAnswerPayload{
			Conclusion: "reviewed conclusion " + sourceCanary,
			Assertions: []agentdomain.Assertion{{
				ID: "assertion-1", Text: "reviewed fact " + sourceCanary, Kind: agentdomain.AssertionFactual, CitationIDs: []string{citation.ID},
			}},
			Citations: []agentdomain.Citation{citation}, ConflictPositions: []agentdomain.ConflictPosition{},
		},
	}
}

func testSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func seedAgentWorkflowRuntime(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceRoot string) {
	t.Helper()
	definitionID := foundation.ID("82000000-0000-4000-8000-000000000030")
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
		 VALUES($1,'agent-workflow',$2,$2,now(),'active',now(),now())`, []any{string(testWorkspaceID), workspaceRoot}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		 VALUES($1,$2,'agent-relation-assessment',1,'{"nodes":[]}',now())`, []any{string(definitionID), string(testWorkspaceID)}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		 VALUES($1,$2,$3,'running','{}',1,now(),now())`, []any{string(testWorkflowRunID), string(testWorkspaceID), string(definitionID)}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
		 VALUES($1,$2,'relation-assessment',$3,'running','{}','worker',now()+interval '5 minutes',1,now(),now())`,
			[]any{string(testNodeRunID), string(testWorkflowRunID), RelationAssessmentNodeKind}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
		 VALUES($1,$2,1,1,0,'agent-relation-attempt','worker',now()+interval '5 minutes','running',now())`, []any{string(testAttemptID), string(testNodeRunID)}},
		{`INSERT INTO retrieval.index_version(
		 id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,
		 manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at)
		 VALUES($1,$2,'simple','v1',repeat('1',64),'{}','agent-workflow:index',repeat('2',64),0,
		 'agent-workflow-index','building','["vector"]',1,now(),now())`, []any{string(testIndexID), string(testWorkspaceID)}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func newAgentWorkflowIntegrationPool(t *testing.T) (*platformpostgres.Pool, context.Context) {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	return fixture.Pool(), t.Context()
}

var _ workflowapplication.Executor = (*Executor)(nil)
