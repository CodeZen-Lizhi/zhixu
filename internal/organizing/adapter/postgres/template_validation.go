package postgres

import (
	"errors"
	"reflect"

	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func validateCreateTemplateRecord(record organizingapp.CreateTemplateRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandCreateTemplate && record.Binding.CommandType != organizingapp.CommandCloneTemplate {
		return invalid(errors.New("organizing template create command type is invalid"))
	}
	if record.Binding.ExpectedVersion != 0 || record.Binding.AggregateID != record.Template.ID ||
		record.Template.Owner != domain.TemplateCustom || record.Template.WorkspaceID != record.Binding.WorkspaceID ||
		record.Template.Version != 1 || record.Revision.RevisionNo != 1 || record.Template.CurrentRevisionID != record.Revision.ID ||
		record.Template.ID != record.Revision.TemplateID || record.Revision.WorkspaceID != record.Binding.WorkspaceID ||
		record.Template.Kind != record.Revision.Declaration.Kind || record.Template.Validate() != nil ||
		record.Revision.Validate(domain.TemplateCustom) != nil {
		return invalid(errors.New("organizing template create record is invalid"))
	}
	return nil
}

func validateReviseTemplateRecord(record organizingapp.ReviseTemplateRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandReviseTemplate || record.Binding.ExpectedVersion < 1 ||
		record.Binding.AggregateID != record.Template.ID || record.Template.Owner != domain.TemplateCustom ||
		record.Template.WorkspaceID != record.Binding.WorkspaceID || record.Template.Version != record.Binding.ExpectedVersion+1 ||
		record.Revision.RevisionNo != record.Template.Version || record.Template.CurrentRevisionID != record.Revision.ID ||
		record.Template.ID != record.Revision.TemplateID || record.Revision.WorkspaceID != record.Binding.WorkspaceID ||
		record.Template.Kind != record.Revision.Declaration.Kind || record.Template.Validate() != nil ||
		record.Revision.Validate(domain.TemplateCustom) != nil {
		return invalid(errors.New("organizing template revision record is invalid"))
	}
	return nil
}

func sameBuiltIn(detail organizingapp.TemplateDetail, template domain.Template, revision domain.TemplateRevision) bool {
	return detail.Template.ID == template.ID && detail.Template.Owner == domain.TemplateBuiltIn &&
		detail.Template.WorkspaceID == "" && detail.Template.Kind == template.Kind && detail.Template.Version == 1 &&
		detail.Template.CurrentRevisionID == revision.ID && detail.Revision.ID == revision.ID &&
		detail.Revision.TemplateID == template.ID && detail.Revision.WorkspaceID == "" &&
		detail.Revision.RevisionNo == 1 && detail.Revision.DeclarationHash == revision.DeclarationHash &&
		reflect.DeepEqual(detail.Revision.Declaration, revision.Declaration)
}
