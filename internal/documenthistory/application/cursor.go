package application

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const cursorVersion = 1

type cursorPayload struct {
	Version     int           `json:"version"`
	WorkspaceID foundation.ID `json:"workspace_id"`
	DocumentID  foundation.ID `json:"document_id"`
	Path        string        `json:"path"`
	Branch      string        `json:"branch"`
	Head        string        `json:"head"`
	Limit       int           `json:"limit"`
	Offset      int           `json:"offset"`
	LastCommit  string        `json:"last_commit"`
}

// CursorCodec signs all mutable Git baselines carried by a History cursor.
type CursorCodec struct{ key []byte }

// NewCursorCodec creates a cursor codec from an explicit 32-byte-or-longer key.
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) < 32 {
		return nil, errors.New("document history cursor key is too short")
	}
	return &CursorCodec{key: append([]byte(nil), key...)}, nil
}

// NewRandomCursorCodec creates a process-local cursor signing key.
func NewRandomCursorCodec() (*CursorCodec, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return NewCursorCodec(key)
}

func (codec *CursorCodec) encode(payload cursorPayload) (string, error) {
	if codec == nil || len(codec.key) < 32 || validateCursorPayload(payload) != nil {
		return "", invalid("history cursor payload is invalid")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (codec *CursorCodec) decode(value string) (cursorPayload, error) {
	if codec == nil || len(codec.key) < 32 || len(value) == 0 || len(value) > 4096 {
		return cursorPayload{}, cursorInvalid("history cursor is invalid")
	}
	payloadPart, signaturePart, found := strings.Cut(value, ".")
	if !found || payloadPart == "" || signaturePart == "" {
		return cursorPayload{}, cursorInvalid("history cursor is invalid")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return cursorPayload{}, cursorInvalid("history cursor is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return cursorPayload{}, cursorInvalid("history cursor is invalid")
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(encoded)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cursorPayload{}, cursorInvalid("history cursor signature is invalid")
	}
	var payload cursorPayload
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || validateCursorPayload(payload) != nil {
		return cursorPayload{}, cursorInvalid("history cursor payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cursorPayload{}, cursorInvalid("history cursor payload is invalid")
	}
	return payload, nil
}

func cursorInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeCursorInvalid, false, errors.New(message))
}

func validateCursorPayload(payload cursorPayload) error {
	if payload.Version != cursorVersion || !domain.ValidID(payload.WorkspaceID) || !domain.ValidID(payload.DocumentID) ||
		!domain.ValidPath(payload.Path) || payload.Branch == "" || len(payload.Branch) > 1024 || strings.ContainsAny(payload.Branch, "\x00\r\n") ||
		!domain.ValidObjectID(payload.Head) || payload.Limit < 1 || payload.Limit > domain.MaxHistoryLimit ||
		payload.Offset < 1 || payload.Offset > domain.MaxHistoryOffset || !domain.ValidObjectID(payload.LastCommit) {
		return errors.New("invalid cursor payload")
	}
	return nil
}
