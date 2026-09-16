package application

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	SynthesisGoalDiscovering  = "DISCOVERING"
	SynthesisGoalCatalogReady = "CATALOG_READY"
)

// 目标请求与 source-ready 投递独立。目录就绪只表示枚举完成，
// 不表示笔记已生成或已发布。
type SynthesisGoalRequest struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	Goal           string
	Status         string
	AfterSourceID  foundation.ID
	CatalogBatches int64
	NextCheckAt    time.Time
	ErrorCode      string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateSynthesisGoalCommand struct {
	WorkspaceID    foundation.ID
	Goal           string
	IdempotencyKey string
}

func (c CreateSynthesisGoalCommand) Validate() error {
	if ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || c.Goal == "" || c.Goal != strings.TrimSpace(c.Goal) || len(c.Goal) > 2048 || !utf8.ValidString(c.Goal) {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis goal is invalid")
	}
	for _, r := range c.Goal {
		if unicode.IsControl(r) {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis goal contains control characters")
		}
	}
	return nil
}

// 目录绑定保存不可变身份。元数据从精确的 ProfileRevision 重新读取，
// 不复制可变的画像投影。
type SynthesisGoalCatalogBinding struct {
	Source            domain.SynthesisSourceVersion
	ProfileRevisionID foundation.ID
	Title             string
}

type SynthesisGoalCatalogBatch struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	RequestID         foundation.ID
	BatchNo           int64
	AfterSourceID     foundation.ID
	NextAfterSourceID foundation.ID
	Items             []SynthesisGoalCatalogBinding
	CreatedAt         time.Time
}

type SynthesisGoalCreateResult struct {
	Request  SynthesisGoalRequest
	Replayed bool
}

type SynthesisGoalFreezeResult struct {
	Request  SynthesisGoalRequest
	Batch    SynthesisGoalCatalogBatch
	Replayed bool
}

type SynthesisGoalRequestStore interface {
	CreateSynthesisGoal(context.Context, CreateSynthesisGoalCommand) (SynthesisGoalCreateResult, error)
	GetSynthesisGoal(context.Context, foundation.ID, foundation.ID) (SynthesisGoalRequest, error)
	ListDiscoveringSynthesisGoals(context.Context, foundation.ID, int) ([]SynthesisGoalRequest, error)
	FreezeSynthesisGoalCatalog(context.Context, SynthesisGoalRequest, SynthesisGoalCatalogPage) (SynthesisGoalFreezeResult, error)
	GetSynthesisGoalCatalogBatch(context.Context, foundation.ID, foundation.ID, int64) (SynthesisGoalCatalogBatch, error)
	DeferSynthesisGoal(context.Context, SynthesisGoalRequest, string) error
}
