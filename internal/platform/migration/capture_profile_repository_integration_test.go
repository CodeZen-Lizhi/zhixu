//go:build integration

package migration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	captureprofile "github.com/CodeZen-Lizhi/zhixu/internal/capture/profile"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type captureProfileIntegrationCaptureRepository interface {
	captureapp.Repository
	captureapp.ProcessingRepository
}

type captureProfileIntegrationProfileRepository interface {
	captureapp.ProfileGenerationRepository
	captureapp.ProfileReader
	captureapp.ProfileBatchReader
	captureapp.ProfileRetryScheduler
}

type captureProfileRepositoryIntegrationHarness struct {
	capture   captureProfileIntegrationCaptureRepository
	profiles  captureProfileIntegrationProfileRepository
	modelRuns agentapp.ModelRunRepository
}

func withCaptureProfileRepositoryIntegration(
	t *testing.T,
	test func(*testing.T, *platformpostgres.Pool, context.Context, captureProfileRepositoryIntegrationHarness),
) {
	t.Helper()
	t.Run("gorm", func(t *testing.T) {
		fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
		platform := fixture.Pool()
		test(t, platform, context.Background(), openGORMCaptureProfileRepositoryIntegration(t, platform))
	})
}

func openGORMCaptureProfileRepositoryIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) captureProfileRepositoryIntegrationHarness {
	t.Helper()
	workspaceRepository, err := workspacepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	captureRepository, err := capturepostgres.NewGORMRepository(platform, workspaceRepository)
	if err != nil {
		t.Fatal(err)
	}
	modelRuns, err := agentpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(platform, modelRuns)
	if err != nil {
		t.Fatal(err)
	}
	return captureProfileRepositoryIntegrationHarness{
		capture: captureRepository, profiles: profiles, modelRuns: modelRuns,
	}
}

func TestUnavailableProfileGeneratorPersistsStateAndAttemptWithoutDerivedContent(t *testing.T) {
	withCaptureProfileRepositoryIntegration(t, testUnavailableProfileGeneratorPersistsStateAndAttemptWithoutDerivedContent)
}

func testUnavailableProfileGeneratorPersistsStateAndAttemptWithoutDerivedContent(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	harness captureProfileRepositoryIntegrationHarness,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 17, 0, 0, 0, time.UTC)
	const (
		workspaceID        foundation.ID = "68400000-0000-4000-8000-000000000001"
		definitionID                     = "68400000-0000-4000-8000-000000000002"
		workflowRunID                    = "68400000-0000-4000-8000-000000000003"
		nodeRunID                        = "68400000-0000-4000-8000-000000000004"
		nodeAttemptID                    = "68400000-0000-4000-8000-000000000005"
		captureAttemptID                 = "68400000-0000-4000-8000-000000000006"
		parseProjectionID                = "68400000-0000-4000-8000-000000000007"
		indexVersionID                   = "68400000-0000-4000-8000-000000000008"
		profileID                        = "68400000-0000-4000-8000-000000000009"
		ingestionAttemptID               = "68400000-0000-4000-8000-000000000010"
	)
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)

	captureRepository := harness.capture
	profiles := harness.profiles
	captureService, err := captureapp.NewService(captureapp.Dependencies{
		Repository: captureRepository, Content: captureIntegrationContent{},
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := captureService.CreateText(ctx, captureapp.TextCommand{
		WorkspaceID: workspaceID, DisplayName: "Unavailable profile", Text: "Java AI evidence",
		IdempotencyKey: "unavailable-profile-capture",
	})
	if err != nil {
		t.Fatal(err)
	}
	var artifactID foundation.ID
	if err := pool.QueryRow(ctx, `SELECT content_artifact_id::text FROM core.source_version WHERE id=$1`,
		string(created.Capture.LatestSourceVersionID)).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}

	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES($1,$2,'capture-profile-unavailable',1,'{"nodes":[]}',$3)`, []any{definitionID, string(workspaceID), now}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{workflowRunID, string(workspaceID), definitionID, now}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
			VALUES($1,$2,'capture.process','capture.process','running','{}','capture-profile-test',$3,1,$4,$4)`, []any{nodeRunID, workflowRunID, now.Add(time.Minute), now}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
			VALUES($1,$2,1,1,0,'capture-profile-delivery','capture-profile-test',$3,'running',$4)`, []any{nodeAttemptID, nodeRunID, now.Add(time.Minute), now}},
		{`INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,warnings,created_at
		) VALUES($1,$2,$3,'text','v1',repeat('a',64),'v1',repeat('b',64),'[]',$4)`, []any{parseProjectionID, string(workspaceID), string(artifactID), now}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
			parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at
		) VALUES($1,$2,$3,$4,'chunked','passed','text','v1',repeat('a',64),'structure-v1','v1',
			'capture-profile-ingestion',1,$5,$5)`, []any{ingestionAttemptID, string(workspaceID), string(created.Capture.LatestSourceVersionID), parseProjectionID, now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			VALUES($1,$2,$3,$4)`, []any{string(created.Capture.LatestSourceVersionID), parseProjectionID, string(workspaceID), now}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
			version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,
			source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version
		) VALUES($1,$2,'simple','v1',repeat('c',64),'{}','capture-profile:index',repeat('d',64),0,
			'capture-profile-index','building','["vector"]',1,$3,$3,repeat('e',64),1,'text','v1',
			repeat('a',64),'structure-v1','v1')`, []any{indexVersionID, string(workspaceID), now}},
		{`INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
		) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{indexVersionID, string(workspaceID), string(created.Capture.SourceID), string(created.Capture.LatestSourceVersionID), parseProjectionID, now}},
		{`UPDATE retrieval.index_version SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`,
			[]any{indexVersionID, now.Add(time.Second)}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	processingCapture, processingAttempt, err := captureRepository.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: captureAttemptID, WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		WorkflowRunID: workflowRunID, AttemptNumber: 1, StartedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	processingCapture, processingAttempt, err = captureRepository.MarkRefreshReady(ctx, captureapp.RefreshCheckpoint{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: parseProjectionID,
		IndexVersionID: indexVersionID, UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, repeatedCheckpointErr := captureRepository.MarkRefreshReady(ctx, captureapp.RefreshCheckpoint{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: parseProjectionID,
		IndexVersionID: indexVersionID, UpdatedAt: now.Add(3 * time.Second),
	})
	var repeatedCheckpointFailure *foundation.Error
	if !errors.As(repeatedCheckpointErr, &repeatedCheckpointFailure) ||
		repeatedCheckpointFailure.Code != "CAPTURE_REFRESH_CHECKPOINT_INVALID" {
		t.Fatalf("repeated refresh checkpoint error=%#v", repeatedCheckpointErr)
	}
	var repeatedCaptureVersion, repeatedAttemptVersion int64
	var repeatedCaptureStatus, repeatedProfileStatus, repeatedAttemptStage string
	var repeatedIngestionAttemptID, repeatedIndexVersionID string
	if err := pool.QueryRow(ctx, `SELECT capture.version,capture.status,capture.profile_status,
		attempt.version,attempt.stage,attempt.ingestion_attempt_id::text,attempt.index_version_id::text
		FROM core.capture AS capture
		JOIN ops.capture_attempt AS attempt ON attempt.capture_id=capture.id AND attempt.workspace_id=capture.workspace_id
		WHERE capture.id=$1 AND capture.workspace_id=$2 AND attempt.id=$3`,
		string(created.Capture.ID), string(workspaceID), captureAttemptID).Scan(
		&repeatedCaptureVersion, &repeatedCaptureStatus, &repeatedProfileStatus,
		&repeatedAttemptVersion, &repeatedAttemptStage, &repeatedIngestionAttemptID, &repeatedIndexVersionID); err != nil {
		t.Fatal(err)
	}
	if repeatedCaptureVersion != processingCapture.Version || repeatedCaptureStatus != string(capturedomain.StatusProcessing) ||
		repeatedProfileStatus != string(capturedomain.StageRunning) || repeatedAttemptVersion != processingAttempt.Version ||
		repeatedAttemptStage != string(capturedomain.AttemptStageProfile) || repeatedIngestionAttemptID != string(ingestionAttemptID) ||
		repeatedIndexVersionID != string(indexVersionID) {
		t.Fatalf("repeated checkpoint changed state capture_version=%d capture_status=%q profile_status=%q attempt_version=%d attempt_stage=%q ingestion=%q index=%q",
			repeatedCaptureVersion, repeatedCaptureStatus, repeatedProfileStatus, repeatedAttemptVersion,
			repeatedAttemptStage, repeatedIngestionAttemptID, repeatedIndexVersionID)
	}

	generator, err := captureprofile.NewUnavailableGenerator(
		profiles, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(3 * time.Second)},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, generateErr := generator.Generate(ctx, captureapp.ProfileGenerationRequest{
		ProfileID: profileID, Capture: processingCapture, Attempt: processingAttempt,
		ParseProjectionID: parseProjectionID, IndexVersionID: indexVersionID,
		WorkflowRunID: workflowRunID, NodeRunID: nodeRunID, NodeAttemptID: nodeAttemptID,
	})
	var classified *foundation.Error
	if !errors.As(generateErr, &classified) || classified.Code != captureprofile.ErrorCodeCapabilityUnavailable || classified.Retryable {
		t.Fatalf("Generate() error = %#v", generateErr)
	}
	completedCapture, completedAttempt, err := captureRepository.CompleteDegraded(ctx, captureapp.DegradedCompletion{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version, ProfileID: profileID,
		IngestionStatus: capturedomain.StageReady, IndexStatus: capturedomain.StageReady,
		ProfileStatus: capturedomain.ProfileStatusCapabilityUnavailable,
		Code:          captureprofile.ErrorCodeCapabilityUnavailable, CompletedAt: now.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if completedCapture.Status != capturedomain.StatusReadyDegraded ||
		completedCapture.ProfileStatus != capturedomain.StageCapabilityUnavailable ||
		completedAttempt.Status != capturedomain.AttemptStatusSucceeded || completedAttempt.ProfileID != profileID {
		t.Fatalf("completed capture=%#v attempt=%#v", completedCapture, completedAttempt)
	}

	var profileStatus, profileError string
	var profileRetryable bool
	if err := pool.QueryRow(ctx, `SELECT status,error_code,retryable FROM learning.document_knowledge_profile
		WHERE workspace_id=$1 AND source_version_id=$2`, string(workspaceID), string(created.Capture.LatestSourceVersionID)).
		Scan(&profileStatus, &profileError, &profileRetryable); err != nil {
		t.Fatal(err)
	}
	if profileStatus != "CAPABILITY_UNAVAILABLE" || profileError != captureprofile.ErrorCodeCapabilityUnavailable || profileRetryable {
		t.Fatalf("profile status=%q error=%q retryable=%v", profileStatus, profileError, profileRetryable)
	}
	var attemptStatus, attemptError string
	var attemptRetryable bool
	var modelRunMissing bool
	if err := pool.QueryRow(ctx, `SELECT status,error_code,retryable,model_run_id IS NULL
		FROM learning.document_knowledge_profile_attempt WHERE profile_id=$1`, profileID).
		Scan(&attemptStatus, &attemptError, &attemptRetryable, &modelRunMissing); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != "CAPABILITY_UNAVAILABLE" || attemptError != captureprofile.ErrorCodeCapabilityUnavailable ||
		attemptRetryable || !modelRunMissing {
		t.Fatalf("attempt status=%q error=%q retryable=%v model_run_missing=%v", attemptStatus, attemptError, attemptRetryable, modelRunMissing)
	}
	var revisionCount, evidenceCount, modelRunCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_revision WHERE profile_id=$1`, profileID).
		Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_evidence WHERE revision_id IN (
		SELECT id FROM learning.document_knowledge_profile_revision WHERE profile_id=$1
	)`, profileID).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.model_run WHERE workflow_run_id=$1`, workflowRunID).
		Scan(&modelRunCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 0 || evidenceCount != 0 || modelRunCount != 0 {
		t.Fatalf("unexpected derived rows revisions=%d evidence=%d model_runs=%d", revisionCount, evidenceCount, modelRunCount)
	}

	profileView, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	retryService, err := captureapp.NewProfileRetryService(captureapp.ProfileRetryDependencies{
		Scheduler: profiles, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now.Add(5 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	retryCommand := captureapp.ProfileRetryCommand{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
		ExpectedVersion: profileView.Profile.Version, IdempotencyKey: "unavailable-profile-rebuild",
	}
	rebuild, err := retryService.RetryProfile(ctx, retryCommand)
	if err != nil {
		t.Fatalf("schedule unavailable profile rebuild: %v", err)
	}
	if rebuild.Replayed || rebuild.View.Profile.Status != capturedomain.ProfileStatusPending ||
		rebuild.View.Profile.CurrentRevisionID != "" || rebuild.View.Revision != nil || len(rebuild.View.Evidence) != 0 {
		t.Fatalf("rebuild=%#v", rebuild)
	}
	rebuiltCapture, err := captureRepository.Get(ctx, workspaceID, created.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltCapture.Status != capturedomain.StatusSourceSaved || rebuiltCapture.IngestionStatus != capturedomain.StageReady ||
		rebuiltCapture.IndexStatus != capturedomain.StageReady || rebuiltCapture.ProfileStatus != capturedomain.StagePending {
		t.Fatalf("rebuilt capture=%#v", rebuiltCapture)
	}
	var retryOutboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.capture_outbox
		WHERE workspace_id=$1 AND capture_id=$2 AND event_key LIKE 'capture.profile-retry:v1:%'`,
		string(workspaceID), string(created.Capture.ID)).Scan(&retryOutboxCount); err != nil {
		t.Fatal(err)
	}
	if retryOutboxCount != 1 {
		t.Fatalf("profile retry outbox count=%d", retryOutboxCount)
	}
	replayed, err := retryService.RetryProfile(ctx, retryCommand)
	if err != nil {
		t.Fatalf("replay unavailable profile rebuild: %v", err)
	}
	if !replayed.Replayed || replayed.View.Profile.Version != rebuild.View.Profile.Version || replayed.View.Revision != nil {
		t.Fatalf("replayed rebuild=%#v", replayed)
	}
}

func TestReadyProfileGeneratorPersistsEvidenceAndReplaysWithoutCallingModelAgain(t *testing.T) {
	withCaptureProfileRepositoryIntegration(t, testReadyProfileGeneratorPersistsEvidenceAndReplaysWithoutCallingModelAgain)
}

func testReadyProfileGeneratorPersistsEvidenceAndReplaysWithoutCallingModelAgain(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	harness captureProfileRepositoryIntegrationHarness,
) {
	pool := platform.DB()
	now := time.Date(2026, 8, 2, 18, 0, 0, 0, time.UTC)
	const (
		workspaceID                    foundation.ID = "68500000-0000-4000-8000-000000000001"
		definitionID                                 = "68500000-0000-4000-8000-000000000002"
		workflowRunID                                = "68500000-0000-4000-8000-000000000003"
		nodeRunID                                    = "68500000-0000-4000-8000-000000000004"
		nodeAttemptID                                = "68500000-0000-4000-8000-000000000005"
		captureAttemptID                             = "68500000-0000-4000-8000-000000000006"
		parseProjectionID                            = "68500000-0000-4000-8000-000000000007"
		indexVersionID                               = "68500000-0000-4000-8000-000000000008"
		profileID                                    = "68500000-0000-4000-8000-000000000009"
		ingestionAttemptID                           = "68500000-0000-4000-8000-000000000010"
		spanID                                       = "68500000-0000-4000-8000-000000000011"
		chunkID                                      = "68500000-0000-4000-8000-000000000012"
		rebuildWorkflowRunID                         = "68500000-0000-4000-8000-000000000013"
		rebuildNodeRunID                             = "68500000-0000-4000-8000-000000000014"
		rebuildNodeAttemptID                         = "68500000-0000-4000-8000-000000000015"
		rebuildCaptureAttemptID                      = "68500000-0000-4000-8000-000000000016"
		rebuildIndexVersionID                        = "68500000-0000-4000-8000-000000000017"
		workerRuntimeID                              = "68500000-0000-4000-8000-000000000018"
		wrongIngestionAttemptID                      = "68500000-0000-4000-8000-000000000019"
		wrongParseProjectionID                       = "68500000-0000-4000-8000-000000000020"
		wrongIndexVersionID                          = "68500000-0000-4000-8000-000000000021"
		alternateParseProjectionID                   = "68500000-0000-4000-8000-000000000022"
		alternateIngestionAttemptID                  = "68500000-0000-4000-8000-000000000023"
		alternateSpanID                              = "68500000-0000-4000-8000-000000000024"
		alternateChunkID                             = "68500000-0000-4000-8000-000000000025"
		alternateIndexVersionID                      = "68500000-0000-4000-8000-000000000026"
		baseIndexActivationID                        = "68500000-0000-4000-8000-000000000027"
		alternateIndexActivationID                   = "68500000-0000-4000-8000-000000000028"
		unrelatedWorkspaceID                         = "68500000-0000-4000-8000-000000000029"
		unrelatedIndexVersionID                      = "68500000-0000-4000-8000-000000000030"
		unrelatedIndexActivationID                   = "68500000-0000-4000-8000-000000000031"
		interleaveWorkflowRunID                      = "68500000-0000-4000-8000-000000000032"
		interleaveNodeRunID                          = "68500000-0000-4000-8000-000000000033"
		interleaveNodeAttemptID                      = "68500000-0000-4000-8000-000000000034"
		interleaveCaptureAttemptID                   = "68500000-0000-4000-8000-000000000035"
		interleaveOldIndexVersionID                  = "68500000-0000-4000-8000-000000000036"
		interleaveOldIndexActivationID               = "68500000-0000-4000-8000-000000000037"
		buildingIndexVersionID                       = "68500000-0000-4000-8000-000000000038"
		oversizedParseProjectionID                   = "68500000-0000-4000-8000-000000000039"
		oversizedIngestionAttemptID                  = "68500000-0000-4000-8000-000000000040"
		oversizedSpanID                              = "68500000-0000-4000-8000-000000000041"
		oversizedChunkID                             = "68500000-0000-4000-8000-000000000042"
	)
	const content = "Spring AI integrates model providers."
	insertCaptureWorkspace(t, ctx, pool, workspaceID, now)

	captureRepository := harness.capture
	profiles := harness.profiles
	modelRuns := harness.modelRuns
	captureService, err := captureapp.NewService(captureapp.Dependencies{
		Repository: captureRepository, Content: captureIntegrationContent{},
		IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := captureService.CreateText(ctx, captureapp.TextCommand{
		WorkspaceID: workspaceID, DisplayName: "Ready profile", Text: content,
		IdempotencyKey: "ready-profile-capture",
	})
	if err != nil {
		t.Fatal(err)
	}
	var artifactID foundation.ID
	var contentHash string
	var byteSize int64
	if err := pool.QueryRow(ctx, `SELECT content_artifact_id::text,content_hash,byte_size
		FROM core.source_version WHERE id=$1`, string(created.Capture.LatestSourceVersionID)).
		Scan(&artifactID, &contentHash, &byteSize); err != nil {
		t.Fatal(err)
	}

	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES($1,$2,'capture-profile-ready',1,'{"nodes":[]}',$3)`, []any{definitionID, string(workspaceID), now}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{workflowRunID, string(workspaceID), definitionID, now}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
			VALUES($1,$2,'capture.process','capture.process','running','{}','capture-profile-test',$3,1,$4,$4)`, []any{nodeRunID, workflowRunID, now.Add(time.Minute), now}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
			VALUES($1,$2,1,1,0,'capture-profile-ready-delivery','capture-profile-test',$3,'running',$4)`, []any{nodeAttemptID, nodeRunID, now.Add(time.Minute), now}},
		{`INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,warnings,created_at
		) VALUES($1,$2,$3,'text','v1',repeat('a',64),'v1',$4,'[]',$5)`, []any{parseProjectionID, string(workspaceID), string(artifactID), contentHash, now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			VALUES($1,$2,$3,$4)`, []any{string(created.Capture.LatestSourceVersionID), parseProjectionID, string(workspaceID), now}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
			parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at
		) VALUES($1,$2,$3,$4,'chunked','passed','text','v1',repeat('a',64),'structure-v1','v1',
			'capture-profile-ready-ingestion',1,$5,$5)`, []any{ingestionAttemptID, string(workspaceID), string(created.Capture.LatestSourceVersionID), parseProjectionID, now}},
		{`INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,
			selector,excerpt_hash,parser_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind":"paragraph"}',$6,'v1','v1',$7)`, []any{spanID, string(workspaceID), string(artifactID), parseProjectionID, byteSize, contentHash, now}},
		{`INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
			byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
		) VALUES($1,$2,$3,0,'["Java AI"]',$4,$5,$6,$7,$7,'v1','structure-v1','v1',false,'active',$8)`, []any{chunkID, string(workspaceID), parseProjectionID, content, contentHash, spanID, byteSize, now}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
			version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,
			source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version
		) VALUES($1,$2,'simple','v1',repeat('c',64),'{}','capture-profile:ready',repeat('d',64),1,
			'capture-profile-ready-index','building','["vector"]',1,$3,$3,repeat('e',64),1,'text','v1',
			repeat('a',64),'structure-v1','v1')`, []any{indexVersionID, string(workspaceID), now}},
		{`INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`, []any{indexVersionID, chunkID, string(workspaceID), contentHash, now}},
		{`INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
		) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{indexVersionID, string(workspaceID), string(created.Capture.SourceID), string(created.Capture.LatestSourceVersionID), parseProjectionID, now}},
		{`INSERT INTO retrieval.chunk_projection(
			index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at
		) VALUES($1,$2,$3,to_tsvector('simple',$4::text),cardinality(tsvector_to_array(to_tsvector('simple',$4::text))),
			'ready','disabled',$5,$5)`, []any{indexVersionID, chunkID, string(workspaceID), content, now}},
		{`UPDATE retrieval.index_version
			SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`, []any{indexVersionID, now.Add(time.Second)}},
		{`INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,warnings,created_at
		) VALUES($1,$2,$3,'text-oversized','v1',repeat('b',64),'v1',$4,'[]',$5)`, []any{
			oversizedParseProjectionID, string(workspaceID), string(artifactID), contentHash, now,
		}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			VALUES($1,$2,$3,$4)`, []any{
			string(created.Capture.LatestSourceVersionID), oversizedParseProjectionID, string(workspaceID), now,
		}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
			parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at
		) VALUES($1,$2,$3,$4,'chunked','passed','text-oversized','v1',repeat('b',64),'structure-v1','v1',
			'capture-profile-oversized-ingestion',1,$5,$5)`, []any{
			oversizedIngestionAttemptID, string(workspaceID), string(created.Capture.LatestSourceVersionID), oversizedParseProjectionID, now,
		}},
		{`INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,
			selector,excerpt_hash,parser_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,'oversized-profile-test',1,1,0,$5,'{"kind":"paragraph"}',$6,'v1','v1',$7)`, []any{
			oversizedSpanID, string(workspaceID), string(artifactID), oversizedParseProjectionID, byteSize, contentHash, now,
		}},
		{`INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
			byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
		) VALUES($1,$2,$3,0,'["Oversized"]',repeat('x',$4),repeat('f',64),$5,
			$4,$4,'v1','structure-v1','v1',true,'active',$6)`, []any{
			oversizedChunkID, string(workspaceID), oversizedParseProjectionID, captureapp.MaxProfileChunkBytes + 1, oversizedSpanID, now,
		}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
			version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,
			source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version
		) VALUES($1,$2,'simple','v1',repeat('c',64),'{}','capture-profile:building',repeat('d',64),1,
			'capture-profile-building-index','building','["vector"]',1,$3,$3,repeat('e',64),1,'text-oversized','v1',
			repeat('b',64),'structure-v1','v1')`, []any{buildingIndexVersionID, string(workspaceID), now}},
		{`INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
		) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{buildingIndexVersionID, string(workspaceID), string(created.Capture.SourceID), string(created.Capture.LatestSourceVersionID), oversizedParseProjectionID, now}},
		{`INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
		) VALUES($1,$2,$3,repeat('f',64),0,'v1','structure-v1','v1',$4)`, []any{
			buildingIndexVersionID, oversizedChunkID, string(workspaceID), now,
		}},
		{`INSERT INTO retrieval.chunk_projection(
			index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at
		) VALUES($1,$2,$3,to_tsvector('simple','oversized'),1,'ready','disabled',$4,$4)`, []any{
			buildingIndexVersionID, oversizedChunkID, string(workspaceID), now,
		}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	processingCapture, processingAttempt, err := captureRepository.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: captureAttemptID, WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		WorkflowRunID: workflowRunID, AttemptNumber: 1, StartedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range []captureapp.RefreshCheckpoint{
		{
			Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
			IngestionAttemptID: wrongIngestionAttemptID, ParseProjectionID: parseProjectionID,
			IndexVersionID: indexVersionID, UpdatedAt: now.Add(2 * time.Second),
		},
		{
			Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
			IngestionAttemptID: ingestionAttemptID, ParseProjectionID: wrongParseProjectionID,
			IndexVersionID: indexVersionID, UpdatedAt: now.Add(2 * time.Second),
		},
		{
			Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
			IngestionAttemptID: ingestionAttemptID, ParseProjectionID: parseProjectionID,
			IndexVersionID: wrongIndexVersionID, UpdatedAt: now.Add(2 * time.Second),
		},
	} {
		_, _, checkpointErr := captureRepository.MarkRefreshReady(ctx, checkpoint)
		var checkpointFailure *foundation.Error
		if !errors.As(checkpointErr, &checkpointFailure) || checkpointFailure.Code != "CAPTURE_REFRESH_CHECKPOINT_BINDING_INVALID" {
			t.Fatalf("invalid refresh checkpoint error=%#v checkpoint=%#v", checkpointErr, checkpoint)
		}
	}
	_, _, buildingCheckpointErr := captureRepository.MarkRefreshReady(ctx, captureapp.RefreshCheckpoint{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: parseProjectionID,
		IndexVersionID: buildingIndexVersionID, UpdatedAt: now.Add(2 * time.Second),
	})
	var buildingCheckpointFailure *foundation.Error
	if !errors.As(buildingCheckpointErr, &buildingCheckpointFailure) ||
		buildingCheckpointFailure.Code != "CAPTURE_REFRESH_CHECKPOINT_BINDING_INVALID" {
		t.Fatalf("building index checkpoint error=%#v", buildingCheckpointErr)
	}
	var persistedCaptureVersion, persistedAttemptVersion int64
	var persistedCaptureStatus, persistedAttemptStage string
	var ingestionBindingMissing, indexBindingMissing bool
	if err := pool.QueryRow(ctx, `SELECT capture.version,capture.status,attempt.version,attempt.stage,
		attempt.ingestion_attempt_id IS NULL,attempt.index_version_id IS NULL
		FROM core.capture AS capture
		JOIN ops.capture_attempt AS attempt ON attempt.capture_id=capture.id AND attempt.workspace_id=capture.workspace_id
		WHERE capture.id=$1 AND capture.workspace_id=$2 AND attempt.id=$3`, string(created.Capture.ID), string(workspaceID), captureAttemptID).
		Scan(&persistedCaptureVersion, &persistedCaptureStatus, &persistedAttemptVersion, &persistedAttemptStage,
			&ingestionBindingMissing, &indexBindingMissing); err != nil {
		t.Fatal(err)
	}
	if persistedCaptureVersion != processingCapture.Version || persistedCaptureStatus != string(capturedomain.StatusProcessing) ||
		persistedAttemptVersion != processingAttempt.Version || persistedAttemptStage != string(capturedomain.AttemptStageIngestion) ||
		!ingestionBindingMissing || !indexBindingMissing {
		t.Fatalf("invalid checkpoint changed state capture_version=%d capture_status=%q attempt_version=%d attempt_stage=%q ingestion_missing=%v index_missing=%v",
			persistedCaptureVersion, persistedCaptureStatus, persistedAttemptVersion, persistedAttemptStage,
			ingestionBindingMissing, indexBindingMissing)
	}
	processingCapture, processingAttempt, err = captureRepository.MarkRefreshReady(ctx, captureapp.RefreshCheckpoint{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: parseProjectionID,
		IndexVersionID: indexVersionID, UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `UPDATE retrieval.index_version
		SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`,
		buildingIndexVersionID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := profiles.LoadSource(ctx, captureapp.ProfileLookup{
		WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		SourceVersionID: created.Capture.LatestSourceVersionID,
	}, oversizedParseProjectionID, buildingIndexVersionID); err == nil {
		t.Fatal("oversized profile chunk unexpectedly crossed the SQL projection boundary")
	} else {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != "CAPTURE_PROFILE_CONTEXT_INVALID" {
			t.Fatalf("oversized profile chunk error=%#v", err)
		}
	}
	modelRef := agentdomain.ModelRef{
		AdapterName: "openai-compatible", AdapterVersion: "v1",
		ModelID: "capture-profile-integration", ModelVersion: "v1",
	}
	profileRef := agentdomain.ModelProfileRef{ID: "capture.profile.integration", Version: "v1"}
	catalog := agentapp.NewRuntimeCatalog()
	if err := captureprofile.RegisterRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterProfile(agentapp.ModelProfile{
		Ref: profileRef, Model: modelRef, Timeout: 5 * time.Second, MaxOutputTokens: 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(map[string]any{
		"result_type": agentdomain.ResultTypeDocumentKnowledgeProfile,
		"schema_id":   captureprofile.SchemaID, "schema_version": capturedomain.ProfileSchemaVersion,
		"summary": "Spring AI provides a model-provider abstraction.",
		"topics": []any{map[string]any{
			"label": "Java AI", "aliases": []string{"Spring AI"}, "evidence_labels": []string{"E0001"},
		}},
		"terms": []any{},
		"knowledge_points": []any{map[string]any{
			"text": content, "evidence_labels": []string{"E0001"},
		}},
		"examples": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	modelStep := agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{
		Model: modelRef, Content: output,
		Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}}
	initialModel := newCaptureProfileBlockingModel(modelStep.Response)
	clock := &captureProfileIntegrationClock{next: now.Add(3 * time.Second)}
	generator, err := captureprofile.NewGenerator(captureprofile.GeneratorDependencies{
		Repository: profiles, ModelRuns: modelRuns, Model: initialModel, Catalog: catalog,
		Scheduler: scheduler, ModelProfileRef: profileRef,
		IDs: foundation.NewUUIDGenerator(nil), Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := captureapp.ProfileGenerationRequest{
		ProfileID: profileID, Capture: processingCapture, Attempt: processingAttempt,
		ParseProjectionID: parseProjectionID, IndexVersionID: indexVersionID,
		WorkflowRunID: workflowRunID, NodeRunID: nodeRunID, NodeAttemptID: nodeAttemptID,
	}
	type generationOutcome struct {
		result captureapp.ProfileGenerationResult
		err    error
	}
	generation := make(chan generationOutcome, 1)
	go func() {
		result, generateErr := generator.Generate(ctx, request)
		generation <- generationOutcome{result: result, err: generateErr}
	}()
	select {
	case <-initialModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("profile model did not start")
	}
	activateCaptureProfileModelSettingsRevision(t, ctx, pool, workerRuntimeID)
	close(initialModel.release)
	outcome := <-generation
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	result := outcome.result
	if result.ProfileID != profileID || result.RevisionID == "" || initialModel.CallCount() != 1 {
		t.Fatalf("generation result=%#v model_calls=%d", result, initialModel.CallCount())
	}

	view, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Profile.Status != capturedomain.ProfileStatusStale || view.Profile.CurrentRevisionID != result.RevisionID ||
		view.Revision == nil || view.Revision.ID != result.RevisionID || view.Revision.ModelRunID == "" ||
		view.Revision.Content.Summary != "Spring AI provides a model-provider abstraction." ||
		len(view.Revision.Content.Topics) != 1 || len(view.Revision.Content.KnowledgePoints) != 1 ||
		len(view.Evidence) != 1 || view.Evidence[0].SourceSpanID != spanID {
		t.Fatalf("profile view=%#v", view)
	}
	completedCapture, completedAttempt, err := captureRepository.CompleteAttempt(ctx, captureapp.ReadyCompletion{
		Attempt: processingAttempt, ExpectedCaptureVersion: processingCapture.Version,
		ProfileID: profileID, CompletedAt: now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if completedCapture.Status != capturedomain.StatusReady || completedCapture.ProfileStatus != capturedomain.StageStale ||
		completedAttempt.Status != capturedomain.AttemptStatusSucceeded || completedAttempt.ProfileID != profileID {
		t.Fatalf("completed capture=%#v attempt=%#v", completedCapture, completedAttempt)
	}

	if _, err := generator.Generate(ctx, request); err == nil || initialModel.CallCount() != 1 {
		t.Fatalf("stale contract replay error=%v model_calls=%d", err, initialModel.CallCount())
	}
	var revisionCount, profileAttemptCount, modelCallCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_revision WHERE profile_id=$1`, profileID).
		Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_attempt WHERE profile_id=$1`, profileID).
		Scan(&profileAttemptCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.model_call WHERE model_run_id=$1`, string(view.Revision.ModelRunID)).
		Scan(&modelCallCount); err != nil {
		t.Fatal(err)
	}
	var modelRunStatus, finalResultType string
	if err := pool.QueryRow(ctx, `SELECT status,final_result_type FROM agent.model_run WHERE id=$1`, string(view.Revision.ModelRunID)).
		Scan(&modelRunStatus, &finalResultType); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 1 || profileAttemptCount != 1 || modelCallCount != 1 ||
		modelRunStatus != string(agentdomain.ModelRunSucceeded) || finalResultType != agentdomain.ResultTypeDocumentKnowledgeProfile {
		t.Fatalf("revisions=%d profile_attempts=%d model_calls=%d model_run=%q result_type=%q",
			revisionCount, profileAttemptCount, modelCallCount, modelRunStatus, finalResultType)
	}

	staleView, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleCapture, err := captureRepository.Get(ctx, workspaceID, created.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if staleView.Profile.Status != capturedomain.ProfileStatusStale || staleView.Profile.CurrentRevisionID != result.RevisionID ||
		staleView.Revision == nil || staleView.Revision.ID != result.RevisionID || len(staleView.Evidence) != 1 ||
		staleCapture.ProfileStatus != capturedomain.StageStale || staleCapture.Status != capturedomain.StatusReady {
		t.Fatalf("stale view=%#v capture=%#v", staleView, staleCapture)
	}
	retryService, err := captureapp.NewProfileRetryService(captureapp.ProfileRetryDependencies{
		Scheduler: profiles, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	retryResult, err := retryService.RetryProfile(ctx, captureapp.ProfileRetryCommand{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
		ExpectedVersion: staleView.Profile.Version, IdempotencyKey: "ready-profile-stale-rebuild",
	})
	if err != nil {
		t.Fatalf("schedule stale profile rebuild: %#v (cause: %v)", err, errors.Unwrap(err))
	}
	if retryResult.Replayed || retryResult.View.Profile.Status != capturedomain.ProfileStatusPending ||
		retryResult.View.Profile.CurrentRevisionID != result.RevisionID || retryResult.View.Revision == nil ||
		retryResult.View.Revision.ID != result.RevisionID || len(retryResult.View.Evidence) != 1 {
		t.Fatalf("stale retry result=%#v", retryResult)
	}
	queuedCapture, err := captureRepository.Get(ctx, workspaceID, created.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queuedCapture.Status != capturedomain.StatusSourceSaved || queuedCapture.ProfileStatus != capturedomain.StagePending ||
		queuedCapture.Retryable || queuedCapture.ErrorCode != "" {
		t.Fatalf("queued stale capture=%#v", queuedCapture)
	}

	rebuildNow := queuedCapture.UpdatedAt.Add(2 * time.Second)
	seedReadyCaptureProfileIndex(t, ctx, pool, rebuildIndexVersionID, workspaceID, chunkID,
		created.Capture.SourceID, created.Capture.LatestSourceVersionID, parseProjectionID, contentHash, content, rebuildNow)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{rebuildWorkflowRunID, string(workspaceID), definitionID, rebuildNow}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
			VALUES($1,$2,'capture.process','capture.process','running','{}','capture-profile-rebuild',$3,1,$4,$4)`, []any{rebuildNodeRunID, rebuildWorkflowRunID, rebuildNow.Add(time.Minute), rebuildNow}},
		{`INSERT INTO workflow.node_attempt(
			id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,
			model_settings_revision,model_runtime_instance_id
		) VALUES($1,$2,1,1,0,'capture-profile-rebuild-delivery','capture-profile-rebuild',$3,'running',$4,1,$5)`, []any{rebuildNodeAttemptID, rebuildNodeRunID, rebuildNow.Add(time.Minute), rebuildNow, workerRuntimeID}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	rebuildCapture, rebuildAttempt, err := captureRepository.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: rebuildCaptureAttemptID, WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		WorkflowRunID: rebuildWorkflowRunID, AttemptNumber: 1, StartedAt: rebuildNow,
	})
	if err != nil {
		t.Fatalf("begin stale rebuild attempt: %#v (cause: %v)", err, errors.Unwrap(err))
	}
	rebuildCapture, rebuildAttempt, err = captureRepository.MarkRefreshReady(ctx, captureapp.RefreshCheckpoint{
		Attempt: rebuildAttempt, ExpectedCaptureVersion: rebuildCapture.Version,
		IngestionAttemptID: ingestionAttemptID, ParseProjectionID: parseProjectionID,
		IndexVersionID: rebuildIndexVersionID, UpdatedAt: rebuildNow.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	rebuildModel := agentapp.NewDeterministicChatModel(modelStep)
	rebuildGenerator, err := captureprofile.NewGenerator(captureprofile.GeneratorDependencies{
		Repository: profiles, ModelRuns: modelRuns, Model: rebuildModel, Catalog: catalog,
		Scheduler: scheduler, ModelProfileRef: profileRef, IDs: foundation.NewUUIDGenerator(nil),
		Clock: &captureProfileIntegrationClock{next: rebuildNow.Add(2 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	settingsRevision := int64(1)
	rebuildRequest := captureapp.ProfileGenerationRequest{
		ProfileID: profileID, Capture: rebuildCapture, Attempt: rebuildAttempt,
		ParseProjectionID: parseProjectionID, IndexVersionID: rebuildIndexVersionID,
		WorkflowRunID: rebuildWorkflowRunID, NodeRunID: rebuildNodeRunID, NodeAttemptID: rebuildNodeAttemptID,
		ModelSettingsRevision: &settingsRevision,
	}
	rebuilt, err := rebuildGenerator.Generate(ctx, rebuildRequest)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.ProfileID != profileID || rebuilt.RevisionID == "" || rebuilt.RevisionID == result.RevisionID || rebuildModel.CallCount() != 1 {
		t.Fatalf("rebuilt=%#v original=%#v model_calls=%d", rebuilt, result, rebuildModel.CallCount())
	}
	rebuildCapture, rebuildAttempt, err = captureRepository.CompleteAttempt(ctx, captureapp.ReadyCompletion{
		Attempt: rebuildAttempt, ExpectedCaptureVersion: rebuildCapture.Version,
		ProfileID: profileID, CompletedAt: rebuildNow.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebuildCapture.Status != capturedomain.StatusReady || rebuildCapture.ProfileStatus != capturedomain.StageReady ||
		rebuildAttempt.Status != capturedomain.AttemptStatusSucceeded {
		t.Fatalf("rebuilt capture=%#v attempt=%#v", rebuildCapture, rebuildAttempt)
	}
	finalView, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalView.Profile.Status != capturedomain.ProfileStatusReady || finalView.Profile.CurrentRevisionID != rebuilt.RevisionID ||
		finalView.Revision == nil || finalView.Revision.ID != rebuilt.RevisionID ||
		finalView.Revision.IndexVersionID != rebuildIndexVersionID || finalView.Revision.ModelSettingsRevision == nil ||
		*finalView.Revision.ModelSettingsRevision != settingsRevision || len(finalView.Evidence) != 1 {
		t.Fatalf("final rebuilt profile=%#v", finalView)
	}
	batchViews, err := profiles.GetProfiles(ctx, captureapp.ProfileBatchQuery{
		WorkspaceID: workspaceID, SourceVersionIDs: []foundation.ID{created.Capture.LatestSourceVersionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batchViews) != 1 || batchViews[0].Profile.ID != profileID ||
		batchViews[0].Revision == nil || batchViews[0].Revision.ID != rebuilt.RevisionID ||
		len(batchViews[0].Evidence) != 1 || batchViews[0].Evidence[0].SourceSpanID != spanID {
		t.Fatalf("batch profile views=%#v", batchViews)
	}
	replayedRebuild, err := rebuildGenerator.Generate(ctx, rebuildRequest)
	if err != nil {
		t.Fatal(err)
	}
	if replayedRebuild != rebuilt || rebuildModel.CallCount() != 1 {
		t.Fatalf("replayed rebuild=%#v rebuilt=%#v model_calls=%d", replayedRebuild, rebuilt, rebuildModel.CallCount())
	}
	var totalRevisions, totalEvidence, totalAttempts, totalModelCalls, oldRevisionEvidence int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_revision WHERE profile_id=$1`, profileID).
		Scan(&totalRevisions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_evidence WHERE revision_id IN (
		SELECT id FROM learning.document_knowledge_profile_revision WHERE profile_id=$1
	)`, profileID).Scan(&totalEvidence); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_attempt WHERE profile_id=$1`, profileID).
		Scan(&totalAttempts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.model_call WHERE model_run_id IN (
		SELECT model_run_id FROM learning.document_knowledge_profile_revision WHERE profile_id=$1
	)`, profileID).Scan(&totalModelCalls); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.document_knowledge_profile_evidence WHERE revision_id=$1`, result.RevisionID).
		Scan(&oldRevisionEvidence); err != nil {
		t.Fatal(err)
	}
	if totalRevisions != 2 || totalEvidence != 2 || totalAttempts != 2 || totalModelCalls != 2 || oldRevisionEvidence != 1 {
		t.Fatalf("revisions=%d evidence=%d attempts=%d model_calls=%d old_evidence=%d",
			totalRevisions, totalEvidence, totalAttempts, totalModelCalls, oldRevisionEvidence)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
		status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		VALUES($1,'capture-profile-unrelated','/tmp/capture-profile-unrelated',repeat('9',64),1,
		'/tmp/capture-profile-unrelated',$2,'inactive','available',NULL,$2,1,$2,$2)`, unrelatedWorkspaceID, rebuildNow); err != nil {
		t.Fatal(err)
	}
	seedEmptyReadyCaptureProfileIndex(t, ctx, pool, unrelatedIndexVersionID, unrelatedWorkspaceID, rebuildNow)
	activateCaptureProfileIndex(t, ctx, pool, unrelatedIndexActivationID, unrelatedWorkspaceID,
		unrelatedIndexVersionID, 2, "", 0, rebuildNow.Add(5*time.Second))
	unchangedView, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil || unchangedView.Profile.Status != capturedomain.ProfileStatusReady || unchangedView.Profile.CurrentRevisionID != rebuilt.RevisionID {
		t.Fatalf("unrelated workspace activation changed profile: view=%#v error=%v", unchangedView, err)
	}

	activateCaptureProfileIndex(t, ctx, pool, baseIndexActivationID, workspaceID,
		rebuildIndexVersionID, 2, "", 0, rebuildNow.Add(6*time.Second))
	sameProjectionView, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil || sameProjectionView.Profile.Status != capturedomain.ProfileStatusReady || sameProjectionView.Profile.CurrentRevisionID != rebuilt.RevisionID {
		t.Fatalf("same projection activation changed profile: view=%#v error=%v", sameProjectionView, err)
	}

	seedAlternateCaptureProfileIndex(t, ctx, pool, alternateIndexVersionID, workspaceID,
		created.Capture.SourceID, created.Capture.LatestSourceVersionID, artifactID, alternateParseProjectionID,
		alternateIngestionAttemptID, alternateSpanID, alternateChunkID, contentHash, content, byteSize,
		rebuildNow.Add(7*time.Second))
	activateCaptureProfileIndex(t, ctx, pool, alternateIndexActivationID, workspaceID,
		alternateIndexVersionID, 2, rebuildIndexVersionID, 3, rebuildNow.Add(9*time.Second))
	parseStaleView, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	parseStaleCapture, err := captureRepository.Get(ctx, workspaceID, created.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parseStaleView.Profile.Status != capturedomain.ProfileStatusStale ||
		parseStaleView.Profile.CurrentRevisionID != rebuilt.RevisionID || parseStaleView.Revision == nil ||
		parseStaleView.Revision.ID != rebuilt.RevisionID || len(parseStaleView.Evidence) != 1 ||
		parseStaleCapture.ProfileStatus != capturedomain.StageStale {
		t.Fatalf("parse dependency activation did not preserve stale profile: view=%#v capture=%#v", parseStaleView, parseStaleCapture)
	}

	interleaveRetry, err := retryService.RetryProfile(ctx, captureapp.ProfileRetryCommand{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
		ExpectedVersion: parseStaleView.Profile.Version, IdempotencyKey: "parse-profile-running-interleave",
	})
	if err != nil {
		t.Fatal(err)
	}
	if interleaveRetry.Replayed || interleaveRetry.View.Profile.Status != capturedomain.ProfileStatusPending ||
		interleaveRetry.View.Profile.CurrentRevisionID != rebuilt.RevisionID {
		t.Fatalf("parse interleave retry=%#v", interleaveRetry)
	}
	interleaveNow := parseStaleCapture.UpdatedAt.Add(2 * time.Second)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{interleaveWorkflowRunID, string(workspaceID), definitionID, interleaveNow}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
			VALUES($1,$2,'capture.process','capture.process','running','{}','capture-profile-interleave',$3,1,$4,$4)`,
			[]any{interleaveNodeRunID, interleaveWorkflowRunID, interleaveNow.Add(time.Minute), interleaveNow}},
		{`INSERT INTO workflow.node_attempt(
			id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,
			model_settings_revision,model_runtime_instance_id
		) VALUES($1,$2,1,1,0,'capture-profile-interleave-delivery','capture-profile-interleave',$3,'running',$4,1,$5)`,
			[]any{interleaveNodeAttemptID, interleaveNodeRunID, interleaveNow.Add(time.Minute), interleaveNow, workerRuntimeID}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	interleaveCapture, interleaveAttempt, err := captureRepository.BeginAttempt(ctx, captureapp.BeginAttemptRequest{
		ID: interleaveCaptureAttemptID, WorkspaceID: workspaceID, CaptureID: created.Capture.ID,
		WorkflowRunID: interleaveWorkflowRunID, AttemptNumber: 1, StartedAt: interleaveNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	interleaveCapture, interleaveAttempt, err = captureRepository.MarkRefreshReady(ctx, captureapp.RefreshCheckpoint{
		Attempt: interleaveAttempt, ExpectedCaptureVersion: interleaveCapture.Version,
		IngestionAttemptID: alternateIngestionAttemptID, ParseProjectionID: alternateParseProjectionID,
		IndexVersionID: alternateIndexVersionID, UpdatedAt: interleaveNow.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	seedReadyCaptureProfileIndex(t, ctx, pool, interleaveOldIndexVersionID, workspaceID, chunkID,
		created.Capture.SourceID, created.Capture.LatestSourceVersionID, parseProjectionID, contentHash, content,
		interleaveNow.Add(2*time.Second))
	interleaveModel := newCaptureProfileBlockingModel(modelStep.Response)
	interleaveGenerator, err := captureprofile.NewGenerator(captureprofile.GeneratorDependencies{
		Repository: profiles, ModelRuns: modelRuns, Model: interleaveModel, Catalog: catalog,
		Scheduler: scheduler, ModelProfileRef: profileRef, IDs: foundation.NewUUIDGenerator(nil),
		Clock: &captureProfileIntegrationClock{next: interleaveNow.Add(4 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	interleaveGeneration := make(chan generationOutcome, 1)
	go func() {
		generated, generateErr := interleaveGenerator.Generate(ctx, captureapp.ProfileGenerationRequest{
			ProfileID: profileID, Capture: interleaveCapture, Attempt: interleaveAttempt,
			ParseProjectionID: alternateParseProjectionID, IndexVersionID: alternateIndexVersionID,
			WorkflowRunID: interleaveWorkflowRunID, NodeRunID: interleaveNodeRunID,
			NodeAttemptID: interleaveNodeAttemptID, ModelSettingsRevision: &settingsRevision,
		})
		interleaveGeneration <- generationOutcome{result: generated, err: generateErr}
	}()
	select {
	case <-interleaveModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("interleaved profile model did not start")
	}
	// Hold both switch rows until completion has locked its attempt and is waiting
	// on the active Index row. This exercises the pre-commit activation window.
	activationTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = activationTx.Rollback(ctx) }()
	if _, err := activationTx.Exec(ctx, `SELECT id FROM retrieval.index_version
		WHERE workspace_id=$1 AND id IN ($2,$3) ORDER BY id FOR UPDATE`, string(workspaceID),
		string(interleaveOldIndexVersionID), string(alternateIndexVersionID)); err != nil {
		t.Fatal(err)
	}
	activationProceed := make(chan struct{})
	activationDone := make(chan error, 1)
	go func() {
		<-activationProceed
		if activationErr := applyCaptureProfileIndexActivation(ctx, activationTx, interleaveOldIndexActivationID,
			workspaceID, interleaveOldIndexVersionID, 2, alternateIndexVersionID, 3,
			interleaveNow.Add(5*time.Second)); activationErr != nil {
			activationDone <- activationErr
			return
		}
		activationDone <- activationTx.Commit(ctx)
	}()
	close(interleaveModel.release)
	if err := waitForCaptureProfileAttemptLock(ctx, pool, profileID); err != nil {
		close(activationProceed)
		t.Fatal(err)
	}
	close(activationProceed)
	if err := <-activationDone; err != nil {
		t.Fatal(err)
	}
	interleaved := <-interleaveGeneration
	if interleaved.err != nil {
		t.Fatal(interleaved.err)
	}
	if interleaved.result.RevisionID == "" || interleaved.result.RevisionID == rebuilt.RevisionID || interleaveModel.CallCount() != 1 {
		t.Fatalf("interleaved generation=%#v model_calls=%d", interleaved, interleaveModel.CallCount())
	}
	interleaveView, err := profiles.GetProfile(ctx, captureapp.ProfileQuery{
		WorkspaceID: workspaceID, SourceVersionID: created.Capture.LatestSourceVersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if interleaveView.Profile.Status != capturedomain.ProfileStatusStale ||
		interleaveView.Profile.CurrentRevisionID != interleaved.result.RevisionID || interleaveView.Revision == nil ||
		interleaveView.Revision.ParseProjectionID != alternateParseProjectionID || len(interleaveView.Evidence) != 1 ||
		interleaveView.Evidence[0].SourceSpanID != alternateSpanID {
		t.Fatalf("interleaved profile view=%#v", interleaveView)
	}
	interleaveCapture, interleaveAttempt, err = captureRepository.CompleteAttempt(ctx, captureapp.ReadyCompletion{
		Attempt: interleaveAttempt, ExpectedCaptureVersion: interleaveCapture.Version,
		ProfileID: profileID, CompletedAt: interleaveNow.Add(7 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if interleaveCapture.ProfileStatus != capturedomain.StageStale || interleaveAttempt.Status != capturedomain.AttemptStatusSucceeded {
		t.Fatalf("interleaved capture=%#v attempt=%#v", interleaveCapture, interleaveAttempt)
	}
}

func activateCaptureProfileModelSettingsRevision(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workerRuntimeID foundation.ID,
) {
	t.Helper()
	insertModelSettingsMigrationRevision(t, ctx, pool)
	const (
		rolloutID    = "68500000-0000-4000-8000-000000000099"
		apiRuntimeID = "68500000-0000-4000-8000-000000000098"
	)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO ops.model_settings_runtime(
			role,instance_id,applied_revision,phase,applied_at,heartbeat_at
		) VALUES('api',$1,0,'active',clock_timestamp(),clock_timestamp()),
			('worker',$2,0,'active',clock_timestamp(),clock_timestamp())`, []any{apiRuntimeID, workerRuntimeID}},
		{`UPDATE ops.model_settings_state SET desired_revision=1,rollout_id=$1,target_revision=1,
			previous_active_revision=0,phase='preparing',lease_expires_at=clock_timestamp()+interval '1 minute',
			last_error_code=NULL,version=version+1,updated_at=clock_timestamp() WHERE singleton=true`, []any{rolloutID}},
		{`INSERT INTO ops.model_settings_rollout_participant(
			rollout_id,role,instance_id,target_revision,phase,heartbeat_at,version
		) VALUES($1,'api',$2,1,'preparing',clock_timestamp(),1),
			($1,'worker',$3,1,'preparing',clock_timestamp(),1)`, []any{rolloutID, apiRuntimeID, workerRuntimeID}},
		{`UPDATE ops.model_settings_rollout_participant SET phase='prepared',prepared_at=clock_timestamp(),
			heartbeat_at=clock_timestamp(),version=version+1 WHERE rollout_id=$1`, []any{rolloutID}},
		{`UPDATE ops.model_settings_state SET phase='arming',lease_expires_at=clock_timestamp()+interval '1 minute',
			version=version+1,updated_at=clock_timestamp() WHERE singleton=true`, nil},
		{`UPDATE ops.model_settings_rollout_participant SET phase='armed',heartbeat_at=clock_timestamp(),
			version=version+1 WHERE rollout_id=$1`, []any{rolloutID}},
		{`UPDATE ops.model_settings_state SET active_revision=target_revision,phase='activating',
			lease_expires_at=clock_timestamp()+interval '1 minute',version=version+1,
			updated_at=clock_timestamp() WHERE singleton=true`, nil},
		{`UPDATE ops.model_settings_runtime SET applied_revision=1,phase='active',rollout_id=NULL,
			applied_at=clock_timestamp(),heartbeat_at=clock_timestamp() WHERE role IN ('api','worker')`, nil},
		{`UPDATE ops.model_settings_rollout_participant SET phase='activated',activated_at=clock_timestamp(),
			heartbeat_at=clock_timestamp(),version=version+1 WHERE rollout_id=$1`, []any{rolloutID}},
		{`UPDATE ops.model_settings_state SET rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,
			phase='idle',lease_expires_at=NULL,last_error_code=NULL,version=version+1,
			updated_at=clock_timestamp() WHERE singleton=true`, nil},
		{`UPDATE ops.model_settings_rollout_participant SET phase='retired',retired_at=clock_timestamp(),
			heartbeat_at=clock_timestamp(),version=version+1 WHERE rollout_id=$1`, []any{rolloutID}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func seedReadyCaptureProfileIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	indexVersionID, workspaceID, chunkID, sourceID, sourceVersionID, parseProjectionID foundation.ID,
	contentHash, content string,
	now time.Time,
) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
			version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,
			source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version
		) VALUES($1,$2,'simple','v1',repeat('c',64),'{}',$3,repeat('f',64),1,$4,'building','["vector"]',
			1,$5,$5,repeat('e',64),1,'text','v1',repeat('a',64),'structure-v1','v1')`,
			[]any{string(indexVersionID), string(workspaceID), "capture-profile:rebuild:" + string(indexVersionID), "capture-profile-rebuild-" + string(indexVersionID), now}},
		{`INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`,
			[]any{string(indexVersionID), string(chunkID), string(workspaceID), contentHash, now}},
		{`INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
		) VALUES($1,$2,$3,$4,$5,'included',$6)`,
			[]any{string(indexVersionID), string(workspaceID), string(sourceID), string(sourceVersionID), string(parseProjectionID), now}},
		{`INSERT INTO retrieval.chunk_projection(
			index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at
		) VALUES($1,$2,$3,to_tsvector('simple',$4::text),cardinality(tsvector_to_array(to_tsvector('simple',$4::text))),
			'ready','disabled',$5,$5)`, []any{string(indexVersionID), string(chunkID), string(workspaceID), content, now}},
		{`UPDATE retrieval.index_version SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`,
			[]any{string(indexVersionID), now.Add(time.Second)}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func seedEmptyReadyCaptureProfileIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	indexVersionID, workspaceID foundation.ID,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
		version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}',$3,repeat('2',64),0,$4,'building','["vector"]',1,$5,$5)`,
		string(indexVersionID), string(workspaceID), "capture-profile:empty:"+string(indexVersionID),
		"capture-profile-empty-"+string(indexVersionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE retrieval.index_version
		SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`, string(indexVersionID), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func seedAlternateCaptureProfileIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	indexVersionID, workspaceID, sourceID, sourceVersionID, artifactID, parseProjectionID,
	ingestionAttemptID, spanID, chunkID foundation.ID,
	contentHash, content string,
	byteSize int64,
	now time.Time,
) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,warnings,created_at
		) VALUES($1,$2,$3,'text','v2',repeat('b',64),'v1',$4,'[]',$5)`,
			[]any{string(parseProjectionID), string(workspaceID), string(artifactID), contentHash, now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			VALUES($1,$2,$3,$4)`, []any{string(sourceVersionID), string(parseProjectionID), string(workspaceID), now}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
			parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at
		) VALUES($1,$2,$3,$4,'chunked','passed','text','v2',repeat('b',64),'structure-v1','v1',$5,1,$6,$6)`,
			[]any{string(ingestionAttemptID), string(workspaceID), string(sourceVersionID), string(parseProjectionID),
				"capture-profile-alternate-" + string(ingestionAttemptID), now}},
		{`INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,
			selector,excerpt_hash,parser_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind":"paragraph"}',$6,'v2','v1',$7)`,
			[]any{string(spanID), string(workspaceID), string(artifactID), string(parseProjectionID), byteSize, contentHash, now}},
		{`INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
			byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
		) VALUES($1,$2,$3,0,'["Java AI"]',$4,$5,$6,$7,$7,'v2','structure-v1','v1',false,'active',$8)`,
			[]any{string(chunkID), string(workspaceID), string(parseProjectionID), content, contentHash, string(spanID), byteSize, now}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
			version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,
			source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version
		) VALUES($1,$2,'simple','v1',repeat('c',64),'{}',$3,repeat('3',64),1,$4,'building','["vector"]',
			1,$5,$5,repeat('4',64),1,'text','v2',repeat('b',64),'structure-v1','v1')`,
			[]any{string(indexVersionID), string(workspaceID), "capture-profile:alternate:" + string(indexVersionID),
				"capture-profile-alternate-" + string(indexVersionID), now}},
		{`INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,0,'v2','structure-v1','v1',$5)`,
			[]any{string(indexVersionID), string(chunkID), string(workspaceID), contentHash, now}},
		{`INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
		) VALUES($1,$2,$3,$4,$5,'included',$6)`,
			[]any{string(indexVersionID), string(workspaceID), string(sourceID), string(sourceVersionID), string(parseProjectionID), now}},
		{`INSERT INTO retrieval.chunk_projection(
			index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at
		) VALUES($1,$2,$3,to_tsvector('simple',$4::text),cardinality(tsvector_to_array(to_tsvector('simple',$4::text))),
			'ready','disabled',$5,$5)`, []any{string(indexVersionID), string(chunkID), string(workspaceID), content, now}},
		{`UPDATE retrieval.index_version SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`,
			[]any{string(indexVersionID), now.Add(time.Second)}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func activateCaptureProfileIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	activationID, workspaceID, targetIndexID foundation.ID,
	targetVersion int64,
	currentIndexID foundation.ID,
	currentVersion int64,
	at time.Time,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applyCaptureProfileIndexActivation(ctx, tx, activationID, workspaceID, targetIndexID,
		targetVersion, currentIndexID, currentVersion, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func applyCaptureProfileIndexActivation(
	ctx context.Context,
	tx pgx.Tx,
	activationID, workspaceID, targetIndexID foundation.ID,
	targetVersion int64,
	currentIndexID foundation.ID,
	currentVersion int64,
	at time.Time,
) error {
	var previousID any
	var previousVersion any
	if currentIndexID != "" {
		previousID = string(currentIndexID)
		previousVersion = currentVersion + 1
	}
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
		id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,
		idempotency_key,reason_code,created_at
	) VALUES($1,'activate',$2,$3,$4,$5,$6,$7,'CAPTURE_PROFILE_TEST',$8)`,
		string(activationID), string(workspaceID), string(targetIndexID), previousID, targetVersion+1, previousVersion,
		"capture-profile-activation-"+string(activationID), at); err != nil {
		return err
	}
	if currentIndexID != "" {
		if tag, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET
			status='retiring',version=version+1,retired_at=$1,updated_at=$1
			WHERE id=$2 AND workspace_id=$3 AND status='active' AND version=$4`,
			at, string(currentIndexID), string(workspaceID), currentVersion); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return errors.New("capture profile current index was not retired")
		}
	}
	if tag, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET
		status='active',version=version+1,activated_at=$1,updated_at=$1
		WHERE id=$2 AND workspace_id=$3 AND status='ready' AND version=$4`,
		at, string(targetIndexID), string(workspaceID), targetVersion); err != nil {
		return err
	} else if tag.RowsAffected() != 1 {
		return errors.New("capture profile target index was not activated")
	}
	return nil
}

func waitForCaptureProfileAttemptLock(ctx context.Context, pool *pgxpool.Pool, profileID foundation.ID) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		var attemptID string
		err = tx.QueryRow(ctx, `SELECT id::text FROM learning.document_knowledge_profile_attempt
			WHERE profile_id=$1 AND status='RUNNING' FOR UPDATE NOWAIT`, string(profileID)).Scan(&attemptID)
		_ = tx.Rollback(ctx)
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "55P03" {
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("profile completion did not lock its running attempt")
}

type captureProfileIntegrationClock struct {
	next time.Time
}

func (clock *captureProfileIntegrationClock) Now() time.Time {
	current := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return current
}

type captureProfileBlockingModel struct {
	mu       sync.Mutex
	once     sync.Once
	response agentapp.ChatResponse
	started  chan struct{}
	release  chan struct{}
	calls    int
}

type captureIntegrationContent struct{}

func (captureIntegrationContent) StageManagedBytes(
	_ context.Context,
	_ foundation.ID,
	sourceRef string,
	content []byte,
	hash string,
) (workspacedomain.ManagedContentStage, error) {
	return workspacedomain.ManagedContentStage{
		ContentHash: hash, ByteSize: int64(len(content)), ManagedLocation: ".knowledge/sources/" + hash,
		StagingLocation: ".knowledge/sources/.staging/" + sourceRef,
	}, nil
}

func (captureIntegrationContent) PublishManagedBytes(
	_ context.Context,
	_ foundation.ID,
	stage workspacedomain.ManagedContentStage,
) (workspacedomain.ContentCapture, error) {
	return workspacedomain.ContentCapture{
		ContentHash: stage.ContentHash, ByteSize: stage.ByteSize,
		ManagedLocation: stage.ManagedLocation, Created: true,
	}, nil
}

func (captureIntegrationContent) DiscardManagedBytes(
	context.Context,
	foundation.ID,
	workspacedomain.ManagedContentStage,
) error {
	return nil
}

func insertCaptureWorkspace(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	workspaceID foundation.ID,
	now time.Time,
) {
	t.Helper()
	if _, err := database.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
		status,availability,availability_reason,availability_checked_at,version,created_at,updated_at
	) VALUES($1,'capture-profile-test','/tmp/capture-profile-test',$2,1,
		'/tmp/capture-profile-test',$3,'inactive','available',NULL,$3,1,$3,$3)`,
		string(workspaceID), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", now); err != nil {
		t.Fatal(err)
	}
}

func insertModelSettingsMigrationRevision(t *testing.T, ctx context.Context, queryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) {
	t.Helper()
	_, err := queryer.Exec(ctx, `INSERT INTO ops.model_settings_revisions(
		revision,chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
		chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
		embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
		embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
		embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
		embedding_max_response_bytes,created_by
	) VALUES(1,'openai-compatible','https://models.example.test/v1','chat-model','2026-07','v1',
		30000000,4194304,4194304,'ollama','http://127.0.0.1:11434','embed-model',768,
		'l2','cosine',128,65536,8388608,30000000,67108864,'migration-test')`)
	if err != nil {
		t.Fatal(err)
	}
}

func newCaptureProfileBlockingModel(response agentapp.ChatResponse) *captureProfileBlockingModel {
	return &captureProfileBlockingModel{
		response: response,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (model *captureProfileBlockingModel) Chat(ctx context.Context, _ agentapp.ChatRequest) (agentapp.ChatResponse, error) {
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	model.once.Do(func() { close(model.started) })
	select {
	case <-model.release:
		return model.response, nil
	case <-ctx.Done():
		return agentapp.ChatResponse{}, ctx.Err()
	}
}

func (model *captureProfileBlockingModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

func TestCaptureProfileRepositoryPreservesContextCauseAndReusesConnection(t *testing.T) {
	withCaptureProfileRepositoryIntegration(t, testCaptureProfileRepositoryPreservesContextCauseAndReusesConnection)
}

func testCaptureProfileRepositoryPreservesContextCauseAndReusesConnection(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	harness captureProfileRepositoryIntegrationHarness,
) {
	query := captureapp.ProfileQuery{
		WorkspaceID:     foundation.ID("68900000-0000-4000-8000-000000000001"),
		SourceVersionID: foundation.ID("68900000-0000-4000-8000-000000000002"),
	}
	cancelCause := errors.New("profile caller stopped waiting")
	canceledCtx, cancel := context.WithCancelCause(ctx)
	cancel(cancelCause)
	view, err := harness.profiles.GetProfile(canceledCtx, query)
	if err == nil || view.Profile.ID != "" || view.Revision != nil || len(view.Evidence) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled profile Get() view=%#v error=%v", view, err)
	}
	if _, isGORM := harness.profiles.(*capturepostgres.GORMProfileRepository); isGORM && !errors.Is(err, cancelCause) {
		t.Fatalf("GORM profile cancellation lost caller cause: %v", err)
	}

	deadlineCause := errors.New("profile caller deadline budget expired")
	deadlineCtx, deadlineCancel := context.WithDeadlineCause(ctx, time.Unix(0, 0), deadlineCause)
	defer deadlineCancel()
	views, err := harness.profiles.GetProfiles(deadlineCtx, captureapp.ProfileBatchQuery{
		WorkspaceID: query.WorkspaceID, SourceVersionIDs: []foundation.ID{query.SourceVersionID},
	})
	if err == nil || len(views) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline profile batch views=%#v error=%v", views, err)
	}
	if _, isGORM := harness.profiles.(*capturepostgres.GORMProfileRepository); isGORM && !errors.Is(err, deadlineCause) {
		t.Fatalf("GORM profile deadline lost caller cause: %v", err)
	}
	var one int
	if err := platform.DB().QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("pool unusable after profile cancellation value=%d error=%v", one, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for platform.DB().Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := platform.DB().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("profile pool acquired connections=%d want=0", acquired)
	}
}

func TestCaptureProfileRepositoryTargetQueryPlansUseDeclaredIndexes(t *testing.T) {
	withCaptureProfileRepositoryIntegration(t, testCaptureProfileRepositoryTargetQueryPlansUseDeclaredIndexes)
}

func testCaptureProfileRepositoryTargetQueryPlansUseDeclaredIndexes(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	harness captureProfileRepositoryIntegrationHarness,
) {
	workspaceID := foundation.ID("69000000-0000-4000-8000-000000000001")
	sourceVersionID := foundation.ID("69000000-0000-4000-8000-000000000002")
	_ = harness
	pool := platform.DB()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		name    string
		sql     string
		args    []any
		indexes []string
	}{
		{
			name:    "profile by source version",
			sql:     `SELECT id FROM learning.document_knowledge_profile WHERE workspace_id=$1 AND source_version_id=$2`,
			args:    []any{string(workspaceID), string(sourceVersionID)},
			indexes: []string{"uq_document_profile_source_version"},
		},
		{
			name:    "profile revision by workspace",
			sql:     `SELECT id FROM learning.document_knowledge_profile_revision WHERE workspace_id=$1 AND id=$2`,
			args:    []any{string(workspaceID), string(sourceVersionID)},
			indexes: []string{"uq_document_profile_revision_workspace", "uq_document_profile_revision_evidence"},
		},
		{
			name:    "active profile attempt",
			sql:     `SELECT id FROM learning.document_knowledge_profile_attempt WHERE profile_id=$1 AND status='RUNNING'`,
			args:    []any{string(workspaceID)},
			indexes: []string{"uq_document_profile_attempt_active"},
		},
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+query.sql, query.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var lines []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			matched := false
			for _, index := range query.indexes {
				if strings.Contains(plan, index) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("query plan missing one of indexes %v:\n%s", query.indexes, plan)
			}
		})
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for pool.Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("profile plan pool acquired connections=%d want=0", acquired)
	}
}
