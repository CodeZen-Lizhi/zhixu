//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCollectionDurableScanRestartsWithStructuredKeysetAndFailsClosedOnDrift(t *testing.T) {
	runCollectionRepositoryIntegrationCases(t, testCollectionDurableScanRestartsWithStructuredKeysetAndFailsClosedOnDrift)
}

func testCollectionDurableScanRestartsWithStructuredKeysetAndFailsClosedOnDrift(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	pool := testCase.pool
	fixture := seedCollectionQueryFixture(t, ctx, pool)
	now := time.Now().UTC()
	service, err := collectionapp.NewService(collectionapp.Dependencies{
		Repository: testCase.repository,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	query := domain.Query{
		SchemaVersion: domain.QuerySchemaVersionV1,
		Root: domain.Clause{
			Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND),
			Clauses: []domain.Clause{{
				Kind: domain.ClauseKindPredicate, Field: "object_type", Operator: string(domain.OperatorIN),
				Values: []json.RawMessage{json.RawMessage(`"CLAIM"`), json.RawMessage(`"TOPIC"`)},
			}},
		},
	}
	created, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID:    fixture.WorkspaceID,
		Name:           "durable scan fixture",
		Query:          query,
		ViewType:       domain.ViewTypeList,
		IdempotencyKey: "durable-scan-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.PlanDurableScan(ctx, fixture.WorkspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ExactCount != 4 || binding.CollectionVersion != 1 || len(binding.QueryHash) != 64 || len(binding.ReadModelRevision) != 64 {
		t.Fatalf("binding=%+v", binding)
	}
	first, err := service.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{Binding: binding, Limit: 2, PairTargetLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.Next == nil || len(first.Items) != 2 || first.Items[0].ObjectType != "CLAIM" || first.Items[1].ObjectType != "CLAIM" || len(first.Pairs) != 5 || !containsMixedDurablePair(first.Pairs) {
		t.Fatalf("first page=%+v", first)
	}

	// A fresh Repository has a different random HTTP cursor key. Structured scan
	// checkpoints must remain readable because they do not depend on that key.
	restartedService, err := collectionapp.NewService(collectionapp.Dependencies{
		Repository: testCase.reopen(t),
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := restartedService.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{Binding: binding, After: first.Next, Limit: 2, PairTargetLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Complete || second.Next != nil || len(second.Items) != 2 || second.Items[0].ObjectType != "TOPIC" || second.Items[1].ObjectType != "TOPIC" || len(second.Pairs) != 1 {
		t.Fatalf("second page=%+v", second)
	}
	seenPairs := make(map[collectionapp.DurableScanPair]struct{}, 6)
	for _, page := range []collectionapp.DurableScanPage{first, second} {
		for _, pair := range page.Pairs {
			if _, duplicate := seenPairs[pair]; duplicate {
				t.Fatalf("duplicate pair=%+v", pair)
			}
			seenPairs[pair] = struct{}{}
		}
	}
	if len(seenPairs) != 6 {
		t.Fatalf("pair count=%d want=6", len(seenPairs))
	}
	if err := verifyCollectionDurableBinding(t, testCase, binding); err != nil {
		t.Fatalf("valid start binding verification: %v", err)
	}
	verifyCollectionDurableBindingRollbackOwnership(t, testCase, binding)

	for name, mutate := range map[string]func(collectionapp.DurableScanBinding) collectionapp.DurableScanBinding{
		"version": func(value collectionapp.DurableScanBinding) collectionapp.DurableScanBinding {
			value.CollectionVersion++
			return value
		},
		"query hash": func(value collectionapp.DurableScanBinding) collectionapp.DurableScanBinding {
			value.QueryHash = strings.Repeat("c", 64)
			return value
		},
		"revision": func(value collectionapp.DurableScanBinding) collectionapp.DurableScanBinding {
			value.ReadModelRevision = strings.Repeat("d", 64)
			return value
		},
		"count": func(value collectionapp.DurableScanBinding) collectionapp.DurableScanBinding {
			value.ExactCount++
			return value
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, readErr := restartedService.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{Binding: mutate(binding), Limit: 2})
			if !hasCollectionCode(readErr, collectionapp.ErrorCodeCursorStale) {
				t.Fatalf("drift error=%v", readErr)
			}
		})
	}

	newTopicID := integrationID(t, "30000000-0000-4000-8000-000000000299")
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
		id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
	) VALUES($1,$2,'Durable revision drift','durable revision drift','revision changed','ACTIVE',1,$3,$3)`, string(newTopicID), string(fixture.WorkspaceID), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := verifyCollectionDurableBinding(t, testCase, binding); !hasCollectionCode(err, collectionapp.ErrorCodeCursorStale) {
		t.Fatalf("plan-to-start drift verification error=%v", err)
	}
	if _, err := restartedService.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{Binding: binding, Limit: 2}); !hasCollectionCode(err, collectionapp.ErrorCodeCursorStale) {
		t.Fatalf("real read-model drift error=%v", err)
	}
}

func TestCollectionDurableScanIgnoresOwnHealthOutputsUnlessHealthDefinesMembership(t *testing.T) {
	runCollectionRepositoryIntegrationCases(t, testCollectionDurableScanIgnoresOwnHealthOutputsUnlessHealthDefinesMembership)
}

func testCollectionDurableScanIgnoresOwnHealthOutputsUnlessHealthDefinesMembership(t *testing.T, testCase collectionIntegrationCase) {
	ctx := testCase.context
	pool := testCase.pool
	fixture := seedCollectionQueryFixture(t, ctx, pool)
	now := time.Now().UTC()
	service, err := collectionapp.NewService(collectionapp.Dependencies{
		Repository: testCase.repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimQuery := domain.Query{
		SchemaVersion: domain.QuerySchemaVersionV1,
		Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{{
			Kind: domain.ClauseKindPredicate, Field: "object_type", Operator: string(domain.OperatorEQ), Value: json.RawMessage(`"CLAIM"`),
		}}},
	}
	created, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: fixture.WorkspaceID, Name: "health output independent membership", Query: claimQuery,
		ViewType: domain.ViewTypeList, IdempotencyKey: "durable-health-independent-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.PlanDurableScan(ctx, fixture.WorkspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: created.Collection.ID, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if result.ScanRevisionHash != binding.ReadModelRevision || result.RevisionHash == result.ScanRevisionHash {
		t.Fatalf("result revisions=%s/%s durable=%s", result.RevisionHash, result.ScanRevisionHash, binding.ReadModelRevision)
	}
	first, err := service.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{Binding: binding, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.Next == nil || len(first.Items) != 1 {
		t.Fatalf("first page=%+v", first)
	}
	insertDurableHealthIssue(t, ctx, pool, fixture.WorkspaceID, fixture.FirstClaimID, "ORPHAN", "health.detector.orphan/v1", "a", now.Add(time.Second))
	insertDurableHealthIssue(t, ctx, pool, fixture.WorkspaceID, fixture.SecondClaimID, "LOW_CONFIDENCE", "health.detector.low-confidence/v1", "b", now.Add(2*time.Second))
	refreshed, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: created.Collection.ID, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RevisionHash == result.RevisionHash || refreshed.ScanRevisionHash != binding.ReadModelRevision {
		t.Fatalf("health output revisions before=%s/%s after=%s/%s", result.RevisionHash, result.ScanRevisionHash, refreshed.RevisionHash, refreshed.ScanRevisionHash)
	}
	second, err := service.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{Binding: binding, After: first.Next, Limit: 1})
	if err != nil {
		t.Fatalf("health outputs must not stale object-type membership: %v", err)
	}
	if !second.Complete || second.Next != nil || len(second.Items) != 1 {
		t.Fatalf("second page=%+v", second)
	}

	healthQuery := domain.Query{
		SchemaVersion: domain.QuerySchemaVersionV1,
		Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{{
			Kind: domain.ClauseKindPredicate, Field: "health_issue_type", Operator: string(domain.OperatorEQ), Value: json.RawMessage(`"ORPHAN"`),
		}}},
	}
	healthCollection, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: fixture.WorkspaceID, Name: "health issue membership", Query: healthQuery,
		ViewType: domain.ViewTypeList, IdempotencyKey: "durable-health-membership-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	healthBinding, err := service.PlanDurableScan(ctx, fixture.WorkspaceID, healthCollection.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if healthBinding.ExactCount != 1 {
		t.Fatalf("health binding=%+v", healthBinding)
	}
	healthResult, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: healthCollection.Collection.ID, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if healthResult.ScanRevisionHash != healthBinding.ReadModelRevision || healthResult.RevisionHash != healthResult.ScanRevisionHash {
		t.Fatalf("health membership revisions=%s/%s durable=%s", healthResult.RevisionHash, healthResult.ScanRevisionHash, healthBinding.ReadModelRevision)
	}
	insertDurableHealthIssue(t, ctx, pool, fixture.WorkspaceID, fixture.SecondClaimID, "ORPHAN", "health.detector.orphan-external/v1", "c", now.Add(3*time.Second))
	refreshedHealthResult, err := service.Results(ctx, collectionapp.ResultsQuery{WorkspaceID: fixture.WorkspaceID, CollectionID: healthCollection.Collection.ID, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if refreshedHealthResult.ScanRevisionHash == healthResult.ScanRevisionHash || refreshedHealthResult.RevisionHash != refreshedHealthResult.ScanRevisionHash {
		t.Fatalf("health membership revisions before=%s/%s after=%s/%s", healthResult.RevisionHash, healthResult.ScanRevisionHash, refreshedHealthResult.RevisionHash, refreshedHealthResult.ScanRevisionHash)
	}
	if _, err := service.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{Binding: healthBinding, Limit: 1}); !hasCollectionCode(err, collectionapp.ErrorCodeCursorStale) {
		t.Fatalf("health membership change stale error=%v", err)
	}
}

func verifyCollectionDurableBinding(t *testing.T, testCase collectionIntegrationCase, binding collectionapp.DurableScanBinding) error {
	t.Helper()
	verifier, ok := testCase.repository.(collectionapp.ScopedDurableScanBindingVerifier)
	if !ok {
		return errors.New("Collection GORM repository does not implement scoped durable binding verification")
	}
	unitOfWork, err := testCase.platform.UnitOfWork()
	if err != nil {
		return err
	}
	return unitOfWork.Within(testCase.context, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		return verifier.VerifyDurableScanBindingScoped(callbackCtx, scope, binding)
	})
}

func verifyCollectionDurableBindingRollbackOwnership(t *testing.T, testCase collectionIntegrationCase, binding collectionapp.DurableScanBinding) {
	t.Helper()
	verifier, ok := testCase.repository.(collectionapp.ScopedDurableScanBindingVerifier)
	if !ok {
		t.Fatal("Collection GORM repository does not implement scoped durable binding verification")
	}
	unitOfWork, err := testCase.platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	rollbackCause := errors.New("Collection caller requested durable binding rollback")
	err = unitOfWork.Within(testCase.context, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		if err := verifier.VerifyDurableScanBindingScoped(callbackCtx, scope, binding); err != nil {
			return err
		}
		return rollbackCause
	})
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("scoped durable binding rollback err=%v", err)
	}
}

func insertDurableHealthIssue(t *testing.T, ctx context.Context, db DB, workspaceID, targetID foundation.ID, issueType, detectorID, hashCharacter string, at time.Time) {
	t.Helper()
	id := collectionQueryID(t)
	identityHash := strings.Repeat(hashCharacter, 64)
	fingerprint := strings.Repeat(hashCharacter, 63) + "d"
	if _, err := db.Exec(ctx, `INSERT INTO ops.health_issue(
		id,workspace_id,type,target_type,target_id,detector_id,identity_hash,
		fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,status,
		version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at
	) VALUES($1,$2,$3,'CLAIM',$4,$5,$6,'health-issue-fingerprint/v1',$7,'detector/v1','MEDIUM','durable scan fixture','OPEN',1,$8,$8,$8,$8,$8)`,
		string(id), string(workspaceID), issueType, string(targetID), detectorID, identityHash, fingerprint, at.UTC()); err != nil {
		t.Fatal(err)
	}
}

func containsMixedDurablePair(pairs []collectionapp.DurableScanPair) bool {
	for _, pair := range pairs {
		if pair.Source.ObjectType != pair.Target.ObjectType {
			return true
		}
	}
	return false
}
