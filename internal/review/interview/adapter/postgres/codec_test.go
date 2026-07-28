package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/jackc/pgx/v5"
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
		{name: "not found", rowError: pgx.ErrNoRows, wantCode: domain.ErrorCodeSessionNotFound},
		{name: "dependency unavailable", rowError: errors.New("database unavailable"), wantCode: domain.ErrorCodeDependencyUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadSession(context.Background(), sessionErrorQueryer{err: test.rowError}, foundation.ID("00000000-0000-0000-0000-000000000001"), foundation.ID("00000000-0000-0000-0000-000000000002"), false)
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

type sessionErrorQueryer struct{ err error }

func (queryer sessionErrorQueryer) QueryRow(context.Context, string, ...any) pgx.Row {
	return sessionErrorRow{err: queryer.err}
}

type sessionErrorRow struct{ err error }

func (row sessionErrorRow) Scan(...any) error { return row.err }
