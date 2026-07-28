// Package scheduler 提供可替换的间隔重复调度 Adapter。
package scheduler

import (
	"errors"
	"math"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

// FSRSParameters 只选择已冻结的参数版本；权重不接受运行时覆盖。
type FSRSParameters struct {
	Version string
}

// FSRSAdapter 是一个无状态、确定性的 FSRS 调度实现。
type FSRSAdapter struct {
	parameters FSRSParameters
}

// NewFSRSAdapter 创建默认 FSRS v1 Adapter。
func NewFSRSAdapter(parameters ...FSRSParameters) (*FSRSAdapter, error) {
	selected := defaultFSRSParameters()
	if len(parameters) > 1 {
		return nil, errors.New("fsrs accepts at most one parameter set")
	}
	if len(parameters) == 1 {
		selected = parameters[0]
	}
	if selected.Version == "" {
		selected.Version = domain.SchedulerVersionFSRSV1
	}
	if selected.Version != domain.SchedulerVersionFSRSV1 {
		return nil, errors.New("unsupported fsrs scheduler version")
	}
	return &FSRSAdapter{parameters: selected}, nil
}

// Version 返回调度参数版本。
func (adapter *FSRSAdapter) Version() string {
	if adapter == nil || adapter.parameters.Version == "" {
		return domain.SchedulerVersionFSRSV1
	}
	return adapter.parameters.Version
}

// Next 根据评分计算下一次复习时间和 FSRS 状态。
func (adapter *FSRSAdapter) Next(input domain.ScheduleInput) (domain.ScheduleDecision, error) {
	if adapter == nil {
		return domain.ScheduleDecision{}, errors.New("fsrs adapter is nil")
	}
	now := input.Now.UTC()
	if now.IsZero() || input.Rating < domain.RatingAgain || input.Rating > domain.RatingEasy {
		return domain.ScheduleDecision{}, errors.New("fsrs input is invalid")
	}
	if input.Previous == nil {
		return adapter.firstReview(now, input.Rating), nil
	}
	previous := *input.Previous
	if err := domain.ValidateSchedule(previous); err != nil {
		return domain.ScheduleDecision{}, err
	}
	if previous.SchedulerVersion != "" && previous.SchedulerVersion != adapter.Version() {
		return domain.ScheduleDecision{}, errors.New("schedule belongs to another scheduler version")
	}
	stability := previous.Stability
	if stability < 0.1 {
		stability = 0.1
	}
	difficulty := clamp(previous.Difficulty, 0.1, 0.9)
	if previous.LastReviewedAt != nil {
		elapsed := now.Sub(previous.LastReviewedAt.UTC()).Hours() / 24
		if elapsed > 0 {
			retrievability := math.Exp(-elapsed / stability)
			stability *= 1 + (1-retrievability)*0.25
		}
	}
	switch input.Rating {
	case domain.RatingAgain:
		stability = math.Max(0.1, stability*0.35)
		difficulty = clamp(difficulty+0.12, 0.1, 0.95)
	case domain.RatingHard:
		stability = math.Max(stability*1.2, stability+0.15)
		difficulty = clamp(difficulty+0.04, 0.1, 0.95)
	case domain.RatingGood:
		stability = math.Max(stability*2.0, stability+0.4)
		difficulty = clamp(difficulty-0.02, 0.1, 0.95)
	case domain.RatingEasy:
		stability = math.Max(stability*3.0, stability+0.8)
		difficulty = clamp(difficulty-0.08, 0.1, 0.95)
	}
	interval := stability
	if input.Rating == domain.RatingAgain {
		interval = 10.0 / (24 * 60)
	} else {
		// Difficulty only dampens the growth; the result remains deterministic.
		interval *= 1.0 - 0.35*difficulty
		if input.Rating == domain.RatingEasy {
			interval *= 1.15
		}
		interval = math.Max(interval, 0.25)
	}
	dueAt := now.Add(time.Duration(interval * float64(24*time.Hour)))
	if !dueAt.After(now) {
		dueAt = now.Add(time.Minute)
	}
	return domain.ScheduleDecision{DueAt: dueAt, IntervalDays: interval, Stability: stability, Difficulty: difficulty, SchedulerVersion: adapter.Version()}, nil
}

func (adapter *FSRSAdapter) firstReview(now time.Time, rating domain.Rating) domain.ScheduleDecision {
	initialStability := [...]float64{0.2, 1.0, 3.0, 7.0}
	initialDifficulty := [...]float64{0.85, 0.7, 0.5, 0.3}
	index := int(rating) - 1
	stability := initialStability[index]
	interval := stability
	if rating == domain.RatingAgain {
		interval = 10.0 / (24 * 60)
	}
	dueAt := now.Add(time.Duration(interval * float64(24*time.Hour)))
	return domain.ScheduleDecision{DueAt: dueAt, IntervalDays: interval, Stability: stability, Difficulty: initialDifficulty[index], SchedulerVersion: adapter.Version()}
}

func defaultFSRSParameters() FSRSParameters {
	return FSRSParameters{Version: domain.SchedulerVersionFSRSV1}
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
