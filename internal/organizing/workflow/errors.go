package workflow

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func workflowError(kind foundation.ErrorKind, code string, retryable bool, message string) error {
	return foundation.NewError(kind, code, retryable, errors.New(message))
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
