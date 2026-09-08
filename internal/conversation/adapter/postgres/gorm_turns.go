package postgres

import (
	"context"
	"database/sql"
	"errors"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// ListTurns 以单条有界关联查询读取 Turn、Answer 和当前 Workflow 阶段。
func (repository *GORMRepository) ListTurns(ctx context.Context, query conversationapplication.ListTurnsQuery) (conversationapplication.TurnPage, error) {
	if repository == nil || repository.db == nil {
		return conversationapplication.TurnPage{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateTurnListQuery(query); err != nil {
		return conversationapplication.TurnPage{}, err
	}
	if _, err := repository.GetConversation(ctx, query.WorkspaceID, query.ConversationID); err != nil {
		return conversationapplication.TurnPage{}, err
	}
	statement := repository.db.WithContext(ctx).Table("agent.question q").Select(turnViewColumns).
		Joins("LEFT JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id AND a.conversation_id=q.conversation_id").
		Joins("LEFT JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id").
		Where("q.workspace_id=? AND q.conversation_id=?", string(query.WorkspaceID), string(query.ConversationID))
	if query.Latest {
		statement = statement.Order("q.ordinal DESC,q.id DESC").Limit(1)
	} else {
		if query.Cursor != nil {
			statement = statement.Where("q.ordinal > ? OR (q.ordinal = ? AND q.id > ?)", query.Cursor.Ordinal, query.Cursor.Ordinal, string(query.Cursor.QuestionID))
		}
		statement = statement.Order("q.ordinal ASC,q.id ASC").Limit(query.Limit + 1)
	}
	rows, err := statement.Rows()
	if err != nil {
		return conversationapplication.TurnPage{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	items := make([]conversationapplication.TurnView, 0, query.Limit)
	for rows.Next() {
		turn, err := scanTurnView(rows)
		if err != nil {
			return conversationapplication.TurnPage{}, err
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
	if !query.Latest && len(items) > query.Limit {
		last := items[query.Limit-1].Question
		page.Items = items[:query.Limit]
		page.NextCursor = &conversationdomain.TurnCursor{Ordinal: last.Ordinal, QuestionID: last.ID}
	}
	return page, nil
}

// GetAnswer 在 Workspace 联合边界内读取 Answer 与 Workflow 权威状态。
func (repository *GORMRepository) GetAnswer(ctx context.Context, workspaceID, answerID foundation.ID) (conversationapplication.AnswerView, error) {
	if repository == nil || repository.db == nil {
		return conversationapplication.AnswerView{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if !validCanonicalID(workspaceID) || !validCanonicalID(answerID) || workspaceID == answerID {
		return conversationapplication.AnswerView{}, invalid(ErrorCodePersistenceInvalid, errors.New("answer query identity is invalid"))
	}
	view, err := scanAnswerView(gormScanRow(repository.db.WithContext(ctx).Table("agent.answer a").Select(answerViewColumns).
		Joins("JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id").
		Where("a.workspace_id=? AND a.id=?", string(workspaceID), string(answerID))))
	if errors.Is(err, sql.ErrNoRows) {
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

// LoadPublishedContext 返回同时满足数量和字节上限的已发布历史后缀。
func (repository *GORMRepository) LoadPublishedContext(ctx context.Context, query conversationapplication.PublishedContextQuery) ([]conversationdomain.PublishedTurn, error) {
	if repository == nil || repository.db == nil {
		return nil, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validatePublishedContextQuery(query); err != nil {
		return nil, err
	}
	if _, err := repository.GetConversation(ctx, query.WorkspaceID, query.ConversationID); err != nil {
		return nil, err
	}
	return loadGORMPublishedContext(ctx, repository.db, query)
}

func loadGORMPublishedContext(ctx context.Context, db *gorm.DB, query conversationapplication.PublishedContextQuery) ([]conversationdomain.PublishedTurn, error) {
	if query.ThroughOrdinal == 0 {
		return []conversationdomain.PublishedTurn{}, nil
	}
	rows, err := db.WithContext(ctx).Table("agent.question q").Select(turnViewColumns).
		Joins("JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id AND a.conversation_id=q.conversation_id").
		Joins("JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id").
		Where("q.workspace_id=? AND q.conversation_id=? AND q.ordinal <= ?", string(query.WorkspaceID), string(query.ConversationID), query.ThroughOrdinal).
		Where("a.publication_status IN ('completed','refused','clarification_required')").
		Order("q.ordinal DESC,q.id DESC").Limit(conversationdomain.MaxContextTurns).Rows()
	if err != nil {
		return nil, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	descending := make([]conversationdomain.PublishedTurn, 0, conversationdomain.MaxContextTurns)
	byteCount := 0
	for rows.Next() {
		turn, err := scanTurnView(rows)
		if err != nil {
			return nil, err
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

// LoadQuestionExecutionContext 复核 Workflow 输入并返回冻结的 Conversation 历史。
func (repository *GORMRepository) LoadQuestionExecutionContext(ctx context.Context, query conversationapplication.QuestionExecutionContextQuery) (conversationapplication.QuestionExecutionContext, error) {
	if repository == nil || repository.db == nil {
		return conversationapplication.QuestionExecutionContext{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateQuestionExecutionContextQuery(query); err != nil {
		return conversationapplication.QuestionExecutionContext{}, err
	}
	turn, err := scanTurnView(gormScanRow(repository.db.WithContext(ctx).Table("agent.question q").Select(turnViewColumns).
		Joins("JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id AND a.conversation_id=q.conversation_id").
		Joins("JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id").
		Where("q.workspace_id=? AND q.conversation_id=? AND q.id=?", string(query.WorkspaceID), string(query.ConversationID), string(query.QuestionID))))
	if errors.Is(err, sql.ErrNoRows) {
		return conversationapplication.QuestionExecutionContext{}, consistency(ErrorCodeExecutionContextCorrupt, errors.New("question execution context is missing"))
	}
	if err != nil {
		return conversationapplication.QuestionExecutionContext{}, err
	}
	if turn.Answer == nil || turn.Question.ID != query.QuestionID || turn.Question.Ordinal != query.QuestionOrdinal ||
		turn.Question.Request.WorkspaceID != query.WorkspaceID || turn.Question.Request.ConversationID != query.ConversationID ||
		turn.Question.ContextHash != query.ContextHash || turn.Answer.Answer.ID != query.AnswerID || turn.Answer.Answer.WorkflowRunID != query.WorkflowRunID {
		return conversationapplication.QuestionExecutionContext{}, consistency(ErrorCodeExecutionContextCorrupt, errors.New("question execution binding differs"))
	}
	history, err := loadGORMPublishedContext(ctx, repository.db, conversationapplication.PublishedContextQuery{
		WorkspaceID: query.WorkspaceID, ConversationID: query.ConversationID, ThroughOrdinal: turn.Question.ContextThroughOrdinal,
	})
	if err != nil {
		return conversationapplication.QuestionExecutionContext{}, err
	}
	hash, through, _, err := conversationdomain.ComputeContextHash(history)
	if err != nil || hash != query.ContextHash || through != turn.Question.ContextThroughOrdinal {
		return conversationapplication.QuestionExecutionContext{}, consistency(ErrorCodeExecutionContextCorrupt, errors.New("question execution context hash differs"))
	}
	return conversationapplication.QuestionExecutionContext{Question: turn.Question, Answer: turn.Answer.Answer, History: history}, nil
}

var _ conversationapplication.Repository = (*GORMRepository)(nil)
var _ conversationapplication.QuestionExecutionContextLoader = (*GORMRepository)(nil)
