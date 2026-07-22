package postgres

import (
	"context"
	"errors"
	"testing"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyCollectionQueryTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "context deadline", err: context.DeadlineExceeded},
		{name: "postgres statement timeout", err: &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}},
		{name: "postgres query cancel", err: &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			classified := classify(test.err)
			var foundationErr *foundation.Error
			if !errors.As(classified, &foundationErr) {
				t.Fatalf("classified=%v is not foundation error", classified)
			}
			if foundationErr.Code != collectionapp.ErrorCodeQueryTimeout || foundationErr.Kind != foundation.ErrorDependencyUnavailable || !foundationErr.Retryable {
				t.Fatalf("foundation error=%+v", foundationErr)
			}
			if !errors.Is(classified, test.err) {
				t.Fatalf("classified error does not preserve cause: %v", classified)
			}
		})
	}
}

func TestClassifyCollectionCancellationIsNonRetryable(t *testing.T) {
	t.Parallel()
	classified := classify(context.Canceled)
	var foundationErr *foundation.Error
	if !errors.As(classified, &foundationErr) {
		t.Fatalf("classified=%v is not foundation error", classified)
	}
	if foundationErr.Code != collectionapp.ErrorCodeDependencyUnavailable || foundationErr.Retryable {
		t.Fatalf("foundation error=%+v", foundationErr)
	}
}
