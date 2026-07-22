package domain

import (
	"testing"
	"time"
)

func TestNextScheduleRunSkipsMissedIntervalsOnce(t *testing.T) {
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	next := time.Date(2026, 7, 2, 3, 30, 0, 0, time.UTC)
	schedule := validScheduleFixture(created, next)
	after := time.Date(2026, 7, 22, 4, 0, 0, 0, time.UTC)
	got, err := NextScheduleRun(schedule, after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 23, 3, 30, 0, 0, time.UTC)
	if got == nil || !got.Equal(want) {
		t.Fatalf("next=%v want=%v", got, want)
	}
}

func TestNextScheduleRunSupportsValidatedCron(t *testing.T) {
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	next := time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)
	schedule := validScheduleFixture(created, next)
	schedule.Cadence = ScheduleCadenceCron
	schedule.CronExpression = "15 */2 * * *"
	got, err := NextScheduleRun(schedule, time.Date(2026, 7, 22, 4, 16, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 22, 6, 15, 0, 0, time.UTC)
	if got == nil || !got.Equal(want) {
		t.Fatalf("next=%v want=%v", got, want)
	}
}

func TestValidateScheduleAllowsMissedLogicalRunBeforeCreation(t *testing.T) {
	created := time.Date(2026, 7, 22, 4, 0, 0, 0, time.UTC)
	last := created.Add(-72 * time.Hour)
	next := created.Add(24 * time.Hour)
	schedule := validScheduleFixture(created, next)
	schedule.LastRunAt = &last
	if err := ValidateSchedule(schedule); err != nil {
		t.Fatal(err)
	}
}

func validScheduleFixture(created, next time.Time) Schedule {
	return Schedule{ID: testID(240), WorkspaceID: testID(241), Scope: ScanScope{Type: ScanScopeTypeTopic, Ref: testID(242), Version: 1, SchemaVersion: "health-scope/topic/v1"}, Cadence: ScheduleCadenceDaily, Timezone: "UTC", MaxItems: 100, NextRunAt: &next, Version: 1, CreatedAt: created, UpdatedAt: created}
}
