package domain

// IndexStatus 是一个不可变 Index Version 的生命周期状态。
type IndexStatus string

const (
	// IndexStatusBuilding 表示 Manifest 已冻结且投影仍在构建。
	IndexStatusBuilding IndexStatus = "building"
	// IndexStatusReady 表示投影完整但尚未成为查询事实源。
	IndexStatusReady IndexStatus = "ready"
	// IndexStatusActive 表示该版本是 Workspace 当前查询事实源。
	IndexStatusActive IndexStatus = "active"
	// IndexStatusRetiring 表示该版本已被新 Active 替换但仍可用于回滚。
	IndexStatusRetiring IndexStatus = "retiring"
	// IndexStatusArchived 表示该版本已结束观察窗口且不可重新激活。
	IndexStatusArchived IndexStatus = "archived"
	// IndexStatusFailed 表示构建失败且未影响现有 Active。
	IndexStatusFailed IndexStatus = "failed"
)

var indexTransitions = map[IndexStatus]map[IndexStatus]struct{}{
	IndexStatusBuilding: {
		IndexStatusReady:  {},
		IndexStatusFailed: {},
	},
	IndexStatusActive: {
		IndexStatusRetiring: {},
	},
	IndexStatusRetiring: {
		IndexStatusArchived: {},
	},
}

// ValidateIndexTransition 校验通用状态迁移；进入 Active 必须走激活专用用例。
func ValidateIndexTransition(from, to IndexStatus) error {
	if targets, ok := indexTransitions[from]; ok {
		if _, accepted := targets[to]; accepted {
			return nil
		}
	}
	return conflict(ErrorCodeIndexTransitionInvalid, "index state transition is not allowed")
}

// IsValidIndexStatus 判断状态是否属于冻结的 Index 生命周期枚举。
func IsValidIndexStatus(status IndexStatus) bool {
	switch status {
	case IndexStatusBuilding, IndexStatusReady, IndexStatusActive, IndexStatusRetiring, IndexStatusArchived, IndexStatusFailed:
		return true
	default:
		return false
	}
}

// IsTerminalIndexStatus 判断 Index 是否已不可继续迁移。
func IsTerminalIndexStatus(status IndexStatus) bool {
	return status == IndexStatusArchived || status == IndexStatusFailed
}
