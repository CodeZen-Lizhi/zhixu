package postgres

import (
	"errors"
	"fmt"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	artifactSchemaVersion         = "artifact/v1"
	artifactRevisionSchemaVersion = domain.RevisionSchemaV1
	artifactReceiptSchemaVersion  = "artifact-command-receipt/v1"
)

func validateExternalReservationBinding(binding artifactapp.CommandBinding) error {
	if err := validateBinding(binding, true); err != nil {
		return err
	}
	if binding.CommandType != artifactapp.CommandExportMarkdown && binding.CommandType != artifactapp.CommandPublish {
		return requestInvalid(errors.New("artifact external transition command is invalid"))
	}
	return nil
}

func validateCreateRecord(record artifactapp.CreateRecord) error {
	if err := validateBinding(record.Binding, true); err != nil {
		return err
	}
	if record.Binding.CommandType != artifactapp.CommandPlan || record.Binding.ExpectedVersion != 0 ||
		record.Binding.WorkspaceID != record.State.Artifact.WorkspaceID || record.Binding.ArtifactID != record.State.Artifact.ID ||
		record.State.Artifact.Version != 1 || record.State.Revision.RevisionNo != 1 {
		return requestInvalid(errors.New("artifact create record is invalid"))
	}
	if err := artifactapp.ValidateState(record.State); err != nil {
		return requestInvalid(fmt.Errorf("artifact create state is invalid: %w", err))
	}
	if err := artifactapp.ValidateVisibilityHoldBinding(
		record.VisibilityHold,
		record.State.Artifact.Type,
		record.Binding.IdempotencyKey,
	); err != nil {
		return err
	}
	return nil
}

func validateTransitionRecord(record artifactapp.TransitionRecord) error {
	if err := validateBinding(record.Binding, true); err != nil {
		return err
	}
	if record.Binding.CommandType == artifactapp.CommandPlan || record.Binding.ExpectedVersion < 1 ||
		!validID(record.CurrentRevisionID) || record.State.Artifact.ID != record.Binding.ArtifactID ||
		record.State.Artifact.WorkspaceID != record.Binding.WorkspaceID ||
		record.State.Artifact.Version != record.Binding.ExpectedVersion+1 {
		return requestInvalid(errors.New("artifact transition record is invalid"))
	}
	if err := artifactapp.ValidateState(record.State); err != nil {
		return requestInvalid(fmt.Errorf("artifact transition state is invalid: %w", err))
	}
	switch record.Binding.CommandType {
	case artifactapp.CommandExportMarkdown:
		if record.Export == nil || record.Publication != nil {
			return requestInvalid(errors.New("artifact export transition must contain only an export fact"))
		}
	case artifactapp.CommandPublish:
		if record.Export != nil || record.Publication == nil {
			return requestInvalid(errors.New("artifact publish transition must contain only a publication fact"))
		}
	default:
		if record.Export != nil || record.Publication != nil {
			return requestInvalid(errors.New("artifact non-external transition must not contain a side fact"))
		}
	}
	if record.Export != nil {
		if err := artifactapp.ValidateExportRecord(*record.Export); err != nil || record.Export.WorkspaceID != record.State.Artifact.WorkspaceID || record.Export.ArtifactID != record.State.Artifact.ID || record.Export.RevisionID != record.State.Revision.ID || record.Export.ArtifactVersion != record.State.Artifact.Version || record.Export.RevisionNo != record.State.Revision.RevisionNo || record.Export.RevisionHash != record.State.Revision.ContentHash {
			return requestInvalid(errors.New("artifact transition export binding is invalid"))
		}
	}
	if record.Publication != nil {
		if err := artifactapp.ValidatePublicationRecord(*record.Publication); err != nil || record.Publication.WorkspaceID != record.State.Artifact.WorkspaceID || record.Publication.ArtifactID != record.State.Artifact.ID || record.Publication.RevisionID != record.State.Revision.ID || record.Publication.ArtifactVersion != record.State.Artifact.Version || record.Publication.RevisionNo != record.State.Revision.RevisionNo || record.Publication.ContentHash != record.State.Revision.ContentHash || record.Publication.IdempotencyKey != record.Binding.IdempotencyKey {
			return requestInvalid(errors.New("artifact transition publication binding is invalid"))
		}
	}
	return nil
}

func validateBinding(binding artifactapp.CommandBinding, requireArtifact bool) error {
	if !validID(binding.WorkspaceID) || (requireArtifact && !validID(binding.ArtifactID)) || binding.IdempotencyKey == "" || len(binding.IdempotencyKey) > 128 || strings.ContainsAny(binding.IdempotencyKey, "\r\n") || !validHash(binding.RequestHash) || binding.CommandType == "" || len(binding.CommandType) > 64 || binding.ExpectedVersion < 0 {
		return requestInvalid(errors.New("artifact command binding is invalid"))
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
