package domain

import "time"

// ScheduleInput 是调度 Adapter 的稳定输入。
type ScheduleInput struct {
	Now      time.Time
	Rating   Rating
	Previous *Schedule
}

// ScheduleDecision 是调度 Adapter 计算出的下一状态。
type ScheduleDecision struct {
	DueAt            time.Time
	IntervalDays     float64
	Stability        float64
	Difficulty       float64
	SchedulerVersion string
}

// Scheduler 是可替换的间隔重复调度端口。
type Scheduler interface {
	// Version 返回算法参数版本，历史记录必须保留该值。
	Version() string
	Next(ScheduleInput) (ScheduleDecision, error)
}
