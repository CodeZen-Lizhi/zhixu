package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestWorkspaceAnalysisCandidateDraftProjectsOnlyValidatedMarkdown(t *testing.T) {
	store := newDraftStreamStoreFake()
	loader := &workspaceAnalysisCandidateDraftLoader{session: store.session}
	coordinator, err := NewWorkspaceAnalysisCandidateDraftCoordinator(
		WorkspaceAnalysisCandidateDraftCoordinatorDependencies{Store: store, Loader: loader},
	)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := coordinator.Begin(context.Background(), draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	document := workspaceAnalysisCandidateDraftDocument(t, "Grounded answer [E1].")
	middle := len(document) / 2
	for _, chunk := range []string{string(document[:middle]), string(document[middle:])} {
		if err := draft.Append(context.Background(), agentapplication.WorkspaceAnalysisCandidateStreamChunk{Content: chunk}); err != nil {
			t.Fatal(err)
		}
	}
	session, err := draft.Complete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != agentapplication.DraftStreamCompleted {
		t.Fatalf("session = %#v", session)
	}
	if ttl := store.beginTTL(); ttl != agentapplication.WorkspaceAnalysisV1MaxRunDuration {
		t.Fatalf("Workspace Analysis draft TTL = %s, want %s", ttl, agentapplication.WorkspaceAnalysisV1MaxRunDuration)
	}
	visible := strings.Join(store.contents(), "")
	if visible != "Grounded answer [E1]." || strings.Contains(visible, "result_type") || strings.Contains(visible, "citation_refs") {
		t.Fatalf("draft visible content = %q", visible)
	}
}

func TestWorkspaceAnalysisCandidateDraftRejectsInvalidEnvelopeBeforeProjection(t *testing.T) {
	store := newDraftStreamStoreFake()
	coordinator, err := NewWorkspaceAnalysisCandidateDraftCoordinator(
		WorkspaceAnalysisCandidateDraftCoordinatorDependencies{
			Store: store, Loader: &workspaceAnalysisCandidateDraftLoader{session: store.session},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := coordinator.Begin(context.Background(), draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := draft.Append(context.Background(), agentapplication.WorkspaceAnalysisCandidateStreamChunk{Content: `{"answer_markdown":"leak"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := draft.Complete(context.Background()); err == nil {
		t.Fatal("Complete accepted an invalid candidate envelope")
	}
	if contents := store.contents(); len(contents) != 0 {
		t.Fatalf("invalid candidate reached Draft: %#v", contents)
	}
	if err := draft.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceAnalysisCandidateDraftCoordinatorLoadsExactTerminalSession(t *testing.T) {
	store := newDraftStreamStoreFake()
	want := store.session
	want.Status = agentapplication.DraftStreamDegraded
	loader := &workspaceAnalysisCandidateDraftLoader{session: want}
	coordinator, err := NewWorkspaceAnalysisCandidateDraftCoordinator(
		WorkspaceAnalysisCandidateDraftCoordinatorDependencies{Store: store, Loader: loader},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := coordinator.Load(context.Background(), agentapplication.WorkspaceAnalysisCandidateDraftQuery{
		WorkspaceID: want.Binding.WorkspaceID, AnswerID: want.Binding.AnswerID, NodeAttemptID: want.Binding.NodeAttemptID,
	})
	if err != nil || got != want || loader.calls != 1 {
		t.Fatalf("Load = %#v, %v calls=%d", got, err, loader.calls)
	}
}

type workspaceAnalysisCandidateDraftLoader struct {
	session agentapplication.DraftStreamSession
	calls   int
}

func (loader *workspaceAnalysisCandidateDraftLoader) LoadWorkspaceAnalysisCandidateDraftSession(
	context.Context,
	agentapplication.WorkspaceAnalysisCandidateDraftQuery,
) (agentapplication.DraftStreamSession, error) {
	loader.calls++
	return loader.session, nil
}

func workspaceAnalysisCandidateDraftDocument(t *testing.T, markdown string) []byte {
	t.Helper()
	document, err := json.Marshal(agentdomain.WorkspaceAnalysisCandidateProviderResult{
		ResultType: agentdomain.ResultTypeWorkspaceAnalysisCandidate,
		SchemaID:   agentdomain.WorkspaceAnalysisCandidateSchemaID, SchemaVersion: "1",
		Payload: agentdomain.WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: markdown, CitationRefs: []string{"E1"}, ProposalSuggestion: nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}
