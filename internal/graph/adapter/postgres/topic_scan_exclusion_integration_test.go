//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	graphtestfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

func TestSemanticLinkPairExclusionsMatchEndpointTypeAndID(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticLinkScanTestPool(t, ctx)
	fixture, err := graphtestfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := graphtestfixture.Cleanup(cleanupCtx, pool, fixture.WorkspaceID); err != nil {
			t.Errorf("cleanup graph fixture: %v", err)
		}
	})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := fixture.WorkspaceID
	sharedID := fixture.SecondClaimID
	proposalTopicID := graphTestID(t)
	for _, topic := range []foundation.ID{sharedID, proposalTopicID} {
		if _, err := tx.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
			VALUES($1,$2,$3,$3,'','ACTIVE',1,$4,$4)`, string(topic), string(workspaceID), "topic "+string(topic), now); err != nil {
			t.Fatal(err)
		}
	}
	insertTypedExclusionProposal(t, ctx, tx, workspaceID, fixture.FirstClaimID, knowledge.NodeTypeClaim, sharedID, knowledge.NodeTypeClaim, knowledge.RelationComplements, "false-type", now)
	insertTypedExclusionProposal(t, ctx, tx, workspaceID, fixture.SecondClaimID, knowledge.NodeTypeClaim, proposalTopicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo, "matching-type", now)

	pairs := []graphdomain.SemanticLinkDiscoveryPair{
		exclusionPair(workspaceID, knowledge.NodeTypeClaim, fixture.FirstClaimID, knowledge.NodeTypeTopic, sharedID),
		exclusionPair(workspaceID, knowledge.NodeTypeTopic, fixture.PrimaryTopicID, knowledge.NodeTypeClaim, fixture.FirstClaimID),
		exclusionPair(workspaceID, knowledge.NodeTypeClaim, fixture.SecondClaimID, knowledge.NodeTypeTopic, proposalTopicID),
	}
	exclusions, err := loadTopicPairExclusions(ctx, tx, workspaceID, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if len(exclusions) != 2 {
		t.Fatalf("exclusions=%#v", exclusions)
	}
	if exclusions[0].Source != pairs[1].Source.Endpoint.Ref || exclusions[0].Target != pairs[1].Target.Endpoint.Ref || exclusions[0].Reason != "FORMAL_RELATION" {
		t.Fatalf("formal exclusion=%#v", exclusions[0])
	}
	if exclusions[1].Source != pairs[2].Source.Endpoint.Ref || exclusions[1].Target != pairs[2].Target.Endpoint.Ref || exclusions[1].Reason != "ACTIVE_PROPOSAL" {
		t.Fatalf("proposal exclusion=%#v", exclusions[1])
	}
}

func exclusionPair(workspaceID foundation.ID, sourceType knowledge.NodeType, sourceID foundation.ID, targetType knowledge.NodeType, targetID foundation.ID) graphdomain.SemanticLinkDiscoveryPair {
	return graphdomain.SemanticLinkDiscoveryPair{
		Source: graphdomain.SemanticLinkDiscoveryNode{WorkspaceID: workspaceID, Endpoint: graphdomain.SemanticLinkCandidateEndpoint{Ref: knowledge.NodeRef{Type: sourceType, ID: sourceID}}},
		Target: graphdomain.SemanticLinkDiscoveryNode{WorkspaceID: workspaceID, Endpoint: graphdomain.SemanticLinkCandidateEndpoint{Ref: knowledge.NodeRef{Type: targetType, ID: targetID}}},
	}
}

func insertTypedExclusionProposal(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, sourceID foundation.ID, sourceType knowledge.NodeType, targetID foundation.ID, targetType knowledge.NodeType, relationType knowledge.RelationType, key string, now time.Time) {
	t.Helper()
	proposalID, revisionID := graphTestID(t), graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,risk_level,status,idempotency_key,request_hash,version,created_at,updated_at)
		VALUES($1,$2,'knowledge_change','HIGH','ready_for_review',$3,$4,1,$5,$5)`,
		string(proposalID), string(workspaceID), "typed-exclusion-"+key, graphHash("proposal-"+key), now); err != nil {
		t.Fatal(err)
	}
	changeSet, err := json.Marshal(changecontroldomain.KnowledgeChangeSet{
		Operation: changecontroldomain.KnowledgeChangeOperationCreateRelation,
		Source:    knowledge.NodeRef{Type: sourceType, ID: sourceID}, Target: knowledge.NodeRef{Type: targetType, ID: targetID},
		RelationType: relationType,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,risk,rollback_plan,change_hash,target_refs,base_versions,change_set,evidence_refs,schema_version,created_at)
		VALUES($1,$2,1,'review relation','reject proposal',$3,$4::jsonb,$5::jsonb,$6::jsonb,$7::jsonb,'knowledge-relation-change/v1',$8)`,
		string(revisionID), string(proposalID), strings.Repeat("9", 64),
		`[{"type":"RELATION_CANDIDATE","id":"`+string(graphTestID(t))+`","fingerprint":"`+strings.Repeat("8", 64)+`"}]`,
		`[{"node_type":"`+string(sourceType)+`","node_id":"`+string(sourceID)+`","version":1}]`,
		changeSet,
		`[{"candidate_evidence_id":"`+string(graphTestID(t))+`","semantic_hash":"`+strings.Repeat("7", 64)+`"}]`, now); err != nil {
		t.Fatal(err)
	}
}
