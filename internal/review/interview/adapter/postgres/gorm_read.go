package postgres

import (
	"context"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// Select reads confirmed, active-index-backed claim evidence as frozen question material.
func (repository *GORMRepository) Select(ctx context.Context, selection interviewapp.Selection) ([]domain.Material, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := validateSelection(selection); err != nil {
		return nil, err
	}

	rows, err := gormInterviewRawRows(ctx, repository.database, `
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
		     WHERE c.workspace_id=?::uuid
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
		       AND (cardinality(?::uuid[]) = 0 OR c.id = ANY(?::uuid[]))
		       AND (cardinality(?::uuid[]) = 0 OR EXISTS (
		           SELECT 1
		             FROM core.relation AS scoped_relation
		            WHERE scoped_relation.workspace_id=c.workspace_id
		              AND scoped_relation.source_node_type='CLAIM'
		              AND scoped_relation.source_node_id=c.id
		              AND scoped_relation.target_node_type='TOPIC'
		              AND scoped_relation.relation_type='BELONGS_TO'
		              AND scoped_relation.status='CONFIRMED'
		              AND scoped_relation.target_node_id = ANY(?::uuid[])
		       ))
		), selected_claims AS (
		    SELECT DISTINCT ON (claim_id) claim_id
		      FROM eligible_evidence
		     ORDER BY claim_id,evidence_hash,source_version_id,source_span_id
		     LIMIT ?
		)
		SELECT evidence.claim_id::text,evidence.statement,evidence.index_version_id::text,evidence.chunk_id::text,
		       evidence.source_version_id::text,evidence.source_span_id::text,evidence.evidence_hash,evidence.topic_id::text
		  FROM selected_claims AS selected
		  JOIN LATERAL (
		      SELECT eligible.*
		        FROM eligible_evidence AS eligible
		       WHERE eligible.claim_id=selected.claim_id
		       ORDER BY eligible.evidence_hash,eligible.source_version_id,eligible.source_span_id
		       LIMIT ?
		  ) AS evidence ON true
		 ORDER BY evidence.claim_id,evidence.evidence_hash,evidence.source_version_id,evidence.source_span_id`,
		string(selection.WorkspaceID), pq.Array(stringsFromIDs(selection.Scope.ClaimIDs)), pq.Array(stringsFromIDs(selection.Scope.ClaimIDs)),
		pq.Array(stringsFromIDs(selection.Scope.TopicIDs)), pq.Array(stringsFromIDs(selection.Scope.TopicIDs)),
		selection.Limit, domain.MaxEvidenceItems)
	if err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()

	type grouped struct{ material domain.Material }
	byClaim := make(map[foundation.ID]*grouped)
	order := make([]foundation.ID, 0, selection.Limit)
	for rows.Next() {
		var claimID, indexVersionID, chunkID, sourceVersionID, sourceSpanID, evidenceHash string
		var statement string
		var topicID *string
		if err := rows.Scan(&claimID, &statement, &indexVersionID, &chunkID, &sourceVersionID, &sourceSpanID, &evidenceHash, &topicID); err != nil {
			return nil, gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
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
			SchemaVersion: domain.EvidenceSchemaVersion, ClaimID: claim, IndexVersionID: foundation.ID(indexVersionID), ChunkID: foundation.ID(chunkID),
			SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID), EvidenceHash: evidenceHash, SupportType: "SUPPORTS",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}

	sort.Slice(order, func(left, right int) bool { return order[left] < order[right] })
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

// Get reads all persisted Interview facts required to restore a Session.
func (repository *GORMRepository) Get(ctx context.Context, workspaceID, sessionID foundation.ID) (interviewapp.Snapshot, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.Snapshot{}, err
	}
	return gormInterviewLoadSnapshot(ctx, repository.database, workspaceID, sessionID, false)
}

// List returns a Workspace-scoped page ordered by the stable Session keyset.
func (repository *GORMRepository) List(ctx context.Context, query interviewapp.SessionListQuery) (interviewapp.SessionListPage, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.SessionListPage{}, err
	}
	if err := validateSessionListQuery(query); err != nil {
		return interviewapp.SessionListPage{}, err
	}
	var cursorTime, cursorID any
	if query.After != nil {
		cursorTime = query.After.StartedAt.UTC().Truncate(time.Microsecond)
		cursorID = string(query.After.ID)
	}
	rows, err := gormInterviewRawRows(ctx, repository.database, `
		SELECT session.session_id::text,session.workspace_id::text,shell.config,shell.status,
		       session.version,session.follow_up_count,shell.started_at,shell.ended_at
		  FROM learning.review_session AS shell
		  JOIN learning.interview_session AS session
		    ON session.session_id=shell.id
		   AND session.workspace_id=shell.workspace_id
		 WHERE shell.workspace_id=?::uuid
		   AND shell.session_type='INTERVIEW'
		   AND (NOT ?::boolean OR jsonb_extract_path(shell.config,'scope','note_revision') IS NULL)
		   AND (?::timestamptz IS NULL OR (shell.started_at,session.session_id)<(?::timestamptz,?::uuid))
		 ORDER BY shell.started_at DESC,session.session_id DESC
		 LIMIT ?`, string(query.WorkspaceID), query.ClaimOnly, cursorTime, cursorTime, cursorID, query.Limit+1)
	if err != nil {
		return interviewapp.SessionListPage{}, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	items := make([]domain.Session, 0, query.Limit+1)
	for rows.Next() {
		session, err := gormInterviewScanSession(rows)
		if err != nil {
			return interviewapp.SessionListPage{}, gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
		}
		items = append(items, session)
	}
	if err := rows.Err(); err != nil {
		return interviewapp.SessionListPage{}, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return interviewapp.SessionListPage{}, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	page := interviewapp.SessionListPage{Items: items}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &interviewapp.SessionCursor{StartedAt: last.StartedAt, ID: last.ID}
	}
	return page, nil
}

// GetPath reads an Interview-owned compatibility-view Path and its ordered Steps.
func (repository *GORMRepository) GetPath(ctx context.Context, workspaceID, pathID foundation.ID) (interviewapp.PathSnapshot, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.PathSnapshot{}, err
	}
	path, steps, err := gormInterviewLoadPath(ctx, repository.database, workspaceID, pathID, false)
	if err != nil {
		return interviewapp.PathSnapshot{}, err
	}
	return interviewapp.PathSnapshot{Path: path, Steps: steps}, nil
}

func gormInterviewLoadSnapshot(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID, lock bool) (interviewapp.Snapshot, error) {
	session, err := gormInterviewLoadSession(ctx, database, workspaceID, sessionID, lock)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	questions, err := gormInterviewLoadQuestions(ctx, database, workspaceID, sessionID)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	turns, err := gormInterviewLoadTurns(ctx, database, workspaceID, sessionID)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	questionsByID := make(map[foundation.ID]domain.Question, len(questions))
	for _, question := range questions {
		questionsByID[question.ID] = question
	}
	for _, turn := range turns {
		question, found := questionsByID[turn.QuestionID]
		if !found {
			return interviewapp.Snapshot{}, persistenceInvalid("interview turn references a missing question", nil)
		}
		if err := domain.ValidateTurn(turn, question); err != nil {
			return interviewapp.Snapshot{}, persistenceInvalid("validate persisted interview turn", err)
		}
	}
	report, err := gormInterviewLoadReport(ctx, database, workspaceID, sessionID)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	var path *domain.LearningPath
	var steps []domain.PathStep
	if report != nil {
		path, steps, err = gormInterviewLoadPathBySession(ctx, database, workspaceID, sessionID)
		if err != nil {
			return interviewapp.Snapshot{}, err
		}
		if path == nil {
			return interviewapp.Snapshot{}, persistenceInvalid("completed interview report is missing its learning path", nil)
		}
	}
	return interviewapp.Snapshot{Session: session, Questions: questions, Turns: turns, Report: report, Path: path, Steps: steps}, nil
}

func gormInterviewLoadSession(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID, lock bool) (domain.Session, error) {
	query := `
		SELECT s.session_id::text,s.workspace_id::text,rs.config,rs.status,s.version,s.follow_up_count,rs.started_at,rs.ended_at
		  FROM learning.interview_session AS s
		  JOIN learning.review_session AS rs
		    ON rs.id=s.session_id AND rs.workspace_id=s.workspace_id
		 WHERE s.workspace_id=?::uuid AND s.session_id=?::uuid`
	if lock {
		query += ` FOR UPDATE OF s,rs`
	}
	row, err := gormInterviewRawRow(ctx, database, query, string(workspaceID), string(sessionID))
	if err != nil {
		return domain.Session{}, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return gormInterviewReadSession(ctx, row)
}

func gormInterviewReadSession(ctx context.Context, row interface{ Scan(...any) error }) (domain.Session, error) {
	session, err := gormInterviewScanSession(row)
	if gormInterviewNoRows(err) {
		return domain.Session{}, domain.NotFoundError(domain.ErrorCodeSessionNotFound, "interview session was not found")
	}
	if err != nil {
		return domain.Session{}, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return session, nil
}

func gormInterviewScanSession(row interface{ Scan(...any) error }) (domain.Session, error) {
	var session domain.Session
	var config interviewJSONB
	var status string
	if err := row.Scan(&session.ID, &session.WorkspaceID, &config, &status, &session.Version, &session.FollowUpCount, &session.StartedAt, &session.EndedAt); err != nil {
		return domain.Session{}, err
	}
	if err := decodeJSON(config, &session.Config); err != nil {
		return domain.Session{}, persistenceInvalid("decode interview session config", err)
	}
	session.Status = domain.SessionStatus(status)
	session.StartedAt = session.StartedAt.UTC()
	if session.EndedAt != nil {
		value := session.EndedAt.UTC()
		session.EndedAt = &value
	}
	if err := domain.ValidateSession(session); err != nil {
		return domain.Session{}, persistenceInvalid("validate persisted interview session", err)
	}
	return session, nil
}

func gormInterviewLoadQuestions(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID) ([]domain.Question, error) {
	rows, err := gormInterviewRawRows(ctx, database, `
		SELECT id::text,workspace_id::text,session_id::text,question_no,follow_up_no,parent_question_id::text,
		       COALESCE(claim_id::text,''),topic_id::text,prompt,answer_points,evidence,status,fingerprint,created_at,answered_at,source_kind,note_source,follow_up_plan
		  FROM learning.interview_question
		 WHERE workspace_id=?::uuid AND session_id=?::uuid
		 ORDER BY question_no,follow_up_no,id`, string(workspaceID), string(sessionID))
	if err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	questions := make([]domain.Question, 0)
	for rows.Next() {
		var question domain.Question
		var parentID, topicID *string
		var points, evidence interviewJSONB
		var status string
		var sourceKind string
		var noteSource, followUpPlan []byte
		if err := rows.Scan(&question.ID, &question.WorkspaceID, &question.SessionID, &question.QuestionNo, &question.FollowUpNo, &parentID,
			&question.ClaimID, &topicID, &question.Prompt, &points, &evidence, &status, &question.Fingerprint, &question.CreatedAt, &question.AnsweredAt, &sourceKind, &noteSource, &followUpPlan); err != nil {
			return nil, gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
		}
		if sourceKind != string(domain.QuestionSourceClaim) {
			question.SourceKind = domain.QuestionSourceKind(sourceKind)
		}
		if len(noteSource) > 0 {
			if err := decodeJSON(noteSource, &question.NoteSource); err != nil {
				return nil, persistenceInvalid("decode interview note source", err)
			}
		}
		if len(followUpPlan) > 0 {
			if err := decodeJSON(followUpPlan, &question.FollowUpPlan); err != nil {
				return nil, persistenceInvalid("decode interview follow-up plan", err)
			}
		}
		if parentID != nil {
			value := foundation.ID(*parentID)
			question.ParentQuestionID = &value
		}
		if topicID != nil {
			value := foundation.ID(*topicID)
			question.TopicID = &value
		}
		if err := decodeJSON(points, &question.AnswerPoints); err != nil {
			return nil, persistenceInvalid("decode interview question points", err)
		}
		if err := decodeJSON(evidence, &question.Evidence); err != nil {
			return nil, persistenceInvalid("decode interview question evidence", err)
		}
		question.Status = domain.QuestionStatus(status)
		question.CreatedAt = question.CreatedAt.UTC()
		if question.AnsweredAt != nil {
			value := question.AnsweredAt.UTC()
			question.AnsweredAt = &value
		}
		if err := domain.ValidateQuestion(question); err != nil {
			return nil, persistenceInvalid("validate persisted interview question", err)
		}
		questions = append(questions, question)
	}
	if err := rows.Err(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return questions, nil
}

func gormInterviewLoadTurns(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID) ([]domain.Turn, error) {
	rows, err := gormInterviewRawRows(ctx, database, `
		SELECT id::text,workspace_id::text,session_id::text,question_id::text,idempotency_key,request_hash,user_answer,
		       score,decision,scorer_version,created_at
		  FROM learning.interview_turn
		 WHERE workspace_id=?::uuid AND session_id=?::uuid
		 ORDER BY created_at,id`, string(workspaceID), string(sessionID))
	if err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	turns := make([]domain.Turn, 0)
	for rows.Next() {
		var turn domain.Turn
		var score, decision interviewJSONB
		if err := rows.Scan(&turn.ID, &turn.WorkspaceID, &turn.SessionID, &turn.QuestionID, &turn.IdempotencyKey, &turn.RequestHash, &turn.UserAnswer,
			&score, &decision, &turn.ScorerVersion, &turn.CreatedAt); err != nil {
			return nil, gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
		}
		if err := decodeJSON(score, &turn.Score); err != nil {
			return nil, persistenceInvalid("decode interview turn score", err)
		}
		if err := decodeJSON(decision, &turn.Decision); err != nil {
			return nil, persistenceInvalid("decode interview turn decision", err)
		}
		turn.CreatedAt = turn.CreatedAt.UTC()
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return turns, nil
}

func gormInterviewLoadReport(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID) (*domain.Report, error) {
	row, err := gormInterviewRawRow(ctx, database, `
		SELECT id::text,report,artifact_id::text,artifact_revision_id::text,artifact_version,report_hash
		  FROM learning.interview_report
		 WHERE workspace_id=?::uuid AND session_id=?::uuid`, string(workspaceID), string(sessionID))
	if err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var report domain.Report
	var raw interviewJSONB
	var reportID, artifactID, revisionID, reportHash string
	var artifactVersion int64
	if err := row.Scan(&reportID, &raw, &artifactID, &revisionID, &artifactVersion, &reportHash); gormInterviewNoRows(err) {
		return nil, nil
	} else if err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := decodeJSON(raw, &report); err != nil {
		return nil, persistenceInvalid("decode interview report", err)
	}
	if report.ID != foundation.ID(reportID) {
		return nil, persistenceInvalid("interview report identity is missing", nil)
	}
	canonicalRaw, err := canonicalJSON(raw)
	if err != nil {
		return nil, persistenceInvalid("canonicalize interview report", err)
	}
	if report.Artifact.ArtifactID != foundation.ID(artifactID) || report.Artifact.RevisionID != foundation.ID(revisionID) || report.Artifact.ArtifactVersion != artifactVersion || !validHash(reportHash) || reportHash != hashBytes(canonicalRaw) {
		return nil, persistenceInvalid("interview report artifact binding drifted", nil)
	}
	if err := domain.ValidateReport(report); err != nil {
		return nil, persistenceInvalid("validate persisted interview report", err)
	}
	return &report, nil
}

func gormInterviewLoadPathBySession(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID) (*domain.LearningPath, []domain.PathStep, error) {
	row, err := gormInterviewRawRow(ctx, database, `
		SELECT id::text,workspace_id::text,session_id::text,report_id::text,artifact_id::text,artifact_revision_id::text,
		       artifact_version,status,version,created_at,updated_at
		  FROM learning.interview_learning_path
		 WHERE workspace_id=?::uuid AND session_id=?::uuid`, string(workspaceID), string(sessionID))
	if err != nil {
		return nil, nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	path, err := gormInterviewScanPath(row)
	if gormInterviewNoRows(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	steps, err := gormInterviewLoadPathSteps(ctx, database, workspaceID, path.ID)
	if err != nil {
		return nil, nil, err
	}
	return &path, steps, nil
}

func gormInterviewLoadPath(ctx context.Context, database *gorm.DB, workspaceID, pathID foundation.ID, lock bool) (domain.LearningPath, []domain.PathStep, error) {
	query := `
		SELECT id::text,workspace_id::text,session_id::text,report_id::text,artifact_id::text,artifact_revision_id::text,
		       artifact_version,status,version,created_at,updated_at
		  FROM learning.interview_learning_path
		 WHERE workspace_id=?::uuid AND id=?::uuid`
	if lock {
		query += ` FOR UPDATE`
	}
	row, err := gormInterviewRawRow(ctx, database, query, string(workspaceID), string(pathID))
	if err != nil {
		return domain.LearningPath{}, nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	path, err := gormInterviewScanPath(row)
	if gormInterviewNoRows(err) {
		return domain.LearningPath{}, nil, domain.NotFoundError(domain.ErrorCodePathInvalid, "learning path was not found")
	}
	if err != nil {
		return domain.LearningPath{}, nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	steps, err := gormInterviewLoadPathSteps(ctx, database, workspaceID, path.ID)
	if err != nil {
		return domain.LearningPath{}, nil, err
	}
	return path, steps, nil
}

func gormInterviewScanPath(row interface{ Scan(...any) error }) (domain.LearningPath, error) {
	var path domain.LearningPath
	var artifactID, revisionID, status string
	if err := row.Scan(&path.ID, &path.WorkspaceID, &path.SessionID, &path.ReportID, &artifactID, &revisionID, &path.Artifact.ArtifactVersion,
		&status, &path.Version, &path.CreatedAt, &path.UpdatedAt); err != nil {
		return domain.LearningPath{}, err
	}
	path.Artifact = domain.ArtifactBinding{Kind: "LEARNING_PATH", ArtifactID: foundation.ID(artifactID), RevisionID: foundation.ID(revisionID), ArtifactVersion: path.Artifact.ArtifactVersion}
	path.Status = domain.PathStatus(status)
	path.CreatedAt, path.UpdatedAt = path.CreatedAt.UTC(), path.UpdatedAt.UTC()
	if err := domain.ValidateLearningPath(path); err != nil {
		return domain.LearningPath{}, persistenceInvalid("validate persisted learning path", err)
	}
	return path, nil
}

func gormInterviewLoadPathSteps(ctx context.Context, database *gorm.DB, workspaceID, pathID foundation.ID) ([]domain.PathStep, error) {
	rows, err := gormInterviewRawRows(ctx, database, `
		SELECT id::text,workspace_id::text,path_id::text,step_no,COALESCE(claim_id::text,''),topic_id::text,COALESCE(source_version_id::text,''),
		       COALESCE(source_span_id::text,''),COALESCE(evidence_hash,''),title,rationale,status,version,created_at,updated_at,source_kind,note_source
		  FROM learning.interview_learning_path_step
		 WHERE workspace_id=?::uuid AND path_id=?::uuid
		 ORDER BY step_no,id`, string(workspaceID), string(pathID))
	if err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	steps := make([]domain.PathStep, 0)
	expectedStepNo := 1
	for rows.Next() {
		var step domain.PathStep
		var topicID *string
		var status string
		var sourceKind string
		var noteSource []byte
		if err := rows.Scan(&step.ID, &step.WorkspaceID, &step.PathID, &step.StepNo, &step.ClaimID, &topicID, &step.SourceVersionID, &step.SourceSpanID,
			&step.EvidenceHash, &step.Title, &step.Rationale, &status, &step.Version, &step.CreatedAt, &step.UpdatedAt, &sourceKind, &noteSource); err != nil {
			return nil, gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
		}
		if sourceKind != string(domain.QuestionSourceClaim) {
			step.SourceKind = domain.QuestionSourceKind(sourceKind)
		}
		if len(noteSource) > 0 {
			if err := decodeJSON(noteSource, &step.NoteSource); err != nil {
				return nil, persistenceInvalid("decode interview path note source", err)
			}
		}
		if topicID != nil {
			value := foundation.ID(*topicID)
			step.TopicID = &value
		}
		step.Status = domain.StepStatus(status)
		step.CreatedAt, step.UpdatedAt = step.CreatedAt.UTC(), step.UpdatedAt.UTC()
		if err := domain.ValidatePathStep(step); err != nil {
			return nil, persistenceInvalid("validate persisted learning path step", err)
		}
		if step.StepNo != expectedStepNo {
			return nil, persistenceInvalid("persisted learning path steps are not contiguous", nil)
		}
		expectedStepNo++
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := rows.Close(); err != nil {
		return nil, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return steps, nil
}
