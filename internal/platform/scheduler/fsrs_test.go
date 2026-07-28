package scheduler

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

func TestFSRSFirstReviewOrdersRatingsAndPreservesVersion(t *testing.T) {
	adapter, err := NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	var previousDue time.Time
	for rating := domain.RatingAgain; rating <= domain.RatingEasy; rating++ {
		decision, nextErr := adapter.Next(domain.ScheduleInput{Now: now, Rating: rating})
		if nextErr != nil {
			t.Fatalf("rating %d: %v", rating, nextErr)
		}
		if decision.SchedulerVersion != domain.SchedulerVersionFSRSV1 || !decision.DueAt.After(now) {
			t.Fatalf("rating %d produced invalid decision: %+v", rating, decision)
		}
		if rating > domain.RatingAgain && !decision.DueAt.After(previousDue) {
			t.Fatalf("rating %d due=%s is not after previous=%s", rating, decision.DueAt, previousDue)
		}
		previousDue = decision.DueAt
	}
}

func TestFSRSRepeatAgainShortensAndEasyExtends(t *testing.T) {
	adapter, err := NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	last := now.Add(-72 * time.Hour)
	previous := &domain.Schedule{
		CardID: foundation.ID("10000000-0000-4000-8000-000000000001"), WorkspaceID: foundation.ID("10000000-0000-4000-8000-000000000002"),
		DueAt: now.Add(-24 * time.Hour), IntervalDays: 3, Stability: 3, Difficulty: 0.5,
		LastReviewedAt: &last, SchedulerVersion: domain.SchedulerVersionFSRSV1, Version: 2,
	}
	again, err := adapter.Next(domain.ScheduleInput{Now: now, Rating: domain.RatingAgain, Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	easy, err := adapter.Next(domain.ScheduleInput{Now: now, Rating: domain.RatingEasy, Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	if again.IntervalDays >= previous.IntervalDays || easy.IntervalDays <= previous.IntervalDays || !easy.DueAt.After(again.DueAt) {
		t.Fatalf("again=%+v easy=%+v previous=%+v", again, easy, previous)
	}
}

func TestFSRSRejectsSchedulerVersionDrift(t *testing.T) {
	adapter, err := NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	previous := &domain.Schedule{
		CardID: foundation.ID("10000000-0000-4000-8000-000000000001"), WorkspaceID: foundation.ID("10000000-0000-4000-8000-000000000002"),
		DueAt: time.Now().UTC().Add(time.Hour), IntervalDays: 1, Stability: 1, Difficulty: 0.5,
		SchedulerVersion: "fsrs/v2", Version: 1,
	}
	if _, err := adapter.Next(domain.ScheduleInput{Now: time.Now().UTC(), Rating: domain.RatingGood, Previous: previous}); err == nil {
		t.Fatal("scheduler version drift was accepted")
	}
}

func TestFSRSRejectsUnknownParameterVersion(t *testing.T) {
	if _, err := NewFSRSAdapter(FSRSParameters{Version: "fsrs/v2"}); err == nil {
		t.Fatal("unknown fsrs parameter version was accepted")
	}
}
