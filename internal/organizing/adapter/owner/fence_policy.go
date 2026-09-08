package owner

import (
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// MaxFrozenCollectionLookups 保持事务 fence 与事务外材料展开的同一有界合同。
const MaxFrozenCollectionLookups = maxSerialOwnerLookups

// SourceMaterialAvailability 让事务 fence 与 owner resolver 共享 Source 可用性规则。
func SourceMaterialAvailability(source retrievaldomain.SourceVersionReference) organizingdomain.MaterialAvailability {
	return sourceAvailability(source)
}
