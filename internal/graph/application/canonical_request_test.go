package application

import (
	"testing"
	"time"

	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestCanonicalGlobalRequestHashNormalizesDefaultsAndSetOrder(t *testing.T) {
	workspaceID := graphServiceID(90)
	implicit := graphdomain.GlobalRequest{WorkspaceID: workspaceID}
	explicit := graphdomain.GlobalRequest{WorkspaceID: workspaceID, Limit: 99, Filter: graphdomain.GraphFilter{
		NodeTypes: []knowledge.NodeType{knowledge.NodeTypeTopic, knowledge.NodeTypeClaim},
		RelationTypes: []knowledge.RelationType{
			knowledge.RelationVersionOf, knowledge.RelationSupports, knowledge.RelationPrerequisiteOf,
			knowledge.RelationImpacts, knowledge.RelationDuplicates, knowledge.RelationDerivedFrom,
			knowledge.RelationConflictsWith, knowledge.RelationComplements, knowledge.RelationCites,
			knowledge.RelationBelongsTo,
		},
		RelationStatuses: []knowledge.RelationStatus{knowledge.RelationStatusConfirmed},
		ClaimStatuses:    []knowledge.ClaimStatus{knowledge.ClaimStatusDisputed, knowledge.ClaimStatusConfirmed},
	}}
	assertSameCanonicalHash(t, CanonicalGlobalRequestHash, implicit, explicit)
}

func TestCanonicalNeighborhoodDepth1HashNormalizesDefaultsAndTimeZone(t *testing.T) {
	at := time.Date(2026, 7, 20, 8, 0, 0, 123, time.FixedZone("CST", 8*60*60))
	first := graphdomain.NeighborhoodRequest{WorkspaceID: graphServiceID(90), Center: graphServiceRef(knowledge.NodeTypeTopic, 1), Depth: 1, Filter: graphdomain.GraphFilter{UpdatedAfter: &at}}
	utc := at.UTC()
	second := first
	second.Depth, second.Limit, second.Direction, second.MaxNodes, second.MaxEdges = 3, 100, graphdomain.TraversalBoth, graphdomain.MaxNodes, graphdomain.MaxEdges
	second.Filter.UpdatedAfter = &utc
	assertSameCanonicalHash(t, CanonicalNeighborhoodDepth1RequestHash, first, second)
}

func TestCanonicalRelationEvidenceHashExcludesPageShape(t *testing.T) {
	first, err := CanonicalRelationEvidenceRequestHash(graphServiceID(90), graphServiceID(3))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalRelationEvidenceRequestHash(graphServiceID(90), graphServiceID(3))
	if err != nil || first != second || !canonicalCursorHash(first) {
		t.Fatalf("hashes=%q/%q err=%v", first, second, err)
	}
}

func assertSameCanonicalHash[T any](t *testing.T, hash func(T) (string, error), first, second T) {
	t.Helper()
	left, err := hash(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := hash(second)
	if err != nil || left != right || !canonicalCursorHash(left) {
		t.Fatalf("hashes=%q/%q err=%v", left, right, err)
	}
}
