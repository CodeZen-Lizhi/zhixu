package application

import "github.com/CodeZen-Lizhi/zhixu/internal/health/domain"

const repairOwnerUnavailableReason = "repair apply owner is not implemented"

var unavailableRepairOptions = map[domain.IssueType]domain.RepairOption{
	domain.IssueTypeOrphan:          {Code: "health.repair.connect-orphan", Title: "连接孤立知识", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeDuplicate:       {Code: "health.repair.merge-duplicate", Title: "合并重复知识", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeConflict:        {Code: "health.repair.resolve-conflict", Title: "解决知识冲突", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeStale:           {Code: "health.repair.replace-stale", Title: "替换过期引用", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeMissingSource:   {Code: "health.repair.bind-source", Title: "绑定支持来源", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeLowConfidence:   {Code: "health.repair.review-confidence", Title: "复核置信度", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeBrokenReference: {Code: "health.repair.restore-reference", Title: "修复损坏引用", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeIndexError:      {Code: "health.repair.rebuild-index", Title: "重建索引投影", UnavailableReason: repairOwnerUnavailableReason},
	domain.IssueTypeSupersededUsage: {Code: "health.repair.replace-superseded", Title: "替换已取代引用", UnavailableReason: repairOwnerUnavailableReason},
}

// RepairOptionsForIssue 返回首版真实 owner 能力；尚无 apply owner 的修复显式 unavailable。
func RepairOptionsForIssue(issueType domain.IssueType) []domain.RepairOption {
	option, found := unavailableRepairOptions[issueType]
	if !found {
		return nil
	}
	return []domain.RepairOption{option}
}
