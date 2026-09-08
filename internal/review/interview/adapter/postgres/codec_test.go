package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestEncodeJSONReturnsPersistenceError(t *testing.T) {
	t.Parallel()

	_, err := encodeJSON(map[string]any{"unsupported": make(chan int)})
	var typed *foundation.Error
	if !errors.As(err, &typed) {
		t.Fatalf("encodeJSON error type = %T, want *foundation.Error", err)
	}
	if typed.Kind != foundation.ErrorConsistencyViolation || typed.Code != domain.ErrorCodePersistenceInvalid {
		t.Fatalf("encodeJSON error = %+v, want persistence consistency violation", typed)
	}
}

func TestLoadSessionClassifiesRowErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rowError error
		wantCode string
	}{
		{name: "not found", rowError: sql.ErrNoRows, wantCode: domain.ErrorCodeSessionNotFound},
		{name: "dependency unavailable", rowError: errors.New("database unavailable"), wantCode: domain.ErrorCodeDependencyUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := gormInterviewReadSession(context.Background(), sessionErrorRow{err: test.rowError})
			var typed *foundation.Error
			if !errors.As(err, &typed) {
				t.Fatalf("loadSession error type = %T, want *foundation.Error", err)
			}
			if typed.Code != test.wantCode {
				t.Fatalf("loadSession error code = %q, want %q", typed.Code, test.wantCode)
			}
		})
	}
}

type sessionErrorRow struct{ err error }

func (row sessionErrorRow) Scan(...any) error { return row.err }
