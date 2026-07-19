package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestFormalClaimReaderLoadsCanonicalFormalClaimsInStableIDOrder(t *testing.T) {
	workspaceID := testID(1)
	first := formalClaimFixture(t, testID(20), domain.ClaimStatusConfirmed)
	second := formalClaimFixture(t, testID(30), domain.ClaimStatusDisputed)
	repository := &fakeRepository{batchClaims: func(_ context.Context, query domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
		if query.WorkspaceID != workspaceID || len(query.IDs) != 2 || query.IDs[0] != first.Claim.ID || query.IDs[1] != second.Claim.ID ||
			len(query.Statuses) != 2 || query.Statuses[0] != domain.ClaimStatusConfirmed || query.Statuses[1] != domain.ClaimStatusDisputed || query.Limit != 2 {
			t.Fatalf("query=%+v", query)
		}
		return []domain.ClaimWithSources{second, first}, nil
	}}
	reader, err := NewFormalClaimReader(repository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Load(context.Background(), workspaceID, []foundation.ID{second.Claim.ID, first.Claim.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0].Claim.ID != first.Claim.ID || result[1].Claim.ID != second.Claim.ID ||
		result[0].Claim.UpdatedAt.Location().String() != "UTC" || result[0].Sources[0].CreatedAt.Location().String() != "UTC" {
		t.Fatalf("result=%+v", result)
	}
	result[0].Claim.Applicability.CanonicalJSON[0] = '['
	if first.Claim.Applicability.CanonicalJSON[0] == '[' {
		t.Fatal("reader returned aliased applicability bytes")
	}
}

func TestFormalClaimReaderFailsClosedForMissingOutOfScopeAndDuplicateResults(t *testing.T) {
	workspaceID := testID(1)
	claim := formalClaimFixture(t, testID(20), domain.ClaimStatusConfirmed)
	tests := []struct {
		name string
		rows []domain.ClaimWithSources
		code string
	}{
		{name: "missing", code: errorCodeFormalClaimNotFound},
		{name: "out of scope", rows: []domain.ClaimWithSources{formalClaimFixture(t, testID(30), domain.ClaimStatusConfirmed)}, code: errorCodeResultConsistency},
		{name: "duplicate", rows: []domain.ClaimWithSources{claim, claim}, code: errorCodeResultConsistency},
		{name: "non formal", rows: []domain.ClaimWithSources{formalClaimFixture(t, claim.Claim.ID, domain.ClaimStatusSuggested)}, code: errorCodeResultConsistency},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, err := NewFormalClaimReader(&fakeRepository{batchClaims: func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
				return test.rows, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = reader.Load(context.Background(), workspaceID, []foundation.ID{claim.Claim.ID})
			if errorCode(err) != test.code {
				t.Fatalf("code=%s err=%v", errorCode(err), err)
			}
		})
	}
}

func TestFormalClaimReaderValidatesBoundaryAndPreservesRepositoryErrors(t *testing.T) {
	repositoryErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "DB_UNAVAILABLE", true, errors.New("down"))
	reader, err := NewFormalClaimReader(&fakeRepository{batchClaims: func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
		return nil, repositoryErr
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Load(nil, testID(1), []foundation.ID{testID(20)}); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("nil context code=%s err=%v", errorCode(err), err)
	}
	if _, err := reader.Load(context.Background(), testID(1), nil); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("empty ids code=%s err=%v", errorCode(err), err)
	}
	if _, err := reader.Load(context.Background(), testID(1), []foundation.ID{testID(20), testID(20)}); errorCode(err) != domain.ErrorCodeRequestInvalid {
		t.Fatalf("duplicate ids code=%s err=%v", errorCode(err), err)
	}
	if _, err := reader.Load(context.Background(), testID(1), []foundation.ID{testID(20)}); !errors.Is(err, repositoryErr) {
		t.Fatalf("repository err=%v", err)
	}
}

func formalClaimFixture(t *testing.T, id foundation.ID, status domain.ClaimStatus) domain.ClaimWithSources {
	t.Helper()
	claim := testClaim(t, id, status, 2)
	source := domain.ClaimSource{
		ID: testID(int(id[len(id)-2]-'0') + 70), WorkspaceID: claim.WorkspaceID, ClaimID: claim.ID,
		Provenance: testProvenance(int(id[len(id)-2]-'0') + 40), SupportType: domain.ClaimSupportSupports,
		Reason: "正式来源", CreatedAt: testTime(),
	}
	source.EvidenceHash = domain.ComputeClaimSourceEvidenceHash(source, claim.Applicability)
	return domain.ClaimWithSources{Claim: claim, Sources: []domain.ClaimSource{source}}
}
