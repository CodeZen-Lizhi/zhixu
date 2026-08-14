package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type roleStatusRow struct {
	values []any
	err    error
}

func (row roleStatusRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	for index := range dest {
		switch value := dest[index].(type) {
		case *bool:
			*value = row.values[index].(bool)
		default:
			return errors.New("unexpected scan destination")
		}
	}
	return nil
}

type roleStatusDB struct{ row pgx.Row }

func (database roleStatusDB) QueryRow(context.Context, string, ...any) pgx.Row { return database.row }

func TestSafeComponentRejectsSQLAndURLDelimiters(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"postgres", "5432", "zhixu-db_1", "bad;drop", "host/name", "host:5432", ""} {
		if value == "postgres" || value == "5432" || value == "zhixu-db_1" {
			if !safeComponent(value) {
				t.Fatalf("safeComponent(%q) rejected valid component", value)
			}
			continue
		}
		if safeComponent(value) {
			t.Fatalf("safeComponent(%q) accepted unsafe component", value)
		}
	}
}

func TestRequiredAndOptionalEnvironmentValues(t *testing.T) {
	t.Parallel()
	lookup := func(key string) (string, bool) {
		switch key {
		case "present":
			return " value ", true
		case "blank":
			return "  ", true
		default:
			return "", false
		}
	}
	value, err := required(lookup, "present")
	if err != nil || value != " value " {
		t.Fatalf("required present = %q, %v", value, err)
	}
	if _, err := required(lookup, "blank"); err == nil {
		t.Fatal("required accepted blank value")
	}
	if value, used := optional(lookup, "blank", "fallback"); value != "fallback" || used {
		t.Fatalf("optional blank = %q, used=%t", value, used)
	}
	if value, used := optional(lookup, "missing", "fallback"); value != "fallback" || used {
		t.Fatalf("optional missing = %q, used=%t", value, used)
	}
}

func TestParseRuntimePassword(t *testing.T) {
	t.Parallel()
	valid := strings.Repeat("a1", 32)
	for _, input := range []string{valid, valid + "\n"} {
		if got, ok := parseRuntimePassword([]byte(input)); !ok || got != valid {
			t.Fatalf("parseRuntimePassword(valid) = %q, %t", got, ok)
		}
	}
	for _, input := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), valid + "\n\n", strings.Repeat("g", 64)} {
		if got, ok := parseRuntimePassword([]byte(input)); ok || got != "" {
			t.Fatalf("parseRuntimePassword(%q) = %q, %t", input, got, ok)
		}
	}
}

func TestLoadRuntimePasswordReusesValidCredential(t *testing.T) {
	t.Parallel()
	valid := strings.Repeat("b2", 32)
	got, err := loadRuntimePassword(func(string) ([]byte, error) {
		return []byte(valid + "\n"), nil
	}, "/credential/database-password")
	if err != nil || got != valid {
		t.Fatalf("loadRuntimePassword() = %q, %v", got, err)
	}
}

func TestLoadRuntimePasswordRegeneratesMissingOrMalformedCredential(t *testing.T) {
	t.Parallel()
	for name, read := range map[string]readFileFunc{
		"missing":   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		"malformed": func(string) ([]byte, error) { return []byte("partial"), nil },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := loadRuntimePassword(read, "/credential/database-password")
			if err != nil {
				t.Fatalf("loadRuntimePassword() error = %v", err)
			}
			if _, ok := parseRuntimePassword([]byte(got)); !ok {
				t.Fatalf("generated password is invalid: %q", got)
			}
		})
	}
}

func TestLoadRuntimePasswordRejectsReadFailure(t *testing.T) {
	t.Parallel()
	readErr := errors.New("permission denied")
	_, err := loadRuntimePassword(func(string) ([]byte, error) { return nil, readErr }, "/credential/database-password")
	if err == nil || errors.Is(err, readErr) {
		t.Fatalf("loadRuntimePassword() error = %v, want redacted error", err)
	}
}

func TestSecureCredentialFileReclaimsBeforeSettingMode(t *testing.T) {
	t.Parallel()
	var calls []string
	err := secureCredentialFile("/credential/database-password", func(path string, mode os.FileMode) error {
		calls = append(calls, path+":"+mode.String())
		return nil
	}, func(path string, uid, gid int) error {
		calls = append(calls, path+":"+fmt.Sprintf("%d:%d", uid, gid))
		return nil
	})
	if err != nil {
		t.Fatalf("secureCredentialFile() error = %v", err)
	}
	want := []string{
		"/credential/database-password:0:0",
		"/credential/database-password:-rw-------",
		"/credential/database-password:10001:10001",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("secureCredentialFile() calls = %#v, want %#v", calls, want)
	}
}

func TestSecureCredentialFileStopsOnFailure(t *testing.T) {
	t.Parallel()
	chmodErr := errors.New("chmod failed")
	var chowns int
	err := secureCredentialFile("/credential/database-password", func(string, os.FileMode) error {
		return chmodErr
	}, func(string, int, int) error {
		chowns++
		return nil
	})
	if err == nil || errors.Is(err, chmodErr) {
		t.Fatalf("secureCredentialFile() error = %v, want redacted failure", err)
	}
	if chowns != 1 {
		t.Fatalf("secureCredentialFile() chown calls = %d, want 1", chowns)
	}
}

func TestValidateRuntimeRole(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		valid  bool
		wantOK bool
	}{
		"migration whitelist":  {valid: true, wantOK: true},
		"missing role":         {valid: false},
		"extra role privilege": {valid: false},
		"object ownership":     {valid: false},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateRuntimeRole(context.Background(), roleStatusDB{row: roleStatusRow{values: []any{test.valid}}})
			if (err == nil) != test.wantOK {
				t.Fatalf("validateRuntimeRole() error=%v, want success=%t", err, test.wantOK)
			}
		})
	}
}

func TestValidateRuntimeRoleRedactsQueryFailure(t *testing.T) {
	t.Parallel()
	err := validateRuntimeRole(context.Background(), roleStatusDB{row: roleStatusRow{err: errors.New("database details")}})
	if err == nil || strings.Contains(err.Error(), "database details") {
		t.Fatalf("validateRuntimeRole() error=%v, want redacted failure", err)
	}
}
