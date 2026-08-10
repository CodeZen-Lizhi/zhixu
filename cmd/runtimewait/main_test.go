package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunRetriesDatabaseBeforeReplacingProcess(t *testing.T) {
	database := &fakeDatabase{pingErrors: []error{errors.New("database is starting"), nil}}
	var replacedPath string
	var replacedArguments []string
	var loadedProfile processProfile
	deps := testDependencies(database)
	deps.load = func(profile processProfile) (waitConfiguration, error) {
		loadedProfile = profile
		return waitConfiguration{databaseURL: "postgres://fixture", pingTimeout: time.Second}, nil
	}
	deps.replace = func(path string, arguments []string, _ []string) error {
		replacedPath = path
		replacedArguments = append([]string(nil), arguments...)
		return nil
	}
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"--profile", "api", "--", "/app/zhixu-api", "--config", "/tmp/config.yml"}, &stderr, deps)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if loadedProfile != profileAPI || database.pingCalls != 2 || replacedPath != "/app/zhixu-api" || !reflect.DeepEqual(replacedArguments, []string{"/app/zhixu-api", "--config", "/tmp/config.yml"}) {
		t.Fatalf("profile=%q ping calls=%d path=%q arguments=%q", loadedProfile, database.pingCalls, replacedPath, replacedArguments)
	}
	if got := stderr.String(); got != "runtime-wait: DATABASE_UNAVAILABLE; waiting for PostgreSQL\n" {
		t.Fatalf("stderr = %q", got)
	}
	if !database.closed {
		t.Fatal("database was not closed after the test process replacement returned")
	}
}

func TestRunStopsRetryingWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	database := &fakeDatabase{pingErrors: []error{errors.New("database is unavailable")}, onPing: cancel}
	deps := testDependencies(database)
	deps.replace = func(string, []string, []string) error {
		t.Fatal("process replacement must not run after cancellation")
		return nil
	}

	err := run(ctx, []string{"--profile", "worker", "--", "/app/zhixu-worker"}, &bytes.Buffer{}, deps)
	if !errors.Is(err, context.Canceled) || stableErrorCode(err) != "RUNTIME_WAIT_CANCELLED" {
		t.Fatalf("run() error = %v code=%s", err, stableErrorCode(err))
	}
	if database.pingCalls != 1 || !database.closed {
		t.Fatalf("database state: ping calls=%d closed=%t", database.pingCalls, database.closed)
	}
}

func TestRunRejectsInvalidConfigurationWithoutExposingCause(t *testing.T) {
	deps := testDependencies(&fakeDatabase{})
	deps.load = func(processProfile) (waitConfiguration, error) {
		return waitConfiguration{}, errors.New("postgres://user:secret-password@example.invalid/database")
	}
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"--profile", "api", "--", "/app/zhixu-api"}, &stderr, deps)
	if stableErrorCode(err) != "RUNTIME_WAIT_CONFIGURATION_INVALID" {
		t.Fatalf("run() error = %v code=%s", err, stableErrorCode(err))
	}
	if strings.Contains(stderr.String(), "secret-password") {
		t.Fatalf("stderr exposed configuration cause: %q", stderr.String())
	}
}

func TestRunRejectsInvalidDatabaseConfigurationWithoutRetrying(t *testing.T) {
	const secret = "database-url-secret"
	deps := testDependencies(&fakeDatabase{})
	openCalls := 0
	deps.open = func(context.Context, waitConfiguration) (database, error) {
		openCalls++
		return nil, errors.New("invalid " + secret)
	}

	err := run(context.Background(), []string{"--profile", "api", "--", "/app/zhixu-api"}, &bytes.Buffer{}, deps)
	if stableErrorCode(err) != "RUNTIME_WAIT_CONFIGURATION_INVALID" || openCalls != 1 {
		t.Fatalf("run() error = %v code=%s open calls=%d", err, stableErrorCode(err), openCalls)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("runtime error exposed database configuration: %v", err)
	}
}

func TestParseCommandRequiresSeparatorAndTarget(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"--profile"},
		{"--profile", "api", "--"},
		{"--profile", "api", "--", "zhixu-api"},
	} {
		if _, _, err := parseCommand(arguments); stableErrorCode(err) != "RUNTIME_WAIT_ARGUMENTS_INVALID" {
			t.Fatalf("parseCommand(%q) error = %v", arguments, err)
		}
	}
}

func TestParseCommandRejectsUnknownProfile(t *testing.T) {
	_, _, err := parseCommand([]string{"--profile", "unknown", "--", "/app/zhixu-api"})
	if stableErrorCode(err) != "RUNTIME_WAIT_PROFILE_INVALID" {
		t.Fatalf("parseCommand() error = %v", err)
	}
}

type fakeDatabase struct {
	pingErrors []error
	pingCalls  int
	closed     bool
	onPing     func()
}

func (database *fakeDatabase) Ping(context.Context) error {
	database.pingCalls++
	if database.onPing != nil {
		database.onPing()
	}
	if len(database.pingErrors) == 0 {
		return nil
	}
	err := database.pingErrors[0]
	database.pingErrors = database.pingErrors[1:]
	return err
}

func (database *fakeDatabase) Close() { database.closed = true }

func testDependencies(db database) dependencies {
	return dependencies{
		load: func(processProfile) (waitConfiguration, error) {
			return waitConfiguration{databaseURL: "postgres://fixture", pingTimeout: time.Second}, nil
		},
		open:          func(context.Context, waitConfiguration) (database, error) { return db, nil },
		replace:       func(string, []string, []string) error { return nil },
		environment:   func() []string { return []string{"FIXTURE=1"} },
		retryInterval: time.Millisecond,
	}
}
