package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewQuestionDispatcherRejectsTypedNilDependencies(t *testing.T) {
	db := &questionConstructorDB{}
	runtime := &questionConstructorRuntime{}
	events := &questionConstructorAppender{}
	ids := foundation.NewUUIDGenerator(nil)
	clock := foundation.SystemClock{}

	var nilDB *questionConstructorDB
	var nilRuntime *questionConstructorRuntime
	var nilEvents *questionConstructorAppender
	tests := []struct {
		name    string
		db      DB
		runtime questionRuntimeStarter
		events  eventsapplication.Appender
		kind    foundation.ErrorKind
		code    string
	}{
		{name: "typed nil database", db: nilDB, runtime: runtime, events: events, kind: foundation.ErrorDependencyUnavailable, code: ErrorCodeQuestionDispatchUnavailable},
		{name: "typed nil runtime", db: db, runtime: nilRuntime, events: events, kind: foundation.ErrorDependencyUnavailable, code: ErrorCodeQuestionDispatchUnavailable},
		{name: "typed nil events", db: db, runtime: runtime, events: nilEvents, kind: foundation.ErrorDependencyUnavailable, code: ErrorCodeQuestionDispatchUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewQuestionDispatcher(test.db, test.runtime, test.events, ids, clock)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Code != test.code {
				t.Fatalf("NewQuestionDispatcher() error = %#v", err)
			}
		})
	}
}

func TestNewQuestionDispatcherUsesRegisteredV2Definition(t *testing.T) {
	dispatcher, err := NewQuestionDispatcher(
		&questionConstructorDB{},
		&questionConstructorRuntime{},
		&questionConstructorAppender{},
		foundation.NewUUIDGenerator(nil),
		foundation.SystemClock{},
	)
	if err != nil || dispatcher == nil {
		t.Fatalf("dispatcher=%#v err=%v", dispatcher, err)
	}
	// The constructor deliberately has no Definition selector; startQuestionWorkflow
	// resolves RegisteredDefinitionV2 at the composition boundary.
	if got := conversationworkflow.RegisteredDefinitionV2().Version; got != conversationworkflow.DefinitionVersionV2 {
		t.Fatalf("registered v2 version=%d", got)
	}
}

func TestClassifyQuestionAnswerInsertFailureUsesTriggerContractCodes(t *testing.T) {
	tests := []struct {
		name      string
		cause     error
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{
			name: "active workflow trigger", cause: &pgconn.PgError{Code: "23505", ConstraintName: answerActiveWorkflowConstraint},
			kind: foundation.ErrorVersionConflict, code: ErrorCodeQuestionActiveWorkflow,
		},
		{
			name: "archived conversation trigger", cause: &pgconn.PgError{Code: "23514", ConstraintName: answerConversationOpenConstraint},
			kind: foundation.ErrorVersionConflict, code: ErrorCodeQuestionConversationArchived,
		},
		{
			name: "unrelated database failure", cause: &pgconn.PgError{Code: "40P01"},
			kind: foundation.ErrorRetryableFailure, code: ErrorCodeQuestionDispatchUnavailable, retryable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyQuestionAnswerInsertFailure(test.cause)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Code != test.code || classified.Retryable != test.retryable {
				t.Fatalf("classified error = %#v", err)
			}
		})
	}
}

func TestNewQuestionDispatcherRejectsTypedNilIdentityAndClockDependencies(t *testing.T) {
	db := &questionConstructorDB{}
	runtime := &questionConstructorRuntime{}
	events := &questionConstructorAppender{}
	var nilIDs *questionConstructorIDs
	var nilClock *questionConstructorClock
	tests := []struct {
		name  string
		ids   foundation.IDGenerator
		clock foundation.Clock
	}{
		{name: "typed nil identity generator", ids: nilIDs, clock: &questionConstructorClock{}},
		{name: "typed nil clock", ids: &questionConstructorIDs{}, clock: nilClock},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewQuestionDispatcher(db, runtime, events, test.ids, test.clock)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable ||
				classified.Code != ErrorCodeQuestionDispatchUnavailable {
				t.Fatalf("NewQuestionDispatcher() error = %#v", err)
			}
		})
	}
}

type questionConstructorDB struct{}

func (*questionConstructorDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unused")
}
func (*questionConstructorDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (*questionConstructorDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type questionConstructorRuntime struct{}

func (*questionConstructorRuntime) StartTx(context.Context, pgx.Tx, workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	return workflowapplication.RuntimeStartResult{}, errors.New("unused")
}

type questionConstructorAppender struct{}

func (*questionConstructorAppender) AppendTx(context.Context, any, eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	return eventsdomain.ServerEvent{}, false, errors.New("unused")
}

type questionConstructorIDs struct{}

func (*questionConstructorIDs) New() (foundation.ID, error) {
	return "", errors.New("unused")
}

type questionConstructorClock struct{}

func (*questionConstructorClock) Now() time.Time { return time.Time{} }
