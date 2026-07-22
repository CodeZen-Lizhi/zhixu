package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdetector "github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
)

type detectorDB struct{ calls int }

func (db *detectorDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	db.calls++
	return nil, nil
}

func TestFactReaderRejectsUnavailableScopesBeforeQuery(t *testing.T) {
	db := &detectorDB{}
	reader, err := NewFactReader(db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Find(context.Background(), "health.detector.orphan", healthPageRequestSmartCollection())
	if err == nil {
		t.Fatal("smart collection detector unexpectedly queried database")
	}
	if db.calls != 0 {
		t.Fatalf("query calls = %d", db.calls)
	}
}

func TestFactReaderRejectsUnsupportedDetectorScopeBeforeQuery(t *testing.T) {
	db := &detectorDB{}
	reader, err := NewFactReader(db)
	if err != nil {
		t.Fatal(err)
	}
	request := healthapp.PageRequest{
		Scope:      healthapp.Scope{WorkspaceID: "10000000-0000-4000-8000-000000000001", Type: domain.ScanScopeTypeTopic, Ref: "20000000-0000-4000-8000-000000000001"},
		BatchSize:  100,
		Descriptor: availableDetectorDescriptor(healthdetector.DetectorIndexError),
	}
	request.Descriptor.SupportedScopes = append(request.Descriptor.SupportedScopes, domain.ScanScopeTypeTopic)
	if _, err := reader.Find(context.Background(), healthdetector.DetectorIndexError, request); !errors.Is(err, healthapp.ErrDetectorUnavailable) {
		t.Fatalf("index detector topic error = %v", err)
	}
	if db.calls != 0 {
		t.Fatalf("query calls = %d", db.calls)
	}
}

func TestDetectorQueryExpandsTopicMembership(t *testing.T) {
	query, args, err := detectorQuery("health.detector.orphan", healthapp.PageRequest{
		Scope:     healthapp.Scope{WorkspaceID: "10000000-0000-4000-8000-000000000001", Type: domain.ScanScopeTypeTopic, Ref: "20000000-0000-4000-8000-000000000001"},
		BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 8 || args[1] != string(domain.ScanScopeTypeTopic) || args[3] != "" || args[7] != nil {
		t.Fatalf("unexpected query args: %#v", args)
	}
	for _, fragment := range []string{
		"core.relation scoped_membership",
		"scoped_membership.status='CONFIRMED'",
		"scoped_membership.relation_type='BELONGS_TO'",
		"core.relation scoped_relation",
		"core.conflict_member scoped_member",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("topic query missing %q: %s", fragment, query)
		}
	}
}

func TestDetectorQueryExpandsSmartCollectionMembersAndDerivedTargets(t *testing.T) {
	membership := scanScopeMembership{
		TopicIDs: []string{"20000000-0000-4000-8000-000000000001"},
		ClaimIDs: []string{"30000000-0000-4000-8000-000000000001"},
	}
	request := healthPageRequestSmartCollection()
	query, args, err := detectorQuery(healthdetector.DetectorConflict, request, membership)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 8 || args[5].([]string)[0] != membership.TopicIDs[0] || args[6].([]string)[0] != membership.ClaimIDs[0] {
		t.Fatalf("unexpected smart-collection args: %#v", args)
	}
	for _, fragment := range []string{
		"target_type='TOPIC'", "target_type='CLAIM'", "core.relation collection_relation",
		"core.conflict_member collection_member", "source_node_id=ANY($7::uuid[])", "claim_id=ANY($7::uuid[])",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("smart-collection query missing %q: %s", fragment, query)
		}
	}
}

func TestDetectorQueryUsesObjectTypeAndIDKeysetCursor(t *testing.T) {
	const targetID = "30000000-0000-4000-8000-000000000001"
	cursor, err := encodeDetectorCursor(string(domain.ObjectTypeClaim), targetID)
	if err != nil {
		t.Fatal(err)
	}
	query, args, err := detectorQuery(healthdetector.DetectorOrphan, healthapp.PageRequest{
		Scope:     healthapp.Scope{WorkspaceID: "10000000-0000-4000-8000-000000000001", Type: domain.ScanScopeTypeWorkspace, Ref: "10000000-0000-4000-8000-000000000001"},
		Cursor:    cursor,
		BatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 8 || args[3] != string(domain.ObjectTypeClaim) || args[7] != targetID {
		t.Fatalf("unexpected cursor args: %#v", args)
	}
	for _, fragment := range []string{"target_type>$4", "target_type=$4 AND target_id>$8::uuid", "ORDER BY target_type,target_id"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("detector keyset query missing %q: %s", fragment, query)
		}
	}
}

func TestDetectorQueryAcceptsLegacyIDCursorButRejectsMalformedCursor(t *testing.T) {
	const targetID = "30000000-0000-4000-8000-000000000001"
	request := healthapp.PageRequest{
		Scope:     healthapp.Scope{WorkspaceID: "10000000-0000-4000-8000-000000000001", Type: domain.ScanScopeTypeWorkspace, Ref: "10000000-0000-4000-8000-000000000001"},
		Cursor:    targetID,
		BatchSize: 1,
	}
	_, args, err := detectorQuery(healthdetector.DetectorOrphan, request)
	if err != nil {
		t.Fatal(err)
	}
	if args[3] != detectorLegacyCursor || args[7] != targetID {
		t.Fatalf("unexpected legacy cursor args: %#v", args)
	}
	request.Cursor = detectorCursorSchema + "|CLAIM|not-a-uuid"
	if _, _, err := detectorQuery(healthdetector.DetectorOrphan, request); err == nil {
		t.Fatal("malformed versioned detector cursor unexpectedly accepted")
	}
}

func TestEvidenceDetectorsUseExistenceQueries(t *testing.T) {
	for _, detectorID := range []string{healthdetector.DetectorBrokenReference, healthdetector.DetectorSupersededUsage} {
		query, _, err := detectorQuery(detectorID, healthapp.PageRequest{
			Scope:     healthapp.Scope{WorkspaceID: "10000000-0000-4000-8000-000000000001", Type: domain.ScanScopeTypeWorkspace, Ref: "10000000-0000-4000-8000-000000000001"},
			BatchSize: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{"FROM core.claim c\n WHERE EXISTS", "FROM core.relation r\n WHERE EXISTS"} {
			if !strings.Contains(query, fragment) {
				t.Fatalf("%s query missing canonical target existence check %q: %s", detectorID, fragment, query)
			}
		}
	}
}

func healthPageRequestSmartCollection() healthapp.PageRequest {
	return healthapp.PageRequest{
		Scope: healthapp.Scope{
			WorkspaceID: "10000000-0000-4000-8000-000000000001", Type: domain.ScanScopeTypeSmartCollection,
			Ref: "20000000-0000-4000-8000-000000000001", Version: 2,
			Hash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64),
		},
		BatchSize: 100, Descriptor: availableDetectorDescriptor(healthdetector.DetectorOrphan),
	}
}

func availableDetectorDescriptor(detectorID string) healthapp.Descriptor {
	return healthapp.Descriptor{
		ID:              detectorID,
		Version:         healthdetector.DefaultDetectorVersion,
		SupportedScopes: healthdetector.DefaultSupportedScopes(detectorID),
		Available:       true,
	}
}
