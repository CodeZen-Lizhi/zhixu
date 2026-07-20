//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

func TestGraphAdjacencyAndEvidencePlansUseKnowledgeIndexes(t *testing.T) {
	_, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	var sourceVersionID, sourceSpanID string
	var applicability []byte
	var schemaVersion, applicabilityHash string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text,applicability,applicability_schema_version,applicability_hash FROM core.relation_evidence WHERE workspace_id=$1 LIMIT 1`, string(fixture.workspaceID)).Scan(&sourceVersionID, &sourceSpanID, &applicability, &schemaVersion, &applicabilityHash); err != nil {
		t.Fatal(err)
	}
	const topicCount = 512
	topics := make([]foundation.ID, topicCount)
	now := time.Now().UTC()
	topicBatch := &pgx.Batch{}
	for index := range topics {
		topics[index] = graphTestID(t)
		topicBatch.Queue(`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,'','ACTIVE',1,$5,$5)`, string(topics[index]), string(fixture.workspaceID), fmt.Sprintf("Plan Topic %03d", index), fmt.Sprintf("plan topic %03d", index), now)
	}
	if err := tx.SendBatch(ctx, topicBatch).Close(); err != nil {
		t.Fatal(err)
	}
	relationIDs := make([]foundation.ID, 0, topicCount*2)
	relationBatch := &pgx.Batch{}
	for sourceIndex, sourceID := range topics {
		for offset := 1; offset <= 2; offset++ {
			targetID := topics[(sourceIndex+offset)%len(topics)]
			relationID := graphTestID(t)
			relationIDs = append(relationIDs, relationID)
			relationBatch.Queue(`INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,'TOPIC',$3,'TOPIC',$4,'IMPACTS','CONFIRMED',0.9,$5,$6,'SOURCE_DERIVED','plan fixture',1,$7,$7)`, string(relationID), string(fixture.workspaceID), string(sourceID), string(targetID), graphHash("plan-relation-"+string(relationID)), graphHash("plan-evidence-"+string(relationID)), now)
			relationBatch.Queue(`INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'plan evidence',$6,$7,$8,$9,'SOURCE_DERIVED','plan fixture',$10)`, string(graphTestID(t)), string(fixture.workspaceID), string(relationID), sourceVersionID, sourceSpanID, graphHash("plan-evidence-row-"+string(relationID)), applicability, schemaVersion, applicabilityHash, now)
		}
	}
	if err := tx.SendBatch(ctx, relationBatch).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE core.relation; ANALYZE core.relation_evidence`); err != nil {
		t.Fatal(err)
	}

	assertPlanUsesIndexWithoutRelationScan(t, ctx, tx,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) SELECT id FROM core.relation WHERE workspace_id=$1 AND source_node_type='TOPIC' AND source_node_id=$2 AND status='CONFIRMED' ORDER BY relation_type,id LIMIT 501`,
		[]any{string(fixture.workspaceID), string(topics[0])}, "idx_knowledge_relation_source", "relation")
	assertPlanUsesIndexWithoutRelationScan(t, ctx, tx,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) SELECT id FROM core.relation WHERE workspace_id=$1 AND target_node_type='TOPIC' AND target_node_id=$2 AND status='CONFIRMED' ORDER BY relation_type,id LIMIT 501`,
		[]any{string(fixture.workspaceID), string(topics[10])}, "idx_knowledge_relation_target", "relation")
	assertPlanUsesIndexWithoutRelationScan(t, ctx, tx,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) SELECT id FROM core.relation_evidence WHERE workspace_id=$1 AND relation_id=$2 ORDER BY created_at,id LIMIT 501`,
		[]any{string(fixture.workspaceID), string(relationIDs[0])}, "idx_knowledge_relation_evidence_owner", "relation_evidence")

	assertGraphPlan(t, ctx, tx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+neighborhoodDepthOneSQL,
		[]any{string(fixture.workspaceID), "TOPIC", string(topics[10]), "BOTH", []string{"CONFIRMED"}, []string{"IMPACTS"}, (*float64)(nil), (*time.Time)(nil), []string{"TOPIC"}, []string{}, []string{"CONFIRMED", "DISPUTED"}, (*float64)(nil), 501},
		[]string{"idx_knowledge_relation_source", "idx_knowledge_relation_target", "idx_knowledge_relation_evidence_owner"})
	assertGraphPlan(t, ctx, tx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+neighborhoodFrontierSQL,
		[]any{string(fixture.workspaceID), []string{"TOPIC", "TOPIC"}, []string{string(topics[10]), string(topics[11])}, "BOTH", []string{}, []string{"CONFIRMED"}, []string{"IMPACTS"}, (*float64)(nil), (*time.Time)(nil), []string{"TOPIC"}, []string{}, []string{"CONFIRMED", "DISPUTED"}, (*float64)(nil), 1001},
		[]string{"idx_knowledge_relation_source", "idx_knowledge_relation_target", "idx_knowledge_relation_evidence_owner"})
	assertGraphPlan(t, ctx, tx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+pathFrontierSQL,
		[]any{string(fixture.workspaceID), []string{"TOPIC"}, []string{string(topics[10])}, "BOTH", []string{"IMPACTS"}, 1001},
		[]string{"idx_knowledge_relation_source", "idx_knowledge_relation_target"})
	assertGraphPlan(t, ctx, tx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+relationEvidenceWindowSQL,
		[]any{string(fixture.workspaceID), string(relationIDs[0]), 501},
		[]string{"idx_knowledge_relation_evidence_owner"})
}

func assertPlanUsesIndexWithoutRelationScan(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args []any, indexName, relationName string) {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid explain json: %v %s", err, raw)
	}
	foundIndex, foundSequentialScan := false, false
	visitPlanNodes(documents[0]["Plan"], func(node map[string]any) {
		if node["Index Name"] == indexName {
			foundIndex = true
		}
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == relationName {
			foundSequentialScan = true
		}
	})
	if !foundIndex || foundSequentialScan {
		t.Fatalf("plan index=%v seq_scan=%v expected_index=%s plan=%s", foundIndex, foundSequentialScan, indexName, raw)
	}
}

func visitPlanNodes(value any, visit func(map[string]any)) {
	node, ok := value.(map[string]any)
	if !ok {
		return
	}
	visit(node)
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		visitPlanNodes(child, visit)
	}
}

func assertGraphPlan(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args []any, expectedIndexes []string) {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid explain json: %v %s", err, raw)
	}
	found := make(map[string]bool, len(expectedIndexes))
	sequentialRelationScan := false
	visitPlanNodes(documents[0]["Plan"], func(node map[string]any) {
		if indexName, ok := node["Index Name"].(string); ok {
			found[indexName] = true
		}
		if node["Node Type"] == "Seq Scan" && (node["Relation Name"] == "relation" || node["Relation Name"] == "relation_evidence") {
			sequentialRelationScan = true
		}
	})
	for _, indexName := range expectedIndexes {
		if !found[indexName] {
			t.Fatalf("plan misses index %s: %s", indexName, raw)
		}
	}
	if sequentialRelationScan {
		t.Fatalf("graph plan contains relation full scan: %s", raw)
	}
}
