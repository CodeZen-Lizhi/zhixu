package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRepositoryQuestionExecutionContextRejectsInvalidQueryBeforeDatabase(t *testing.T) {
	valid := conversationapplication.QuestionExecutionContextQuery{
		WorkspaceID: "76000000-0000-4000-8000-000000000001", WorkflowRunID: "76000000-0000-4000-8000-000000000002",
		ConversationID: "76000000-0000-4000-8000-000000000003", QuestionID: "76000000-0000-4000-8000-000000000004",
		AnswerID: "76000000-0000-4000-8000-000000000005", QuestionOrdinal: 1, ContextHash: strings.Repeat("a", 64),
	}
	tests := []struct {
		name   string
		mutate func(*conversationapplication.QuestionExecutionContextQuery)
	}{
		{name: "malformed identity", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) { query.QuestionID = "bad" }},
		{name: "reused identity", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) { query.AnswerID = query.QuestionID }},
		{name: "zero ordinal", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) { query.QuestionOrdinal = 0 }},
		{name: "non canonical hash", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) {
			query.ContextHash = strings.Repeat("A", 64)
		}},
	}
	database, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: noQueryExecutionContextDB{}}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	repository := &GORMRepository{db: database}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := valid
			test.mutate(&query)
			_, err := repository.LoadQuestionExecutionContext(context.Background(), query)
			requireExecutionContextError(t, err, foundation.ErrorInvalidInput, ErrorCodeExecutionContextInvalid)
		})
	}
}

func requireExecutionContextError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("execution context error = %#v, want kind=%s code=%s", err, kind, code)
	}
}

type noQueryExecutionContextDB struct{}

func (noQueryExecutionContextDB) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	panic("invalid execution context must not begin a transaction")
}

func (noQueryExecutionContextDB) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("invalid execution context must not prepare a database query")
}

func (noQueryExecutionContextDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("invalid execution context must not write to the database")
}

func (noQueryExecutionContextDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("invalid execution context must not query the database")
}

func (noQueryExecutionContextDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("invalid execution context must not query the database")
}
