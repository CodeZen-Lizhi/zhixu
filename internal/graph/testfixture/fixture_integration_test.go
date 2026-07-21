//go:build integration

package testfixture

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFunctionalFixtureCommitsAndCleansCanonicalFacts(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture, err := SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := Cleanup(cleanupCtx, pool, fixture.WorkspaceID); err != nil {
			t.Errorf("clean functional graph fixture: %v", err)
		}
	})

	var topics, claims, relations, evidence int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM core.topic WHERE workspace_id=$1),
		(SELECT count(*) FROM core.claim WHERE workspace_id=$1),
		(SELECT count(*) FROM core.relation WHERE workspace_id=$1),
		(SELECT count(*) FROM core.relation_evidence WHERE workspace_id=$1)`,
		string(fixture.WorkspaceID),
	).Scan(&topics, &claims, &relations, &evidence); err != nil {
		t.Fatal(err)
	}
	if topics != 2 || claims != 2 || relations != 4 || evidence != 4 {
		t.Fatalf("unexpected committed fixture counts: topics=%d claims=%d relations=%d evidence=%d", topics, claims, relations, evidence)
	}

	if err := Cleanup(ctx, pool, fixture.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(ctx, pool, fixture.WorkspaceID); err != nil {
		t.Fatalf("cleanup must be idempotent: %v", err)
	}
	var workspaces int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.workspace WHERE id=$1`, string(fixture.WorkspaceID)).Scan(&workspaces); err != nil {
		t.Fatal(err)
	}
	if workspaces != 0 {
		t.Fatalf("fixture workspace remains after cleanup: %d", workspaces)
	}

	unmarkedWorkspaceID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	unmarkedRoot := "/tmp/not-a-graph-fixture-" + string(unmarkedWorkspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'not-a-graph-fixture',$2,$2,$3,'test',1,$3,$3)`,
		string(unmarkedWorkspaceID), unmarkedRoot, time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM core.workspace WHERE id=$1`, string(unmarkedWorkspaceID)); cleanupErr != nil {
			t.Errorf("clean unmarked workspace: %v", cleanupErr)
		}
	})
	if err := Cleanup(ctx, pool, unmarkedWorkspaceID); err == nil {
		t.Fatal("cleanup accepted an unmarked workspace")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.workspace WHERE id=$1`, string(unmarkedWorkspaceID)).Scan(&workspaces); err != nil {
		t.Fatal(err)
	}
	if workspaces != 1 {
		t.Fatalf("cleanup removed an unmarked workspace: %d", workspaces)
	}
}

func TestSemanticLinkBrowserFixtureAddsDiscoverableClaimWithoutFormalRelation(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture, err := SeedSemanticLinkBrowser(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := CleanupSemanticLinkBrowser(cleanupCtx, pool, fixture.WorkspaceID); err != nil {
			t.Errorf("clean semantic-link browser fixture: %v", err)
		}
	})

	var claims, memberships, claimRelations int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM core.claim WHERE workspace_id=$1),
		(SELECT count(*) FROM core.relation WHERE workspace_id=$1 AND relation_type='BELONGS_TO' AND status='CONFIRMED'),
		(SELECT count(*) FROM core.relation WHERE workspace_id=$1 AND source_node_type='CLAIM' AND target_node_type='CLAIM'
			AND (source_node_id=$2 OR target_node_id=$2) AND status IN ('CONFIRMED','STALE'))`,
		string(fixture.WorkspaceID), string(fixture.DiscoveryClaimID),
	).Scan(&claims, &memberships, &claimRelations); err != nil {
		t.Fatal(err)
	}
	if claims != 3 || memberships != 4 || claimRelations != 0 {
		t.Fatalf("fixture counts claims=%d memberships=%d discovery_claim_relations=%d", claims, memberships, claimRelations)
	}
}
