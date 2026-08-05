package owner

import (
	"context"
	"reflect"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// DocumentContentReader 通过 Authoring owner 打开生成所需的短暂正文。
type DocumentContentReader struct{ authoring AuthoringReader }

var _ organizingapp.FrozenDocumentContentReader = (*DocumentContentReader)(nil)

// NewDocumentContentReader 创建只具备不可变 Revision 批读能力的生成适配器。
func NewDocumentContentReader(authoring AuthoringReader) (*DocumentContentReader, error) {
	if nilDocumentReader(authoring) {
		return nil, dependencyUnavailable("organizing authoring document reader is unavailable")
	}
	return &DocumentContentReader{authoring: authoring}, nil
}

// OpenFrozenDocumentContents 复核 Snapshot 身份、顺序、版本与正文哈希后返回短暂正文。
func (reader *DocumentContentReader) OpenFrozenDocumentContents(ctx context.Context, workspaceID foundation.ID, references []organizingdomain.MaterialRef) ([]organizingapp.FrozenDocumentContent, error) {
	if reader == nil || nilDocumentReader(reader.authoring) {
		return nil, dependencyUnavailable("organizing authoring document reader is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || len(references) == 0 || len(references) > organizingdomain.MaxDraftMaterials {
		return nil, invalid("organizing document content request is invalid")
	}
	identities := make([]authoringapp.ArticleRevisionIdentity, len(references))
	seen := make(map[authoringapp.ArticleRevisionIdentity]struct{}, len(references))
	for index, reference := range references {
		if reference.Validate() != nil || reference.Kind != organizingdomain.MaterialDocumentRevision {
			return nil, invalid("organizing document content reference is invalid")
		}
		identity := authoringapp.ArticleRevisionIdentity{DocumentID: reference.DocumentID, RevisionID: reference.ArticleRevisionID}
		if _, duplicate := seen[identity]; duplicate {
			return nil, invalid("organizing document content reference is duplicated")
		}
		seen[identity] = struct{}{}
		identities[index] = identity
	}
	snapshots, err := reader.authoring.GetArticleRevisions(ctx, authoringapp.ArticleRevisionBatchQuery{WorkspaceID: workspaceID, Items: identities})
	if err != nil {
		return nil, err
	}
	if len(snapshots) != len(references) {
		return nil, inconsistent("authoring returned an incomplete organizing document batch")
	}
	result := make([]organizingapp.FrozenDocumentContent, len(snapshots))
	for index, snapshot := range snapshots {
		reference := references[index]
		document, revision := snapshot.Document, snapshot.Revision
		if document.Validate() != nil || revision.Validate() != nil || document.WorkspaceID != workspaceID || revision.WorkspaceID != workspaceID ||
			document.ID != reference.DocumentID || revision.DocumentID != reference.DocumentID || revision.ID != reference.ArticleRevisionID ||
			int64(revision.RevisionNo) != reference.Version || revision.ContentHash != reference.ContentHash ||
			document.Lifecycle == authoringdomain.DocumentDeleted {
			return nil, inconsistent("authoring document content differs from the frozen organizing material")
		}
		result[index] = organizingapp.FrozenDocumentContent{
			DocumentID: document.ID, ArticleRevisionID: revision.ID, RevisionNo: int64(revision.RevisionNo),
			ContentHash: revision.ContentHash, Title: document.Title, Content: revision.Content,
		}
	}
	return result, nil
}

func nilDocumentReader(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
