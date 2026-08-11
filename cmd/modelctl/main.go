// Command modelctl performs managed model-settings activation recovery.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modeldomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

const (
	defaultCommandTimeout        = 2 * time.Minute
	committedQueuePausedExitCode = 20
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := runCommand(ctx, os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "modelctl: %s\n", stableErrorCode(err))
		os.Exit(modelctlExitCode(err))
	}
}

func runCommand(ctx context.Context, arguments []string, stdout io.Writer) error {
	if ctx == nil || stdout == nil {
		return stateError(errors.New("modelctl process boundary is unavailable"))
	}
	command, err := parseCommand(arguments)
	if err != nil {
		return err
	}
	if isLegacyRolloutCommand(command.name) {
		return legacyRolloutError()
	}
	executionTimeout := defaultCommandTimeout
	if command.name == commandWaitQuiesced || command.name == commandWaitPrepared {
		executionTimeout = command.waitTimeout + 10*time.Second
	}
	executionCtx, cancelExecution := context.WithTimeout(ctx, executionTimeout)
	defer cancelExecution()
	cfg, err := config.LoadMigration("")
	if err != nil {
		return err
	}
	if cfg.ModelSettingsMode != config.ModelSettingsModeManaged {
		return foundation.NewError(foundation.ErrorInvalidInput, "MODELCTL_MANAGED_MODE_REQUIRED", false, errors.New("managed model settings mode is required"))
	}
	databaseURL, err := cfg.DatabaseConnectionString()
	if err != nil {
		return err
	}
	database, err := platformpostgres.Open(executionCtx, databaseURL, cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
	if err != nil {
		return err
	}
	defer database.Close()
	pingCtx, cancelPing := context.WithTimeout(executionCtx, cfg.DatabasePingTimeout)
	err = database.Ping(pingCtx)
	cancelPing()
	if err != nil {
		return err
	}
	bootstrap, bootstrapErr := modelruntime.Bootstrap(executionCtx, database.DB(), cfg)
	if bootstrap.Service == nil || bootstrap.Repository == nil {
		if bootstrapErr != nil {
			return bootstrapErr
		}
		return stateError(errors.New("managed model settings bootstrap is unavailable"))
	}
	control := &controller{
		activations: bootstrap.Service,
		stdout:      stdout,
	}
	return control.execute(executionCtx, command)
}

func stableErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code
	}
	if errors.Is(err, context.Canceled) {
		return "MODELCTL_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "MODELCTL_TIMEOUT"
	}
	return "MODELCTL_FAILED"
}

func modelctlExitCode(err error) int {
	if stableErrorCode(err) == modeldomain.ErrorCodeCommittedQueuePaused {
		return committedQueuePausedExitCode
	}
	return 1
}
