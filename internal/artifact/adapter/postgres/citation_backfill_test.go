package postgres

import (
	"context"
	"errors"
	"testing"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestShouldPersistCitationBackfillFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "deadline", err: context.DeadlineExceeded, want: false},
		{name: "wrapped canceled", err: classify(context.Canceled), want: false},
		{name: "retryable", err: foundation.NewError(foundation.ErrorRetryableFailure, artifactapp.ErrorCodeDependencyUnavailable, true, errors.New("retry")), want: false},
		{name: "unavailable", err: unavailable(errors.New("unavailable")), want: false},
		{name: "consistency", err: inconsistent(errors.New("corrupt revision")), want: true},
		{name: "unclassified data", err: errors.New("invalid canonical data"), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldPersistCitationBackfillFailure(test.err); got != test.want {
				t.Fatalf("shouldPersistCitationBackfillFailure()=%t want %t", got, test.want)
			}
		})
	}
}
