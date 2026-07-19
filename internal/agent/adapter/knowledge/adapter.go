// Package knowledge 将 Agent 的最小 Knowledge Port 适配到 Knowledge Application 与 Domain。
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const errorCodeAdapterUnavailable = "AGENT_KNOWLEDGE_ADAPTER_UNAVAILABLE"

// EligibilityService 是 Adapter 允许调用的 Knowledge Application 最小查询 seam。
type EligibilityService interface {
	// CheckEvidenceEligibility 单批返回正式 Knowledge Evidence 资格。
	CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error)
}

// FormalClaimService 是 Adapter 允许调用的 Knowledge 正式 Claim 最小查询 seam。
type FormalClaimService interface {
	// Load 单批加载 Confirmed/Disputed Claim 完整事实。
	Load(context.Context, foundation.ID, []foundation.ID) ([]knowledgedomain.ClaimWithSources, error)
}

// Adapter 只暴露 Evidence Eligibility 与纯候选动作映射，不持有 Repository 或 SQL。
type Adapter struct {
	eligibility EligibilityService
	claims      FormalClaimService
}

// NewAdapter 创建 Agent 到 Knowledge Application/Domain 的最小适配器。
func NewAdapter(eligibility EligibilityService, claims FormalClaimService) (*Adapter, error) {
	if nilDependency(eligibility) || nilDependency(claims) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeAdapterUnavailable, false, errors.New("knowledge read services are unavailable"))
	}
	return &Adapter{eligibility: eligibility, claims: claims}, nil
}

// LoadFormalClaims 单批读取正式 Existing Claim，并返回防御性副本。
func (adapter *Adapter) LoadFormalClaims(ctx context.Context, workspaceID foundation.ID, claimIDs []foundation.ID) ([]knowledgedomain.ClaimWithSources, error) {
	if adapter == nil || nilDependency(adapter.claims) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeAdapterUnavailable, false, errors.New("knowledge adapter is not initialized"))
	}
	result, err := adapter.claims.Load(ctx, workspaceID, claimIDs)
	if err != nil {
		return nil, err
	}
	cloned := append([]knowledgedomain.ClaimWithSources(nil), result...)
	for index := range cloned {
		cloned[index].Claim.Applicability.CanonicalJSON = append(json.RawMessage(nil), result[index].Claim.Applicability.CanonicalJSON...)
		cloned[index].Claim.ConfidenceFactors = append(json.RawMessage(nil), result[index].Claim.ConfidenceFactors...)
		cloned[index].Sources = append([]knowledgedomain.ClaimSource(nil), result[index].Sources...)
	}
	return cloned, nil
}

// CheckEvidenceEligibility 委托 Knowledge Application 执行 Workspace 作用域批量资格判断。
func (adapter *Adapter) CheckEvidenceEligibility(ctx context.Context, query knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	if adapter == nil || nilDependency(adapter.eligibility) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeAdapterUnavailable, false, errors.New("knowledge adapter is not initialized"))
	}
	return adapter.eligibility.CheckEvidenceEligibility(ctx, query)
}

// MapAssessment 调用 Knowledge Domain 唯一事实源；CONFLICT 仅打开候选 Conflict，不建议直接 Relation。
func (adapter *Adapter) MapAssessment(assessment knowledgedomain.RelationAssessment, source, target knowledgedomain.NodeRef) (knowledgedomain.AssessmentAction, error) {
	if adapter == nil || nilDependency(adapter.eligibility) || nilDependency(adapter.claims) {
		return knowledgedomain.AssessmentAction{}, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeAdapterUnavailable, false, errors.New("knowledge adapter is not initialized"))
	}
	return knowledgedomain.MapAssessment(assessment, source, target, false)
}

func nilDependency(service any) bool {
	if service == nil {
		return true
	}
	value := reflect.ValueOf(service)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ EligibilityService = (*knowledgeapplication.Service)(nil)
var _ FormalClaimService = (*knowledgeapplication.FormalClaimReader)(nil)
var _ agentapplication.RelationKnowledgePort = (*Adapter)(nil)
