package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// FrozenDocumentContent 是生成期间短暂打开的不可变文章正文，不得写入 Snapshot 或 Workflow 输出。
type FrozenDocumentContent struct {
	DocumentID        foundation.ID
	ArticleRevisionID foundation.ID
	RevisionNo        int64
	ContentHash       string
	Title             string
	Content           string
}

// FrozenDocumentContentReader 按 Snapshot 顺序打开精确的 Document Revision 正文。
type FrozenDocumentContentReader interface {
	OpenFrozenDocumentContents(context.Context, foundation.ID, []organizingdomain.MaterialRef) ([]FrozenDocumentContent, error)
}
