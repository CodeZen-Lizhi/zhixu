package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestNewGitSyncHandlerRequiresProductionDependencies(t *testing.T) {
	keyFile := writeGitSyncKeyFile(t)
	workspaces := documentHistoryWorkspaceRepositoryFake{}
	pool := apiConstructorPool(t)
	for _, test := range []struct {
		name       string
		pool       *platformpostgres.Pool
		workspaces gitcli.WorkspaceRepository
		keyFile    string
	}{
		{name: "database", workspaces: workspaces, keyFile: keyFile},
		{name: "workspace repository", pool: pool, keyFile: keyFile},
		{name: "key file", pool: pool, workspaces: workspaces},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, err := newGitSyncHandler(test.pool, test.workspaces, test.keyFile, time.Second)
			if err == nil || handler != nil {
				t.Fatalf("handler=%#v err=%v", handler, err)
			}
		})
	}
}

func TestNewGitSyncHandlerComposesProductionBoundaries(t *testing.T) {
	handler, err := newGitSyncHandler(
		apiConstructorPool(t), documentHistoryWorkspaceRepositoryFake{}, writeGitSyncKeyFile(t), time.Second,
	)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewGitSyncHandlerDoesNotExposeKeyPath(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "missing-git-sync.key")
	_, err := newGitSyncHandler(apiConstructorPool(t), documentHistoryWorkspaceRepositoryFake{}, keyFile, time.Second)
	if err == nil || bytes.Contains([]byte(err.Error()), []byte(keyFile)) {
		t.Fatalf("error leaked key path: %v", err)
	}
}

func writeGitSyncKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git-sync.key")
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
