package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

type sourceReviewCommandRow struct {
	WorkspaceID, IdempotencyKey, Operation, RequestHash, ReviewID, ResultReviewID string
	ExpectedVersion                                                               int64
	CreatedAt                                                                     time.Time
}

func (sourceReviewCommandRow) TableName() string {
	return "organizing.synthesis_manuscript_source_review_command"
}

type sourceReviewRecoveryRow struct {
	ID, WorkspaceID, ReviewID, WorkflowRunID, CommandKey string
	CreatedAt                                            time.Time
}

func (sourceReviewRecoveryRow) TableName() string {
	return "organizing.synthesis_manuscript_source_review_recovery"
}

func (s *GORMSynthesisManuscriptSourceReviewStore) ConfigureSourceReviewCommands(starter workflowapp.ScopedRuntimeStarter, definitions *workflowapp.DefinitionRegistry) error {
	if isNilInterface(starter) || definitions == nil {
		return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	if state, ok := starter.(workflowapp.RuntimeStatePort); ok {
		control, err := workflowapp.NewRuntimeCoordinator(state)
		if err != nil {
			return err
		}
		s.control = control
	}
	s.starter, s.definitions = starter, definitions
	return nil
}
func (s *GORMSynthesisManuscriptSourceReviewStore) ExecuteSourceReviewCommand(ctx context.Context, c app.SynthesisManuscriptSourceReviewCommand, operation string) (app.SynthesisSourceReviewView, error) {
	if !validID(c.WorkspaceID) || !validID(c.ReviewID) || c.ExpectedVersion < 1 || len(c.IdempotencyKey) < 1 || len(c.IdempotencyKey) > 128 || (operation != "RECHECK" && operation != "RECOVER") {
		return app.SynthesisSourceReviewView{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_COMMAND_INVALID")
	}
	raw, err := json.Marshal(struct {
		Command   app.SynthesisManuscriptSourceReviewCommand
		Operation string
	}{c, operation})
	if err != nil {
		return app.SynthesisSourceReviewView{}, err
	}
	hash := sha256Hex(raw)
	result := c.ReviewID
	err = s.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if _, err := s.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, c.WorkspaceID); err != nil {
			return err
		}
		// 先串行化同键竞争者，再检查当前版本和最新状态。
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", string(c.WorkspaceID)+":source-review:"+c.IdempotencyKey).Error; err != nil {
			return err
		}
		var previous sourceReviewCommandRow
		err := tx.Where("workspace_id=? AND idempotency_key=?", string(c.WorkspaceID), c.IdempotencyKey).Take(&previous).Error
		if err == nil {
			if previous.RequestHash != hash {
				return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CONFLICT")
			}
			result = foundation.ID(previous.ResultReviewID)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		first, err := readSourceReview(tx, c.WorkspaceID, c.ReviewID, false)
		if err != nil {
			return err
		}
		// 来源锁串行化不同键；工作流锁先于评审锁。
		if err = tx.Exec("SELECT id FROM organizing.synthesis_processing WHERE workspace_id=? AND id=? FOR UPDATE", first.WorkspaceID, first.OriginProcessingID).Error; err != nil {
			return err
		}
		var execution struct{ Status string }
		if err = tx.Raw("SELECT status FROM workflow.run WHERE workspace_id=? AND id=? FOR UPDATE", first.WorkspaceID, first.WorkflowRunID).Scan(&execution).Error; err != nil {
			return err
		}
		row, err := readSourceReview(tx, c.WorkspaceID, c.ReviewID, true)
		if err != nil {
			return err
		}
		if row.Version != c.ExpectedVersion {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CONFLICT")
		}
		var latest int
		if err = tx.Model(&sourceReviewRow{}).Select("max(attempt_no)").Where("workspace_id=? AND origin_processing_id=? AND origin_workflow_run_id=?", row.WorkspaceID, row.OriginProcessingID, row.OriginWorkflowRunID).Scan(&latest).Error; err != nil {
			return err
		}
		if latest != row.AttemptNo {
			return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_NOT_LATEST")
		}
		terminal := execution.Status == "succeeded" || execution.Status == "failed" || execution.Status == "cancelled"
		if operation == "RECOVER" {
			var receipt int64
			if err = tx.Table("organizing.synthesis_manuscript_source_review_recovery_receipt").Where("workspace_id=? AND review_id=?", row.WorkspaceID, row.ID).Count(&receipt).Error; err != nil {
				return err
			}
			if row.Status != "SUCCEEDED" && receipt == 0 {
				if !terminal {
					return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_ACTIVE")
				}
				if (row.Status != "REVIEWED" && row.Status != "STALE") || len(row.Output) == 0 {
					return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_NOT_RECOVERABLE")
				}
				if _, err = s.verifyModel(ctx, scope, row, row.Output, true); err != nil {
					return err
				}
				if _, err = s.recheck(ctx, scope, tx, row); err != nil {
					return err
				}
				var recovery sourceReviewRecoveryRow
				q := tx.Where("workspace_id=? AND review_id=?", row.WorkspaceID, row.ID).Order("created_at DESC,id DESC").Take(&recovery)
				if q.Error == nil {
					var status string
					if err = tx.Raw("SELECT status FROM workflow.run WHERE id=? AND workspace_id=? FOR UPDATE", recovery.WorkflowRunID, row.WorkspaceID).Scan(&status).Error; err != nil {
						return err
					}
					if status != "failed" && status != "cancelled" && status != "succeeded" {
						return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_ACTIVE")
					}
				}
				if q.Error != nil && !errors.Is(q.Error, gorm.ErrRecordNotFound) {
					return q.Error
				}
				id, err := s.runtime.dependencies.Storage.IDs.New()
				if err != nil {
					return err
				}
				run, err := s.startSourceReview(ctx, scope, c.WorkspaceID, c.ReviewID, app.SynthesisSourceReviewRecoveryDefinition, "source-review-recover:"+string(id))
				if err != nil {
					return err
				}
				if err = tx.Create(&sourceReviewRecoveryRow{ID: string(id), WorkspaceID: row.WorkspaceID, ReviewID: row.ID, CommandKey: c.IdempotencyKey, WorkflowRunID: string(run), CreatedAt: canonicalTime(s.runtime.dependencies.Storage.Clock.Now())}).Error; err != nil {
					return err
				}
			} else {
				if _, err = s.verifyModel(ctx, scope, row, row.Output, true); err != nil {
					return err
				}
			}
		} else {
			var activeRecovery int64
			if err = tx.Raw("SELECT count(*) FROM organizing.synthesis_manuscript_source_review_recovery x JOIN workflow.run r ON r.id=x.workflow_run_id WHERE x.workspace_id=? AND x.review_id=? AND r.status NOT IN ('succeeded','failed','cancelled')", row.WorkspaceID, row.ID).Scan(&activeRecovery).Error; err != nil {
				return err
			}
			if activeRecovery > 0 {
				return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_ACTIVE")
			}
			if row.Status == "RECOVERY_REQUIRED" {
				return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED")
			}
			if !terminal {
				return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_ACTIVE")
			}
			if row.Status != "STALE" && row.Status != "REJECTED" && row.Status != "SUCCEEDED" && !(row.Status == "FAILED" && row.Retryable) {
				return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_NOT_RECHECKABLE")
			}
			prospective := row
			prospective.AttemptNo = row.AttemptNo + 1
			current, err := s.current(ctx, scope, tx, prospective)
			if err != nil {
				return err
			}
			old, err := row.project()
			if err != nil {
				return err
			}
			same := old.Snapshot != nil && reflect.DeepEqual(*old.Snapshot, current.Snapshot)
			if same && row.Status != "FAILED" {
				var receipt int64
				if err = tx.Table("organizing.synthesis_manuscript_source_review_recovery_receipt").Where("workspace_id=? AND review_id=?", row.WorkspaceID, row.ID).Count(&receipt).Error; err != nil {
					return err
				}
				if row.Status != "SUCCEEDED" && receipt == 0 {
					return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_BASELINE_UNCHANGED")
				}
				if _, err = s.verifyModel(ctx, scope, row, row.Output, true); err != nil {
					return err
				}
			} else {
				if row.AttemptNo >= 10 {
					return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ATTEMPT_LIMIT")
				}
				id, err := s.runtime.dependencies.Storage.IDs.New()
				if err != nil {
					return err
				}
				now := canonicalTime(s.runtime.dependencies.Storage.Clock.Now())
				next := sourceReviewRow{ID: string(id), WorkspaceID: row.WorkspaceID, OriginProcessingID: row.OriginProcessingID, OriginWorkflowRunID: row.OriginWorkflowRunID, AttemptNo: row.AttemptNo + 1, SupersedesID: &row.ID, Status: "PENDING", Version: 1, CreatedAt: now, UpdatedAt: now}
				if err = tx.Create(&next).Error; err != nil {
					return err
				}
				run, err := s.startSourceReview(ctx, scope, c.WorkspaceID, id, app.SynthesisSourceReviewDefinition, "source-review-start:"+string(id))
				if err != nil {
					return err
				}
				if err = s.update(tx, next, map[string]any{"workflow_run_id": string(run)}); err != nil {
					return err
				}
				next.Version++
				runString := string(run)
				next.WorkflowRunID = &runString
				snapshot, err := json.Marshal(current.Snapshot)
				if err != nil {
					return err
				}
				if err = s.update(tx, next, map[string]any{"status": "PREPARED", "snapshot": snapshot, "snapshot_hash": sha256Hex(snapshot)}); err != nil {
					return err
				}
				result = id
			}
		}
		return tx.Create(&sourceReviewCommandRow{WorkspaceID: string(c.WorkspaceID), IdempotencyKey: c.IdempotencyKey, Operation: operation, RequestHash: hash, ReviewID: row.ID, ResultReviewID: string(result), ExpectedVersion: c.ExpectedVersion, CreatedAt: canonicalTime(s.runtime.dependencies.Storage.Clock.Now())}).Error
	})
	if err != nil {
		return app.SynthesisSourceReviewView{}, err
	}
	return s.SourceReviewView(ctx, c.WorkspaceID, result)
}
func (s *GORMSynthesisManuscriptSourceReviewStore) startSourceReview(ctx context.Context, scope foundation.TransactionScope, w, id foundation.ID, key, idempotency string) (foundation.ID, error) {
	if s.starter == nil || s.definitions == nil {
		return "", app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	definition, err := s.definitions.Resolve(key, 1)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]foundation.ID{"review_id": id})
	if err != nil {
		return "", err
	}
	request, err := workflowapp.BuildRuntimeStartRequest(s.runtime.dependencies.Storage.IDs, s.runtime.dependencies.Storage.Clock, w, idempotency, raw, definition)
	if err != nil {
		return "", err
	}
	result, err := s.starter.StartScoped(ctx, scope, request)
	if err != nil {
		return "", err
	}
	if result.Run.WorkspaceID != w || result.Job.JobID < 1 {
		return "", app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	return result.Run.ID, nil
}
