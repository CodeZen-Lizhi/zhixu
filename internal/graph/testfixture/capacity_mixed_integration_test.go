//go:build integration

package testfixture

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM10MixedCapacityFixtureReferenceWindow(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to run the mixed Graph capacity fixture integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	shape := m10MixedCapacityShape
	shape.topicCount, shape.claimCount = 300, 1_000
	shape.relationCount, shape.evidenceCount, shape.hotCenterDegree = 5_000, 5_000, 99
	fixture, err := seedCapacity(ctx, pool, "m10-mixed-reference-window-v1", shape)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if cleanupErr := CleanupCapacityFixture(cleanupContext, pool, fixture); cleanupErr != nil {
			t.Errorf("cleanup mixed Graph capacity fixture: %v", cleanupErr)
		}
	})
	if fixture.TopicCount != 300 || fixture.ClaimCount != 1_000 || fixture.RelationCount != 5_000 ||
		fixture.BelongsToCount != 2_500 || fixture.ImpactsCount != 2_500 || fixture.HotCenterDegree != 99 {
		t.Fatalf("unexpected mixed Graph fixture: %#v", fixture)
	}
}
