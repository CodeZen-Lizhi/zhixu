// Command runtimewait keeps a runtime container alive until PostgreSQL is ready.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

const defaultRetryInterval = 500 * time.Millisecond

type waitConfiguration struct {
	databaseURL string
	maxConns    int32
	minConns    int32
	pingTimeout time.Duration
}

type processProfile string

const (
	profileAPI    processProfile = "api"
	profileWorker processProfile = "worker"
)

type database interface {
	Ping(context.Context) error
	Close()
}

type dependencies struct {
	load          func(processProfile) (waitConfiguration, error)
	open          func(context.Context, waitConfiguration) (database, error)
	replace       func(string, []string, []string) error
	environment   func() []string
	retryInterval time.Duration
}

type codedError struct {
	code  string
	cause error
}

func (err *codedError) Error() string { return err.code }

func (err *codedError) Unwrap() error { return err.cause }

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stderr, defaultDependencies()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "runtime-wait: %s\n", stableErrorCode(err))
		os.Exit(1)
	}
}

func defaultDependencies() dependencies {
	return dependencies{
		load: loadConfiguration,
		open: func(ctx context.Context, cfg waitConfiguration) (database, error) {
			database, err := postgres.Open(ctx, cfg.databaseURL, cfg.maxConns, cfg.minConns)
			if err != nil {
				return nil, err
			}
			return database, nil
		},
		replace:       syscall.Exec,
		environment:   os.Environ,
		retryInterval: defaultRetryInterval,
	}
}

func loadConfiguration(profile processProfile) (waitConfiguration, error) {
	var (
		cfg config.Config
		err error
	)
	switch profile {
	case profileAPI:
		cfg, err = config.Load("")
	case profileWorker:
		cfg, err = config.LoadWorker("")
	default:
		return waitConfiguration{}, newCodedError("RUNTIME_WAIT_PROFILE_INVALID", errors.New("runtime profile is invalid"))
	}
	if err != nil {
		return waitConfiguration{}, err
	}
	databaseURL, err := cfg.DatabaseConnectionString()
	if err != nil {
		return waitConfiguration{}, err
	}
	return waitConfiguration{
		databaseURL: databaseURL,
		maxConns:    cfg.DatabaseMaxConns,
		minConns:    cfg.DatabaseMinConns,
		pingTimeout: cfg.DatabasePingTimeout,
	}, nil
}

func run(ctx context.Context, arguments []string, stderr io.Writer, deps dependencies) error {
	if ctx == nil || stderr == nil || deps.load == nil || deps.open == nil || deps.replace == nil || deps.environment == nil || deps.retryInterval <= 0 {
		return newCodedError("RUNTIME_WAIT_STATE_INVALID", errors.New("runtime wait dependencies are invalid"))
	}
	profile, command, err := parseCommand(arguments)
	if err != nil {
		return err
	}
	cfg, err := deps.load(profile)
	if err != nil {
		return newCodedError("RUNTIME_WAIT_CONFIGURATION_INVALID", err)
	}
	database, err := deps.open(ctx, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return newCodedError("RUNTIME_WAIT_CONFIGURATION_INVALID", err)
	}
	defer database.Close()

	waitingLogged := false
	for {
		pingCtx, cancelPing := context.WithTimeout(ctx, cfg.pingTimeout)
		pingErr := database.Ping(pingCtx)
		cancelPing()
		if pingErr == nil {
			if err := deps.replace(command[0], command, deps.environment()); err != nil {
				return newCodedError("RUNTIME_WAIT_EXEC_FAILED", err)
			}
			return nil
		}
		if !waitingLogged {
			_, _ = fmt.Fprintln(stderr, "runtime-wait: DATABASE_UNAVAILABLE; waiting for PostgreSQL")
			waitingLogged = true
		}
		timer := time.NewTimer(deps.retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func parseCommand(arguments []string) (processProfile, []string, error) {
	if len(arguments) < 4 || arguments[0] != "--profile" || !filepath.IsAbs(arguments[3]) || arguments[2] != "--" {
		return "", nil, newCodedError("RUNTIME_WAIT_ARGUMENTS_INVALID", errors.New("profile and target command are required"))
	}
	profile := processProfile(arguments[1])
	if profile != profileAPI && profile != profileWorker {
		return "", nil, newCodedError("RUNTIME_WAIT_PROFILE_INVALID", errors.New("runtime profile is invalid"))
	}
	return profile, append([]string(nil), arguments[3:]...), nil
}

func newCodedError(code string, cause error) error {
	return &codedError{code: code, cause: cause}
}

func stableErrorCode(err error) string {
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	if errors.Is(err, context.Canceled) {
		return "RUNTIME_WAIT_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "RUNTIME_WAIT_TIMEOUT"
	}
	return "RUNTIME_WAIT_FAILED"
}
