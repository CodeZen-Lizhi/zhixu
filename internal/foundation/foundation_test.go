package foundation

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUUIDGeneratorCreatesCanonicalVersion4ID(t *testing.T) {
	generator := NewUUIDGenerator(bytes.NewReader(make([]byte, 16)))
	id, err := generator.New()
	if err != nil {
		t.Fatal(err)
	}
	if id != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("id = %q", id)
	}
	if _, err := ParseID(string(id)); err != nil {
		t.Fatalf("generated id is invalid: %v", err)
	}
}

func TestParseIDRejectsInvalidValue(t *testing.T) {
	_, err := ParseID("../../workspace")
	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != ErrorInvalidInput || classified.Code != "INVALID_ID" {
		t.Fatalf("error = %#v", err)
	}
}

func TestUUIDGeneratorClassifiesRandomSourceFailure(t *testing.T) {
	cause := errors.New("entropy unavailable")
	generator := NewUUIDGenerator(errorReader{err: cause})
	_, err := generator.New()
	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != ErrorDependencyUnavailable || !classified.Retryable {
		t.Fatalf("error = %#v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error does not wrap cause: %v", err)
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("safe error leaks cause: %v", err)
	}
}

func TestFixedClockReturnsUTC(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	instant := time.Date(2026, 7, 16, 9, 30, 0, 0, location)
	got := (FixedClock{Value: instant}).Now()
	if got.Location() != time.UTC || !got.Equal(instant) {
		t.Fatalf("time = %s (%s)", got, got.Location())
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
