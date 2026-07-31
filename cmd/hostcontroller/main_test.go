package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reconcileSequence struct {
	errors []error
	calls  int
}

func (sequence *reconcileSequence) Reconcile(context.Context) error {
	sequence.calls++
	if len(sequence.errors) == 0 {
		return nil
	}
	err := sequence.errors[0]
	sequence.errors = sequence.errors[1:]
	return err
}

func TestRunReconcileLoopRetriesUntilRuntimeIsRestored(t *testing.T) {
	sequence := &reconcileSequence{errors: []error{errors.New("docker unavailable"), errors.New("database unavailable")}}
	reports := 0
	runReconcileLoop(context.Background(), sequence, time.Second, time.Millisecond, func(error) { reports++ })
	if sequence.calls != 3 || reports != 2 {
		t.Fatalf("calls=%d reports=%d", sequence.calls, reports)
	}
}

func TestParseConfigRequiresExactLoopbackAndProtectedOverrideScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	arguments := []string{
		"--addr", "127.0.0.1:8080",
		"--assets", filepath.Join(root, "state", "bundle", "web"),
		"--state-dir", filepath.Join(root, "state"),
		"--compose-file", filepath.Join(root, "compose.yml"),
		"--env-file", filepath.Join(root, ".env"),
		"--grant-override", filepath.Join(root, "state", "grant.yml"),
		"--bootstrap-token-fd", "9",
		"--database-url-fd", "8",
	}
	config, err := parseConfig(arguments)
	if err != nil {
		t.Fatalf("parseConfig() error: %v", err)
	}
	if config.address != "127.0.0.1:8080" || config.bootstrapTokenFD != 9 || config.databaseURLFD != 8 {
		t.Fatalf("unexpected config: %+v", config)
	}

	public := append([]string(nil), arguments...)
	public[1] = "0.0.0.0:8080"
	if _, err := parseConfig(public); err == nil {
		t.Fatal("parseConfig() accepted a public listener")
	}
	outside := append([]string(nil), arguments...)
	outside[11] = filepath.Join(root, "grant.yml")
	if _, err := parseConfig(outside); err == nil {
		t.Fatal("parseConfig() accepted an override outside state")
	}
	sharedDescriptor := append([]string(nil), arguments...)
	sharedDescriptor[15] = "9"
	if _, err := parseConfig(sharedDescriptor); err == nil {
		t.Fatal("parseConfig() accepted one descriptor for two credentials")
	}
}

func TestProtectedInputsRejectPermissiveFilesAndAssetAliases(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	assets := filepath.Join(state, "bundle", "web")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatalf("create assets: %v", err)
	}
	environment := filepath.Join(state, ".env")
	if err := os.WriteFile(environment, []byte("A=B\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := validateProtectedInputFile(environment); err != nil {
		t.Fatalf("protected env rejected: %v", err)
	}
	if err := os.Chmod(environment, 0o644); err != nil {
		t.Fatalf("chmod env: %v", err)
	}
	if err := validateProtectedInputFile(environment); err == nil {
		t.Fatal("permissive env accepted")
	}
	if err := validateAssetsDirectory(assets, state); err != nil {
		t.Fatalf("assets rejected: %v", err)
	}
	alias := filepath.Join(state, "bundle", "web-alias")
	if err := os.Symlink(assets, alias); err != nil {
		t.Fatalf("symlink assets: %v", err)
	}
	if err := validateAssetsDirectory(alias, state); err == nil {
		t.Fatal("asset symlink accepted")
	}
}

func TestReadBootstrapTokenUsesInheritedDescriptor(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	token := "bootstrap_token_abcdefghijklmnopqrstuvwxyz0123456789"
	if _, err := writer.WriteString(token + "\n"); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	value, err := readBootstrapToken(int(reader.Fd()))
	if err != nil {
		t.Fatalf("readBootstrapToken() error: %v", err)
	}
	if value != token {
		t.Fatalf("token=%q", value)
	}
}

func TestReadBootstrapTokenRejectsWhitespace(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	_, _ = writer.WriteString(strings.Repeat("a", 32) + " extra\n")
	_ = writer.Close()
	if _, err := readBootstrapToken(int(reader.Fd())); err == nil {
		t.Fatal("readBootstrapToken() accepted whitespace")
	}
}

func TestReadDatabaseURLUsesInheritedDescriptor(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	databaseURL := "postgres://user:p%40ss@127.0.0.1:5432/zhixu?sslmode=disable"
	if _, err := writer.WriteString(databaseURL + "\n"); err != nil {
		t.Fatalf("write database URL: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	value, err := readDatabaseURL(int(reader.Fd()))
	if err != nil {
		t.Fatalf("readDatabaseURL() error: %v", err)
	}
	if value != databaseURL {
		t.Fatalf("database URL=%q", value)
	}
}

func TestReadDatabaseURLRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"mysql://user:secret@localhost/db", " postgres://user:secret@localhost/db", "postgres://user:secret@localhost/db\nextra"} {
		value := value
		t.Run(value, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe(): %v", err)
			}
			_, _ = writer.WriteString(value)
			_ = writer.Close()
			if _, err := readDatabaseURL(int(reader.Fd())); err == nil {
				t.Fatal("readDatabaseURL() accepted an unsafe value")
			}
		})
	}
}
