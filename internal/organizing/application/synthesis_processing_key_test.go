package application

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestGoalProcessingIdentitySurvivesSeedChangesWithoutMergingGoals(t *testing.T) {
	prepared, _, _ := goalPreparedFixture(t)
	event := domain.SynthesisSourceReady{ID: prepared.Progress.Request.ID, Source: prepared.Sources[0].Excerpt.Reference.Source, IngestionAttemptID: prepared.Sources[0].Points[0].SelectionID, ProcessorVersion: domain.SynthesisProcessorVersion, CreatedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	goal := prepared.Progress.Request.ID
	first, err := SynthesisProcessingKey(event, goal)
	if err != nil {
		t.Fatal(err)
	}
	// 调度器可以使用另一个选中的溯源种子恢复同一目标，
	// 它仍属于原来的生成身份。
	event.Source = prepared.Sources[1].Excerpt.Reference.Source
	second, err := SynthesisProcessingKey(event, goal)
	if err != nil || second != first {
		t.Fatal("goal identity depends on seed")
	}
	anotherGoal := foundation.ID("10000000-0000-0000-0000-000000000123")
	another, err := SynthesisProcessingKey(event, anotherGoal)
	if err != nil || another == first {
		t.Fatal("two goals sharing a source merged")
	}
	legacy, err := event.ProcessingKey()
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := SynthesisProcessingKey(event, "")
	if err != nil || ordinary != legacy || ordinary == first {
		t.Fatal("ordinary source identity changed")
	}
	if _, err := SynthesisProcessingKey(event, "invalid"); err == nil {
		t.Fatal("invalid goal accepted")
	}
}

func TestBodyRefreshIdentityGroupsDifferentImpactBaselines(t *testing.T) {
	request := SynthesisBodyRefreshRequest{
		ID: foundation.ID("10000000-0000-0000-0000-000000000101"), WorkspaceID: foundation.ID("10000000-0000-0000-0000-000000000102"),
		NoteID: foundation.ID("10000000-0000-0000-0000-000000000103"), PublicationID: foundation.ID("10000000-0000-0000-0000-000000000104"),
		ImpactID: foundation.ID("10000000-0000-0000-0000-000000000105"), CreatedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
	}
	first, err := request.GroupKey()
	if err != nil {
		t.Fatal(err)
	}
	request.ImpactID = foundation.ID("10000000-0000-0000-0000-000000000106")
	second, err := request.GroupKey()
	if err != nil || first != second {
		t.Fatal("different baseline impacts split the same publication request")
	}
	request.PublicationID = foundation.ID("10000000-0000-0000-0000-000000000107")
	third, err := request.GroupKey()
	if err != nil || third == first {
		t.Fatal("different upstream publications merged")
	}
}
