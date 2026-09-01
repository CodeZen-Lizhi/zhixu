package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifySQLStateMatrix(t *testing.T) {
	tests := []struct {
		name      string
		sqlState  string
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{name: "serialization", sqlState: "40001", kind: foundation.ErrorRetryableFailure, code: errorCodeStorageRetryable, retryable: true},
		{name: "deadlock", sqlState: "40P01", kind: foundation.ErrorRetryableFailure, code: errorCodeStorageRetryable, retryable: true},
		{name: "lock unavailable", sqlState: "55P03", kind: foundation.ErrorRetryableFailure, code: errorCodeStorageRetryable, retryable: true},
		{name: "unique", sqlState: "23505", kind: foundation.ErrorVersionConflict, code: domain.ErrorCodeVersionConflict},
		{name: "foreign key", sqlState: "23503", kind: foundation.ErrorConsistencyViolation, code: errorCodeStorageConsistency},
		{name: "not null", sqlState: "23502", kind: foundation.ErrorConsistencyViolation, code: errorCodeStorageConsistency},
		{name: "check", sqlState: "23514", kind: foundation.ErrorConsistencyViolation, code: errorCodeStorageConsistency},
		{name: "invalid text", sqlState: "22P02", kind: foundation.ErrorConsistencyViolation, code: errorCodeStorageConsistency},
		{name: "string too long", sqlState: "22001", kind: foundation.ErrorConsistencyViolation, code: errorCodeStorageConsistency},
		{name: "numeric out of range", sqlState: "22003", kind: foundation.ErrorConsistencyViolation, code: errorCodeStorageConsistency},
		{name: "guard", sqlState: "55000", kind: foundation.ErrorConsistencyViolation, code: errorCodeStorageConsistency},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classify(&pgconn.PgError{Code: test.sqlState}, "KNOWLEDGE_TEST_FAILURE")
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Code != test.code || classified.Retryable != test.retryable {
				t.Fatalf("classified=%#v err=%v", classified, err)
			}
		})
	}
}

func TestClassifyPreservesCallerCancellation(t *testing.T) {
	err := classify(context.Canceled, "KNOWLEDGE_TEST_CANCELLED")
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || classified.Retryable || !errors.Is(err, context.Canceled) {
		t.Fatalf("classified=%#v err=%v", classified, err)
	}
}

func TestClassifyGORMKnowledgePreservesFoundationEnvelope(t *testing.T) {
	original := foundation.NewError(foundation.ErrorVersionConflict, "KNOWLEDGE_TEST_CONFLICT", false, errors.New("conflict"))
	wrapped := fmt.Errorf("execute PostgreSQL transaction: %w", original)

	if classified := classifyGORMKnowledge(context.Background(), wrapped, "KNOWLEDGE_TEST_FALLBACK"); classified != original {
		t.Fatalf("classified=%#v want original=%#v", classified, original)
	}
	if classified := classifyGORMRelationApply(context.Background(), wrapped, "KNOWLEDGE_TEST_FALLBACK"); classified != original {
		t.Fatalf("relation apply classified=%#v want original=%#v", classified, original)
	}
}

func TestClassifyGORMKnowledgeContextMatrix(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		kind      foundation.ErrorKind
		retryable bool
	}{
		{name: "caller cancellation", err: context.Canceled, kind: foundation.ErrorNonRetryableFailure},
		{name: "deadline", err: context.DeadlineExceeded, kind: foundation.ErrorRetryableFailure, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyGORMKnowledge(context.Background(), test.err, "KNOWLEDGE_TEST_CONTEXT")
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Retryable != test.retryable || !errors.Is(err, test.err) {
				t.Fatalf("classified=%#v err=%v", classified, err)
			}
		})
	}
}

func TestTimelineProjectionTreatsUniqueEventBindingConflictAsPoison(t *testing.T) {
	err := timelineStorage(&pgconn.PgError{Code: "23505"})
	if !shouldPoisonTimelineProjection(err) {
		t.Fatalf("unique event binding conflict must poison projection: %v", err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict || classified.Retryable {
		t.Fatalf("classified=%#v err=%v", classified, err)
	}
}
