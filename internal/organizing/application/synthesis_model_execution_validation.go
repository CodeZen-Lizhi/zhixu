package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func (receipt SynthesisSemanticReceipt) Validate() error {
	if !validID(receipt.ModelRunID) || !validID(receipt.GenerationModelRunID) || receipt.ModelRunID == receipt.GenerationModelRunID ||
		!validSynthesisHash(receipt.RequestHash) || !validSynthesisHash(receipt.GenerationOutputHash) || !validSynthesisHash(receipt.OutputHash) ||
		receipt.CheckCount < 1 || receipt.CheckCount > MaxSynthesisSemanticChecks {
		return invalidSynthesisModelStep()
	}
	return nil
}

// Validate checks the persistent step shape without treating it as semantic
// proof. The model adapter additionally rebinds raw output and verifies the
// exact successful ModelCall; the store proves the Workflow/ModelRun relation.
func (record SynthesisModelStepRecord) Validate() error {
	if !validID(record.ID) || !validID(record.WorkspaceID) || !validID(record.ProcessingID) || !validID(record.WorkflowRunID) ||
		!validID(record.NodeRunID) || !validID(record.NodeAttemptID) || !validSynthesisHash(record.RequestHash) || !validSynthesisHash(record.InputRequestHash) ||
		record.ModelRunID != "" && !validID(record.ModelRunID) || record.ModelSettingsRevision != nil && *record.ModelSettingsRevision < 0 ||
		record.Version < 1 || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) ||
		!synthesisModelTime(record.CreatedAt) || !synthesisModelTime(record.UpdatedAt) {
		return invalidSynthesisModelStep()
	}
	if record.Stage == SynthesisModelGenerate {
		if record.GenerationOutputHash != "" {
			return invalidSynthesisModelStep()
		}
	} else if record.Stage != SynthesisModelValidate || !validSynthesisHash(record.GenerationOutputHash) {
		return invalidSynthesisModelStep()
	}
	switch record.Status {
	case SynthesisModelStepRunning:
		if record.CompletedAt != nil || len(record.Output) != 0 || record.OutputHash != "" || record.Generation != nil || record.Semantic != nil || record.ErrorCode != "" || record.Retryable {
			return invalidSynthesisModelStep()
		}
	case SynthesisModelStepReady:
		if !validID(record.ModelRunID) || record.CompletedAt == nil || !record.CompletedAt.Equal(record.UpdatedAt) ||
			len(record.Output) == 0 || len(record.Output) > MaxSynthesisModelOutputBytes || !json.Valid(record.Output) ||
			!validSynthesisHash(record.OutputHash) || record.ErrorCode != "" || record.Retryable {
			return invalidSynthesisModelStep()
		}
		digest := sha256.Sum256(record.Output)
		if hex.EncodeToString(digest[:]) != record.OutputHash {
			return invalidSynthesisModelStep()
		}
		if record.Stage == SynthesisModelGenerate {
			if record.Generation == nil || record.Semantic != nil || record.Generation.ModelRunID != record.ModelRunID ||
				record.Generation.RequestHash != record.InputRequestHash || record.Generation.OutputHash != record.OutputHash ||
				record.Generation.Notes == nil || len(record.Generation.Notes) > MaxSynthesisGeneratedNotes {
				return invalidSynthesisModelStep()
			}
			for _, note := range record.Generation.Notes {
				if domain.ValidateSynthesisTopic(note.TopicKey, note.Title, note.Aliases) != nil || note.Delta.Validate(record.WorkspaceID) != nil ||
					note.NoteID != "" && (!validID(note.NoteID) || !validID(note.BaseRevisionID)) || note.NoteID == "" && (note.BaseRevisionID != "" || len(note.Delta.Operations) == 0) {
					return invalidSynthesisModelStep()
				}
			}
		} else if record.Generation != nil || record.Semantic == nil || record.Semantic.Validate() != nil ||
			record.Semantic.ModelRunID != record.ModelRunID || record.Semantic.RequestHash != record.InputRequestHash ||
			record.Semantic.GenerationOutputHash != record.GenerationOutputHash || record.Semantic.OutputHash != record.OutputHash {
			return invalidSynthesisModelStep()
		}
	case SynthesisModelStepFailed, SynthesisModelStepRecoveryRequired:
		if record.CompletedAt == nil || !record.CompletedAt.Equal(record.UpdatedAt) || len(record.Output) != 0 || record.OutputHash != "" ||
			record.Generation != nil || record.Semantic != nil || (domain.SynthesisFailure{Code: record.ErrorCode}).Validate() != nil ||
			record.Status == SynthesisModelStepRecoveryRequired && record.Retryable {
			return invalidSynthesisModelStep()
		}
	default:
		return invalidSynthesisModelStep()
	}
	return nil
}

func synthesisModelTime(value time.Time) bool {
	return value.Equal(value.UTC().Truncate(time.Microsecond))
}

func invalidSynthesisModelStep() error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisModelStepInvalid, false, errors.New("synthesis model step binding is invalid"))
}
