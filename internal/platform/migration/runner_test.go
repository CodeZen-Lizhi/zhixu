package migration

import (
	"testing"
	"testing/fstest"

	"github.com/riverqueue/river/rivermigrate"
)

func TestNewRunnerRejectsMissingDependencies(t *testing.T) {
	if _, err := NewRunner(nil, fstest.MapFS{}); err == nil {
		t.Fatal("nil pool was accepted")
	}
}

func TestValidationMessage(t *testing.T) {
	if got := validationMessage(nil); got != "empty validation result" {
		t.Fatalf("nil validation message=%q", got)
	}
	if got := validationMessage(&rivermigrate.ValidateResult{}); got != "validation failed without details" {
		t.Fatalf("empty validation message=%q", got)
	}
	if got := validationMessage(&rivermigrate.ValidateResult{Messages: []string{"one", "two"}}); got != "one; two" {
		t.Fatalf("validation message=%q", got)
	}
}
