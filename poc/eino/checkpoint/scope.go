// Package checkpoint validates the narrow Eino interrupt/checkpoint boundary.
package checkpoint

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
)

const (
	checkpointIDPrefix = "zhixu-eino-checkpoint-v1:"
	maxScopeFieldBytes = 256
	keyBytes           = 32
)

var (
	// ErrInvalidScope means an Attempt checkpoint identity is incomplete or non-canonical.
	ErrInvalidScope = errors.New("checkpoint scope is invalid")
	// ErrInvalidKey means a checkpoint cryptographic key is missing or has the wrong size.
	ErrInvalidKey = errors.New("checkpoint key must contain exactly 32 bytes")
)

// AttemptScope binds one checkpoint to the current durable execution fence.
type AttemptScope struct {
	WorkflowRunID string
	NodeRunID     string
	AttemptID     string
	Fence         string
}

// Keyer derives opaque checkpoint IDs without exposing durable identities.
type Keyer struct{ key [keyBytes]byte }

// NewKeyer creates an opaque checkpoint ID derivation boundary.
func NewKeyer(key []byte) (*Keyer, error) {
	if len(key) != keyBytes {
		return nil, ErrInvalidKey
	}
	keyer := &Keyer{}
	copy(keyer.key[:], key)
	return keyer, nil
}

// ID returns a versioned HMAC over the complete Attempt scope.
func (keyer *Keyer) ID(scope AttemptScope) (string, error) {
	if keyer == nil {
		return "", ErrInvalidKey
	}
	fields := []string{scope.WorkflowRunID, scope.NodeRunID, scope.AttemptID, scope.Fence}
	for _, field := range fields {
		if !validScopeField(field) {
			return "", ErrInvalidScope
		}
	}

	mac := hmac.New(sha256.New, keyer.key[:])
	writeScopeField(mac, "checkpoint-scope/v1")
	for _, field := range fields {
		writeScopeField(mac, field)
	}
	return checkpointIDPrefix + hex.EncodeToString(mac.Sum(nil)), nil
}

func validScopeField(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxScopeFieldBytes && !strings.ContainsRune(value, 0)
}

func writeScopeField(target hash.Hash, value string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = target.Write(size[:])
	_, _ = target.Write([]byte(value))
}

func validCheckpointID(value string) bool {
	if !strings.HasPrefix(value, checkpointIDPrefix) || len(value) != len(checkpointIDPrefix)+sha256.Size*2 {
		return false
	}
	digest := strings.TrimPrefix(value, checkpointIDPrefix)
	if digest != strings.ToLower(digest) {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}
