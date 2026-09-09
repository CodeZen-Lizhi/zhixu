package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const ErrorCodeSynthesisGenerationInvalid = "SYNTHESIS_GENERATION_INVALID"

// Validate checks exact excerpt bytes, not merely a reference-shaped object.
// Owner readers additionally prove the content artifact and current provenance.
func (excerpt SynthesisSourceExcerpt) Validate(workspaceID foundation.ID) error {
	if !validID(workspaceID) || excerpt.Reference.Validate() != nil || excerpt.Reference.Source.WorkspaceID != workspaceID ||
		len(excerpt.Text) == 0 || len(excerpt.Text) > MaxSynthesisSourceExcerptBytes || !utf8.ValidString(excerpt.Text) || strings.ContainsRune(excerpt.Text, 0) {
		return invalid(domain.ErrorCodeSynthesisSourceInvalid, "synthesis source excerpt is invalid")
	}
	digest := sha256.Sum256([]byte(excerpt.Text))
	if hex.EncodeToString(digest[:]) != excerpt.Reference.ExcerptHash {
		return invalid(domain.ErrorCodeSynthesisSourceInvalid, "synthesis source excerpt hash changed")
	}
	return nil
}

func (view SynthesisSourceView) Validate(workspaceID foundation.ID) error {
	if !validID(workspaceID) || view.Reference.Validate() != nil || view.Reference.Source.WorkspaceID != workspaceID || !view.Availability.Valid() {
		return invalid(domain.ErrorCodeSynthesisSourceInvalid, "synthesis source view is invalid")
	}
	if view.Availability == domain.MaterialAvailable {
		return (SynthesisSourceExcerpt{Reference: view.Reference, Text: view.Text}).Validate(workspaceID)
	}
	if view.Text != "" {
		return invalid(domain.ErrorCodeSynthesisSourceInvalid, "unavailable synthesis source cannot carry replacement text")
	}
	return nil
}

func (input SynthesisGenerationInput) Validate() error {
	if input.SourceEvent.Validate() != nil || !validID(input.ProcessingID) || !validID(input.WorkflowRunID) ||
		!validID(input.NodeRunID) || !validID(input.NodeAttemptID) || !validSynthesisHash(input.RequestHash) ||
		len(input.Notes) > MaxSynthesisCandidateNotes || len(input.Sources) == 0 || len(input.Sources) > domain.MaxSynthesisSources {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis generation input is invalid")
	}
	workspaceID := input.SourceEvent.Source.WorkspaceID
	knownSources := make(map[string]bool)
	notes := make(map[foundation.ID]bool, len(input.Notes))
	for _, candidate := range input.Notes {
		if candidate.Note.Validate() != nil || candidate.Revision.Validate() != nil || candidate.Note.WorkspaceID != workspaceID ||
			candidate.Revision.WorkspaceID != workspaceID || candidate.Revision.NoteID != candidate.Note.ID ||
			candidate.Revision.DocumentID != candidate.Note.DocumentID || candidate.Note.CurrentRevisionID != candidate.Revision.ID ||
			candidate.Note.Title != candidate.Revision.Title || notes[candidate.Note.ID] {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis candidate note binding changed")
		}
		notes[candidate.Note.ID] = true
		for _, item := range candidate.Revision.Items {
			for _, reference := range item.SourceReferences() {
				key, _ := reference.IdentityKey() // The revision has already validated every reference.
				knownSources[key] = true
			}
		}
	}
	totalBytes := 0
	incomingOpened := false
	catalog := make([]domain.SynthesisLabelledSource, 0, len(input.Sources))
	for _, source := range input.Sources {
		if err := source.Validate(workspaceID); err != nil {
			return err
		}
		totalBytes += len(source.Text)
		if totalBytes > MaxSynthesisSourceInputBytes {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis source input exceeds byte limit")
		}
		key, _ := source.Reference.IdentityKey()
		if source.Reference.Source == input.SourceEvent.Source {
			incomingOpened = true
		} else if !knownSources[key] {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis source is outside the frozen input")
		}
		// ResolveSynthesisSourceLabels also checks duplicates and shared tuple
		// consistency. Empty requested labels validate the complete catalog.
		catalog = append(catalog, domain.SynthesisLabelledSource{Label: fmt.Sprintf("S%03d", len(catalog)+1), Reference: source.Reference})
	}
	if !incomingOpened {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis incoming source was not opened")
	}
	_, err := domain.ResolveSynthesisSourceLabels(workspaceID, nil, catalog)
	return err
}

// Validate proves the output is still bound to the frozen request and that all
// deltas can be safely applied. It does not replace semantic support validation.
func (result SynthesisGenerationResult) Validate(input SynthesisGenerationInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if !validID(result.ModelRunID) || result.RequestHash != input.RequestHash || !validSynthesisHash(result.OutputHash) || len(result.Notes) > MaxSynthesisGeneratedNotes {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis generation result binding changed")
	}
	workspaceID := input.SourceEvent.Source.WorkspaceID
	candidates := make(map[foundation.ID]SynthesisGenerationNote, len(input.Notes))
	knownKeys := make(map[string]bool)
	for _, candidate := range input.Notes {
		candidates[candidate.Note.ID] = candidate
		knownKeys[candidate.Note.TopicKey] = true
		for _, alias := range candidate.Note.Aliases {
			knownKeys[alias] = true
		}
	}
	available := make([]domain.SynthesisSourceRef, len(input.Sources))
	for index, source := range input.Sources {
		available[index] = source.Reference
	}
	seen := make(map[foundation.ID]bool)
	for _, generated := range result.Notes {
		if err := domain.ValidateSynthesisTopic(generated.TopicKey, generated.Title, generated.Aliases); err != nil {
			return err
		}
		var current []domain.SynthesisItem
		if generated.NoteID == "" {
			if generated.BaseRevisionID != "" || len(generated.Delta.Operations) == 0 {
				return invalid(ErrorCodeSynthesisGenerationInvalid, "new synthesis topic has an existing base or an empty delta")
			}
			for _, key := range append([]string{generated.TopicKey}, generated.Aliases...) {
				if knownKeys[key] {
					return invalid(ErrorCodeSynthesisGenerationInvalid, "new synthesis topic duplicates an existing topic or alias")
				}
				knownKeys[key] = true
			}
		} else {
			candidate, found := candidates[generated.NoteID]
			if !found || seen[generated.NoteID] || generated.BaseRevisionID != candidate.Revision.ID ||
				generated.TopicKey != candidate.Note.TopicKey || generated.Title != candidate.Note.Title || !slices.Equal(generated.Aliases, candidate.Note.Aliases) {
				return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis output changes a frozen note binding")
			}
			seen[generated.NoteID] = true
			current = candidate.Revision.Items
		}
		if _, err := domain.ApplySynthesisDelta(workspaceID, current, generated.Delta, available); err != nil {
			return err
		}
	}
	return nil
}

func (status SynthesisProcessingStatus) Valid() bool {
	switch status {
	case SynthesisProcessingPending, SynthesisProcessingRunning, SynthesisProcessingSucceeded, SynthesisProcessingNoChange,
		SynthesisProcessingSkipped, SynthesisProcessingFailed, SynthesisProcessingRecoveryRequired:
		return true
	default:
		return false
	}
}

func validSynthesisHash(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
