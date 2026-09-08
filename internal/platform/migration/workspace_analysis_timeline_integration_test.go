//go:build integration

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisTimelineProjectsAuthoritativeSafeSnapshot(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	applyWorkspaceAnalysisPublicationMigration(t, ctx, pool)
	insertWorkspaceAnalysisPublicationReadyFixture(t, ctx, pool)
	publishWorkspaceAnalysisCompletedProof(t, ctx, pool, "83000000-0000-4000-8000-000000000190", workspaceAnalysisPublicationDocument)

	var watermark int64
	if err := pool.QueryRow(ctx, `INSERT INTO ops.server_event(
		workspace_id,conversation_id,workflow_run_id,event_type,resource_ref,resource_version,
		payload_summary,schema_version,source_event_ref,occurred_at,expires_at
	) SELECT
		'83000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000002',
		'83000000-0000-4000-8000-000000000012','workspace_analysis.terminated',
		'answer:83000000-0000-4000-8000-000000000013',2,'{"status":"completed"}',1,
		'workspace_analysis.timeline:83000000-0000-4000-8000-000000000014:v2',clock.at,clock.at+interval '24 hours'
	FROM (SELECT clock_timestamp() AS at) AS clock
	RETURNING seq`).Scan(&watermark); err != nil {
		t.Fatal(err)
	}

	runtime := openMigrationRuntimePool(t, ctx, pool)
	defer runtime.Close()
	events, err := eventspostgres.NewGORMStore(runtime)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := conversationpostgres.NewGORMRepository(runtime, events)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := foundation.ID("83000000-0000-4000-8000-000000000001")
	answerID := foundation.ID("83000000-0000-4000-8000-000000000013")
	timeline, err := repository.GetWorkspaceAnalysisTimeline(ctx, conversationapplication.WorkspaceAnalysisTimelineQuery{
		WorkspaceID: workspaceID, AnswerID: answerID,
	})
	if err != nil {
		t.Fatalf("GetWorkspaceAnalysisTimeline(): %v", err)
	}
	completed := conversationdomain.WorkspaceAnalysisCompleted
	if timeline.WorkspaceID != workspaceID || timeline.AnswerID != answerID ||
		timeline.AnalysisRunID != "83000000-0000-4000-8000-000000000014" ||
		timeline.RunStatus != conversationdomain.WorkspaceAnalysisTimelineRunSucceeded ||
		timeline.TerminationReason == nil || *timeline.TerminationReason != completed ||
		timeline.LatestServerEventSequence != watermark || len(timeline.Items) != 13 {
		t.Fatalf("timeline envelope=%#v items=%d watermark=%d", timeline, len(timeline.Items), watermark)
	}
	if timeline.Budget.ModelCalls.Used != 3 || timeline.Budget.ToolCalls.Used != 4 ||
		timeline.Budget.SourceReads.Used != 1 || timeline.Budget.SourceReads.Max != 3 ||
		timeline.Budget.InputTokens.Used != 30 || timeline.Budget.OutputTokens.Used != 15 ||
		timeline.Budget.OutputTokens.Max != 5376 || timeline.Budget.EstimatedCostMicrounits != nil {
		t.Fatalf("timeline budget=%#v", timeline.Budget)
	}

	var git, search, source, citation, model int
	for index, item := range timeline.Items {
		if item.Sequence != index+1 {
			t.Fatalf("item sequence[%d]=%d", index, item.Sequence)
		}
		if item.Summary == nil {
			continue
		}
		switch item.Summary.Kind {
		case conversationdomain.WorkspaceAnalysisTimelineSummaryGit:
			git++
			if item.Summary.Git.Branch != "main" || !item.Summary.Git.Clean {
				t.Fatalf("Git summary=%#v", item.Summary.Git)
			}
		case conversationdomain.WorkspaceAnalysisTimelineSummarySearch:
			search++
			if item.Summary.Search.HitCount != 1 || len(item.Summary.Search.DegradationCodes) != 0 {
				t.Fatalf("Search summary=%#v", item.Summary.Search)
			}
		case conversationdomain.WorkspaceAnalysisTimelineSummarySource:
			source++
			if item.Summary.Source.EvidenceRef != "E1" || item.Summary.Source.ContentHash != strings.Repeat("a", 64) {
				t.Fatalf("Source summary=%#v", item.Summary.Source)
			}
		case conversationdomain.WorkspaceAnalysisTimelineSummaryCitation:
			citation++
			if item.Summary.Citation.ValidCount != 1 || item.Summary.Citation.InvalidCount != 0 ||
				len(item.Summary.Citation.ReasonCodes) != 1 || item.Summary.Citation.ReasonCodes[0] != "OK" {
				t.Fatalf("Citation summary=%#v", item.Summary.Citation)
			}
		case conversationdomain.WorkspaceAnalysisTimelineSummaryModelUsage:
			model++
		}
	}
	if git != 1 || search != 1 || source != 1 || citation != 1 || model != 3 {
		t.Fatalf("summary counts git=%d search=%d source=%d citation=%d model=%d", git, search, source, citation, model)
	}
	document, err := json.Marshal(timeline)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"snippet", "excerpt", "server_binding", "private_binding", "candidate_body", "prompt",
		"publication-index", "83000000-0000-4000-8000-000000000138", "cite-b6f57b12",
	} {
		if strings.Contains(string(document), forbidden) {
			t.Fatalf("timeline leaked %q: %s", forbidden, document)
		}
	}

	_, err = repository.GetWorkspaceAnalysisTimeline(ctx, conversationapplication.WorkspaceAnalysisTimelineQuery{
		WorkspaceID: "83000000-0000-4000-8000-000000000002", AnswerID: answerID,
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNotFound ||
		classified.Code != conversationpostgres.ErrorCodeAnswerNotFound {
		t.Fatalf("cross-workspace error=%#v", err)
	}
}
