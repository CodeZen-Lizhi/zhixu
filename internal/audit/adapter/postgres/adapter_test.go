package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestEncodeAuditJSONAddsReservedEnvelopeAndRejectsCollisions(t *testing.T) {
	event := adapterEvent()
	correlation, payload, err := encodeAuditJSON(event)
	if err != nil {
		t.Fatalf("encodeAuditJSON: %v", err)
	}
	if string(correlation) != string(event.Correlation) {
		t.Fatalf("correlation changed: %s", correlation)
	}
	for _, fragment := range []string{`"audit_outcome":"FAILED"`, `"audit_error_code":"E_TEST"`, `"audit_schema_version":"` + domain.SchemaVersion + `"`} {
		if !strings.Contains(string(payload), fragment) {
			t.Fatalf("payload missing %s: %s", fragment, payload)
		}
	}
	event.Metadata = []byte(`{"audit_outcome":"spoofed"}`)
	if _, _, err := encodeAuditJSON(event); err == nil {
		t.Fatal("reserved metadata key must be rejected")
	}
}

func TestScanEventFailsClosedWhenDatabaseContainsPlaintextSecret(t *testing.T) {
	row := fakeScanner{values: []any{
		"10000000-0000-4000-8000-000000000020", nil, "SYSTEM", nil, "security.check", nil, nil,
		"scan-key-1", `{"request_id":"request-1"}`, `{"audit_outcome":"SUCCEEDED","audit_schema_version":"` + domain.SchemaVersion + `","token":"plaintext-secret"}`,
		time.Date(2026, 7, 23, 8, 9, 10, 123456000, time.UTC),
	}}
	_, err := scanEvent(row)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeCorrupt {
		t.Fatalf("error=%v, want corrupt audit event", err)
	}
	if strings.Contains(err.Error(), "plaintext-secret") {
		t.Fatalf("corrupt error leaked secret: %v", err)
	}
}

func TestScanEventDecodesSafeEnvelope(t *testing.T) {
	row := fakeScanner{values: []any{
		"10000000-0000-4000-8000-000000000021", nil, "SYSTEM", nil, "security.check", nil, nil,
		"scan-key-2", `{"request_id":"request-1"}`, `{"audit_outcome":"SUCCEEDED","audit_schema_version":"` + domain.SchemaVersion + `","summary_hash":"sha256:ok"}`,
		time.Date(2026, 7, 23, 8, 9, 10, 123456000, time.UTC),
	}}
	event, err := scanEvent(row)
	if err != nil {
		t.Fatalf("scanEvent: %v", err)
	}
	if event.Outcome != domain.OutcomeSucceeded || event.SchemaVersion != domain.SchemaVersion || string(event.Metadata) != `{"summary_hash":"sha256:ok"}` {
		t.Fatalf("decoded event = %#v", event)
	}
}

func TestClassifyStoreErrorsUsesStableCodesAndDoesNotExposeDriverText(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{name: "unique", err: &pgconn.PgError{Code: "23505", Message: "token=database-secret"}, kind: foundation.ErrorVersionConflict, code: domain.ErrorCodeIdempotencyConflict},
		{name: "serialization", err: &pgconn.PgError{Code: "40001", Message: "dsn=postgres://user:secret@db/app"}, kind: foundation.ErrorRetryableFailure, code: domain.ErrorCodeStoreUnavailable, retryable: true},
		{name: "constraint", err: &pgconn.PgError{Code: "23514", Message: "absolute path /Users/private/a"}, kind: foundation.ErrorConsistencyViolation, code: domain.ErrorCodeEventInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyStoreError(test.err)
			var classified *foundation.Error
			if !errors.As(got, &classified) || classified.Kind != test.kind || classified.Code != test.code || classified.Retryable != test.retryable {
				t.Fatalf("error=%v classified=%#v", got, classified)
			}
			if strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "/Users") {
				t.Fatalf("classified error leaked driver text: %v", got)
			}
		})
	}
}

func adapterEvent() domain.Event {
	return domain.Event{
		ID:             foundation.ID("10000000-0000-4000-8000-000000000022"),
		ActorType:      domain.ActorSystem,
		Action:         "security.check",
		Outcome:        domain.OutcomeFailed,
		ErrorCode:      "E_TEST",
		IdempotencyKey: "adapter-key-1",
		Correlation:    []byte(`{"request_id":"request-1"}`),
		Metadata:       []byte(`{"summary_hash":"sha256:ok"}`),
		SchemaVersion:  domain.SchemaVersion,
		OccurredAt:     time.Date(2026, 7, 23, 8, 9, 10, 123456000, time.UTC),
	}
}

type fakeScanner struct {
	values []any
	err    error
}

func (scanner fakeScanner) Scan(dest ...any) error {
	if scanner.err != nil {
		return scanner.err
	}
	if len(dest) != len(scanner.values) {
		return errors.New("fake scanner column count mismatch")
	}
	for index, target := range dest {
		assignScanValue(target, scanner.values[index])
	}
	return nil
}

func assignScanValue(target any, source any) {
	destination := reflect.ValueOf(target).Elem()
	if source == nil {
		destination.Set(reflect.Zero(destination.Type()))
		return
	}
	value := reflect.ValueOf(source)
	if value.Type().AssignableTo(destination.Type()) {
		destination.Set(value)
		return
	}
	if destination.Kind() == reflect.Pointer && value.Type().AssignableTo(destination.Type().Elem()) {
		pointer := reflect.New(destination.Type().Elem())
		pointer.Elem().Set(value)
		destination.Set(pointer)
		return
	}
	panic("fake scanner type mismatch")
}
