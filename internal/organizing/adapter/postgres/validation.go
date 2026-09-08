package postgres

import (
	"errors"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func validateBinding(binding organizingapp.CommandBinding) error {
	key := strings.TrimSpace(binding.IdempotencyKey)
	if !validID(binding.WorkspaceID) || key == "" || key != binding.IdempotencyKey ||
		len(key) > organizingapp.MaxIdempotencyKeyBytes || strings.ContainsAny(key, "\r\n\x00") ||
		!validHash(binding.RequestHash) || !validCommandType(binding.CommandType) || binding.ExpectedVersion < 0 ||
		(binding.AggregateID != "" && !validID(binding.AggregateID)) ||
		(binding.AggregateID == "" && !serverGeneratedAggregateCommand(binding.CommandType)) {
		return invalid(errors.New("organizing command binding is invalid"))
	}
	return nil
}

func serverGeneratedAggregateCommand(commandType string) bool {
	return commandType == organizingapp.CommandCreateDraft || commandType == organizingapp.CommandCreateTemplate ||
		commandType == organizingapp.CommandCloneTemplate
}

func validCommandType(value string) bool {
	switch value {
	case organizingapp.CommandCreateDraft, organizingapp.CommandUpdateDraft, organizingapp.CommandAddMaterial,
		organizingapp.CommandRemoveMaterial, organizingapp.CommandSetMaterialSelection,
		organizingapp.CommandReplaceSuggestions, organizingapp.CommandConfirmDraft,
		organizingapp.CommandCreateTemplate, organizingapp.CommandCloneTemplate, organizingapp.CommandReviseTemplate:
		return true
	default:
		return false
	}
}

func validMaterialReplacementCommand(value string) bool {
	return value == organizingapp.CommandAddMaterial || value == organizingapp.CommandRemoveMaterial ||
		value == organizingapp.CommandSetMaterialSelection || value == organizingapp.CommandReplaceSuggestions
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validateCreateDraftRecord(record organizingapp.CreateDraftRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandCreateDraft || record.Binding.ExpectedVersion != 0 ||
		record.Binding.AggregateID != record.Draft.ID || record.Draft.Version != 1 || record.Draft.Status != domain.DraftEditing ||
		record.Draft.WorkspaceID != record.Binding.WorkspaceID || len(record.Draft.Materials) != 0 || record.Draft.Validate() != nil {
		return invalid(errors.New("organizing create draft record is invalid"))
	}
	return nil
}

func validateConfirmRecord(record organizingapp.ConfirmRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandConfirmDraft || record.Binding.AggregateID == "" || isNilInterface(record.Fence) ||
		!validID(record.SnapshotID) || !validID(record.OutboxID) || !validID(record.TemplateID) ||
		!validID(record.TemplateRevisionID) || !validHash(record.TemplateHash) || record.ConfirmedAt.IsZero() ||
		len(record.FrozenMaterials) < 1 || len(record.FrozenMaterials) != len(record.SnapshotMaterialIDs) ||
		len(record.FrozenMaterials) > domain.MaxSnapshotMaterials {
		return invalid(errors.New("organizing confirmation record is invalid"))
	}
	seenIDs := make(map[foundation.ID]struct{}, len(record.SnapshotMaterialIDs))
	for index, id := range record.SnapshotMaterialIDs {
		if !validID(id) {
			return invalid(errors.New("organizing snapshot material identity is invalid"))
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return invalid(errors.New("organizing snapshot material identity is duplicated"))
		}
		seenIDs[id] = struct{}{}
		if err := record.FrozenMaterials[index].Validate(); err != nil {
			return invalid(errors.New("organizing frozen material is invalid"))
		}
	}
	return nil
}

func verifyTemplatePolicy(policy domain.MaterialPolicy, materials []domain.MaterialRef) error {
	if len(materials) < policy.MinMaterials || len(materials) > policy.MaxMaterials || len(materials) > domain.MaxSnapshotMaterials {
		return invalid(errors.New("organizing confirmation material count violates template policy"))
	}
	allowed := make(map[domain.MaterialKind]struct{}, len(policy.AllowedKinds))
	for _, kind := range policy.AllowedKinds {
		allowed[kind] = struct{}{}
	}
	for _, material := range materials {
		if _, ok := allowed[material.Kind]; !ok {
			return invalid(errors.New("organizing confirmation material kind violates template policy"))
		}
	}
	return nil
}
