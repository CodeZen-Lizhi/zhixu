package postgres

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyAppendFailureMapsStablePostgreSQLCategories(t *testing.T) {
	tests := []struct {
		postgresCode string
		kind         foundation.ErrorKind
		code         string
		retryable    bool
	}{
		{postgresCode: "23503", kind: foundation.ErrorConsistencyViolation, code: domain.ErrorCodeAppendBindingInvalid},
		{postgresCode: "23514", kind: foundation.ErrorConsistencyViolation, code: domain.ErrorCodeAppendBindingInvalid},
		{postgresCode: "23505", kind: foundation.ErrorVersionConflict, code: domain.ErrorCodeAppendConflict},
		{postgresCode: "40001", kind: foundation.ErrorRetryableFailure, code: domain.ErrorCodeStoreUnavailable, retryable: true},
		{postgresCode: "40P01", kind: foundation.ErrorRetryableFailure, code: domain.ErrorCodeStoreUnavailable, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.postgresCode, func(t *testing.T) {
			err := classifyAppendFailure(&pgconn.PgError{Code: test.postgresCode})
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Code != test.code || classified.Retryable != test.retryable {
				t.Fatalf("classified error=%#v raw=%v", classified, err)
			}
		})
	}
}
