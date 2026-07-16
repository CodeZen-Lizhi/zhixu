package foundation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ID is the canonical UUID identifier used by domain objects.
type ID string

// ParseID validates and normalizes a canonical UUID string.
func ParseID(value string) (ID, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", NewError(ErrorInvalidInput, "INVALID_ID", false, errors.New("id must be a canonical UUID"))
	}
	compact := strings.ReplaceAll(value, "-", "")
	if len(compact) != 32 {
		return "", NewError(ErrorInvalidInput, "INVALID_ID", false, errors.New("id must contain 16 bytes"))
	}
	if _, err := hex.DecodeString(compact); err != nil {
		return "", NewError(ErrorInvalidInput, "INVALID_ID", false, errors.New("id contains non-hexadecimal characters"))
	}
	return ID(value), nil
}

// IDGenerator creates stable identifiers without exposing implementation details.
type IDGenerator interface {
	New() (ID, error)
}

// UUIDGenerator creates RFC 4122 version 4 identifiers.
type UUIDGenerator struct{ reader io.Reader }

// NewUUIDGenerator creates a generator. A nil reader uses crypto/rand.Reader.
func NewUUIDGenerator(reader io.Reader) UUIDGenerator {
	if reader == nil {
		reader = rand.Reader
	}
	return UUIDGenerator{reader: reader}
}

// New creates one UUID v4 identifier.
func (g UUIDGenerator) New() (ID, error) {
	if g.reader == nil {
		g.reader = rand.Reader
	}
	var raw [16]byte
	if _, err := io.ReadFull(g.reader, raw[:]); err != nil {
		return "", NewError(ErrorDependencyUnavailable, "ID_GENERATION_FAILED", true, fmt.Errorf("read random bytes: %w", err))
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	return ParseID(value)
}
