// Package application 编排 Retrieval 索引构建、投影与原子激活用例。
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const serviceUnavailableCode = "RETRIEVAL_SERVICE_UNAVAILABLE"

// Store 隐藏 Retrieval 持久化、批量写入、锁与事务实现。
type Store interface {
	RegisterEmbeddingVersion(context.Context, domain.EmbeddingVersion) (domain.EmbeddingVersionResult, error)
	BeginIndex(context.Context, domain.IndexBuild) (domain.IndexVersionResult, error)
	BeginWorkspaceSnapshot(context.Context, domain.WorkspaceSnapshotCommand) (domain.WorkspaceSnapshotResult, error)
	BuildLexical(context.Context, domain.LexicalBuildCommand) (domain.ProjectionBatchResult, error)
	SaveVectorBatch(context.Context, domain.VectorProjectionBatch) (domain.ProjectionBatchResult, error)
	TransitionIndex(context.Context, domain.IndexTransition) (domain.IndexVersion, error)
	Activate(context.Context, domain.ActivationCommand) (domain.ActivationResult, error)
	RollbackActivate(context.Context, domain.RollbackActivationCommand) (domain.ActivationResult, error)
	GetEmbeddingVersion(context.Context, foundation.ID) (domain.EmbeddingVersion, error)
	GetIndex(context.Context, foundation.ID, foundation.ID) (domain.IndexVersion, error)
	GetIndexByIdempotencyKey(context.Context, foundation.ID, string) (domain.IndexVersion, error)
	GetBuildStatus(context.Context, foundation.ID, foundation.ID) (domain.BuildStatus, error)
	GetActive(context.Context, foundation.ID) (domain.IndexVersion, error)
}

// BeginWorkspaceSnapshotRequest 描述一次完整 Workspace Source/Chunk Snapshot 构建。
type BeginWorkspaceSnapshotRequest struct {
	WorkspaceID             foundation.ID
	EmbeddingVersionID      *foundation.ID
	TargetSourceID          foundation.ID
	TargetSourceVersionID   foundation.ID
	TargetParseProjectionID foundation.ID
	TokenizerID             string
	TokenizerVersion        string
	TokenizerConfigHash     string
	FusionConfig            json.RawMessage
	SourceSnapshotRef       string
	IdempotencyKey          string
	ProcessingContract      domain.ProcessingContract
	PageSize                int32
	MaxSources              int64
	MaxChunks               int64
}

// BeginWorkspaceSnapshot 生成 FTS-only 或 Hybrid Index 身份，并由 Store 在一个 bounded 事务中物化 Snapshot。
func (s *Service) BeginWorkspaceSnapshot(ctx context.Context, request BeginWorkspaceSnapshotRequest) (domain.WorkspaceSnapshotResult, error) {
	if err := s.available(); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	var err error
	var fusion json.RawMessage
	var modelSettingsRevision *int64
	if request.EmbeddingVersionID == nil {
		fusion, err = canonicalJSONObject(request.FusionConfig)
		if err != nil {
			return domain.WorkspaceSnapshotResult{}, invalidRequest(domain.ErrorCodeFusionConfigInvalid, err)
		}
	} else {
		if _, err := domain.ParseRRFConfig(request.FusionConfig); err != nil {
			return domain.WorkspaceSnapshotResult{}, err
		}
		embedding, err := s.validateEmbeddingVersionReference(ctx, *request.EmbeddingVersionID)
		if err != nil {
			return domain.WorkspaceSnapshotResult{}, err
		}
		modelSettingsRevision = cloneInt64(embedding.ModelSettingsRevision)
		fusion = append(json.RawMessage(nil), request.FusionConfig...)
	}
	pageSize := request.PageSize
	if pageSize == 0 {
		pageSize = domain.DefaultSnapshotPageSize
	}
	maxSources := request.MaxSources
	if maxSources == 0 {
		maxSources = domain.DefaultSnapshotMaxSources
	}
	maxChunks := request.MaxChunks
	if maxChunks == 0 {
		maxChunks = domain.DefaultSnapshotMaxChunks
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	now := s.dependencies.Clock.Now()
	command := domain.WorkspaceSnapshotCommand{
		IndexVersion: domain.IndexVersion{
			ID: id, WorkspaceID: request.WorkspaceID,
			EmbeddingVersionID: cloneID(request.EmbeddingVersionID), ModelSettingsRevision: modelSettingsRevision,
			TokenizerID: clean(request.TokenizerID), TokenizerVersion: clean(request.TokenizerVersion),
			TokenizerConfigHash: cleanHash(request.TokenizerConfigHash), FusionConfig: fusion,
			SourceSnapshotRef: clean(request.SourceSnapshotRef), IdempotencyKey: clean(request.IdempotencyKey),
			Status: domain.IndexStatusBuilding, DegradedCapabilities: snapshotInitialDegradations(request.EmbeddingVersionID),
			Version: 1, CreatedAt: now, UpdatedAt: now,
			ProcessingContract: &domain.ProcessingContract{
				ParserID: clean(request.ProcessingContract.ParserID), ParserVersion: clean(request.ProcessingContract.ParserVersion),
				ParserConfigHash:     cleanHash(request.ProcessingContract.ParserConfigHash),
				ChunkStrategyVersion: clean(request.ProcessingContract.ChunkStrategyVersion), SchemaVersion: clean(request.ProcessingContract.SchemaVersion),
			},
		},
		TargetSourceID: request.TargetSourceID, TargetSourceVersionID: request.TargetSourceVersionID,
		TargetParseProjectionID: request.TargetParseProjectionID,
		PageSize:                pageSize, MaxSources: maxSources, MaxChunks: maxChunks,
	}
	if err := domain.ValidateWorkspaceSnapshotCommand(command); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	return s.dependencies.Store.BeginWorkspaceSnapshot(ctx, command)
}

func snapshotInitialDegradations(embeddingVersionID *foundation.ID) []domain.DegradedCapability {
	if embeddingVersionID == nil {
		return []domain.DegradedCapability{domain.DegradedVector}
	}
	return nil
}

// Dependencies 是 Retrieval Application Service 的显式依赖。
type Dependencies struct {
	Store Store
	IDs   foundation.IDGenerator
	Clock foundation.Clock
}

// Service 编排输入规范化、领域校验与 Store 原子操作。
type Service struct{ dependencies Dependencies }

// NewService 创建 Retrieval Application Service；依赖不完整时拒绝启动。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Store == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, serviceUnavailableCode, false, errors.New("retrieval dependencies are incomplete"))
	}
	return &Service{dependencies: dependencies}, nil
}

// RegisterEmbeddingRequest 描述一个不含凭据的不可变 Embedding 配置。
type RegisterEmbeddingRequest struct {
	Provider              string
	AdapterName           string
	AdapterVersion        string
	Model                 string
	Dimensions            int32
	Normalization         domain.EmbeddingNormalization
	DistanceMetric        domain.DistanceMetric
	ConfigHash            string
	ModelSettingsRevision *int64
}

// RegisterEmbedding 注册或精确重放 Embedding Version。
func (s *Service) RegisterEmbedding(ctx context.Context, request RegisterEmbeddingRequest) (domain.EmbeddingVersionResult, error) {
	if err := s.available(); err != nil {
		return domain.EmbeddingVersionResult{}, err
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.EmbeddingVersionResult{}, err
	}
	version := domain.EmbeddingVersion{
		ID:                    id,
		Provider:              clean(request.Provider),
		AdapterName:           clean(request.AdapterName),
		AdapterVersion:        clean(request.AdapterVersion),
		Model:                 clean(request.Model),
		Dimensions:            request.Dimensions,
		Normalization:         request.Normalization,
		DistanceMetric:        request.DistanceMetric,
		ConfigHash:            cleanHash(request.ConfigHash),
		ModelSettingsRevision: cloneInt64(request.ModelSettingsRevision),
		CreatedAt:             s.dependencies.Clock.Now(),
	}
	if err := domain.ValidateEmbeddingVersion(version); err != nil {
		return domain.EmbeddingVersionResult{}, err
	}
	return s.dependencies.Store.RegisterEmbeddingVersion(ctx, version)
}

// ManifestChunkInput 是调用方提供的 Canonical Chunk 冻结元数据。
type ManifestChunkInput struct {
	ChunkID              foundation.ID
	ContentHash          string
	Sequence             int32
	ParserVersion        string
	ChunkStrategyVersion string
	SchemaVersion        string
}

// BeginIndexRequest 描述一次幂等 Index Build 及其完整 Manifest。
type BeginIndexRequest struct {
	WorkspaceID          foundation.ID
	EmbeddingVersionID   *foundation.ID
	TokenizerID          string
	TokenizerVersion     string
	TokenizerConfigHash  string
	FusionConfig         json.RawMessage
	SourceSnapshotRef    string
	IdempotencyKey       string
	DegradedCapabilities []domain.DegradedCapability
	Manifest             []ManifestChunkInput
}

// BeginIndex 创建 Building Index 并在同一 Store 操作中冻结 Manifest。
func (s *Service) BeginIndex(ctx context.Context, request BeginIndexRequest) (domain.IndexVersionResult, error) {
	if err := s.available(); err != nil {
		return domain.IndexVersionResult{}, err
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.IndexVersionResult{}, err
	}
	now := s.dependencies.Clock.Now()
	fusion, err := canonicalJSONObject(request.FusionConfig)
	if err != nil {
		return domain.IndexVersionResult{}, invalidRequest("RETRIEVAL_FUSION_CONFIG_INVALID", err)
	}
	capabilities, err := domain.NormalizeDegradedCapabilities(request.DegradedCapabilities)
	if err != nil {
		return domain.IndexVersionResult{}, err
	}
	var modelSettingsRevision *int64
	if request.EmbeddingVersionID != nil {
		embedding, err := s.validateEmbeddingVersionReference(ctx, *request.EmbeddingVersionID)
		if err != nil {
			return domain.IndexVersionResult{}, err
		}
		modelSettingsRevision = cloneInt64(embedding.ModelSettingsRevision)
	}
	if request.EmbeddingVersionID == nil && !domain.HasDegradedCapability(capabilities, domain.DegradedVector) {
		capabilities = []domain.DegradedCapability{domain.DegradedVector}
	}
	manifest := make([]domain.ManifestChunk, len(request.Manifest))
	for i, chunk := range request.Manifest {
		manifest[i] = domain.ManifestChunk{
			IndexVersionID:       id,
			ChunkID:              chunk.ChunkID,
			WorkspaceID:          request.WorkspaceID,
			ContentHash:          cleanHash(chunk.ContentHash),
			Sequence:             chunk.Sequence,
			ParserVersion:        clean(chunk.ParserVersion),
			ChunkStrategyVersion: clean(chunk.ChunkStrategyVersion),
			SchemaVersion:        clean(chunk.SchemaVersion),
			CreatedAt:            now,
		}
	}
	canonical, manifestHash, err := domain.CanonicalizeManifest(request.WorkspaceID, id, manifest)
	if err != nil {
		return domain.IndexVersionResult{}, err
	}
	index := domain.IndexVersion{
		ID:                    id,
		WorkspaceID:           request.WorkspaceID,
		EmbeddingVersionID:    cloneID(request.EmbeddingVersionID),
		ModelSettingsRevision: modelSettingsRevision,
		TokenizerID:           clean(request.TokenizerID),
		TokenizerVersion:      clean(request.TokenizerVersion),
		TokenizerConfigHash:   cleanHash(request.TokenizerConfigHash),
		FusionConfig:          fusion,
		SourceSnapshotRef:     clean(request.SourceSnapshotRef),
		ManifestHash:          manifestHash,
		ExpectedChunkCount:    int64(len(canonical)),
		IdempotencyKey:        clean(request.IdempotencyKey),
		Status:                domain.IndexStatusBuilding,
		DegradedCapabilities:  capabilities,
		Version:               1,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	build := domain.IndexBuild{IndexVersion: index, Manifest: canonical}
	if err := domain.ValidateIndexBuild(build); err != nil {
		return domain.IndexVersionResult{}, err
	}
	return s.dependencies.Store.BeginIndex(ctx, build)
}

func (s *Service) validateEmbeddingVersionReference(ctx context.Context, embeddingVersionID foundation.ID) (domain.EmbeddingVersion, error) {
	embedding, err := s.dependencies.Store.GetEmbeddingVersion(ctx, embeddingVersionID)
	if err != nil {
		return domain.EmbeddingVersion{}, err
	}
	if embedding.ID != embeddingVersionID {
		return domain.EmbeddingVersion{}, foundation.NewError(
			foundation.ErrorConsistencyViolation,
			"RETRIEVAL_EMBEDDING_VERSION_BINDING_INVALID",
			false,
			errors.New("embedding version lookup returned a different identity"),
		)
	}
	if err := domain.ValidateEmbeddingVersion(embedding); err != nil {
		return domain.EmbeddingVersion{}, err
	}
	return embedding, nil
}

// BuildLexical 批量生成冻结 Manifest 对应的全文投影。
func (s *Service) BuildLexical(ctx context.Context, request TransitionRequest) (domain.ProjectionBatchResult, error) {
	if err := s.available(); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	index, err := s.dependencies.Store.GetIndex(ctx, request.WorkspaceID, request.IndexVersionID)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	command := domain.LexicalBuildCommand{
		WorkspaceID:          request.WorkspaceID,
		IndexVersionID:       request.IndexVersionID,
		ExpectedIndexVersion: request.ExpectedVersion,
		At:                   s.dependencies.Clock.Now(),
	}
	if err := domain.ValidateLexicalBuildCommand(index, command); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	return s.dependencies.Store.BuildLexical(ctx, command)
}

// VectorProjectionInput 是一行待原子保存的 Projection 内容。
type VectorProjectionInput struct {
	ChunkID      foundation.ID
	Embedding    []float32
	TokenCount   int32
	VectorStatus domain.VectorStatus
	FailureCode  string
}

// SaveVectorBatchRequest 描述一次全有或全无的 Projection 批次。
type SaveVectorBatchRequest struct {
	WorkspaceID          foundation.ID
	IndexVersionID       foundation.ID
	ExpectedIndexVersion int64
	Projections          []VectorProjectionInput
}

// SaveVectorBatch 读取冻结绑定、校验全部 Projection 后执行批量写入。
func (s *Service) SaveVectorBatch(ctx context.Context, request SaveVectorBatchRequest) (domain.ProjectionBatchResult, error) {
	if err := s.available(); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	index, err := s.dependencies.Store.GetIndex(ctx, request.WorkspaceID, request.IndexVersionID)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	if index.EmbeddingVersionID == nil {
		return domain.ProjectionBatchResult{}, invalidRequest(domain.ErrorCodeProjectionInvalid, errors.New("FTS-only index cannot accept vector projections"))
	}
	embedding, err := s.dependencies.Store.GetEmbeddingVersion(ctx, *index.EmbeddingVersionID)
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	now := s.dependencies.Clock.Now()
	rows := make([]domain.VectorProjectionWrite, len(request.Projections))
	for i, row := range request.Projections {
		rows[i] = domain.VectorProjectionWrite{
			ChunkID:      row.ChunkID,
			Embedding:    append([]float32(nil), row.Embedding...),
			TokenCount:   row.TokenCount,
			VectorStatus: row.VectorStatus,
			FailureCode:  clean(row.FailureCode),
		}
	}
	batch := domain.VectorProjectionBatch{
		WorkspaceID:          request.WorkspaceID,
		IndexVersionID:       request.IndexVersionID,
		EmbeddingVersionID:   *index.EmbeddingVersionID,
		ExpectedIndexVersion: request.ExpectedIndexVersion,
		Projections:          rows,
		At:                   now,
	}
	if err := domain.ValidateVectorProjectionBatch(index, embedding, batch); err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	return s.dependencies.Store.SaveVectorBatch(ctx, batch)
}

// TransitionRequest 绑定一个 Index 及其期望乐观锁版本。
type TransitionRequest struct {
	WorkspaceID     foundation.ID
	IndexVersionID  foundation.ID
	ExpectedVersion int64
}

// Ready 在聚合完整性证明通过后将 Building Index 标记为 Ready。
func (s *Service) Ready(ctx context.Context, request TransitionRequest) (domain.IndexVersion, error) {
	if err := s.available(); err != nil {
		return domain.IndexVersion{}, err
	}
	index, err := s.dependencies.Store.GetIndex(ctx, request.WorkspaceID, request.IndexVersionID)
	if err != nil {
		return domain.IndexVersion{}, err
	}
	status, err := s.dependencies.Store.GetBuildStatus(ctx, request.WorkspaceID, request.IndexVersionID)
	if err != nil {
		return domain.IndexVersion{}, err
	}
	capabilities := domain.DeriveReadyDegradedCapabilities(index, status)
	candidate := index
	candidate.DegradedCapabilities = capabilities
	status.DegradedCapabilities = capabilities
	if err := domain.ValidateBuildReady(candidate, status); err != nil {
		return domain.IndexVersion{}, err
	}
	return s.transition(ctx, index, request, domain.IndexStatusReady, capabilities, "")
}

// FailRequest 描述 Building Index 的稳定失败结果。
type FailRequest struct {
	TransitionRequest
	FailureCode string
}

// Fail 将 Building Index 标记为 Failed，且不影响旧 Active。
func (s *Service) Fail(ctx context.Context, request FailRequest) (domain.IndexVersion, error) {
	if err := s.available(); err != nil {
		return domain.IndexVersion{}, err
	}
	index, err := s.dependencies.Store.GetIndex(ctx, request.WorkspaceID, request.IndexVersionID)
	if err != nil {
		return domain.IndexVersion{}, err
	}
	return s.transition(ctx, index, request.TransitionRequest, domain.IndexStatusFailed, index.DegradedCapabilities, clean(request.FailureCode))
}

func (s *Service) transition(ctx context.Context, index domain.IndexVersion, request TransitionRequest, status domain.IndexStatus, capabilities []domain.DegradedCapability, failureCode string) (domain.IndexVersion, error) {
	command := domain.IndexTransition{
		WorkspaceID:          request.WorkspaceID,
		IndexVersionID:       request.IndexVersionID,
		ExpectedVersion:      request.ExpectedVersion,
		Status:               status,
		DegradedCapabilities: append([]domain.DegradedCapability(nil), capabilities...),
		FailureCode:          failureCode,
		At:                   s.dependencies.Clock.Now(),
	}
	if err := domain.ValidateIndexTransitionCommand(index, command); err != nil {
		return domain.IndexVersion{}, err
	}
	return s.dependencies.Store.TransitionIndex(ctx, command)
}

// ActivateRequest 描述 Ready Index 的原子激活期望。
type ActivateRequest struct {
	WorkspaceID                   foundation.ID
	TargetIndexVersionID          foundation.ID
	ExpectedTargetVersion         int64
	ExpectedCurrentIndexVersionID *foundation.ID
	ExpectedCurrentVersion        *int64
	IdempotencyKey                string
	ReasonCode                    string
}

// Activate 原子激活 Ready Index，并在 Store 内追加 Activation 事实。
func (s *Service) Activate(ctx context.Context, request ActivateRequest) (domain.ActivationResult, error) {
	if err := s.available(); err != nil {
		return domain.ActivationResult{}, err
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.ActivationResult{}, err
	}
	command := domain.ActivationCommand{
		ActivationID:                  id,
		WorkspaceID:                   request.WorkspaceID,
		TargetIndexVersionID:          request.TargetIndexVersionID,
		ExpectedTargetVersion:         request.ExpectedTargetVersion,
		ExpectedCurrentIndexVersionID: cloneID(request.ExpectedCurrentIndexVersionID),
		ExpectedCurrentVersion:        cloneInt64(request.ExpectedCurrentVersion),
		IdempotencyKey:                clean(request.IdempotencyKey),
		ReasonCode:                    clean(request.ReasonCode),
		At:                            s.dependencies.Clock.Now(),
	}
	if err := domain.ValidateActivationCommandInput(command); err != nil {
		return domain.ActivationResult{}, err
	}
	return s.dependencies.Store.Activate(ctx, command)
}

// RollbackActivateRequest 描述恢复 Retiring Index 的原子切换期望。
type RollbackActivateRequest struct {
	WorkspaceID                   foundation.ID
	TargetIndexVersionID          foundation.ID
	ExpectedTargetVersion         int64
	ExpectedCurrentIndexVersionID foundation.ID
	ExpectedCurrentVersion        int64
	IdempotencyKey                string
	ReasonCode                    string
}

// RollbackActivate 原子退役当前 Active 并恢复指定 Retiring Index。
func (s *Service) RollbackActivate(ctx context.Context, request RollbackActivateRequest) (domain.ActivationResult, error) {
	if err := s.available(); err != nil {
		return domain.ActivationResult{}, err
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.ActivationResult{}, err
	}
	command := domain.RollbackActivationCommand{
		ActivationID:                  id,
		WorkspaceID:                   request.WorkspaceID,
		TargetIndexVersionID:          request.TargetIndexVersionID,
		ExpectedTargetVersion:         request.ExpectedTargetVersion,
		ExpectedCurrentIndexVersionID: request.ExpectedCurrentIndexVersionID,
		ExpectedCurrentVersion:        request.ExpectedCurrentVersion,
		IdempotencyKey:                clean(request.IdempotencyKey),
		ReasonCode:                    clean(request.ReasonCode),
		At:                            s.dependencies.Clock.Now(),
	}
	if err := domain.ValidateRollbackActivationCommandInput(command); err != nil {
		return domain.ActivationResult{}, err
	}
	return s.dependencies.Store.RollbackActivate(ctx, command)
}

// GetIndex 精确读取 Workspace 内的 Index Version。
func (s *Service) GetIndex(ctx context.Context, workspaceID, indexVersionID foundation.ID) (domain.IndexVersion, error) {
	if err := s.available(); err != nil {
		return domain.IndexVersion{}, err
	}
	return s.dependencies.Store.GetIndex(ctx, workspaceID, indexVersionID)
}

// GetIndexByIdempotencyKey 按 Workspace 与规范化幂等键恢复 Index Build。
func (s *Service) GetIndexByIdempotencyKey(ctx context.Context, workspaceID foundation.ID, key string) (domain.IndexVersion, error) {
	if err := s.available(); err != nil {
		return domain.IndexVersion{}, err
	}
	key = clean(key)
	if key == "" {
		return domain.IndexVersion{}, invalidRequest("RETRIEVAL_IDEMPOTENCY_KEY_INVALID", errors.New("idempotency key is required"))
	}
	return s.dependencies.Store.GetIndexByIdempotencyKey(ctx, workspaceID, key)
}

// GetBuildStatus 读取 Index 的 Manifest 与 Projection 聚合状态。
func (s *Service) GetBuildStatus(ctx context.Context, workspaceID, indexVersionID foundation.ID) (domain.BuildStatus, error) {
	if err := s.available(); err != nil {
		return domain.BuildStatus{}, err
	}
	return s.dependencies.Store.GetBuildStatus(ctx, workspaceID, indexVersionID)
}

// GetActive 只返回 Workspace 当前 Active Index，不做降级回退。
func (s *Service) GetActive(ctx context.Context, workspaceID foundation.ID) (domain.IndexVersion, error) {
	if err := s.available(); err != nil {
		return domain.IndexVersion{}, err
	}
	return s.dependencies.Store.GetActive(ctx, workspaceID)
}

func (s *Service) available() error {
	if s == nil || s.dependencies.Store == nil || s.dependencies.IDs == nil || s.dependencies.Clock == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, serviceUnavailableCode, false, errors.New("retrieval service is unavailable"))
	}
	return nil
}

func canonicalJSONObject(value json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("fusion config must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("fusion config contains trailing JSON")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func clean(value string) string {
	return strings.TrimSpace(value)
}

func cleanHash(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
func invalidRequest(code string, err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, err)
}
