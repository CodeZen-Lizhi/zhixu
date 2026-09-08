package postgres

import (
	"errors"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func validateArticleRevisionBatchQuery(query authoringapp.ArticleRevisionBatchQuery) error {
	if !validID(query.WorkspaceID) || len(query.Items) == 0 || len(query.Items) > authoringapp.MaxArticleRevisionBatchSize {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("article revision batch query is invalid"))
	}
	seen := make(map[authoringapp.ArticleRevisionIdentity]struct{}, len(query.Items))
	for _, item := range query.Items {
		if !validID(item.DocumentID) || !validID(item.RevisionID) {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("article revision batch identity is invalid"))
		}
		if _, duplicate := seen[item]; duplicate {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("article revision batch contains duplicates"))
		}
		seen[item] = struct{}{}
	}
	return nil
}
