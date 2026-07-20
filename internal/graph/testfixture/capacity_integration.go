//go:build integration

package testfixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// CapacityFixtureVersion 是容量夹具的拓扑与哈希算法版本。
	CapacityFixtureVersion = "graph-capacity/v1"
	// CapacityNodeCount 是容量夹具中 ACTIVE TOPIC 的固定数量。
	CapacityNodeCount = 20_000
	// CapacityRelationCount 是容量夹具中 CONFIRMED IMPACTS 的固定数量。
	CapacityRelationCount = 100_000
	// CapacityEvidenceCount 是容量夹具中 Relation Evidence 的固定数量。
	CapacityEvidenceCount = 100_000
	// CapacityHotCenterDegree 是一跳基准中心节点的固定关联边数。
	CapacityHotCenterDegree = 499

	capacityConfirmationReference = "graph capacity benchmark fixture"
	capacityEvidenceReason        = "capacity benchmark evidence"
	capacityTopicWriteChunk       = 1_000
	capacityRelationWriteChunk    = 5_000
	capacityWorkspaceName         = "graph-capacity-benchmark"
	maxCapacitySeedBytes          = 128
)

// CapacityFixture 描述已提交的确定性 Graph 容量数据及代表查询身份。
type CapacityFixture struct {
	Version            string
	Seed               string
	WorkspaceID        foundation.ID
	CenterTopicID      foundation.ID
	PathTargetTopicID  foundation.ID
	EvidenceRelationID foundation.ID
	NodeCount          int
	RelationCount      int
	EvidenceCount      int
	HotCenterDegree    int
}

type capacityIDs struct {
	workspaceID, artifactID, sourceID, sourceVersionID   foundation.ID
	projectionID, sourceSpanID                           foundation.ID
	centerTopicID, pathTargetTopicID, evidenceRelationID foundation.ID
}

// SeedCapacity 使用集合式 SQL 提交由 seed 决定的 20k/100k Graph 容量夹具。
func SeedCapacity(ctx context.Context, pool *pgxpool.Pool, seed string) (CapacityFixture, error) {
	if ctx == nil {
		return CapacityFixture{}, errors.New("graph capacity fixture context is nil")
	}
	if pool == nil {
		return CapacityFixture{}, errors.New("graph capacity fixture pool is nil")
	}
	if seed == "" || seed != strings.TrimSpace(seed) || !utf8.ValidString(seed) || len([]byte(seed)) > maxCapacitySeedBytes {
		return CapacityFixture{}, errors.New("graph capacity fixture seed must be canonical bounded UTF-8")
	}
	ids, err := deriveCapacityIDs(ctx, pool, seed)
	if err != nil {
		return CapacityFixture{}, fmt.Errorf("derive graph capacity fixture identities: %w", err)
	}
	if err := CleanupCapacity(ctx, pool, ids.workspaceID); err != nil {
		return CapacityFixture{}, fmt.Errorf("remove previous graph capacity fixture: %w", err)
	}
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	applicability, err := knowledge.ParseApplicability(json.RawMessage(`{"scope":"graph-capacity","version":"v1"}`))
	if err != nil {
		return CapacityFixture{}, fmt.Errorf("build graph capacity applicability: %w", err)
	}
	fail := func(cause error) (CapacityFixture, error) {
		if cleanupErr := cleanupCapacityFixture(pool, ids.workspaceID); cleanupErr != nil {
			return CapacityFixture{}, errors.Join(cause, fmt.Errorf("cleanup partial graph capacity fixture: %w", cleanupErr))
		}
		return CapacityFixture{}, cause
	}
	if err := withCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
		if err := insertCapacityWorkspace(ctx, tx, ids.workspaceID, now); err != nil {
			return fmt.Errorf("insert workspace: %w", err)
		}
		if err := insertCapacityProvenance(ctx, tx, ids, seed, now); err != nil {
			return fmt.Errorf("insert provenance: %w", err)
		}
		return nil
	}); err != nil {
		return fail(fmt.Errorf("commit graph capacity foundation: %w", err))
	}
	if err := insertCapacityTopicChunks(ctx, pool, seed, ids.workspaceID, now); err != nil {
		return fail(err)
	}
	if err := insertCapacityRelationChunks(ctx, pool, seed, ids, applicability, now); err != nil {
		return fail(err)
	}

	fixture := CapacityFixture{
		Version: CapacityFixtureVersion, Seed: seed, WorkspaceID: ids.workspaceID,
		CenterTopicID: ids.centerTopicID, PathTargetTopicID: ids.pathTargetTopicID,
		EvidenceRelationID: ids.evidenceRelationID, NodeCount: CapacityNodeCount,
		RelationCount: CapacityRelationCount, EvidenceCount: CapacityEvidenceCount,
		HotCenterDegree: CapacityHotCenterDegree,
	}
	if err := analyzeCapacityFixture(ctx, pool); err != nil {
		return fail(err)
	}
	if err := verifyCapacityFixture(ctx, pool, fixture); err != nil {
		return fail(err)
	}
	return fixture, nil
}

// CleanupCapacity 仅删除 marker 完整匹配的 Graph 容量测试 Workspace。
func CleanupCapacity(ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) error {
	return cleanupWorkspace(ctx, pool, workspaceID, capacityWorkspaceName, capacityWorkspaceRoot(workspaceID))
}

func capacityWorkspaceRoot(workspaceID foundation.ID) string {
	return "/tmp/graph-capacity-benchmark-" + string(workspaceID)
}

func insertCapacityWorkspace(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, now time.Time) error {
	root := capacityWorkspaceRoot(workspaceID)
	_, err := tx.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(workspaceID), capacityWorkspaceName, root, now)
	return err
}

func withCapacityTransaction(ctx context.Context, pool *pgxpool.Pool, operation func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func insertCapacityTopicChunks(ctx context.Context, pool *pgxpool.Pool, seed string, workspaceID foundation.ID, now time.Time) error {
	for start := 0; start < CapacityNodeCount; start += capacityTopicWriteChunk {
		end := start + capacityTopicWriteChunk
		if end > CapacityNodeCount {
			end = CapacityNodeCount
		}
		if err := withCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, insertCapacityTopicsSQL, seed, string(workspaceID), start, end, now)
			return err
		}); err != nil {
			return fmt.Errorf("commit graph capacity topics [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func insertCapacityRelationChunks(
	ctx context.Context,
	pool *pgxpool.Pool,
	seed string,
	ids capacityIDs,
	applicability knowledge.Applicability,
	now time.Time,
) error {
	for start := 0; start < CapacityRelationCount; start += capacityRelationWriteChunk {
		end := start + capacityRelationWriteChunk
		if end > CapacityRelationCount {
			end = CapacityRelationCount
		}
		if err := withCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, insertCapacityRelationsSQL,
				seed, string(ids.workspaceID), start, end, CapacityHotCenterDegree, CapacityNodeCount, now,
			); err != nil {
				return fmt.Errorf("insert relations: %w", err)
			}
			if _, err := tx.Exec(ctx, insertCapacityEvidenceSQL,
				seed, string(ids.workspaceID), start, end,
				string(ids.sourceVersionID), string(ids.sourceSpanID), capacityEvidenceReason,
				string(applicability.CanonicalJSON), applicability.SchemaVersion, applicability.Hash,
				capacityConfirmationReference, now,
			); err != nil {
				return fmt.Errorf("insert evidence: %w", err)
			}
			if _, err := tx.Exec(ctx, confirmCapacityRelationsSQL,
				seed, string(ids.workspaceID), start, end, capacityConfirmationReference, now.Add(time.Second),
			); err != nil {
				return fmt.Errorf("confirm relations: %w", err)
			}
			if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
				return fmt.Errorf("verify deferred constraints: %w", err)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("commit graph capacity relations [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func deriveCapacityIDs(ctx context.Context, pool *pgxpool.Pool, seed string) (capacityIDs, error) {
	var raw [9]string
	err := pool.QueryRow(ctx, `SELECT
		md5($1 || ':workspace')::uuid::text,
		md5($1 || ':artifact')::uuid::text,
		md5($1 || ':source')::uuid::text,
		md5($1 || ':source-version')::uuid::text,
		md5($1 || ':projection')::uuid::text,
		md5($1 || ':source-span')::uuid::text,
		md5($1 || ':topic:0')::uuid::text,
		md5($1 || ':topic:499')::uuid::text,
		md5($1 || ':relation:0')::uuid::text`, seed).Scan(
		&raw[0], &raw[1], &raw[2], &raw[3], &raw[4], &raw[5], &raw[6], &raw[7], &raw[8],
	)
	if err != nil {
		return capacityIDs{}, err
	}
	parsed := make([]foundation.ID, len(raw))
	for index, value := range raw {
		parsed[index], err = foundation.ParseID(value)
		if err != nil {
			return capacityIDs{}, err
		}
	}
	return capacityIDs{
		workspaceID: parsed[0], artifactID: parsed[1], sourceID: parsed[2], sourceVersionID: parsed[3],
		projectionID: parsed[4], sourceSpanID: parsed[5], centerTopicID: parsed[6],
		pathTargetTopicID: parsed[7], evidenceRelationID: parsed[8],
	}, nil
}

func insertCapacityProvenance(ctx context.Context, tx pgx.Tx, ids capacityIDs, seed string, now time.Time) error {
	content := []byte(CapacityFixtureVersion + "\n" + seed)
	contentHash := sha256Hex(content)
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
				VALUES($1,$2,$3,$4,$5,$6)`,
			args: []any{string(ids.artifactID), string(ids.workspaceID), contentHash, len(content), ".knowledge/sources/" + contentHash, now},
		},
		{
			query: `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
				VALUES($1,$2,'text',$3,$3,$4)`,
			args: []any{string(ids.sourceID), string(ids.workspaceID), "graph-capacity-fixture.txt", now},
		},
		{
			query: `INSERT INTO core.source_version(
				id,source_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
			) VALUES($1,$2,$3,$4,$5,'text/plain',$6,'pending',$7)`,
			args: []any{string(ids.sourceVersionID), string(ids.sourceID), string(ids.artifactID), contentHash, len(content), "graph-capacity-fixture.txt", now},
		},
		{
			query: `INSERT INTO ingestion.parse_projection(
				id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at
			) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`,
			args: []any{string(ids.projectionID), string(ids.workspaceID), string(ids.artifactID), sha256Hex([]byte("graph-capacity-parser/v1")), contentHash, now},
		},
		{
			query: `INSERT INTO ingestion.source_span(
				id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at
			) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{}',$6,'v1','v1',$7)`,
			args: []any{string(ids.sourceSpanID), string(ids.workspaceID), string(ids.artifactID), string(ids.projectionID), len(content), contentHash, now},
		},
		{
			query: `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
				VALUES($1,$2,$3,$4)`,
			args: []any{string(ids.sourceVersionID), string(ids.projectionID), string(ids.workspaceID), now},
		},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func analyzeCapacityFixture(ctx context.Context, pool *pgxpool.Pool) error {
	for _, statement := range []string{
		`ANALYZE core.topic`,
		`ANALYZE core.relation`,
		`ANALYZE core.relation_evidence`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("analyze graph capacity fixture: %w", err)
		}
	}
	return nil
}

func verifyCapacityFixture(ctx context.Context, pool *pgxpool.Pool, fixture CapacityFixture) error {
	var nodes, relations, evidence, hotDegree int
	err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*)::int FROM core.topic WHERE workspace_id=$1),
		(SELECT COUNT(*)::int FROM core.relation WHERE workspace_id=$1 AND status='CONFIRMED' AND relation_type='IMPACTS'),
		(SELECT COUNT(*)::int FROM core.relation_evidence WHERE workspace_id=$1),
		(SELECT COUNT(*)::int FROM core.relation WHERE workspace_id=$1
			AND ((source_node_type='TOPIC' AND source_node_id=$2) OR (target_node_type='TOPIC' AND target_node_id=$2)))`,
		string(fixture.WorkspaceID), string(fixture.CenterTopicID),
	).Scan(&nodes, &relations, &evidence, &hotDegree)
	if err != nil {
		return fmt.Errorf("count graph capacity fixture: %w", err)
	}
	if nodes != fixture.NodeCount || relations != fixture.RelationCount || evidence != fixture.EvidenceCount || hotDegree != fixture.HotCenterDegree {
		return fmt.Errorf("graph capacity fixture counts nodes=%d relations=%d evidence=%d hot_degree=%d", nodes, relations, evidence, hotDegree)
	}
	return nil
}

func cleanupCapacityFixture(pool *pgxpool.Pool, workspaceID foundation.ID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return CleanupCapacity(ctx, pool, workspaceID)
}

// MD5 仅把 seed 映射为稳定的 128-bit 测试 UUID；Knowledge 业务指纹仍使用 canonical SHA-256。
const insertCapacityTopicsSQL = `
INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
SELECT md5($1 || ':topic:' || ordinal::text)::uuid,$2,
       'Capacity Topic ' || to_char(ordinal,'FM00000'),
       'capacity topic ' || to_char(ordinal,'FM00000'),
       'Deterministic graph capacity fixture','ACTIVE',1,$5,$5
FROM generate_series($3::int,$4::int-1) AS generated(ordinal)`

const insertCapacityRelationsSQL = `
WITH parameters AS (
    SELECT (('x' || substr(md5($1 || ':topology'),1,8))::bit(32)::bigint % ($6::int-1)) AS rotation
), topology AS (
    SELECT ordinal,
	       CASE WHEN ordinal < $5::int THEN 0
	            ELSE 1 + (((ordinal-$5::int) + rotation) % ($6::int-1))
	       END AS source_ordinal,
	       CASE WHEN ordinal < $5::int THEN ordinal+1
	            ELSE 1 + ((((ordinal-$5::int) + rotation) % ($6::int-1) + 1 + ((ordinal-$5::int) / ($6::int-1))) % ($6::int-1))
	       END AS target_ordinal
	FROM generate_series($3::int,$4::int-1) AS generated(ordinal)
	CROSS JOIN parameters
), edges AS (
    SELECT ordinal,
           md5($1 || ':relation:' || ordinal::text)::uuid AS relation_id,
           md5($1 || ':topic:' || source_ordinal::text)::uuid AS source_id,
           md5($1 || ':topic:' || target_ordinal::text)::uuid AS target_id
    FROM topology
)
INSERT INTO core.relation(
    id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,
    confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at
)
SELECT relation_id,$2::uuid,'TOPIC',source_id,'TOPIC',target_id,'IMPACTS','SUGGESTED',0.9,
       encode(sha256(
           convert_to('knowledge-relation/v1' || chr(10) || $2::text || chr(10) || 'IMPACTS' || chr(10) || 'TOPIC','UTF8')
           || decode('00','hex') || convert_to(source_id::text || chr(10) || 'TOPIC','UTF8')
           || decode('00','hex') || convert_to(target_id::text,'UTF8')
       ),'hex'),
	   NULL,NULL,NULL,1,$7,$7
FROM edges`

const insertCapacityEvidenceSQL = `
WITH generated AS (
    SELECT ordinal,
           md5($1 || ':relation:' || ordinal::text)::uuid AS relation_id,
           md5($1 || ':evidence:' || ordinal::text)::uuid AS evidence_id
	FROM generate_series($3::int,$4::int-1) AS series(ordinal)
), semantic AS (
    SELECT ordinal,relation_id,evidence_id,
           encode(sha256(convert_to(format(
		       '{"schema_version":"knowledge-relation-evidence-semantic/v1","workspace_id":"%s","source_version_id":"%s","source_span_id":"%s","reason":%s,"applicability_hash":"%s","model_run_ref":"","confirmation_method":"SOURCE_DERIVED","confirmation_reference":%s}',
		       $2::text,$5::text,$6::text,to_json($7::text)::text,$10::text,to_json($11::text)::text
		   ),'UTF8')),'hex') AS semantic_hash
    FROM generated
), evidence AS (
    SELECT ordinal,relation_id,evidence_id,
           encode(sha256(convert_to(format(
               '{"schema_version":"knowledge-relation-evidence/v1","relation_id":"%s","semantic_hash":"%s"}',
               relation_id::text,semantic_hash
           ),'UTF8')),'hex') AS evidence_hash
    FROM semantic
)
INSERT INTO core.relation_evidence(
    id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,
    applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at
)
SELECT evidence_id,$2::uuid,relation_id,$5::uuid,$6::uuid,$7,evidence_hash,$8::jsonb,$9,$10,'SOURCE_DERIVED',$11,$12
FROM evidence`

const confirmCapacityRelationsSQL = `
WITH selected AS (
    SELECT md5($1 || ':relation:' || ordinal::text)::uuid AS relation_id
    FROM generate_series($3::int,$4::int-1) AS series(ordinal)
)
UPDATE core.relation AS relation
SET status='CONFIRMED',
    evidence_fingerprint=encode(sha256(convert_to(
        'knowledge-relation-evidence-set/v1' || chr(10) || evidence.evidence_hash,'UTF8'
    )),'hex'),
	confirmation_method='SOURCE_DERIVED',confirmation_ref=$5,
	version=relation.version+1,updated_at=$6
FROM core.relation_evidence AS evidence
JOIN selected ON selected.relation_id=evidence.relation_id
WHERE relation.workspace_id=$2 AND relation.status='SUGGESTED'
  AND evidence.workspace_id=relation.workspace_id AND evidence.relation_id=relation.id`
