//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// composeRAGKnowledgeFixture identifies only the formal Knowledge facts that
// have no public HTTP seed API. Retrieval provenance remains owned by the
// smoke setup; Conversation, Answer, Workflow and Event facts are never seeded.
type composeRAGKnowledgeFixture struct {
	WorkspaceID, SourceVersionID, SourceSpanID foundation.ID
	ClaimID, TopicID, RelationID               foundation.ID
}

func TestComposeRAGKnowledgeSeedCreatesOnlyEligibilityFacts(t *testing.T) {
	databaseURL := testDatabaseURL(t)
	pool := newMigratedWorkerTestPool(t, databaseURL)
	ctx := context.Background()
	fixture := seedComposeRAGProvenance(t, ctx, pool)
	seeded := seedComposeRAGKnowledge(t, ctx, pool, fixture.WorkspaceID, fixture.SourceVersionID, fixture.SourceSpanID)

	repository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	ref := knowledgedomain.ProvenanceRef{WorkspaceID: seeded.WorkspaceID, SourceVersionID: seeded.SourceVersionID, SourceSpanID: seeded.SourceSpanID}
	eligibility, err := repository.BatchCheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{WorkspaceID: seeded.WorkspaceID, Provenance: []knowledgedomain.ProvenanceRef{ref}})
	if err != nil || len(eligibility) != 1 || eligibility[0].Eligibility != knowledgedomain.EvidenceEligible {
		t.Fatalf("eligibility=%#v err=%v", eligibility, err)
	}
	topics, err := repository.ResolveEvidenceTopics(ctx, seeded.WorkspaceID, []knowledgedomain.ProvenanceRef{ref})
	if err != nil || len(topics) != 1 || topics[0].TopicID != seeded.TopicID {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	for _, table := range []string{"agent.conversation", "agent.question", "agent.answer", "workflow.run"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("seed created forbidden facts in %s: count=%d err=%v", table, count, err)
		}
	}
}

// TestComposeRAGKnowledgeSeedExternalFixture qualifies public Retrieval
// provenance without seeding Conversation, Question, Answer or Workflow facts.
func TestComposeRAGKnowledgeSeedExternalFixture(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_RAG_FIXTURE_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_RAG_FIXTURE_DATABASE_URL for the external Compose fixture")
	}
	workspaceID, err := foundation.ParseID(os.Getenv("ZHIXU_RAG_FIXTURE_WORKSPACE_ID"))
	if err != nil {
		t.Fatal(err)
	}
	sourceVersionID, err := foundation.ParseID(os.Getenv("ZHIXU_RAG_FIXTURE_SOURCE_VERSION_ID"))
	if err != nil {
		t.Fatal(err)
	}
	sourceSpanID, err := foundation.ParseID(os.Getenv("ZHIXU_RAG_FIXTURE_SOURCE_SPAN_ID"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var workflowCountBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run`).Scan(&workflowCountBefore); err != nil {
		t.Fatal(err)
	}
	seeded := seedComposeRAGKnowledge(t, ctx, pool, workspaceID, sourceVersionID, sourceSpanID)
	repository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	ref := knowledgedomain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: sourceSpanID}
	eligibility, err := repository.BatchCheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{WorkspaceID: workspaceID, Provenance: []knowledgedomain.ProvenanceRef{ref}})
	if err != nil || len(eligibility) != 1 || eligibility[0].Eligibility != knowledgedomain.EvidenceEligible {
		t.Fatalf("eligibility=%#v err=%v", eligibility, err)
	}
	topics, err := repository.ResolveEvidenceTopics(ctx, workspaceID, []knowledgedomain.ProvenanceRef{ref})
	if err != nil || len(topics) != 1 || topics[0].TopicID != seeded.TopicID {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	for _, table := range []string{"agent.conversation", "agent.question", "agent.answer"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("external seed created forbidden facts in %s: count=%d err=%v", table, count, err)
		}
	}
	var workflowCountAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run`).Scan(&workflowCountAfter); err != nil || workflowCountAfter != workflowCountBefore {
		t.Fatalf("external seed changed Workflow facts: before=%d after=%d err=%v", workflowCountBefore, workflowCountAfter, err)
	}
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	value := getenv("ZHIXU_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	return value
}

func seedComposeRAGKnowledge(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sourceVersionID, sourceSpanID foundation.ID) composeRAGKnowledgeFixture {
	t.Helper()
	repository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	fixture := composeRAGKnowledgeFixture{
		WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: sourceSpanID,
		ClaimID: ragSmokeClaimID, TopicID: ragSmokeTopicID, RelationID: ragSmokeRelationID,
	}
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{"scope":"compose-rag-smoke"}`))
	if err != nil {
		t.Fatal(err)
	}
	name, normalizedName, err := knowledgedomain.NormalizeTopicText("RAG Smoke Evidence")
	if err != nil {
		t.Fatal(err)
	}
	topicResult, err := repository.CreateTopic(ctx, knowledgedomain.CreateTopicRecord{
		Topic:          knowledgedomain.Topic{ID: fixture.TopicID, WorkspaceID: workspaceID, Name: name, NormalizedName: normalizedName, Description: "Deterministic Compose RAG smoke evidence", Aliases: []knowledgedomain.TopicAlias{}, Status: knowledgedomain.TopicStatusActive, Version: 1, CreatedAt: at, UpdatedAt: at},
		IdempotencyKey: "compose-rag-smoke:topic", RequestHash: composeRAGHash("topic"),
	})
	if err != nil {
		t.Fatal(err)
	}
	statement, normalizedStatement, err := knowledgedomain.NormalizeStatement("Approved recovery requires durable replay without duplicate provider work.")
	if err != nil {
		t.Fatal(err)
	}
	factors, err := knowledgedomain.NormalizeConfidenceFactors(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claim := knowledgedomain.Claim{ID: fixture.ClaimID, WorkspaceID: workspaceID, Statement: statement, NormalizedStatement: normalizedStatement, Applicability: applicability, Status: knowledgedomain.ClaimStatusSuggested, ConfidenceFactors: factors, Version: 1, CreatedAt: at, UpdatedAt: at}
	claim.Fingerprint = knowledgedomain.ComputeClaimFingerprint(workspaceID, normalizedStatement, applicability)
	claimResult, err := repository.SuggestClaim(ctx, knowledgedomain.SuggestClaimRecord{Claim: claim, IdempotencyKey: "compose-rag-smoke:claim:suggest", RequestHash: composeRAGHash("claim-suggest")})
	if err != nil {
		t.Fatal(err)
	}
	source := knowledgedomain.ClaimSource{ID: "a1000000-0000-4000-8000-000000000004", WorkspaceID: workspaceID, ClaimID: claimResult.Claim.ID, Provenance: knowledgedomain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: sourceSpanID}, SupportType: knowledgedomain.ClaimSupportSupports, Reason: "The immutable smoke source directly supports the claim", CreatedAt: at.Add(time.Second)}
	source.EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(source, applicability)
	confirmedClaim, err := repository.ConfirmClaim(ctx, knowledgedomain.ConfirmClaimRecord{WorkspaceID: workspaceID, ClaimID: claimResult.Claim.ID, ExpectedVersion: claimResult.Claim.Version, Source: source, IdempotencyKey: "compose-rag-smoke:claim:confirm", RequestHash: composeRAGHash("claim-confirm"), At: at.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	relationSource, relationTarget, err := knowledgedomain.CanonicalizeRelationEndpoints(knowledgedomain.RelationBelongsTo, knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: confirmedClaim.Claim.ID}, knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeTopic, ID: topicResult.Topic.ID})
	if err != nil {
		t.Fatal(err)
	}
	relation := knowledgedomain.Relation{ID: fixture.RelationID, WorkspaceID: workspaceID, Source: relationSource, Target: relationTarget, Type: knowledgedomain.RelationBelongsTo, Status: knowledgedomain.RelationStatusSuggested, Version: 1, CreatedAt: at.Add(2 * time.Second), UpdatedAt: at.Add(2 * time.Second)}
	relation.Fingerprint = knowledgedomain.ComputeRelationFingerprint(workspaceID, relation.Type, relation.Source, relation.Target)
	initial := composeRAGRelationEvidence(fixture, applicability, "Topic binding candidate", at.Add(2*time.Second), "a1000000-0000-4000-8000-000000000005")
	if _, err := repository.SuggestRelation(ctx, knowledgedomain.SuggestRelationRecord{Relation: relation, Evidence: []knowledgedomain.RelationEvidence{initial}, IdempotencyKey: "compose-rag-smoke:relation:suggest", RequestHash: composeRAGHash("relation-suggest")}); err != nil {
		t.Fatal(err)
	}
	confirmedEvidence := composeRAGRelationEvidence(fixture, applicability, "Source-derived topic binding confirmation", at.Add(3*time.Second), "a1000000-0000-4000-8000-000000000006")
	confirmation := knowledgedomain.Confirmation{Method: knowledgedomain.ConfirmationSourceDerived, Reference: "source:compose-rag-smoke"}
	confirmedEvidence.Confirmation = &confirmation
	confirmedEvidence.EvidenceHash = knowledgedomain.ComputeRelationEvidenceHash(confirmedEvidence)
	if _, err := repository.ConfirmRelation(ctx, knowledgedomain.ConfirmRelationRecord{WorkspaceID: workspaceID, RelationID: fixture.RelationID, ExpectedVersion: 1, Evidence: confirmedEvidence, Confirmation: confirmation, IdempotencyKey: "compose-rag-smoke:relation:confirm", RequestHash: composeRAGHash("relation-confirm"), At: at.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// seedRAGConversationKnowledge creates the real Retrieval provenance and only
// the missing formal-Knowledge qualification. Public APIs still own every
// Conversation, Question, Answer, Workflow, Event and Feedback fact.
func seedRAGConversationKnowledge(t *testing.T, ctx context.Context, pool *pgxpool.Pool, root string) {
	t.Helper()
	content := []byte("Approved recovery replays durable facts without duplicating provider work.")
	digest := sha256.Sum256(content)
	contentHash := hex.EncodeToString(digest[:])
	at := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	managedLocation := filepath.Join(".knowledge", "sources", contentHash)
	artifactPath := filepath.Join(root, managedLocation)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	parserHash, normalizedHash := composeRAGHash("rag-parser"), composeRAGHash("rag-normalized")
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'rag-conversation-smoke',$2,$2,$3,'test',1,$3,$3)`, []any{string(ragSmokeWorkspaceID), root, at}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,$4,$5,$6)`, []any{string(ragSmokeArtifactID), string(ragSmokeWorkspaceID), contentHash, int64(len(content)), managedLocation, at}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text','Recovery','docs/recovery.txt',$3)`, []any{string(ragSmokeSourceID), string(ragSmokeWorkspaceID), at}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,$6,'text/plain','docs/recovery.txt','passed',$7)`, []any{string(ragSmokeSourceVersionID), string(ragSmokeSourceID), string(ragSmokeWorkspaceID), string(ragSmokeArtifactID), contentHash, int64(len(content)), at}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{string(ragSmokeProjectionID), string(ragSmokeWorkspaceID), string(ragSmokeArtifactID), parserHash, normalizedHash, at}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(ragSmokeSourceVersionID), string(ragSmokeProjectionID), string(ragSmokeWorkspaceID), at}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,failure_stage,error_code,retryable,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at,version) VALUES('a1400000-0000-4000-8000-000000000012',$1,$2,$3,'chunked','passed','','',false,'text','v1',$4,'structure-v1','v1','rag-smoke-ingestion',1,$5,$5,1)`, []any{string(ragSmokeWorkspaceID), string(ragSmokeSourceVersionID), string(ragSmokeProjectionID), parserHash, at}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind":"paragraph"}'::jsonb,$6,'v1','v1',$7)`, []any{string(ragSmokeSpanID), string(ragSmokeWorkspaceID), string(ragSmokeArtifactID), string(ragSmokeProjectionID), int64(len(content)), contentHash, at}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at) VALUES($1,$2,$3,0,'["Recovery"]',$4,$5,$6,$7,$7,'v1','structure-v1','v1',false,'active',$8)`, []any{string(ragSmokeChunkID), string(ragSmokeWorkspaceID), string(ragSmokeProjectionID), string(content), contentHash, string(ragSmokeSpanID), int64(len(content)), at}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version) VALUES($1,$2,'postgres-simple','v1',$3,'{}','rag-compose-smoke',$4,1,'rag-compose-smoke','building','["vector"]',1,$5,$5,$6,1,'text','v1',$3,'structure-v1','v1')`, []any{string(ragSmokeIndexID), string(ragSmokeWorkspaceID), parserHash, composeRAGHash("rag-manifest"), at, composeRAGHash("rag-source-manifest")}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at) VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`, []any{string(ragSmokeIndexID), string(ragSmokeChunkID), string(ragSmokeWorkspaceID), contentHash, at}},
		{`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{string(ragSmokeIndexID), string(ragSmokeWorkspaceID), string(ragSmokeSourceID), string(ragSmokeSourceVersionID), string(ragSmokeProjectionID), at}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	retrievalRepository, err := retrievalpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retrievalRepository.BuildLexical(ctx, retrievaldomain.LexicalBuildCommand{WorkspaceID: ragSmokeWorkspaceID, IndexVersionID: ragSmokeIndexID, ExpectedIndexVersion: 1, At: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	service, err := retrievalapplication.NewService(retrievalapplication.Dependencies{Store: retrievalRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: at.Add(2 * time.Second)}})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := service.Ready(ctx, retrievalapplication.TransitionRequest{WorkspaceID: ragSmokeWorkspaceID, IndexVersionID: ragSmokeIndexID, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx, retrievalapplication.ActivateRequest{WorkspaceID: ragSmokeWorkspaceID, TargetIndexVersionID: ragSmokeIndexID, ExpectedTargetVersion: ready.Version, IdempotencyKey: "rag-compose-smoke:activate", ReasonCode: "SMOKE"}); err != nil {
		t.Fatal(err)
	}
	seedComposeRAGKnowledge(t, ctx, pool, ragSmokeWorkspaceID, ragSmokeSourceVersionID, ragSmokeSpanID)
}

func composeRAGRelationEvidence(fixture composeRAGKnowledgeFixture, applicability knowledgedomain.Applicability, reason string, at time.Time, id foundation.ID) knowledgedomain.RelationEvidence {
	evidence := knowledgedomain.RelationEvidence{ID: id, WorkspaceID: fixture.WorkspaceID, RelationID: fixture.RelationID, Provenance: knowledgedomain.ProvenanceRef{WorkspaceID: fixture.WorkspaceID, SourceVersionID: fixture.SourceVersionID, SourceSpanID: fixture.SourceSpanID}, Reason: reason, Applicability: applicability, CreatedAt: at}
	evidence.EvidenceHash = knowledgedomain.ComputeRelationEvidenceHash(evidence)
	return evidence
}

func composeRAGHash(value string) string {
	digest := sha256.Sum256([]byte("compose-rag-smoke:" + value))
	return hex.EncodeToString(digest[:])
}

func seedComposeRAGProvenance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) composeRAGKnowledgeFixture {
	t.Helper()
	fixture := composeRAGKnowledgeFixture{WorkspaceID: "a0000000-0000-4000-8000-000000000001", SourceVersionID: "a0000000-0000-4000-8000-000000000004", SourceSpanID: "a0000000-0000-4000-8000-000000000006"}
	at := time.Date(2026, 7, 19, 23, 59, 0, 0, time.UTC)
	contentHash := composeRAGHash("content")
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'compose-rag-smoke','/tmp/compose-rag-smoke','/tmp/compose-rag-smoke',$2,'test',1,$2,$2)`, []any{string(fixture.WorkspaceID), at}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES('a0000000-0000-4000-8000-000000000002',$1,$2,75,'.knowledge/sources/'||$2,$3)`, []any{string(fixture.WorkspaceID), contentHash, at}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES('a0000000-0000-4000-8000-000000000003',$1,'text','rag-smoke.txt','rag-smoke.txt',$2)`, []any{string(fixture.WorkspaceID), at}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,'a0000000-0000-4000-8000-000000000003',$2,'a0000000-0000-4000-8000-000000000002',$3,75,'text/plain','rag-smoke.txt','passed',$4)`, []any{string(fixture.SourceVersionID), string(fixture.WorkspaceID), contentHash, at}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES('a0000000-0000-4000-8000-000000000005',$1,'a0000000-0000-4000-8000-000000000002','text','v1',$2,'v1',$3,'[]',$4)`, []any{string(fixture.WorkspaceID), composeRAGHash("parser"), composeRAGHash("normalized"), at}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,'a0000000-0000-4000-8000-000000000005',$2,$3)`, []any{string(fixture.SourceVersionID), string(fixture.WorkspaceID), at}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,'a0000000-0000-4000-8000-000000000002','a0000000-0000-4000-8000-000000000005','paragraph',1,1,0,75,'{}',$3,'v1','v1',$4)`, []any{string(fixture.SourceSpanID), string(fixture.WorkspaceID), composeRAGHash("excerpt"), at}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

// getenv is isolated so the deterministic seed helper remains reusable by the
// full public-API smoke without importing configuration internals.
var getenv = os.Getenv
