package gitmerge

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
)

type mergerFunc func(context.Context, gitcli.MergeInput) (gitcli.MergeResult, error)

func (function mergerFunc) Merge(ctx context.Context, input gitcli.MergeInput) (gitcli.MergeResult, error) {
	return function(ctx, input)
}

func TestAdapterMapsFixedGitContract(t *testing.T) {
	adapter, err := New(mergerFunc(func(_ context.Context, input gitcli.MergeInput) (gitcli.MergeResult, error) {
		if string(input.Base) != "base" || string(input.Current) != "current" || string(input.Proposed) != "proposed" {
			t.Fatalf("input=%#v", input)
		}
		return gitcli.MergeResult{
			Candidate: []byte("candidate"), Algorithm: "myers", Contract: gitcli.MergeContractVersion,
			Conflicts: []gitcli.MergeConflict{{Ordinal: 1, Base: []byte("b"), Current: []byte("c"), Proposed: []byte("p")}},
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Merge(context.Background(), application.RevisionMergeDocuments{Base: []byte("base"), Current: []byte("current"), Proposed: []byte("proposed")})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Candidate) != "candidate" || result.Algorithm != domain.ProposalRevisionMergeAlgorithm || result.Contract != domain.ProposalRevisionMergeAlgorithmVersion || len(result.Conflicts) != 1 || result.Conflicts[0].Ordinal != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func TestAdapterRejectsMissingOrDriftedEngine(t *testing.T) {
	if adapter, err := New(nil); err == nil || adapter != nil {
		t.Fatalf("adapter=%#v err=%v", adapter, err)
	}
	adapter, err := New(mergerFunc(func(context.Context, gitcli.MergeInput) (gitcli.MergeResult, error) {
		return gitcli.MergeResult{Algorithm: "histogram", Contract: gitcli.MergeContractVersion}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Merge(context.Background(), application.RevisionMergeDocuments{})
	if err == nil {
		t.Fatal("expected drifted engine contract to fail")
	}
	classified, ok := err.(*foundation.Error)
	if !ok || classified.Code != "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID" || classified.Kind != foundation.ErrorDependencyUnavailable {
		t.Fatalf("err=%T %v", err, err)
	}
}

func TestAdapterProbeValidatesGoldenConflict(t *testing.T) {
	adapter, err := New(mergerFunc(func(_ context.Context, input gitcli.MergeInput) (gitcli.MergeResult, error) {
		return gitcli.MergeResult{
			Candidate: append([]byte(nil), input.Current...), Algorithm: "myers", Contract: gitcli.MergeContractVersion,
			Conflicts: []gitcli.MergeConflict{{
				Ordinal: 1, Base: append([]byte(nil), input.Base...), Current: append([]byte(nil), input.Current...),
				Proposed: append([]byte(nil), input.Proposed...),
			}},
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}

	drifted, err := New(mergerFunc(func(_ context.Context, input gitcli.MergeInput) (gitcli.MergeResult, error) {
		return gitcli.MergeResult{Candidate: input.Proposed, Algorithm: "myers", Contract: gitcli.MergeContractVersion}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = drifted.Probe(context.Background())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID" {
		t.Fatalf("err=%T %v", err, err)
	}
}

func TestAdapterProbeAcceptsProductionGit(t *testing.T) {
	adapter, err := New(gitcli.New(""))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}
