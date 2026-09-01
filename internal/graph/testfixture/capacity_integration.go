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
	// M10CapacityFixtureVersion 是正式 500k Relation 容量夹具版本。
	M10CapacityFixtureVersion = "graph-capacity/m10-v1"
	// M10MixedCapacityFixtureVersion 是 Claim-heavy 混合图谱容量夹具版本。
	M10MixedCapacityFixtureVersion = "graph-capacity/m10-mixed-v1"
	// CapacityNodeCount 是容量夹具中 ACTIVE TOPIC 的固定数量。
	CapacityNodeCount = 20_000
	// M10CapacityNodeCount 是正式 Relation 容量夹具使用的节点数量。
	M10CapacityNodeCount = 100_000
	// M10MixedCapacityTopicCount 是 Claim-heavy 正式容量夹具中的 Topic 数量。
	M10MixedCapacityTopicCount = 20_000
	// M10MixedCapacityClaimCount 是 Claim-heavy 正式容量夹具中的 CONFIRMED Claim 数量。
	M10MixedCapacityClaimCount = 100_000
	// CapacityRelationCount 是容量夹具中 CONFIRMED IMPACTS 的固定数量。
	CapacityRelationCount = 100_000
	// M10CapacityRelationCount 是正式容量基线的 Relation 数量。
	M10CapacityRelationCount = 500_000
	// CapacityEvidenceCount 是容量夹具中 Relation Evidence 的固定数量。
	CapacityEvidenceCount = 100_000
	// M10CapacityEvidenceCount 是正式容量基线的 Relation Evidence 数量。
	M10CapacityEvidenceCount = 500_000
	// CapacityHotCenterDegree 是一跳基准中心节点的固定关联边数。
	CapacityHotCenterDegree = 499

	capacityConfirmationReference = "graph capacity benchmark fixture"
	capacityEvidenceReason        = "capacity benchmark evidence"
	capacityTopicWriteChunk       = 1_000
	capacityRelationWriteChunk    = 5_000
	capacityWorkspaceName         = "graph-capacity-benchmark"
	m10CapacityWorkspaceName      = "graph-capacity-m10-benchmark"
	m10MixedCapacityWorkspaceName = "graph-capacity-m10-mixed-benchmark"
	maxCapacitySeedBytes          = 128
	capacityClaimSupportReason    = "capacity benchmark claim support"
)

type capacityShape struct {
	version, workspaceName, rootPrefix string
	topicCount, claimCount             int
	relationCount                      int
	evidenceCount, hotCenterDegree     int
	mixed                              bool
}

var referenceCapacityShape = capacityShape{
	version: CapacityFixtureVersion, workspaceName: capacityWorkspaceName, rootPrefix: "graph-capacity-benchmark",
	topicCount: CapacityNodeCount, relationCount: CapacityRelationCount,
	evidenceCount: CapacityEvidenceCount, hotCenterDegree: CapacityHotCenterDegree,
}

var m10CapacityShape = capacityShape{
	version: M10CapacityFixtureVersion, workspaceName: m10CapacityWorkspaceName, rootPrefix: "graph-capacity-m10-benchmark",
	topicCount: M10CapacityNodeCount, relationCount: M10CapacityRelationCount,
	evidenceCount: M10CapacityEvidenceCount, hotCenterDegree: CapacityHotCenterDegree,
}

var m10MixedCapacityShape = capacityShape{
	version: M10MixedCapacityFixtureVersion, workspaceName: m10MixedCapacityWorkspaceName,
	rootPrefix: "graph-capacity-m10-mixed-benchmark",
	topicCount: M10MixedCapacityTopicCount, claimCount: M10MixedCapacityClaimCount,
	relationCount: M10CapacityRelationCount, evidenceCount: M10CapacityEvidenceCount,
	hotCenterDegree: CapacityHotCenterDegree, mixed: true,
}

func (shape capacityShape) totalNodeCount() int {
	return shape.topicCount + shape.claimCount
}

// CapacityFixture 描述已提交的确定性 Graph 容量数据及代表查询身份。
type CapacityFixture struct {
	Version            string
	Seed               string
	WorkspaceName      string
	WorkspaceRoot      string
	WorkspaceID        foundation.ID
	CenterNodeType     knowledge.NodeType
	CenterNodeID       foundation.ID
	PathTargetNodeType knowledge.NodeType
	PathTargetNodeID   foundation.ID
	CenterTopicID      foundation.ID
	PathTargetTopicID  foundation.ID
	EvidenceRelationID foundation.ID
	NodeCount          int
	TopicCount         int
	ClaimCount         int
	RelationCount      int
	BelongsToCount     int
	ImpactsCount       int
	EvidenceCount      int
	HotCenterDegree    int
}

type capacityIDs struct {
	workspaceID, artifactID, sourceID, sourceVersionID foundation.ID
	projectionID, sourceSpanID                         foundation.ID
	centerNodeID, pathTargetNodeID, evidenceRelationID foundation.ID
}

// SeedCapacity 使用集合式 SQL 提交由 seed 决定的 M7 20k/100k Graph 参考夹具。
func SeedCapacity(ctx context.Context, pool *pgxpool.Pool, seed string) (CapacityFixture, error) {
	return seedCapacity(ctx, pool, seed, referenceCapacityShape)
}

// SeedM10Capacity 使用集合式 SQL 提交 100k Node/500k Relation 正式容量夹具。
func SeedM10Capacity(ctx context.Context, pool *pgxpool.Pool, seed string) (CapacityFixture, error) {
	return seedCapacity(ctx, pool, seed, m10CapacityShape)
}

// SeedM10MixedCapacity 提交 20k Topic、100k Confirmed Claim 与 500k 混合 Relation 正式夹具。
func SeedM10MixedCapacity(ctx context.Context, pool *pgxpool.Pool, seed string) (CapacityFixture, error) {
	return seedCapacity(ctx, pool, seed, m10MixedCapacityShape)
}

func seedCapacity(ctx context.Context, pool *pgxpool.Pool, seed string, shape capacityShape) (CapacityFixture, error) {
	if ctx == nil {
		return CapacityFixture{}, errors.New("graph capacity fixture context is nil")
	}
	if pool == nil {
		return CapacityFixture{}, errors.New("graph capacity fixture pool is nil")
	}
	if seed == "" || seed != strings.TrimSpace(seed) || !utf8.ValidString(seed) || len([]byte(seed)) > maxCapacitySeedBytes {
		return CapacityFixture{}, errors.New("graph capacity fixture seed must be canonical bounded UTF-8")
	}
	if shape.topicCount < 2 || shape.claimCount < 0 || shape.relationCount < 1 || shape.evidenceCount != shape.relationCount ||
		shape.hotCenterDegree < 1 || shape.hotCenterDegree >= shape.totalNodeCount() || shape.version == "" ||
		shape.workspaceName == "" || shape.rootPrefix == "" {
		return CapacityFixture{}, errors.New("graph capacity fixture shape is invalid")
	}
	if shape.mixed && (shape.claimCount < shape.hotCenterDegree || shape.relationCount%2 != 0) {
		return CapacityFixture{}, errors.New("mixed graph capacity fixture shape is invalid")
	}
	ids, err := deriveCapacityIDs(ctx, pool, seed, shape)
	if err != nil {
		return CapacityFixture{}, fmt.Errorf("derive graph capacity fixture identities: %w", err)
	}
	workspaceRoot := capacityWorkspaceRoot(shape, ids.workspaceID)
	if err := cleanupWorkspace(ctx, pool, ids.workspaceID, shape.workspaceName, workspaceRoot, false); err != nil {
		return CapacityFixture{}, fmt.Errorf("remove previous graph capacity fixture: %w", err)
	}
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	applicabilityJSON, marshalErr := json.Marshal(map[string]string{"scope": "graph-capacity", "version": shape.version})
	if marshalErr != nil {
		return CapacityFixture{}, fmt.Errorf("build graph capacity applicability JSON: %w", marshalErr)
	}
	applicability, err := knowledge.ParseApplicability(applicabilityJSON)
	if err != nil {
		return CapacityFixture{}, fmt.Errorf("build graph capacity applicability: %w", err)
	}
	fail := func(cause error) (CapacityFixture, error) {
		if cleanupErr := cleanupCapacityFixture(pool, ids.workspaceID, shape); cleanupErr != nil {
			return CapacityFixture{}, errors.Join(cause, fmt.Errorf("cleanup partial graph capacity fixture: %w", cleanupErr))
		}
		return CapacityFixture{}, cause
	}
	if err := withCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
		if err := insertCapacityWorkspace(ctx, tx, ids.workspaceID, shape, now); err != nil {
			return fmt.Errorf("insert workspace: %w", err)
		}
		if err := insertCapacityProvenance(ctx, tx, ids, seed, shape, now); err != nil {
			return fmt.Errorf("insert provenance: %w", err)
		}
		return nil
	}); err != nil {
		return fail(fmt.Errorf("commit graph capacity foundation: %w", err))
	}
	if err := insertCapacityTopicChunks(ctx, pool, seed, ids.workspaceID, shape, now); err != nil {
		return fail(err)
	}
	if shape.mixed {
		if err := insertCapacityClaimChunks(ctx, pool, seed, ids, shape, applicability, now); err != nil {
			return fail(err)
		}
	}
	if err := insertCapacityRelationChunks(ctx, pool, seed, ids, shape, applicability, now); err != nil {
		return fail(err)
	}

	centerType, pathTargetType := knowledge.NodeTypeTopic, knowledge.NodeTypeTopic
	if shape.mixed {
		centerType = knowledge.NodeTypeClaim
	}
	belongsToCount := 0
	impactsCount := shape.relationCount
	if shape.mixed {
		belongsToCount, impactsCount = shape.relationCount/2, shape.relationCount/2
	}
	fixture := CapacityFixture{
		Version: shape.version, Seed: seed, WorkspaceName: shape.workspaceName, WorkspaceRoot: workspaceRoot,
		WorkspaceID:    ids.workspaceID,
		CenterNodeType: centerType, CenterNodeID: ids.centerNodeID,
		PathTargetNodeType: pathTargetType, PathTargetNodeID: ids.pathTargetNodeID,
		EvidenceRelationID: ids.evidenceRelationID, NodeCount: shape.totalNodeCount(),
		TopicCount: shape.topicCount, ClaimCount: shape.claimCount,
		RelationCount: shape.relationCount, BelongsToCount: belongsToCount, ImpactsCount: impactsCount,
		EvidenceCount:   shape.evidenceCount,
		HotCenterDegree: shape.hotCenterDegree,
	}
	if !shape.mixed {
		fixture.CenterTopicID, fixture.PathTargetTopicID = ids.centerNodeID, ids.pathTargetNodeID
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
	return cleanupWorkspace(ctx, pool, workspaceID, referenceCapacityShape.workspaceName, capacityWorkspaceRoot(referenceCapacityShape, workspaceID), false)
}

// CleanupCapacityFixture 按 fixture 中的精确 marker 删除参考或 M10 容量数据。
func CleanupCapacityFixture(ctx context.Context, pool *pgxpool.Pool, fixture CapacityFixture) error {
	var shape capacityShape
	switch fixture.Version {
	case CapacityFixtureVersion:
		shape = referenceCapacityShape
	case M10CapacityFixtureVersion:
		shape = m10CapacityShape
	case M10MixedCapacityFixtureVersion:
		shape = m10MixedCapacityShape
	default:
		return errors.New("graph capacity fixture cleanup version is invalid")
	}
	expectedRoot := capacityWorkspaceRoot(shape, fixture.WorkspaceID)
	if fixture.WorkspaceName != shape.workspaceName || fixture.WorkspaceRoot != expectedRoot {
		return errors.New("graph capacity fixture cleanup marker does not match version")
	}
	return cleanupWorkspace(ctx, pool, fixture.WorkspaceID, shape.workspaceName, expectedRoot, false)
}

func capacityWorkspaceRoot(shape capacityShape, workspaceID foundation.ID) string {
	return "/tmp/" + shape.rootPrefix + "-" + string(workspaceID)
}

func insertCapacityWorkspace(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, shape capacityShape, now time.Time) error {
	root := capacityWorkspaceRoot(shape, workspaceID)
	_, err := tx.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), shape.workspaceName, root, now)
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

func insertCapacityTopicChunks(
	ctx context.Context,
	pool *pgxpool.Pool,
	seed string,
	workspaceID foundation.ID,
	shape capacityShape,
	now time.Time,
) error {
	for start := 0; start < shape.topicCount; start += capacityTopicWriteChunk {
		end := start + capacityTopicWriteChunk
		if end > shape.topicCount {
			end = shape.topicCount
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

func insertCapacityClaimChunks(
	ctx context.Context,
	pool *pgxpool.Pool,
	seed string,
	ids capacityIDs,
	shape capacityShape,
	applicability knowledge.Applicability,
	now time.Time,
) error {
	for start := 0; start < shape.claimCount; start += capacityTopicWriteChunk {
		end := min(start+capacityTopicWriteChunk, shape.claimCount)
		if err := withCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, insertCapacityClaimsSQL,
				seed, string(ids.workspaceID), start, end,
				string(applicability.CanonicalJSON), applicability.SchemaVersion, applicability.Hash, now,
			); err != nil {
				return fmt.Errorf("insert claims: %w", err)
			}
			if _, err := tx.Exec(ctx, insertCapacityClaimSourcesSQL,
				seed, string(ids.workspaceID), start, end, string(ids.sourceVersionID), string(ids.sourceSpanID),
				capacityClaimSupportReason, applicability.Hash, now,
			); err != nil {
				return fmt.Errorf("insert claim sources: %w", err)
			}
			if _, err := tx.Exec(ctx, confirmCapacityClaimsSQL,
				seed, string(ids.workspaceID), start, end, now.Add(time.Second),
			); err != nil {
				return fmt.Errorf("confirm claims: %w", err)
			}
			if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
				return fmt.Errorf("verify claim constraints: %w", err)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("commit graph capacity claims [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func insertCapacityRelationChunks(
	ctx context.Context,
	pool *pgxpool.Pool,
	seed string,
	ids capacityIDs,
	shape capacityShape,
	applicability knowledge.Applicability,
	now time.Time,
) error {
	for start := 0; start < shape.relationCount; start += capacityRelationWriteChunk {
		end := start + capacityRelationWriteChunk
		if end > shape.relationCount {
			end = shape.relationCount
		}
		if err := withCapacityTransaction(ctx, pool, func(tx pgx.Tx) error {
			relationSQL := insertCapacityRelationsSQL
			arguments := []any{seed, string(ids.workspaceID), start, end, shape.hotCenterDegree, shape.topicCount, now}
			if shape.mixed {
				relationSQL = insertMixedCapacityRelationsSQL
				arguments = []any{
					seed, string(ids.workspaceID), start, end, shape.hotCenterDegree,
					shape.topicCount, shape.claimCount, now,
				}
			}
			if _, err := tx.Exec(ctx, relationSQL, arguments...); err != nil {
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

func deriveCapacityIDs(ctx context.Context, pool *pgxpool.Pool, seed string, shape capacityShape) (capacityIDs, error) {
	centerKind, pathTargetKind, pathTargetOrdinal := "topic", "topic", shape.hotCenterDegree
	if shape.mixed {
		centerKind, pathTargetOrdinal = "claim", 1
	}
	var raw [9]string
	err := pool.QueryRow(ctx, `SELECT
		md5($1 || ':workspace')::uuid::text,
		md5($1 || ':artifact')::uuid::text,
		md5($1 || ':source')::uuid::text,
		md5($1 || ':source-version')::uuid::text,
		md5($1 || ':projection')::uuid::text,
		md5($1 || ':source-span')::uuid::text,
		md5($1 || ':' || $2 || ':0')::uuid::text,
		md5($1 || ':' || $3 || ':' || $4::int::text)::uuid::text,
		md5($1 || ':relation:0')::uuid::text`, seed, centerKind, pathTargetKind, pathTargetOrdinal).Scan(
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
		projectionID: parsed[4], sourceSpanID: parsed[5], centerNodeID: parsed[6],
		pathTargetNodeID: parsed[7], evidenceRelationID: parsed[8],
	}, nil
}

func insertCapacityProvenance(ctx context.Context, tx pgx.Tx, ids capacityIDs, seed string, shape capacityShape, now time.Time) error {
	content := []byte(shape.version + "\n" + seed)
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
				id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
			) VALUES($1,$2,$3,$4,$5,$6,'text/plain',$7,'pending',$8)`,
			args: []any{string(ids.sourceVersionID), string(ids.sourceID), string(ids.workspaceID), string(ids.artifactID), contentHash, len(content), "graph-capacity-fixture.txt", now},
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
		`ANALYZE core.claim`,
		`ANALYZE core.claim_source`,
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
	var topics, claims, belongsTo, impacts, evidence, hotDegree int
	err := pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*)::int FROM core.topic WHERE workspace_id=$1),
		(SELECT COUNT(*)::int FROM core.claim WHERE workspace_id=$1 AND status='CONFIRMED'),
		(SELECT COUNT(*)::int FROM core.relation WHERE workspace_id=$1 AND status='CONFIRMED' AND relation_type='BELONGS_TO'),
		(SELECT COUNT(*)::int FROM core.relation WHERE workspace_id=$1 AND status='CONFIRMED' AND relation_type='IMPACTS'),
		(SELECT COUNT(*)::int FROM core.relation_evidence WHERE workspace_id=$1),
		(SELECT COUNT(*)::int FROM core.relation WHERE workspace_id=$1
			AND ((source_node_type=$2 AND source_node_id=$3) OR (target_node_type=$2 AND target_node_id=$3)))`,
		string(fixture.WorkspaceID), string(fixture.CenterNodeType), string(fixture.CenterNodeID),
	).Scan(&topics, &claims, &belongsTo, &impacts, &evidence, &hotDegree)
	if err != nil {
		return fmt.Errorf("count graph capacity fixture: %w", err)
	}
	if topics != fixture.TopicCount || claims != fixture.ClaimCount || belongsTo != fixture.BelongsToCount ||
		impacts != fixture.ImpactsCount || belongsTo+impacts != fixture.RelationCount ||
		evidence != fixture.EvidenceCount || hotDegree != fixture.HotCenterDegree {
		return fmt.Errorf(
			"graph capacity fixture counts topics=%d claims=%d belongs_to=%d impacts=%d evidence=%d hot_degree=%d",
			topics, claims, belongsTo, impacts, evidence, hotDegree,
		)
	}
	return nil
}

func cleanupCapacityFixture(pool *pgxpool.Pool, workspaceID foundation.ID, shape capacityShape) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return cleanupWorkspace(ctx, pool, workspaceID, shape.workspaceName, capacityWorkspaceRoot(shape, workspaceID), false)
}

// MD5 仅把 seed 映射为稳定的 128-bit 测试 UUID；Knowledge 业务指纹仍使用 canonical SHA-256。
const insertCapacityTopicsSQL = `
INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
SELECT md5($1 || ':topic:' || ordinal::text)::uuid,$2,
       'Capacity Topic ' || to_char(ordinal,'FM00000'),
       'capacity topic ' || to_char(ordinal,'FM00000'),
       'Deterministic graph capacity fixture','ACTIVE',1,$5,$5
FROM generate_series($3::int,$4::int-1) AS generated(ordinal)`

const insertCapacityClaimsSQL = `
WITH generated AS (
	SELECT ordinal,
		md5($1 || ':claim:' || ordinal::text)::uuid AS claim_id,
		'Capacity claim ' || to_char(ordinal,'FM000000') AS statement
	FROM generate_series($3::int,$4::int-1) AS series(ordinal)
)
INSERT INTO core.claim(
	id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
	applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
)
SELECT claim_id,$2::uuid,statement,lower(statement),$5::jsonb,$6,$7,'SUGGESTED',0.9,'{}'::jsonb,
	encode(sha256(convert_to(
		'knowledge-claim/v1' || chr(10) || $2::text || chr(10) || lower(statement) || chr(10) || $7,
		'UTF8'
	)),'hex'),1,$8,$8
FROM generated`

const insertCapacityClaimSourcesSQL = `
WITH generated AS (
	SELECT ordinal,
		md5($1 || ':claim:' || ordinal::text)::uuid AS claim_id,
		md5($1 || ':claim-source:' || ordinal::text)::uuid AS claim_source_id
	FROM generate_series($3::int,$4::int-1) AS series(ordinal)
), evidence AS (
	SELECT ordinal,claim_id,claim_source_id,
		encode(sha256(convert_to(format(
			'{"schema_version":"knowledge-claim-source/v1","workspace_id":"%s","claim_id":"%s","source_version_id":"%s","source_span_id":"%s","support_type":"SUPPORTS","reason":%s,"applicability_hash":"%s","model_run_ref":""}',
			$2::text,claim_id::text,$5::text,$6::text,to_json($7::text)::text,$8::text
		),'UTF8')),'hex') AS evidence_hash
	FROM generated
)
INSERT INTO core.claim_source(
	id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,model_run_ref,created_at
)
SELECT claim_source_id,$2::uuid,claim_id,$5::uuid,$6::uuid,'SUPPORTS',$7,evidence_hash,NULL,$9
FROM evidence`

const confirmCapacityClaimsSQL = `
WITH selected AS (
	SELECT md5($1 || ':claim:' || ordinal::text)::uuid AS claim_id
	FROM generate_series($3::int,$4::int-1) AS series(ordinal)
)
UPDATE core.claim AS claim
SET status='CONFIRMED',version=claim.version+1,updated_at=$5
FROM selected
WHERE claim.workspace_id=$2::uuid AND claim.id=selected.claim_id AND claim.status='SUGGESTED'`

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

const insertMixedCapacityRelationsSQL = `
WITH topology AS (
	SELECT ordinal,
		CASE WHEN ordinal < $5::int THEN 0
			 ELSE 1 + ((ordinal / 2) % ($7::int - 1))
		END AS source_claim_ordinal,
		CASE WHEN ordinal % 2 = 0 THEN 'BELONGS_TO' ELSE 'IMPACTS' END AS relation_type,
		CASE WHEN ordinal % 2 = 0 THEN 'TOPIC' ELSE 'CLAIM' END AS target_node_type,
		CASE
			WHEN ordinal < $5::int THEN 1 + (ordinal / 2)
			WHEN ordinal % 2 = 0 THEN (ordinal / 2) % $6::int
			ELSE 1 + ((((ordinal / 2) % ($7::int - 1)) + 1 + (ordinal / (2 * ($7::int - 1)))) % ($7::int - 1))
		END AS target_ordinal
	FROM generate_series($3::int,$4::int-1) AS generated(ordinal)
), edges AS (
	SELECT ordinal,
		md5($1 || ':relation:' || ordinal::text)::uuid AS relation_id,
		md5($1 || ':claim:' || source_claim_ordinal::text)::uuid AS source_id,
		relation_type,target_node_type,
		md5($1 || ':' || lower(target_node_type) || ':' || target_ordinal::text)::uuid AS target_id
	FROM topology
)
INSERT INTO core.relation(
	id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,
	confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at
)
SELECT relation_id,$2::uuid,'CLAIM',source_id,target_node_type,target_id,relation_type,'SUGGESTED',0.9,
	encode(sha256(
		convert_to('knowledge-relation/v1' || chr(10) || $2::text || chr(10) || relation_type || chr(10) || 'CLAIM','UTF8')
		|| decode('00','hex') || convert_to(source_id::text || chr(10) || target_node_type,'UTF8')
		|| decode('00','hex') || convert_to(target_id::text,'UTF8')
	),'hex'),
	NULL,NULL,NULL,1,$8,$8
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
