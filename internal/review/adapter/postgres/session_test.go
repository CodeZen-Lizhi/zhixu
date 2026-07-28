package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

func TestStartSessionRejectsNonReviewBindingBeforeDatabase(t *testing.T) {
	deckID := foundation.ID("73000000-0000-4000-8000-000000000003")
	tests := []struct {
		name        string
		deckID      *foundation.ID
		sessionType domain.SessionType
	}{
		{name: "interview", sessionType: domain.SessionTypeInterview},
		{name: "interview with deck", deckID: &deckID, sessionType: domain.SessionTypeInterview},
		{name: "review without deck", sessionType: domain.SessionTypeReview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &Repository{}
			_, err := repository.StartSession(context.Background(), domain.Session{
				ID:             foundation.ID("73000000-0000-4000-8000-000000000001"),
				WorkspaceID:    foundation.ID("73000000-0000-4000-8000-000000000002"),
				DeckID:         test.deckID,
				SessionType:    test.sessionType,
				Status:         domain.SessionStatusActive,
				Config:         json.RawMessage(`{}`),
				IdempotencyKey: "invalid-review-session",
				StartedAt:      time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC),
			}, strings.Repeat("a", 64))
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != domain.ErrorCodeSessionInvalid {
				t.Fatalf("err=%v classified=%+v", err, classified)
			}
		})
	}
}
