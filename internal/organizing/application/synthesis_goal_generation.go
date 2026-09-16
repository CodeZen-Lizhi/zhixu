package application

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisGoalGenerationSeed 标识不可变且全部选择完成的目标，
// 以及选中来源中的一个真实摄取事件。它不是生成输入；准备阶段会重新打开全部选中证据。
type SynthesisGoalGenerationSeed struct {
	GoalRequestID foundation.ID
	SourceEvent   domain.SynthesisSourceReady
}
