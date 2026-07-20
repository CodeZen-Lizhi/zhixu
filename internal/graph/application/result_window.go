package application

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

const MaxResultWindowItems = 500

// ResultWindowRequest 是 application 独占的分页绑定；adapter 只提供完整稳定排序窗口。
type ResultWindowRequest struct {
	QueryKind            string
	WorkspaceID          foundation.ID
	CanonicalRequestHash string
	Limit                int
	Cursor               string
}

// ResultWindowPage 是从完整有界窗口切出的页面。
type ResultWindowPage[T any] struct {
	Items       []T
	Fingerprint string
	NextCursor  string
	Complete    bool
}

// PaginateResultWindow 重算窗口指纹、校验 cursor 新鲜度、切页并签发下一页 cursor。
func PaginateResultWindow[T any](codec *CursorCodec, request ResultWindowRequest, orderedWindow []T) (ResultWindowPage[T], error) {
	return paginateResultWindow(codec, request, orderedWindow, orderedWindow)
}

func paginateResultWindow[T any](codec *CursorCodec, request ResultWindowRequest, orderedWindow []T, fingerprintValue any) (ResultWindowPage[T], error) {
	if codec == nil || !codec.initialized || !validQueryKind(request.QueryKind) || !validWorkspaceID(request.WorkspaceID) ||
		!canonicalCursorHash(request.CanonicalRequestHash) || request.Limit < 1 || request.Limit > graphdomain.MaxLimit {
		return ResultWindowPage[T]{}, cursorInvalid("graph result-window request is invalid")
	}
	if len(orderedWindow) > MaxResultWindowItems {
		return ResultWindowPage[T]{}, inconsistent("graph adapter result window exceeds application bound")
	}
	fingerprint, err := hashCanonicalCursorValue(fingerprintValue)
	if err != nil {
		return ResultWindowPage[T]{}, err
	}
	offset := 0
	if request.Cursor != "" {
		decoded, decodeErr := codec.Decode(request.Cursor, CursorBinding{
			QueryKind: request.QueryKind, WorkspaceID: request.WorkspaceID,
			CanonicalRequestHash: request.CanonicalRequestHash, ResultFingerprint: fingerprint,
			Limit: int32(request.Limit),
		})
		if decodeErr != nil {
			return ResultWindowPage[T]{}, decodeErr
		}
		offset = int(decoded.Offset)
		if offset >= len(orderedWindow) {
			return ResultWindowPage[T]{}, cursorInvalid("graph cursor offset is outside the result window")
		}
	}
	end := offset + request.Limit
	if end > len(orderedWindow) {
		end = len(orderedWindow)
	}
	items := append([]T(nil), orderedWindow[offset:end]...)
	page := ResultWindowPage[T]{Items: items, Fingerprint: fingerprint, Complete: end == len(orderedWindow)}
	if !page.Complete {
		page.NextCursor, err = codec.Encode(ResultWindowCursor{
			QueryKind: request.QueryKind, WorkspaceID: request.WorkspaceID,
			CanonicalRequestHash: request.CanonicalRequestHash, ResultFingerprint: fingerprint,
			Limit: int32(request.Limit), Offset: int32(end),
		})
		if err != nil {
			return ResultWindowPage[T]{}, err
		}
	}
	return page, nil
}
