package postgres

import (
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
)

// collectionModel 显式映射 Atlas 管理的聚合，不启用默认时间或关联写入。
type collectionModel struct {
	ID                  string          `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID         string          `gorm:"column:workspace_id;type:uuid"`
	Name                string          `gorm:"column:name"`
	NormalizedName      string          `gorm:"column:normalized_name"`
	Description         string          `gorm:"column:description"`
	QuerySchemaVersion  string          `gorm:"column:query_schema_version"`
	QueryVersion        int64           `gorm:"column:query_version"`
	QueryDefinition     collectionJSONB `gorm:"column:query_definition;type:jsonb"`
	QueryHash           string          `gorm:"column:query_hash"`
	ViewType            string          `gorm:"column:view_type"`
	ViewConfig          collectionJSONB `gorm:"column:view_config;type:jsonb"`
	Status              string          `gorm:"column:status"`
	CachedResultVersion *string         `gorm:"column:cached_result_version"`
	LastExecutedAt      *time.Time      `gorm:"column:last_executed_at"`
	Version             int64           `gorm:"column:version"`
	CreatedAt           time.Time       `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt           time.Time       `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (collectionModel) TableName() string { return "learning.smart_collection" }

// collectionCommandModel 只写入不可变回执，历史响应仍由统一 codec 校验。
type collectionCommandModel struct {
	WorkspaceID       string          `gorm:"column:workspace_id;type:uuid;primaryKey"`
	IdempotencyKey    string          `gorm:"column:idempotency_key;primaryKey"`
	RequestHash       string          `gorm:"column:request_hash"`
	CommandType       string          `gorm:"column:command_type"`
	CollectionID      string          `gorm:"column:collection_id;type:uuid"`
	CollectionVersion int64           `gorm:"column:collection_version"`
	Receipt           collectionJSONB `gorm:"column:receipt;type:jsonb"`
	CreatedAt         time.Time       `gorm:"column:created_at;autoCreateTime:false"`
}

func (collectionCommandModel) TableName() string { return "learning.smart_collection_command" }

func collectionModelFrom(collection collectionapp.Collection) collectionModel {
	return collectionModel{
		ID: string(collection.ID), WorkspaceID: string(collection.WorkspaceID),
		Name: collection.Name, NormalizedName: collection.NormalizedName, Description: collection.Description,
		QuerySchemaVersion: collection.QuerySchemaVersion, QueryVersion: collection.QueryVersion,
		QueryDefinition: collectionJSONB(queryDefinition(collection)), QueryHash: collection.QueryHash,
		ViewType: string(collection.ViewType), ViewConfig: collectionJSONB(viewConfig(collection)),
		Status: string(collection.Status), CachedResultVersion: collection.CachedResultVersion,
		LastExecutedAt: collection.LastExecutedAt, Version: collection.Version,
		CreatedAt: collection.CreatedAt.UTC(), UpdatedAt: collection.UpdatedAt.UTC(),
	}
}

func collectionCommandModelFrom(collection collectionapp.Collection, key, commandType, requestHash string, at time.Time) collectionCommandModel {
	return collectionCommandModel{
		WorkspaceID: string(collection.WorkspaceID), IdempotencyKey: key, RequestHash: requestHash,
		CommandType: commandType, CollectionID: string(collection.ID), CollectionVersion: collection.Version,
		Receipt: collectionJSONB(commandReceiptJSON(collection, key, commandType, requestHash)), CreatedAt: at.UTC(),
	}
}
