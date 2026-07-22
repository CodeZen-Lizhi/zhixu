// Package application 定义 Health detector 的稳定应用边界。
package application

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

// ErrDetectorUnavailable 表示当前 scope 或底层 owner 尚未提供 detector 能力。
var ErrDetectorUnavailable = errors.New("health detector capability is unavailable")

// Scope 是 detector 读取的不可变扫描范围。
type Scope struct {
	WorkspaceID       foundation.ID
	Type              domain.ScanScopeType
	Ref               foundation.ID
	Version           int64
	Hash              string
	ReadModelRevision string
	ExactCount        int64
}

// PageRequest 是 detector 的有界、可恢复读取请求。
type PageRequest struct {
	Scope     Scope
	Cursor    string
	Page      int64
	BatchSize int
	// Descriptor 是本次执行绑定的 registry 元数据；adapter 不得自行推断版本或能力。
	Descriptor Descriptor
	// LowConfidenceThreshold 由 detector 版本配置注入，不能由 SQL 自行决定。
	LowConfidenceThreshold float64
}

// EvidenceFact 是 adapter 返回的可验证证据引用。
type EvidenceFact struct {
	Ref     domain.ObjectRef
	Hash    string
	Summary string
}

// FindingFact 是 canonical read model 中一条待解释质量事实。
type FindingFact struct {
	Target        domain.ObjectRef
	TargetVersion int64
	Severity      domain.Severity
	Summary       string
	Evidence      []EvidenceFact
}

// Page 是一次 detector 批量读取结果。
type Page struct {
	Findings   []FindingFact
	NextCursor string
	Complete   bool
	Processed  int64
}

// FactReader 是 detector adapter 的唯一读取端口；实现必须按 detector 批量读取。
type FactReader interface {
	Find(ctx context.Context, detectorID string, request PageRequest) (Page, error)
}

// Descriptor 是稳定 registry 元数据，供 scan coverage 和 API 展示。
type Descriptor struct {
	ID                string
	Version           string
	IssueType         domain.IssueType
	SupportedScopes   []domain.ScanScopeType
	SupportedTarget   []domain.ObjectType
	DefaultSeverity   domain.Severity
	Available         bool
	UnavailableReason string
}

// Detector 只产出 observations，不创建或更新 Issue。
type Detector interface {
	Descriptor() Descriptor
	ScanPage(context.Context, PageRequest) (Page, error)
}

// Registry 是 detector 的稳定 ID/version 单一事实源。
type Registry struct {
	detectors map[string]Detector
	ordered   []Descriptor
}

// NewRegistry 校验并构造稳定排序的 detector registry。
func NewRegistry(detectors []Detector, unavailable []Descriptor) (*Registry, error) {
	all := make([]Descriptor, 0, len(detectors)+len(unavailable))
	result := &Registry{detectors: make(map[string]Detector, len(detectors))}
	for _, detector := range detectors {
		if detector == nil {
			return nil, errors.New("health detector is nil")
		}
		descriptor := detector.Descriptor()
		if err := validateDescriptor(descriptor, true); err != nil {
			return nil, err
		}
		if _, exists := result.detectors[descriptor.ID]; exists {
			return nil, errors.New("health detector id is duplicated")
		}
		descriptor.Available = true
		result.detectors[descriptor.ID] = detector
		all = append(all, descriptor)
	}
	for _, descriptor := range unavailable {
		if err := validateDescriptor(descriptor, false); err != nil {
			return nil, err
		}
		if _, exists := result.detectors[descriptor.ID]; exists {
			return nil, errors.New("health detector id is duplicated")
		}
		if _, exists := findDescriptor(all, descriptor.ID); exists {
			return nil, errors.New("health detector id is duplicated")
		}
		descriptor.Available = false
		all = append(all, descriptor)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	result.ordered = all
	return result, nil
}

func findDescriptor(descriptors []Descriptor, id string) (Descriptor, bool) {
	for _, descriptor := range descriptors {
		if descriptor.ID == id {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func validateDescriptor(descriptor Descriptor, available bool) error {
	if descriptor.ID == "" || strings.TrimSpace(descriptor.ID) != descriptor.ID || descriptor.Version == "" || strings.TrimSpace(descriptor.Version) != descriptor.Version || descriptor.IssueType == "" {
		return errors.New("health detector descriptor identity is incomplete")
	}
	if !detectorValidIssueType(descriptor.IssueType) {
		return errors.New("health detector issue type is unsupported")
	}
	if available {
		if !detectorValidSeverity(descriptor.DefaultSeverity) || len(descriptor.SupportedScopes) == 0 || len(descriptor.SupportedTarget) == 0 {
			return errors.New("health detector descriptor capability is incomplete")
		}
		if descriptor.UnavailableReason != "" {
			return errors.New("available detector cannot carry unavailable reason")
		}
	} else if strings.TrimSpace(descriptor.UnavailableReason) == "" {
		return errors.New("unavailable detector requires a reason")
	}
	return nil
}

func detectorValidIssueType(value domain.IssueType) bool {
	switch value {
	case domain.IssueTypeOrphan, domain.IssueTypeDuplicate, domain.IssueTypeConflict, domain.IssueTypeStale,
		domain.IssueTypeMissingSource, domain.IssueTypeLowConfidence, domain.IssueTypeBrokenReference,
		domain.IssueTypeIndexError, domain.IssueTypeSupersededUsage, domain.IssueTypeReviewInvalidated:
		return true
	default:
		return false
	}
}

func detectorValidSeverity(value domain.Severity) bool {
	switch value {
	case domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow:
		return true
	default:
		return false
	}
}

// Descriptors 返回稳定 ID 顺序的 registry 元数据。
func (registry *Registry) Descriptors() []Descriptor {
	if registry == nil {
		return nil
	}
	result := make([]Descriptor, len(registry.ordered))
	for i, descriptor := range registry.ordered {
		result[i] = cloneDescriptor(descriptor)
	}
	return result
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.SupportedScopes = append([]domain.ScanScopeType(nil), descriptor.SupportedScopes...)
	descriptor.SupportedTarget = append([]domain.ObjectType(nil), descriptor.SupportedTarget...)
	return descriptor
}

// Get 返回可执行 detector；不可用 entry 返回 ErrDetectorUnavailable。
func (registry *Registry) Get(id string) (Detector, error) {
	if registry == nil {
		return nil, ErrDetectorUnavailable
	}
	detector, ok := registry.detectors[id]
	if !ok {
		return nil, ErrDetectorUnavailable
	}
	return detector, nil
}

// Descriptor 返回指定 detector 的不可变 registry 元数据。
func (registry *Registry) Descriptor(id string) (Descriptor, bool) {
	if registry == nil {
		return Descriptor{}, false
	}
	descriptor, found := findDescriptor(registry.ordered, id)
	if !found {
		return Descriptor{}, false
	}
	return cloneDescriptor(descriptor), true
}

// Supports 判断 descriptor 是否覆盖指定 scope 与 target 类型。
func (descriptor Descriptor) Supports(scope domain.ScanScopeType, target domain.ObjectType) bool {
	if !descriptor.SupportsScope(scope) {
		return false
	}
	return descriptor.SupportsTarget(target)
}

// SupportsScope 判断 descriptor 是否覆盖指定 scan scope。
func (descriptor Descriptor) SupportsScope(scope domain.ScanScopeType) bool {
	return scopeSupported(descriptor.SupportedScopes, scope)
}

// SupportsTarget 判断 descriptor 是否允许产出指定 target 类型。
func (descriptor Descriptor) SupportsTarget(target domain.ObjectType) bool {
	for _, supported := range descriptor.SupportedTarget {
		if supported == target {
			return true
		}
	}
	return false
}

// Coverage 为指定 scope 生成完整 coverage，显式暴露 scope 或 owner 缺失。
func (registry *Registry) Coverage(scope Scope) []domain.DetectorCoverage {
	if registry == nil {
		return nil
	}
	coverage := make([]domain.DetectorCoverage, 0, len(registry.ordered))
	for _, descriptor := range registry.ordered {
		item := domain.DetectorCoverage{DetectorID: descriptor.ID, DetectorVersion: descriptor.Version, Status: domain.DetectorCoverageStatusPending}
		if !descriptor.Available {
			item.Status = domain.DetectorCoverageStatusUnavailable
			item.UnavailableReason = descriptor.UnavailableReason
		} else if !scopeSupported(descriptor.SupportedScopes, scope.Type) {
			item.Status = domain.DetectorCoverageStatusUnavailable
			item.UnavailableReason = "detector does not support this scan scope"
		}
		coverage = append(coverage, item)
	}
	return coverage
}

func scopeSupported(scopes []domain.ScanScopeType, scope domain.ScanScopeType) bool {
	for _, supported := range scopes {
		if supported == scope {
			return true
		}
	}
	return false
}
