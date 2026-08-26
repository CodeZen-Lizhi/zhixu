//go:build integration && testcontainers

package testdb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestFixtureStartsMigratesAndSharesPlatformPool(t *testing.T) {
	fixture := Require(t, Config{Availability: SkipWhenUnavailable, MaxConns: 4})
	if fixture == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if fixture.Pool() == nil {
		t.Fatal("fixture returned nil platform pool")
	}
	if _, err := fixture.Pool().GORM(); err != nil {
		t.Fatalf("fixture did not initialize GORM: %v", err)
	}
	if _, err := fixture.Pool().UnitOfWork(); err != nil {
		t.Fatalf("fixture did not initialize Unit of Work: %v", err)
	}
	if _, err := fixture.Pool().RiverSQLDriver(); err != nil {
		t.Fatalf("fixture did not initialize River database/sql driver: %v", err)
	}

	var vectorExtension bool
	if err := fixture.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')`).Scan(&vectorExtension); err != nil {
		t.Fatalf("read vector extension: %v", err)
	}
	if !vectorExtension {
		t.Fatal("fixture did not install vector extension")
	}
	var projectMigrations int
	if err := fixture.Pool().QueryRow(ctx, `
		SELECT count(*)
		FROM public.goose_db_version
		WHERE is_applied AND version_id > 0`).Scan(&projectMigrations); err != nil {
		t.Fatalf("read project migration history: %v", err)
	}
	if projectMigrations == 0 {
		t.Fatal("fixture did not apply project migrations")
	}
	var riverTable bool
	if err := fixture.Pool().QueryRow(ctx, `
		SELECT to_regclass('workflow.river_job') IS NOT NULL`).Scan(&riverTable); err != nil {
		t.Fatalf("read River schema: %v", err)
	}
	if !riverTable {
		t.Fatal("fixture did not apply River schema")
	}
}

func TestFixturesAreParallelAndIsolated(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	fixtures := make([]*Fixture, 2)
	errorsByFixture := make([]error, 2)
	var waitGroup sync.WaitGroup
	for index := range fixtures {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			fixtures[index], errorsByFixture[index] = Open(ctx, Config{
				Migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
					if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
						return err
					}
					_, err := pool.Exec(ctx, `CREATE TABLE testdb_probe (value text PRIMARY KEY)`)
					return err
				},
			})
		}(index)
	}
	waitGroup.Wait()
	for index, err := range errorsByFixture {
		if err != nil {
			for _, fixture := range fixtures {
				_ = fixture.Close(context.Background())
			}
			t.Fatalf("open parallel fixture %d: %v", index, err)
		}
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Cleanup(func() {
			if err := fixture.Close(context.Background()); err != nil {
				t.Errorf("close parallel fixture: %v", err)
			}
		})
	}
	if fixtures[0].Diagnostics().ContainerID == fixtures[1].Diagnostics().ContainerID {
		t.Fatalf("parallel fixtures share container %q", fixtures[0].Diagnostics().ContainerID)
	}
	if fixtures[0].databaseURL == fixtures[1].databaseURL {
		t.Fatal("parallel fixtures share a database URL")
	}

	for index, fixture := range fixtures {
		value := fmt.Sprintf("fixture-%d", index)
		if _, err := fixture.Pool().DB().Exec(ctx, `INSERT INTO testdb_probe(value) VALUES ($1)`, value); err != nil {
			t.Fatalf("write fixture %d probe: %v", index, err)
		}
	}
	for index, fixture := range fixtures {
		ownValue := fmt.Sprintf("fixture-%d", index)
		otherValue := fmt.Sprintf("fixture-%d", 1-index)
		var ownCount, otherCount int
		if err := fixture.Pool().QueryRow(ctx, `SELECT count(*) FROM testdb_probe WHERE value = $1`, ownValue).Scan(&ownCount); err != nil {
			t.Fatalf("read fixture %d own probe: %v", index, err)
		}
		if err := fixture.Pool().QueryRow(ctx, `SELECT count(*) FROM testdb_probe WHERE value = $1`, otherValue).Scan(&otherCount); err != nil {
			t.Fatalf("read fixture %d other probe: %v", index, err)
		}
		if ownCount != 1 || otherCount != 0 {
			t.Fatalf("fixture %d probe counts = own:%d other:%d", index, ownCount, otherCount)
		}
	}
}

func TestExternalAdminCreatesAndDropsOnlyGeneratedDatabase(t *testing.T) {
	admin := runAdminContainer(t)
	adminURL, err := admin.ConnectionString(context.Background(), "sslmode=disable")
	if err != nil {
		t.Fatalf("resolve admin connection: %v", err)
	}
	fixture, err := Open(context.Background(), Config{
		ExternalAdminURL: adminURL,
	})
	if err != nil {
		t.Fatalf("open external-admin fixture: %v", err)
	}
	name := fixture.Diagnostics().Database
	if name == "" || name == "postgres" {
		t.Fatalf("unexpected generated database name: %q", name)
	}
	assertDatabaseExists(t, adminURL, name, true)
	assertRelationExists(t, adminURL, "public.goose_db_version", false)
	if err := fixture.Close(context.Background()); err != nil {
		t.Fatalf("close external-admin fixture: %v", err)
	}
	if err := fixture.Close(context.Background()); err != nil {
		t.Fatalf("second close external-admin fixture: %v", err)
	}
	assertDatabaseExists(t, adminURL, name, false)
	assertDatabaseExists(t, adminURL, "postgres", true)
}

func TestContainerMigrationFailureReturnsSentinelAndTerminatesContainer(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	sentinel := errors.New("migration sentinel")
	var provisioned *Fixture
	fixture, err := Open(context.Background(), Config{
		Migrate: func(context.Context, *pgxpool.Pool) error {
			return sentinel
		},
		provisioned: func(fixture *Fixture) {
			provisioned = fixture
		},
	})
	if err == nil {
		if fixture != nil {
			_ = fixture.Close(context.Background())
		}
		t.Fatal("expected migration failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("migration error lost sentinel: %v", err)
	}
	if provisioned == nil || !provisioned.Diagnostics().Closed {
		t.Fatal("failed container fixture did not run cleanup")
	}
	if provisioned.container.IsRunning() {
		t.Fatal("failed container fixture is still running")
	}
}

func TestExternalAdminMigrationFailureDropsGeneratedDatabase(t *testing.T) {
	admin := runAdminContainer(t)
	adminURL, err := admin.ConnectionString(context.Background(), "sslmode=disable")
	if err != nil {
		t.Fatalf("resolve admin connection: %v", err)
	}
	sentinel := errors.New("external migration sentinel")
	var provisioned *Fixture
	fixture, err := Open(context.Background(), Config{
		ExternalAdminURL: adminURL,
		Migrate: func(context.Context, *pgxpool.Pool) error {
			return sentinel
		},
		provisioned: func(fixture *Fixture) {
			provisioned = fixture
		},
	})
	if err == nil {
		if fixture != nil {
			_ = fixture.Close(context.Background())
		}
		t.Fatal("expected external migration failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("external migration error lost sentinel: %v", err)
	}
	if provisioned == nil || !provisioned.Diagnostics().Closed {
		t.Fatal("failed external fixture did not run cleanup")
	}
	assertDatabaseExists(t, adminURL, provisioned.databaseName, false)
	assertDatabaseExists(t, adminURL, "postgres", true)
}

func runAdminContainer(t *testing.T) *postgrescontainer.PostgresContainer {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := postgrescontainer.Run(
		ctx,
		DefaultImage,
		postgrescontainer.WithDatabase("postgres"),
		postgrescontainer.WithUsername("postgres"),
		postgrescontainer.WithPassword("zhixu-test"),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start admin PostgreSQL container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate admin PostgreSQL container: %v", err)
		}
	})
	return container
}

func assertDatabaseExists(t *testing.T, adminURL, databaseName string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin assertion pool: %v", err)
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, databaseName).Scan(&exists); err != nil {
		t.Fatalf("check database %q: %v", databaseName, err)
	}
	if exists != want {
		t.Fatalf("database %q exists = %t, want %t", databaseName, exists, want)
	}
}

func assertRelationExists(t *testing.T, databaseURL, relationName string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open relation assertion pool: %v", err)
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, relationName).Scan(&exists); err != nil {
		t.Fatalf("check relation %q: %v", relationName, err)
	}
	if exists != want {
		t.Fatalf("relation %q exists = %t, want %t", relationName, exists, want)
	}
}
