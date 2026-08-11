package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestExecutorReplaysFinalReceiptBeforeContextRetrievalOrProvider(t *testing.T) {
	input, execution := validArtifactExecution(t)
	receipt := validOutputReceipt(input)
	receipt.BaseRevisionID = artifactWorkflowTestID(20)
	receipt.RevisionID = artifactWorkflowTestID(21)
	receipt.RevisionNo += 2
	receipt.ArtifactVersion += 2
	finalizer := &artifactFinalizerFake{receipt: receipt, found: true}
	loader := &artifactContextLoaderFake{}
	retrieval := &artifactRetrievalFake{}
	model := agentapplication.NewDeterministicChatModel()
	repository := &artifactModelRepository{}
	executor := newArtifactExecutor(t, model, repository, loader, retrieval, artifactEligibilityFake{}, finalizer, nil)

	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOutputReceipt(result.Output)
	if err != nil || decoded != receipt {
		t.Fatalf("receipt = %#v, err = %v", decoded, err)
	}
	if finalizer.lookupCalls != 1 || finalizer.finalizeCalls != 0 || loader.calls != 0 || retrieval.retrieveCalls != 0 ||
		model.CallCount() != 0 || repository.createCalls != 0 {
		t.Fatalf("finalizer=%#v loader=%#v retrieval=%#v model_calls=%d repository=%#v", finalizer, loader, retrieval, model.CallCount(), repository)
	}
}

func TestExecutorGeneratesFromFrozenSourceAndRebasesOntoCurrentRevision(t *testing.T) {
	source, current := artifactSourceAndAdvancedCurrent(t)
	input := Input{
		SchemaVersion: InputSchemaVersion, ArtifactID: source.Artifact.ID, RevisionID: source.Revision.ID,
		RevisionNo: source.Revision.RevisionNo, ArtifactVersion: source.Artifact.Version, SectionKey: "second",
	}
	execution := artifactExecutionForInput(t, input, source.Artifact.WorkspaceID)
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: artifactTestModelRef(), Content: sectionGenerationDocument(t, SectionGenerationPayload{
			CoverageStatus: artifactdomain.CoverageGap, Content: "", CitationLabels: []string{},
			Gaps: []GapOutput{{Code: "missing-second", Description: "no approved evidence"}},
		}), Usage: agentdomain.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
	}})
	repository := &artifactModelRepository{}
	loader := &artifactContextLoaderFake{result: GenerationContext{
		Current: current, SourceRevision: source.Revision, ProfileRef: artifactTestProfileRef(),
	}}
	retrieval := &artifactRetrievalFake{batch: agentapplication.RetrievalBatch{
		WorkspaceID: source.Artifact.WorkspaceID, IndexVersionID: artifactWorkflowTestID(30), Items: []agentapplication.RetrievedEvidence{},
	}}
	finalizer := &artifactFinalizerFake{current: current}
	executor := newArtifactExecutor(t, model, repository, loader, retrieval, artifactEligibilityFake{}, finalizer,
		[]foundation.ID{artifactWorkflowTestID(40), artifactWorkflowTestID(41)})

	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := DecodeOutputReceipt(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BaseRevisionID != current.Revision.ID || receipt.RevisionNo != current.Revision.RevisionNo+1 ||
		receipt.ArtifactVersion != current.Artifact.Version+1 || receipt.ArtifactVersion <= input.ArtifactVersion+1 {
		t.Fatalf("receipt = %#v", receipt)
	}
	if loader.calls != 1 || loader.query.Input != input || retrieval.retrieveCalls != 1 || retrieval.openBatchCalls != 0 ||
		finalizer.lookupCalls != 1 || finalizer.finalizeCalls != 1 {
		t.Fatalf("loader=%#v retrieval=%#v finalizer=%#v", loader, retrieval, finalizer)
	}
	command := finalizer.command
	if command.Input != input || command.Proposal.SectionKey != "second" || command.Proposal.Title != "Server second" ||
		command.Proposal.Coverage.Status != artifactdomain.CoverageGap || command.Proposal.Content != "" ||
		len(command.Proposal.Citations) != 0 || command.Proposal.Metadata.ModelVersion != artifactTestModelRef().ModelVersion {
		t.Fatalf("finalize command = %#v", command)
	}
	calls := model.Calls()
	if len(calls) != 1 || calls[0].SchemaRef != ReducedSchemaRef() || repository.run.ID != command.ModelRunID || len(repository.calls) != 1 {
		t.Fatalf("calls=%#v run=%#v persisted_calls=%#v", calls, repository.run, repository.calls)
	}
	modelInput := calls[0].Messages[len(calls[0].Messages)-1].Content
	if !strings.Contains(modelInput, "Server second") || strings.Contains(modelInput, "missing-first") {
		t.Fatalf("model input did not use frozen target context: %s", modelInput)
	}
}

func TestExecutorRejectsTargetSectionDriftBeforeRetrievalOrProvider(t *testing.T) {
	source, _ := artifactSourceAndAdvancedCurrent(t)
	at := source.Artifact.UpdatedAt.Add(time.Second)
	artifact, revision, err := artifactdomain.RecordSection(source.Artifact, source.Revision, artifactWorkflowTestID(60), artifactdomain.Section{
		Key: "second", Title: "Server second", Content: "", Citations: []artifactdomain.Citation{},
		Coverage: artifactdomain.Coverage{SectionKey: "second", Status: artifactdomain.CoverageGap,
			Gaps: []artifactdomain.Gap{{Code: "changed-second", Description: "target changed"}}},
	}, artifactdomain.CreatorHuman, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{
		SchemaVersion: InputSchemaVersion, ArtifactID: source.Artifact.ID, RevisionID: source.Revision.ID,
		RevisionNo: source.Revision.RevisionNo, ArtifactVersion: source.Artifact.Version, SectionKey: "second",
	}
	model := agentapplication.NewDeterministicChatModel()
	repository := &artifactModelRepository{}
	retrieval := &artifactRetrievalFake{}
	executor := newArtifactExecutor(t, model, repository, &artifactContextLoaderFake{result: GenerationContext{
		Current: artifactapplication.State{Artifact: artifact, Revision: revision}, SourceRevision: source.Revision,
		ProfileRef: artifactTestProfileRef(),
	}}, retrieval, artifactEligibilityFake{}, &artifactFinalizerFake{}, nil)

	_, err = executor.Execute(context.Background(), artifactExecutionForInput(t, input, source.Artifact.WorkspaceID))
	if testWorkflowErrorCode(err) != ErrorCodeContextInvalid || retrieval.retrieveCalls != 0 || model.CallCount() != 0 || repository.createCalls != 0 {
		t.Fatalf("err=%v retrieval=%d model=%d repository=%d", err, retrieval.retrieveCalls, model.CallCount(), repository.createCalls)
	}

	outlineDrift := artifactdomain.CloneRevision(source.Revision)
	outlineDrift.Outline[1].Title = "Drifted title"
	outlineDrift.ContentHash, err = artifactdomain.ComputeRevisionContentHash(outlineDrift)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifactapplication.ValidateSectionGenerationRebase(outlineDrift, source, "second"); err == nil {
		t.Fatal("outline drift was accepted")
	}
}

func TestExecutorSendsOnlyUncontestedEligibleEvidenceToModel(t *testing.T) {
	source, _ := artifactSourceAndAdvancedCurrent(t)
	target := source.Revision.Outline[1]
	citations := []agentdomain.Citation{
		artifactEvidenceCitation(1), artifactEvidenceCitation(2), artifactEvidenceCitation(3),
	}
	items := make([]agentapplication.RetrievedEvidence, len(citations))
	opened := make([]agentapplication.OpenedEvidence, len(citations))
	results := make([]knowledgedomain.ProvenanceEligibility, len(citations))
	for index, citation := range citations {
		items[index] = agentapplication.RetrievedEvidence{Citation: citation, SearchExcerpt: "search excerpt", CapturedAt: time.Unix(200+int64(index), 0).UTC()}
		opened[index] = agentapplication.OpenedEvidence{Citation: citation, Excerpt: fmt.Sprintf("opened-%d", index+1)}
		results[index] = knowledgedomain.ProvenanceEligibility{Provenance: provenanceFromCitation(citation), Eligibility: knowledgedomain.EvidenceIneligible, Bindings: []knowledgedomain.EvidenceEligibilityBinding{}}
	}
	results[0] = eligibleArtifactEvidence(citations[0])
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	results[1] = knowledgedomain.ProvenanceEligibility{
		Provenance: provenanceFromCitation(citations[1]), Eligibility: knowledgedomain.EvidenceEligibleWithConflict,
		Bindings: []knowledgedomain.EvidenceEligibilityBinding{{
			OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: artifactWorkflowTestID(74), EvidenceID: artifactWorkflowTestID(75),
			ClaimStatus: knowledgedomain.ClaimStatusDisputed, SupportType: knowledgedomain.ClaimSupportSupports,
			ConflictIDs: []foundation.ID{artifactWorkflowTestID(76)}, DisputedApplicability: applicability,
			DisputedClaimUpdatedAtUTC: time.Unix(300, 0).UTC(),
		}},
	}
	retrieval := &artifactRetrievalFake{batch: agentapplication.RetrievalBatch{
		WorkspaceID: source.Artifact.WorkspaceID, IndexVersionID: artifactWorkflowTestID(30), Items: items,
	}, opened: opened}
	executor := newArtifactExecutor(t, agentapplication.NewDeterministicChatModel(), &artifactModelRepository{},
		&artifactContextLoaderFake{}, retrieval, artifactEligibilityFake{results: results}, &artifactFinalizerFake{}, nil)

	_, evidence, err := executor.loadEligibleEvidence(context.Background(), GenerationContext{Current: source}, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Label != "citation-001" || evidence[0].Citation != citations[0] || evidence[0].Excerpt != "opened-1" {
		t.Fatalf("eligible evidence = %#v", evidence)
	}
}

func TestCreateModelRunReusesOnlyRunningReplayWithoutCalls(t *testing.T) {
	_, execution := validArtifactExecution(t)
	catalog := newArtifactRuntimeCatalog(t)
	snapshot, err := catalog.Snapshot(PromptRef(), SchemaRef(), ReducedSchemaRef(), artifactTestProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	retrieval := agentdomain.RetrievalRef{IndexVersionID: artifactWorkflowTestID(30)}
	repository := &artifactModelRepository{}
	executor := newArtifactExecutor(t, agentapplication.NewDeterministicChatModel(), repository, &artifactContextLoaderFake{},
		&artifactRetrievalFake{}, artifactEligibilityFake{}, &artifactFinalizerFake{}, []foundation.ID{artifactWorkflowTestID(40)})
	first, err := executor.createModelRun(context.Background(), execution, snapshot, retrieval)
	if err != nil {
		t.Fatal(err)
	}

	repository.replayCreate = true
	executor = newArtifactExecutor(t, agentapplication.NewDeterministicChatModel(), repository, &artifactContextLoaderFake{},
		&artifactRetrievalFake{}, artifactEligibilityFake{}, &artifactFinalizerFake{}, []foundation.ID{artifactWorkflowTestID(41)})
	replayed, err := executor.createModelRun(context.Background(), execution, snapshot, retrieval)
	if err != nil || replayed.ID != first.ID || repository.getCalls != 1 {
		t.Fatalf("replayed = %#v, err = %v, gets = %d", replayed, err, repository.getCalls)
	}

	corrupted := first
	corrupted.Profile = agentdomain.ModelProfileRef{ID: "artifact.other", Version: "v1"}
	repository.createReplayResult = &corrupted
	executor = newArtifactExecutor(t, agentapplication.NewDeterministicChatModel(), repository, &artifactContextLoaderFake{},
		&artifactRetrievalFake{}, artifactEligibilityFake{}, &artifactFinalizerFake{}, []foundation.ID{artifactWorkflowTestID(42)})
	if _, err := executor.createModelRun(context.Background(), execution, snapshot, retrieval); testWorkflowErrorCode(err) != ErrorCodeRunReplayUnsafe {
		t.Fatalf("conflicting create replay err = %v", err)
	}
	repository.createReplayResult = nil
	repository.calls = []agentdomain.ModelCall{{}}
	executor = newArtifactExecutor(t, agentapplication.NewDeterministicChatModel(), repository, &artifactContextLoaderFake{},
		&artifactRetrievalFake{}, artifactEligibilityFake{}, &artifactFinalizerFake{}, []foundation.ID{artifactWorkflowTestID(43)})
	if _, err := executor.createModelRun(context.Background(), execution, snapshot, retrieval); testWorkflowErrorCode(err) != ErrorCodeRunReplayUnsafe {
		t.Fatalf("unsafe replay err = %v", err)
	}
}

func TestCreateModelRunClonesModelSettingsRevisionProvenance(t *testing.T) {
	tests := []struct {
		name     string
		revision *int64
	}{
		{name: "static nil"},
		{name: "managed zero", revision: artifactModelSettingsRevision(0)},
		{name: "managed positive", revision: artifactModelSettingsRevision(7)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, execution := validArtifactExecution(t)
			execution.ModelSettingsRevision = cloneModelSettingsRevision(test.revision)
			catalog := newArtifactRuntimeCatalog(t)
			snapshot, err := catalog.Snapshot(PromptRef(), SchemaRef(), ReducedSchemaRef(), artifactTestProfileRef())
			if err != nil {
				t.Fatal(err)
			}
			repository := &artifactModelRepository{}
			executor := newArtifactExecutor(t, agentapplication.NewDeterministicChatModel(), repository,
				&artifactContextLoaderFake{}, &artifactRetrievalFake{}, artifactEligibilityFake{}, &artifactFinalizerFake{},
				[]foundation.ID{artifactWorkflowTestID(40)})

			created, err := executor.createModelRun(context.Background(), execution, snapshot,
				agentdomain.RetrievalRef{IndexVersionID: artifactWorkflowTestID(30)})
			if err != nil {
				t.Fatal(err)
			}
			if test.revision == nil {
				if created.ModelSettingsRevision != nil || repository.run.ModelSettingsRevision != nil {
					t.Fatalf("created revision = %v, stored revision = %v", created.ModelSettingsRevision, repository.run.ModelSettingsRevision)
				}
				return
			}
			if created.ModelSettingsRevision == nil || *created.ModelSettingsRevision != *test.revision ||
				repository.run.ModelSettingsRevision == nil || *repository.run.ModelSettingsRevision != *test.revision {
				t.Fatalf("created revision = %v, stored revision = %v, want = %d", created.ModelSettingsRevision, repository.run.ModelSettingsRevision, *test.revision)
			}
			if created.ModelSettingsRevision == execution.ModelSettingsRevision {
				t.Fatal("model run retained the execution revision pointer")
			}
			persisted := *created.ModelSettingsRevision
			*execution.ModelSettingsRevision = persisted + 1
			if *created.ModelSettingsRevision != persisted || *repository.run.ModelSettingsRevision != persisted {
				t.Fatalf("execution mutation changed model run provenance: created=%d stored=%d", *created.ModelSettingsRevision, *repository.run.ModelSettingsRevision)
			}
		})
	}
}

func TestCreateModelRunReplayUsesNilSensitiveModelSettingsRevision(t *testing.T) {
	tests := []struct {
		name      string
		stored    *int64
		requested *int64
		wantError bool
	}{
		{name: "same nil"},
		{name: "same zero", stored: artifactModelSettingsRevision(0), requested: artifactModelSettingsRevision(0)},
		{name: "same positive", stored: artifactModelSettingsRevision(7), requested: artifactModelSettingsRevision(7)},
		{name: "nil differs from zero", requested: artifactModelSettingsRevision(0), wantError: true},
		{name: "zero differs from nil", stored: artifactModelSettingsRevision(0), wantError: true},
		{name: "different positive", stored: artifactModelSettingsRevision(7), requested: artifactModelSettingsRevision(8), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, execution := validArtifactExecution(t)
			execution.ModelSettingsRevision = cloneModelSettingsRevision(test.stored)
			catalog := newArtifactRuntimeCatalog(t)
			snapshot, err := catalog.Snapshot(PromptRef(), SchemaRef(), ReducedSchemaRef(), artifactTestProfileRef())
			if err != nil {
				t.Fatal(err)
			}
			model := agentapplication.NewDeterministicChatModel()
			repository := &artifactModelRepository{}
			executor := newArtifactExecutor(t, model, repository, &artifactContextLoaderFake{}, &artifactRetrievalFake{},
				artifactEligibilityFake{}, &artifactFinalizerFake{},
				[]foundation.ID{artifactWorkflowTestID(40), artifactWorkflowTestID(41)})
			first, err := executor.createModelRun(context.Background(), execution, snapshot,
				agentdomain.RetrievalRef{IndexVersionID: artifactWorkflowTestID(30)})
			if err != nil {
				t.Fatal(err)
			}

			repository.replayCreate = true
			execution.ModelSettingsRevision = cloneModelSettingsRevision(test.requested)
			replayed, err := executor.createModelRun(context.Background(), execution, snapshot,
				agentdomain.RetrievalRef{IndexVersionID: artifactWorkflowTestID(30)})
			if test.wantError {
				if testWorkflowErrorCode(err) != ErrorCodeRunReplayUnsafe {
					t.Fatalf("replay error = %v", err)
				}
			} else if err != nil || replayed.ID != first.ID {
				t.Fatalf("replayed = %#v, err = %v", replayed, err)
			}
			if model.CallCount() != 0 {
				t.Fatalf("provider calls = %d", model.CallCount())
			}
		})
	}
}

func TestExecutorRejectsModelSettingsRevisionReplayDriftBeforeProvider(t *testing.T) {
	tests := []struct {
		name      string
		stored    *int64
		requested *int64
	}{
		{name: "nil differs from zero", requested: artifactModelSettingsRevision(0)},
		{name: "zero differs from nil", stored: artifactModelSettingsRevision(0)},
		{name: "different positive", stored: artifactModelSettingsRevision(7), requested: artifactModelSettingsRevision(8)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, _ := artifactSourceAndAdvancedCurrent(t)
			input := Input{
				SchemaVersion: InputSchemaVersion, ArtifactID: source.Artifact.ID, RevisionID: source.Revision.ID,
				RevisionNo: source.Revision.RevisionNo, ArtifactVersion: source.Artifact.Version, SectionKey: "second",
			}
			execution := artifactExecutionForInput(t, input, source.Artifact.WorkspaceID)
			execution.ModelSettingsRevision = cloneModelSettingsRevision(test.stored)
			model := agentapplication.NewDeterministicChatModel()
			repository := &artifactModelRepository{}
			loader := &artifactContextLoaderFake{result: GenerationContext{
				Current: source, SourceRevision: source.Revision, ProfileRef: artifactTestProfileRef(),
			}}
			retrieval := &artifactRetrievalFake{batch: agentapplication.RetrievalBatch{
				WorkspaceID: source.Artifact.WorkspaceID, IndexVersionID: artifactWorkflowTestID(30),
				Items: []agentapplication.RetrievedEvidence{},
			}}
			finalizer := &artifactFinalizerFake{}
			executor := newArtifactExecutor(t, model, repository, loader, retrieval, artifactEligibilityFake{}, finalizer,
				[]foundation.ID{artifactWorkflowTestID(40), artifactWorkflowTestID(41)})
			snapshot, err := executor.dependencies.Catalog.Snapshot(PromptRef(), ReducedSchemaRef(), ReducedSchemaRef(), artifactTestProfileRef())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executor.createModelRun(context.Background(), execution, snapshot,
				agentdomain.RetrievalRef{IndexVersionID: artifactWorkflowTestID(30)}); err != nil {
				t.Fatal(err)
			}

			repository.replayCreate = true
			execution.ModelSettingsRevision = cloneModelSettingsRevision(test.requested)
			_, err = executor.Execute(context.Background(), execution)
			if testWorkflowErrorCode(err) != ErrorCodeRunReplayUnsafe {
				t.Fatalf("err = %v", err)
			}
			if model.CallCount() != 0 || finalizer.finalizeCalls != 0 {
				t.Fatalf("provider calls = %d, finalizer calls = %d", model.CallCount(), finalizer.finalizeCalls)
			}
		})
	}
}

func TestExecutorRejectsNegativeModelSettingsRevisionBeforeDependencies(t *testing.T) {
	_, execution := validArtifactExecution(t)
	execution.ModelSettingsRevision = artifactModelSettingsRevision(-1)
	model := agentapplication.NewDeterministicChatModel()
	repository := &artifactModelRepository{}
	loader := &artifactContextLoaderFake{}
	retrieval := &artifactRetrievalFake{}
	finalizer := &artifactFinalizerFake{}
	executor := newArtifactExecutor(t, model, repository, loader, retrieval, artifactEligibilityFake{}, finalizer, nil)

	_, err := executor.Execute(context.Background(), execution)
	if testWorkflowErrorCode(err) != ErrorCodeInputInvalid {
		t.Fatalf("err = %v", err)
	}
	if finalizer.lookupCalls != 0 || loader.calls != 0 || retrieval.retrieveCalls != 0 ||
		model.CallCount() != 0 || repository.createCalls != 0 {
		t.Fatalf("finalizer=%#v loader=%#v retrieval=%#v model_calls=%d repository=%#v", finalizer, loader, retrieval, model.CallCount(), repository)
	}
}

func newArtifactExecutor(
	t *testing.T,
	model agentapplication.ChatModel,
	repository *artifactModelRepository,
	loader *artifactContextLoaderFake,
	retrieval *artifactRetrievalFake,
	eligibility artifactEligibilityFake,
	finalizer *artifactFinalizerFake,
	ids []foundation.ID,
) *Executor {
	t.Helper()
	executor, err := NewExecutor(ExecutorDependencies{
		Model: model, Catalog: newArtifactRuntimeCatalog(t), Repository: repository, Context: loader,
		Retrieval: retrieval, Eligibility: eligibility, Finalizer: finalizer,
		IDs: &artifactIDs{values: ids}, Clock: &artifactClock{next: time.Unix(1_000, 0).UTC()},
		Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func validArtifactExecution(t *testing.T) (Input, workflowapplication.ExecutionContext) {
	t.Helper()
	input := validWorkflowInput()
	return input, artifactExecutionForInput(t, input, artifactWorkflowTestID(1))
}

func artifactExecutionForInput(t *testing.T, input Input, workspaceID foundation.ID) workflowapplication.ExecutionContext {
	t.Helper()
	encoded, err := EncodeInput(input)
	if err != nil {
		t.Fatal(err)
	}
	return workflowapplication.ExecutionContext{
		WorkspaceID: workspaceID, DefinitionID: artifactWorkflowTestID(7), DefinitionVersion: DefinitionVersion,
		DefinitionHash: definitionGraphHash, RunID: artifactWorkflowTestID(8), NodeKey: NodeKey,
		NodeRunID: artifactWorkflowTestID(9), NodeAttemptID: artifactWorkflowTestID(10), NodeKind: NodeKind,
		NodeVersion: 1, InputSchemaVersion: InputSchemaVersion, AttemptNo: 1, DispatchNo: 1,
		LeaseOwner: "artifact-worker", Input: encoded,
	}
}

func artifactModelSettingsRevision(value int64) *int64 {
	return &value
}

func artifactSourceAndAdvancedCurrent(t *testing.T) (artifactapplication.State, artifactapplication.State) {
	t.Helper()
	at := time.Unix(100, 0).UTC()
	artifact, revision, err := artifactdomain.PlanArtifact(artifactdomain.PlanInput{
		ArtifactID: artifactWorkflowTestID(2), InitialRevisionID: artifactWorkflowTestID(3), WorkspaceID: artifactWorkflowTestID(1),
		Type: "study-guide", Title: "Server artifact", ScopeDefinition: "Server scope", CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = artifactdomain.SubmitOutline(artifact, revision, artifactWorkflowTestID(4), []artifactdomain.OutlineSection{
		{Key: "first", Title: "Server first"}, {Key: "second", Title: "Server second"},
	}, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = artifactdomain.ApproveOutline(artifact, revision, artifactWorkflowTestID(5), at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	source := artifactapplication.State{Artifact: artifact, Revision: revision}
	artifact, revision, err = artifactdomain.RecordSection(artifact, revision, artifactWorkflowTestID(6), artifactdomain.Section{
		Key: "first", Title: "Server first", Content: "", Citations: []artifactdomain.Citation{},
		Coverage: artifactdomain.Coverage{SectionKey: "first", Status: artifactdomain.CoverageGap, Gaps: []artifactdomain.Gap{{Code: "missing-first", Description: "missing first"}}},
	}, artifactdomain.CreatorHuman, nil, at.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return source, artifactapplication.State{Artifact: artifact, Revision: revision}
}

func artifactEvidenceCitation(index int) agentdomain.Citation {
	return agentdomain.Citation{
		ID: fmt.Sprintf("retrieved-%d", index), WorkspaceID: artifactWorkflowTestID(1), IndexVersionID: artifactWorkflowTestID(30),
		ChunkID: artifactWorkflowTestID(60 + index*3), SourceVersionID: artifactWorkflowTestID(61 + index*3),
		SourceSpanID: artifactWorkflowTestID(62 + index*3),
	}
}

func eligibleArtifactEvidence(citation agentdomain.Citation) knowledgedomain.ProvenanceEligibility {
	return knowledgedomain.ProvenanceEligibility{
		Provenance: provenanceFromCitation(citation), Eligibility: knowledgedomain.EvidenceEligible,
		Bindings: []knowledgedomain.EvidenceEligibilityBinding{{
			OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: artifactWorkflowTestID(70), EvidenceID: artifactWorkflowTestID(71),
			ClaimStatus: knowledgedomain.ClaimStatusConfirmed, SupportType: knowledgedomain.ClaimSupportSupports,
		}},
	}
}

type artifactContextLoaderFake struct {
	calls  int
	query  GenerationContextQuery
	result GenerationContext
	err    error
}

func (fake *artifactContextLoaderFake) LoadGenerationContext(_ context.Context, query GenerationContextQuery) (GenerationContext, error) {
	fake.calls++
	fake.query = query
	return fake.result, fake.err
}

type artifactRetrievalFake struct {
	batch          agentapplication.RetrievalBatch
	opened         []agentapplication.OpenedEvidence
	retrieveCalls  int
	openBatchCalls int
}

func (fake *artifactRetrievalFake) Retrieve(context.Context, agentapplication.RetrievalRequest) (agentapplication.RetrievalBatch, error) {
	fake.retrieveCalls++
	return fake.batch, nil
}

func (*artifactRetrievalFake) Open(context.Context, agentdomain.Citation) (agentapplication.OpenedEvidence, error) {
	return agentapplication.OpenedEvidence{}, errors.New("single open is not allowed")
}

func (fake *artifactRetrievalFake) OpenBatch(context.Context, []agentdomain.Citation) ([]agentapplication.OpenedEvidence, error) {
	fake.openBatchCalls++
	return append([]agentapplication.OpenedEvidence(nil), fake.opened...), nil
}

type artifactEligibilityFake struct {
	results []knowledgedomain.ProvenanceEligibility
}

func (fake artifactEligibilityFake) CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	return append([]knowledgedomain.ProvenanceEligibility(nil), fake.results...), nil
}

type artifactFinalizerFake struct {
	receipt       OutputReceipt
	found         bool
	current       artifactapplication.State
	lookupCalls   int
	finalizeCalls int
	command       FinalizeSectionCommand
}

func (fake *artifactFinalizerFake) Lookup(context.Context, FinalizationLookup) (OutputReceipt, bool, error) {
	fake.lookupCalls++
	return fake.receipt, fake.found, nil
}

func (fake *artifactFinalizerFake) Finalize(_ context.Context, command FinalizeSectionCommand) (OutputReceipt, bool, error) {
	fake.finalizeCalls++
	fake.command = command
	if fake.receipt.ArtifactID == "" {
		fake.receipt = OutputReceipt{
			SchemaVersion: OutputSchemaVersion, ArtifactID: command.Input.ArtifactID,
			BaseRevisionID: fake.current.Revision.ID, RevisionID: artifactWorkflowTestID(50),
			RevisionNo: fake.current.Revision.RevisionNo + 1, ArtifactVersion: fake.current.Artifact.Version + 1,
			SectionKey: command.Input.SectionKey, ModelRunID: command.ModelRunID, ContentHash: strings.Repeat("b", 64),
		}
	}
	return fake.receipt, false, nil
}

type artifactModelRepository struct {
	run                agentdomain.ModelRun
	calls              []agentdomain.ModelCall
	createReplayResult *agentdomain.ModelRun
	replayCreate       bool
	createCalls        int
	getCalls           int
}

func (repository *artifactModelRepository) CreateModelRun(_ context.Context, run agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	repository.createCalls++
	if repository.replayCreate {
		if repository.createReplayResult != nil {
			return *repository.createReplayResult, true, nil
		}
		return repository.run, true, nil
	}
	repository.run = run
	return run, false, nil
}

func (repository *artifactModelRepository) GetModelRun(context.Context, foundation.ID, foundation.ID) (agentapplication.ModelRunRecord, error) {
	repository.getCalls++
	return agentapplication.ModelRunRecord{Run: repository.run, Calls: append([]agentdomain.ModelCall(nil), repository.calls...)}, nil
}

func (repository *artifactModelRepository) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	if err := agentdomain.ValidateModelCall(call); err != nil {
		return agentdomain.ModelCall{}, false, err
	}
	repository.calls = append(repository.calls, call)
	return call, false, nil
}

func (repository *artifactModelRepository) CompleteModelCall(_ context.Context, command agentapplication.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	if err := agentdomain.ValidateModelCall(command.Call); err != nil {
		return agentdomain.ModelCall{}, false, err
	}
	repository.calls[command.Call.CallNo-1] = command.Call
	return command.Call, false, nil
}

func (repository *artifactModelRepository) FinalizeModelRun(_ context.Context, command agentapplication.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	repository.run = command.Run
	return command.Run, false, nil
}

func (*artifactModelRepository) MarkStaleModelCallsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelCall, error) {
	return nil, nil
}

func (*artifactModelRepository) MarkStaleModelRunsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelRun, error) {
	return nil, nil
}

type artifactIDs struct {
	values []foundation.ID
	next   int
}

func (ids *artifactIDs) New() (foundation.ID, error) {
	if ids.next >= len(ids.values) {
		return "", fmt.Errorf("artifact test id sequence exhausted")
	}
	value := ids.values[ids.next]
	ids.next++
	return value, nil
}

type artifactClock struct{ next time.Time }

func (clock *artifactClock) Now() time.Time {
	value := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return value
}

var _ agentapplication.ModelRunRepository = (*artifactModelRepository)(nil)
var _ ContextLoader = (*artifactContextLoaderFake)(nil)
var _ agentapplication.RetrievalPort = (*artifactRetrievalFake)(nil)
var _ Finalizer = (*artifactFinalizerFake)(nil)
