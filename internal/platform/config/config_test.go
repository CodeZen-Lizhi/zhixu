package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadWithLookupYAMLThenEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http_addr: 127.0.0.1:9090\ndatabase_url: yaml-value\nhealth_interval: 1m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) (string, bool) {
		if key == "ZHIXU_HTTP_ADDR" {
			return "127.0.0.1:9191", true
		}
		if key == "ZHIXU_DATABASE_URL" {
			return "env-value", true
		}
		return "", false
	}
	cfg, err := LoadWithLookup(path, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9191" || cfg.DatabaseURL != "env-value" {
		t.Fatalf("environment should override YAML: %+v", cfg)
	}
	if cfg.HealthInterval != time.Minute {
		t.Fatalf("YAML duration not loaded: %s", cfg.HealthInterval)
	}
}

func TestLoadWithLookupRejectsInvalidYAMLDuration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("health_interval: not-a-duration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithLookup(path, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected invalid YAML duration error")
	}
}

func TestLoadWithLookupRejectsUnknownYAMLField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("unexpected: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithLookup(path, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected unknown YAML field error")
	}
}

func TestLoadWithLookupRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()
	_, err := LoadWithLookup("", func(key string) (string, bool) {
		if key == "ZHIXU_HEALTH_INTERVAL" {
			return "not-a-duration", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestValidateDatabaseRequiresExplicitURL(t *testing.T) {
	t.Parallel()
	if err := Defaults().ValidateDatabase(); err == nil {
		t.Fatal("expected missing database URL error")
	}
}

func TestDatabaseConnectionStringEscapesPassword(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.DatabaseHost = "db"
	cfg.DatabaseName = "zhixu"
	cfg.DatabaseUser = "app"
	cfg.DatabasePassword = "p@ss/word#1"
	got, err := cfg.DatabaseConnectionString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://app:p%40ss%2Fword%231@db:5432/zhixu?sslmode=disable" {
		t.Fatalf("unexpected connection string: %s", got)
	}
}

func TestDatabaseConnectionStringRejectsMalformedURL(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.DatabaseURL = "postgres://%zz"
	if _, err := cfg.DatabaseConnectionString(); err == nil {
		t.Fatal("expected malformed database URL error")
	}
}
