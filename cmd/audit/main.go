// Command audit reads bounded Audit summaries for an operator authorized by
// database credentials. It is not an HTTP API or an application authentication
// bypass, and never runs migrations or appends Audit events.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

const (
	defaultLimit   = 50
	defaultTimeout = 10 * time.Second
	maxTimeout     = time.Minute
	usage          = `Usage: audit (--workspace UUID | --global) [options]

Read one page of safe Audit summaries using operator database credentials.
--global reads only events with no Workspace; it does not read all Workspaces.

  --workspace UUID    Read exactly one Workspace
  --global            Read only global events (workspace_id IS NULL)
  --limit N           Page size, 1..200 (default 50)
  --before TIME       Continue before a returned RFC3339 timestamp
  --before-id UUID    Required with --before; use next_cursor.before_id
  --timeout DURATION  Total database timeout, positive and at most 1m (default 10s)
  --config PATH       Optional YAML configuration; uses the non-API profile
  --help              Show this help without connecting to the database

Output is JSON ordered by occurred_at DESC, id DESC. Continue with the same
scope and both next_cursor fields. A full final page can yield an empty next page.
Database configuration uses the existing ZHIXU_DATABASE_* variables or YAML.
Use protected credentials with SELECT on ops.audit_event; do not pass secrets
in arguments. This command does not query other domain-specific history stores.
`
)

type options struct {
	configPath string
	query      domain.ListQuery
	timeout    time.Duration
}

// auditReader intentionally excludes every write operation on the Store.
type auditReader interface {
	List(context.Context, domain.ListQuery) ([]domain.Event, error)
}

type readerOpener func(context.Context, string) (auditReader, func(), error)

type commandFailure struct {
	Code    string `json:"error_code"`
	Message string `json:"message"`
}

func (failure *commandFailure) Error() string { return failure.Code }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, openAuditReader)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer, open readerOpener) int {
	settings, err := parseOptions(arguments)
	if errors.Is(err, flag.ErrHelp) {
		if _, err := io.WriteString(stdout, usage); err != nil {
			writeFailure(stderr, outputFailure())
			return 1
		}
		return 0
	}
	if err != nil {
		writeFailure(stderr, err)
		return 2
	}
	if ctx == nil || open == nil {
		writeFailure(stderr, errors.New("audit query dependency unavailable"))
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, settings.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		writeFailure(stderr, err)
		return 1
	}
	reader, closeReader, err := open(ctx, settings.configPath)
	if closeReader != nil {
		defer closeReader()
	}
	if err != nil || reader == nil || closeReader == nil {
		writeFailure(stderr, err)
		return 1
	}
	page, err := readPage(ctx, reader, settings.query)
	if err != nil {
		writeFailure(stderr, err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(page); err != nil {
		writeFailure(stderr, outputFailure())
		return 1
	}
	return 0
}

func parseOptions(arguments []string) (options, error) {
	var settings options
	var workspace, before, beforeID string
	var global bool
	flags := flag.NewFlagSet("audit", flag.ContinueOnError)
	// The flag package includes raw arguments in parse errors. Only fixed
	// messages below may reach stderr, including for invalid flags and values.
	flags.SetOutput(io.Discard)
	flags.StringVar(&settings.configPath, "config", "", "optional YAML configuration")
	flags.StringVar(&workspace, "workspace", "", "Workspace UUID")
	flags.BoolVar(&global, "global", false, "global events only")
	flags.IntVar(&settings.query.Limit, "limit", defaultLimit, "page size")
	flags.StringVar(&before, "before", "", "cursor timestamp")
	flags.StringVar(&beforeID, "before-id", "", "cursor event UUID")
	flags.DurationVar(&settings.timeout, "timeout", defaultTimeout, "database timeout")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, flag.ErrHelp
		}
		return options{}, argumentsFailure("Invalid arguments; use --help.")
	}
	if flags.NArg() != 0 {
		return options{}, argumentsFailure("Positional arguments are not supported; use --help.")
	}
	if global && workspace != "" || !global && workspace == "" {
		return options{}, argumentsFailure("Select exactly one of --workspace UUID or --global.")
	}
	if workspace != "" {
		id, err := foundation.ParseID(workspace)
		if err != nil {
			return options{}, argumentsFailure("--workspace must be a UUID.")
		}
		settings.query.WorkspaceID = &id
	}
	if settings.query.Limit < 1 || settings.query.Limit > domain.MaxListLimit {
		return options{}, argumentsFailure("--limit must be between 1 and 200.")
	}
	if settings.timeout <= 0 || settings.timeout > maxTimeout {
		return options{}, argumentsFailure("--timeout must be positive and at most 1m.")
	}
	if (before == "") != (beforeID == "") {
		return options{}, argumentsFailure("--before and --before-id must be supplied together.")
	}
	if before != "" {
		at, err := time.Parse(time.RFC3339Nano, before)
		if err != nil || at.IsZero() || at.Nanosecond()%1000 != 0 {
			return options{}, argumentsFailure("--before must be an RFC3339 timestamp with at most microsecond precision.")
		}
		id, err := foundation.ParseID(beforeID)
		if err != nil {
			return options{}, argumentsFailure("--before-id must be a UUID.")
		}
		settings.query.Before, settings.query.BeforeID = at.UTC(), id
	}
	return settings, nil
}

func openAuditReader(ctx context.Context, configPath string) (auditReader, func(), error) {
	cfg, err := config.LoadMigration(configPath)
	if err != nil {
		return nil, nil, configFailure()
	}
	databaseURL, err := cfg.DatabaseConnectionString()
	if err != nil {
		return nil, nil, configFailure()
	}
	databaseURL, err = readOnlyDatabaseURL(databaseURL)
	if err != nil {
		return nil, nil, err
	}
	database, err := platformpostgres.Open(ctx, databaseURL, cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
	if err != nil {
		return nil, nil, err
	}
	pingContext, cancelPing := context.WithTimeout(ctx, cfg.DatabasePingTimeout)
	err = database.Ping(pingContext)
	cancelPing()
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	store, err := auditpostgres.NewGORMStore(database)
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	return store, database.Close, nil
}

func readOnlyDatabaseURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", configFailure()
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", configFailure()
	}
	// A startup parameter applies to every connection in the shared Pool, unlike
	// issuing SET on an arbitrary connection after opening a pool.
	query.Set("default_transaction_read_only", "on")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func argumentsFailure(message string) error {
	return &commandFailure{Code: "AUDIT_QUERY_ARGUMENTS_INVALID", Message: message}
}

func configFailure() error {
	return &commandFailure{Code: "AUDIT_QUERY_CONFIG_INVALID", Message: "Audit database configuration is invalid; check the protected YAML or environment configuration."}
}

func outputFailure() error {
	return &commandFailure{Code: "AUDIT_QUERY_OUTPUT_FAILED", Message: "Audit output could not be written."}
}

func writeFailure(writer io.Writer, err error) {
	failure := &commandFailure{Code: "AUDIT_QUERY_UNAVAILABLE", Message: "Audit query failed; check database availability, schema and SELECT permission."}
	var command *commandFailure
	var classified *foundation.Error
	switch {
	case errors.As(err, &command):
		failure = command
	case errors.Is(err, context.Canceled):
		failure = &commandFailure{Code: "AUDIT_QUERY_CANCELLED", Message: "Audit query was cancelled."}
	case errors.Is(err, context.DeadlineExceeded):
		failure = &commandFailure{Code: "AUDIT_QUERY_TIMEOUT", Message: "Audit query exceeded its timeout."}
	case errors.As(err, &classified) && classified.Code == domain.ErrorCodeCorrupt:
		failure = &commandFailure{Code: domain.ErrorCodeCorrupt, Message: "Audit records failed validation; no page was returned."}
	}
	// Neither raw errors nor configuration objects are serialized here.
	_ = json.NewEncoder(writer).Encode(failure)
}
