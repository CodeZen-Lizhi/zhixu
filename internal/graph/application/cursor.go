package application

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

const (
	cursorVersion          = 1
	cursorSigningKeyBytes  = 32
	maxEncodedCursorBytes  = 2048
	maxCursorQueryKindSize = 64
)

// ResultWindowCursor 绑定一个 Graph 查询有界结果窗口的下一页位置。
type ResultWindowCursor struct {
	QueryKind            string
	WorkspaceID          foundation.ID
	CanonicalRequestHash string
	ResultFingerprint    string
	Limit                int32
	Offset               int32
}

// CursorBinding 描述解码 cursor 时必须重新确认的当前查询与结果范围。
type CursorBinding struct {
	QueryKind            string
	WorkspaceID          foundation.ID
	CanonicalRequestHash string
	ResultFingerprint    string
	Limit                int32
}

// CursorCodec 使用进程生命周期内的 HMAC-SHA256 密钥签发 Graph cursor。
type CursorCodec struct {
	key         [cursorSigningKeyBytes]byte
	initialized bool
}

type cursorDocument struct {
	Version              int           `json:"version"`
	QueryKind            string        `json:"query_kind"`
	WorkspaceID          foundation.ID `json:"workspace_id"`
	CanonicalRequestHash string        `json:"canonical_request_hash"`
	ResultFingerprint    string        `json:"result_fingerprint"`
	Limit                int32         `json:"limit"`
	Offset               int32         `json:"offset"`
}

// NewCursorCodec 使用显式的 32 字节密钥创建 Graph cursor codec。
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) != cursorSigningKeyBytes {
		return nil, cursorUnavailable("graph cursor signing key must contain exactly 32 bytes")
	}
	codec := &CursorCodec{}
	copy(codec.key[:], key)
	codec.initialized = true
	return codec, nil
}

// NewRandomCursorCodec 使用 crypto/rand 创建当前进程生命周期内的 Graph cursor codec。
func NewRandomCursorCodec() (*CursorCodec, error) {
	return newRandomCursorCodec(rand.Reader)
}

func newRandomCursorCodec(random io.Reader) (*CursorCodec, error) {
	if random == nil {
		return nil, cursorUnavailable("graph cursor random source is unavailable")
	}
	key := make([]byte, cursorSigningKeyBytes)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, cursorUnavailable("graph cursor signing key generation failed")
	}
	return NewCursorCodec(key)
}

// Encode 编码并签名完整的 result-window cursor。
func (codec *CursorCodec) Encode(cursor ResultWindowCursor) (string, error) {
	document := cursorDocument{
		Version: cursorVersion, QueryKind: cursor.QueryKind, WorkspaceID: cursor.WorkspaceID,
		CanonicalRequestHash: cursor.CanonicalRequestHash, ResultFingerprint: cursor.ResultFingerprint,
		Limit: cursor.Limit, Offset: cursor.Offset,
	}
	if codec == nil || !codec.initialized || validateCursorDocument(document) != nil {
		return "", cursorInvalid("graph cursor fields are invalid")
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", cursorUnavailable("graph cursor payload encoding failed")
	}
	mac := hmac.New(sha256.New, codec.key[:])
	_, _ = mac.Write(payload)
	raw := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(raw) > maxEncodedCursorBytes {
		return "", cursorInvalid("graph cursor is oversized")
	}
	return raw, nil
}

// Decode 验证签名与当前绑定；查询范围变化为 invalid，结果变化为 stale。
func (codec *CursorCodec) Decode(raw string, binding CursorBinding) (ResultWindowCursor, error) {
	if err := validateCursorBinding(binding); err != nil {
		return ResultWindowCursor{}, cursorInvalid("graph cursor binding is invalid")
	}
	document, err := codec.decodeDocument(raw)
	if err != nil {
		return ResultWindowCursor{}, err
	}
	if document.QueryKind != binding.QueryKind || document.WorkspaceID != binding.WorkspaceID ||
		document.CanonicalRequestHash != binding.CanonicalRequestHash || document.Limit != binding.Limit {
		return ResultWindowCursor{}, cursorInvalid("graph cursor belongs to a different request")
	}
	if document.ResultFingerprint != binding.ResultFingerprint {
		return ResultWindowCursor{}, cursorStale()
	}
	return ResultWindowCursor{
		QueryKind: document.QueryKind, WorkspaceID: document.WorkspaceID,
		CanonicalRequestHash: document.CanonicalRequestHash, ResultFingerprint: document.ResultFingerprint,
		Limit: document.Limit, Offset: document.Offset,
	}, nil
}

func hashCanonicalCursorValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", cursorUnavailable("graph cursor binding fingerprint failed")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (codec *CursorCodec) decodeDocument(raw string) (cursorDocument, error) {
	if codec == nil || !codec.initialized || len(raw) == 0 || len(raw) > maxEncodedCursorBytes {
		return cursorDocument{}, cursorInvalid("graph cursor is empty or oversized")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return cursorDocument{}, cursorInvalid("graph cursor encoding is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursorDocument{}, cursorInvalid("graph cursor payload is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return cursorDocument{}, cursorInvalid("graph cursor signature is invalid")
	}
	mac := hmac.New(sha256.New, codec.key[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cursorDocument{}, cursorInvalid("graph cursor signature does not match")
	}

	var document cursorDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || ensureCursorJSONEOF(decoder) != nil || validateCursorDocument(document) != nil {
		return cursorDocument{}, cursorInvalid("graph cursor payload is invalid")
	}
	return document, nil
}

func validateCursorDocument(document cursorDocument) error {
	if document.Version != cursorVersion || !validQueryKind(document.QueryKind) ||
		!validWorkspaceID(document.WorkspaceID) || !canonicalCursorHash(document.CanonicalRequestHash) ||
		!canonicalCursorHash(document.ResultFingerprint) || document.Limit <= 0 || document.Limit > graphdomain.MaxLimit ||
		document.Offset <= 0 {
		return errors.New("invalid graph cursor document")
	}
	return nil
}

func validateCursorBinding(binding CursorBinding) error {
	if !validQueryKind(binding.QueryKind) || !validWorkspaceID(binding.WorkspaceID) ||
		!canonicalCursorHash(binding.CanonicalRequestHash) || !canonicalCursorHash(binding.ResultFingerprint) ||
		binding.Limit <= 0 || binding.Limit > graphdomain.MaxLimit {
		return errors.New("invalid graph cursor binding")
	}
	return nil
}

func validWorkspaceID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}

func validQueryKind(value string) bool {
	if len(value) == 0 || len(value) > maxCursorQueryKindSize || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func canonicalCursorHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func ensureCursorJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("json contains multiple values")
		}
		return err
	}
	return nil
}

func cursorInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid, false, errors.New(message))
}

func cursorStale() error {
	return foundation.NewError(foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale, false, errors.New("graph cursor result is stale"))
}

func cursorUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, false, errors.New(message))
}
