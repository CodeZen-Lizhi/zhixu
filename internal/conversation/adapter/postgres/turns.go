package postgres

import (
	"context"
	"errors"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const questionViewColumns = `
	q.id::text,q.workspace_id::text,q.conversation_id::text,q.ordinal,q.question_text,q.scope::text,
	q.answer_depth,q.output_format,q.context_through_ordinal,q.context_hash,q.request_hash,q.created_at`

const answerViewColumns = `
	a.id::text,a.workspace_id::text,a.conversation_id::text,a.question_id::text,a.workflow_run_id::text,
	a.model_run_id::text,a.publication_status,a.result_type,a.result::text,a.result_hash,a.retrieval_summary::text,
	a.version,a.created_at,a.updated_at,a.published_at,
	w.id::text,w.status,w.version,w.updated_at`

const turnViewColumns = questionViewColumns + `,` + answerViewColumns

// ListTurns 批量返回一个 Conversation 内按 ordinal 稳定排序的 Turn 页面。
func (repository *Repository) ListTurns(ctx context.Context, query conversationapplication.ListTurnsQuery) (conversationapplication.TurnPage, error) {
	if repository == nil || isNilInterface(repository.db) {
		return conversationapplication.TurnPage{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateTurnListQuery(query); err != nil {
		return conversationapplication.TurnPage{}, err
	}
	if _, err := repository.GetConversation(ctx, query.WorkspaceID, query.ConversationID); err != nil {
		return conversationapplication.TurnPage{}, err
	}
	pageSize := query.Limit + 1
	var rows pgx.Rows
	var err error
	if query.Cursor == nil {
		rows, err = repository.db.Query(ctx, `SELECT `+turnViewColumns+`
			FROM agent.question q
			LEFT JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id AND a.conversation_id=q.conversation_id
			LEFT JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
			WHERE q.workspace_id=$1 AND q.conversation_id=$2
			ORDER BY q.ordinal ASC,q.id ASC
			LIMIT $3`, string(query.WorkspaceID), string(query.ConversationID), pageSize)
	} else {
		rows, err = repository.db.Query(ctx, `SELECT `+turnViewColumns+`
			FROM agent.question q
			LEFT JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id AND a.conversation_id=q.conversation_id
			LEFT JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
			WHERE q.workspace_id=$1 AND q.conversation_id=$2
			  AND (q.ordinal > $3 OR (q.ordinal = $3 AND q.id > $4))
			ORDER BY q.ordinal ASC,q.id ASC
			LIMIT $5`, string(query.WorkspaceID), string(query.ConversationID), query.Cursor.Ordinal, string(query.Cursor.QuestionID), pageSize)
	}
	if err != nil {
		return conversationapplication.TurnPage{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()

	items := make([]conversationapplication.TurnView, 0, query.Limit)
	for rows.Next() {
		turn, scanErr := scanTurnView(rows)
		if scanErr != nil {
			return conversationapplication.TurnPage{}, scanErr
		}
		if turn.Question.Request.WorkspaceID != query.WorkspaceID || turn.Question.Request.ConversationID != query.ConversationID ||
			turn.Answer == nil || turn.Answer.Answer.WorkspaceID != query.WorkspaceID || turn.Answer.Answer.ConversationID != query.ConversationID {
			return conversationapplication.TurnPage{}, consistency(ErrorCodePersistenceCorrupt, errors.New("turn read crossed conversation scope"))
		}
		items = append(items, turn)
	}
	if err := rows.Err(); err != nil {
		return conversationapplication.TurnPage{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	page := conversationapplication.TurnPage{Items: items}
	if len(items) > query.Limit {
		last := items[query.Limit-1].Question
		page.Items = items[:query.Limit]
		page.NextCursor = &conversationdomain.TurnCursor{Ordinal: last.Ordinal, QuestionID: last.ID}
	}
	return page, nil
}

// GetAnswer 在 Workspace 联合边界内读取 Answer 与 Workflow 权威状态。
func (repository *Repository) GetAnswer(ctx context.Context, workspaceID, answerID foundation.ID) (conversationapplication.AnswerView, error) {
	if repository == nil || isNilInterface(repository.db) {
		return conversationapplication.AnswerView{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if !validCanonicalID(workspaceID) || !validCanonicalID(answerID) || workspaceID == answerID {
		return conversationapplication.AnswerView{}, invalid(ErrorCodePersistenceInvalid, errors.New("answer query identity is invalid"))
	}
	view, err := scanAnswerView(repository.db.QueryRow(ctx, `SELECT `+answerViewColumns+`
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.id=$2`, string(workspaceID), string(answerID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.AnswerView{}, notFound(ErrorCodeAnswerNotFound, err)
	}
	if err != nil {
		return conversationapplication.AnswerView{}, err
	}
	if view.Answer.WorkspaceID != workspaceID || view.Answer.ID != answerID {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, errors.New("answer read crossed workspace boundary"))
	}
	return view, nil
}

// LoadPublishedContext 返回最新的已发布历史后缀，并同时满足 Turn 和字节上限。
func (repository *Repository) LoadPublishedContext(ctx context.Context, query conversationapplication.PublishedContextQuery) ([]conversationdomain.PublishedTurn, error) {
	if repository == nil || isNilInterface(repository.db) {
		return nil, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validatePublishedContextQuery(query); err != nil {
		return nil, err
	}
	if _, err := repository.GetConversation(ctx, query.WorkspaceID, query.ConversationID); err != nil {
		return nil, err
	}
	if query.ThroughOrdinal == 0 {
		return []conversationdomain.PublishedTurn{}, nil
	}
	rows, err := repository.db.Query(ctx, `SELECT `+turnViewColumns+`
		FROM agent.question q
		JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id AND a.conversation_id=q.conversation_id
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE q.workspace_id=$1 AND q.conversation_id=$2 AND q.ordinal <= $3
		  AND a.publication_status IN ('completed','refused','clarification_required')
		ORDER BY q.ordinal DESC,q.id DESC
		LIMIT $4`, string(query.WorkspaceID), string(query.ConversationID), query.ThroughOrdinal, conversationdomain.MaxContextTurns)
	if err != nil {
		return nil, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()

	descending := make([]conversationdomain.PublishedTurn, 0, conversationdomain.MaxContextTurns)
	byteCount := 0
	for rows.Next() {
		turn, scanErr := scanTurnView(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if turn.Answer == nil || turn.Answer.Answer.PublicationStatus == conversationdomain.AnswerPublicationPending ||
			turn.Question.Request.WorkspaceID != query.WorkspaceID || turn.Question.Request.ConversationID != query.ConversationID {
			return nil, consistency(ErrorCodePersistenceCorrupt, errors.New("published context row is not a published turn"))
		}
		turnBytes := len(turn.Question.Request.QuestionText) + len(turn.Answer.AssistantText)
		if byteCount+turnBytes > conversationdomain.MaxContextBytes {
			break
		}
		descending = append(descending, conversationdomain.PublishedTurn{
			QuestionID: turn.Question.ID, AnswerID: turn.Answer.Answer.ID, Ordinal: turn.Question.Ordinal,
			QuestionText: turn.Question.Request.QuestionText, AssistantText: turn.Answer.AssistantText,
			ResultType: string(turn.Answer.Answer.ResultType), ResultHash: turn.Answer.Answer.ResultHash,
		})
		byteCount += turnBytes
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, ErrorCodeDatabaseUnavailable)
	}
	turns := make([]conversationdomain.PublishedTurn, len(descending))
	for index := range descending {
		turns[len(descending)-1-index] = descending[index]
	}
	if _, through, _, err := conversationdomain.ComputeContextHash(turns); err != nil || through > query.ThroughOrdinal {
		return nil, consistency(ErrorCodePersistenceCorrupt, err)
	}
	return turns, nil
}

func validateTurnListQuery(query conversationapplication.ListTurnsQuery) error {
	if err := validateScopedConversationIDs(query.WorkspaceID, query.ConversationID); err != nil {
		return err
	}
	if err := conversationdomain.ValidatePageLimit(query.Limit); err != nil {
		return err
	}
	if query.Cursor != nil {
		if err := query.Cursor.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validatePublishedContextQuery(query conversationapplication.PublishedContextQuery) error {
	if err := validateScopedConversationIDs(query.WorkspaceID, query.ConversationID); err != nil {
		return err
	}
	if query.ThroughOrdinal < 0 {
		return invalid(ErrorCodePersistenceInvalid, errors.New("published context ordinal is invalid"))
	}
	return nil
}

var _ conversationapplication.Repository = (*Repository)(nil)
