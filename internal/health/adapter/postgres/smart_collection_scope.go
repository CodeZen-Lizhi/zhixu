package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

type scanScopeMembership struct {
	TopicIDs []string
	ClaimIDs []string
}

func loadSmartCollectionScope(ctx context.Context, port healthapp.SmartCollectionMembershipPort, workspaceID foundation.ID, scope domain.ScanScope) (scanScopeMembership, healthapp.SmartCollectionBinding, error) {
	if port == nil {
		return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, healthScopeUnavailable(errors.New("smart-collection membership is unavailable"))
	}
	if err := domain.ValidateScanScope(scope); err != nil {
		return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, err
	}
	if fast, ok := port.(healthapp.SmartCollectionMembershipSnapshotPort); ok {
		snapshot, err := fast.LoadForScope(ctx, workspaceID, scope)
		if err != nil {
			return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, err
		}
		if snapshot.Binding.WorkspaceID != workspaceID || snapshot.Binding.CollectionID != scope.Ref ||
			snapshot.Binding.CollectionVersion != scope.Version || snapshot.Binding.QueryHash != scope.Hash ||
			snapshot.Binding.ReadModelRevision != scope.ReadModelRevision || snapshot.Binding.ExactCount != scope.ExactCount {
			return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, healthScopeStale(errors.New("smart-collection scope binding changed"))
		}
		return buildScanScopeMembership(snapshot)
	}
	binding, err := port.Plan(ctx, workspaceID, scope.Ref)
	if err != nil {
		return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, err
	}
	if binding.WorkspaceID != workspaceID || binding.CollectionID != scope.Ref || binding.CollectionVersion != scope.Version ||
		binding.QueryHash != scope.Hash || binding.ReadModelRevision != scope.ReadModelRevision || binding.ExactCount != scope.ExactCount {
		return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, healthScopeStale(errors.New("smart-collection scope binding changed"))
	}
	snapshot, err := port.Load(ctx, binding, int(domain.MaxScanItems))
	if err != nil {
		return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, err
	}
	if snapshot.Binding != binding || int64(len(snapshot.Members)) != binding.ExactCount {
		return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, healthScopeConsistency(errors.New("smart-collection membership snapshot is inconsistent"))
	}
	return buildScanScopeMembership(snapshot)
}

func buildScanScopeMembership(snapshot healthapp.SmartCollectionMembership) (scanScopeMembership, healthapp.SmartCollectionBinding, error) {
	result := scanScopeMembership{TopicIDs: make([]string, 0, len(snapshot.Members)), ClaimIDs: make([]string, 0, len(snapshot.Members))}
	seen := make(map[domain.ObjectRef]struct{}, len(snapshot.Members))
	for _, member := range snapshot.Members {
		if _, duplicate := seen[member]; duplicate {
			return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, healthScopeConsistency(errors.New("smart-collection membership contains a duplicate"))
		}
		seen[member] = struct{}{}
		switch member.Type {
		case domain.ObjectTypeTopic:
			result.TopicIDs = append(result.TopicIDs, string(member.ID))
		case domain.ObjectTypeClaim:
			result.ClaimIDs = append(result.ClaimIDs, string(member.ID))
		default:
			return scanScopeMembership{}, healthapp.SmartCollectionBinding{}, healthScopeConsistency(errors.New("smart-collection membership contains an unsupported target"))
		}
	}
	return result, snapshot.Binding, nil
}

func revalidateSmartCollectionScope(ctx context.Context, port healthapp.SmartCollectionMembershipPort, binding healthapp.SmartCollectionBinding) error {
	if port == nil {
		return healthScopeUnavailable(errors.New("smart-collection membership is unavailable"))
	}
	if verifier, ok := port.(healthapp.SmartCollectionBindingVerifier); ok {
		if err := verifier.VerifyBinding(ctx, binding); err != nil {
			return err
		}
		return nil
	}
	latest, err := port.Plan(ctx, binding.WorkspaceID, binding.CollectionID)
	if err != nil {
		return err
	}
	if latest != binding {
		return healthScopeStale(errors.New("smart-collection membership changed during health operation"))
	}
	return nil
}

func healthScopeUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, cause)
}

func healthScopeStale(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeScanScopeStale, false, cause)
}

func healthScopeConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, cause)
}
