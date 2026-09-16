//go:build integration

package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestAnchorModelRecordsActualCallsAndRecommendationOutcomes(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 103)
	ctx := t.Context()
	generation := f.generation(t, 110000, nil, "Redis provides cache expiration.")
	generation.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(generation, "redis model")}
	applied, err := f.service.ApplyGeneration(ctx, generation.Input, generation.Generation)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.GetSynthesisNote(ctx, f.workspace, applied.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewAnchorRecommendationModelVerifier(runs)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMAnchorStore(f.platform, f.sources, verifier)
	if err != nil {
		t.Fatal(err)
	}
	catalog := agentapp.NewRuntimeCatalog()
	if err := organizingagent.RegisterAnchorRecommendationRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "anchor-model-test", Version: "v1"}, Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "anchor", ModelVersion: "v1"}, Timeout: time.Second, MaxOutputTokens: 2048}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var anchor *domain.Anchor
	cases := []struct {
		name, raw string
		expected  domain.AnchorRecommendationStatus
		failure   bool
	}{
		{"initial", " \n" + `{"recommendation":{"title":"Redis","kind":"INITIAL_SCOPE","scope":{"topics":["Redis"],"audiences":["review"],"description":"Redis cache review"},"reason":"The note describes Redis expiration.","evidence":["S001"]},"no_recommendation":false}` + "\n", domain.AnchorRecommendationSucceeded, false},
		{"none", `{"recommendation":null,"no_recommendation":true}`, domain.AnchorRecommendationNoRecommendation, false},
		{"association", `{"recommendation":{"title":"Redis","kind":"SOURCE_ASSOCIATION","scope":null,"reason":"Redis expiration belongs to this scope.","evidence":["S001"]},"no_recommendation":false}`, domain.AnchorRecommendationSucceeded, false},
		{"provider failure", "", domain.AnchorRecommendationFailed, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var request app.AnchorRecommendationRequest
			if anchor == nil || tc.name == "none" {
				request, err = store.RequestInitialAnchorRecommendation(ctx, app.RequestInitialAnchorRecommendationCommand{WorkspaceID: f.workspace, NoteID: detail.Note.ID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, IdempotencyKey: "model-" + tc.name})
			} else {
				request, err = store.RequestAnchorSourceRecommendation(ctx, app.RequestAnchorSourceRecommendationCommand{WorkspaceID: f.workspace, AnchorID: anchor.ID, ExpectedScopeVersion: anchor.ScopeVersion, Source: generation.Input.Sources[0].Reference.Source, Evidence: []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference}, IdempotencyKey: "model-" + tc.name})
			}
			if err != nil {
				t.Fatal(err)
			}
			seed := 111000 + i*1000
			workflow := f.generation(t, seed+100, nil, "dedicated recommendation workflow "+tc.name).Input.WorkflowRunID
			node, attempt := organizingIntegrationID(seed+1), organizingIntegrationID(seed+2)
			now := time.Now().UTC().Truncate(time.Microsecond)
			seedOrganizingRunningNodeAttempt(t, ctx, f.platform.DB(), workflow, node, attempt, "anchor-recommendation", now.Add(time.Hour), now)
			step := agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: profile.Model, Content: []byte(tc.raw), Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}}}
			if tc.failure {
				step = agentapp.DeterministicChatStep{Err: foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_PROVIDER_UNAVAILABLE", true, nil)}
			}
			provider := agentapp.NewDeterministicChatModel(step)
			model, err := organizingagent.NewAnchorModel(organizingagent.AnchorModelDependencies{Model: provider, ModelRuns: runs, Store: store, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
			if err != nil {
				t.Fatal(err)
			}
			var target *domain.Anchor
			if request.AnchorID != "" {
				target = anchor
			}
			views := make([]app.SynthesisSourceView, len(request.Evidence))
			for i, ref := range request.Evidence {
				views[i], err = f.sources.OpenSynthesisSource(ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
			}
			completed, err := model.Recommend(ctx, workflowapp.ExecutionContext{WorkspaceID: f.workspace, RunID: workflow, NodeRunID: node, NodeAttemptID: attempt}, request, *detail.CurrentRevision, target, views)
			if tc.failure && err == nil || !tc.failure && err != nil {
				t.Fatalf("recommend: %v", err)
			}
			persisted, readErr := store.GetAnchorRecommendation(ctx, f.workspace, request.ID)
			if readErr != nil || persisted.Status != tc.expected || provider.CallCount() != 1 {
				t.Fatalf("persisted=%+v calls=%d err=%v modelerr=%v", persisted, provider.CallCount(), readErr, err)
			}
			if tc.failure {
				retried, err := store.RetryAnchorRecommendation(ctx, app.RetryAnchorRecommendationCommand{WorkspaceID: f.workspace, RequestID: request.ID, ExpectedVersion: persisted.Version, IdempotencyKey: "retry-provider"})
				if err != nil || retried.Status != domain.AnchorRecommendationPending || retried.ModelInputHash != "" || retried.ModelRunID != "" {
					t.Fatalf("retry=%+v err=%v", retried, err)
				}
				replay, err := store.RetryAnchorRecommendation(ctx, app.RetryAnchorRecommendationCommand{WorkspaceID: f.workspace, RequestID: request.ID, ExpectedVersion: persisted.Version, IdempotencyKey: "retry-provider"})
				if err != nil || replay.Version != retried.Version {
					t.Fatalf("retry replay=%+v err=%v", replay, err)
				}
				return
			}
			if completed.Status != tc.expected || !bytes.Equal(persisted.ModelOutput, []byte(tc.raw)) {
				t.Fatal("completion changed state or original model bytes")
			}
			recorded, err := runs.GetModelRun(ctx, f.workspace, persisted.ModelRunID)
			if err != nil || len(recorded.Calls) != 1 {
				t.Fatalf("model audit=%+v err=%v", recorded, err)
			}
			actual, err := json.Marshal(provider.Calls()[0])
			if err != nil || recorded.Calls[0].RequestHash != sha256Hex(actual) || persisted.ModelInputHash != recorded.Calls[0].RequestHash {
				t.Fatal("request proof did not bind actual provider request")
			}
			if persisted.Kind == app.AnchorInitialScopeRecommendation && persisted.ProposalID != "" {
				t.Fatal("initial inference created a proposal")
			}
			if tc.name == "initial" {
				f.count(t, "organizing.knowledge_anchor", "note_id", string(detail.Note.ID), 0)
				created, err := store.CreateAnchor(ctx, app.CreateAnchorCommand{WorkspaceID: f.workspace, IdempotencyKey: "confirm-initial", NoteID: detail.Note.ID, ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, Title: "Redis", Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"review"}, Description: "Redis cache review"}})
				if err != nil {
					t.Fatal(err)
				}
				anchor = &created.Anchor
			}
			if tc.name == "association" {
				if persisted.ProposalID == "" {
					t.Fatal("association suggestion missing")
				}
				f.count(t, "organizing.anchor_fusion_request", "anchor_id", string(anchor.ID), 0)
			}
		})
	}
}
