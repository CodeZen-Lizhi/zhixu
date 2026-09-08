package postgres

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func draftToModel(draft domain.Draft) draftModel {
	return draftModel{ID: string(draft.ID), WorkspaceID: string(draft.WorkspaceID), Intent: draft.Intent,
		Status: string(draft.Status), TemplateRevisionID: optionalString(string(draft.TemplateRevisionID)),
		ConfirmedSnapshotID: optionalString(string(draft.ConfirmedSnapshotID)), Version: draft.Version,
		CreatedAt: draft.CreatedAt.UTC(), UpdatedAt: draft.UpdatedAt.UTC()}
}

func (model draftModel) draft() domain.Draft {
	return domain.Draft{ID: foundation.ID(model.ID), WorkspaceID: foundation.ID(model.WorkspaceID), Intent: model.Intent,
		Status: domain.DraftStatus(model.Status), TemplateRevisionID: foundation.ID(stringValue(model.TemplateRevisionID)),
		ConfirmedSnapshotID: foundation.ID(stringValue(model.ConfirmedSnapshotID)), Version: model.Version,
		CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(), Materials: []domain.DraftMaterial{}}
}

func referenceToModel(reference domain.MaterialRef) (materialRefModel, error) {
	evidence, err := encodeEvidence(reference.Evidence)
	if err != nil {
		return materialRefModel{}, invalid(err)
	}
	return materialRefModel{Kind: string(reference.Kind), SourceVersionID: optionalString(string(reference.SourceVersionID)),
		DocumentID: optionalString(string(reference.DocumentID)), ArticleRevisionID: optionalString(string(reference.ArticleRevisionID)),
		ClaimID: optionalString(string(reference.ClaimID)), CollectionID: optionalString(string(reference.CollectionID)),
		OriginCollectionID: optionalString(string(reference.OriginCollectionID)), ProfileRevisionID: optionalString(string(reference.ProfileRevisionID)),
		MaterialVersion: reference.Version, ContentHash: optionalString(reference.ContentHash), QueryHash: optionalString(reference.QueryHash),
		ReadModelRevision: optionalString(reference.ReadModelRevision), Evidence: organizingJSONB(evidence)}, nil
}

func (model materialRefModel) reference() (domain.MaterialRef, error) {
	return decodeMaterialRef(model.Kind, stringValue(model.SourceVersionID), stringValue(model.DocumentID),
		stringValue(model.ArticleRevisionID), stringValue(model.ClaimID), stringValue(model.CollectionID),
		stringValue(model.OriginCollectionID), stringValue(model.ProfileRevisionID), model.MaterialVersion,
		stringValue(model.ContentHash), stringValue(model.QueryHash), stringValue(model.ReadModelRevision), model.Evidence)
}

func gormLoadCurrentDraft(tx *gorm.DB, workspaceID, draftID foundation.ID, lock bool) (domain.Draft, error) {
	var model draftModel
	query := tx.Select(gormDraftColumns).Where("workspace_id = ? AND id = ?", string(workspaceID), string(draftID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&model).Error; err != nil {
		if gormNoRows(err) {
			return domain.Draft{}, notFound(err)
		}
		return domain.Draft{}, classifyGORM(err)
	}
	return gormLoadDraftMaterials(tx, model.draft())
}

func gormLoadDraftVersion(tx *gorm.DB, workspaceID, draftID foundation.ID, version int64) (domain.Draft, error) {
	var model draftVersionModel
	err := tx.Select(gormDraftVersionColumns).Where("workspace_id = ? AND draft_id = ? AND version = ?",
		string(workspaceID), string(draftID), version).Take(&model).Error
	if err != nil {
		if gormNoRows(err) {
			return domain.Draft{}, notFound(err)
		}
		return domain.Draft{}, classifyGORM(err)
	}
	draft := draftModel{ID: model.DraftID, WorkspaceID: model.WorkspaceID, Intent: model.Intent, Status: model.Status,
		TemplateRevisionID: model.TemplateRevisionID, ConfirmedSnapshotID: model.ConfirmedSnapshotID, Version: model.Version,
		CreatedAt: model.DraftCreatedAt, UpdatedAt: model.UpdatedAt}.draft()
	return gormLoadDraftMaterials(tx, draft)
}

func gormLoadDraftMaterials(tx *gorm.DB, draft domain.Draft) (domain.Draft, error) {
	var models []draftMaterialModel
	err := tx.Select(gormDraftMaterialColumns).Where("workspace_id = ? AND draft_id = ? AND draft_version = ?",
		string(draft.WorkspaceID), string(draft.ID), draft.Version).Order("position").Limit(domain.MaxDraftMaterials + 1).Find(&models).Error
	if err != nil {
		return domain.Draft{}, classifyGORM(err)
	}
	if len(models) > domain.MaxDraftMaterials {
		return domain.Draft{}, inconsistent(errors.New("stored organizing material set exceeds the bound"))
	}
	for index, model := range models {
		if model.Position != index || model.WorkspaceID != string(draft.WorkspaceID) || model.DraftID != string(draft.ID) || model.DraftVersion != draft.Version {
			return domain.Draft{}, inconsistent(errors.New("stored organizing material binding is invalid"))
		}
		reference, err := model.Reference.reference()
		if err != nil {
			return domain.Draft{}, err
		}
		reasons := make([]domain.SuggestionReasonCode, len(model.Reasons))
		for index, reason := range model.Reasons {
			reasons[index] = domain.SuggestionReasonCode(reason)
		}
		draft.Materials = append(draft.Materials, domain.DraftMaterial{ID: foundation.ID(model.ID), DraftID: draft.ID, Ref: reference,
			Title: model.Title, Reasons: reasons, Origin: domain.MaterialOrigin(model.Origin), Availability: domain.MaterialAvailability(model.Availability),
			Score: model.Score, Selected: model.Selected, Position: model.Position, CreatedAt: model.CreatedAt.UTC()})
	}
	if err := draft.Validate(); err != nil {
		return domain.Draft{}, inconsistent(err)
	}
	return draft, nil
}

func gormLoadSnapshot(tx *gorm.DB, workspaceID, snapshotID foundation.ID) (domain.Snapshot, error) {
	var model snapshotModel
	if err := tx.Select(gormSnapshotColumns).Where("workspace_id = ? AND id = ?", string(workspaceID), string(snapshotID)).Take(&model).Error; err != nil {
		if gormNoRows(err) {
			return domain.Snapshot{}, notFound(err)
		}
		return domain.Snapshot{}, classifyGORM(err)
	}
	snapshot := domain.Snapshot{ID: foundation.ID(model.ID), WorkspaceID: foundation.ID(model.WorkspaceID), DraftID: foundation.ID(model.DraftID),
		DraftVersion: model.DraftVersion, TemplateID: foundation.ID(model.TemplateID), TemplateRevisionID: foundation.ID(model.TemplateRevisionID),
		TemplateHash: model.TemplateHash, Intent: model.Intent, Hash: model.SnapshotHash, CreatedAt: model.CreatedAt.UTC(), Materials: []domain.MaterialRef{}}
	var models []snapshotMaterialModel
	err := tx.Select(gormSnapshotMaterialColumns).Where("workspace_id = ? AND snapshot_id = ?", string(workspaceID), string(snapshotID)).
		Order("position").Limit(domain.MaxSnapshotMaterials + 1).Find(&models).Error
	if err != nil {
		return domain.Snapshot{}, classifyGORM(err)
	}
	if len(models) > domain.MaxSnapshotMaterials {
		return domain.Snapshot{}, inconsistent(errors.New("stored snapshot material set exceeds the bound"))
	}
	for index, material := range models {
		if material.Position != index || material.WorkspaceID != string(workspaceID) || material.SnapshotID != string(snapshotID) {
			return domain.Snapshot{}, inconsistent(errors.New("stored snapshot material binding is invalid"))
		}
		reference, err := material.Reference.reference()
		if err != nil {
			return domain.Snapshot{}, err
		}
		snapshot.Materials = append(snapshot.Materials, reference)
	}
	if err := snapshot.Validate(); err != nil {
		return domain.Snapshot{}, inconsistent(err)
	}
	return snapshot, nil
}

func gormLoadReceipt(tx *gorm.DB, workspaceID foundation.ID, key string) (receiptRow, bool, error) {
	receipt, err := scanReceipt(tx.Raw(`SELECT `+receiptSelect+` FROM organizing.command_receipt AS r
		WHERE r.workspace_id=? AND r.idempotency_key=? FOR UPDATE`, string(workspaceID), key).Row())
	if gormNoRows(err) {
		return receiptRow{}, false, nil
	}
	if err != nil {
		return receiptRow{}, false, classifyGORM(err)
	}
	return receipt, true, nil
}

func gormReplayDraft(tx *gorm.DB, binding organizingapp.CommandBinding) (organizingapp.DraftResult, bool, error) {
	receipt, found, err := gormLoadReceipt(tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return organizingapp.DraftResult{}, found, err
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	if receipt.draftID == "" || receipt.resultDraftVersion < 1 {
		return organizingapp.DraftResult{}, false, inconsistent(errors.New("organizing draft receipt is incomplete"))
	}
	draft, err := gormLoadDraftVersion(tx, binding.WorkspaceID, foundation.ID(receipt.draftID), receipt.resultDraftVersion)
	if err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	return organizingapp.DraftResult{Draft: draft, Replayed: true}, true, nil
}

func gormReplayConfirm(tx *gorm.DB, binding organizingapp.CommandBinding) (organizingapp.ConfirmResult, bool, error) {
	receipt, found, err := gormLoadReceipt(tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return organizingapp.ConfirmResult{}, found, err
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	if receipt.commandType != organizingapp.CommandConfirmDraft || receipt.draftID == "" || receipt.resultDraftVersion < 1 || receipt.snapshotID == "" || receipt.outboxID == "" {
		return organizingapp.ConfirmResult{}, false, inconsistent(errors.New("organizing confirmation receipt is incomplete"))
	}
	draft, err := gormLoadDraftVersion(tx, binding.WorkspaceID, foundation.ID(receipt.draftID), receipt.resultDraftVersion)
	if err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	snapshot, err := gormLoadSnapshot(tx, binding.WorkspaceID, foundation.ID(receipt.snapshotID))
	if err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	return organizingapp.ConfirmResult{Draft: draft, Snapshot: snapshot, OutboxID: foundation.ID(receipt.outboxID), Replayed: true}, true, nil
}

func gormInsertDraftVersion(tx *gorm.DB, draft domain.Draft) error {
	model := draftVersionModel{WorkspaceID: string(draft.WorkspaceID), DraftID: string(draft.ID), Version: draft.Version,
		Intent: draft.Intent, Status: string(draft.Status), TemplateRevisionID: optionalString(string(draft.TemplateRevisionID)),
		ConfirmedSnapshotID: optionalString(string(draft.ConfirmedSnapshotID)), DraftCreatedAt: draft.CreatedAt.UTC(), UpdatedAt: draft.UpdatedAt.UTC()}
	return classifyGORM(tx.Create(&model).Error)
}

func gormUpdateDraftCurrent(tx *gorm.DB, draft domain.Draft, expectedVersion int64) error {
	result := tx.Model(&draftModel{}).Where("workspace_id = ? AND id = ? AND version = ?", string(draft.WorkspaceID), string(draft.ID), expectedVersion).
		Updates(map[string]any{"intent": draft.Intent, "status": string(draft.Status), "template_revision_id": optionalString(string(draft.TemplateRevisionID)),
			"confirmed_snapshot_id": optionalString(string(draft.ConfirmedSnapshotID)), "version": draft.Version, "updated_at": draft.UpdatedAt.UTC()})
	if result.Error != nil {
		return classifyGORM(result.Error)
	}
	if result.RowsAffected != 1 {
		return versionConflict(errors.New("organizing draft compare-and-swap lost"))
	}
	return nil
}

func gormInsertDraftMaterials(tx *gorm.DB, draft domain.Draft) error {
	if len(draft.Materials) == 0 {
		return nil
	}
	if len(draft.Materials) > domain.MaxDraftMaterials {
		return invalid(errors.New("organizing draft material batch exceeds the bound"))
	}
	models := make([]draftMaterialModel, len(draft.Materials))
	for index, material := range draft.Materials {
		if material.Validate() != nil || material.DraftID != draft.ID || material.Position != index {
			return invalid(errors.New("organizing draft material batch is invalid"))
		}
		reference, err := referenceToModel(material.Ref)
		if err != nil {
			return err
		}
		reasons := make([]string, len(material.Reasons))
		for index, reason := range material.Reasons {
			reasons[index] = string(reason)
		}
		models[index] = draftMaterialModel{ID: string(material.ID), WorkspaceID: string(draft.WorkspaceID), DraftID: string(draft.ID), DraftVersion: draft.Version,
			Position: index, Reference: reference, Title: material.Title, Reasons: reasons, Origin: string(material.Origin), Availability: string(material.Availability),
			Score: material.Score, Selected: material.Selected, CreatedAt: material.CreatedAt.UTC()}
	}
	return classifyGORM(tx.CreateInBatches(&models, domain.MaxDraftMaterials).Error)
}

func gormInsertSnapshotMaterials(tx *gorm.DB, snapshot domain.Snapshot, ids []foundation.ID) error {
	if len(snapshot.Materials) == 0 || len(snapshot.Materials) > domain.MaxSnapshotMaterials || len(snapshot.Materials) != len(ids) {
		return invalid(errors.New("organizing snapshot material batch is invalid"))
	}
	models := make([]snapshotMaterialModel, len(ids))
	for index, material := range snapshot.Materials {
		reference, err := referenceToModel(material)
		if err != nil {
			return err
		}
		models[index] = snapshotMaterialModel{ID: string(ids[index]), WorkspaceID: string(snapshot.WorkspaceID), SnapshotID: string(snapshot.ID),
			Position: index, Reference: reference, CreatedAt: snapshot.CreatedAt.UTC()}
	}
	return classifyGORM(tx.CreateInBatches(&models, domain.MaxSnapshotMaterials).Error)
}

func gormInsertDraftReceipt(tx *gorm.DB, binding organizingapp.CommandBinding, draftID foundation.ID,
	version int64, snapshotID, outboxID foundation.ID, createdAt time.Time,
) error {
	model := commandReceiptModel{WorkspaceID: string(binding.WorkspaceID), IdempotencyKey: binding.IdempotencyKey, RequestHash: binding.RequestHash,
		CommandType: binding.CommandType, AggregateID: string(draftID), ExpectedVersion: binding.ExpectedVersion,
		DraftID: optionalString(string(draftID)), ResultDraftVersion: &version, SnapshotID: optionalString(string(snapshotID)), OutboxID: optionalString(string(outboxID)),
		CreatedAt: canonicalTime(createdAt)}
	return classifyGORM(tx.Create(&model).Error)
}
