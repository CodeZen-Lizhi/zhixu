// Package authoring verifies immutable Artifact document sources through the Authoring owner boundary.
package authoring

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const maxDocumentSourceBatch = 64

// RevisionReader 是验证 Artifact 来源文档所需的最小 Authoring 批读边界。
type RevisionReader interface {
	GetArticleRevisions(context.Context, authoringapp.ArticleRevisionBatchQuery) ([]authoringapp.ArticleRevisionSnapshot, error)
}

// Verifier 只返回从 Authoring owner 重建的可信 DocumentSource。
type Verifier struct{ reader RevisionReader }

var _ artifactapp.DocumentSourceVerifier = (*Verifier)(nil)

// NewVerifier 创建失败关闭的来源文档验证器。
func NewVerifier(reader RevisionReader) (*Verifier, error) {
	if nilInterface(reader) {
		return nil, unavailable("artifact authoring revision reader is unavailable")
	}
	return &Verifier{reader: reader}, nil
}

// VerifyDocumentSources 按请求顺序复核 Workspace、Revision 号与正文哈希。
func (verifier *Verifier) VerifyDocumentSources(ctx context.Context, workspaceID foundation.ID, input []artifactapp.DocumentSourceInput) ([]artifactdomain.DocumentSource, error) {
	if verifier == nil || nilInterface(verifier.reader) {
		return nil, unavailable("artifact document source verifier is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || len(input) == 0 || len(input) > maxDocumentSourceBatch {
		return nil, invalid("artifact document source request is invalid")
	}
	items := make([]authoringapp.ArticleRevisionIdentity, len(input))
	seen := make(map[authoringapp.ArticleRevisionIdentity]struct{}, len(input))
	for index, source := range input {
		identity := authoringapp.ArticleRevisionIdentity{DocumentID: source.DocumentID, RevisionID: source.ArticleRevisionID}
		if !validID(source.DocumentID) || !validID(source.ArticleRevisionID) || source.DocumentID == source.ArticleRevisionID ||
			source.RevisionNo < 1 || !validHash(source.ContentHash) {
			return nil, invalid("artifact document source identity is invalid")
		}
		if _, duplicate := seen[identity]; duplicate {
			return nil, invalid("artifact document source identity is duplicated")
		}
		seen[identity] = struct{}{}
		items[index] = identity
	}
	snapshots, err := verifier.reader.GetArticleRevisions(ctx, authoringapp.ArticleRevisionBatchQuery{WorkspaceID: workspaceID, Items: items})
	if err != nil {
		return nil, err
	}
	if len(snapshots) != len(items) {
		return nil, inconsistent("authoring returned an incomplete document source batch")
	}
	result := make([]artifactdomain.DocumentSource, len(snapshots))
	for index, snapshot := range snapshots {
		requested := input[index]
		document, revision := snapshot.Document, snapshot.Revision
		if document.Validate() != nil || revision.Validate() != nil || document.WorkspaceID != workspaceID || revision.WorkspaceID != workspaceID ||
			document.ID != requested.DocumentID || revision.DocumentID != requested.DocumentID || revision.ID != requested.ArticleRevisionID ||
			int64(revision.RevisionNo) != requested.RevisionNo || revision.ContentHash != requested.ContentHash ||
			document.Lifecycle == authoringdomain.DocumentDeleted {
			return nil, inconsistent("authoring document source differs from the requested immutable revision")
		}
		result[index] = artifactdomain.DocumentSource{
			DocumentID: document.ID, ArticleRevisionID: revision.ID, RevisionNo: int64(revision.RevisionNo),
			VerifiedContentHash: revision.ContentHash, Verified: true,
		}
	}
	return result, nil
}

func nilInterface(value any) bool {
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

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, artifactapp.ErrorCodeEvidenceInvalid, false, errors.New(message))
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, artifactapp.ErrorCodeResultInconsistent, false, errors.New(message))
}

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, artifactapp.ErrorCodeDependencyUnavailable, true, errors.New(message))
}
