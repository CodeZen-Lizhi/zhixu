package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// BatchSourceRefreshEntry identifies one immutable Source Version selected by
// a Git fast-forward capture.
type BatchSourceRefreshEntry struct {
	SourceID        foundation.ID
	SourceVersionID foundation.ID
	AttemptNumber   int32
}

// BatchSourceRefreshRequest replaces all changed and removed Sources in one
// Workspace snapshot.
type BatchSourceRefreshRequest struct {
	RequestID        foundation.ID
	WorkspaceID      foundation.ID
	Upserts          []BatchSourceRefreshEntry
	RemovedSourceIDs []foundation.ID
}

// BatchSourceRefreshResult reports one activation shared by the whole batch.
type BatchSourceRefreshResult struct {
	IngestionAttemptIDs []foundation.ID
	ParseProjectionIDs  []foundation.ID
	IndexVersionID      foundation.ID
	ActivationID        foundation.ID
	VectorDegraded      bool
	Reindexed           bool
}

// RefreshBatch ingests every changed Source and activates exactly one snapshot.
// A deletion-only batch reuses the active processing contract; when no index
// exists yet, it is a successful no-op.
func (refresher *SourceRefresher) RefreshBatch(ctx context.Context, request BatchSourceRefreshRequest) (result BatchSourceRefreshResult, returnErr error) {
	if err := validateBatchRefreshRequest(refresher, request); err != nil {
		return BatchSourceRefreshResult{}, err
	}
	prefix := "git-fast-forward:" + string(request.RequestID)
	targets := make([]domain.SnapshotTarget, 0, len(request.Upserts))
	var contract *domain.ProcessingContract
	for _, entry := range request.Upserts {
		ingested, err := refresher.dependencies.Ingestion.Process(ctx, ingestionapplication.ProcessRequest{
			SourceVersionID: entry.SourceVersionID,
			IdempotencyKey:  prefix + ":ingestion:" + string(entry.SourceVersionID),
			AttemptNumber:   entry.AttemptNumber,
		})
		if err != nil {
			return BatchSourceRefreshResult{}, err
		}
		if err := validateRefreshIngestion(SourceRefreshRequest{
			RequestID: request.RequestID, WorkspaceID: request.WorkspaceID, SourceID: entry.SourceID,
			SourceVersionID: entry.SourceVersionID, AttemptNumber: entry.AttemptNumber,
		}, ingested.Attempt); err != nil {
			return BatchSourceRefreshResult{}, err
		}
		candidate := refreshProcessingContract(ingested.Attempt)
		if contract == nil {
			contract = &candidate
		} else if !domain.SameProcessingContract(contract, &candidate) {
			return BatchSourceRefreshResult{}, refreshConsistency("batch source refresh processing contracts differ")
		}
		projectionID := *ingested.Attempt.ParseProjectionID
		targets = append(targets, domain.SnapshotTarget{
			SourceID: entry.SourceID, SourceVersionID: entry.SourceVersionID, ParseProjectionID: projectionID,
		})
		result.IngestionAttemptIDs = append(result.IngestionAttemptIDs, ingested.Attempt.ID)
		result.ParseProjectionIDs = append(result.ParseProjectionIDs, projectionID)
	}

	lease, err := refresher.dependencies.Locker.AcquireSourceRefresh(ctx, request.WorkspaceID)
	if err != nil {
		return BatchSourceRefreshResult{}, err
	}
	if lease == nil {
		return BatchSourceRefreshResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "SOURCE_REFRESH_LEASE_INVALID", true, errors.New("source refresh locker returned a nil lease"))
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), sourceRefreshLeaseReleaseTimeout)
		defer cancel()
		returnErr = errors.Join(returnErr, lease.Release(releaseCtx))
	}()

	if contract == nil {
		active, activeErr := refresher.dependencies.Retrieval.GetActive(ctx, request.WorkspaceID)
		if refreshNotFound(activeErr) {
			return result, nil
		}
		if activeErr != nil {
			return BatchSourceRefreshResult{}, activeErr
		}
		if active.ProcessingContract == nil {
			return BatchSourceRefreshResult{}, refreshConsistency("active index has no processing contract")
		}
		copied := *active.ProcessingContract
		contract = &copied
	}

	snapshot, err := refresher.dependencies.Retrieval.BeginWorkspaceSnapshot(ctx, BeginWorkspaceSnapshotRequest{
		WorkspaceID: request.WorkspaceID, EmbeddingVersionID: cloneID(refresher.options.EmbeddingVersionID),
		Targets: targets, RemovedSourceIDs: append([]foundation.ID(nil), request.RemovedSourceIDs...),
		TokenizerID: refresher.options.TokenizerID, TokenizerVersion: refresher.options.TokenizerVersion,
		TokenizerConfigHash: refresher.options.TokenizerConfigHash,
		FusionConfig:        append(json.RawMessage(nil), refresher.options.FusionConfig...),
		SourceSnapshotRef:   "git-fast-forward-v1:" + string(request.RequestID),
		IdempotencyKey:      prefix + ":snapshot", ProcessingContract: *contract,
		PageSize: refresher.options.PageSize, MaxSources: refresher.options.MaxSources, MaxChunks: refresher.options.MaxChunks,
	})
	if err != nil {
		return BatchSourceRefreshResult{}, err
	}
	index := snapshot.IndexVersion
	if index.ID == "" || index.WorkspaceID != request.WorkspaceID || index.ProcessingContract == nil {
		return BatchSourceRefreshResult{}, refreshConsistency("batch source refresh snapshot binding is invalid")
	}
	if index.Status == domain.IndexStatusBuilding {
		lexical, buildErr := refresher.dependencies.Retrieval.BuildLexical(ctx, TransitionRequest{
			WorkspaceID: request.WorkspaceID, IndexVersionID: index.ID, ExpectedVersion: index.Version,
		})
		if buildErr != nil {
			return BatchSourceRefreshResult{}, buildErr
		}
		if lexical.IndexVersionID != index.ID {
			return BatchSourceRefreshResult{}, refreshConsistency("batch source refresh lexical result is invalid")
		}
		if index.EmbeddingVersionID != nil {
			for {
				batch, vectorErr := refresher.dependencies.Vectors.BuildNextVectorBatch(ctx, BuildNextVectorBatchRequest{
					WorkspaceID: request.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: index.Version,
				})
				if vectorErr != nil {
					return BatchSourceRefreshResult{}, vectorErr
				}
				if batch.IndexVersionID != index.ID {
					return BatchSourceRefreshResult{}, refreshConsistency("batch source refresh vector result is invalid")
				}
				if batch.Done {
					break
				}
			}
		}
		index, err = refresher.dependencies.Retrieval.Ready(ctx, TransitionRequest{
			WorkspaceID: request.WorkspaceID, IndexVersionID: index.ID, ExpectedVersion: index.Version,
		})
		if err != nil {
			return BatchSourceRefreshResult{}, err
		}
	}
	result.IndexVersionID = index.ID
	result.VectorDegraded = domain.HasDegradedCapability(index.DegradedCapabilities, domain.DegradedVector)
	result.Reindexed = true
	if index.Status == domain.IndexStatusActive {
		return result, nil
	}
	if index.Status != domain.IndexStatusReady {
		return BatchSourceRefreshResult{}, refreshConsistency("batch source refresh index did not reach ready")
	}
	var currentID *foundation.ID
	var currentVersion *int64
	current, activeErr := refresher.dependencies.Retrieval.GetActive(ctx, request.WorkspaceID)
	if activeErr == nil {
		if current.ID == index.ID {
			return result, nil
		}
		currentID, currentVersion = &current.ID, &current.Version
	} else if !refreshNotFound(activeErr) {
		return BatchSourceRefreshResult{}, activeErr
	}
	activation, err := refresher.dependencies.Retrieval.Activate(ctx, ActivateRequest{
		WorkspaceID: request.WorkspaceID, TargetIndexVersionID: index.ID,
		ExpectedTargetVersion: index.Version, ExpectedCurrentIndexVersionID: currentID,
		ExpectedCurrentVersion: currentVersion, IdempotencyKey: prefix + ":activate", ReasonCode: "git_fast_forward",
	})
	if err != nil {
		return BatchSourceRefreshResult{}, err
	}
	if activation.ActiveIndexVersion.ID != index.ID || activation.ActiveIndexVersion.Status != domain.IndexStatusActive {
		return BatchSourceRefreshResult{}, refreshConsistency("batch source refresh activation result is invalid")
	}
	result.ActivationID = activation.Activation.ID
	return result, nil
}

func validateBatchRefreshRequest(refresher *SourceRefresher, request BatchSourceRefreshRequest) error {
	if refresher == nil || refresher.dependencies.Ingestion == nil || refresher.dependencies.Retrieval == nil || refresher.dependencies.Locker == nil ||
		!refreshID(request.RequestID) || !refreshID(request.WorkspaceID) || len(request.Upserts)+len(request.RemovedSourceIDs) == 0 {
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_BATCH_REFRESH_REQUEST_INVALID", false, errors.New("batch source refresh request is invalid"))
	}
	seen := make(map[foundation.ID]struct{}, len(request.Upserts)+len(request.RemovedSourceIDs))
	for index, entry := range request.Upserts {
		if !refreshID(entry.SourceID) || !refreshID(entry.SourceVersionID) || entry.AttemptNumber < 1 ||
			(index > 0 && request.Upserts[index-1].SourceID >= entry.SourceID) {
			return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_BATCH_REFRESH_REQUEST_INVALID", false, errors.New("batch source upserts must be valid, unique, and sorted"))
		}
		seen[entry.SourceID] = struct{}{}
	}
	if !sort.SliceIsSorted(request.RemovedSourceIDs, func(left, right int) bool { return request.RemovedSourceIDs[left] < request.RemovedSourceIDs[right] }) {
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_BATCH_REFRESH_REQUEST_INVALID", false, errors.New("batch source removals must be sorted"))
	}
	for index, sourceID := range request.RemovedSourceIDs {
		if !refreshID(sourceID) || (index > 0 && request.RemovedSourceIDs[index-1] == sourceID) {
			return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_BATCH_REFRESH_REQUEST_INVALID", false, errors.New("batch source removals must be valid and unique"))
		}
		if _, exists := seen[sourceID]; exists {
			return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_BATCH_REFRESH_REQUEST_INVALID", false, errors.New("batch source cannot be updated and removed"))
		}
	}
	return nil
}
