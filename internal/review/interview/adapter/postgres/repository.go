package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/jackc/pgx/v5"
)

// DB 是 Repository 使用的最小 pgx 数据库能力。
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 实现 Interview 专属 Store 与 QuestionSource，不触碰 FSRS 表。
type Repository struct{ db DB }

// NewRepository 构造 PostgreSQL Interview Repository。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "interview database is required")
	}
	return &Repository{db: db}, nil
}

// Select 只读取无高严重度冲突、来源未隔离的 CONFIRMED Claim 及其 SUPPORTS Evidence，并稳定聚合为题目材料。
func (r *Repository) Select(ctx context.Context, selection interviewapp.Selection) ([]domain.Material, error) {
	if err := validateSelection(selection); err != nil {
		return nil, err
	}
	claimIDs := stringsFromIDs(selection.Scope.ClaimIDs)
	topicIDs := stringsFromIDs(selection.Scope.TopicIDs)
	rows, err := r.db.Query(ctx, `
		WITH eligible_evidence AS NOT MATERIALIZED (
		    SELECT c.id AS claim_id,c.statement,frozen.index_version_id,frozen.chunk_id,
		           cs.source_version_id,cs.source_span_id,cs.evidence_hash,topic.id AS topic_id
		      FROM core.claim AS c
		      JOIN core.claim_source AS cs
		        ON cs.workspace_id=c.workspace_id
		       AND cs.claim_id=c.id
		       AND cs.support_type='SUPPORTS'
		      JOIN core.source_version AS source_version
		        ON source_version.workspace_id=c.workspace_id
		       AND source_version.id=cs.source_version_id
		       AND source_version.security_status <> 'quarantined'
		      JOIN LATERAL (
		          SELECT active_index.id AS index_version_id,chunk.id AS chunk_id
		            FROM retrieval.index_version AS active_index
		            JOIN retrieval.index_manifest_source AS source_manifest
		              ON source_manifest.index_version_id=active_index.id
		             AND source_manifest.workspace_id=active_index.workspace_id
		             AND source_manifest.source_version_id=cs.source_version_id
		             AND source_manifest.selection_status='included'
		            JOIN ingestion.canonical_chunk AS chunk
		              ON chunk.workspace_id=active_index.workspace_id
		             AND chunk.parse_projection_id=source_manifest.parse_projection_id
		             AND chunk.source_span_id=cs.source_span_id
		            JOIN retrieval.index_manifest_chunk AS chunk_manifest
		              ON chunk_manifest.index_version_id=active_index.id
		             AND chunk_manifest.workspace_id=active_index.workspace_id
		             AND chunk_manifest.chunk_id=chunk.id
		             AND chunk_manifest.content_hash=chunk.content_hash
		           WHERE active_index.workspace_id=c.workspace_id
		             AND active_index.status='active'
		           ORDER BY chunk_manifest.sequence,chunk_manifest.chunk_id
		           LIMIT 1
		      ) AS frozen ON true
		      LEFT JOIN LATERAL (
		          SELECT t.id
		            FROM core.relation AS relation
		            JOIN core.topic AS t
		              ON t.id=relation.target_node_id
		             AND t.workspace_id=relation.workspace_id
		           WHERE relation.workspace_id=c.workspace_id
		             AND relation.source_node_type='CLAIM'
		             AND relation.source_node_id=c.id
		             AND relation.target_node_type='TOPIC'
		             AND relation.relation_type='BELONGS_TO'
		             AND relation.status='CONFIRMED'
		           ORDER BY t.id
		           LIMIT 1
		      ) AS topic ON true
		     WHERE c.workspace_id=$1
		       AND c.status='CONFIRMED'
		       AND octet_length(c.statement) <= 8192
		       AND NOT EXISTS (
		           SELECT 1
		             FROM core.conflict_member AS conflict_member
		             JOIN core.conflict AS conflict
		               ON conflict.workspace_id=conflict_member.workspace_id
		              AND conflict.id=conflict_member.conflict_id
		            WHERE conflict_member.workspace_id=c.workspace_id
		              AND conflict_member.claim_id=c.id
		              AND conflict.severity IN ('CRITICAL','HIGH')
		              AND conflict.status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE')
		       )
		       AND COALESCE((
		           SELECT attempt.security_status
		             FROM ingestion.attempt AS attempt
		            WHERE attempt.workspace_id=c.workspace_id
		              AND attempt.source_version_id=cs.source_version_id
		            ORDER BY attempt.started_at DESC,attempt.id DESC
		            LIMIT 1
		       ), 'passed') <> 'quarantined'
		       AND (cardinality($2::uuid[]) = 0 OR c.id = ANY($2::uuid[]))
		       AND (cardinality($3::uuid[]) = 0 OR EXISTS (
		           SELECT 1
		             FROM core.relation AS scoped_relation
		            WHERE scoped_relation.workspace_id=c.workspace_id
		              AND scoped_relation.source_node_type='CLAIM'
		              AND scoped_relation.source_node_id=c.id
		              AND scoped_relation.target_node_type='TOPIC'
		              AND scoped_relation.relation_type='BELONGS_TO'
		              AND scoped_relation.status='CONFIRMED'
		              AND scoped_relation.target_node_id = ANY($3::uuid[])
		       ))
		), selected_claims AS (
		    SELECT DISTINCT ON (claim_id) claim_id
		      FROM eligible_evidence
		     ORDER BY claim_id,evidence_hash,source_version_id,source_span_id
		     LIMIT $4
		)
		SELECT evidence.claim_id::text,evidence.statement,evidence.index_version_id::text,evidence.chunk_id::text,
		       evidence.source_version_id::text,evidence.source_span_id::text,evidence.evidence_hash,evidence.topic_id::text
		  FROM selected_claims AS selected
		  JOIN LATERAL (
		      SELECT eligible.*
		        FROM eligible_evidence AS eligible
		       WHERE eligible.claim_id=selected.claim_id
		       ORDER BY eligible.evidence_hash,eligible.source_version_id,eligible.source_span_id
		       LIMIT $5
		  ) AS evidence ON true
		 ORDER BY evidence.claim_id,evidence.evidence_hash,evidence.source_version_id,evidence.source_span_id`,
		string(selection.WorkspaceID), claimIDs, topicIDs, selection.Limit, domain.MaxEvidenceItems)
	if err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	type grouped struct {
		material domain.Material
	}
	byClaim := make(map[foundation.ID]*grouped)
	order := make([]foundation.ID, 0, selection.Limit)
	for rows.Next() {
		var claimID, indexVersionID, chunkID, sourceVersionID, sourceSpanID, evidenceHash string
		var statement string
		var topicID *string
		if err := rows.Scan(&claimID, &statement, &indexVersionID, &chunkID, &sourceVersionID, &sourceSpanID, &evidenceHash, &topicID); err != nil {
			return nil, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		claim := foundation.ID(claimID)
		entry, found := byClaim[claim]
		if !found {
			material := domain.Material{ClaimID: claim, ClaimStatus: "CONFIRMED", Statement: statement, AnswerPoints: []string{statement}}
			if topicID != nil {
				value := foundation.ID(*topicID)
				material.TopicID = &value
			}
			entry = &grouped{material: material}
			byClaim[claim] = entry
			order = append(order, claim)
		}
		entry.material.Evidence = append(entry.material.Evidence, domain.EvidenceRef{
			SchemaVersion: domain.EvidenceSchemaVersion, ClaimID: claim, IndexVersionID: foundation.ID(indexVersionID), ChunkID: foundation.ID(chunkID), SourceVersionID: foundation.ID(sourceVersionID),
			SourceSpanID: foundation.ID(sourceSpanID), EvidenceHash: evidenceHash, SupportType: "SUPPORTS",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	materials := make([]domain.Material, 0, min(selection.Limit, len(order)))
	for _, claimID := range order {
		material := byClaim[claimID].material
		if err := domain.ValidateMaterial(material); err != nil {
			return nil, persistenceInvalid("selected claim material is invalid", err)
		}
		materials = append(materials, material)
		if len(materials) == selection.Limit {
			break
		}
	}
	return materials, nil
}

// List 返回一个 Workspace 内按 started_at、session_id 稳定倒序的有界页面。
func (r *Repository) List(ctx context.Context, query interviewapp.SessionListQuery) (interviewapp.SessionListPage, error) {
	if err := validateSessionListQuery(query); err != nil {
		return interviewapp.SessionListPage{}, err
	}
	var cursorTime, cursorID any
	if query.After != nil {
		cursorTime = query.After.StartedAt.UTC().Truncate(time.Microsecond)
		cursorID = string(query.After.ID)
	}
	rows, err := r.db.Query(ctx, `
		SELECT session.session_id::text,session.workspace_id::text,shell.config,shell.status,
		       session.version,session.follow_up_count,shell.started_at,shell.ended_at
		  FROM learning.review_session AS shell
		  JOIN learning.interview_session AS session
		    ON session.session_id=shell.id
		   AND session.workspace_id=shell.workspace_id
		 WHERE shell.workspace_id=$1
		   AND shell.session_type='INTERVIEW'
		   AND ($2::timestamptz IS NULL OR (shell.started_at,session.session_id)<($2::timestamptz,$3::uuid))
		 ORDER BY shell.started_at DESC,session.session_id DESC
		 LIMIT $4`, string(query.WorkspaceID), cursorTime, cursorID, query.Limit+1)
	if err != nil {
		return interviewapp.SessionListPage{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	items := make([]domain.Session, 0, query.Limit+1)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return interviewapp.SessionListPage{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		items = append(items, session)
	}
	if err := rows.Err(); err != nil {
		return interviewapp.SessionListPage{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	page := interviewapp.SessionListPage{Items: items}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &interviewapp.SessionCursor{StartedAt: last.StartedAt, ID: last.ID}
	}
	return page, nil
}

// AbandonStaleCompletions 用数据库时钟有界放弃超过 24 小时的 PENDING reservation，并隐藏其 ACTIVE hold。
func (r *Repository) AbandonStaleCompletions(ctx context.Context, batchSize int) (interviewapp.CompletionMaintenanceResult, error) {
	if r == nil || r.db == nil {
		return interviewapp.CompletionMaintenanceResult{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "interview completion maintenance database is unavailable")
	}
	if batchSize < 1 || batchSize > interviewapp.MaxCompletionMaintenanceBatch {
		return interviewapp.CompletionMaintenanceResult{}, domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview completion maintenance batch is invalid")
	}
	var result interviewapp.CompletionMaintenanceResult
	err := r.db.QueryRow(ctx, `
			-- This path intentionally locks reservation -> hold without Session.
			-- The hold guard returns before Session locking for ORPHANED updates,
			-- while every ACTIVE hold writer locks Session -> reservation first.
			WITH maintenance_clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		), candidates AS MATERIALIZED (
			SELECT reservation.workspace_id,reservation.session_id
			  FROM learning.interview_completion_reservation AS reservation
			 CROSS JOIN maintenance_clock
			 WHERE reservation.status='PENDING'
			   AND reservation.updated_at <= maintenance_clock.now - interval '24 hours'
			 ORDER BY reservation.updated_at,reservation.workspace_id,reservation.session_id
			 LIMIT $1
			 FOR UPDATE OF reservation SKIP LOCKED
		), abandoned AS (
			UPDATE learning.interview_completion_reservation AS reservation
			   SET status='ABANDONED',abandoned_at=maintenance_clock.now,updated_at=maintenance_clock.now
			  FROM candidates,maintenance_clock
			 WHERE reservation.workspace_id=candidates.workspace_id
			   AND reservation.session_id=candidates.session_id
			   AND reservation.status='PENDING'
			 RETURNING reservation.workspace_id,reservation.session_id,reservation.artifact_digest
		), orphaned AS (
			UPDATE learning.artifact_visibility_hold AS hold
			   SET attempt_digest=COALESCE(hold.attempt_digest,abandoned.artifact_digest),
			       disposition='ORPHANED'
			  FROM abandoned
			 WHERE hold.workspace_id=abandoned.workspace_id
			   AND hold.owner_type='INTERVIEW_COMPLETE'
			   AND hold.owner_id=abandoned.session_id
			   AND hold.disposition='ACTIVE'
			 RETURNING hold.artifact_id
		)
		SELECT (SELECT count(*) FROM abandoned),(SELECT count(*) FROM orphaned)`, batchSize).Scan(
		&result.AbandonedReservations, &result.OrphanedHolds,
	)
	if err != nil {
		return interviewapp.CompletionMaintenanceResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return result, nil
}

func validateSelection(selection interviewapp.Selection) error {
	if !validID(selection.WorkspaceID) || selection.Limit < 1 || selection.Limit > 20 || (len(selection.Scope.ClaimIDs) == 0 && len(selection.Scope.TopicIDs) == 0) {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview selection is invalid")
	}
	seen := make(map[foundation.ID]struct{}, len(selection.Scope.ClaimIDs)+len(selection.Scope.TopicIDs))
	for _, values := range [][]foundation.ID{selection.Scope.ClaimIDs, selection.Scope.TopicIDs} {
		for _, value := range values {
			if !validID(value) {
				return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview selection contains invalid identifiers")
			}
			if _, found := seen[value]; found {
				return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview selection contains duplicate identifiers")
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func validateSessionListQuery(query interviewapp.SessionListQuery) error {
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > interviewapp.MaxSessionListLimit {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview session list query is invalid")
	}
	if query.After != nil && (query.After.StartedAt.IsZero() || !validID(query.After.ID)) {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview session list cursor is invalid")
	}
	return nil
}

func stringsFromIDs(values []foundation.ID) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = string(values[index])
	}
	return result
}

func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func advisoryKey(workspaceID foundation.ID, key string) string {
	return string(workspaceID) + ":" + key
}

func lockCommand(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, advisoryKey(workspaceID, key))
	return err
}

func withTransaction[T any](ctx context.Context, db DB, operation func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.Begin(ctx)
	if err != nil {
		return zero, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := operation(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return result, nil
}

func require(condition bool, code, message string) error {
	if !condition {
		return domain.InvalidError(code, message)
	}
	return nil
}

func wrapError(message string, err error) error {
	if err == nil {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, err)
}
