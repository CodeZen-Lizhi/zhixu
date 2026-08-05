package security

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
)

const maxCredentialKeyFileBytes = 128

// NewCredentialSealerFromFile loads a base64-encoded AES-256 key from one
// private regular file. The path and its contents are never returned in errors.
func NewCredentialSealerFromFile(path string) (*CredentialSealer, error) {
	key, err := loadCredentialKeyFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return NewCredentialSealer(key)
}

func loadCredentialKeyFile(path string) ([]byte, error) {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsRune(path, '\x00') {
		return nil, secretError(errors.New("Git remote key path is invalid"))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, secretError(errors.New("open Git remote key failed"))
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return nil, secretError(errors.New("stat opened Git remote key failed"))
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, secretError(errors.New("stat Git remote key path failed"))
	}
	permissions := info.Mode().Perm()
	if !info.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) ||
		(permissions != 0o400 && permissions != 0o600) || info.Size() < 1 || info.Size() > maxCredentialKeyFileBytes {
		return nil, secretError(errors.New("Git remote key file is not private regular file"))
	}
	encoded, err := io.ReadAll(io.LimitReader(file, maxCredentialKeyFileBytes+1))
	if err != nil {
		return nil, secretError(errors.New("read Git remote key failed"))
	}
	defer clear(encoded)
	if len(encoded) > maxCredentialKeyFileBytes {
		return nil, secretError(errors.New("Git remote key file is too large"))
	}
	encoded = bytes.TrimSpace(encoded)
	if len(encoded) == 0 || bytes.ContainsAny(encoded, " \t\r\n") {
		return nil, secretError(errors.New("Git remote key encoding is invalid"))
	}
	key := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	decoded, err := base64.StdEncoding.Decode(key, encoded)
	if err != nil || decoded != masterKeySize {
		clear(key)
		return nil, secretError(errors.New("Git remote key must decode to 32 bytes"))
	}
	return key[:decoded], nil
}
