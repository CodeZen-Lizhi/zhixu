//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSectionGenerationPostgreSQLStartReplayConflictAndContextRebase(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceA := artifactIntegrationID(301)
	workspaceB := artifactIntegrationID(302)
	seedArtifactWorkspace(t, ctx, pool, workspaceA, "artifact-generation-a")
	seedArtifactWorkspace(t, ctx, pool, workspaceB, "artifact-generation-b")

	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceA, 310, now)
	coordinator, _ := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), &generationCitationVerifierFake{})
	command := artifactapplication.StartSectionGenerationCommand{
		WorkspaceID: workspaceA, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
		SectionKey: "second", IdempotencyKey: "generate-second",
	}

	started, err := coordinator.StartSectionGeneration(ctx, command)
	if err != nil {
		t.Fatalf("start generation: %v (cause: %v)", err, errors.Unwrap(err))
	}
	if started.Replayed || started.Generation.Status != artifactapplication.SectionGenerationPending ||
		started.Generation.SourceRevisionID != source.Revision.ID || started.Generation.WorkflowRunID == "" || started.Generation.NodeRunID == "" {
		t.Fatalf("started generation = %#v", started)
	}
	replayed, err := coordinator.StartSectionGeneration(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Generation.ID != started.Generation.ID ||
		replayed.Generation.WorkflowRunID != started.Generation.WorkflowRunID || replayed.Generation.NodeRunID != started.Generation.NodeRunID {
		t.Fatalf("replayed generation = %#v, err = %v", replayed, err)
	}

	conflicting := command
	conflicting.SectionKey = "first"
	if _, err := coordinator.StartSectionGeneration(ctx, conflicting); !artifactIntegrationErrorCode(err, artifactapplication.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same-key conflict error = %v", err)
	}
	otherKey := command
	otherKey.IdempotencyKey = "generate-second-again"
	if _, err := coordinator.StartSectionGeneration(ctx, otherKey); !artifactIntegrationErrorCode(err, artifactapplication.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same-source conflict error = %v", err)
	}
	crossWorkspace := command
	crossWorkspace.WorkspaceID = workspaceB
	crossWorkspace.IdempotencyKey = "cross-workspace"
	if _, err := coordinator.StartSectionGeneration(ctx, crossWorkspace); !artifactIntegrationErrorCode(err, artifactapplication.ErrorCodeNotFound) {
		t.Fatalf("cross-workspace error = %v", err)
	}

	advanced := recordGenerationGapSection(t, source, "first", artifactIntegrationID(320), now.Add(11*time.Minute))
	artifactIntegrationTransition(t, artifactRepository,
		artifactIntegrationBinding(workspaceA, source.Artifact.ID, "record-first", '7', artifactapplication.CommandRecordSection, source.Artifact.Version),
		source, advanced, true, nil, nil)
	crossRevision := command
	crossRevision.ExpectedVersion = advanced.Artifact.Version
	crossRevision.IdempotencyKey = "generate-second-after-revision-advance"
	if _, err := coordinator.StartSectionGeneration(ctx, crossRevision); !artifactIntegrationErrorCode(err, artifactapplication.ErrorCodeIdempotencyConflict) {
		t.Fatalf("cross-revision active section conflict error = %v", err)
	}
	input := artifactworkflow.Input{
		SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: source.Artifact.ID,
		RevisionID: source.Revision.ID, RevisionNo: source.Revision.RevisionNo,
		ArtifactVersion: source.Artifact.Version, SectionKey: command.SectionKey,
	}
	loaded, err := coordinator.LoadGenerationContext(ctx, artifactworkflow.GenerationContextQuery{
		WorkspaceID: workspaceA, WorkflowRunID: started.Generation.WorkflowRunID,
		NodeRunID: started.Generation.NodeRunID, Input: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SourceRevision.ID != source.Revision.ID || loaded.Current.Revision.ID != advanced.Revision.ID ||
		loaded.Current.Artifact.Version != advanced.Artifact.Version || loaded.ProfileRef != generationIntegrationProfile() {
		t.Fatalf("loaded context = %#v", loaded)
	}
}

func TestSectionGenerationPostgreSQLStartRespectsTerminalSourceSlotOwnership(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(451)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-source-slot")
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 460, now)
	coordinator, _ := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), &generationCitationVerifierFake{})
	command := artifactapplication.StartSectionGenerationCommand{
		WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
		SectionKey: "second", IdempotencyKey: "source-slot-first",
	}
	started, err := coordinator.StartSectionGeneration(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	transitionGenerationFailure(t, ctx, pool, started.Generation, artifactapplication.SectionGenerationFailed,
		"non_retryable", "ARTIFACT_GENERATION_FAILED", "generation failed safely", now.Add(11*time.Minute))
	failedReplay, err := coordinator.StartSectionGeneration(ctx, command)
	if err != nil || !failedReplay.Replayed || failedReplay.Generation.ID != started.Generation.ID ||
		failedReplay.Generation.Status != artifactapplication.SectionGenerationFailed || failedReplay.Generation.TerminalAt == nil {
		t.Fatalf("failed generation replay=%#v err=%v", failedReplay, err)
	}

	retryCommand := command
	retryCommand.IdempotencyKey = "source-slot-retry"
	retried, err := coordinator.StartSectionGeneration(ctx, retryCommand)
	if err != nil || retried.Replayed || retried.Generation.Status != artifactapplication.SectionGenerationPending {
		t.Fatalf("safe terminal restart=%#v err=%v", retried, err)
	}
	recoveryAttemptID := artifactIntegrationID(490)
	seedGenerationAttempt(t, ctx, pool, retried.Generation, recoveryAttemptID, 1, now.Add(11*time.Minute+30*time.Second))
	transitionGenerationFailure(t, ctx, pool, retried.Generation, artifactapplication.SectionGenerationRecoveryRequired,
		"manual_recovery", "ARTIFACT_GENERATION_OUTCOME_UNKNOWN", "generation outcome requires recovery", now.Add(12*time.Minute))
	recoveryReplay, err := coordinator.StartSectionGeneration(ctx, retryCommand)
	if err != nil || !recoveryReplay.Replayed || recoveryReplay.Generation.ID != retried.Generation.ID ||
		recoveryReplay.Generation.Status != artifactapplication.SectionGenerationRecoveryRequired {
		t.Fatalf("recovery generation replay=%#v err=%v", recoveryReplay, err)
	}
	_, found, err := coordinator.Lookup(ctx, artifactworkflow.FinalizationLookup{
		WorkspaceID: workspaceID, WorkflowRunID: retried.Generation.WorkflowRunID,
		NodeRunID: retried.Generation.NodeRunID, NodeAttemptID: recoveryAttemptID,
		Input: artifactworkflow.Input{
			SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: source.Artifact.ID,
			RevisionID: source.Revision.ID, RevisionNo: source.Revision.RevisionNo,
			ArtifactVersion: source.Artifact.Version, SectionKey: "second",
		},
	})
	if found || !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeFinalizationUnknown) {
		t.Fatalf("recovery lookup found=%t err=%v", found, err)
	}
	blocked := command
	blocked.IdempotencyKey = "source-slot-blocked"
	if _, err := coordinator.StartSectionGeneration(ctx, blocked); !artifactIntegrationErrorCode(err, artifactapplication.ErrorCodeIdempotencyConflict) {
		t.Fatalf("recovery source slot error=%v", err)
	}
	var generations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_section_generation WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(source.Artifact.ID)).Scan(&generations); err != nil || generations != 2 {
		t.Fatalf("generation count=%d err=%v", generations, err)
	}
}

func TestSectionGenerationPostgreSQLReadSnapshotRecoversAuthoritativeCurrentOutline(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(1701)
	otherWorkspaceID := artifactIntegrationID(1702)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-read")
	seedArtifactWorkspace(t, ctx, pool, otherWorkspaceID, "artifact-generation-read-other")
	now := time.Date(2026, 7, 26, 15, 30, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 1710, now)

	readDB := &generationReadOptionsDB{Pool: pool}
	readRepository, err := NewRepository(readDB)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := readRepository.ListSectionGenerations(ctx, workspaceID, source.Artifact.ID)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.State.Revision.ID != source.Revision.ID {
		t.Fatalf("empty snapshot=%#v err=%v", empty, err)
	}
	if len(readDB.options) != 1 || readDB.options[0].IsoLevel != pgx.RepeatableRead || readDB.options[0].AccessMode != pgx.ReadOnly {
		t.Fatalf("snapshot options=%#v", readDB.options)
	}
	for _, lookup := range []struct {
		workspaceID foundation.ID
		artifactID  foundation.ID
	}{
		{workspaceID: otherWorkspaceID, artifactID: source.Artifact.ID},
		{workspaceID: workspaceID, artifactID: artifactIntegrationID(1799)},
	} {
		if _, queryErr := readRepository.ListSectionGenerations(ctx, lookup.workspaceID, lookup.artifactID); !artifactIntegrationErrorCode(queryErr, artifactapplication.ErrorCodeNotFound) {
			t.Fatalf("opaque lookup workspace=%s artifact=%s err=%v", lookup.workspaceID, lookup.artifactID, queryErr)
		}
	}

	coordinator, _ := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), &generationCitationVerifierFake{})
	start := func(sectionKey, key string) artifactapplication.SectionGeneration {
		t.Helper()
		result, startErr := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
			WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
			SectionKey: sectionKey, IdempotencyKey: key,
		})
		if startErr != nil {
			t.Fatalf("start %s: %v", key, startErr)
		}
		return result.Generation
	}

	firstFailed := start("first", "read-first-failed")
	transitionGenerationFailure(t, ctx, pool, firstFailed, artifactapplication.SectionGenerationFailed,
		"non_retryable", "ARTIFACT_GENERATION_FAILED", "first generation failed", now.Add(11*time.Minute))
	firstCancelled := start("first", "read-first-cancelled")
	transitionGenerationFailure(t, ctx, pool, firstCancelled, artifactapplication.SectionGenerationCancelled,
		"cancelled", "ARTIFACT_GENERATION_CANCELLED", "first generation cancelled", now.Add(12*time.Minute))

	secondFailed := start("second", "read-second-failed")
	transitionGenerationFailure(t, ctx, pool, secondFailed, artifactapplication.SectionGenerationFailed,
		"non_retryable", "ARTIFACT_GENERATION_FAILED", "second generation failed", now.Add(13*time.Minute))
	secondPending := start("second", "read-second-pending")

	snapshot, err := readRepository.ListSectionGenerations(ctx, workspaceID, source.Artifact.ID)
	if err != nil || len(snapshot.Items) != 2 {
		t.Fatalf("authoritative snapshot=%#v err=%v", snapshot, err)
	}
	if snapshot.Items[0].SectionKey != "first" || snapshot.Items[0].ID != firstCancelled.ID ||
		snapshot.Items[0].Status != artifactapplication.SectionGenerationCancelled ||
		snapshot.Items[1].SectionKey != "second" || snapshot.Items[1].ID != secondPending.ID ||
		snapshot.Items[1].Status != artifactapplication.SectionGenerationPending {
		t.Fatalf("outline/latest/active selection=%#v", snapshot.Items)
	}

	transitionGenerationFailure(t, ctx, pool, secondPending, artifactapplication.SectionGenerationRecoveryRequired,
		"manual_recovery", "ARTIFACT_GENERATION_OUTCOME_UNKNOWN", "second generation requires recovery", now.Add(14*time.Minute))
	advanced := recordGenerationGapSection(t, source, "first", artifactIntegrationID(1750), now.Add(15*time.Minute))
	artifactIntegrationTransition(t, artifactRepository,
		artifactIntegrationBinding(workspaceID, source.Artifact.ID, "read-record-first", '6', artifactapplication.CommandRecordSection, source.Artifact.Version),
		source, advanced, true, nil, nil)

	snapshot, err = readRepository.ListSectionGenerations(ctx, workspaceID, source.Artifact.ID)
	if err != nil || len(snapshot.Items) != 2 || snapshot.Items[0].ID != firstCancelled.ID ||
		snapshot.Items[0].Status != artifactapplication.SectionGenerationCancelled ||
		snapshot.Items[1].ID != secondPending.ID || snapshot.Items[1].Status != artifactapplication.SectionGenerationRecoveryRequired ||
		snapshot.Items[1].SourceRevisionID != source.Revision.ID || snapshot.State.Revision.ID != advanced.Revision.ID {
		t.Fatalf("cross-revision recovery snapshot=%#v err=%v", snapshot, err)
	}

	complete := recordGenerationGapSection(t, advanced, "second", artifactIntegrationID(1751), now.Add(16*time.Minute))
	artifactIntegrationTransition(t, artifactRepository,
		artifactIntegrationBinding(workspaceID, source.Artifact.ID, "read-record-second", '7', artifactapplication.CommandRecordSection, advanced.Artifact.Version),
		advanced, complete, true, nil, nil)
	snapshot, err = readRepository.ListSectionGenerations(ctx, workspaceID, source.Artifact.ID)
	if err != nil || snapshot.Items == nil || len(snapshot.Items) != 0 || snapshot.State.Artifact.Status != artifactdomain.StatusDraft {
		t.Fatalf("completed-section exclusion snapshot=%#v err=%v", snapshot, err)
	}
}

func TestSectionGenerationPostgreSQLFinalizeReplayAndCrossAttemptLookup(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(501)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-finalize")
	now := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 510, now)
	coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), &generationCitationVerifierFake{})
	started, err := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
		WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
		SectionKey: "second", IdempotencyKey: "finalize-second",
	})
	if err != nil {
		t.Fatal(err)
	}
	attemptID := artifactIntegrationID(530)
	seedGenerationAttempt(t, ctx, pool, started.Generation, attemptID, 1, now.Add(11*time.Minute))
	run := seedGenerationModelRun(t, ctx, pool, agentRepository, started.Generation, attemptID, artifactIntegrationID(531), artifactIntegrationID(532), now.Add(12*time.Minute))
	input := artifactworkflow.Input{
		SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: source.Artifact.ID,
		RevisionID: source.Revision.ID, RevisionNo: source.Revision.RevisionNo,
		ArtifactVersion: source.Artifact.Version, SectionKey: "second",
	}
	proposal := artifactworkflow.SectionProposal{
		SectionKey: "second", Title: "Second", Content: "", Citations: []agentdomain.Citation{},
		Coverage: artifactdomain.Coverage{SectionKey: "second", Status: artifactdomain.CoverageGap, Gaps: []artifactdomain.Gap{{Code: "NO_SOURCE", Description: "no approved source"}}},
		Metadata: artifactdomain.GenerationMetadata{
			PromptVersion: artifactworkflow.PromptRef().Version, ModelVersion: run.Model.ModelVersion,
			WorkflowDefinitionVersion: "1", SchemaVersion: run.Schema.Version,
		},
	}
	command := artifactworkflow.FinalizeSectionCommand{
		FinalizationLookup: artifactworkflow.FinalizationLookup{
			WorkspaceID: workspaceID, WorkflowRunID: started.Generation.WorkflowRunID,
			NodeRunID: started.Generation.NodeRunID, NodeAttemptID: attemptID, Input: input,
		},
		ModelRunID: run.ID, ExpectedModelRunVersion: run.Version, Proposal: proposal,
	}
	receipt, replayed, err := coordinator.Finalize(ctx, command)
	if err != nil || replayed {
		t.Fatalf("finalize receipt=%#v replayed=%t err=%v", receipt, replayed, err)
	}
	if receipt.ArtifactID != source.Artifact.ID || receipt.BaseRevisionID != source.Revision.ID ||
		receipt.ModelRunID != run.ID || receipt.SectionKey != "second" || receipt.RevisionNo != source.Revision.RevisionNo+1 {
		t.Fatalf("receipt=%#v", receipt)
	}
	persisted, err := artifactRepository.Get(ctx, workspaceID, source.Artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision.ID != receipt.RevisionID || persisted.Artifact.Version != receipt.ArtifactVersion ||
		len(persisted.Revision.Sections) != 1 || persisted.Revision.Sections[0].Key != "second" || persisted.Revision.CreatedBy != artifactdomain.CreatorAgent {
		t.Fatalf("persisted artifact=%#v", persisted)
	}
	record, err := agentRepository.GetModelRun(ctx, workspaceID, run.ID)
	if err != nil || record.Run.Status != agentdomain.ModelRunSucceeded || record.Run.FinalResultType != agentdomain.ResultTypeArtifactSection {
		t.Fatalf("terminal model run=%#v err=%v", record, err)
	}
	replayedReceipt, replayed, err := coordinator.Finalize(ctx, command)
	if err != nil || !replayed || replayedReceipt != receipt {
		t.Fatalf("finalize replay receipt=%#v replayed=%t err=%v", replayedReceipt, replayed, err)
	}

	newAttemptID := artifactIntegrationID(533)
	reclaimGenerationAttempt(t, ctx, pool, started.Generation.NodeRunID, attemptID, newAttemptID, now.Add(14*time.Minute))
	lookup := command.FinalizationLookup
	lookup.NodeAttemptID = newAttemptID
	lookedUp, found, err := coordinator.Lookup(ctx, lookup)
	if err != nil || !found || lookedUp != receipt {
		t.Fatalf("cross-attempt lookup receipt=%#v found=%t err=%v", lookedUp, found, err)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(source.Artifact.ID)).Scan(&revisions); err != nil || revisions != 4 {
		t.Fatalf("revision count=%d err=%v", revisions, err)
	}
}

func TestSectionGenerationPostgreSQLRejectsForgedEvidenceWithoutSplitState(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(601)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-evidence")
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 610, now)
	verifier := &generationCitationVerifierFake{}
	coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), verifier)
	started, err := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
		WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
		SectionKey: "second", IdempotencyKey: "evidence-second",
	})
	if err != nil {
		t.Fatal(err)
	}
	attemptID := artifactIntegrationID(630)
	seedGenerationAttempt(t, ctx, pool, started.Generation, attemptID, 1, now.Add(11*time.Minute))
	run := seedGenerationModelRun(t, ctx, pool, agentRepository, started.Generation, attemptID, artifactIntegrationID(631), artifactIntegrationID(632), now.Add(12*time.Minute))
	input := artifactworkflow.Input{
		SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: source.Artifact.ID,
		RevisionID: source.Revision.ID, RevisionNo: source.Revision.RevisionNo,
		ArtifactVersion: source.Artifact.Version, SectionKey: "second",
	}
	citation := agentdomain.Citation{
		ID: "citation-001", WorkspaceID: artifactIntegrationID(699), IndexVersionID: run.Retrieval.IndexVersionID,
		ChunkID: artifactIntegrationID(640), SourceVersionID: artifactIntegrationID(641), SourceSpanID: artifactIntegrationID(642),
	}
	proposal := artifactworkflow.SectionProposal{
		SectionKey: "second", Title: "Second", Content: "approved content", Citations: []agentdomain.Citation{citation},
		Coverage: artifactdomain.Coverage{SectionKey: "second", Status: artifactdomain.CoverageCovered, Gaps: []artifactdomain.Gap{}},
		Metadata: artifactdomain.GenerationMetadata{
			PromptVersion: run.Prompt.Version, ModelVersion: run.Model.ModelVersion,
			WorkflowDefinitionVersion: "1", SchemaVersion: run.Schema.Version,
		},
	}
	command := artifactworkflow.FinalizeSectionCommand{
		FinalizationLookup: artifactworkflow.FinalizationLookup{
			WorkspaceID: workspaceID, WorkflowRunID: started.Generation.WorkflowRunID,
			NodeRunID: started.Generation.NodeRunID, NodeAttemptID: attemptID, Input: input,
		},
		ModelRunID: run.ID, ExpectedModelRunVersion: run.Version, Proposal: proposal,
	}
	if _, _, err := coordinator.Finalize(ctx, command); !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeEvidenceInvalid) {
		t.Fatalf("cross-workspace citation error=%v", err)
	}
	if verifier.calls != 0 {
		t.Fatalf("cross-workspace citation reached verifier calls=%d", verifier.calls)
	}

	command.Proposal.Citations[0].WorkspaceID = workspaceID
	verifier.results = []artifactdomain.Citation{{
		SourceVersionID: command.Proposal.Citations[0].SourceVersionID,
		SourceSpanID:    artifactIntegrationID(643), VerifiedContentHash: artifactIntegrationHash('d'),
		Excerpt: "drifted span", Verified: true,
	}}
	if _, _, err := coordinator.Finalize(ctx, command); !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeEvidenceInvalid) {
		t.Fatalf("drifted verifier tuple error=%v", err)
	}
	verifier.err = foundation.NewError(foundation.ErrorDependencyUnavailable, "RETRIEVAL_EVIDENCE_UNAVAILABLE", true, errors.New("temporary evidence failure"))
	if _, _, err := coordinator.Finalize(ctx, command); !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeEvidenceInvalid) {
		t.Fatalf("mapped verifier error=%v", err)
	} else {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || !classified.Retryable {
			t.Fatalf("mapped verifier classification=%#v", classified)
		}
	}
	verifier.err = nil

	command.Proposal.Metadata.ModelVersion = "forged-model-version"
	if _, _, err := coordinator.Finalize(ctx, command); !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeOutputInvalid) {
		t.Fatalf("forged metadata error=%v", err)
	}
	command.Proposal.Metadata.ModelVersion = run.Model.ModelVersion
	verifier.results[0].SourceSpanID = command.Proposal.Citations[0].SourceSpanID
	verifier.results[0].Excerpt = "approved source"

	pendingState, err := artifactRepository.Get(ctx, workspaceID, source.Artifact.ID)
	if err != nil || pendingState.Revision.ID != source.Revision.ID || pendingState.Artifact.Version != source.Artifact.Version {
		t.Fatalf("artifact changed after rejected finalization state=%#v err=%v", pendingState, err)
	}
	pendingRun, err := agentRepository.GetModelRun(ctx, workspaceID, run.ID)
	if err != nil || pendingRun.Run.Status != agentdomain.ModelRunRunning {
		t.Fatalf("model run changed after rejected finalization run=%#v err=%v", pendingRun, err)
	}
	generation, found, err := loadSectionGenerationByKey(ctx, pool, workspaceID, "evidence-second", false)
	if err != nil || !found || generation.Status != artifactapplication.SectionGenerationPending {
		t.Fatalf("generation changed after rejected finalization generation=%#v found=%t err=%v", generation, found, err)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(source.Artifact.ID)).Scan(&revisions); err != nil || revisions != 3 {
		t.Fatalf("split revision count=%d err=%v", revisions, err)
	}

	receipt, replayed, err := coordinator.Finalize(ctx, command)
	if err != nil || replayed {
		t.Fatalf("valid evidence finalize receipt=%#v replayed=%t err=%v", receipt, replayed, err)
	}
	finalState, err := artifactRepository.Get(ctx, workspaceID, source.Artifact.ID)
	if err != nil || len(finalState.Revision.Sections) != 1 || len(finalState.Revision.Sections[0].Citations) != 1 ||
		finalState.Revision.Sections[0].Citations[0].SourceSpanID != command.Proposal.Citations[0].SourceSpanID {
		t.Fatalf("verified citation state=%#v err=%v", finalState, err)
	}
	verificationCalls := verifier.calls
	verifier.err = errors.New("completed replay must not reverify evidence")
	replayedReceipt, replayed, err := coordinator.Finalize(ctx, command)
	if err != nil || !replayed || replayedReceipt != receipt || verifier.calls != verificationCalls {
		t.Fatalf("covered replay receipt=%#v replayed=%t calls=%d want_calls=%d err=%v", replayedReceipt, replayed, verifier.calls, verificationCalls, err)
	}
}

func TestSectionGenerationPostgreSQLEvidenceVerificationDoesNotHoldFinalizationRows(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(651)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-verifier-locks")
	now := time.Date(2026, 7, 26, 17, 30, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 660, now)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	verifier := &generationCitationVerifierFake{entered: entered, release: release}
	coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), verifier)
	started, err := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
		WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
		SectionKey: "second", IdempotencyKey: "verifier-locks-second",
	})
	if err != nil {
		t.Fatal(err)
	}
	attemptID := artifactIntegrationID(680)
	seedGenerationAttempt(t, ctx, pool, started.Generation, attemptID, 1, now.Add(11*time.Minute))
	run := seedGenerationModelRun(t, ctx, pool, agentRepository, started.Generation, attemptID, artifactIntegrationID(681), artifactIntegrationID(682), now.Add(12*time.Minute))
	citation := agentdomain.Citation{
		ID: "citation-001", WorkspaceID: workspaceID, IndexVersionID: run.Retrieval.IndexVersionID,
		ChunkID: artifactIntegrationID(683), SourceVersionID: artifactIntegrationID(684), SourceSpanID: artifactIntegrationID(685),
	}
	verifier.results = []artifactdomain.Citation{{
		SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		VerifiedContentHash: artifactIntegrationHash('e'), Excerpt: "approved evidence", Verified: true,
	}}
	command := artifactworkflow.FinalizeSectionCommand{
		FinalizationLookup: artifactworkflow.FinalizationLookup{
			WorkspaceID: workspaceID, WorkflowRunID: started.Generation.WorkflowRunID,
			NodeRunID: started.Generation.NodeRunID, NodeAttemptID: attemptID,
			Input: artifactworkflow.Input{
				SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: source.Artifact.ID,
				RevisionID: source.Revision.ID, RevisionNo: source.Revision.RevisionNo,
				ArtifactVersion: source.Artifact.Version, SectionKey: "second",
			},
		},
		ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
		Proposal: artifactworkflow.SectionProposal{
			SectionKey: "second", Title: "Second", Content: "approved content", Citations: []agentdomain.Citation{citation},
			Coverage: artifactdomain.Coverage{SectionKey: "second", Status: artifactdomain.CoverageCovered, Gaps: []artifactdomain.Gap{}},
			Metadata: artifactdomain.GenerationMetadata{
				PromptVersion: run.Prompt.Version, ModelVersion: run.Model.ModelVersion,
				WorkflowDefinitionVersion: "1", SchemaVersion: run.Schema.Version,
			},
		},
	}
	type finalizeResult struct {
		receipt artifactworkflow.OutputReceipt
		err     error
	}
	result := make(chan finalizeResult, 1)
	go func() {
		receipt, _, finalizeErr := coordinator.Finalize(ctx, command)
		result <- finalizeResult{receipt: receipt, err: finalizeErr}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("citation verifier was not entered")
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		name  string
		query string
		id    foundation.ID
	}{
		{name: "generation", query: `SELECT id::text FROM learning.artifact_section_generation WHERE id=$1 FOR UPDATE NOWAIT`, id: started.Generation.ID},
		{name: "artifact", query: `SELECT id::text FROM learning.artifact WHERE id=$1 FOR UPDATE NOWAIT`, id: source.Artifact.ID},
		{name: "model run", query: `SELECT id::text FROM agent.model_run WHERE id=$1 FOR UPDATE NOWAIT`, id: run.ID},
	} {
		var lockedID string
		if err := lockTx.QueryRow(ctx, target.query, string(target.id)).Scan(&lockedID); err != nil {
			_ = lockTx.Rollback(ctx)
			t.Fatalf("%s row remained locked during evidence verification: %v", target.name, err)
		}
	}
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	released = true
	select {
	case finalized := <-result:
		if finalized.err != nil || finalized.receipt.ModelRunID != run.ID {
			t.Fatalf("finalize after verifier release receipt=%#v err=%v", finalized.receipt, finalized.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("finalization did not finish after evidence verification")
	}
}

func TestSectionGenerationPostgreSQLRebasesTwoSectionsFinalizedInReverseOrder(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(701)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-rebase")
	now := time.Date(2026, 7, 26, 18, 0, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 710, now)
	coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), &generationCitationVerifierFake{})

	start := func(key string) artifactapplication.SectionGeneration {
		result, err := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
			WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
			SectionKey: key, IdempotencyKey: "rebase-" + key,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.Generation
	}
	firstGeneration := start("first")
	secondGeneration := start("second")
	firstAttempt, secondAttempt := artifactIntegrationID(730), artifactIntegrationID(731)
	seedGenerationAttempt(t, ctx, pool, firstGeneration, firstAttempt, 1, now.Add(11*time.Minute))
	seedGenerationAttempt(t, ctx, pool, secondGeneration, secondAttempt, 1, now.Add(12*time.Minute))
	firstRun := seedGenerationModelRun(t, ctx, pool, agentRepository, firstGeneration, firstAttempt, artifactIntegrationID(732), artifactIntegrationID(733), now.Add(13*time.Minute))
	secondRun := seedGenerationModelRun(t, ctx, pool, agentRepository, secondGeneration, secondAttempt, artifactIntegrationID(734), artifactIntegrationID(735), now.Add(14*time.Minute))

	finalize := func(generation artifactapplication.SectionGeneration, attemptID foundation.ID, run agentdomain.ModelRun, key, title string) artifactworkflow.OutputReceipt {
		receipt, replayed, err := coordinator.Finalize(ctx, artifactworkflow.FinalizeSectionCommand{
			FinalizationLookup: artifactworkflow.FinalizationLookup{
				WorkspaceID: workspaceID, WorkflowRunID: generation.WorkflowRunID, NodeRunID: generation.NodeRunID,
				NodeAttemptID: attemptID, Input: artifactworkflow.Input{
					SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: source.Artifact.ID,
					RevisionID: source.Revision.ID, RevisionNo: source.Revision.RevisionNo,
					ArtifactVersion: source.Artifact.Version, SectionKey: key,
				},
			},
			ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
			Proposal: artifactworkflow.SectionProposal{
				SectionKey: key, Title: title, Content: "", Citations: []agentdomain.Citation{},
				Coverage: artifactdomain.Coverage{SectionKey: key, Status: artifactdomain.CoverageGap, Gaps: []artifactdomain.Gap{{Code: "NO_SOURCE", Description: "no approved source"}}},
				Metadata: artifactdomain.GenerationMetadata{
					PromptVersion: run.Prompt.Version, ModelVersion: run.Model.ModelVersion,
					WorkflowDefinitionVersion: "1", SchemaVersion: run.Schema.Version,
				},
			},
		})
		if err != nil || replayed {
			t.Fatalf("finalize %s receipt=%#v replayed=%t err=%v", key, receipt, replayed, err)
		}
		return receipt
	}
	secondReceipt := finalize(secondGeneration, secondAttempt, secondRun, "second", "Second")
	firstReceipt := finalize(firstGeneration, firstAttempt, firstRun, "first", "First")
	if secondReceipt.BaseRevisionID != source.Revision.ID || firstReceipt.BaseRevisionID != secondReceipt.RevisionID ||
		firstReceipt.RevisionNo != secondReceipt.RevisionNo+1 || firstReceipt.ArtifactVersion != secondReceipt.ArtifactVersion+1 {
		t.Fatalf("second=%#v first=%#v", secondReceipt, firstReceipt)
	}
	finalState, err := artifactRepository.Get(ctx, workspaceID, source.Artifact.ID)
	_, hasFirst := revisionSectionByKey(finalState.Revision, "first")
	_, hasSecond := revisionSectionByKey(finalState.Revision, "second")
	if err != nil || finalState.Artifact.Status != artifactdomain.StatusDraft || len(finalState.Revision.Sections) != 2 || !hasFirst || !hasSecond {
		t.Fatalf("rebased final state=%#v err=%v", finalState, err)
	}
}

func TestSectionGenerationPostgreSQLRejectsMissingSuccessfulCallAndTargetDrift(t *testing.T) {
	t.Run("missing successful call", func(t *testing.T) {
		ctx := context.Background()
		artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
		workspaceID := artifactIntegrationID(801)
		seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-no-call")
		now := time.Date(2026, 7, 26, 19, 0, 0, 0, time.UTC)
		source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 810, now)
		coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), &generationCitationVerifierFake{})
		started, err := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
			WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
			SectionKey: "second", IdempotencyKey: "no-call-second",
		})
		if err != nil {
			t.Fatal(err)
		}
		attemptID := artifactIntegrationID(830)
		seedGenerationAttempt(t, ctx, pool, started.Generation, attemptID, 1, now.Add(11*time.Minute))
		run := seedGenerationRunningModelRun(t, ctx, pool, agentRepository, started.Generation, attemptID, artifactIntegrationID(831), now.Add(12*time.Minute))
		command := generationGapFinalizeCommand(source, started.Generation, attemptID, run, "second", "Second")
		if _, _, err := coordinator.Finalize(ctx, command); !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeOutputInvalid) {
			t.Fatalf("missing successful call error=%v", err)
		}
		assertGenerationStillPending(t, ctx, pool, artifactRepository, agentRepository, source, started.Generation, run)
	})

	t.Run("target section drift", func(t *testing.T) {
		ctx := context.Background()
		artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
		workspaceID := artifactIntegrationID(901)
		seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-target-drift")
		now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
		source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 910, now)
		coordinator, agentRepository := newGenerationIntegrationCoordinator(t, pool, now.Add(10*time.Minute), &generationCitationVerifierFake{})
		started, err := coordinator.StartSectionGeneration(ctx, artifactapplication.StartSectionGenerationCommand{
			WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
			SectionKey: "second", IdempotencyKey: "drift-second",
		})
		if err != nil {
			t.Fatal(err)
		}
		drifted := recordGenerationGapSection(t, source, "second", artifactIntegrationID(920), now.Add(11*time.Minute))
		artifactIntegrationTransition(t, artifactRepository,
			artifactIntegrationBinding(workspaceID, source.Artifact.ID, "manual-second", 'e', artifactapplication.CommandRecordSection, source.Artifact.Version),
			source, drifted, true, nil, nil)
		attemptID := artifactIntegrationID(930)
		seedGenerationAttempt(t, ctx, pool, started.Generation, attemptID, 1, now.Add(12*time.Minute))
		run := seedGenerationModelRun(t, ctx, pool, agentRepository, started.Generation, attemptID, artifactIntegrationID(931), artifactIntegrationID(932), now.Add(13*time.Minute))
		command := generationGapFinalizeCommand(source, started.Generation, attemptID, run, "second", "Second")
		if _, _, err := coordinator.Finalize(ctx, command); !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeContextInvalid) {
			t.Fatalf("target drift error=%v", err)
		}
		persisted, err := artifactRepository.Get(ctx, workspaceID, source.Artifact.ID)
		if err != nil || persisted.Revision.ID != drifted.Revision.ID || persisted.Artifact.Version != drifted.Artifact.Version {
			t.Fatalf("drifted state changed persisted=%#v err=%v", persisted, err)
		}
		generation, found, err := loadSectionGenerationByKey(ctx, pool, workspaceID, started.Generation.IdempotencyKey, false)
		if err != nil || !found || generation.Status != artifactapplication.SectionGenerationPending {
			t.Fatalf("drifted generation=%#v found=%t err=%v", generation, found, err)
		}
		record, err := agentRepository.GetModelRun(ctx, workspaceID, run.ID)
		if err != nil || record.Run.Status != agentdomain.ModelRunRunning {
			t.Fatalf("drifted run=%#v err=%v", record, err)
		}
	})
}

func TestSectionGenerationPostgreSQLRecoversStartAndFinalizeCommitResponseLoss(t *testing.T) {
	ctx := context.Background()
	artifactRepository, pool := newArtifactIntegrationRepository(t, ctx)
	workspaceID := artifactIntegrationID(1001)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-generation-commit-loss")
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	source := seedGeneratingArtifact(t, artifactRepository, workspaceID, 1010, now)
	lossDB := generationCommitResponseLossDB{Pool: pool}
	lossCoordinator, agentRepository := newGenerationIntegrationCoordinatorWithDB(
		t, pool, lossDB, now.Add(10*time.Minute), &generationCitationVerifierFake{},
	)
	startCommand := artifactapplication.StartSectionGenerationCommand{
		WorkspaceID: workspaceID, ArtifactID: source.Artifact.ID, ExpectedVersion: source.Artifact.Version,
		SectionKey: "second", IdempotencyKey: "commit-loss-second",
	}
	started, err := lossCoordinator.StartSectionGeneration(ctx, startCommand)
	if err != nil || started.Replayed || started.Generation.Status != artifactapplication.SectionGenerationPending {
		t.Fatalf("recovered start=%#v err=%v", started, err)
	}
	normalCoordinator, _ := newGenerationIntegrationCoordinator(t, pool, now.Add(11*time.Minute), &generationCitationVerifierFake{})
	replayedStart, err := normalCoordinator.StartSectionGeneration(ctx, startCommand)
	if err != nil || !replayedStart.Replayed || replayedStart.Generation.ID != started.Generation.ID {
		t.Fatalf("replayed recovered start=%#v err=%v", replayedStart, err)
	}

	attemptID := artifactIntegrationID(1030)
	seedGenerationAttempt(t, ctx, pool, started.Generation, attemptID, 1, now.Add(12*time.Minute))
	run := seedGenerationModelRun(t, ctx, pool, agentRepository, started.Generation, attemptID, artifactIntegrationID(1031), artifactIntegrationID(1032), now.Add(13*time.Minute))
	command := generationGapFinalizeCommand(source, started.Generation, attemptID, run, "second", "Second")
	if _, _, err := lossCoordinator.Finalize(ctx, command); !artifactIntegrationErrorCode(err, artifactworkflow.ErrorCodeFinalizationUnknown) {
		t.Fatalf("finalize response-loss error=%v", err)
	}
	receipt, found, err := normalCoordinator.Lookup(ctx, command.FinalizationLookup)
	if err != nil || !found || receipt.ModelRunID != run.ID || receipt.ArtifactID != source.Artifact.ID || receipt.SectionKey != "second" {
		t.Fatalf("recovered finalize receipt=%#v found=%t err=%v", receipt, found, err)
	}
	var revisions, calls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), string(source.Artifact.ID)).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.model_call WHERE model_run_id=$1`, string(run.ID)).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if revisions != 4 || calls != 1 {
		t.Fatalf("response-loss revisions=%d calls=%d", revisions, calls)
	}
}

func newGenerationIntegrationCoordinator(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
	verifier artifactapplication.CitationVerifier,
) (*SectionGenerationRepository, *agentpostgres.Repository) {
	return newGenerationIntegrationCoordinatorWithDB(t, pool, pool, now, verifier)
}

func newGenerationIntegrationCoordinatorWithDB(
	t *testing.T,
	pool *pgxpool.Pool,
	db DB,
	now time.Time,
	verifier artifactapplication.CitationVerifier,
) (*SectionGenerationRepository, *agentpostgres.Repository) {
	t.Helper()
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	agentRepository, err := agentpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewSectionGenerationRepository(
		db, runtime, agentRepository, verifier,
		&generationIntegrationIDs{next: 400}, foundation.FixedClock{Value: now}, generationIntegrationProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, agentRepository
}

type generationCommitResponseLossDB struct{ Pool *pgxpool.Pool }

type generationReadOptionsDB struct {
	Pool    *pgxpool.Pool
	options []pgx.TxOptions
}

func (db *generationReadOptionsDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return db.Pool.Exec(ctx, sql, arguments...)
}

func (db *generationReadOptionsDB) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	return db.Pool.Query(ctx, sql, arguments...)
}

func (db *generationReadOptionsDB) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	return db.Pool.QueryRow(ctx, sql, arguments...)
}

func (db *generationReadOptionsDB) Begin(ctx context.Context) (pgx.Tx, error) {
	return db.Pool.Begin(ctx)
}

func (db *generationReadOptionsDB) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	db.options = append(db.options, options)
	return db.Pool.BeginTx(ctx, options)
}

func (db generationCommitResponseLossDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return db.Pool.Exec(ctx, sql, arguments...)
}

func (db generationCommitResponseLossDB) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	return db.Pool.Query(ctx, sql, arguments...)
}

func (db generationCommitResponseLossDB) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	return db.Pool.QueryRow(ctx, sql, arguments...)
}

func (db generationCommitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return generationCommitResponseLossTx{Tx: tx}, nil
}

func (db generationCommitResponseLossDB) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := db.Pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return generationCommitResponseLossTx{Tx: tx}, nil
}

type generationCommitResponseLossTx struct{ pgx.Tx }

func (tx generationCommitResponseLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("simulated commit response loss")
}

func seedGeneratingArtifact(t *testing.T, repository *Repository, workspaceID foundation.ID, idBase int, now time.Time) artifactapplication.State {
	t.Helper()
	planned := artifactIntegrationPlan(t, workspaceID, idBase, idBase+1, now)
	if _, err := repository.Create(context.Background(), artifactapplication.CreateRecord{
		Binding: artifactIntegrationBinding(workspaceID, planned.Artifact.ID, fmt.Sprintf("plan-generation-%d", idBase), '8', artifactapplication.CommandPlan, 0),
		State:   planned,
	}); err != nil {
		t.Fatal(err)
	}
	artifact, revision, err := artifactdomain.SubmitOutline(planned.Artifact, planned.Revision, artifactIntegrationID(idBase+2), []artifactdomain.OutlineSection{
		{Key: "first", Title: "First"}, {Key: "second", Title: "Second"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	outlined := artifactapplication.State{Artifact: artifact, Revision: revision}
	artifactIntegrationTransition(t, repository,
		artifactIntegrationBinding(workspaceID, planned.Artifact.ID, fmt.Sprintf("outline-generation-%d", idBase), '9', artifactapplication.CommandSubmitOutline, planned.Artifact.Version),
		planned, outlined, true, nil, nil)
	artifact, revision, err = artifactdomain.ApproveOutline(outlined.Artifact, outlined.Revision, artifactIntegrationID(idBase+3), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approved := artifactapplication.State{Artifact: artifact, Revision: revision}
	artifactIntegrationTransition(t, repository,
		artifactIntegrationBinding(workspaceID, planned.Artifact.ID, fmt.Sprintf("approve-generation-%d", idBase), 'a', artifactapplication.CommandApproveOutline, outlined.Artifact.Version),
		outlined, approved, true, nil, nil)
	return approved
}

func recordGenerationGapSection(t *testing.T, current artifactapplication.State, key string, revisionID foundation.ID, at time.Time) artifactapplication.State {
	t.Helper()
	title := "First"
	if key == "second" {
		title = "Second"
	}
	artifact, revision, err := artifactdomain.RecordSection(current.Artifact, current.Revision, revisionID, artifactdomain.Section{
		Key: key, Title: title, Content: "", Citations: []artifactdomain.Citation{},
		Coverage: artifactdomain.Coverage{SectionKey: key, Status: artifactdomain.CoverageGap, Gaps: []artifactdomain.Gap{{Code: "NO_SOURCE", Description: "no approved source"}}},
	}, artifactdomain.CreatorHuman, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	return artifactapplication.State{Artifact: artifact, Revision: revision}
}

func generationGapFinalizeCommand(
	source artifactapplication.State,
	generation artifactapplication.SectionGeneration,
	attemptID foundation.ID,
	run agentdomain.ModelRun,
	key, title string,
) artifactworkflow.FinalizeSectionCommand {
	return artifactworkflow.FinalizeSectionCommand{
		FinalizationLookup: artifactworkflow.FinalizationLookup{
			WorkspaceID: source.Artifact.WorkspaceID, WorkflowRunID: generation.WorkflowRunID,
			NodeRunID: generation.NodeRunID, NodeAttemptID: attemptID,
			Input: artifactworkflow.Input{
				SchemaVersion: artifactworkflow.InputSchemaVersion, ArtifactID: source.Artifact.ID,
				RevisionID: source.Revision.ID, RevisionNo: source.Revision.RevisionNo,
				ArtifactVersion: source.Artifact.Version, SectionKey: key,
			},
		},
		ModelRunID: run.ID, ExpectedModelRunVersion: run.Version,
		Proposal: artifactworkflow.SectionProposal{
			SectionKey: key, Title: title, Content: "", Citations: []agentdomain.Citation{},
			Coverage: artifactdomain.Coverage{SectionKey: key, Status: artifactdomain.CoverageGap, Gaps: []artifactdomain.Gap{{Code: "NO_SOURCE", Description: "no approved source"}}},
			Metadata: artifactdomain.GenerationMetadata{
				PromptVersion: run.Prompt.Version, ModelVersion: run.Model.ModelVersion,
				WorkflowDefinitionVersion: "1", SchemaVersion: run.Schema.Version,
			},
		},
	}
}

func assertGenerationStillPending(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	artifactRepository *Repository,
	agentRepository *agentpostgres.Repository,
	source artifactapplication.State,
	generation artifactapplication.SectionGeneration,
	run agentdomain.ModelRun,
) {
	t.Helper()
	persisted, err := artifactRepository.Get(ctx, source.Artifact.WorkspaceID, source.Artifact.ID)
	if err != nil || persisted.Revision.ID != source.Revision.ID || persisted.Artifact.Version != source.Artifact.Version {
		t.Fatalf("artifact changed after rejected finalization persisted=%#v err=%v", persisted, err)
	}
	loadedGeneration, found, err := loadSectionGenerationByKey(ctx, pool, source.Artifact.WorkspaceID, generation.IdempotencyKey, false)
	if err != nil || !found || loadedGeneration.Status != artifactapplication.SectionGenerationPending {
		t.Fatalf("generation changed after rejected finalization generation=%#v found=%t err=%v", loadedGeneration, found, err)
	}
	record, err := agentRepository.GetModelRun(ctx, source.Artifact.WorkspaceID, run.ID)
	if err != nil || record.Run.Status != agentdomain.ModelRunRunning {
		t.Fatalf("model run changed after rejected finalization record=%#v err=%v", record, err)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_revision WHERE workspace_id=$1 AND artifact_id=$2`, string(source.Artifact.WorkspaceID), string(source.Artifact.ID)).Scan(&revisions); err != nil || revisions != 3 {
		t.Fatalf("revision count=%d err=%v", revisions, err)
	}
}

func transitionGenerationFailure(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	generation artifactapplication.SectionGeneration,
	status artifactapplication.SectionGenerationStatus,
	failureClass, errorCode, errorSummary string,
	at time.Time,
) {
	t.Helper()
	tag, err := pool.Exec(ctx, `
		UPDATE learning.artifact_section_generation
		SET status=$2,failure_class=$3,error_code=$4,error_summary=$5,version=2,updated_at=$6,terminal_at=$6
		WHERE id=$1 AND status='PENDING' AND version=1`,
		string(generation.ID), string(status), failureClass, errorCode, errorSummary, at.UTC())
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("terminal generation transition affected %d rows", tag.RowsAffected())
	}
}

func generationIntegrationProfile() agentdomain.ModelProfileRef {
	return agentdomain.ModelProfileRef{ID: "artifact.default", Version: "v1"}
}

type generationIntegrationIDs struct{ next int }

func (ids *generationIntegrationIDs) New() (foundation.ID, error) {
	value := artifactIntegrationID(ids.next)
	ids.next++
	return value, nil
}

type generationCitationVerifierFake struct {
	results []artifactdomain.Citation
	err     error
	calls   int
	entered chan<- struct{}
	release <-chan struct{}
}

func (fake *generationCitationVerifierFake) VerifyCitations(context.Context, foundation.ID, []artifactapplication.CitationInput) ([]artifactdomain.Citation, error) {
	fake.calls++
	if fake.entered != nil {
		fake.entered <- struct{}{}
	}
	if fake.release != nil {
		<-fake.release
	}
	return append([]artifactdomain.Citation(nil), fake.results...), fake.err
}

func seedGenerationAttempt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	generation artifactapplication.SectionGeneration,
	attemptID foundation.ID,
	attemptNo int,
	at time.Time,
) {
	t.Helper()
	leaseUntil := at.Add(time.Minute)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,river_job_id,river_job_attempt,delivery_id,
		lease_owner,lease_until,status,started_at,heartbeat_at
	) VALUES($1,$2,$3,1,0,1,1,$4,'artifact-worker',$5,'running',$6,$6)`,
		string(attemptID), string(generation.NodeRunID), attemptNo, fmt.Sprintf("delivery-%d", attemptNo), leaseUntil, at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET status='running',attempt=$2,lease_owner='artifact-worker',lease_until=$3,updated_at=$4,version=version+1 WHERE id=$1`,
		string(generation.NodeRunID), attemptNo, leaseUntil, at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.run SET status='running',updated_at=$2,version=version+1 WHERE id=$1`, string(generation.WorkflowRunID), at); err != nil {
		t.Fatal(err)
	}
}

func reclaimGenerationAttempt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	nodeRunID, previousAttemptID, nextAttemptID foundation.ID,
	at time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET status='lease_lost',failure_class='lease_lost',error_kind=$2,error_code='WORKFLOW_LEASE_LOST',error_summary='lease lost after finalization',lease_owner=NULL,lease_until=NULL,ended_at=$3,heartbeat_at=$3 WHERE id=$1`,
		string(previousAttemptID), string(foundation.ErrorVersionConflict), at); err != nil {
		t.Fatal(err)
	}
	leaseUntil := at.Add(2 * time.Minute)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,river_job_id,river_job_attempt,delivery_id,
		lease_owner,lease_until,status,started_at,heartbeat_at
	) VALUES($1,$2,2,1,0,2,1,'delivery-2','artifact-worker',$3,'running',$4,$4)`,
		string(nextAttemptID), string(nodeRunID), leaseUntil, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run SET attempt=2,lease_owner='artifact-worker',lease_until=$2,updated_at=$3,version=version+1 WHERE id=$1`,
		string(nodeRunID), leaseUntil, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func seedGenerationModelRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *agentpostgres.Repository,
	generation artifactapplication.SectionGeneration,
	attemptID, runID, callID foundation.ID,
	at time.Time,
) agentdomain.ModelRun {
	t.Helper()
	run := seedGenerationRunningModelRun(t, ctx, pool, repository, generation, attemptID, runID, at)
	call := agentdomain.ModelCall{
		ID: callID, ModelRunID: run.ID, CallNo: 1, Phase: agentdomain.ModelCallInitial,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 1024,
		Status: agentdomain.ModelCallStarted, RequestHash: artifactIntegrationHash('b'), RequestBytes: 128,
		Version: 1, StartedAt: at.Add(time.Second),
	}
	if _, replayed, err := repository.StartModelCall(ctx, run.WorkspaceID, call); err != nil || replayed {
		t.Fatalf("start model call replayed=%t err=%v", replayed, err)
	}
	completedAt := at.Add(2 * time.Second)
	call.Status = agentdomain.ModelCallSucceeded
	call.ResponseHash = artifactIntegrationHash('c')
	call.ResponseBytes = 64
	call.Usage = agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	call.LatencyMillis = 20
	call.Version = 2
	call.CompletedAt = &completedAt
	if _, replayed, err := repository.CompleteModelCall(ctx, agentapplication.CompleteModelCallCommand{
		WorkspaceID: run.WorkspaceID, ExpectedVersion: 1, Call: call,
	}); err != nil || replayed {
		t.Fatalf("complete model call replayed=%t err=%v", replayed, err)
	}
	return run
}

func seedGenerationRunningModelRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *agentpostgres.Repository,
	generation artifactapplication.SectionGeneration,
	attemptID, runID foundation.ID,
	at time.Time,
) agentdomain.ModelRun {
	t.Helper()
	indexVersionID := artifactIntegrationID(540)
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}','artifact-generation:index',repeat('2',64),0,
		'artifact-generation-index','building','["vector"]',1,$3,$3)
		ON CONFLICT (id) DO NOTHING`,
		string(indexVersionID), string(generation.WorkspaceID), at); err != nil {
		t.Fatal(err)
	}
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: generation.WorkspaceID, WorkflowRunID: generation.WorkflowRunID,
		NodeRunID: generation.NodeRunID, NodeAttemptID: attemptID,
		Model:   agentdomain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "artifact-model", ModelVersion: "2026-07"},
		Profile: generationIntegrationProfile(), Prompt: artifactworkflow.PromptRef(), Schema: artifactworkflow.SchemaRef(),
		ReducedSchema: artifactworkflow.ReducedSchemaRef(), Retrieval: agentdomain.RetrievalRef{IndexVersionID: indexVersionID},
		Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	if _, replayed, err := repository.CreateModelRun(ctx, run); err != nil || replayed {
		t.Fatalf("create model run replayed=%t err=%v", replayed, err)
	}
	return run
}
