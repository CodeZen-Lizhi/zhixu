package domain

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateIndexTransitionAcceptsOnlyGenericLifecycleEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     IndexStatus
		to       IndexStatus
		accepted bool
	}{
		{name: "build becomes ready", from: IndexStatusBuilding, to: IndexStatusReady, accepted: true},
		{name: "build fails", from: IndexStatusBuilding, to: IndexStatusFailed, accepted: true},
		{name: "active retires", from: IndexStatusActive, to: IndexStatusRetiring, accepted: true},
		{name: "retiring archives", from: IndexStatusRetiring, to: IndexStatusArchived, accepted: true},
		{name: "ready cannot activate generically", from: IndexStatusReady, to: IndexStatusActive, accepted: false},
		{name: "retiring cannot reactivate generically", from: IndexStatusRetiring, to: IndexStatusActive, accepted: false},
		{name: "terminal cannot reopen", from: IndexStatusArchived, to: IndexStatusBuilding, accepted: false},
		{name: "unknown rejected", from: IndexStatus("future"), to: IndexStatusReady, accepted: false},
		{name: "self transition rejected", from: IndexStatusBuilding, to: IndexStatusBuilding, accepted: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateIndexTransition(test.from, test.to)
			if (err == nil) != test.accepted {
				t.Fatalf("ValidateIndexTransition(%q, %q) error = %v, accepted = %t", test.from, test.to, err, test.accepted)
			}
			if err != nil {
				assertDomainError(t, err, foundation.ErrorVersionConflict, ErrorCodeIndexTransitionInvalid)
			}
		})
	}
}

func TestIndexStatusHelpers(t *testing.T) {
	t.Parallel()

	for _, status := range []IndexStatus{IndexStatusBuilding, IndexStatusReady, IndexStatusActive, IndexStatusRetiring, IndexStatusArchived, IndexStatusFailed} {
		if !IsValidIndexStatus(status) {
			t.Errorf("IsValidIndexStatus(%q) = false", status)
		}
	}
	if IsValidIndexStatus("future") {
		t.Fatal("future status accepted")
	}
	if !IsTerminalIndexStatus(IndexStatusArchived) || !IsTerminalIndexStatus(IndexStatusFailed) {
		t.Fatal("terminal statuses were not recognized")
	}
	if IsTerminalIndexStatus(IndexStatusActive) {
		t.Fatal("active status marked terminal")
	}
}

func TestIndexTransitionTableCoversEveryStatusPair(t *testing.T) {
	t.Parallel()

	statuses := []IndexStatus{
		IndexStatusBuilding,
		IndexStatusReady,
		IndexStatusActive,
		IndexStatusRetiring,
		IndexStatusArchived,
		IndexStatusFailed,
	}
	for _, from := range statuses {
		for _, to := range statuses {
			_, accepted := indexTransitions[from][to]
			if err := ValidateIndexTransition(from, to); (err == nil) != accepted {
				t.Errorf("ValidateIndexTransition(%q, %q) error = %v, accepted = %t", from, to, err, accepted)
			}
		}
	}
}

func assertDomainError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error = %T %v, want *foundation.Error", err, err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable {
		t.Fatalf("error = %#v, want kind=%q code=%q retryable=false", classified, kind, code)
	}
}
