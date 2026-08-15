//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryListProposalsWithPostgres(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	workspaceID := proposalListID(1)
	otherWorkspaceID := proposalListID(2)
	insertProposalListWorkspace(t, ctx, tx, workspaceID, "proposal-list", now)
	insertProposalListWorkspace(t, ctx, tx, otherWorkspaceID, "proposal-list-other", now)

	firstID := proposalListID(11)
	secondID := proposalListID(12)
	thirdID := proposalListID(13)
	fourthID := proposalListID(14)
	rejected := domain.DecisionRejected
	approved := domain.DecisionApproved
	approvedGitHead := "abcdef0123456789abcdef0123456789abcdef01"
	workflowRunID := proposalListRelatedID(fourthID, "95000000")
	insertProposalListFixture(t, ctx, tx, proposalListFixture{
		ID: firstID, WorkspaceID: workspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady,
		Target: "docs/newest.md", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", CreatedAt: now.Add(4 * time.Hour), UpdatedAt: now.Add(8 * time.Hour),
	})
	insertProposalListFixture(t, ctx, tx, proposalListFixture{
		ID: secondID, WorkspaceID: workspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusRejected,
		Target: "docs/rejected.md", RiskLevel: domain.ProposalRiskLevelHigh, Risk: "high", Decision: &rejected, CreatedAt: now.Add(3 * time.Hour), UpdatedAt: now.Add(7 * time.Hour),
	})
	insertProposalListFixture(t, ctx, tx, proposalListFixture{
		ID: thirdID, WorkspaceID: workspaceID, Type: domain.ProposalTypeKnowledgeChange, Status: domain.StatusApproved,
		RiskLevel: domain.ProposalRiskLevelHigh, Risk: "medium reviewer narrative", Decision: &approved, CreatedAt: now.Add(2 * time.Hour), UpdatedAt: now.Add(6 * time.Hour),
	})
	insertProposalListFixture(t, ctx, tx, proposalListFixture{
		ID: fourthID, WorkspaceID: workspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusApproved,
		Target: "docs/oldest.md", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", Decision: &approved, ApprovedGitHead: &approvedGitHead, WorkflowRunID: workflowRunID,
		CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(5 * time.Hour),
	})
	insertProposalListFixture(t, ctx, tx, proposalListFixture{
		ID: proposalListID(21), WorkspaceID: otherWorkspaceID, Type: domain.ProposalTypeFilePatch, Status: domain.StatusReady,
		Target: "docs/other.md", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", CreatedAt: now.Add(10 * time.Hour), UpdatedAt: now.Add(10 * time.Hour),
	})
	planNow := now.Add(48 * time.Hour)
	seedProposalListPlanFixtures(t, ctx, tx, otherWorkspaceID, planNow)
	assertProposalListPlans(t, ctx, tx, otherWorkspaceID, planNow)

	firstPage, hasMore, err := repository.ListProposals(ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(firstPage) != 2 || firstPage[0].ProposalID != firstID || firstPage[1].ProposalID != secondID {
		t.Fatalf("first page=%#v hasMore=%v", firstPage, hasMore)
	}
	if firstPage[0].RiskLevel != domain.ProposalRiskLevelLow || firstPage[0].Approval != nil || firstPage[0].WorkflowRunID != nil || firstPage[1].RiskLevel != domain.ProposalRiskLevelHigh || firstPage[1].Approval == nil || firstPage[1].Approval.Decision != domain.DecisionRejected || firstPage[1].Approval.ApprovedGitHead != nil || firstPage[1].WorkflowRunID != nil {
		t.Fatalf("first page approval bindings=%#v", firstPage)
	}
	cursorTime := firstPage[1].UpdatedAt
	secondPage, hasMore, err := repository.ListProposals(ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, CursorTime: &cursorTime, CursorID: firstPage[1].ProposalID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(secondPage) != 2 || secondPage[0].ProposalID != thirdID || secondPage[1].ProposalID != fourthID {
		t.Fatalf("second page=%#v hasMore=%v", secondPage, hasMore)
	}
	if secondPage[0].RiskLevel != domain.ProposalRiskLevelHigh || secondPage[0].Risk != "medium reviewer narrative" || secondPage[0].Approval == nil || secondPage[0].Approval.Decision != domain.DecisionApproved || secondPage[0].Approval.ApprovedGitHead != nil || secondPage[0].WorkflowRunID != nil ||
		secondPage[1].Approval == nil || secondPage[1].Approval.ApprovedGitHead == nil || *secondPage[1].Approval.ApprovedGitHead != approvedGitHead || secondPage[1].WorkflowRunID == nil || *secondPage[1].WorkflowRunID != workflowRunID {
		t.Fatalf("second page approval bindings=%#v", secondPage)
	}

	assertProposalListIDs(t, repository, ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, Status: domain.StatusReady, Limit: 10}, firstID)
	assertProposalListIDs(t, repository, ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, Status: domain.StatusApproved, Limit: 10}, thirdID, fourthID)
	assertProposalListIDs(t, repository, ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, Type: domain.ProposalTypeKnowledgeChange, Limit: 10}, thirdID)
	assertProposalListIDs(t, repository, ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, RiskLevel: domain.ProposalRiskLevelLow, Limit: 10}, firstID, fourthID)
	createdAfter := now.Add(150 * time.Minute)
	assertProposalListIDs(t, repository, ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, CreatedAfter: &createdAfter, Limit: 10}, firstID, secondID)
	assertProposalListIDs(t, repository, ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, Status: domain.ProposalStatus("draft"), Limit: 10})
}

type proposalListFixture struct {
	ID              foundation.ID
	WorkspaceID     foundation.ID
	Type            domain.ProposalType
	Status          domain.ProposalStatus
	Target          string
	RiskLevel       domain.ProposalRiskLevel
	Risk            string
	Decision        *domain.Decision
	ApprovedGitHead *string
	WorkflowRunID   foundation.ID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func insertProposalListWorkspace(t *testing.T, ctx context.Context, tx pgx.Tx, id foundation.ID, suffix string, now time.Time) {
	t.Helper()
	root := "/tmp/zhixu-m9-" + suffix
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(id), suffix, root, now); err != nil {
		t.Fatal(err)
	}
}

func insertProposalListFixture(t *testing.T, ctx context.Context, tx pgx.Tx, fixture proposalListFixture) {
	t.Helper()
	revisionID := proposalListRelatedID(fixture.ID, "92000000")
	requestHash := strings.Repeat(string(fixture.ID)[0:1], 64)
	changeHash := strings.Repeat(string(fixture.ID)[1:2], 64)
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at,current_revision_id) VALUES($1,$2,$3,$4,$5,$6,$7,1,$8,$9,$10)`, string(fixture.ID), string(fixture.WorkspaceID), string(fixture.Type), string(fixture.RiskLevel), "list-"+string(fixture.ID), requestHash, string(fixture.Status), fixture.CreatedAt, fixture.UpdatedAt, string(revisionID)); err != nil {
		t.Fatal(err)
	}
	if fixture.Type == domain.ProposalTypeKnowledgeChange {
		if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(id,proposal_id,revision_no,risk,rollback_plan,change_hash,target_refs,base_versions,change_set,evidence_refs,schema_version,created_at) VALUES($1,$2,1,$3,'rollback relation',$4,'[{"node_type":"CLAIM","node_id":"11111111-1111-4111-8111-111111111111"}]','[{"node_type":"CLAIM","node_id":"11111111-1111-4111-8111-111111111111","version":1}]','{"operation":"upsert_relation"}','[{"source_version_id":"22222222-2222-4222-8222-222222222222","source_span_id":"33333333-3333-4333-8333-333333333333"}]','knowledge-relation-change/v1',$5)`, string(revisionID), string(fixture.ID), fixture.Risk, changeHash, fixture.CreatedAt); err != nil {
			t.Fatal(err)
		}
	} else {
		baseHash := strings.Repeat("a", 64)
		if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at) VALUES($1,$2,1,$3,$4,'proposed content','verified evidence',$5,'revert commit',$6,$7)`, string(revisionID), string(fixture.ID), fixture.Target, baseHash, fixture.Risk, changeHash, fixture.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.Decision != nil {
		approvalID := proposalListRelatedID(fixture.ID, "93000000")
		if _, err := tx.Exec(ctx, `INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, string(approvalID), string(fixture.ID), string(revisionID), changeHash, string(*fixture.Decision), fixture.ApprovedGitHead, fixture.UpdatedAt); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.WorkflowRunID != "" {
		definitionID := proposalListRelatedID(fixture.ID, "94000000")
		if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,'{}',$4)`, string(definitionID), string(fixture.WorkspaceID), "proposal-list-"+string(fixture.ID), fixture.CreatedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(fixture.WorkflowRunID), string(fixture.WorkspaceID), string(definitionID), fixture.CreatedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE change_control.proposal SET workflow_run_id=$1,version=version+1 WHERE id=$2`, string(fixture.WorkflowRunID), string(fixture.ID)); err != nil {
			t.Fatal(err)
		}
		approvalID := proposalListRelatedID(fixture.ID, "93000000")
		if _, err := tx.Exec(ctx, `
			INSERT INTO change_control.proposal_revision_dispatch(
				workspace_id,proposal_id,revision_id,approval_id,workflow_run_id,created_at
			) VALUES($1,$2,$3,$4,$5,$6)`,
			string(fixture.WorkspaceID), string(fixture.ID), string(revisionID), string(approvalID), string(fixture.WorkflowRunID), fixture.UpdatedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func assertProposalListIDs(t *testing.T, repository *Repository, ctx context.Context, query domain.ProposalListQuery, want ...foundation.ID) {
	t.Helper()
	items, hasMore, err := repository.ListProposals(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(items) != len(want) {
		t.Fatalf("items=%#v hasMore=%v want=%#v", items, hasMore, want)
	}
	for index := range want {
		if items[index].ProposalID != want[index] || items[index].WorkspaceID != query.WorkspaceID {
			t.Fatalf("items[%d]=%#v want=%s", index, items[index], want[index])
		}
	}
}

func seedProposalListPlanFixtures(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, newest time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at
	)
	SELECT ('9b000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid,
		$1,'file_patch','HIGH','proposal-list-plan-' || value::text,repeat('a',64),'ready_for_review',1,
		$2::timestamptz - value * interval '1 second' - interval '1 hour',
		$2::timestamptz - value * interval '1 second'
	FROM generate_series(1,1024) AS value`, string(workspaceID), newest.UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
	)
	SELECT ('9c000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid,
		('9b000000-0000-4000-8000-' || lpad(value::text,12,'0'))::uuid,
		1,'docs/plan-' || value::text || '.md',repeat('b',64),'content','evidence','HIGH','rollback',repeat('c',64),
		$1::timestamptz - value * interval '1 second'
	FROM generate_series(1,1024) AS value`, newest.UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE change_control.proposal, change_control.proposal_revision, change_control.approval`); err != nil {
		t.Fatal(err)
	}
}

func assertProposalListPlans(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, newest time.Time) {
	t.Helper()
	indexAvailable := proposalListIndexAvailable(t, ctx, tx)
	if !indexAvailable {
		t.Log("proposal list updated_at index is absent; validating the migration-29 compatibility plan separately from the M9 performance gate")
	}
	assertProposalListPlan(t, ctx, tx, "first page", domain.ProposalListQuery{WorkspaceID: workspaceID, Limit: 2}, indexAvailable)
	cursorTime := newest.Add(-256 * time.Second)
	assertProposalListPlan(t, ctx, tx, "cursor page", domain.ProposalListQuery{
		WorkspaceID: workspaceID,
		CursorTime:  &cursorTime,
		CursorID:    proposalListPlanID(256),
		Limit:       2,
	}, indexAvailable)
}

func proposalListIndexAvailable(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "change_control.idx_proposal_workspace_updated_id").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func assertProposalListPlan(t *testing.T, ctx context.Context, tx pgx.Tx, label string, request domain.ProposalListQuery, performanceIndexAvailable bool) {
	t.Helper()
	query, args := buildProposalListQuery(request)
	var raw []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan proposalListExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid proposal list explain json: %v %s", err, raw)
	}
	if documents[0].Plan.NodeType != "Limit" {
		t.Fatalf("proposal %s plan is not bounded by Limit: %s", label, raw)
	}
	if performanceIndexAvailable {
		assertProposalListRootIndexPath(t, documents[0].Plan, raw)
	} else if !proposalListPlanHasRootRelation(documents[0].Plan) {
		t.Fatalf("proposal %s compatibility plan has no proposal relation: %s", label, raw)
	}
	t.Logf("proposal %s plan: %s", label, raw)
}

type proposalListExplainPlan struct {
	NodeType     string                    `json:"Node Type"`
	RelationName string                    `json:"Relation Name"`
	Alias        string                    `json:"Alias"`
	IndexName    string                    `json:"Index Name"`
	Plans        []proposalListExplainPlan `json:"Plans"`
}

func assertProposalListRootIndexPath(t *testing.T, plan proposalListExplainPlan, raw []byte) {
	t.Helper()
	const expectedIndex = "idx_proposal_workspace_updated_id"
	found := 0
	var visit func(proposalListExplainPlan, []proposalListExplainPlan)
	visit = func(node proposalListExplainPlan, path []proposalListExplainPlan) {
		if node.RelationName == "proposal" && node.Alias == "p" {
			found++
			if (node.NodeType != "Index Scan" && node.NodeType != "Index Only Scan") || node.IndexName != expectedIndex {
				t.Fatalf("proposal root does not use %s via an index scan: %s", expectedIndex, raw)
			}
			for _, ancestor := range path {
				if strings.Contains(ancestor.NodeType, "Sort") {
					t.Fatalf("proposal root path contains %s: %s", ancestor.NodeType, raw)
				}
			}
		}
		nextPath := append(append([]proposalListExplainPlan(nil), path...), node)
		for _, child := range node.Plans {
			visit(child, nextPath)
		}
	}
	visit(plan, nil)
	if found != 1 {
		t.Fatalf("proposal root scan count=%d, want 1: %s", found, raw)
	}
}

func proposalListPlanHasRootRelation(plan proposalListExplainPlan) bool {
	if plan.RelationName == "proposal" && plan.Alias == "p" {
		return true
	}
	for _, child := range plan.Plans {
		if proposalListPlanHasRootRelation(child) {
			return true
		}
	}
	return false
}

func proposalListID(n int) foundation.ID {
	return foundation.ID(strings.Replace("91000000-0000-4000-8000-000000000000", "000000000000", strings.Repeat("0", 10)+string(rune('0'+n/10))+string(rune('0'+n%10)), 1))
}

func proposalListRelatedID(id foundation.ID, prefix string) foundation.ID {
	return foundation.ID(prefix + string(id)[8:])
}

func proposalListPlanID(n int) foundation.ID {
	return foundation.ID("9b000000-0000-4000-8000-" + fmt.Sprintf("%012d", n))
}
