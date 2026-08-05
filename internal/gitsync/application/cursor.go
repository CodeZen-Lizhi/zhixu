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
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const runCursorVersion = 1

type runCursorPayload struct {
	Version     int           `json:"version"`
	WorkspaceID foundation.ID `json:"workspace_id"`
	Limit       int           `json:"limit"`
	BeforeTime  time.Time     `json:"before_time"`
	BeforeID    foundation.ID `json:"before_id"`
}

// CursorCodec 签名绑定 Workspace 的运行 keyset 游标。
type CursorCodec struct{ key []byte }

// NewCursorCodec 使用显式且不少于 32 字节的密钥创建编解码器。
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) < 32 {
		return nil, errors.New("Git sync cursor key is too short")
	}
	return &CursorCodec{key: append([]byte(nil), key...)}, nil
}

// NewRandomCursorCodec 创建进程内游标签名密钥。
func NewRandomCursorCodec() (*CursorCodec, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return NewCursorCodec(key)
}

func (codec *CursorCodec) encode(payload runCursorPayload) (string, error) {
	if codec == nil || len(codec.key) < 32 || validateRunCursor(payload) != nil {
		return "", invalid(domain.ErrorCodeInvalid, "Git sync cursor payload is invalid")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (codec *CursorCodec) decode(value string) (runCursorPayload, error) {
	if codec == nil || len(codec.key) < 32 || len(value) == 0 || len(value) > 2048 {
		return runCursorPayload{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor is invalid")
	}
	payloadPart, signaturePart, found := bytes.Cut([]byte(value), []byte("."))
	if !found || len(payloadPart) == 0 || len(signaturePart) == 0 {
		return runCursorPayload{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor is invalid")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(string(payloadPart))
	if err != nil {
		return runCursorPayload{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(string(signaturePart))
	if err != nil {
		return runCursorPayload{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor is invalid")
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(encoded)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return runCursorPayload{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor is invalid")
	}
	var payload runCursorPayload
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || validateRunCursor(payload) != nil {
		return runCursorPayload{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return runCursorPayload{}, invalid(domain.ErrorCodeInvalid, "Git sync cursor is invalid")
	}
	return payload, nil
}

func validateRunCursor(payload runCursorPayload) error {
	if payload.Version != runCursorVersion || payload.Limit < 1 || payload.Limit > domain.MaxListLimit ||
		payload.BeforeTime.IsZero() {
		return errors.New("invalid Git sync cursor")
	}
	workspaceID, workspaceErr := foundation.ParseID(string(payload.WorkspaceID))
	beforeID, beforeErr := foundation.ParseID(string(payload.BeforeID))
	if workspaceErr != nil || beforeErr != nil || workspaceID != payload.WorkspaceID || beforeID != payload.BeforeID {
		return errors.New("invalid Git sync cursor identity")
	}
	return nil
}
