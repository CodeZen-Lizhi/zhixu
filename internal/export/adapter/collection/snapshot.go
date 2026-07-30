// Package collection 将 Smart Collection durable read seam 适配为导出快照。
package collection

import (
	"context"
	"errors"
	"reflect"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const durablePageLimit = 100

// Service 是生成稳定导出快照所需的最小 Collection 应用边界。
type Service interface {
	Get(context.Context, foundation.ID, foundation.ID) (collectionapp.Collection, error)
	PlanDurableScan(context.Context, foundation.ID, foundation.ID) (collectionapp.DurableScanBinding, error)
	ReadDurableScanPage(context.Context, collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error)
}

// SnapshotReader 按固定 Collection version、query hash 和 read-model revision 分页读取结果。
type SnapshotReader struct{ service Service }

var _ exportapp.SnapshotReader = (*SnapshotReader)(nil)

// NewSnapshotReader 创建不会在组件内重新过滤结果的 Collection 导出适配器。
func NewSnapshotReader(service Service) (*SnapshotReader, error) {
	if isNil(service) {
		return nil, unavailable(errors.New("collection export service is unavailable"))
	}
	return &SnapshotReader{service: service}, nil
}

// ReadCollection 读取一个固定 durable binding；任何定义或 read model 漂移都会显式失败。
func (reader *SnapshotReader) ReadCollection(ctx context.Context, workspaceID foundation.ID, scope domain.Scope, maxItems int) (exportapp.CollectionSnapshot, error) {
	if reader == nil || isNil(reader.service) {
		return exportapp.CollectionSnapshot{}, unavailable(errors.New("collection export reader is unavailable"))
	}
	if ctx == nil || !validID(workspaceID) || scope.Kind != domain.ScopeCollection || scope.CollectionID == nil || scope.CollectionVersion == nil ||
		!validID(*scope.CollectionID) || *scope.CollectionVersion < 1 || !validHash(scope.QueryHash) || maxItems < 1 {
		return exportapp.CollectionSnapshot{}, invalid(errors.New("collection export scope is invalid"))
	}

	collection, err := reader.service.Get(ctx, workspaceID, *scope.CollectionID)
	if err != nil {
		return exportapp.CollectionSnapshot{}, err
	}
	if collection.WorkspaceID != workspaceID || collection.ID != *scope.CollectionID || collection.Version != *scope.CollectionVersion || collection.QueryHash != scope.QueryHash {
		return exportapp.CollectionSnapshot{}, inconsistent(errors.New("collection export definition changed"))
	}
	binding, err := reader.service.PlanDurableScan(ctx, workspaceID, *scope.CollectionID)
	if err != nil {
		return exportapp.CollectionSnapshot{}, err
	}
	if binding.WorkspaceID != workspaceID || binding.CollectionID != *scope.CollectionID || binding.CollectionVersion != *scope.CollectionVersion ||
		binding.QueryHash != scope.QueryHash || !validHash(binding.ReadModelRevision) || binding.ExactCount < 0 {
		return exportapp.CollectionSnapshot{}, inconsistent(errors.New("collection export durable binding changed"))
	}
	if binding.ExactCount > int64(maxItems) {
		return exportapp.CollectionSnapshot{}, invalid(errors.New("collection export exceeds the item limit"))
	}

	items := make([]exportapp.Item, 0, int(binding.ExactCount))
	var after *collectionapp.DurableScanKey
	for {
		remaining := maxItems - len(items)
		limit := min(durablePageLimit, remaining)
		if limit < 1 {
			return exportapp.CollectionSnapshot{}, inconsistent(errors.New("collection export exceeded the frozen count"))
		}
		page, readErr := reader.service.ReadDurableScanPage(ctx, collectionapp.DurableScanPageRequest{
			Binding: binding, After: cloneKey(after), Limit: limit,
		})
		if readErr != nil {
			return exportapp.CollectionSnapshot{}, readErr
		}
		if page.Binding != binding || len(page.Items) > limit || page.Complete != (page.Next == nil) || len(page.Items) == 0 && !page.Complete {
			return exportapp.CollectionSnapshot{}, inconsistent(errors.New("collection export page is inconsistent"))
		}
		for _, item := range page.Items {
			mapped, mapErr := mapItem(item)
			if mapErr != nil {
				return exportapp.CollectionSnapshot{}, mapErr
			}
			items = append(items, mapped)
		}
		if len(items) > maxItems || int64(len(items)) > binding.ExactCount {
			return exportapp.CollectionSnapshot{}, inconsistent(errors.New("collection export page crossed its frozen count"))
		}
		if page.Complete {
			break
		}
		after = cloneKey(page.Next)
	}
	if int64(len(items)) != binding.ExactCount {
		return exportapp.CollectionSnapshot{}, inconsistent(errors.New("collection export item count changed"))
	}
	collectionID, collectionVersion := binding.CollectionID, binding.CollectionVersion
	return exportapp.CollectionSnapshot{
		WorkspaceID: workspaceID, CollectionID: &collectionID, CollectionVersion: &collectionVersion,
		QueryHash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount,
		Name: collection.Name, Items: items,
	}, nil
}

func mapItem(value collectionapp.CollectionItem) (exportapp.Item, error) {
	if !validID(value.ID) || value.ObjectType == "" || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		return exportapp.Item{}, inconsistent(errors.New("collection export item is invalid"))
	}
	result := exportapp.Item{
		ObjectType: value.ObjectType, ID: value.ID, TopicID: cloneID(value.TopicID), Title: value.Title, Summary: value.Summary,
		Status: value.Status, Confidence: cloneFloat(value.Confidence), Applicability: append([]byte(nil), value.Applicability...),
		ApplicabilitySchemaVersion: value.ApplicabilitySchemaVersion, ApplicabilityHash: value.ApplicabilityHash,
		Aliases: append([]string(nil), value.Aliases...), Relations: append([]string(nil), value.RelationTypes...),
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	for _, source := range value.SourceSummaries {
		result.Sources = append(result.Sources, exportapp.Source{Type: source.SourceType, Path: source.FilePath, Support: source.SupportType, CreatedAt: source.CreatedAt})
	}
	if value.HealthSummary != nil {
		result.Health = &exportapp.Health{Count: value.HealthSummary.Count, MaxSeverity: value.HealthSummary.MaxSeverity, IssueTypes: append([]string(nil), value.HealthSummary.IssueTypes...), Summary: value.HealthSummary.Summary}
	}
	return result, nil
}

func cloneKey(value *collectionapp.DurableScanKey) *collectionapp.DurableScanKey {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, cause)
}

func inconsistent(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInvalid, false, cause)
}
