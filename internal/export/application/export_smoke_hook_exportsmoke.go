//go:build exportsmoke

package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	exportSmokeBarrierDirectoryEnv = "ZHIXU_EXPORT_SMOKE_BARRIER_DIR"
	exportSmokeBarrierExportIDEnv  = "ZHIXU_EXPORT_SMOKE_BARRIER_EXPORT_ID"
	exportSmokeBarrierPollInterval = 25 * time.Millisecond
)

// AttachmentExportSmokeAfterScan blocks one explicitly targeted attachment job
// after its first scan and before ZIP entries are read. It only exists in the
// exportsmoke build used by the disposable browser smoke fixture.
func AttachmentExportSmokeAfterScan(ctx context.Context, exportID foundation.ID) error {
	barrierDirectory := strings.TrimSpace(os.Getenv(exportSmokeBarrierDirectoryEnv))
	targetID := strings.TrimSpace(os.Getenv(exportSmokeBarrierExportIDEnv))
	if barrierDirectory == "" || targetID == "" || targetID != string(exportID) {
		return nil
	}
	if _, err := foundation.ParseID(targetID); err != nil {
		return fmt.Errorf("export smoke barrier export ID is invalid: %w", err)
	}
	info, err := os.Stat(barrierDirectory)
	if err != nil || !info.IsDir() {
		return errors.New("export smoke barrier directory is unavailable")
	}
	reached := filepath.Join(barrierDirectory, targetID+".reached")
	release := filepath.Join(barrierDirectory, targetID+".release")
	file, err := os.OpenFile(reached, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create export smoke reached marker: %w", err)
	}
	if _, err := file.WriteString("reached\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write export smoke reached marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close export smoke reached marker: %w", err)
	}

	ticker := time.NewTicker(exportSmokeBarrierPollInterval)
	defer ticker.Stop()
	for {
		if info, err := os.Stat(release); err == nil && info.Mode().IsRegular() {
			return nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read export smoke release marker: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
