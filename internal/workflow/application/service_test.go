package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type fakeRepository struct {
	start                domain.StartRequest
	claimNow, claimUntil time.Time
	submittedVersion     int64
	err                  error
}

func (f *fakeRepository) Start(_ context.Context, r domain.StartRequest) (domain.Run, error) {
	f.start = r
	return r.Run, f.err
}
func (f *fakeRepository) GetRun(context.Context, foundation.ID) (domain.Run, error) {
	return domain.Run{}, f.err
}
func (f *fakeRepository) ClaimNode(_ context.Context, _ foundation.ID, _ string, n, u time.Time) (domain.NodeRun, error) {
	f.claimNow = n
	f.claimUntil = u
	return domain.NodeRun{}, f.err
}
func (f *fakeRepository) HeartbeatNode(context.Context, foundation.ID, string, time.Time, time.Time) (domain.NodeRun, error) {
	return domain.NodeRun{}, f.err
}
func (f *fakeRepository) CompleteNode(context.Context, domain.Completion) (domain.NodeRun, error) {
	return domain.NodeRun{}, f.err
}
func (f *fakeRepository) CreateHumanTask(_ context.Context, t domain.HumanTask, _ domain.OutboxEvent, _ time.Time) (domain.HumanTask, error) {
	return t, f.err
}
func (f *fakeRepository) SubmitHumanTask(_ context.Context, _ foundation.ID, v int64, _ json.RawMessage, _ time.Time, _ domain.OutboxEvent) (domain.HumanTask, error) {
	f.submittedVersion = v
	return domain.HumanTask{}, f.err
}

type sequenceIDs struct {
	values []foundation.ID
	index  int
}

func (s *sequenceIDs) New() (foundation.ID, error) {
	if s.index >= len(s.values) {
		return "", errors.New("exhausted")
	}
	v := s.values[s.index]
	s.index++
	return v, nil
}
func id(n byte) foundation.ID {
	return foundation.ID("00000000-0000-4000-8000-00000000000" + string([]byte{'0' + n}))
}

func TestStartBuildsAtomicRequest(t *testing.T) {
	repo := &fakeRepository{}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	ids := &sequenceIDs{values: []foundation.ID{id(1), id(2), id(3), id(4)}}
	service, err := NewService(repo, ids, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(5), DefinitionKey: "ingest", DefinitionVersion: 1, Graph: json.RawMessage(`{"nodes":[]}`), Input: json.RawMessage(`{"source":"a"}`), FirstNodeKey: "scan", FirstNodeType: "deterministic", IdempotencyKey: "request-1"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.StatusPending || repo.start.FirstNode.RunID != run.ID || repo.start.Event.RunID == nil || *repo.start.Event.RunID != run.ID {
		t.Fatalf("request=%#v", repo.start)
	}
}

func TestStartRejectsInvalidGraph(t *testing.T) {
	service, _ := NewService(&fakeRepository{}, &sequenceIDs{}, foundation.FixedClock{})
	_, err := service.Start(context.Background(), StartCommand{WorkspaceID: id(1), DefinitionKey: "x", DefinitionVersion: 1, Graph: json.RawMessage(`[]`), FirstNodeKey: "n", FirstNodeType: "x", IdempotencyKey: "request-1"})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_START_INVALID" {
		t.Fatalf("error=%v", err)
	}
}

func TestClaimUsesClockAndPositiveLease(t *testing.T) {
	repo := &fakeRepository{}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	service, _ := NewService(repo, &sequenceIDs{}, foundation.FixedClock{Value: now})
	_, err := service.Claim(context.Background(), id(1), "worker-a", 30*time.Second)
	if err != nil || !repo.claimNow.Equal(now) || !repo.claimUntil.Equal(now.Add(30*time.Second)) {
		t.Fatalf("now=%v until=%v err=%v", repo.claimNow, repo.claimUntil, err)
	}
	if _, err = service.Claim(context.Background(), id(1), "", time.Second); err == nil {
		t.Fatal("empty owner accepted")
	}
}

func TestSubmitRejectsInvalidVersion(t *testing.T) {
	service, _ := NewService(&fakeRepository{}, &sequenceIDs{}, foundation.FixedClock{})
	if _, err := service.SubmitHumanDecision(context.Background(), id(1), 0, json.RawMessage(`{}`), id(2), id(3)); err == nil {
		t.Fatal("invalid version accepted")
	}
}
