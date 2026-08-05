package application

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestClaimQueryServiceUsesOnlyBoundedReadRepository(t *testing.T) {
	called := false
	repository := &fakeRepository{batchClaims: func(_ context.Context, query domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
		called = true
		if query.WorkspaceID != testID(1) || query.Limit != 5 || len(query.Statuses) != 1 || query.Statuses[0] != domain.ClaimStatusConfirmed {
			t.Fatalf("query = %#v", query)
		}
		return []domain.ClaimWithSources{}, nil
	}}
	service, err := NewClaimQueryService(repository)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.GetClaims(context.Background(), GetClaimsQuery{
		WorkspaceID: testID(1), Statuses: []domain.ClaimStatus{domain.ClaimStatusConfirmed}, Limit: 5,
	})
	if err != nil || !called || len(items) != 0 {
		t.Fatalf("GetClaims() items=%#v called=%v err=%v", items, called, err)
	}
}

func TestClaimQueryServiceRejectsMissingDependenciesAndContext(t *testing.T) {
	if _, err := NewClaimQueryService(nil); err == nil {
		t.Fatal("NewClaimQueryService(nil) accepted a missing repository")
	}
	repository := &fakeRepository{batchClaims: func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
		t.Fatal("repository called for a nil context")
		return nil, nil
	}}
	service, err := NewClaimQueryService(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetClaims(nil, GetClaimsQuery{}); err == nil {
		t.Fatal("GetClaims(nil) accepted a nil context")
	}
}
