package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

const (
	semanticSignalUnsupportedReason = "CLAIM_SEMANTIC_PROVIDER_UNAVAILABLE"
	ragSignalUnsupportedReason      = "RAG_CO_RETRIEVAL_PROVIDER_UNAVAILABLE"
)

// SemanticLinkDiscoverySignalProvider 是 Semantic 或 RAG 的一次批量 signal 端口。
// 实现必须返回自身真实版本和 Evidence semantic hash；不支持时返回
// Supported=false + UnsupportedReason，不能返回伪空成功。
type SemanticLinkDiscoverySignalProvider interface {
	Evaluate(context.Context, graphdomain.SemanticLinkDiscoveryProviderRequest) (graphdomain.SemanticLinkDiscoveryExternalResult, error)
}

// SemanticLinkDiscoveryCommand 是 deterministic discovery 的应用命令。
type SemanticLinkDiscoveryCommand struct {
	WorkspaceID    foundation.ID
	Pairs          []graphdomain.SemanticLinkDiscoveryPair
	Exclusions     []graphdomain.SemanticLinkDiscoveryExclusion
	Limit          int
	PerSourceLimit int
	RuleGeneration graphdomain.SemanticLinkCandidateGeneration
}

// SemanticLinkDiscoveryService 只编排批量 provider 和纯领域规则；它不写 Candidate/Relation。
type SemanticLinkDiscoveryService struct {
	semantic SemanticLinkDiscoverySignalProvider
	rag      SemanticLinkDiscoverySignalProvider
}

// NewSemanticLinkDiscoveryService 创建允许显式 unsupported provider 的发现服务。
func NewSemanticLinkDiscoveryService(semantic, rag SemanticLinkDiscoverySignalProvider) *SemanticLinkDiscoveryService {
	if nilInterface(semantic) {
		semantic = nil
	}
	if nilInterface(rag) {
		rag = nil
	}
	return &SemanticLinkDiscoveryService{semantic: semantic, rag: rag}
}

// Discover 对整个有界 pair 批次各调用 provider 一次，再执行确定性规则合并。
func (service *SemanticLinkDiscoveryService) Discover(ctx context.Context, command SemanticLinkDiscoveryCommand) (graphdomain.SemanticLinkDiscoveryResult, error) {
	if service == nil {
		return graphdomain.SemanticLinkDiscoveryResult{}, discoveryUnavailable(errors.New("semantic-link discovery service is unavailable"))
	}
	if ctx == nil {
		return graphdomain.SemanticLinkDiscoveryResult{}, discoveryInvalid(errors.New("semantic-link discovery context is nil"))
	}

	request := graphdomain.SemanticLinkDiscoveryRequest{
		WorkspaceID: command.WorkspaceID, Pairs: command.Pairs, Exclusions: command.Exclusions,
		Limit: command.Limit, PerSourceLimit: command.PerSourceLimit, RuleGeneration: command.RuleGeneration,
		Semantic: unsupportedDiscoveryResult(graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity, semanticSignalUnsupportedReason),
		RAG:      unsupportedDiscoveryResult(graphdomain.SemanticLinkDiscoveryMethodRAGCoRetrieval, ragSignalUnsupportedReason),
	}
	if err := graphdomain.ValidateSemanticLinkDiscoveryRequest(request); err != nil {
		return graphdomain.SemanticLinkDiscoveryResult{}, err
	}

	providerRequest := graphdomain.SemanticLinkDiscoveryProviderRequest{
		WorkspaceID: command.WorkspaceID,
		Pairs:       discoveryProviderPairs(command.Pairs),
		Limit:       len(command.Pairs),
	}
	if service.semantic != nil {
		result, err := service.semantic.Evaluate(ctx, cloneDiscoveryProviderRequest(providerRequest))
		if err != nil {
			return graphdomain.SemanticLinkDiscoveryResult{}, discoveryProviderFailure(err)
		}
		request.Semantic = result
	}
	if service.rag != nil {
		result, err := service.rag.Evaluate(ctx, cloneDiscoveryProviderRequest(providerRequest))
		if err != nil {
			return graphdomain.SemanticLinkDiscoveryResult{}, discoveryProviderFailure(err)
		}
		request.RAG = result
	}
	return graphdomain.EvaluateSemanticLinkDiscovery(request)
}

func unsupportedDiscoveryResult(method graphdomain.SemanticLinkDiscoveryMethod, reason string) graphdomain.SemanticLinkDiscoveryExternalResult {
	return graphdomain.SemanticLinkDiscoveryExternalResult{Method: method, UnsupportedReason: reason}
}

func cloneDiscoveryProviderRequest(request graphdomain.SemanticLinkDiscoveryProviderRequest) graphdomain.SemanticLinkDiscoveryProviderRequest {
	request.Pairs = append([]graphdomain.SemanticLinkDiscoveryProviderPair(nil), request.Pairs...)
	return request
}

func discoveryProviderPairs(values []graphdomain.SemanticLinkDiscoveryPair) []graphdomain.SemanticLinkDiscoveryProviderPair {
	result := make([]graphdomain.SemanticLinkDiscoveryProviderPair, len(values))
	for index, pair := range values {
		result[index] = graphdomain.SemanticLinkDiscoveryProviderPair{
			Source: pair.Source.Endpoint.Ref, SourceVersion: pair.Source.Endpoint.Version,
			Target: pair.Target.Endpoint.Ref, TargetVersion: pair.Target.Endpoint.Version,
		}
	}
	return result
}

func discoveryProviderFailure(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeSemanticLinkDiscoveryUnavailable, true, err)
}

func discoveryUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeSemanticLinkDiscoveryUnavailable, false, err)
}

func discoveryInvalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeSemanticLinkDiscoveryRequestInvalid, false, err)
}
