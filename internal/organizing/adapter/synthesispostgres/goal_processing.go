package synthesispostgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"gorm.io/gorm"
)

func matchesGoal(id foundation.ID, binding *app.SynthesisGoalBinding) bool {
	if id == "" {
		return binding == nil
	}
	return binding != nil && binding.RequestID == id
}

// 旧版来源或融合处理省略新增可空列；刻意运行较早迁移的隔离兼容测试也遵循此规则。
func createProcessing(tx *gorm.DB, row *processingModel) error {
	var omitted []string
	if row.BodyRefreshRequestID == nil {
		omitted = append(omitted, "BodyRefreshRequestID")
	}
	if row.GoalRequestID == nil {
		omitted = append(omitted, "GoalRequestID")
	}
	if len(omitted) > 0 {
		tx = tx.Omit(omitted...)
	}
	return tx.Create(row).Error
}

func (store *Store) FindSynthesisGoalProcessingScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID, goalID foundation.ID) (app.SynthesisProcessing, bool, error) {
	if !validID(workspaceID) || !validID(goalID) {
		return app.SynthesisProcessing{}, false, invalid("synthesis goal identity is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return app.SynthesisProcessing{}, false, err
	}
	var row processingModel
	err = tx.Where("workspace_id=? AND goal_request_id=?", string(workspaceID), string(goalID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return app.SynthesisProcessing{}, false, nil
	}
	if err != nil {
		return app.SynthesisProcessing{}, false, classify(ctx, err)
	}
	result, err := row.projection()
	return result, err == nil, err
}

// ReadSynthesisGoalProcessingBatchScoped 在调用方事务内，为有界目标页投影持久化生成账本。目标视图所属模块使用此投影，而不从存储列重建处理状态。
func (store *Store) ReadSynthesisGoalProcessingBatchScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID foundation.ID, goalIDs []foundation.ID) (map[foundation.ID]app.SynthesisProcessing, error) {
	result := make(map[foundation.ID]app.SynthesisProcessing, len(goalIDs))
	if err := store.ready(ctx); err != nil {
		return nil, err
	}
	if !validID(workspaceID) || len(goalIDs) > app.MaxSynthesisListLimit {
		return nil, invalid("synthesis goal processing batch is invalid")
	}
	if len(goalIDs) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(goalIDs))
	seen := make(map[foundation.ID]struct{}, len(goalIDs))
	for _, goalID := range goalIDs {
		if !validID(goalID) {
			return nil, invalid("synthesis goal processing batch is invalid")
		}
		if _, exists := seen[goalID]; exists {
			return nil, invalid("synthesis goal processing batch repeats a goal")
		}
		seen[goalID] = struct{}{}
		ids = append(ids, string(goalID))
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return nil, err
	}
	var rows []processingModel
	if err := tx.Where("workspace_id=? AND goal_request_id IN ?", string(workspaceID), ids).Find(&rows).Error; err != nil {
		return nil, classify(ctx, err)
	}
	for _, row := range rows {
		value, err := row.projection()
		if err != nil {
			return nil, err
		}
		if value.GoalRequestID == "" {
			return nil, invalid("synthesis goal processing lost its goal identity")
		}
		if _, requested := seen[value.GoalRequestID]; !requested {
			return nil, invalid("synthesis goal processing escaped its batch")
		}
		if _, duplicate := result[value.GoalRequestID]; duplicate {
			return nil, invalid("synthesis goal has more than one processing record")
		}
		result[value.GoalRequestID] = value
	}
	return result, nil
}

// 成功选择及其目录、Profile 快照均不可变。冻结前在写事务外验证；应用时在笔记加锁前重新读取相同完整证明。此处不调用提供方，也不读取原始文件。
func (store *Store) verifyGoal(ctx context.Context, workspaceID foundation.ID, goal *app.SynthesisGoalBinding) error {
	if goal == nil {
		return nil
	}
	if store == nil || ctx == nil || nilDependency(store.dependencies.Goals) {
		return invalid("synthesis goal proof verifier is unavailable")
	}
	return store.dependencies.Goals.VerifySynthesisGoalBinding(ctx, workspaceID, goal)
}
