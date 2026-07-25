package postgres

import (
	"context"
	"errors"
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
