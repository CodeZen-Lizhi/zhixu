package main

import (
	"context"
	"testing"
)

func TestRunRejectsMissingDatabaseConfiguration(t *testing.T) {
	for _, name := range []string{"ZHIXU_DATABASE_URL", "ZHIXU_DATABASE_HOST", "ZHIXU_DATABASE_PORT", "ZHIXU_DATABASE_NAME", "ZHIXU_DATABASE_USER", "ZHIXU_DATABASE_PASSWORD"} {
		t.Setenv(name, "")
	}
	if err := run(context.Background(), ""); err == nil {
		t.Fatal("missing database configuration was accepted")
	}
}

func TestRunRejectsNilContext(t *testing.T) {
	if err := run(nil, ""); err == nil {
		t.Fatal("nil context was accepted")
	}
}
