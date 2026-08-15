// Package gitmerge adapts the fixed repository-free Git text merger to the
// Change Control application port.
package gitmerge

import (
	"bytes"
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
)

// Adapter keeps Git CLI DTOs and its full engine contract out of Change
// Control while preserving the public algorithm/version identity.
type Adapter struct {
	merger gitcli.ThreeWayMerger
}

var conformanceInput = application.RevisionMergeDocuments{
	Base: []byte("value=base\n"), Current: []byte("value=current\n"), Proposed: []byte("value=proposed\n"),
}

// New constructs the narrow Change Control merge adapter.
func New(merger gitcli.ThreeWayMerger) (*Adapter, error) {
	if merger == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_UNAVAILABLE", true, errors.New("three-way merge engine is unavailable"))
	}
	return &Adapter{merger: merger}, nil
}

// Merge executes the fixed Git merge contract and copies only bounded text
// facts into application-owned DTOs.
func (adapter *Adapter) Merge(ctx context.Context, input application.RevisionMergeDocuments) (application.RevisionMergeResult, error) {
	if adapter == nil || adapter.merger == nil {
		return application.RevisionMergeResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_UNAVAILABLE", true, errors.New("three-way merge engine is unavailable"))
	}
	result, err := adapter.merger.Merge(ctx, gitcli.MergeInput{
		Base: clone(input.Base), Current: clone(input.Current), Proposed: clone(input.Proposed),
	})
	if err != nil {
		return application.RevisionMergeResult{}, err
	}
	if result.Algorithm != "myers" || result.Contract != gitcli.MergeContractVersion {
		return application.RevisionMergeResult{}, outputInvalid(errors.New("three-way merge engine contract is invalid"))
	}
	conflicts := make([]application.RevisionMergeConflict, len(result.Conflicts))
	for index, conflict := range result.Conflicts {
		conflicts[index] = application.RevisionMergeConflict{
			Ordinal: conflict.Ordinal, Current: clone(conflict.Current), Base: clone(conflict.Base), Proposed: clone(conflict.Proposed),
		}
	}
	return application.RevisionMergeResult{
		Candidate: clone(result.Candidate), Conflicts: conflicts,
		Algorithm: domain.ProposalRevisionMergeAlgorithm, Contract: domain.ProposalRevisionMergeAlgorithmVersion,
	}, nil
}

// Probe executes a content-free golden conflict at process startup so a
// missing binary, unsupported flags or changed Git output disables the
// capability before the first user request.
func (adapter *Adapter) Probe(ctx context.Context) error {
	result, err := adapter.Merge(ctx, conformanceInput)
	if err != nil {
		return err
	}
	if !bytes.Equal(result.Candidate, conformanceInput.Current) || len(result.Conflicts) != 1 {
		return outputInvalid(errors.New("three-way merge engine conformance result is invalid"))
	}
	conflict := result.Conflicts[0]
	if conflict.Ordinal != 1 || !bytes.Equal(conflict.Base, conformanceInput.Base) ||
		!bytes.Equal(conflict.Current, conformanceInput.Current) || !bytes.Equal(conflict.Proposed, conformanceInput.Proposed) {
		return outputInvalid(errors.New("three-way merge engine conformance conflict is invalid"))
	}
	return nil
}

func outputInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID", false, cause)
}

func clone(value []byte) []byte {
	return append([]byte(nil), value...)
}

var _ application.RevisionMergeEngine = (*Adapter)(nil)
