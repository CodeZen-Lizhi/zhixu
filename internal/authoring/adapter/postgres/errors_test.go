package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestClassifyImmutableTriggerFailureAsConsistencyViolation(t *testing.T) {
	err := classify(&pgconn.PgError{Code: "55000"}, "AUTHORING_WRITE_FAILED")
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("classified error = %T %v", err, err)
	}
	if classified.Kind != foundation.ErrorConsistencyViolation ||
		classified.Code != authoringapp.ErrorCodeResultInvalid || classified.Retryable {
		t.Fatalf("classification = %#v", classified)
	}
}
