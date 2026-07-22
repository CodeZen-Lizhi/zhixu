package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

// DetectorPageReconcileRequest 是一页 detector observations 的原子持久化请求。
type DetectorPageReconcileRequest struct {
	WorkspaceID  foundation.ID
	ScanID       foundation.ID
	DetectorID   string
	Observations []domain.IssueObservation
	ObservedAt   time.Time
}

// DetectorPageReconcileResult 是一页 observation upsert 的稳定计数。
type DetectorPageReconcileResult struct {
	Created   int64
	Reopened  int64
	Unchanged int64
	Failed    int64
}

// DetectorPageStore 原子 upsert observations 并持久化 scan-scoped seen identities。
type DetectorPageStore interface {
	ReconcileDetectorPage(context.Context, DetectorPageReconcileRequest) (DetectorPageReconcileResult, error)
	ResolveMissingForCompleteScan(context.Context, foundation.ID, foundation.ID, string) (int64, error)
}

// ValidateDetectorPageResult 防止 adapter 返回负数或超过 observation 数量的计数。
func ValidateDetectorPageResult(result DetectorPageReconcileResult, observationCount int) error {
	if result.Created < 0 || result.Reopened < 0 || result.Unchanged < 0 || result.Failed < 0 || result.Created+result.Reopened+result.Unchanged+result.Failed != int64(observationCount) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, errors.New("health detector page counters are inconsistent"))
	}
	return nil
}
