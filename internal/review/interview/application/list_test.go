package application

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestServiceListReturnsWorkspaceBoundStablePage(t *testing.T) {
	now := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	newer := listSession(applicationID(21), applicationID(1), now)
	older := listSession(applicationID(20), applicationID(1), now.Add(-time.Minute))
	store := &fakeStore{list: SessionListPage{
		Items: []domain.Session{newer, older},
		Next:  &SessionCursor{StartedAt: older.StartedAt, ID: older.ID},
	}}
	service := newTestService(t, store, []domain.Material{applicationMaterial(t)})

	page, err := service.List(context.Background(), SessionListQuery{WorkspaceID: applicationID(1), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != newer.ID || page.Items[1].ID != older.ID || page.Next == nil || page.Next.ID != older.ID {
		t.Fatalf("page=%+v", page)
	}
}

func TestServiceListRejectsInvalidQueryAndUntrustedStorePage(t *testing.T) {
	now := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		query SessionListQuery
		page  SessionListPage
	}{
		{name: "zero limit", query: SessionListQuery{WorkspaceID: applicationID(1)}},
		{name: "invalid cursor", query: SessionListQuery{WorkspaceID: applicationID(1), Limit: 1, After: &SessionCursor{ID: applicationID(2)}}},
		{
			name:  "workspace escape",
			query: SessionListQuery{WorkspaceID: applicationID(1), Limit: 1},
			page:  SessionListPage{Items: []domain.Session{listSession(applicationID(2), applicationID(9), now)}},
		},
		{
			name:  "unstable order",
			query: SessionListQuery{WorkspaceID: applicationID(1), Limit: 2},
			page: SessionListPage{Items: []domain.Session{
				listSession(applicationID(2), applicationID(1), now.Add(-time.Minute)),
				listSession(applicationID(3), applicationID(1), now),
			}},
		},
		{
			name:  "cursor mismatch",
			query: SessionListQuery{WorkspaceID: applicationID(1), Limit: 1},
			page: SessionListPage{
				Items: []domain.Session{listSession(applicationID(2), applicationID(1), now)},
				Next:  &SessionCursor{StartedAt: now, ID: applicationID(3)},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{list: test.page}
			service := newTestService(t, store, []domain.Material{applicationMaterial(t)})
			if _, err := service.List(context.Background(), test.query); err == nil {
				t.Fatal("invalid list contract was accepted")
			}
		})
	}
}

func listSession(id, workspaceID foundation.ID, startedAt time.Time) domain.Session {
	return domain.Session{
		ID: id, WorkspaceID: workspaceID, Config: applicationConfig(), Status: domain.SessionStatusActive,
		Version: 1, StartedAt: startedAt,
	}
}
