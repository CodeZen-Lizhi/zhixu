package retrievalhttp

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
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	cursorVersion               = 1
	cursorSigningKeyBytes       = 32
	maxEncodedSearchCursorBytes = 2048
	searchCursorUnavailableCode = "RETRIEVAL_SEARCH_CURSOR_UNAVAILABLE"
)

// CursorCodec 使用进程内 HMAC 密钥签发有界 Search Cursor。
type CursorCodec struct {
	key [cursorSigningKeyBytes]byte
}

// SearchPage 是 top-100 检索窗口的一页及可选下一页 Cursor。
type SearchPage struct {
	Result     domain.SearchResult
	NextCursor string
}

type cursorPayload struct {
	Version        int           `json:"version"`
	RequestHash    string        `json:"request_hash"`
	IndexVersionID foundation.ID `json:"index_version_id"`
	ResultHash     string        `json:"result_hash"`
	Offset         int32         `json:"offset"`
}

// NewCursorCodec 使用显式测试/Composition 密钥创建 Cursor Codec。
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) != cursorSigningKeyBytes {
		return nil, cursorUnavailable("cursor signing key must contain exactly 32 bytes")
	}
	codec := &CursorCodec{}
	copy(codec.key[:], key)
	return codec, nil
}

// NewRandomCursorCodec 使用 crypto/rand 创建当前 API 进程生命周期内的 Cursor Codec。
func NewRandomCursorCodec() (*CursorCodec, error) {
	return newRandomCursorCodec(rand.Reader)
}

func newRandomCursorCodec(random io.Reader) (*CursorCodec, error) {
	if random == nil {
		return nil, cursorUnavailable("cursor random source is unavailable")
	}
	key := make([]byte, cursorSigningKeyBytes)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, cursorUnavailable("cursor signing key generation failed")
	}
	return NewCursorCodec(key)
}

// Paginate 验证可选 Cursor，并从完整 top-100 SearchResult 返回稳定页面。
func (codec *CursorCodec) Paginate(
	request domain.SearchRequest,
	result domain.SearchResult,
	pageLimit int32,
	rawCursor string,
) (SearchPage, error) {
	canonical, requestHash, err := codec.pageRequestHash(request, pageLimit)
	if err != nil {
		return SearchPage{}, err
	}
	if err := domain.ValidateSearchResult(canonical, result); err != nil {
		return SearchPage{}, err
	}
	resultHash, err := hashCanonical(result)
	if err != nil {
		return SearchPage{}, cursorUnavailable("search result fingerprint failed")
	}

	offset := int32(0)
	if rawCursor != "" {
		payload, decodeErr := codec.decode(rawCursor)
		if decodeErr != nil {
			return SearchPage{}, decodeErr
		}
		if payload.RequestHash != requestHash {
			return SearchPage{}, cursorInvalid("search cursor belongs to a different request")
		}
		if payload.IndexVersionID != result.IndexVersionID || payload.ResultHash != resultHash {
			return SearchPage{}, cursorStale()
		}
		offset = payload.Offset
	}
	if offset < 0 || offset > int32(len(result.Items)) || offset >= domain.MaxSearchLimit {
		return SearchPage{}, cursorStale()
	}
	end := offset + pageLimit
	if end > int32(len(result.Items)) {
		end = int32(len(result.Items))
	}
	pageResult := result
	pageResult.Items = append([]domain.EvidenceV1(nil), result.Items[offset:end]...)

	nextCursor := ""
	if end < int32(len(result.Items)) && end < domain.MaxSearchLimit {
		nextCursor, err = codec.encode(cursorPayload{
			Version: cursorVersion, RequestHash: requestHash, IndexVersionID: result.IndexVersionID,
			ResultHash: resultHash, Offset: end,
		})
		if err != nil {
			return SearchPage{}, err
		}
	}
	return SearchPage{Result: pageResult, NextCursor: nextCursor}, nil
}

// ValidateRequestCursor 在执行 Search 前验证 Cursor 签名与规范请求绑定。
func (codec *CursorCodec) ValidateRequestCursor(request domain.SearchRequest, pageLimit int32, rawCursor string) error {
	if rawCursor == "" {
		_, _, err := codec.pageRequestHash(request, pageLimit)
		return err
	}
	_, requestHash, err := codec.pageRequestHash(request, pageLimit)
	if err != nil {
		return err
	}
	payload, err := codec.decode(rawCursor)
	if err != nil {
		return err
	}
	if payload.RequestHash != requestHash {
		return cursorInvalid("search cursor belongs to a different request")
	}
	return nil
}

func (codec *CursorCodec) pageRequestHash(request domain.SearchRequest, pageLimit int32) (domain.SearchRequest, string, error) {
	if codec == nil || pageLimit <= 0 || pageLimit > domain.MaxSearchLimit {
		return domain.SearchRequest{}, "", cursorInvalid("search page limit or cursor codec is invalid")
	}
	canonical, err := domain.CanonicalizeSearchRequest(request)
	if err != nil || canonical.Limit != domain.MaxSearchLimit {
		return domain.SearchRequest{}, "", cursorInvalid("search cursor requires the canonical top window request")
	}
	requestHash, err := hashCanonical(struct {
		Search    domain.SearchRequest `json:"search"`
		PageLimit int32                `json:"page_limit"`
	}{Search: canonical, PageLimit: pageLimit})
	if err != nil {
		return domain.SearchRequest{}, "", cursorUnavailable("search request fingerprint failed")
	}
	return canonical, requestHash, nil
}

func (codec *CursorCodec) encode(payload cursorPayload) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", cursorUnavailable("search cursor payload encoding failed")
	}
	mac := hmac.New(sha256.New, codec.key[:])
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (codec *CursorCodec) decode(raw string) (cursorPayload, error) {
	if codec == nil || len(raw) == 0 || len(raw) > maxEncodedSearchCursorBytes {
		return cursorPayload{}, cursorInvalid("search cursor is empty or oversized")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return cursorPayload{}, cursorInvalid("search cursor encoding is invalid")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursorPayload{}, cursorInvalid("search cursor payload is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return cursorPayload{}, cursorInvalid("search cursor signature is invalid")
	}
	mac := hmac.New(sha256.New, codec.key[:])
	_, _ = mac.Write(payloadBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cursorPayload{}, cursorInvalid("search cursor signature does not match")
	}
	var payload cursorPayload
	decoder := json.NewDecoder(bytes.NewReader(payloadBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return cursorPayload{}, cursorInvalid("search cursor payload is invalid")
	}
	if err := ensureJSONEOF(decoder); err != nil || payload.Version != cursorVersion ||
		!canonicalHash(payload.RequestHash) || !canonicalHash(payload.ResultHash) ||
		payload.Offset <= 0 || payload.Offset >= domain.MaxSearchLimit {
		return cursorPayload{}, cursorInvalid("search cursor fields are invalid")
	}
	parsedIndexID, err := foundation.ParseID(string(payload.IndexVersionID))
	if err != nil || parsedIndexID != payload.IndexVersionID {
		return cursorPayload{}, cursorInvalid("search cursor index identity is invalid")
	}
	return payload, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("json contains multiple values")
		}
		return err
	}
	return nil
}

func hashCanonical(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalHash(value string) bool {
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

func cursorInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeSearchCursorInvalid, false, errors.New(message))
}

func cursorStale() error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSearchCursorStale, false, errors.New("search cursor result is stale"))
}

func cursorUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, searchCursorUnavailableCode, false, errors.New(message))
}
