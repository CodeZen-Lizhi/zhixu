//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestInterviewRepositoryClaimOnlyListFiltersBeforeLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 55*time.Second)
	defer cancel()
	repository, _, pool := (interviewIntegrationVariant{}).open(t)
	seedInterviewPersistence(t, ctx, pool)
	workspaceID := interviewIntegrationID(1)
	startedAt := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	for index := 0; index < 6; index++ {
		sessionID := interviewIntegrationID(60 + index)
		key := fmt.Sprintf("interview-source-list-%d", index)
		record := interviewListStartRecord(t, workspaceID, sessionID, interviewIntegrationID(70+index), startedAt.Add(time.Duration(index/2)*time.Minute), key)
		if index%2 == 0 {
			if _, err := repository.Start(ctx, record); err != nil {
				t.Fatal(err)
			}
			continue
		}
		ref := domain.NoteRevisionRef{WorkspaceID: workspaceID, NoteID: interviewIntegrationID(201), RevisionID: interviewIntegrationID(202), DocumentID: interviewIntegrationID(203), ArticleRevisionID: interviewIntegrationID(204), RevisionNo: 1, ArticleRevisionNo: 1, ContentHash: strings.Repeat("a", 64), ProjectionHash: strings.Repeat("b", 64), Title: "Frozen note scope"}
		record.Session.Config.Scope = domain.Scope{NoteRevision: &ref}
		if err := domain.ValidateSession(record.Session); err != nil {
			t.Fatal(err)
		}
		config, err := json.Marshal(record.Session.Config)
		if err != nil {
			t.Fatal(err)
		}
		// List reads the frozen shell only. A shell plus Interview child is enough
		// to exercise the real source predicate without fabricating Claim material.
		_, err = pool.Exec(ctx, `WITH shell AS (
			INSERT INTO learning.review_session(id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at)
			VALUES($1::uuid,$2::uuid,NULL,'INTERVIEW','ACTIVE',$3::jsonb,$4,$5,$6::timestamptz)
			RETURNING id,workspace_id,started_at
		) INSERT INTO learning.interview_session(session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at)
		  SELECT id,workspace_id,$7,1,0,started_at,started_at FROM shell`, string(sessionID), string(workspaceID), string(config), key, record.RequestHash, record.Session.StartedAt, domain.SchemaVersion)
		if err != nil {
			t.Fatal(err)
		}
	}

	counter := installInterviewStatementCounter(t, repository)
	query := interviewapp.SessionListQuery{WorkspaceID: workspaceID, Limit: 2, ClaimOnly: true}
	counter.reset()
	first, err := repository.List(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != interviewIntegrationID(64) || first.Items[1].ID != interviewIntegrationID(62) || first.Next == nil || first.Next.ID != interviewIntegrationID(62) {
		t.Fatalf("Claim page must be filled after filtering mixed sources: %+v", first)
	}
	if counter.statements() != 1 {
		t.Fatalf("filtered List statements=%d, want one bounded query", counter.statements())
	}
	query.After = first.Next
	last, err := repository.List(ctx, query)
	if err != nil || len(last.Items) != 1 || last.Items[0].ID != interviewIntegrationID(60) || last.Next != nil {
		t.Fatalf("Claim second page=%+v error=%v", last, err)
	}
	query.ClaimOnly, query.After = false, nil
	all, err := repository.List(ctx, query)
	if err != nil || len(all.Items) != 2 || all.Items[0].ID != interviewIntegrationID(65) || all.Items[0].Config.Scope.NoteRevision == nil || all.Items[1].ID != interviewIntegrationID(64) {
		t.Fatalf("unrestricted list lost NOTE or Claim sources: page=%+v error=%v", all, err)
	}
	query.ClaimOnly, query.WorkspaceID = true, interviewIntegrationID(99)
	empty, err := repository.List(ctx, query)
	if err != nil || len(empty.Items) != 0 || empty.Next != nil {
		t.Fatalf("source filter escaped Workspace: page=%+v error=%v", empty, err)
	}

	// The source constraint also protects direct Store entry: without it a
	// manual completion would create a durable reservation before any replay.
	noteSession := interviewIntegrationID(65)
	before := interviewRowCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_completion_reservation WHERE workspace_id=$1`, string(workspaceID))
	for _, phase := range []string{"begin", "prepare"} {
		var err error
		if phase == "begin" {
			_, err = repository.BeginComplete(ctx, interviewapp.BeginCompleteRecord{ClaimOnly: true, WorkspaceID: workspaceID, SessionID: noteSession, ManualEnd: true, IdempotencyKey: "reject-note-complete", RequestHash: strings.Repeat("c", 64)})
		} else {
			_, err = repository.PrepareComplete(ctx, interviewapp.PrepareCompleteRecord{ClaimOnly: true, WorkspaceID: workspaceID, SessionID: noteSession, ManualEnd: true, IdempotencyKey: "reject-note-complete", RequestHash: strings.Repeat("c", 64), SnapshotVersion: 1, ArtifactDigest: strings.Repeat("d", 64)})
		}
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != interviewapp.ErrorCodeAPIVersionUnsupported || classified.Kind != foundation.ErrorVersionConflict {
			t.Fatalf("%s did not enforce its persisted source: %v", phase, err)
		}
	}
	if after := interviewRowCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_completion_reservation WHERE workspace_id=$1`, string(workspaceID)); after != before {
		t.Fatalf("rejected source persisted a completion reservation: before=%d after=%d", before, after)
	}
}
