package application

import (
	"bytes"
	"compress/zlib"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	collectionCursorVersion = 1
	collectionCursorKeySize = 32
	collectionCursorMaxSize = 32 * 1024
	collectionCursorMaxJSON = 64 * 1024
)

// CursorScope 区分已保存集合与临时预览，避免把 ad-hoc query 伪装成 Collection。
type CursorScope string

const (
	// CursorScopeSaved 表示 cursor 绑定已保存 Collection ID/version。
	CursorScopeSaved CursorScope = "SAVED"
	// CursorScopePreview 表示 cursor 只绑定 canonical ad-hoc query。
	CursorScopePreview CursorScope = "PREVIEW"
	// CursorScopeList 表示 cursor 绑定 Workspace 内的 Collection 管理列表。
	CursorScopeList CursorScope = "LIST"
)

// ResultCursor 绑定一个 Collection 结果页的完整查询上下文与最后一项 key。
type ResultCursor struct {
	Scope             CursorScope
	WorkspaceID       foundation.ID
	CollectionID      foundation.ID
	CollectionVersion int64
	QueryHash         string
	SortHash          string
	Limit             int
	RevisionHash      string
	LastObjectType    string
	LastID            foundation.ID
	LastSortValues    []*string
}

// CursorBinding 是解码时必须重新确认的当前查询上下文。
type CursorBinding struct {
	Scope             CursorScope
	WorkspaceID       foundation.ID
	CollectionID      foundation.ID
	CollectionVersion int64
	QueryHash         string
	SortHash          string
	Limit             int
	RevisionHash      string
}

// CursorCodec 使用进程生命周期内的 HMAC 密钥签发 opaque cursor。
type CursorCodec struct{ key []byte }

type cursorDocument struct {
	Version           int       `json:"v"`
	Scope             string    `json:"k"`
	WorkspaceID       string    `json:"w"`
	CollectionID      string    `json:"c"`
	CollectionVersion int64     `json:"cv"`
	QueryHash         string    `json:"q"`
	SortHash          string    `json:"s"`
	Limit             int       `json:"l"`
	RevisionHash      string    `json:"r"`
	LastObjectType    string    `json:"ot"`
	LastID            string    `json:"id"`
	LastSortValues    []*string `json:"sv,omitempty"`
}

// NewCursorCodec 创建一个显式密钥的 Collection cursor codec。
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) != collectionCursorKeySize {
		return nil, requestInvalid("collection cursor key must contain exactly 32 bytes")
	}
	return &CursorCodec{key: append([]byte(nil), key...)}, nil
}

// NewRandomCursorCodec 创建只在当前进程有效的 Collection cursor codec。
func NewRandomCursorCodec() (*CursorCodec, error) {
	key := make([]byte, collectionCursorKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "COLLECTION_CURSOR_UNAVAILABLE", true, err)
	}
	return NewCursorCodec(key)
}

// Encode 签发带上下文绑定的 opaque cursor。
func (codec *CursorCodec) Encode(cursor ResultCursor) (string, error) {
	if codec == nil || len(codec.key) != collectionCursorKeySize || !validCursor(cursor) {
		return "", requestInvalid("collection cursor fields are invalid")
	}
	document := cursorDocument{Version: collectionCursorVersion, Scope: string(cursor.Scope), WorkspaceID: string(cursor.WorkspaceID), CollectionID: string(cursor.CollectionID), CollectionVersion: cursor.CollectionVersion, QueryHash: cursor.QueryHash, SortHash: cursor.SortHash, Limit: cursor.Limit, RevisionHash: cursor.RevisionHash, LastObjectType: cursor.LastObjectType, LastID: string(cursor.LastID), LastSortValues: cursor.LastSortValues}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorDependencyUnavailable, "COLLECTION_CURSOR_UNAVAILABLE", true, err)
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return "", foundation.NewError(foundation.ErrorDependencyUnavailable, "COLLECTION_CURSOR_UNAVAILABLE", true, err)
	}
	if err := writer.Close(); err != nil {
		return "", foundation.NewError(foundation.ErrorDependencyUnavailable, "COLLECTION_CURSOR_UNAVAILABLE", true, err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	signature := codec.sign(encoded)
	result := encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(result) > collectionCursorMaxSize {
		return "", requestInvalid("collection cursor is oversized")
	}
	return result, nil
}

// Decode 校验签名、请求绑定和 revision；revision 变化返回 stale。
func (codec *CursorCodec) Decode(raw string, binding CursorBinding) (ResultCursor, error) {
	if codec == nil || len(codec.key) != collectionCursorKeySize || strings.TrimSpace(raw) == "" || len(raw) > collectionCursorMaxSize {
		return ResultCursor{}, cursorInvalid("collection cursor is invalid")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ResultCursor{}, cursorInvalid("collection cursor is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, codec.sign(parts[0])) {
		return ResultCursor{}, cursorInvalid("collection cursor signature is invalid")
	}
	compressed, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ResultCursor{}, cursorInvalid("collection cursor payload is invalid")
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return ResultCursor{}, cursorInvalid("collection cursor payload is invalid")
	}
	var payloadBuffer bytes.Buffer
	if _, err := payloadBuffer.ReadFrom(io.LimitReader(reader, collectionCursorMaxJSON+1)); err != nil {
		_ = reader.Close()
		return ResultCursor{}, cursorInvalid("collection cursor payload is invalid")
	}
	if err := reader.Close(); err != nil {
		return ResultCursor{}, cursorInvalid("collection cursor payload is invalid")
	}
	payload := payloadBuffer.Bytes()
	if len(payload) > collectionCursorMaxJSON {
		return ResultCursor{}, cursorInvalid("collection cursor payload is oversized")
	}
	var document cursorDocument
	if json.Unmarshal(payload, &document) != nil || document.Version != collectionCursorVersion {
		return ResultCursor{}, cursorInvalid("collection cursor payload is invalid")
	}
	cursor := ResultCursor{Scope: CursorScope(document.Scope), WorkspaceID: foundation.ID(document.WorkspaceID), CollectionID: foundation.ID(document.CollectionID), CollectionVersion: document.CollectionVersion, QueryHash: document.QueryHash, SortHash: document.SortHash, Limit: document.Limit, RevisionHash: document.RevisionHash, LastObjectType: document.LastObjectType, LastID: foundation.ID(document.LastID), LastSortValues: document.LastSortValues}
	if !validCursor(cursor) || cursor.Scope != binding.Scope || cursor.WorkspaceID != binding.WorkspaceID || cursor.CollectionID != binding.CollectionID || cursor.CollectionVersion != binding.CollectionVersion || cursor.QueryHash != binding.QueryHash || cursor.SortHash != binding.SortHash || cursor.Limit != binding.Limit {
		return ResultCursor{}, cursorInvalid("collection cursor does not match the request")
	}
	if cursor.RevisionHash != binding.RevisionHash {
		return ResultCursor{}, foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeCursorStale, false, errors.New("collection result revision changed"))
	}
	return cursor, nil
}

func (codec *CursorCodec) sign(payload string) []byte {
	hash := hmac.New(sha256.New, codec.key)
	_, _ = hash.Write([]byte(payload))
	return hash.Sum(nil)
}

func validCursor(cursor ResultCursor) bool {
	if _, err := foundation.ParseID(string(cursor.WorkspaceID)); err != nil {
		return false
	}
	if _, err := foundation.ParseID(string(cursor.LastID)); err != nil {
		return false
	}
	switch cursor.Scope {
	case CursorScopeSaved:
		if _, err := foundation.ParseID(string(cursor.CollectionID)); err != nil || cursor.CollectionVersion < 1 {
			return false
		}
	case CursorScopePreview:
		if cursor.CollectionID != "" || cursor.CollectionVersion != 0 {
			return false
		}
	case CursorScopeList:
		if cursor.CollectionID != "" || cursor.CollectionVersion != 0 || cursor.LastObjectType != "COLLECTION" || len(cursor.LastSortValues) != 2 || cursor.LastSortValues[0] == nil || cursor.LastSortValues[1] == nil {
			return false
		}
	default:
		return false
	}
	return cursor.Limit >= 1 && cursor.Limit <= 100 && cursor.LastObjectType != "" && len(cursor.LastSortValues) <= domain.MaxSortTerms+2 && isHexHash(cursor.QueryHash) && isHexHash(cursor.SortHash) && isHexHash(cursor.RevisionHash)
}

func isHexHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
