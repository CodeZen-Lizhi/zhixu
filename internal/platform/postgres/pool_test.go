package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBuildPoolConfigInstallsPGVectorRegistration(t *testing.T) {
	registrationCalled := false
	config, err := buildPoolConfig(
		"postgres://test:test@localhost:5432/zhixu?sslmode=disable",
		12,
		3,
		func(context.Context, *pgx.Conn) error {
			registrationCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("build pool config: %v", err)
	}
	if config.AfterConnect == nil {
		t.Fatal("expected pgvector AfterConnect registration")
	}
	if config.MaxConns != 12 {
		t.Fatalf("MaxConns = %d, want 12", config.MaxConns)
	}
	if config.MinConns != 3 {
		t.Fatalf("MinConns = %d, want 3", config.MinConns)
	}
	if err := config.AfterConnect(context.Background(), new(pgx.Conn)); err != nil {
		t.Fatalf("run AfterConnect: %v", err)
	}
	if !registrationCalled {
		t.Fatal("expected pgvector registration to run")
	}
}

func TestBuildPoolConfigForMigrationSkipsPGVectorRegistration(t *testing.T) {
	config, err := buildPoolConfig(
		"postgres://test:test@localhost:5432/zhixu?sslmode=disable",
		12,
		3,
		nil,
	)
	if err != nil {
		t.Fatalf("build migration pool config: %v", err)
	}
	if config.AfterConnect != nil {
		t.Fatal("migration pool must not register extension-specific types")
	}
}

func TestBuildPoolConfigRedactsInvalidDatabaseURL(t *testing.T) {
	const secret = "super-secret"
	_, err := buildPoolConfig(
		"postgres://user:"+secret+"@localhost/database?pool_max_conns=invalid",
		0,
		0,
		nil,
	)
	if err == nil {
		t.Fatal("expected invalid database URL error")
	}
	if !strings.Contains(err.Error(), "parse database configuration") {
		t.Fatalf("error lacks parsing context: %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("error leaks database URL: %v", err)
	}
}

func TestConfigureAfterConnectComposesExistingHookBeforeRegistration(t *testing.T) {
	config := &pgxpool.Config{}
	var calls []string
	config.AfterConnect = func(context.Context, *pgx.Conn) error {
		calls = append(calls, "existing")
		return nil
	}

	err := configureAfterConnect(config, func(context.Context, *pgx.Conn) error {
		calls = append(calls, "pgvector")
		return nil
	})
	if err != nil {
		t.Fatalf("configure AfterConnect: %v", err)
	}
	if err := config.AfterConnect(context.Background(), new(pgx.Conn)); err != nil {
		t.Fatalf("run AfterConnect: %v", err)
	}
	if want := []string{"existing", "pgvector"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestConfigureAfterConnectPreservesExistingHookError(t *testing.T) {
	existingErr := errors.New("existing hook failed")
	config := &pgxpool.Config{
		AfterConnect: func(context.Context, *pgx.Conn) error {
			return existingErr
		},
	}
	registrationCalled := false

	if err := configureAfterConnect(config, func(context.Context, *pgx.Conn) error {
		registrationCalled = true
		return nil
	}); err != nil {
		t.Fatalf("configure AfterConnect: %v", err)
	}
	err := config.AfterConnect(context.Background(), new(pgx.Conn))
	if !errors.Is(err, existingErr) {
		t.Fatalf("AfterConnect error = %v, want existing error", err)
	}
	if registrationCalled {
		t.Fatal("pgvector registration ran after existing hook failure")
	}
}

func TestConfigureAfterConnectWrapsRegistrationError(t *testing.T) {
	registrationErr := errors.New("type lookup failed")
	config := &pgxpool.Config{}
	if err := configureAfterConnect(config, func(context.Context, *pgx.Conn) error {
		return registrationErr
	}); err != nil {
		t.Fatalf("configure AfterConnect: %v", err)
	}

	err := config.AfterConnect(context.Background(), new(pgx.Conn))
	if !errors.Is(err, registrationErr) {
		t.Fatalf("AfterConnect error = %v, want registration error", err)
	}
	if !strings.Contains(err.Error(), "register pgvector types") {
		t.Fatalf("AfterConnect error lacks context: %v", err)
	}
	if strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("AfterConnect error leaks database URL: %v", err)
	}
}

func TestConfigureAfterConnectRejectsNilDependencies(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		if err := configureAfterConnect(nil, func(context.Context, *pgx.Conn) error { return nil }); err == nil {
			t.Fatal("expected nil config error")
		}
	})

	t.Run("registrar", func(t *testing.T) {
		if err := configureAfterConnect(&pgxpool.Config{}, nil); err == nil {
			t.Fatal("expected nil registrar error")
		}
	})
}
