// Package domain 定义异步导出的稳定领域契约。
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Kind 是 M9-03 已交付的导出内容类型。
type Kind string

const (
	KindMarkdown       Kind = "MARKDOWN"
	KindMetadataJSON   Kind = "METADATA_JSON"
	KindAttachmentsZIP Kind = "ATTACHMENTS_ZIP"
)

// ScopeKind 区分 Collection 投影与 Workspace 原始附件两种事实范围。
type ScopeKind string

const (
	ScopeCollection           ScopeKind = "COLLECTION"
	ScopeWorkspaceAttachments ScopeKind = "WORKSPACE_ATTACHMENTS"
)

// Status 是导出任务生命周期状态。
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusRunning   Status = "RUNNING"
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"
	StatusExpired   Status = "EXPIRED"
	StatusCancelled Status = "CANCELLED"
)

// CleanupStatus 是过期结果物理清理的持久状态。
type CleanupStatus string

const (
	// CleanupNotRequired 表示任务尚未到期，不应执行物理清理。
	CleanupNotRequired CleanupStatus = "NOT_REQUIRED"
	// CleanupPending 表示任务已到期，等待清理持久化路径。
	CleanupPending CleanupStatus = "PENDING"
	// CleanupFailed 表示最近一次物理清理失败，可由后台任务重试。
	CleanupFailed CleanupStatus = "FAILED"
	// CleanupSucceeded 表示持久化路径均已确认不存在。
	CleanupSucceeded CleanupStatus = "SUCCEEDED"
)

// RedactionPolicy 控制导出中可能包含敏感字段的处理方式。
type RedactionPolicy string

const (
	RedactionMasked       RedactionPolicy = "MASKED"
	RedactionFull         RedactionPolicy = "FULL"
	RedactionRawUserOwned RedactionPolicy = "RAW_USER_OWNED"
)

// Scope 描述导出绑定的事实范围。
type Scope struct {
	Kind                          ScopeKind
	CollectionID                  *foundation.ID
	CollectionVersion             *int64
	QueryHash                     string
	AttachmentRootContractVersion string
}

// Field 是允许导出的领域字段白名单成员。
type Field string

const (
	FieldObjectType    Field = "object_type"
	FieldID            Field = "id"
	FieldTitle         Field = "title"
	FieldSummary       Field = "summary"
	FieldStatus        Field = "status"
	FieldTopic         Field = "topic"
	FieldSource        Field = "source"
	FieldRelations     Field = "relations"
	FieldHealth        Field = "health"
	FieldConfidence    Field = "confidence"
	FieldCreatedAt     Field = "created_at"
	FieldUpdatedAt     Field = "updated_at"
	FieldApplicability Field = "applicability"
)

// CreateRequest 是创建导出任务的应用命令。
type CreateRequest struct {
	WorkspaceID      foundation.ID
	Kind             Kind
	SchemaVersion    string
	Scope            Scope
	Fields           []Field
	Redaction        RedactionPolicy
	IncludeSensitive bool
	IdempotencyKey   string
	RequestedBy      string
	PermissionScope  string
	ExpiresIn        time.Duration
}

// Job 是导出任务的持久化事实和结果追踪投影。
type Job struct {
	ID                     foundation.ID
	WorkspaceID            foundation.ID
	Kind                   Kind
	SchemaVersion          string
	Scope                  Scope
	Fields                 []Field
	Redaction              RedactionPolicy
	IncludeSensitive       bool
	PermissionScope        string
	RequestedBy            string
	IdempotencyKey         string
	RequestHash            string
	RequestTTLSeconds      int64
	Status                 Status
	Version                int64
	ReadModelRevision      string
	ExactCount             *int64
	ManifestHash           string
	EntryCount             *int64
	TotalUncompressedBytes *int64
	PreparedAt             *time.Time
	PreparedStagingPath    string
	FilePath               string
	FileHash               string
	FileSize               int64
	ErrorCode              string
	ErrorMessage           string
	AttemptCount           int
	LeaseOwner             string
	LeaseExpiresAt         *time.Time
	ExpiresAt              time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	StartedAt              *time.Time
	CompletedAt            *time.Time
	DownloadCount          int
	LastDownloadedAt       *time.Time
	CleanupStatus          CleanupStatus
	CleanupAttemptCount    int
	CleanupError           string
	CleanupUpdatedAt       *time.Time
	FileDeletedAt          *time.Time
}

// Validate 校验任务事实的跨层不变量。
func (j Job) Validate() error {
	if !validID(j.ID) || !validID(j.WorkspaceID) || !validKind(j.Kind) || strings.TrimSpace(j.SchemaVersion) == "" ||
		!validStatus(j.Status) || strings.TrimSpace(j.IdempotencyKey) == "" || len(j.IdempotencyKey) > 128 ||
		!validHash(j.RequestHash) || j.RequestTTLSeconds < 1 || j.RequestTTLSeconds > 7*24*60*60 || j.Version < 1 ||
		j.ExpiresAt.IsZero() || j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() || j.UpdatedAt.Before(j.CreatedAt) ||
		!j.ExpiresAt.After(j.CreatedAt) || j.AttemptCount < 0 || j.DownloadCount < 0 || j.CleanupAttemptCount < 0 ||
		!validCleanupStatus(j.CleanupStatus) {
		return errors.New("export job is invalid")
	}
	if err := j.validateScopeAndPolicy(); err != nil {
		return err
	}
	seenFields := make(map[Field]struct{}, len(j.Fields))
	for _, field := range j.Fields {
		if !ValidField(field) {
			return errors.New("export fields contain an unknown value")
		}
		if _, exists := seenFields[field]; exists {
			return errors.New("export fields contain a duplicate")
		}
		seenFields[field] = struct{}{}
	}
	prepared := j.PreparedAt != nil
	if prepared != (j.ReadModelRevision != "" || j.ExactCount != nil || j.ManifestHash != "" || j.EntryCount != nil ||
		j.TotalUncompressedBytes != nil || j.PreparedStagingPath != "" || j.FilePath != "" || j.FileHash != "") {
		return errors.New("export prepared result binding is incomplete")
	}
	if prepared {
		if !validHash(j.FileHash) || j.FileSize < 0 || strings.TrimSpace(j.PreparedStagingPath) == "" || strings.TrimSpace(j.FilePath) == "" ||
			j.PreparedAt.Before(j.CreatedAt) || j.PreparedAt.After(j.UpdatedAt) {
			return errors.New("export prepared result binding is invalid")
		}
		if j.Scope.Kind == ScopeCollection && (!validHash(j.ReadModelRevision) || j.ExactCount == nil || *j.ExactCount < 0 || *j.ExactCount > 10_000 ||
			j.ManifestHash != "" || j.EntryCount != nil || j.TotalUncompressedBytes != nil) {
			return errors.New("collection export prepared result binding is invalid")
		}
		if j.Scope.Kind == ScopeWorkspaceAttachments && (j.ReadModelRevision != "" || j.ExactCount != nil || !validHash(j.ManifestHash) ||
			j.EntryCount == nil || *j.EntryCount < 0 || *j.EntryCount > 10_000 || j.TotalUncompressedBytes == nil || *j.TotalUncompressedBytes < 0 ||
			*j.TotalUncompressedBytes > 1<<30 || j.FileSize > 1<<30) {
			return errors.New("attachment export prepared result binding is invalid")
		}
	} else if j.FileSize != 0 {
		return errors.New("unprepared export cannot carry a file size")
	}
	if j.Status == StatusRunning {
		if strings.TrimSpace(j.LeaseOwner) == "" || j.LeaseExpiresAt == nil {
			return errors.New("running export has no lease")
		}
	} else if j.LeaseOwner != "" || j.LeaseExpiresAt != nil {
		return errors.New("non-running export retains a lease")
	}
	if j.StartedAt != nil && (j.StartedAt.Before(j.CreatedAt) || j.StartedAt.After(j.UpdatedAt)) ||
		j.CompletedAt != nil && (j.CompletedAt.Before(j.CreatedAt) || j.CompletedAt.After(j.UpdatedAt)) ||
		j.LastDownloadedAt != nil && (j.LastDownloadedAt.Before(j.CreatedAt) || j.LastDownloadedAt.After(j.UpdatedAt)) {
		return errors.New("export lifecycle timestamp is invalid")
	}
	if (j.DownloadCount == 0) != (j.LastDownloadedAt == nil) {
		return errors.New("export download statistics are inconsistent")
	}
	hasFailure := strings.TrimSpace(j.ErrorCode) != "" || strings.TrimSpace(j.ErrorMessage) != ""
	switch j.Status {
	case StatusPending:
		if prepared || j.CompletedAt != nil || hasFailure {
			return errors.New("pending export has terminal or prepared fields")
		}
	case StatusRunning:
		if j.StartedAt == nil || j.CompletedAt != nil || hasFailure {
			return errors.New("running export lifecycle is invalid")
		}
	case StatusSucceeded:
		if !prepared || j.StartedAt == nil || j.CompletedAt == nil || hasFailure {
			return errors.New("successful export lifecycle is invalid")
		}
	case StatusFailed:
		if j.StartedAt == nil || j.CompletedAt == nil || strings.TrimSpace(j.ErrorCode) == "" || strings.TrimSpace(j.ErrorMessage) == "" {
			return errors.New("failed export lifecycle is invalid")
		}
	case StatusExpired:
		if j.CompletedAt == nil || hasFailure {
			return errors.New("expired export lifecycle is invalid")
		}
	case StatusCancelled:
		// 历史 CANCELLED 可缺少 started/completed 时间，但不得伪装失败。
		if hasFailure {
			return errors.New("cancelled export cannot carry failure fields")
		}
	}
	if j.Status == StatusExpired {
		if j.CleanupStatus == CleanupNotRequired {
			return errors.New("expired export has no cleanup state")
		}
	} else if j.CleanupStatus != CleanupNotRequired {
		return errors.New("active export has an invalid cleanup state")
	}
	if (j.CleanupStatus == CleanupSucceeded) != (j.FileDeletedAt != nil) {
		return errors.New("export cleanup completion binding is invalid")
	}
	if j.CleanupUpdatedAt != nil && (j.CleanupUpdatedAt.Before(j.CreatedAt) || j.CleanupUpdatedAt.After(j.UpdatedAt)) ||
		j.FileDeletedAt != nil && (j.FileDeletedAt.Before(j.CreatedAt) || j.FileDeletedAt.After(j.UpdatedAt)) {
		return errors.New("export cleanup timestamp is invalid")
	}
	switch j.CleanupStatus {
	case CleanupNotRequired, CleanupPending:
		if j.CleanupAttemptCount != 0 || j.CleanupUpdatedAt != nil || j.CleanupError != "" || j.FileDeletedAt != nil {
			return errors.New("export cleanup pending fields are invalid")
		}
	case CleanupFailed:
		if j.CleanupAttemptCount < 1 || j.CleanupUpdatedAt == nil || j.FileDeletedAt != nil {
			return errors.New("failed export cleanup binding is invalid")
		}
	case CleanupSucceeded:
		if j.CleanupAttemptCount < 1 || j.CleanupUpdatedAt == nil || j.FileDeletedAt == nil || !j.FileDeletedAt.Equal(*j.CleanupUpdatedAt) {
			return errors.New("successful export cleanup binding is invalid")
		}
	}
	if j.CleanupStatus == CleanupFailed && strings.TrimSpace(j.CleanupError) == "" {
		return errors.New("failed export cleanup has no bounded error")
	}
	if j.CleanupStatus != CleanupFailed && j.CleanupError != "" {
		return errors.New("export cleanup error is not attached to a failed cleanup")
	}
	return nil
}

func (j Job) validateScopeAndPolicy() error {
	if strings.TrimSpace(j.PermissionScope) == "" || strings.TrimSpace(j.RequestedBy) == "" {
		return errors.New("export request policy is invalid")
	}
	switch j.Scope.Kind {
	case ScopeCollection:
		if j.Kind != KindMarkdown && j.Kind != KindMetadataJSON || j.SchemaVersion != "export/v1" ||
			j.Scope.CollectionID == nil || !validID(*j.Scope.CollectionID) || j.Scope.CollectionVersion == nil || *j.Scope.CollectionVersion < 1 ||
			!validHash(j.Scope.QueryHash) || j.Scope.AttachmentRootContractVersion != "" || len(j.Fields) == 0 || len(j.Fields) > 32 ||
			(j.Redaction != RedactionMasked && j.Redaction != RedactionFull) || (j.Redaction == RedactionFull) != j.IncludeSensitive {
			return errors.New("export collection scope or policy is invalid")
		}
	case ScopeWorkspaceAttachments:
		if j.Kind != KindAttachmentsZIP || j.SchemaVersion != "attachment-export/v1" || j.Scope.CollectionID != nil ||
			j.Scope.CollectionVersion != nil || j.Scope.QueryHash != "" || j.Scope.AttachmentRootContractVersion != "workspace-attachments/v1" ||
			len(j.Fields) != 0 || j.Redaction != RedactionRawUserOwned || j.IncludeSensitive {
			return errors.New("export attachment scope or policy is invalid")
		}
	default:
		return errors.New("export scope kind is invalid")
	}
	return nil
}

// IsPrepared reports whether a fixed staging/final result binding has been persisted.
func (j Job) IsPrepared() bool { return j.PreparedAt != nil }

// IsTerminal reports whether the job can no longer be executed.
func (j Job) IsTerminal() bool {
	return j.Status == StatusSucceeded || j.Status == StatusFailed || j.Status == StatusExpired || j.Status == StatusCancelled
}

// SameRequest 判断两个任务是否绑定同一个幂等导出请求；任务 ID、状态和时间不属于请求身份。
func SameRequest(left, right Job) bool {
	if left.WorkspaceID != right.WorkspaceID || left.Kind != right.Kind || left.SchemaVersion != right.SchemaVersion ||
		left.Redaction != right.Redaction || left.IncludeSensitive != right.IncludeSensitive ||
		left.PermissionScope != right.PermissionScope || left.RequestedBy != right.RequestedBy ||
		left.IdempotencyKey != right.IdempotencyKey || left.RequestHash != right.RequestHash ||
		left.RequestTTLSeconds != right.RequestTTLSeconds || left.Scope.Kind != right.Scope.Kind ||
		left.Scope.QueryHash != right.Scope.QueryHash || left.Scope.AttachmentRootContractVersion != right.Scope.AttachmentRootContractVersion ||
		!sameOptionalInt64(left.Scope.CollectionVersion, right.Scope.CollectionVersion) ||
		!sameOptionalID(left.Scope.CollectionID, right.Scope.CollectionID) || len(left.Fields) != len(right.Fields) {
		return false
	}
	for index := range left.Fields {
		if left.Fields[index] != right.Fields[index] {
			return false
		}
	}
	return true
}

func sameOptionalID(left, right *foundation.ID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validKind(value Kind) bool {
	switch value {
	case KindMarkdown, KindMetadataJSON, KindAttachmentsZIP:
		return true
	default:
		return false
	}
}

func validStatus(value Status) bool {
	switch value {
	case StatusPending, StatusRunning, StatusSucceeded, StatusFailed, StatusExpired, StatusCancelled:
		return true
	default:
		return false
	}
}

func validCleanupStatus(value CleanupStatus) bool {
	switch value {
	case CleanupNotRequired, CleanupPending, CleanupFailed, CleanupSucceeded:
		return true
	default:
		return false
	}
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

// ValidKind 用于 HTTP/Adapter 边界复用枚举校验。
func ValidKind(value Kind) bool { return validKind(value) }

// ValidStatus 用于 HTTP/Adapter 边界复用枚举校验。
func ValidStatus(value Status) bool { return validStatus(value) }

// ValidField 判断字段是否属于固定导出白名单。
func ValidField(value Field) bool {
	switch value {
	case FieldObjectType, FieldID, FieldTitle, FieldSummary, FieldStatus, FieldTopic, FieldSource,
		FieldRelations, FieldHealth, FieldConfidence, FieldCreatedAt, FieldUpdatedAt,
		FieldApplicability:
		return true
	default:
		return false
	}
}

// DefaultFields 返回不包含敏感正文的安全默认字段。
func DefaultFields(kind Kind) []Field {
	return []Field{FieldObjectType, FieldID, FieldTitle, FieldSummary, FieldStatus, FieldTopic, FieldSource, FieldRelations, FieldHealth, FieldConfidence, FieldCreatedAt, FieldUpdatedAt}
}
