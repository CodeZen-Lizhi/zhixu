//go:build exportsmoke

package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestAttachmentExportSmokeAfterScanWaitsForTargetedRelease(t *testing.T) {
	directory := t.TempDir()
	target := foundation.ID("10000000-0000-4000-8000-000000000001")
	t.Setenv(exportSmokeBarrierDirectoryEnv, directory)
	t.Setenv(exportSmokeBarrierExportIDEnv, string(target))

	result := make(chan error, 1)
	go func() { result <- AttachmentExportSmokeAfterScan(context.Background(), target) }()
	reached := filepath.Join(directory, string(target)+".reached")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(reached); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("reached marker err=%v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(directory, string(target)+".release"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("barrier did not release")
	}
}

func TestAttachmentExportSmokeAfterScanIgnoresOtherExport(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(exportSmokeBarrierDirectoryEnv, directory)
	t.Setenv(exportSmokeBarrierExportIDEnv, "10000000-0000-4000-8000-000000000001")
	if err := AttachmentExportSmokeAfterScan(context.Background(), foundation.ID("20000000-0000-4000-8000-000000000002")); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("other export wrote barrier markers: entries=%v err=%v", entries, err)
	}
}
