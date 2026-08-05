package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const sourceRefreshLeaseReleaseTimeout = 5 * time.Second

// SourceRefreshIngestion runs the existing idempotent ingestion use case.
type SourceRefreshIngestion interface {
	Process(context.Context, ingestionapplication.ProcessRequest) (ingestionapplication.ProcessResult, error)
}

// SourceRefreshRetrieval exposes reusable snapshot build and activation use cases.
type SourceRefreshRetrieval interface {
	BeginWorkspaceSnapshot(context.Context, BeginWorkspaceSnapshotRequest) (domain.WorkspaceSnapshotResult, error)
	BuildLexical(context.Context, TransitionRequest) (domain.ProjectionBatchResult, error)
	Ready(context.Context, TransitionRequest) (domain.IndexVersion, error)
	Activate(context.Context, ActivateRequest) (domain.ActivationResult, error)
	GetActive(context.Context, foundation.ID) (domain.IndexVersion, error)
}

// SourceRefreshLease serializes one Workspace snapshot build and activation interval.
type SourceRefreshLease interface {
	Release(context.Context) error
}

// SourceRefreshLocker acquires the cross-process lease for one Workspace refresh.
type SourceRefreshLocker interface {
	AcquireSourceRefresh(context.Context, foundation.ID) (SourceRefreshLease, error)
}

// SourceRefresherDependencies are the existing owners used by a generic Source Version refresh.
type SourceRefresherDependencies struct {
	Ingestion SourceRefreshIngestion
	Retrieval SourceRefreshRetrieval
	Locker    SourceRefreshLocker
	Vectors   ProcessorVectorPort
}

// SourceRefresherOptions freeze the retrieval contract shared by Capture and Git sync.
type SourceRefresherOptions struct {
	EmbeddingVersionID  *foundation.ID
	TokenizerID         string
	TokenizerVersion    string
	TokenizerConfigHash string
	FusionConfig        json.RawMessage
	PageSize            int32
	MaxSources          int64
	MaxChunks           int64
}

// SourceRefreshRequest identifies one immutable Source Version refresh.
type SourceRefreshRequest struct {
	RequestID       foundation.ID
	WorkspaceID     foundation.ID
	SourceID        foundation.ID
	SourceVersionID foundation.ID
	AttemptNumber   int32
}

// SourceRefreshResult freezes downstream identities needed by Capture and history projections.
type SourceRefreshResult struct {
	IngestionAttemptID foundation.ID
	ParseProjectionID  foundation.ID
	IndexVersionID     foundation.ID
	ActivationID       foundation.ID
	VectorDegraded     bool
}

// SourceRefresher incrementally ingests one Source Version and activates a new Workspace snapshot.
type SourceRefresher struct {
	dependencies SourceRefresherDependencies
	options      SourceRefresherOptions
}

// NewSourceRefresher constructs the generic refresh seam reused by Capture and Git sync.
func NewSourceRefresher(dependencies SourceRefresherDependencies, options SourceRefresherOptions) (*SourceRefresher, error) {
	if dependencies.Ingestion == nil || dependencies.Retrieval == nil || dependencies.Locker == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "SOURCE_REFRESH_UNAVAILABLE", true, errors.New("source refresh dependencies are incomplete"))
	}
	processorOptions, err := normalizeProcessorOptions(ProcessorOptions{
		EmbeddingVersionID: options.EmbeddingVersionID, TokenizerID: options.TokenizerID,
		TokenizerVersion: options.TokenizerVersion, TokenizerConfigHash: options.TokenizerConfigHash,
		FusionConfig: options.FusionConfig, PageSize: options.PageSize, MaxSources: options.MaxSources,
		MaxChunks: options.MaxChunks, RetryDelay: time.Second,
	})
	if err != nil {
		return nil, err
	}
	if processorOptions.EmbeddingVersionID != nil && dependencies.Vectors == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "SOURCE_REFRESH_VECTOR_UNAVAILABLE", true, errors.New("hybrid source refresh requires a vector builder"))
	}
	return &SourceRefresher{dependencies: dependencies, options: SourceRefresherOptions{
		EmbeddingVersionID: processorOptions.EmbeddingVersionID, TokenizerID: processorOptions.TokenizerID,
		TokenizerVersion: processorOptions.TokenizerVersion, TokenizerConfigHash: processorOptions.TokenizerConfigHash,
		FusionConfig: processorOptions.FusionConfig, PageSize: processorOptions.PageSize,
		MaxSources: processorOptions.MaxSources, MaxChunks: processorOptions.MaxChunks,
	}}, nil
}

// Refresh runs idempotent ingestion, then serializes the Workspace snapshot build and activation.
func (refresher *SourceRefresher) Refresh(ctx context.Context, request SourceRefreshRequest) (result SourceRefreshResult, returnErr error) {
	if refresher == nil || refresher.dependencies.Ingestion == nil || refresher.dependencies.Retrieval == nil || refresher.dependencies.Locker == nil ||
		!refreshID(request.RequestID) || !refreshID(request.WorkspaceID) || !refreshID(request.SourceID) ||
		!refreshID(request.SourceVersionID) || request.AttemptNumber < 1 {
		return SourceRefreshResult{}, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_REFRESH_REQUEST_INVALID", false, errors.New("source refresh request is invalid"))
	}
	prefix := "source-refresh:" + string(request.RequestID)
	ingested, err := refresher.dependencies.Ingestion.Process(ctx, ingestionapplication.ProcessRequest{
		SourceVersionID: request.SourceVersionID, IdempotencyKey: prefix + ":ingestion",
		AttemptNumber: request.AttemptNumber,
	})
	if err != nil {
		return SourceRefreshResult{}, err
	}
	if err := validateRefreshIngestion(request, ingested.Attempt); err != nil {
		return SourceRefreshResult{}, err
	}
	lease, err := refresher.dependencies.Locker.AcquireSourceRefresh(ctx, request.WorkspaceID)
	if err != nil {
		return SourceRefreshResult{}, err
	}
	if lease == nil {
		return SourceRefreshResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "SOURCE_REFRESH_LEASE_INVALID", true, errors.New("source refresh locker returned a nil lease"))
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), sourceRefreshLeaseReleaseTimeout)
		defer cancel()
		returnErr = errors.Join(returnErr, lease.Release(releaseCtx))
	}()
	projectionID := *ingested.Attempt.ParseProjectionID
	snapshot, err := refresher.dependencies.Retrieval.BeginWorkspaceSnapshot(ctx, BeginWorkspaceSnapshotRequest{
		WorkspaceID: request.WorkspaceID, EmbeddingVersionID: cloneID(refresher.options.EmbeddingVersionID),
		TargetSourceID: request.SourceID, TargetSourceVersionID: request.SourceVersionID,
		TargetParseProjectionID: projectionID, TokenizerID: refresher.options.TokenizerID,
		TokenizerVersion: refresher.options.TokenizerVersion, TokenizerConfigHash: refresher.options.TokenizerConfigHash,
		FusionConfig:      append(json.RawMessage(nil), refresher.options.FusionConfig...),
		SourceSnapshotRef: "source-refresh-v1:" + string(request.RequestID),
		IdempotencyKey:    prefix + ":snapshot", ProcessingContract: refreshProcessingContract(ingested.Attempt),
		PageSize: refresher.options.PageSize, MaxSources: refresher.options.MaxSources, MaxChunks: refresher.options.MaxChunks,
	})
	if err != nil {
		return SourceRefreshResult{}, err
	}
	index := snapshot.IndexVersion
	if index.WorkspaceID != request.WorkspaceID || index.ProcessingContract == nil || index.ID == "" {
		return SourceRefreshResult{}, refreshConsistency("source refresh snapshot binding is invalid")
	}
	if index.Status == domain.IndexStatusBuilding {
		lexical, buildErr := refresher.dependencies.Retrieval.BuildLexical(ctx, TransitionRequest{
			WorkspaceID: request.WorkspaceID, IndexVersionID: index.ID, ExpectedVersion: index.Version,
		})
		if buildErr != nil {
			return SourceRefreshResult{}, buildErr
		}
		if lexical.IndexVersionID != index.ID {
			return SourceRefreshResult{}, refreshConsistency("source refresh lexical result is invalid")
		}
		if index.EmbeddingVersionID != nil {
			for {
				batch, vectorErr := refresher.dependencies.Vectors.BuildNextVectorBatch(ctx, BuildNextVectorBatchRequest{
					WorkspaceID: request.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: index.Version,
				})
				if vectorErr != nil {
					return SourceRefreshResult{}, vectorErr
				}
				if batch.IndexVersionID != index.ID {
					return SourceRefreshResult{}, refreshConsistency("source refresh vector result is invalid")
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
			return SourceRefreshResult{}, err
		}
	}
	result = SourceRefreshResult{
		IngestionAttemptID: ingested.Attempt.ID, ParseProjectionID: projectionID,
		IndexVersionID: index.ID, VectorDegraded: domain.HasDegradedCapability(index.DegradedCapabilities, domain.DegradedVector),
	}
	if index.Status == domain.IndexStatusActive {
		return result, nil
	}
	if index.Status != domain.IndexStatusReady {
		return SourceRefreshResult{}, refreshConsistency("source refresh index did not reach ready")
	}
	var currentID *foundation.ID
	var currentVersion *int64
	current, activeErr := refresher.dependencies.Retrieval.GetActive(ctx, request.WorkspaceID)
	if activeErr == nil {
		if current.ID == index.ID {
			return result, nil
		}
		currentID = &current.ID
		currentVersion = &current.Version
	} else if !refreshNotFound(activeErr) {
		return SourceRefreshResult{}, activeErr
	}
	activation, err := refresher.dependencies.Retrieval.Activate(ctx, ActivateRequest{
		WorkspaceID: request.WorkspaceID, TargetIndexVersionID: index.ID,
		ExpectedTargetVersion: index.Version, ExpectedCurrentIndexVersionID: currentID,
		ExpectedCurrentVersion: currentVersion, IdempotencyKey: prefix + ":activate", ReasonCode: "source_refresh",
	})
	if err != nil {
		return SourceRefreshResult{}, err
	}
	if activation.ActiveIndexVersion.ID != index.ID || activation.ActiveIndexVersion.Status != domain.IndexStatusActive {
		return SourceRefreshResult{}, refreshConsistency("source refresh activation result is invalid")
	}
	result.ActivationID = activation.Activation.ID
	return result, nil
}

func validateRefreshIngestion(request SourceRefreshRequest, attempt ingestiondomain.AttemptRecord) error {
	if attempt.SourceVersionID != request.SourceVersionID || attempt.Status != ingestiondomain.AttemptChunked ||
		attempt.SecurityStatus != ingestiondomain.SecurityPassed || attempt.ParseProjectionID == nil ||
		attempt.ID == "" || attempt.AttemptNumber != request.AttemptNumber {
		return refreshConsistency("source refresh ingestion result is invalid")
	}
	return nil
}

func refreshProcessingContract(attempt ingestiondomain.AttemptRecord) domain.ProcessingContract {
	return domain.ProcessingContract{
		ParserID: attempt.ParserID, ParserVersion: attempt.ParserVersion,
		ParserConfigHash: attempt.ParserConfigHash, ChunkStrategyVersion: attempt.ChunkStrategyVersion,
		SchemaVersion: attempt.SchemaVersion,
	}
}

func refreshNotFound(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorNotFound
}

func refreshID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func refreshConsistency(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_REFRESH_RESULT_INVALID", false, errors.New(strings.TrimSpace(message)))
}
