//go:build integration

package testfixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture 描述 Graph HTTP 集成测试所需的已提交知识事实集合。
type Fixture struct {
	WorkspaceID                   foundation.ID
	PrimaryTopicID                foundation.ID
	SecondaryTopicID              foundation.ID
	FirstClaimID                  foundation.ID
	SecondClaimID                 foundation.ID
	MembershipRelationID          foundation.ID
	SupportRelationID             foundation.ID
	SecondaryMembershipRelationID foundation.ID
}

// SemanticLinkBrowserFixture 在正式 Graph fixture 上增加可被 Topic scan 发现的第三条 Claim。
type SemanticLinkBrowserFixture struct {
	Fixture
	DiscoveryClaimID foundation.ID
}

// SeedFunctional 写入可用于 Global→Local→Path→Evidence 公共 HTTP 链路的已提交夹具。
func SeedFunctional(ctx context.Context, pool *pgxpool.Pool) (Fixture, error) {
	if pool == nil {
		return Fixture{}, errors.New("graph integration fixture pool is nil")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Fixture{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	ids, err := newIDs(5)
	if err != nil {
		return Fixture{}, err
	}
	workspaceID, primaryTopicID, secondaryTopicID := ids[0], ids[1], ids[2]
	firstClaimID, secondClaimID := ids[3], ids[4]

	if err := insertWorkspace(ctx, tx, workspaceID, now); err != nil {
		return Fixture{}, err
	}
	provenance, err := insertProvenance(ctx, tx, workspaceID, now)
	if err != nil {
		return Fixture{}, err
	}

	if err := insertTopic(ctx, tx, workspaceID, primaryTopicID, "Go Concurrency", "goroutine 和 channel 的并发事实", now); err != nil {
		return Fixture{}, err
	}
	if err := insertTopic(ctx, tx, workspaceID, secondaryTopicID, "Graph Databases", "图查询与投影对比", now.Add(time.Second)); err != nil {
		return Fixture{}, err
	}

	firstClaim, err := newClaim(workspaceID, firstClaimID, "Channels coordinate goroutines", 0.93, now.Add(2*time.Second))
	if err != nil {
		return Fixture{}, err
	}
	secondClaim, err := newClaim(workspaceID, secondClaimID, "Graph projections should stay read only", 0.87, now.Add(3*time.Second))
	if err != nil {
		return Fixture{}, err
	}

	if err := insertConfirmedClaim(ctx, tx, firstClaim, provenance, "primary source confirms channel coordination"); err != nil {
		return Fixture{}, err
	}
	if err := insertConfirmedClaim(ctx, tx, secondClaim, provenance, "projection source confirms read-only graph query"); err != nil {
		return Fixture{}, err
	}

	relationIDs, err := newIDs(4)
	if err != nil {
		return Fixture{}, err
	}
	membershipRelationID, primarySecondMembershipID := relationIDs[0], relationIDs[1]
	secondaryMembershipID, supportRelationID := relationIDs[2], relationIDs[3]

	membershipRelation, err := newRelation(workspaceID, membershipRelationID, knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: firstClaimID}, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: primaryTopicID}, knowledge.RelationBelongsTo, 0.96, now.Add(4*time.Second))
	if err != nil {
		return Fixture{}, err
	}
	if err := insertConfirmedRelation(ctx, tx, membershipRelation, provenance, "membership evidence for primary topic"); err != nil {
		return Fixture{}, err
	}
	primarySecondMembership, err := newRelation(workspaceID, primarySecondMembershipID, knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: secondClaimID}, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: primaryTopicID}, knowledge.RelationBelongsTo, 0.91, now.Add(5*time.Second))
	if err != nil {
		return Fixture{}, err
	}
	if err := insertConfirmedRelation(ctx, tx, primarySecondMembership, provenance, "second claim also belongs to primary topic"); err != nil {
		return Fixture{}, err
	}
	secondaryMembership, err := newRelation(workspaceID, secondaryMembershipID, knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: secondClaimID}, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: secondaryTopicID}, knowledge.RelationBelongsTo, 0.89, now.Add(6*time.Second))
	if err != nil {
		return Fixture{}, err
	}
	if err := insertConfirmedRelation(ctx, tx, secondaryMembership, provenance, "path evidence for secondary topic membership"); err != nil {
		return Fixture{}, err
	}
	supportRelation, err := newRelation(workspaceID, supportRelationID, knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: firstClaimID}, knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: secondClaimID}, knowledge.RelationSupports, 0.94, now.Add(7*time.Second))
	if err != nil {
		return Fixture{}, err
	}
	if err := insertConfirmedRelation(ctx, tx, supportRelation, provenance, "support evidence for graph read-only projection"); err != nil {
		return Fixture{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if cleanupErr := Cleanup(cleanupCtx, pool, workspaceID); cleanupErr != nil {
			return Fixture{}, errors.Join(err, fmt.Errorf("cleanup graph fixture after unknown commit result: %w", cleanupErr))
		}
		return Fixture{}, err
	}
	committed = true

	return Fixture{
		WorkspaceID:                   workspaceID,
		PrimaryTopicID:                primaryTopicID,
		SecondaryTopicID:              secondaryTopicID,
		FirstClaimID:                  firstClaimID,
		SecondClaimID:                 secondClaimID,
		MembershipRelationID:          membershipRelationID,
		SupportRelationID:             supportRelationID,
		SecondaryMembershipRelationID: secondaryMembershipID,
	}, nil
}

// SeedSemanticLinkBrowser 写入同时覆盖正式 Graph 与 Semantic Link Candidate 的浏览器夹具。
func SeedSemanticLinkBrowser(ctx context.Context, pool *pgxpool.Pool) (result SemanticLinkBrowserFixture, resultErr error) {
	base, err := SeedFunctional(ctx, pool)
	if err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	cleanupRequired := true
	defer func() {
		if !cleanupRequired {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if cleanupErr := CleanupSemanticLinkBrowser(cleanupCtx, pool, base.WorkspaceID); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("cleanup semantic-link browser fixture: %w", cleanupErr))
		}
	}()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	var sourceVersionID, sourceSpanID string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text
		FROM core.claim_source WHERE workspace_id=$1 AND claim_id=$2 ORDER BY id LIMIT 1`,
		string(base.WorkspaceID), string(base.FirstClaimID),
	).Scan(&sourceVersionID, &sourceSpanID); err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	provenance := provenanceBinding{sourceVersionID: foundation.ID(sourceVersionID), sourceSpanID: foundation.ID(sourceSpanID)}
	ids, err := newIDs(2)
	if err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	discoveryClaimID, membershipRelationID := ids[0], ids[1]
	now := time.Date(2026, 7, 20, 9, 0, 10, 0, time.UTC)
	claim, err := newClaim(base.WorkspaceID, discoveryClaimID, "Durable channels coordinate concurrent workers", 0.91, now)
	if err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	if err := insertConfirmedClaim(ctx, tx, claim, provenance, "semantic browser candidate evidence"); err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	membership, err := newRelation(
		base.WorkspaceID,
		membershipRelationID,
		knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: discoveryClaimID},
		knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: base.PrimaryTopicID},
		knowledge.RelationBelongsTo,
		0.92,
		now.Add(time.Second),
	)
	if err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	if err := insertConfirmedRelation(ctx, tx, membership, provenance, "semantic browser topic membership"); err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SemanticLinkBrowserFixture{}, err
	}
	committed = true
	cleanupRequired = false
	return SemanticLinkBrowserFixture{Fixture: base, DiscoveryClaimID: discoveryClaimID}, nil
}

// Cleanup 按外键依赖顺序删除指定 Workspace 的集成夹具。
func Cleanup(ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) error {
	return cleanupWorkspace(ctx, pool, workspaceID, functionalWorkspaceName, functionalWorkspaceRoot(workspaceID), false)
}

// CleanupSemanticLinkBrowser 删除浏览器夹具及其 Candidate、Scan、Workflow 和 River 事实。
func CleanupSemanticLinkBrowser(ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) error {
	return cleanupWorkspace(ctx, pool, workspaceID, functionalWorkspaceName, functionalWorkspaceRoot(workspaceID), true)
}

func cleanupWorkspace(
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID foundation.ID,
	expectedName string,
	expectedRoot string,
	semanticLinkBrowser bool,
) error {
	if pool == nil {
		return errors.New("graph integration cleanup pool is nil")
	}
	parsedWorkspaceID, err := foundation.ParseID(string(workspaceID))
	if err != nil {
		return err
	}
	if parsedWorkspaceID != workspaceID || expectedName == "" || expectedRoot == "" {
		return errors.New("graph integration cleanup marker is invalid")
	}
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

	var name, rootPath, gitRepositoryPath, status string
	var version int64
	err = tx.QueryRow(ctx, `SELECT name,root_path,git_repository_path,status,version
		FROM core.workspace WHERE id=$1 FOR UPDATE`, string(workspaceID)).Scan(
		&name, &rootPath, &gitRepositoryPath, &status, &version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err != nil {
		return err
	}
	if name != expectedName || rootPath != expectedRoot || gitRepositoryPath != expectedRoot || status != "test" || version != 1 {
		return errors.New("graph integration cleanup workspace marker does not match")
	}

	// Knowledge provenance is immutable in production. Integration fixtures use
	// a transaction-local replica role so cleanup cannot weaken runtime sessions.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return err
	}
	if semanticLinkBrowser {
		if _, err := tx.Exec(ctx, `DELETE FROM workflow.river_job
			WHERE kind=$2 AND args->>'node_run_id' IN (
				SELECT node.id::text
				FROM workflow.node_run node
				JOIN workflow.run run ON run.id=node.run_id
				WHERE run.workspace_id=$1
			)`, string(workspaceID), riveradapter.NodeJobKind); err != nil {
			return err
		}
		semanticLinkStatements := []string{
			`DELETE FROM graph.semantic_link_candidate_decision WHERE workspace_id=$1`,
			`DELETE FROM graph.semantic_link_candidate_evidence WHERE workspace_id=$1`,
			`DELETE FROM graph.semantic_link_candidate WHERE workspace_id=$1`,
			`DELETE FROM workflow.control_command WHERE run_id IN (SELECT id FROM workflow.run WHERE workspace_id=$1)`,
			`DELETE FROM workflow.human_task WHERE run_id IN (SELECT id FROM workflow.run WHERE workspace_id=$1)`,
			`DELETE FROM workflow.node_attempt WHERE node_run_id IN (
				SELECT node.id FROM workflow.node_run node JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1
			)`,
			`DELETE FROM workflow.outbox_event WHERE workspace_id=$1`,
			`DELETE FROM graph.semantic_link_scan WHERE workspace_id=$1`,
			`DELETE FROM workflow.node_run WHERE run_id IN (SELECT id FROM workflow.run WHERE workspace_id=$1)`,
			`DELETE FROM workflow.run WHERE workspace_id=$1`,
			`DELETE FROM workflow.definition WHERE workspace_id=$1`,
		}
		for _, statement := range semanticLinkStatements {
			if _, err := tx.Exec(ctx, statement, string(workspaceID)); err != nil {
				return err
			}
		}
	}
	statements := []string{
		`DELETE FROM ingestion.canonical_chunk WHERE workspace_id=$1`,
		`DELETE FROM core.relation_evidence WHERE workspace_id=$1`,
		`DELETE FROM core.relation WHERE workspace_id=$1`,
		`DELETE FROM core.claim_source WHERE workspace_id=$1`,
		`DELETE FROM core.claim WHERE workspace_id=$1`,
		`DELETE FROM core.topic_alias WHERE workspace_id=$1`,
		`DELETE FROM core.topic WHERE workspace_id=$1`,
		`DELETE FROM ingestion.source_version_projection WHERE workspace_id=$1`,
		`DELETE FROM ingestion.source_span WHERE workspace_id=$1`,
		`DELETE FROM ingestion.parse_projection WHERE workspace_id=$1`,
		`DELETE FROM core.source_version WHERE source_id IN (SELECT id FROM core.source WHERE workspace_id=$1)`,
		`DELETE FROM core.source WHERE workspace_id=$1`,
		`DELETE FROM core.content_artifact WHERE workspace_id=$1`,
		`DELETE FROM core.workspace WHERE id=$1`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, string(workspaceID)); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

type provenanceBinding struct {
	sourceVersionID foundation.ID
	sourceSpanID    foundation.ID
}

const functionalWorkspaceName = "graph-http-integration"

func functionalWorkspaceRoot(workspaceID foundation.ID) string {
	return "/tmp/graph-http-integration-" + string(workspaceID)
}

func insertWorkspace(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, now time.Time) error {
	root := functionalWorkspaceRoot(workspaceID)
	_, err := tx.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(workspaceID), functionalWorkspaceName, root, now)
	return err
}

func insertProvenance(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, now time.Time) (provenanceBinding, error) {
	ids, err := newIDs(5)
	if err != nil {
		return provenanceBinding{}, err
	}
	artifactID, sourceID, sourceVersionID := ids[0], ids[1], ids[2]
	projectionID, sourceSpanID := ids[3], ids[4]
	content := []byte("graph integration provenance")
	contentHash := sha256Hex(content)

	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
				VALUES($1,$2,$3,$4,$5,$6)`,
			args: []any{string(artifactID), string(workspaceID), contentHash, len(content), ".knowledge/sources/" + contentHash, now},
		},
		{
			query: `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
				VALUES($1,$2,'text',$3,$3,$4)`,
			args: []any{string(sourceID), string(workspaceID), "graph-http-fixture.txt", now},
		},
		{
			query: `INSERT INTO core.source_version(
				id,source_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
			) VALUES($1,$2,$3,$4,$5,'text/plain',$6,'pending',$7)`,
			args: []any{string(sourceVersionID), string(sourceID), string(artifactID), contentHash, len(content), "graph-http-fixture.txt", now},
		},
		{
			query: `INSERT INTO ingestion.parse_projection(
				id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at
			) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`,
			args: []any{string(projectionID), string(workspaceID), string(artifactID), sha256Hex([]byte("parser")), sha256Hex([]byte("normalized")), now},
		},
		{
			query: `INSERT INTO ingestion.source_span(
				id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at
			) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{}',$6,'v1','v1',$7)`,
			args: []any{string(sourceSpanID), string(workspaceID), string(artifactID), string(projectionID), len(content), contentHash, now},
		},
		{
			query: `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
				VALUES($1,$2,$3,$4)`,
			args: []any{string(sourceVersionID), string(projectionID), string(workspaceID), now},
		},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			return provenanceBinding{}, err
		}
	}
	return provenanceBinding{sourceVersionID: sourceVersionID, sourceSpanID: sourceSpanID}, nil
}

func insertTopic(ctx context.Context, tx pgx.Tx, workspaceID, topicID foundation.ID, name, description string, now time.Time) error {
	display, normalized, err := knowledge.NormalizeTopicText(name)
	if err != nil {
		return err
	}
	normalizedDescription, err := knowledge.NormalizeDescription(description)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO core.topic(
		id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'ACTIVE',1,$6,$6)`, string(topicID), string(workspaceID), display, normalized, normalizedDescription, now)
	return err
}

func newClaim(workspaceID, claimID foundation.ID, statement string, confidence float64, now time.Time) (knowledge.Claim, error) {
	display, normalized, err := knowledge.NormalizeStatement(statement)
	if err != nil {
		return knowledge.Claim{}, err
	}
	applicability, err := knowledge.ParseApplicability(json.RawMessage(`{"scope":"graph-http-integration"}`))
	if err != nil {
		return knowledge.Claim{}, err
	}
	confidenceFactors, err := knowledge.NormalizeConfidenceFactors(json.RawMessage(`{"source":"fixture"}`))
	if err != nil {
		return knowledge.Claim{}, err
	}
	claim := knowledge.Claim{
		ID:                  claimID,
		WorkspaceID:         workspaceID,
		Statement:           display,
		NormalizedStatement: normalized,
		Applicability:       applicability,
		Fingerprint:         knowledge.ComputeClaimFingerprint(workspaceID, normalized, applicability),
		Status:              knowledge.ClaimStatusSuggested,
		ConfidenceScore:     float64Ptr(confidence),
		ConfidenceFactors:   confidenceFactors,
		Version:             1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	return claim, knowledge.ValidateClaim(claim)
}

func insertConfirmedClaim(ctx context.Context, tx pgx.Tx, claim knowledge.Claim, provenance provenanceBinding, reason string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim(
		id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,
		status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		string(claim.ID), string(claim.WorkspaceID), claim.Statement, claim.NormalizedStatement,
		string(claim.Applicability.CanonicalJSON), claim.Applicability.SchemaVersion, claim.Applicability.Hash,
		string(claim.Status), claim.ConfidenceScore, string(claim.ConfidenceFactors), claim.Fingerprint,
		claim.Version, claim.CreatedAt, claim.UpdatedAt,
	); err != nil {
		return err
	}
	source, err := newClaimSource(claim, provenance, reason, claim.CreatedAt.Add(time.Microsecond))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(
		id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		string(source.ID), string(source.WorkspaceID), string(source.ClaimID), string(source.Provenance.SourceVersionID), string(source.Provenance.SourceSpanID),
		string(source.SupportType), source.Reason, source.EvidenceHash, source.CreatedAt,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=version+1,updated_at=$3 WHERE workspace_id=$1 AND id=$2`,
		string(claim.WorkspaceID), string(claim.ID), claim.CreatedAt.Add(2*time.Microsecond),
	); err != nil {
		return err
	}
	return nil
}

func newClaimSource(claim knowledge.Claim, provenance provenanceBinding, reason string, now time.Time) (knowledge.ClaimSource, error) {
	normalizedReason, err := knowledge.NormalizeReason(reason, true)
	if err != nil {
		return knowledge.ClaimSource{}, err
	}
	id, err := newID()
	if err != nil {
		return knowledge.ClaimSource{}, err
	}
	source := knowledge.ClaimSource{
		ID:          id,
		WorkspaceID: claim.WorkspaceID,
		ClaimID:     claim.ID,
		Provenance: knowledge.ProvenanceRef{
			WorkspaceID:     claim.WorkspaceID,
			SourceVersionID: provenance.sourceVersionID,
			SourceSpanID:    provenance.sourceSpanID,
		},
		SupportType: knowledge.ClaimSupportSupports,
		Reason:      normalizedReason,
		CreatedAt:   now,
	}
	source.EvidenceHash = knowledge.ComputeClaimSourceEvidenceHash(source, claim.Applicability)
	return source, knowledge.ValidateClaimSource(source, claim.Applicability)
}

func newRelation(workspaceID, relationID foundation.ID, source, target knowledge.NodeRef, relationType knowledge.RelationType, confidence float64, now time.Time) (knowledge.Relation, error) {
	canonicalSource, canonicalTarget, err := knowledge.CanonicalizeRelationEndpoints(relationType, source, target)
	if err != nil {
		return knowledge.Relation{}, err
	}
	relation := knowledge.Relation{
		ID:              relationID,
		WorkspaceID:     workspaceID,
		Source:          canonicalSource,
		Target:          canonicalTarget,
		Type:            relationType,
		Status:          knowledge.RelationStatusConfirmed,
		Confirmation:    &knowledge.Confirmation{Method: knowledge.ConfirmationSourceDerived, Reference: "graph integration fixture"},
		ConfidenceScore: float64Ptr(confidence),
		Fingerprint:     knowledge.ComputeRelationFingerprint(workspaceID, relationType, canonicalSource, canonicalTarget),
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	return relation, knowledge.ValidateRelation(relation)
}

func insertConfirmedRelation(ctx context.Context, tx pgx.Tx, relation knowledge.Relation, provenance provenanceBinding, reason string) error {
	evidence, err := newRelationEvidence(relation, provenance, reason, relation.CreatedAt.Add(time.Microsecond))
	if err != nil {
		return err
	}
	relation.EvidenceFingerprint = knowledge.ComputeRelationEvidenceFingerprint([]knowledge.RelationEvidence{evidence})
	if err := knowledge.ValidateRelationAggregate(relation, []knowledge.RelationEvidence{evidence}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(
		id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,
		confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		string(relation.ID), string(relation.WorkspaceID), string(relation.Source.Type), string(relation.Source.ID),
		string(relation.Target.Type), string(relation.Target.ID), string(relation.Type), string(knowledge.RelationStatusSuggested),
		relation.ConfidenceScore, relation.Fingerprint, nil,
		nil, nil, 1, relation.CreatedAt, relation.CreatedAt,
	); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO core.relation_evidence(
		id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,
		applicability_hash,confirmation_method,confirmed_by,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		string(evidence.ID), string(evidence.WorkspaceID), string(evidence.RelationID),
		string(evidence.Provenance.SourceVersionID), string(evidence.Provenance.SourceSpanID), evidence.Reason, evidence.EvidenceHash,
		string(evidence.Applicability.CanonicalJSON), evidence.Applicability.SchemaVersion, evidence.Applicability.Hash,
		string(evidence.Confirmation.Method), "graph integration fixture", evidence.CreatedAt,
	)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE core.relation SET status='CONFIRMED',evidence_fingerprint=$3,confirmation_method=$4,confirmation_ref=$5,version=2,updated_at=$6 WHERE workspace_id=$1 AND id=$2`, string(relation.WorkspaceID), string(relation.ID), relation.EvidenceFingerprint, string(relation.Confirmation.Method), relation.Confirmation.Reference, relation.UpdatedAt)
	return err
}

func newRelationEvidence(relation knowledge.Relation, provenance provenanceBinding, reason string, now time.Time) (knowledge.RelationEvidence, error) {
	normalizedReason, err := knowledge.NormalizeReason(reason, true)
	if err != nil {
		return knowledge.RelationEvidence{}, err
	}
	applicability, err := knowledge.ParseApplicability(json.RawMessage(`{"scope":"graph-http-integration","mode":"read-model"}`))
	if err != nil {
		return knowledge.RelationEvidence{}, err
	}
	id, err := newID()
	if err != nil {
		return knowledge.RelationEvidence{}, err
	}
	evidence := knowledge.RelationEvidence{
		ID:          id,
		WorkspaceID: relation.WorkspaceID,
		RelationID:  relation.ID,
		Provenance: knowledge.ProvenanceRef{
			WorkspaceID:     relation.WorkspaceID,
			SourceVersionID: provenance.sourceVersionID,
			SourceSpanID:    provenance.sourceSpanID,
		},
		Reason:        normalizedReason,
		Applicability: applicability,
		Confirmation:  relation.Confirmation,
		CreatedAt:     now,
	}
	evidence.EvidenceHash = knowledge.ComputeRelationEvidenceHash(evidence)
	return evidence, knowledge.ValidateRelationEvidence(evidence)
}

func newID() (foundation.ID, error) {
	return foundation.NewUUIDGenerator(nil).New()
}

func newIDs(count int) ([]foundation.ID, error) {
	ids := make([]foundation.ID, count)
	for index := range ids {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		ids[index] = id
	}
	return ids, nil
}

func float64Ptr(value float64) *float64 {
	return &value
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
