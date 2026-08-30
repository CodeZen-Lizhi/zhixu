// Package testdb provides one lifecycle contract for PostgreSQL integration
// tests. It owns database provisioning while exposing the project's shared
// platform Pool so GORM, pgx, River, and Unit of Work use one physical pool.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	atlasmigrations "github.com/CodeZen-Lizhi/zhixu/atlas"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/docker/go-connections/nat"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// DefaultImage pins the PostgreSQL/pgvector major version used by fixtures.
	DefaultImage = "pgvector/pgvector:pg16"

	defaultUser      = "postgres"
	defaultPass      = "zhixu-test"
	defaultDB        = "zhixu"
	databasePrefix   = "zhixu_test_"
	providerTimeout  = 10 * time.Second
	startupTimeout   = 2 * time.Minute
	provisionTimeout = 4 * time.Minute
	cleanupTimeout   = 30 * time.Second
)

// AvailabilityPolicy controls how Require handles an unavailable container
// provider. Open always returns an error and never skips a test.
type AvailabilityPolicy uint8

const (
	// FailWhenUnavailable is the default and makes a missing provider fail the test.
	FailWhenUnavailable AvailabilityPolicy = iota
	// SkipWhenUnavailable explicitly allows a test to skip when Docker is unavailable.
	SkipWhenUnavailable
)

// MigrationFunc applies the project's complete schema and River migrations to
// an already reachable PostgreSQL pool. The Atlas runner replaced Goose
// through this callback without changing fixture provisioning or cleanup.
type MigrationFunc func(context.Context, *pgxpool.Pool) error

// Config controls one isolated integration database. ExternalAdminURL must
// identify a database whose role can create and drop temporary databases; the
// admin database itself is never migrated or removed.
type Config struct {
	ExternalAdminURL string
	Image            string
	MaxConns         int32
	MinConns         int32
	Migrate          MigrationFunc
	Availability     AvailabilityPolicy
	provisioned      func(*Fixture)
}

// Diagnostics contains only values safe to print in test output.
type Diagnostics struct {
	Mode        string
	Image       string
	Host        string
	Database    string
	ContainerID string
	Closed      bool
}

// Fixture owns one migrated database and the shared application pool derived
// from it. Close is idempotent and safe to call from multiple cleanup paths.
type Fixture struct {
	platform     *platformpostgres.Pool
	container    *postgrescontainer.PostgresContainer
	adminPool    *pgxpool.Pool
	databaseURL  string
	databaseName string
	createdDB    bool
	diagnostics  Diagnostics
	closeOnce    sync.Once
	closeErr     error
	closed       atomic.Bool
}

// Open creates an external-admin or Testcontainers-backed fixture and applies
// the configured migration. The default migration is the Atlas runner.
func Open(ctx context.Context, config Config) (*Fixture, error) {
	if ctx == nil {
		return nil, errors.New("test database context is nil")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if config.Migrate == nil {
		config.Migrate = AtlasMigration
	}
	if strings.TrimSpace(config.ExternalAdminURL) != "" {
		return openExternalAdmin(ctx, config)
	}
	return openContainer(ctx, config)
}

func validateConfig(config Config) error {
	if config.MaxConns < 0 || config.MinConns < 0 {
		return errors.New("test database pool limits must not be negative")
	}
	if config.MinConns > 0 && config.MaxConns > 0 && config.MinConns > config.MaxConns {
		return errors.New("test database minimum connections exceed maximum connections")
	}
	if config.Availability != FailWhenUnavailable && config.Availability != SkipWhenUnavailable {
		return errors.New("test database availability policy is invalid")
	}
	return nil
}

// Require opens a fixture for a test and registers its cleanup. Docker
// provider failures only skip when SkipWhenUnavailable is explicitly selected.
func Require(t testing.TB, config Config) *Fixture {
	t.Helper()
	if err := validateConfig(config); err != nil {
		t.Fatalf("invalid PostgreSQL test fixture configuration: %v", err)
		return nil
	}

	if strings.TrimSpace(config.ExternalAdminURL) == "" {
		ctx, cancel := context.WithTimeout(t.Context(), providerTimeout)
		err := containerProviderHealth(ctx)
		cancel()
		if err != nil {
			message := "Docker provider is unavailable; start Docker or explicitly select testdb.SkipWhenUnavailable"
			if config.Availability == SkipWhenUnavailable {
				t.Skip(message)
				return nil
			}
			t.Fatalf("%s", message)
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	fixture, err := Open(ctx, config)
	cancel()
	if err != nil {
		t.Fatalf("open PostgreSQL test fixture: %v", err)
		return nil
	}
	t.Cleanup(func() {
		if err := fixture.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL test fixture: %v", err)
		}
	})
	return fixture
}

// AtlasMigration runs the production migration entry point against a
// migration-only pgx pool. It is the default fixture migration callback.
func AtlasMigration(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("test database migration pool is nil")
	}
	dir, err := platformmigration.LoadAtlasDir(atlasmigrations.MigrationDir())
	if err != nil {
		return err
	}
	runner, err := platformmigration.NewAtlasRunner(pool, dir)
	if err != nil {
		return err
	}
	return runner.Up(ctx)
}

func openExternalAdmin(ctx context.Context, config Config) (*Fixture, error) {
	adminURL, err := parseAdminURL(strings.TrimSpace(config.ExternalAdminURL))
	if err != nil {
		return nil, err
	}
	adminPool, err := openAdminPool(ctx, adminURL.String())
	if err != nil {
		return nil, safeError("open external PostgreSQL admin pool", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		return nil, safeError("ping external PostgreSQL admin pool", err)
	}

	databaseName, err := randomDatabaseName()
	if err != nil {
		adminPool.Close()
		return nil, safeError("generate temporary PostgreSQL database name", err)
	}
	databaseURL := databaseURLForName(adminURL, databaseName)
	host, database := describeDatabaseURL(databaseURL)
	fixture := &Fixture{
		adminPool:    adminPool,
		databaseURL:  databaseURL,
		databaseName: databaseName,
		createdDB:    true,
		diagnostics:  Diagnostics{Mode: "external-admin", Host: host, Database: database},
	}
	// CREATE DATABASE is not transactional. Treat an error as an unknown
	// outcome and conservatively attempt to drop only the generated name.
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		return nil, cleanupOpenFailure(fixture, safeError("create temporary PostgreSQL database", err))
	}
	if config.provisioned != nil {
		config.provisioned(fixture)
	}
	platform, err := openMigratedPool(ctx, databaseURL, config)
	if err != nil {
		return nil, cleanupOpenFailure(fixture, err)
	}
	fixture.platform = platform
	return fixture, nil
}

func openContainer(ctx context.Context, config Config) (*Fixture, error) {
	image := strings.TrimSpace(config.Image)
	if image == "" {
		image = DefaultImage
	}
	container, err := postgrescontainer.Run(
		ctx,
		image,
		postgrescontainer.WithDatabase(defaultDB),
		postgrescontainer.WithUsername(defaultUser),
		postgrescontainer.WithPassword(defaultPass),
		postgrescontainer.BasicWaitStrategies(),
		testcontainers.WithAdditionalWaitStrategyAndDeadline(startupTimeout, postgresSQLWaitStrategy()),
	)
	if err != nil {
		fixture := &Fixture{container: container, diagnostics: Diagnostics{Mode: "container", Image: image}}
		if container != nil {
			return nil, cleanupOpenFailure(fixture, safeError("start PostgreSQL test container", err))
		}
		return nil, safeError("start PostgreSQL test container", err)
	}

	fixture := &Fixture{
		container: container,
		diagnostics: Diagnostics{
			Mode:        "container",
			Image:       image,
			ContainerID: shortContainerID(container.GetContainerID()),
		},
	}
	databaseURL, err := container.ConnectionString(ctx, "sslmode=disable", "application_name=zhixu-integration")
	if err != nil {
		return nil, cleanupOpenFailure(fixture, safeError("resolve PostgreSQL test container connection", err))
	}
	fixture.databaseURL = databaseURL
	fixture.diagnostics.Host, fixture.diagnostics.Database = describeDatabaseURL(databaseURL)
	if config.provisioned != nil {
		config.provisioned(fixture)
	}

	platform, err := openMigratedPool(ctx, databaseURL, config)
	if err != nil {
		return nil, cleanupOpenFailure(fixture, err)
	}
	fixture.platform = platform
	return fixture, nil
}

func openAdminPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid external PostgreSQL admin connection settings")
	}
	config.MaxConns = 1
	config.MinConns = 0
	return pgxpool.NewWithConfig(ctx, config)
}

func openMigratedPool(ctx context.Context, databaseURL string, config Config) (*platformpostgres.Pool, error) {
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, config.MaxConns, config.MinConns)
	if err != nil {
		return nil, safeError("open test database migration pool", err)
	}
	if err := migrationPool.Ping(ctx); err != nil {
		migrationPool.Close()
		return nil, safeError("ping test database migration pool", err)
	}
	if err := config.Migrate(ctx, migrationPool.DB()); err != nil {
		migrationPool.Close()
		return nil, safeError("migrate test database", err)
	}
	migrationPool.Close()

	platform, err := platformpostgres.Open(ctx, databaseURL, config.MaxConns, config.MinConns)
	if err != nil {
		return nil, safeError("open shared test database pool", err)
	}
	if err := platform.Ping(ctx); err != nil {
		platform.Close()
		return nil, safeError("ping shared test database pool", err)
	}
	return platform, nil
}

// Pool returns the shared platform pool. The caller owns no additional pool;
// Require registers cleanup automatically and Open callers must call Close.
func (f *Fixture) Pool() *platformpostgres.Pool {
	if f == nil {
		return nil
	}
	return f.platform
}

// Diagnostics returns a printable snapshot without credentials or a full DSN.
func (f *Fixture) Diagnostics() Diagnostics {
	if f == nil {
		return Diagnostics{}
	}
	diagnostics := f.diagnostics
	diagnostics.Closed = f.closed.Load()
	return diagnostics
}

// Close releases the shared pool, drops a generated external database, and
// terminates a container. It may be called more than once.
func (f *Fixture) Close(_ context.Context) error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		var cleanupErrors []error
		if f.platform != nil {
			f.platform.Close()
		}
		if f.createdDB && f.adminPool != nil {
			statement := "DROP DATABASE " + pgx.Identifier{f.databaseName}.Sanitize() + " WITH (FORCE)"
			if _, err := f.adminPool.Exec(cleanupCtx, statement); err != nil {
				cleanupErrors = append(cleanupErrors, safeError("drop temporary PostgreSQL database", err))
			}
		}
		if f.adminPool != nil {
			f.adminPool.Close()
		}
		if f.container != nil {
			if err := f.container.Terminate(cleanupCtx); err != nil {
				cleanupErrors = append(cleanupErrors, safeError("terminate PostgreSQL test container", err))
			}
		}
		f.closeErr = errors.Join(cleanupErrors...)
		f.closed.Store(true)
	})
	return f.closeErr
}

func containerProviderHealth(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("Docker provider health check panicked")
		}
	}()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err
	}
	return provider.Health(ctx)
}

func postgresSQLWaitStrategy() wait.Strategy {
	return wait.ForSQL("5432/tcp", "pgx", func(host string, port nat.Port) string {
		endpoint := net.JoinHostPort(host, port.Port())
		databaseURL := &url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(defaultUser, defaultPass),
			Host:   endpoint,
			Path:   "/" + defaultDB,
		}
		query := databaseURL.Query()
		query.Set("sslmode", "disable")
		databaseURL.RawQuery = query.Encode()
		return databaseURL.String()
	}).WithQuery("SELECT 1").WithStartupTimeout(startupTimeout)
}

func parseAdminURL(raw string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return nil, errors.New("external PostgreSQL admin URL is invalid")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return nil, errors.New("external PostgreSQL admin URL must use postgres or postgresql scheme")
	}
	if parsed.Host == "" {
		return nil, errors.New("external PostgreSQL admin URL must include a host")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" || strings.Contains(database, "/") {
		return nil, errors.New("external PostgreSQL admin URL must identify one admin database")
	}
	query := parsed.Query()
	query.Del("database")
	query.Del("dbname")
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed, nil
}

func databaseURLForName(adminURL *url.URL, databaseName string) string {
	target := *adminURL
	target.Path = "/" + databaseName
	target.RawPath = ""
	target.Fragment = ""
	query := target.Query()
	query.Del("database")
	query.Del("dbname")
	target.RawQuery = query.Encode()
	return target.String()
}

func randomDatabaseName() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return databasePrefix + hex.EncodeToString(suffix[:]), nil
}

func cleanupOpenFailure(fixture *Fixture, primary error) error {
	cleanupErr := fixture.Close(context.Background())
	if cleanupErr == nil {
		return primary
	}
	return errors.Join(primary, cleanupErr)
}

func shortContainerID(containerID string) string {
	if len(containerID) <= 12 {
		return containerID
	}
	return containerID[:12]
}

func describeDatabaseURL(raw string) (string, string) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid>", "<invalid>"
	}
	return parsed.Host, strings.TrimPrefix(parsed.Path, "/")
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "<invalid-database-url>"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if parsed.User != nil {
		parsed.User = url.User(parsed.User.Username())
	}
	return parsed.String()
}

type operationError struct {
	operation string
	cause     error
}

func safeError(operation string, cause error) error {
	if cause == nil {
		return nil
	}
	return &operationError{operation: operation, cause: cause}
}

func (e *operationError) Error() string {
	return e.operation
}

func (e *operationError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func (e *operationError) Format(state fmt.State, verb rune) {
	_, _ = fmt.Fprint(state, e.operation)
}
