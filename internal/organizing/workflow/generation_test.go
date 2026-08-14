package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestGeneratorRecoversFinalizationResponseLossWithoutCallingModelAgain(t *testing.T) {
	snapshot, verifier := semanticSnapshot([]semanticEvidence{{source: 1, span: 11, excerpt: "Cache entries expire after five minutes."}})
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateKnowledgeReport)
	snapshot.TemplateID, snapshot.TemplateRevisionID, snapshot.TemplateHash = revision.TemplateID, revision.ID, revision.DeclarationHash
	var err error
	snapshot.Hash, err = organizingdomain.ComputeSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	outline := outlineFromTemplate(revision)
	output := generationDocumentOutput(t, outline)
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "organizing", ModelVersion: "v1"}
	model := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{
		Model: modelRef, Content: output,
		Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}})
	modelRuns := &generationModelRunStore{}
	generations := &generationStoreFake{modelRuns: modelRuns, loseCompleteResponse: true}
	generator := newGenerationTestGenerator(t, model, modelRuns, generations, verifier, modelRef)
	execution := generationExecution(100)

	if _, err := generator.GenerateDocument(context.Background(), execution, snapshot, revision, outline); reviewErrorCode(err) != "ORGANIZING_GENERATION_FINALIZATION_UNKNOWN" {
		t.Fatalf("first GenerateDocument() error = %#v", err)
	}
	if model.CallCount() != 1 || generations.record.Status != GenerationReady || modelRuns.run.Status != agentdomain.ModelRunSucceeded {
		t.Fatalf("calls=%d generation=%+v model-run=%+v", model.CallCount(), generations.record, modelRuns.run)
	}

	retry := execution
	retry.NodeAttemptID = semanticID(104)
	retry.AttemptNo = 2
	document, err := generator.GenerateDocument(context.Background(), retry, snapshot, revision, outline)
	if err != nil {
		t.Fatal(err)
	}
	if model.CallCount() != 1 || len(document.Sections) != len(outline) || document.Sections[0].Content != "Verified cache guidance." {
		t.Fatalf("calls=%d document=%+v", model.CallCount(), document)
	}
	if document.Metadata.ModelVersion != "test/organizing@v1" ||
		document.Metadata.PromptVersion != DocumentPromptID+"@"+GenerationRuntimeVersion {
		t.Fatalf("metadata=%+v", document.Metadata)
	}

	requests := model.Calls()
	if len(requests) != 1 {
		t.Fatalf("requests=%d", len(requests))
	}
	var visible strings.Builder
	for _, message := range requests[0].Messages {
		visible.WriteString(message.Content)
	}
	modelInput := visible.String()
	if !strings.Contains(modelInput, "E001") || !strings.Contains(modelInput, "Cache entries expire") ||
		strings.Contains(modelInput, string(snapshot.ID)) || strings.Contains(modelInput, string(snapshot.Materials[0].SourceVersionID)) ||
		strings.Contains(modelInput, snapshot.Materials[0].ContentHash) {
		t.Fatalf("model input exposed an internal identity or omitted evidence: %s", modelInput)
	}
}

func TestGeneratorExecutesConfiguredEinoStructuredScheduler(t *testing.T) {
	snapshot, verifier := semanticSnapshot([]semanticEvidence{{source: 1, span: 11, excerpt: "Cache entries expire after five minutes."}})
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateKnowledgeReport)
	snapshot.TemplateID, snapshot.TemplateRevisionID, snapshot.TemplateHash = revision.TemplateID, revision.ID, revision.DeclarationHash
	var err error
	snapshot.Hash, err = organizingdomain.ComputeSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	outline := outlineFromTemplate(revision)
	scheduler := newOrganizingTrackingEinoScheduler(t)
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "organizing", ModelVersion: "v1"}
	model := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{
		Model: modelRef, Content: generationDocumentOutput(t, outline),
		Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}})
	modelRuns := &generationModelRunStore{}
	generations := &generationStoreFake{modelRuns: modelRuns}
	generator := newGenerationTestGeneratorWithScheduler(
		t,
		model,
		modelRuns,
		generations,
		verifier,
		modelRef,
		scheduler,
	)
	if generator.dependencies.Scheduler != scheduler {
		t.Fatalf("scheduler=%T want=%T", generator.dependencies.Scheduler, scheduler)
	}
	document, err := generator.GenerateDocument(context.Background(), generationExecution(105), snapshot, revision, outline)
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Calls()
	if len(requests) != 1 || requests[0].Phase != agentdomain.ModelCallInitial || len(modelRuns.calls) != 1 ||
		modelRuns.calls[0].CallNo != 1 || modelRuns.calls[0].Phase != agentdomain.ModelCallInitial ||
		modelRuns.calls[0].Status != agentdomain.ModelCallSucceeded || modelRuns.calls[0].ResponseBytes == 0 ||
		modelRuns.calls[0].Usage.TotalTokens != 15 {
		t.Fatalf("requests=%+v model-calls=%+v", requests, modelRuns.calls)
	}
	if scheduler.calls.Load() != 1 || generations.record.Status != GenerationReady || modelRuns.run.Status != agentdomain.ModelRunSucceeded ||
		len(document.Sections) != len(outline) || document.Sections[0].Content != "Verified cache guidance." {
		t.Fatalf("scheduler_calls=%d generation=%+v model-run=%+v document=%+v", scheduler.calls.Load(), generations.record, modelRuns.run, document)
	}
}

type organizingTrackingScheduler struct {
	delegate agentapp.StructuredPhaseScheduler
	calls    atomic.Int64
}

func newOrganizingTrackingEinoScheduler(t *testing.T) *organizingTrackingScheduler {
	t.Helper()
	scheduler, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return &organizingTrackingScheduler{delegate: scheduler}
}

func (scheduler *organizingTrackingScheduler) Schedule(ctx context.Context, run *agentapp.StructuredPhaseRun) error {
	scheduler.calls.Add(1)
	return scheduler.delegate.Schedule(ctx, run)
}

func TestGeneratorUsesHistoricalDocumentWithoutFabricatingRetrievalEvidence(t *testing.T) {
	snapshot, verifier := semanticSnapshot(nil)
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateKnowledgeReport)
	snapshot.TemplateID, snapshot.TemplateRevisionID, snapshot.TemplateHash = revision.TemplateID, revision.ID, revision.DeclarationHash
	document := organizingapp.FrozenDocumentContent{
		DocumentID: semanticID(301), ArticleRevisionID: semanticID(302), RevisionNo: 2,
		ContentHash: hashText("historical-java-ai"), Title: "Java AI historical notes",
		Content: "RAW-HISTORICAL-MATERIAL: Spring AI uses portable model abstractions.",
	}
	snapshot.Materials = []organizingdomain.MaterialRef{{
		Kind: organizingdomain.MaterialDocumentRevision, DocumentID: document.DocumentID, ArticleRevisionID: document.ArticleRevisionID,
		Version: document.RevisionNo, ContentHash: document.ContentHash, Evidence: []organizingdomain.EvidenceRef{},
	}}
	var err error
	snapshot.Hash, err = organizingdomain.ComputeSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	outline := outlineFromTemplate(revision)
	sections := make([]generatedSection, len(outline))
	for index, section := range outline {
		sections[index] = generatedSection{
			Key: section.Key, CoverageStatus: artifactdomain.CoverageCovered, Content: "Portable Java AI model abstractions.",
			EvidenceLabels: []string{}, DocumentLabels: []string{"D001"}, Gaps: []generatedGap{},
		}
	}
	output, err := json.Marshal(generatedDocumentEnvelope{
		ResultType: agentdomain.ResultTypeOrganizingDocument, SchemaID: DocumentSchemaID, SchemaVersion: GenerationRuntimeVersion,
		Payload: generatedDocumentPayload{Sections: sections, Comparisons: []generatedComparison{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "organizing", ModelVersion: "v1"}
	model := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{
		Model: modelRef, Content: output, Usage: agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28},
	}})
	modelRuns := &generationModelRunStore{}
	generations := &generationStoreFake{modelRuns: modelRuns}
	generator := newGenerationTestGeneratorWithDocuments(t, model, modelRuns, generations, verifier, modelRef, generationDocumentReaderFake{documents: []organizingapp.FrozenDocumentContent{document}})

	generated, err := generator.GenerateDocument(context.Background(), generationExecution(110), snapshot, revision, outline)
	if err != nil {
		t.Fatal(err)
	}
	if modelRuns.run.Retrieval != (agentdomain.RetrievalRef{}) {
		t.Fatalf("document-only generation fabricated retrieval binding: %+v", modelRuns.run.Retrieval)
	}
	if len(generated.Sections) == 0 || len(generated.Sections[0].Citations) != 0 || len(generated.Sections[0].DocumentSources) != 1 ||
		generated.Sections[0].DocumentSources[0].ArticleRevisionID != document.ArticleRevisionID {
		t.Fatalf("generated sections=%+v", generated.Sections)
	}
	requests := model.Calls()
	if len(requests) != 1 {
		t.Fatalf("model requests=%d", len(requests))
	}
	var visible strings.Builder
	for _, message := range requests[0].Messages {
		visible.WriteString(message.Content)
	}
	modelInput := visible.String()
	if !strings.Contains(modelInput, "D001") || !strings.Contains(modelInput, document.Content) ||
		strings.Contains(modelInput, string(document.DocumentID)) || strings.Contains(modelInput, string(document.ArticleRevisionID)) ||
		strings.Contains(modelInput, document.ContentHash) {
		t.Fatalf("document model input leaked identity or omitted content: %s", modelInput)
	}
	if strings.Contains(string(generations.record.Output), document.Content) {
		t.Fatal("raw historical document content was persisted in generation output")
	}
}

func TestGeneratorMakesDocumentCapacityLimitVisible(t *testing.T) {
	snapshot, verifier := semanticSnapshot(nil)
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateKnowledgeReport)
	snapshot.TemplateID, snapshot.TemplateRevisionID, snapshot.TemplateHash = revision.TemplateID, revision.ID, revision.DeclarationHash
	document := organizingapp.FrozenDocumentContent{
		DocumentID: semanticID(311), ArticleRevisionID: semanticID(312), RevisionNo: 1,
		ContentHash: hashText("oversized-document"), Title: "Oversized notes", Content: strings.Repeat("x", maxGenerationDocumentBytes+1),
	}
	snapshot.Materials = []organizingdomain.MaterialRef{{
		Kind: organizingdomain.MaterialDocumentRevision, DocumentID: document.DocumentID, ArticleRevisionID: document.ArticleRevisionID,
		Version: 1, ContentHash: document.ContentHash, Evidence: []organizingdomain.EvidenceRef{},
	}}
	var err error
	snapshot.Hash, err = organizingdomain.ComputeSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	outline := outlineFromTemplate(revision)
	sections := make([]generatedSection, len(outline))
	for index, section := range outline {
		sections[index] = generatedSection{Key: section.Key, CoverageStatus: artifactdomain.CoverageCovered, Content: "Bounded summary.", EvidenceLabels: []string{}, DocumentLabels: []string{"D001"}, Gaps: []generatedGap{}}
	}
	output, err := json.Marshal(generatedDocumentEnvelope{ResultType: agentdomain.ResultTypeOrganizingDocument, SchemaID: DocumentSchemaID, SchemaVersion: GenerationRuntimeVersion, Payload: generatedDocumentPayload{Sections: sections, Comparisons: []generatedComparison{}}})
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "organizing", ModelVersion: "v1"}
	model := agentapp.NewDeterministicChatModel(agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: modelRef, Content: output, Usage: agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}}})
	modelRuns := &generationModelRunStore{}
	generations := &generationStoreFake{modelRuns: modelRuns}
	generator := newGenerationTestGeneratorWithDocuments(t, model, modelRuns, generations, verifier, modelRef, generationDocumentReaderFake{documents: []organizingapp.FrozenDocumentContent{document}})
	generated, err := generator.GenerateDocument(context.Background(), generationExecution(120), snapshot, revision, outline)
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range generated.Sections {
		if section.Coverage.Status != artifactdomain.CoveragePartial || !containsGenerationGap(section.Coverage.Gaps, documentCapacityGap) {
			t.Fatalf("capacity limit was not visible: %+v", section.Coverage)
		}
	}
}

func generationDocumentOutput(t *testing.T, outline []artifactdomain.OutlineSection) []byte {
	t.Helper()
	sections := make([]generatedSection, len(outline))
	for index, item := range outline {
		sections[index] = generatedSection{
			Key: item.Key, CoverageStatus: artifactdomain.CoverageCovered,
			Content: "Verified cache guidance.", EvidenceLabels: []string{"E001"}, DocumentLabels: []string{}, Gaps: []generatedGap{},
		}
	}
	encoded, err := json.Marshal(generatedDocumentEnvelope{
		ResultType: agentdomain.ResultTypeOrganizingDocument, SchemaID: DocumentSchemaID, SchemaVersion: GenerationRuntimeVersion,
		Payload: generatedDocumentPayload{Sections: sections, Comparisons: []generatedComparison{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func newGenerationTestGenerator(
	t *testing.T,
	model agentapp.ChatModel,
	modelRuns *generationModelRunStore,
	generations *generationStoreFake,
	verifier CitationVerifier,
	modelRef agentdomain.ModelRef,
) *Generator {
	return newGenerationTestGeneratorWithScheduler(t, model, modelRuns, generations, verifier, modelRef, newOrganizingTrackingEinoScheduler(t))
}

func newGenerationTestGeneratorWithScheduler(
	t *testing.T,
	model agentapp.ChatModel,
	modelRuns *generationModelRunStore,
	generations *generationStoreFake,
	verifier CitationVerifier,
	modelRef agentdomain.ModelRef,
	scheduler agentapp.StructuredPhaseScheduler,
) *Generator {
	return newGenerationTestGeneratorWithDocumentsAndScheduler(
		t, model, modelRuns, generations, verifier, modelRef, emptyGenerationDocumentReader{}, scheduler,
	)
}

func newGenerationTestGeneratorWithDocuments(
	t *testing.T,
	model agentapp.ChatModel,
	modelRuns *generationModelRunStore,
	generations *generationStoreFake,
	verifier CitationVerifier,
	modelRef agentdomain.ModelRef,
	documents organizingapp.FrozenDocumentContentReader,
) *Generator {
	return newGenerationTestGeneratorWithDocumentsAndScheduler(
		t, model, modelRuns, generations, verifier, modelRef, documents, newOrganizingTrackingEinoScheduler(t),
	)
}

func newGenerationTestGeneratorWithDocumentsAndScheduler(
	t *testing.T,
	model agentapp.ChatModel,
	modelRuns *generationModelRunStore,
	generations *generationStoreFake,
	verifier CitationVerifier,
	modelRef agentdomain.ModelRef,
	documents organizingapp.FrozenDocumentContentReader,
	scheduler agentapp.StructuredPhaseScheduler,
) *Generator {
	t.Helper()
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterGenerationRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profileRef := agentdomain.ModelProfileRef{ID: "organizing-test", Version: "v1"}
	if err := catalog.RegisterProfile(agentapp.ModelProfile{
		Ref: profileRef, Model: modelRef, Timeout: time.Second, MaxOutputTokens: 4096,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	evidence, err := NewEvidenceRenderer(verifier)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(GeneratorDependencies{
		Model: model, Scheduler: scheduler, Catalog: catalog, ModelRuns: modelRuns, Store: generations, Evidence: evidence, Documents: documents,
		ProfileRef: profileRef, IDs: &generationIDs{next: 200}, Clock: &generationClock{next: time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generator
}

type generationDocumentReaderFake struct {
	documents []organizingapp.FrozenDocumentContent
}

func (fake generationDocumentReaderFake) OpenFrozenDocumentContents(context.Context, foundation.ID, []organizingdomain.MaterialRef) ([]organizingapp.FrozenDocumentContent, error) {
	return append([]organizingapp.FrozenDocumentContent(nil), fake.documents...), nil
}

func containsGenerationGap(gaps []artifactdomain.Gap, code string) bool {
	for _, gap := range gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

type emptyGenerationDocumentReader struct{}

func (emptyGenerationDocumentReader) OpenFrozenDocumentContents(context.Context, foundation.ID, []organizingdomain.MaterialRef) ([]organizingapp.FrozenDocumentContent, error) {
	return []organizingapp.FrozenDocumentContent{}, nil
}

func generationExecution(attemptID int) workflowapp.ExecutionContext {
	return workflowapp.ExecutionContext{
		WorkspaceID: semanticID(71), DefinitionID: semanticID(99), DefinitionVersion: DefinitionVersion,
		DefinitionHash: hashText("definition"), RunID: semanticID(101), NodeKey: KnowledgeReportNodeKind,
		NodeRunID: semanticID(102), NodeAttemptID: semanticID(attemptID), NodeKind: KnowledgeReportNodeKind,
		NodeVersion: 2, InputSchemaVersion: InputSchemaVersion, AttemptNo: 1, DispatchNo: 1, LeaseOwner: "generation-test",
	}
}

type generationStoreFake struct {
	record               GenerationRecord
	modelRuns            *generationModelRunStore
	loseCompleteResponse bool
}

func (store *generationStoreFake) LookupReady(
	_ context.Context,
	workspaceID foundation.ID,
	nodeRunID foundation.ID,
	kind GenerationKind,
	requestHash string,
) (GenerationRecord, bool, error) {
	record := store.record
	found := record.Status == GenerationReady && record.WorkspaceID == workspaceID && record.NodeRunID == nodeRunID &&
		record.Kind == kind && record.RequestHash == requestHash
	return cloneGenerationRecord(record), found, nil
}

func (store *generationStoreFake) Prepare(_ context.Context, command PrepareGenerationCommand) (GenerationRecord, bool, error) {
	if store.record.ID != "" {
		return cloneGenerationRecord(store.record), true, nil
	}
	store.record = cloneGenerationRecord(command.Record)
	return cloneGenerationRecord(store.record), false, nil
}

func (store *generationStoreFake) BindModelRun(_ context.Context, command BindGenerationModelRunCommand) (GenerationRecord, error) {
	if command.GenerationID != store.record.ID || command.ExpectedVersion != store.record.Version {
		return GenerationRecord{}, errors.New("generation bind conflict")
	}
	store.record.ModelRunID = command.ModelRunID
	store.record.Version++
	store.record.UpdatedAt = store.record.UpdatedAt.Add(time.Millisecond)
	return cloneGenerationRecord(store.record), nil
}

func (store *generationStoreFake) Complete(_ context.Context, command CompleteGenerationCommand) (GenerationRecord, bool, error) {
	if command.GenerationID != store.record.ID || command.ExpectedVersion != store.record.Version ||
		command.ModelRunID != store.record.ModelRunID || command.OutputHash != hashBytes(command.Output) {
		return GenerationRecord{}, false, errors.New("generation complete conflict")
	}
	store.record.Status = GenerationReady
	store.record.Output = append(json.RawMessage(nil), command.Output...)
	store.record.OutputHash = command.OutputHash
	store.record.Version++
	store.record.UpdatedAt = command.CompletedAt
	completedAt := command.CompletedAt
	store.record.CompletedAt = &completedAt
	store.modelRuns.succeed(command.ModelRunID, command.ResultType, completedAt)
	result := cloneGenerationRecord(store.record)
	if store.loseCompleteResponse {
		store.loseCompleteResponse = false
		return GenerationRecord{}, false, errors.New("response lost after commit")
	}
	return result, false, nil
}

func (*generationStoreFake) Fail(context.Context, FailGenerationCommand) error { return nil }

func cloneGenerationRecord(record GenerationRecord) GenerationRecord {
	copy := record
	copy.Output = append(json.RawMessage(nil), record.Output...)
	copy.ModelSettingsRevision = cloneRevision(record.ModelSettingsRevision)
	if record.CompletedAt != nil {
		completedAt := *record.CompletedAt
		copy.CompletedAt = &completedAt
	}
	return copy
}

type generationModelRunStore struct {
	run   agentdomain.ModelRun
	calls []agentdomain.ModelCall
}

func (store *generationModelRunStore) CreateModelRun(_ context.Context, run agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	if store.run.ID != "" {
		if store.run.NodeAttemptID == run.NodeAttemptID {
			return store.run, true, nil
		}
		return agentdomain.ModelRun{}, false, errors.New("unexpected second model run")
	}
	store.run = run
	return run, false, nil
}

func (store *generationModelRunStore) GetModelRun(context.Context, foundation.ID, foundation.ID) (agentapp.ModelRunRecord, error) {
	return agentapp.ModelRunRecord{Run: store.run, Calls: append([]agentdomain.ModelCall(nil), store.calls...)}, nil
}

func (store *generationModelRunStore) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	store.calls = append(store.calls, call)
	return call, false, nil
}

func (store *generationModelRunStore) CompleteModelCall(_ context.Context, command agentapp.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	index := command.Call.CallNo - 1
	if index < 0 || index >= len(store.calls) || store.calls[index].Version != command.ExpectedVersion {
		return agentdomain.ModelCall{}, false, errors.New("model call conflict")
	}
	store.calls[index] = command.Call
	return command.Call, false, nil
}

func (store *generationModelRunStore) FinalizeModelRun(_ context.Context, command agentapp.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	store.run = command.Run
	return command.Run, false, nil
}

func (*generationModelRunStore) MarkStaleModelCallsUnknown(context.Context, agentapp.UnknownRecoveryQuery) ([]agentdomain.ModelCall, error) {
	return nil, nil
}

func (*generationModelRunStore) MarkStaleModelRunsUnknown(context.Context, agentapp.UnknownRecoveryQuery) ([]agentdomain.ModelRun, error) {
	return nil, nil
}

func (store *generationModelRunStore) succeed(modelRunID foundation.ID, resultType string, completedAt time.Time) {
	if store.run.ID != modelRunID {
		panic("model run binding mismatch")
	}
	store.run.Status = agentdomain.ModelRunSucceeded
	store.run.FinalResultType = resultType
	store.run.Version++
	store.run.UpdatedAt = completedAt
	store.run.CompletedAt = &completedAt
}

type generationIDs struct{ next int }

func (ids *generationIDs) New() (foundation.ID, error) {
	id := semanticID(ids.next)
	ids.next++
	return id, nil
}

type generationClock struct{ next time.Time }

func (clock *generationClock) Now() time.Time {
	at := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return at
}

var (
	_ GenerationStore             = (*generationStoreFake)(nil)
	_ agentapp.ModelRunRepository = (*generationModelRunStore)(nil)
	_ foundation.IDGenerator      = (*generationIDs)(nil)
	_ foundation.Clock            = (*generationClock)(nil)
)
