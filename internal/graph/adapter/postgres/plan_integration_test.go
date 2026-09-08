//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func TestGraphAdjacencyAndEvidencePlansUseKnowledgeIndexes(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
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
			relationBatch.Queue(`INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,'TOPIC',$3,'TOPIC',$4,'IMPACTS','SUGGESTED',0.9,$5,$6,NULL,NULL,1,$7,$7)`, string(relationID), string(fixture.workspaceID), string(sourceID), string(targetID), graphHash("plan-relation-"+string(relationID)), graphHash("plan-evidence-"+string(relationID)), now)
			relationBatch.Queue(`INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'plan evidence',$6,$7,$8,$9,'SOURCE_DERIVED','plan fixture',$10)`, string(graphTestID(t)), string(fixture.workspaceID), string(relationID), sourceVersionID, sourceSpanID, graphHash("plan-evidence-row-"+string(relationID)), applicability, schemaVersion, applicabilityHash, now)
			relationBatch.Queue(`UPDATE core.relation SET status='CONFIRMED',confirmation_method='SOURCE_DERIVED',confirmation_ref='plan fixture',version=version+1 WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(relationID))
		}
	}
	if err := tx.SendBatch(ctx, relationBatch).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE core.relation; ANALYZE core.relation_evidence`); err != nil {
		t.Fatal(err)
	}

	assertPlanUsesIndexWithoutRelationScan(t, ctx, repository.database,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) SELECT id FROM core.relation WHERE workspace_id=(@p1) AND source_node_type='TOPIC' AND source_node_id=(@p2) AND status='CONFIRMED' ORDER BY relation_type,id LIMIT 501`,
		[]any{sql.Named("p1", string(fixture.workspaceID)), sql.Named("p2", string(topics[0]))}, "idx_knowledge_relation_source", "relation")
	assertPlanUsesIndexWithoutRelationScan(t, ctx, repository.database,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) SELECT id FROM core.relation WHERE workspace_id=(@p1) AND target_node_type='TOPIC' AND target_node_id=(@p2) AND status='CONFIRMED' ORDER BY relation_type,id LIMIT 501`,
		[]any{sql.Named("p1", string(fixture.workspaceID)), sql.Named("p2", string(topics[10]))}, "idx_knowledge_relation_target", "relation")
	assertPlanUsesIndexWithoutRelationScan(t, ctx, repository.database,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) SELECT id FROM core.relation_evidence WHERE workspace_id=(@p1) AND relation_id=(@p2) ORDER BY created_at,id LIMIT 501`,
		[]any{sql.Named("p1", string(fixture.workspaceID)), sql.Named("p2", string(relationIDs[0]))}, "idx_knowledge_relation_evidence_owner", "relation_evidence")

	assertGraphPlan(t, ctx, repository.database, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+neighborhoodDepthOneSQL,
		[]any{sql.Named("p1", string(fixture.workspaceID)), sql.Named("p2", "TOPIC"), sql.Named("p3", string(topics[10])), sql.Named("p4", "BOTH"), sql.Named("p5", pq.Array([]string{"CONFIRMED"})), sql.Named("p6", pq.Array([]string{"IMPACTS"})), sql.Named("p7", (*float64)(nil)), sql.Named("p8", (*time.Time)(nil)), sql.Named("p9", pq.Array([]string{"TOPIC"})), sql.Named("p10", pq.Array([]string{})), sql.Named("p11", pq.Array([]string{"CONFIRMED", "DISPUTED"})), sql.Named("p12", (*float64)(nil)), sql.Named("p13", 501)},
		[]string{"idx_knowledge_relation_source", "idx_knowledge_relation_target", "idx_knowledge_relation_evidence_owner"})
	assertGraphPlan(t, ctx, repository.database, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+neighborhoodFrontierSQL,
		[]any{sql.Named("p1", string(fixture.workspaceID)), sql.Named("p2", pq.Array([]string{"TOPIC", "TOPIC"})), sql.Named("p3", pq.Array([]string{string(topics[10]), string(topics[11])})), sql.Named("p4", "BOTH"), sql.Named("p5", pq.Array([]string{})), sql.Named("p6", pq.Array([]string{"CONFIRMED"})), sql.Named("p7", pq.Array([]string{"IMPACTS"})), sql.Named("p8", (*float64)(nil)), sql.Named("p9", (*time.Time)(nil)), sql.Named("p10", pq.Array([]string{"TOPIC"})), sql.Named("p11", pq.Array([]string{})), sql.Named("p12", pq.Array([]string{"CONFIRMED", "DISPUTED"})), sql.Named("p13", (*float64)(nil)), sql.Named("p14", 1001)},
		[]string{"idx_knowledge_relation_source", "idx_knowledge_relation_target", "idx_knowledge_relation_evidence_owner"})
	assertGraphPlan(t, ctx, repository.database, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+pathFrontierSQL,
		[]any{sql.Named("p1", string(fixture.workspaceID)), sql.Named("p2", pq.Array([]string{"TOPIC"})), sql.Named("p3", pq.Array([]string{string(topics[10])})), sql.Named("p4", "BOTH"), sql.Named("p5", pq.Array([]string{"IMPACTS"})), sql.Named("p6", 1001)},
		[]string{"idx_knowledge_relation_source", "idx_knowledge_relation_target"})
	assertGraphPlan(t, ctx, repository.database, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+relationEvidenceWindowSQL,
		[]any{sql.Named("p1", string(fixture.workspaceID)), sql.Named("p2", string(relationIDs[0])), sql.Named("p3", 501)},
		[]string{"idx_knowledge_relation_evidence_owner"})
}

func assertPlanUsesIndexWithoutRelationScan(t *testing.T, ctx context.Context, db *gorm.DB, query string, args []any, indexName, relationName string) {
	t.Helper()
	var raw []byte
	if err := gormQueryRow(ctx, db, query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid explain json: %v %s", err, raw)
	}
	foundIndex, foundSequentialScan := false, false
	visitPlanNodes(documents[0]["Plan"], func(node map[string]any) {
		if actual, ok := node["Index Name"].(string); ok && graphEquivalentQueryIndex(indexName, actual) {
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

func assertSemanticLinkTopicPairPlan(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, workspaceID, topicID foundation.ID, sourceIDs []string) {
	t.Helper()
	unit, err := pool.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	err = unit.Within(ctx, foundation.TransactionOptions{ReadOnly: true}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if err := database.WithContext(callbackCtx).Exec(`SET LOCAL enable_seqscan = off`).Error; err != nil {
			return err
		}
		return gormQueryRow(callbackCtx, database, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+semanticLinkTopicClaimPairsSQL,
			sql.Named("p1", string(workspaceID)), sql.Named("p2", string(topicID)), sql.Named("p3", pq.Array(sourceIDs)), sql.Named("p4", graphapp.MaxSemanticLinkScanPagePairsPerNode)).Scan(&raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid semantic link pair explain json: %v %s", err, raw)
	}
	foundIndex, foundSequentialScan, foundPerSourceLimit := false, false, false
	visitPlanNodes(documents[0]["Plan"], func(node map[string]any) {
		if node["Index Name"] == "idx_knowledge_relation_semantic_scan_topic_claim" {
			foundIndex = true
		}
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == "relation" {
			foundSequentialScan = true
		}
		if node["Node Type"] == "Limit" {
			if loops, ok := node["Actual Loops"].(float64); ok && loops >= float64(len(sourceIDs)) {
				foundPerSourceLimit = true
			}
		}
	})
	if !foundIndex || foundSequentialScan || !foundPerSourceLimit {
		t.Fatalf("semantic link pair plan index=%v seq_scan=%v per_source_limit=%v plan=%s", foundIndex, foundSequentialScan, foundPerSourceLimit, raw)
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

func assertGraphPlan(t *testing.T, ctx context.Context, db *gorm.DB, query string, args []any, expectedIndexes []string) {
	t.Helper()
	var raw []byte
	if err := gormQueryRow(ctx, db, query, args...).Scan(&raw); err != nil {
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
		if !graphPlanHasIndex(found, indexName) {
			t.Fatalf("plan misses index %s: %s", indexName, raw)
		}
	}
	if sequentialRelationScan {
		t.Fatalf("graph plan contains relation full scan: %s", raw)
	}
}

// Atlas 00026 added status-covering adjacency indexes with the same Workspace
// and endpoint prefix as 00017. Either serves the original bounded lookup;
// relation/evidence sequential scans remain forbidden independently.
func graphEquivalentQueryIndex(required, actual string) bool {
	if required == actual {
		return true
	}
	switch required {
	case "idx_knowledge_relation_source":
		return actual == "idx_knowledge_relation_workspace_source_status_type"
	case "idx_knowledge_relation_target":
		return actual == "idx_knowledge_relation_workspace_target_status_type"
	default:
		return false
	}
}

func graphPlanHasIndex(found map[string]bool, required string) bool {
	for actual := range found {
		if graphEquivalentQueryIndex(required, actual) {
			return true
		}
	}
	return false
}
