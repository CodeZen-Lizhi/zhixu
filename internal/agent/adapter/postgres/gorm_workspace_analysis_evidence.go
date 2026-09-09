package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type gormWorkspaceAnalysisEvidenceRow struct {
	WorkspaceID       string    `gorm:"column:workspace_id"`
	AnalysisRunID     string    `gorm:"column:analysis_run_id"`
	ReferenceNo       int       `gorm:"column:reference_no"`
	SearchOperationID string    `gorm:"column:search_operation_id"`
	SearchReceiptID   string    `gorm:"column:search_receipt_id"`
	SearchReceiptHash string    `gorm:"column:search_receipt_hash"`
	LocalEvidenceRef  string    `gorm:"column:local_evidence_ref"`
	CitationID        string    `gorm:"column:citation_id"`
	IndexVersionID    string    `gorm:"column:index_version_id"`
	ChunkID           string    `gorm:"column:chunk_id"`
	SourceVersionID   string    `gorm:"column:source_version_id"`
	SourceSpanID      string    `gorm:"column:source_span_id"`
	ContentHash       string    `gorm:"column:content_hash"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime:false;autoUpdateTime:false"`
}

// AppendWorkspaceAnalysisEvidenceScoped runs after the successful Search closure
// while the caller still owns the Analysis Run lock. It cannot commit by itself.
func (repository *GORMRepository) AppendWorkspaceAnalysisEvidenceScoped(ctx context.Context, scope foundation.TransactionScope, c application.AppendWorkspaceAnalysisEvidenceCommand) error {
	if ctx == nil {
		return workspaceAnalysisModelInvalid(errors.New("dynamic evidence context is nil"))
	}
	if err := c.Validate(); err != nil {
		return err
	}
	db, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return err
	}
	run, found, err := loadGORMWorkspaceAnalysisToolRunForUpdate(ctx, db, c.Identity.WorkspaceID, c.Identity.WorkflowRunID, c.OperationKey.AnalysisRunID)
	if err != nil {
		return err
	}
	if !found || run.DefinitionVersion != 2 || validateGORMWorkspaceAnalysisToolLiveRun(run, c.Identity, c.OperationKey.AnalysisRunID) != nil {
		return workspaceAnalysisModelConflict(errors.New("dynamic evidence run differs"))
	}
	a, found, err := repository.LoadWorkspaceAnalysisSuccessfulToolAuthorityScoped(ctx, scope, application.WorkspaceAnalysisSuccessfulToolAuthorityQuery{WorkspaceID: c.Identity.WorkspaceID, WorkflowRunID: c.Identity.WorkflowRunID, OperationKey: c.OperationKey})
	if err != nil {
		return err
	}
	if !found || a.OperationID != c.OperationID || a.ResultID != c.ReceiptID || a.ResultHash != c.ReceiptHash {
		return workspaceAnalysisModelConflict(errors.New("dynamic evidence search closure differs"))
	}
	all, err := repository.LoadWorkspaceAnalysisEvidenceScoped(ctx, scope, application.WorkspaceAnalysisEvidenceQuery{WorkspaceID: c.Identity.WorkspaceID, WorkflowRunID: c.Identity.WorkflowRunID, AnalysisRunID: c.OperationKey.AnalysisRunID})
	if err != nil {
		return err
	}
	var existing []application.WorkspaceAnalysisEvidenceBinding
	for _, b := range all {
		if b.SearchReceiptID == c.ReceiptID {
			existing = append(existing, b)
		}
	}
	if len(existing) > 0 {
		if len(existing) != len(c.Items) {
			return consistency(errors.New("dynamic evidence replay count differs"))
		}
		for i, b := range existing {
			if b.SearchOperationID != c.OperationID || b.SearchReceiptHash != c.ReceiptHash || b.WorkspaceAnalysisEvidenceSeed != c.Items[i] {
				return consistency(errors.New("dynamic evidence replay tuple differs"))
			}
		}
		return nil
	}
	if len(all)+len(c.Items) > domain.WorkspaceAnalysisV2MaxEvidenceRefs {
		return workspaceAnalysisModelConflict(errors.New("dynamic evidence capacity exceeded"))
	}
	if len(c.Items) == 0 {
		return nil
	}
	now, err := loadGORMWorkspaceAnalysisToolDatabaseTime(ctx, db)
	if err != nil {
		return err
	}
	rows := make([]gormWorkspaceAnalysisEvidenceRow, len(c.Items))
	for i, item := range c.Items {
		rows[i] = gormWorkspaceAnalysisEvidenceRow{WorkspaceID: string(c.Identity.WorkspaceID), AnalysisRunID: string(c.OperationKey.AnalysisRunID), ReferenceNo: len(all) + i + 1, SearchOperationID: string(c.OperationID), SearchReceiptID: string(c.ReceiptID), SearchReceiptHash: c.ReceiptHash, LocalEvidenceRef: item.LocalEvidenceRef, CitationID: item.CitationID, IndexVersionID: string(item.IndexVersionID), ChunkID: string(item.ChunkID), SourceVersionID: string(item.SourceVersionID), SourceSpanID: string(item.SourceSpanID), ContentHash: item.ContentHash, CreatedAt: now}
	}
	result := db.Table("agent.workspace_analysis_evidence").Create(&rows)
	if result.Error != nil {
		return classifyGORM(ctx, result.Error)
	}
	if result.RowsAffected != int64(len(rows)) {
		return consistency(errors.New("dynamic evidence append did not persist all references"))
	}
	return nil
}

func (repository *GORMRepository) LoadWorkspaceAnalysisEvidenceScoped(ctx context.Context, scope foundation.TransactionScope, q application.WorkspaceAnalysisEvidenceQuery) ([]application.WorkspaceAnalysisEvidenceBinding, error) {
	if ctx == nil || validateWorkspaceAnalysisEvidenceQuery(q) != nil {
		return nil, workspaceAnalysisModelInvalid(errors.New("dynamic evidence query is invalid"))
	}
	db, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return nil, err
	}
	run, found, err := loadGORMWorkspaceAnalysisToolAuthorityRun(ctx, db, q.WorkspaceID, q.WorkflowRunID)
	if err != nil {
		return nil, err
	}
	if !found || run.ID != q.AnalysisRunID || run.DefinitionVersion != 2 {
		return nil, notFound()
	}
	type readRow struct {
		Evidence gormWorkspaceAnalysisEvidenceRow `gorm:"embedded"`
		Ordinal  int                              `gorm:"column:ordinal"`
	}
	var rows []readRow
	result := db.Table("agent.workspace_analysis_evidence AS evidence").Select("evidence.*, operation.ordinal").Joins("JOIN agent.workspace_analysis_operation AS operation ON operation.id=evidence.search_operation_id AND operation.workspace_id=evidence.workspace_id AND operation.analysis_run_id=evidence.analysis_run_id").Where("evidence.workspace_id=? AND evidence.analysis_run_id=? AND operation.workflow_run_id=? AND operation.node_key=? AND operation.operation_kind=? AND operation.status=?", string(q.WorkspaceID), string(q.AnalysisRunID), string(q.WorkflowRunID), string(domain.WorkspaceAnalysisOperationNodeDecideNext), string(domain.WorkspaceAnalysisOperationKnowledgeSearch), string(domain.WorkspaceAnalysisOperationSucceeded)).Order("evidence.reference_no").Limit(domain.WorkspaceAnalysisV2MaxEvidenceRefs + 1).Find(&rows)
	if result.Error != nil {
		return nil, classifyGORM(ctx, result.Error)
	}
	if len(rows) > domain.WorkspaceAnalysisV2MaxEvidenceRefs {
		return nil, consistency(errors.New("dynamic evidence namespace exceeds bound"))
	}
	bindings := make([]application.WorkspaceAnalysisEvidenceBinding, len(rows))
	for i, value := range rows {
		row := value.Evidence
		if row.ReferenceNo != i+1 || value.Ordinal < 1 || value.Ordinal > domain.WorkspaceAnalysisV2MaxDecisions || !domain.ValidWorkspaceAnalysisV2EvidenceRef("E"+strconv.Itoa(row.ReferenceNo)) {
			return nil, consistency(errors.New("dynamic evidence namespace has a gap"))
		}
		bindings[i] = application.WorkspaceAnalysisEvidenceBinding{WorkspaceAnalysisEvidenceSeed: application.WorkspaceAnalysisEvidenceSeed{LocalEvidenceRef: row.LocalEvidenceRef, CitationID: row.CitationID, IndexVersionID: foundation.ID(row.IndexVersionID), ChunkID: foundation.ID(row.ChunkID), SourceVersionID: foundation.ID(row.SourceVersionID), SourceSpanID: foundation.ID(row.SourceSpanID), ContentHash: row.ContentHash}, ReferenceNo: row.ReferenceNo, SearchOperationKey: domain.WorkspaceAnalysisOperationKey{AnalysisRunID: q.AnalysisRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeDecideNext, Kind: domain.WorkspaceAnalysisOperationKnowledgeSearch, Ordinal: value.Ordinal}, SearchOperationID: foundation.ID(row.SearchOperationID), SearchReceiptID: foundation.ID(row.SearchReceiptID), SearchReceiptHash: row.SearchReceiptHash}
	}
	return bindings, nil
}

func (repository *GORMRepository) LoadWorkspaceAnalysisSuccessfulReadKeysScoped(ctx context.Context, scope foundation.TransactionScope, q application.WorkspaceAnalysisEvidenceQuery) ([]domain.WorkspaceAnalysisOperationKey, error) {
	if ctx == nil || validateWorkspaceAnalysisEvidenceQuery(q) != nil {
		return nil, workspaceAnalysisModelInvalid(errors.New("dynamic read query is invalid"))
	}
	db, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return nil, err
	}
	run, found, err := loadGORMWorkspaceAnalysisToolAuthorityRun(ctx, db, q.WorkspaceID, q.WorkflowRunID)
	if err != nil {
		return nil, err
	}
	if !found || run.ID != q.AnalysisRunID || run.DefinitionVersion != 2 {
		return nil, notFound()
	}
	var rows []struct {
		Ordinal int `gorm:"column:ordinal"`
	}
	result := db.Table("agent.workspace_analysis_operation").Select("ordinal").Where("workspace_id=? AND analysis_run_id=? AND workflow_run_id=? AND node_key=? AND operation_kind=? AND status=?", string(q.WorkspaceID), string(q.AnalysisRunID), string(q.WorkflowRunID), string(domain.WorkspaceAnalysisOperationNodeDecideNext), string(domain.WorkspaceAnalysisOperationSourceRead), string(domain.WorkspaceAnalysisOperationSucceeded)).Order("ordinal").Limit(domain.WorkspaceAnalysisV2MaxSourceReads + 1).Find(&rows)
	if result.Error != nil {
		return nil, classifyGORM(ctx, result.Error)
	}
	if len(rows) > domain.WorkspaceAnalysisV2MaxSourceReads {
		return nil, consistency(errors.New("dynamic successful read count exceeds bound"))
	}
	keys := make([]domain.WorkspaceAnalysisOperationKey, len(rows))
	for i, row := range rows {
		keys[i] = domain.WorkspaceAnalysisOperationKey{AnalysisRunID: q.AnalysisRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeDecideNext, Kind: domain.WorkspaceAnalysisOperationSourceRead, Ordinal: row.Ordinal}
		if keys[i].Validate() != nil {
			return nil, consistency(errors.New("dynamic read operation key is invalid"))
		}
	}
	return keys, nil
}

func validateWorkspaceAnalysisEvidenceQuery(q application.WorkspaceAnalysisEvidenceQuery) error {
	ids := []foundation.ID{q.WorkspaceID, q.WorkflowRunID, q.AnalysisRunID}
	for i, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return errors.New("dynamic evidence owner is invalid")
		}
		for _, other := range ids[:i] {
			if id == other {
				return errors.New("dynamic evidence owner is reused")
			}
		}
	}
	return nil
}

var _ application.ScopedWorkspaceAnalysisEvidenceParticipant = (*GORMRepository)(nil)
