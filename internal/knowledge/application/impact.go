package application

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// ImpactRepository 是 Impact Analysis 的持久化边界。
// 实现只能读取 Knowledge/Health/Proposal 事实并保存 report，不能直接修改 Knowledge。
type ImpactRepository interface {
	GetEvent(context.Context, foundation.ID, foundation.ID) (domain.KnowledgeEvent, error)
	GetImpactReport(context.Context, foundation.ID, foundation.ID, domain.ImpactAnalysisVersion) (domain.ImpactReport, bool, error)
	// ImpactAnalysisReady 仅在 Workspace 的 v2 selector 契约完成后返回 true。
	ImpactAnalysisReady(context.Context, foundation.ID) (bool, error)
	ListImpactObjects(context.Context, domain.KnowledgeEvent) ([]domain.ImpactObject, error)
	SaveImpactReport(context.Context, domain.ImpactReport) (domain.ImpactReport, bool, error)
	// SaveImpactReportWithAudit 在同一事务中保存首次报告并追加请求审计。
	SaveImpactReportWithAudit(context.Context, domain.ImpactReport, string, ImpactAuditPort) (domain.ImpactReport, bool, error)
	GetImpactReportByID(context.Context, foundation.ID, foundation.ID) (domain.ImpactReport, error)
}

// ImpactAuditPort 是 Impact Analysis 使用的最小审计适配接口。
// Application 不依赖具体 Audit 类型，避免 Timeline 与审计形成第二套历史事实。
type ImpactAuditPort interface {
	RecordImpactAnalysis(context.Context, ImpactAuditRecord) error
	// RecordImpactAnalysisTx 在调用方事务中追加审计，不提交或回滚事务。
	RecordImpactAnalysisTx(context.Context, any, ImpactAuditRecord) error
}

// ImpactAuditRecord 是写入 Audit Port 的脱敏关联摘要。
type ImpactAuditRecord struct {
	WorkspaceID    foundation.ID
	ReportID       foundation.ID
	AuditID        foundation.ID
	SourceEventID  foundation.ID
	ObjectCount    int
	Replayed       bool
	IdempotencyKey string
	OccurredAt     time.Time
}

// ImpactAnalysisRequest 请求对一个已投影事件执行影响分析。
type ImpactAnalysisRequest struct {
	WorkspaceID    foundation.ID
	SourceEventID  foundation.ID
	IdempotencyKey string
}

// ImpactAnalysisResult 是报告和不可执行待审批建议的组合结果。
type ImpactAnalysisResult struct {
	Report         domain.ImpactReport
	ProposalDrafts []domain.ProposalDraft
	Replayed       bool
}

// ImpactService 生成只读 Impact 报告和 Proposal 草稿，不执行知识写回。
type ImpactService struct {
	repository ImpactRepository
	ids        foundation.IDGenerator
	clock      foundation.Clock
	audit      ImpactAuditPort
}

// NewImpactService 构造不带审计适配器的只读 Impact 服务。
func NewImpactService(repository ImpactRepository, ids foundation.IDGenerator, clock foundation.Clock) (*ImpactService, error) {
	return NewImpactServiceWithAudit(repository, ids, clock, nil)
}

// NewImpactServiceWithAudit 构造带可选审计适配器的只读 Impact 服务。
func NewImpactServiceWithAudit(repository ImpactRepository, ids foundation.IDGenerator, clock foundation.Clock, audit ImpactAuditPort) (*ImpactService, error) {
	if isNil(repository) || ids == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeImpactUnavailable, true, errors.New("impact analysis dependencies are unavailable"))
	}
	return &ImpactService{repository: repository, ids: ids, clock: clock, audit: audit}, nil
}

// Analyze 读取源事件和下游事实，幂等保存当前策略的 Impact 报告。
func (service *ImpactService) Analyze(ctx context.Context, request ImpactAnalysisRequest) (ImpactAnalysisResult, error) {
	if service == nil || isNil(service.repository) || service.ids == nil || service.clock == nil {
		return ImpactAnalysisResult{}, impactUnavailable("impact analysis service is unavailable")
	}
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.SourceEventID) {
		return ImpactAnalysisResult{}, impactInvalid("impact analysis identity is invalid")
	}
	if !validImpactIdempotencyKey(request.IdempotencyKey) {
		return ImpactAnalysisResult{}, impactInvalid("impact analysis idempotency key is invalid")
	}
	const currentAnalysisVersion = domain.ImpactAnalysisVersionV2
	if existing, found, err := service.repository.GetImpactReport(ctx, request.WorkspaceID, request.SourceEventID, currentAnalysisVersion); err != nil {
		return ImpactAnalysisResult{}, err
	} else if found {
		return service.replayImpactReport(ctx, request, existing, currentAnalysisVersion)
	}

	ready, err := service.repository.ImpactAnalysisReady(ctx, request.WorkspaceID)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	if !ready {
		return ImpactAnalysisResult{}, impactUnavailable("impact analysis v2 selector projection is not ready")
	}

	// Event lookup is intentionally delegated to the repository. The repository
	// must enforce the same Workspace predicate for both event and downstream facts.
	event, found, err := service.repositoryEvent(ctx, request.WorkspaceID, request.SourceEventID)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	if !found {
		return ImpactAnalysisResult{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeImpactNotFound, false, errors.New("source timeline event was not found"))
	}
	if event.WorkspaceID != request.WorkspaceID || event.ID != request.SourceEventID {
		return ImpactAnalysisResult{}, impactInconsistent("source timeline event crossed workspace boundary")
	}
	if err := event.Validate(); err != nil {
		return ImpactAnalysisResult{}, impactInconsistent("source timeline event projection is invalid")
	}
	var supersedesReportID *foundation.ID
	if predecessor, predecessorFound, predecessorErr := service.repository.GetImpactReport(
		ctx, request.WorkspaceID, request.SourceEventID, domain.ImpactAnalysisVersionV1,
	); predecessorErr != nil {
		return ImpactAnalysisResult{}, predecessorErr
	} else if predecessorFound {
		if predecessor.WorkspaceID != request.WorkspaceID || predecessor.SourceEventID != request.SourceEventID || predecessor.EffectiveAnalysisVersion() != domain.ImpactAnalysisVersionV1 {
			return ImpactAnalysisResult{}, impactInconsistent("impact predecessor crossed its version or workspace boundary")
		}
		if err := validateImpactReportIntegrity(predecessor); err != nil || validateImpactReportSourceBinding(predecessor, event) != nil {
			return ImpactAnalysisResult{}, impactInconsistent("impact predecessor source event binding is invalid")
		}
		predecessorID := predecessor.ID
		supersedesReportID = &predecessorID
	}

	objects, err := service.repository.ListImpactObjects(ctx, event)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	if len(objects) > domain.MaxImpactObjects {
		return ImpactAnalysisResult{}, impactInconsistent("impact repository returned too many objects")
	}
	objects, err = canonicalizeImpactObjects(request.WorkspaceID, objects)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(currentAnalysisVersion, event.ID, int64(event.EventVersion), objects)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	now := domain.CanonicalTimelineTime(service.clock.Now())
	id, err := service.ids.New()
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	report := domain.ImpactReport{
		ID: id, WorkspaceID: request.WorkspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: currentAnalysisVersion, SupersedesReportID: supersedesReportID,
		Status: domain.ImpactReportReady, Objects: objects, Summary: domain.SummarizeImpactObjects(objects), Fingerprint: fingerprint,
		GeneratedAt: now, CreatedAt: now, Version: 1,
	}
	if err := validateImpactReportIntegrity(report); err != nil {
		return ImpactAnalysisResult{}, err
	}
	var persisted domain.ImpactReport
	var replayed bool
	if isNil(service.audit) {
		persisted, replayed, err = service.repository.SaveImpactReport(ctx, report)
	} else {
		persisted, replayed, err = service.repository.SaveImpactReportWithAudit(ctx, report, request.IdempotencyKey, service.audit)
	}
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	if persisted.WorkspaceID != request.WorkspaceID || persisted.SourceEventID != event.ID {
		return ImpactAnalysisResult{}, impactInconsistent("persisted impact report crossed workspace boundary")
	}
	if err := validateImpactReportIntegrity(persisted); err != nil {
		return ImpactAnalysisResult{}, impactInconsistent("persisted impact report is invalid")
	}
	if validateImpactReportSourceBinding(persisted, event) != nil || persisted.Status != domain.ImpactReportReady ||
		persisted.EffectiveAnalysisVersion() != currentAnalysisVersion || persisted.Fingerprint != fingerprint ||
		!sameImpactReportID(persisted.SupersedesReportID, supersedesReportID) || !reflect.DeepEqual(persisted.Objects, objects) {
		return ImpactAnalysisResult{}, impactInconsistent("persisted impact report binding is invalid")
	}
	result := ImpactAnalysisResult{Report: persisted, ProposalDrafts: draftsForReport(persisted), Replayed: replayed}
	return result, nil
}

func (service *ImpactService) replayImpactReport(ctx context.Context, request ImpactAnalysisRequest, existing domain.ImpactReport, analysisVersion domain.ImpactAnalysisVersion) (ImpactAnalysisResult, error) {
	if existing.WorkspaceID != request.WorkspaceID || existing.SourceEventID != request.SourceEventID || existing.EffectiveAnalysisVersion() != analysisVersion {
		return ImpactAnalysisResult{}, impactInconsistent("impact report crossed its version or workspace boundary")
	}
	if err := validateImpactReportIntegrity(existing); err != nil {
		return ImpactAnalysisResult{}, impactInconsistent("impact report projection is invalid")
	}
	event, eventFound, eventErr := service.repositoryEvent(ctx, request.WorkspaceID, request.SourceEventID)
	if eventErr != nil {
		return ImpactAnalysisResult{}, eventErr
	}
	if !eventFound || validateImpactReportSourceBinding(existing, event) != nil {
		return ImpactAnalysisResult{}, impactInconsistent("impact report source event binding is invalid")
	}
	if existing.Status != domain.ImpactReportReady {
		return ImpactAnalysisResult{}, impactInconsistent("impact report is not ready for replay")
	}
	objects, err := service.repository.ListImpactObjects(ctx, event)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	if len(objects) > domain.MaxImpactObjects {
		return ImpactAnalysisResult{}, impactInconsistent("impact repository returned too many objects")
	}
	objects, err = canonicalizeImpactObjects(request.WorkspaceID, objects)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(analysisVersion, event.ID, int64(event.EventVersion), objects)
	if err != nil {
		return ImpactAnalysisResult{}, err
	}
	if existing.Fingerprint != fingerprint || !reflect.DeepEqual(existing.Objects, objects) {
		return ImpactAnalysisResult{}, impactInconsistent("impact report owner binding has drifted")
	}
	result := ImpactAnalysisResult{Report: existing, ProposalDrafts: draftsForReport(existing), Replayed: true}
	if err := service.recordAudit(ctx, result, request.IdempotencyKey); err != nil {
		return ImpactAnalysisResult{}, err
	}
	return result, nil
}

// GetReport 返回 Workspace 隔离的 Impact 报告。
func (service *ImpactService) GetReport(ctx context.Context, workspaceID, reportID foundation.ID) (domain.ImpactReport, error) {
	if service == nil || isNil(service.repository) {
		return domain.ImpactReport{}, impactUnavailable("impact analysis service is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || !validID(reportID) {
		return domain.ImpactReport{}, impactInvalid("impact report identity is invalid")
	}
	report, err := service.repository.GetImpactReportByID(ctx, workspaceID, reportID)
	if err != nil {
		return domain.ImpactReport{}, err
	}
	if report.WorkspaceID != workspaceID || report.ID != reportID {
		return domain.ImpactReport{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeImpactNotFound, false, errors.New("impact report is not visible in workspace"))
	}
	if err := validateImpactReportIntegrity(report); err != nil {
		return domain.ImpactReport{}, impactInconsistent("impact report projection is invalid")
	}
	event, found, eventErr := service.repositoryEvent(ctx, workspaceID, report.SourceEventID)
	if eventErr != nil {
		return domain.ImpactReport{}, eventErr
	}
	if !found || validateImpactReportSourceBinding(report, event) != nil {
		return domain.ImpactReport{}, impactInconsistent("impact report source event binding is invalid")
	}
	return report, nil
}

func (service *ImpactService) repositoryEvent(ctx context.Context, workspaceID, eventID foundation.ID) (domain.KnowledgeEvent, bool, error) {
	// The ImpactRepository intentionally keeps event access optional so a single
	// PostgreSQL adapter can own both event and report reads without a cross-module
	// transaction. Implementations expose this private capability through the
	// narrow interface below.
	event, err := service.repository.GetEvent(ctx, workspaceID, eventID)
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Kind == foundation.ErrorNotFound {
			return domain.KnowledgeEvent{}, false, nil
		}
		return domain.KnowledgeEvent{}, false, err
	}
	return event, true, nil
}

func draftsForReport(report domain.ImpactReport) []domain.ProposalDraft {
	if report.Status != domain.ImpactReportReady {
		return []domain.ProposalDraft{}
	}
	drafts := make([]domain.ProposalDraft, 0)
	for _, object := range report.Objects {
		if !object.RequiresProposal || object.Action == domain.ImpactActionNoop {
			continue
		}
		drafts = append(drafts, domain.ProposalDraft{WorkspaceID: report.WorkspaceID, SourceEventID: report.SourceEventID, Operation: string(object.Action), TargetType: object.Type, TargetID: object.ID, BaseVersion: object.Version, Reason: object.Reason, RequiresApproval: true, RequiresWriteAuthorization: true})
	}
	return drafts
}

func validateImpactReportSourceBinding(report domain.ImpactReport, event domain.KnowledgeEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if report.WorkspaceID != event.WorkspaceID || report.SourceEventID != event.ID ||
		report.SourceEventRef != event.SourceEventRef || report.SourceVersion != int64(event.EventVersion) {
		return errors.New("impact report source event binding does not match")
	}
	return nil
}

func validateImpactReportIntegrity(report domain.ImpactReport) error {
	if err := domain.ValidateImpactReport(report); err != nil {
		return err
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(
		report.EffectiveAnalysisVersion(), report.SourceEventID, report.SourceVersion, report.Objects,
	)
	if err != nil {
		return err
	}
	if fingerprint != report.Fingerprint {
		return errors.New("impact report fingerprint does not match its source and objects")
	}
	return nil
}

func (service *ImpactService) recordAudit(ctx context.Context, result ImpactAnalysisResult, idempotencyKey string) error {
	if isNil(service.audit) {
		return nil
	}
	record, err := BuildImpactAuditRecord(result.Report, idempotencyKey, result.Replayed)
	if err != nil {
		return err
	}
	return service.audit.RecordImpactAnalysis(ctx, record)
}

// BuildImpactAuditRecord 为持久报告和 HTTP 幂等键构造稳定、脱敏的 Audit 记录。
func BuildImpactAuditRecord(report domain.ImpactReport, idempotencyKey string, replayed bool) (ImpactAuditRecord, error) {
	if err := validateImpactReportIntegrity(report); err != nil {
		return ImpactAuditRecord{}, impactInconsistent("impact audit report is invalid")
	}
	auditID, err := deriveImpactAuditID(report.ID, idempotencyKey)
	if err != nil {
		return ImpactAuditRecord{}, err
	}
	return ImpactAuditRecord{
		WorkspaceID: report.WorkspaceID, ReportID: report.ID, AuditID: auditID, SourceEventID: report.SourceEventID,
		ObjectCount: len(report.Objects), Replayed: replayed, IdempotencyKey: "impact:" + idempotencyKey,
		OccurredAt: report.GeneratedAt.UTC().Truncate(time.Microsecond),
	}, nil
}

// deriveImpactAuditID 为同一报告上的每个 HTTP 幂等请求派生稳定的 Audit UUID。
// 它使用报告 ID 作为 UUID v5 namespace，因此同键重试不会依赖新的随机数，而不同键不会复用 Audit 主键。
func deriveImpactAuditID(reportID foundation.ID, idempotencyKey string) (foundation.ID, error) {
	parsed, err := foundation.ParseID(string(reportID))
	if err != nil || parsed != reportID || !validImpactIdempotencyKey(idempotencyKey) {
		return "", impactInconsistent("impact audit identity is invalid")
	}
	namespace, err := hex.DecodeString(strings.ReplaceAll(string(parsed), "-", ""))
	if err != nil {
		return "", impactInconsistent("impact audit namespace cannot be decoded")
	}
	sum := sha1.Sum(append(namespace, []byte("impact-audit/v1\n"+idempotencyKey)...)) // #nosec G401 -- UUID v5 requires SHA-1 and does not protect a secret.
	raw := sum[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	auditID, err := foundation.ParseID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
	if err != nil {
		return "", impactInconsistent("impact audit identity cannot be encoded")
	}
	return auditID, nil
}

func canonicalizeImpactObjects(workspaceID foundation.ID, objects []domain.ImpactObject) ([]domain.ImpactObject, error) {
	canonical := append([]domain.ImpactObject(nil), objects...)
	for _, object := range canonical {
		if object.WorkspaceID != workspaceID {
			return nil, impactInconsistent("impact repository crossed workspace boundary")
		}
		if err := domain.ValidateImpactObject(object); err != nil {
			return nil, impactInconsistent("impact repository returned an invalid object")
		}
	}
	sort.Slice(canonical, func(left, right int) bool {
		leftKey := string(canonical[left].Type) + ":" + string(canonical[left].ID)
		rightKey := string(canonical[right].Type) + ":" + string(canonical[right].ID)
		return leftKey < rightKey
	})
	result := make([]domain.ImpactObject, 0, len(canonical))
	for _, object := range canonical {
		if len(result) == 0 || result[len(result)-1].Type != object.Type || result[len(result)-1].ID != object.ID {
			result = append(result, object)
			continue
		}
		if !reflect.DeepEqual(result[len(result)-1], object) {
			return nil, impactInconsistent("impact repository returned conflicting duplicate objects")
		}
	}
	return result, nil
}

func sameImpactReportID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validImpactIdempotencyKey(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || redaction.ContainsSecret(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func impactInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeImpactInvalid, false, errors.New(message))
}

func impactUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeImpactUnavailable, true, errors.New(message))
}

func impactInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeImpactConflict, false, errors.New(message))
}
