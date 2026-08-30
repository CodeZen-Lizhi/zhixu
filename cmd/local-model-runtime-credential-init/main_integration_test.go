//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidateRuntimeRoleAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required")
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	databaseName := fmt.Sprintf("zhixu_runtime_role_%d_%d", os.Getpid(), time.Now().UnixNano())
	databaseIdentifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+databaseIdentifier); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = databaseName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), `ALTER ROLE zhixu_local_model_runtime RESET ALL`)
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+databaseIdentifier+" WITH (FORCE)")
	}()
	if err := platformmigration.MigrateAtlas(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeRole(ctx, pool); err != nil {
		t.Fatalf("validate clean runtime role: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER ROLE zhixu_local_model_runtime SET statement_timeout='1s'`); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeRole(ctx, pool); err == nil {
		t.Fatal("runtime role with a global setting unexpectedly passed validation")
	}
}
