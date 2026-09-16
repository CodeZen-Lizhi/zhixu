package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
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
	if view.SnapshotText != "" {
		if view.Availability == domain.MaterialAvailable {
			return invalid(domain.ErrorCodeSynthesisSourceInvalid, "current source cannot also carry historical snapshot text")
		}
		if err := (SynthesisSourceExcerpt{Reference: view.Reference, Text: view.SnapshotText}).Validate(workspaceID); err != nil {
			return err
		}
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
	if !ValidSynthesisGenerationPromptContract(input) {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis semantic prompt version is invalid")
	}
	if input.SourceEvent.Validate() != nil || !validID(input.ProcessingID) || !validID(input.WorkflowRunID) ||
		!validID(input.NodeRunID) || !validID(input.NodeAttemptID) || !validSynthesisHash(input.RequestHash) ||
		len(input.Notes) > MaxSynthesisCandidateNotes || len(input.Sources) == 0 || len(input.Sources) > domain.MaxSynthesisSources {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis generation input is invalid")
	}
	workspaceID := input.SourceEvent.Source.WorkspaceID
	if err := validateBodyRefreshInput(input); err != nil {
		return err
	}
	if input.Goal != nil {
		refs := make([]domain.SynthesisSourceRef, len(input.Sources))
		for i, source := range input.Sources {
			refs[i] = source.Reference
		}
		if input.SourceEvent.Fusion != nil || len(input.Notes) != 0 {
			return goalSelectionInvalid()
		}
		if err := input.Goal.Validate(workspaceID, refs); err != nil {
			return err
		}
	}
	knownSources := make(map[string]bool)
	if input.BodyRefresh != nil {
		for _, binding := range input.BodyRefresh.Items {
			_, revision := bodyRefreshRevisions(input, binding)
			for _, item := range revision.Items {
				if item.ID == binding.Updated.ItemID {
					for _, ref := range item.SourceReferences() {
						knownSources[refKey(ref)] = true
					}
				}
			}
		}
	}
	notes := make(map[foundation.ID]bool, len(input.Notes))
	for _, candidate := range input.Notes {
		if candidate.PublicationID != "" && !validID(candidate.PublicationID) {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis publication identity is invalid")
		}
		if candidate.Note.Validate() != nil || candidate.Revision.Validate() != nil || candidate.Note.WorkspaceID != workspaceID ||
			candidate.Revision.WorkspaceID != workspaceID || candidate.Revision.NoteID != candidate.Note.ID ||
			candidate.Revision.DocumentID != candidate.Note.DocumentID || candidate.Note.CurrentRevisionID != candidate.Revision.ID ||
			candidate.Note.Title != candidate.Revision.Title || notes[candidate.Note.ID] {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis candidate note binding changed")
		}
		if err := candidate.Anchor.Validate(workspaceID); err != nil {
			return err
		}
		notes[candidate.Note.ID] = true
		if len(candidate.Supplements) > domain.MaxSynthesisSources {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis supplementary sources exceed input limit")
		}
		supplementIDs := make(map[foundation.ID]bool)
		for _, supplement := range candidate.Supplements {
			if supplement.Validate() != nil || !supplement.MatchesItem(candidate.Revision.Items) || supplementIDs[supplement.ID] || supplement.WorkspaceID != workspaceID || supplement.NoteID != candidate.Note.ID {
				return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis supplement binding is invalid")
			}
			supplementIDs[supplement.ID] = true
			key, _ := supplement.Reference.IdentityKey()
			knownSources[key] = true
		}
		for _, item := range candidate.Revision.Items {
			for _, reference := range item.SourceReferences() {
				key, _ := reference.IdentityKey() // The revision has already validated every reference.
				knownSources[key] = true
			}
		}
	}
	totalBytes := 0
	incomingOpened := false
	frozenSourceKeys := make(map[string]bool, len(input.Sources))
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
		frozenSourceKeys[key] = true
		if source.Reference.Source == input.SourceEvent.Source {
			incomingOpened = true
		} else if input.Goal == nil && !knownSources[key] {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis source is outside the frozen input")
		}
		// ResolveSynthesisSourceLabels also checks duplicates and shared tuple
		// consistency. Empty requested labels validate the complete catalog.
		catalog = append(catalog, domain.SynthesisLabelledSource{Label: fmt.Sprintf("S%03d", len(catalog)+1), Reference: source.Reference})
	}
	if !incomingOpened && input.Goal == nil && input.BodyRefresh == nil {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis incoming source was not opened")
	}
	for _, candidate := range input.Notes {
		if candidate.Anchor == nil {
			continue
		}
		for _, allowed := range candidate.Anchor.AllowedSources {
			key, _ := allowed.IdentityKey()
			if !frozenSourceKeys[key] {
				return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis anchor admission source is outside the frozen input")
			}
		}
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
	if input.Goal != nil && (len(result.Notes) != 1 || result.Notes[0].NoteID != "" || result.Notes[0].BaseRevisionID != "") {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "a goal must produce exactly one new main note")
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
	appliedItems := make(map[foundation.ID][]domain.SynthesisItem, len(input.Notes))
	seen := make(map[foundation.ID]bool)
	for _, generated := range result.Notes {
		if input.BodyRefresh != nil && generated.NoteID != input.BodyRefresh.Request.NoteID {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "refresh output escaped target")
		}
		for _, op := range generated.Delta.Operations {
			if input.BodyRefresh == nil && op.Kind == domain.SynthesisRefreshItem {
				return invalid(ErrorCodeSynthesisGenerationInvalid, "ordinary synthesis cannot refresh body")
			}
			if input.BodyRefresh != nil {
				expected, err := BodyRefreshOperation(input, op.TargetItemID)
				if err != nil || !reflect.DeepEqual(op, expected) {
					return invalid(ErrorCodeSynthesisGenerationInvalid, "refresh output differs from exact published item")
				}
			}
		}
		if trigger := input.SourceEvent.Fusion; trigger != nil && (generated.NoteID != trigger.NoteID || generated.BaseRevisionID == "") {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "fusion output may only update its approved anchor note")
		}
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
		applied, err := domain.ApplySynthesisDelta(workspaceID, current, generated.Delta, available)
		if err != nil {
			return err
		}
		if generated.NoteID != "" {
			appliedItems[generated.NoteID] = applied.Items
		}
		for _, operation := range generated.Delta.Operations {
			if operation.Kind == domain.SynthesisRefreshItem || operation.Item == nil || operation.Item.BodyReference == nil {
				continue
			}
			ref := operation.Item.BodyReference
			upstream, ok := candidates[ref.NoteID]
			if !ok || upstream.PublicationID == "" || ref.NoteID == generated.NoteID ||
				!domain.MatchesSynthesisBodyItem(*operation.Item, upstream.Revision, upstream.PublicationID) {
				return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis included item differs from its frozen published source")
			}
		}
	}
	return validateSynthesisDistinctSourceSupport(input, appliedItems)
}

// ValidateSynthesisAnchorGeneration 在解析模型标签后应用各目标的来源白名单。
// 它与数据库门禁独立；后者会在应用事务内重新检查锚点当前范围和已接受的决策。
func ValidateSynthesisAnchorGeneration(input SynthesisGenerationInput, result SynthesisGenerationResult) error {
	if err := result.Validate(input); err != nil {
		return err
	}
	byNote := make(map[foundation.ID]SynthesisGenerationNote, len(input.Notes))
	for _, candidate := range input.Notes {
		byNote[candidate.Note.ID] = candidate
	}
	for _, generated := range result.Notes {
		if generated.NoteID == "" {
			continue
		}
		candidate := byNote[generated.NoteID]
		if candidate.Anchor == nil {
			continue
		}
		allowed := make(map[string]bool)
		for _, item := range candidate.Revision.Items {
			for _, ref := range item.SourceReferences() {
				key, _ := ref.IdentityKey()
				allowed[key] = true
			}
		}
		for _, supplement := range candidate.Supplements {
			key, _ := supplement.Reference.IdentityKey()
			allowed[key] = true
		}
		for _, ref := range candidate.Anchor.AllowedSources {
			key, _ := ref.IdentityKey()
			allowed[key] = true
		}
		for _, operation := range generated.Delta.Operations {
			for _, ref := range SynthesisOperationSourceReferences(operation) {
				key, err := ref.IdentityKey()
				if err != nil || !allowed[key] {
					return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis anchor generation cites a source outside the accepted scope")
				}
			}
		}
	}
	return nil
}

func SynthesisOperationSourceReferences(operation domain.SynthesisOperation) []domain.SynthesisSourceRef {
	refs := append([]domain.SynthesisSourceRef{}, operation.Sources...)
	if operation.Item != nil {
		refs = append(refs, operation.Item.SourceReferences()...)
	}
	if operation.Resolution != nil {
		refs = append(refs, operation.Resolution.Sources...)
	}
	return refs
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

// synthesisSourceObligation 标识一个可信断言槽位及必须补入的每个不同的已准入来源身份。
// 预检查与结果门禁使用同一计算，均不会截断义务集合。
type synthesisSourceObligation struct {
	noteIndex, itemIndex, slot int
	missing                    map[string]bool
	existingSources            int
}

func synthesisDistinctSourceObligations(input SynthesisGenerationInput) []synthesisSourceObligation {
	var obligations []synthesisSourceObligation
	if input.GenerationPromptVersion == "" || input.BodyRefresh != nil {
		return obligations
	}
	loaded := make(map[string]string, len(input.Sources))
	for _, source := range input.Sources {
		loaded[refKey(source.Reference)] = source.Text
	}
	for noteIndex, candidate := range input.Notes {
		admitted := make(map[string]bool)
		if candidate.Anchor != nil {
			for _, ref := range candidate.Anchor.AllowedSources {
				admitted[refKey(ref)] = true
			}
		}
		incomingSources := make(map[string]string)
		for _, source := range input.Sources {
			key := refKey(source.Reference)
			if source.Reference.Source == input.SourceEvent.Source && (candidate.Anchor == nil || admitted[key]) {
				incomingSources[key] = source.Text
			}
		}
		current := candidate.Revision.Items
		for index, item := range current {
			for slot, refs := range synthesisAssertionSourceSlots(item) {
				represented := make(map[string]bool, len(refs))
				oldTexts := make(map[string]bool, len(refs))
				for _, ref := range refs {
					key := refKey(ref)
					represented[key] = true
					if text, ok := loaded[key]; ok {
						oldTexts[text] = true
					}
				}
				for _, supplement := range candidate.Supplements {
					if supplement.ItemID != item.ID || !((item.Kind == domain.SynthesisFactItem && supplement.Slot == "FACT" && supplement.AlternativeIndex == -1) || (item.Kind == domain.SynthesisConflictItem && supplement.Slot == "CONFLICT" && supplement.AlternativeIndex == slot)) {
						continue
					}
					key := refKey(supplement.Reference)
					represented[key] = true
					if text, ok := loaded[key]; ok {
						oldTexts[text] = true
					}
				}
				missing := make(map[string]bool)
				for key, text := range incomingSources {
					if represented[key] || !oldTexts[text] {
						continue
					}
					missing[key] = true
				}
				if len(missing) > 0 {
					obligations = append(obligations, synthesisSourceObligation{noteIndex: noteIndex, itemIndex: index, slot: slot, missing: missing, existingSources: len(refs)})
				}
			}
		}
	}
	return obligations
}

// ValidateSynthesisSourceSupportCapacity 在产生模型副作用前拒绝确定无法完成的请求。
// 它检查全部必需槽位所需的最低操作与复核容量，包括合并 ADD_CONFLICT 操作的情况。
// 实际模型输出仍须独立通过完整容量检查。
func ValidateSynthesisSourceSupportCapacity(input SynthesisGenerationInput) error {
	notes := make(map[int]map[int]int)
	obligations := synthesisDistinctSourceObligations(input)
	tooLarge := func() error {
		return invalid(ErrorCodeSynthesisModelInputTooLarge, "required source support exceeds fixed synthesis capacity")
	}
	for _, obligation := range obligations {
		if notes[obligation.noteIndex] == nil {
			notes[obligation.noteIndex] = make(map[int]int)
		}
		notes[obligation.noteIndex][obligation.itemIndex]++
		if len(notes) > MaxSynthesisGeneratedNotes || len(notes[obligation.noteIndex]) > domain.MaxSynthesisOperations || obligation.existingSources+len(obligation.missing) > domain.MaxSynthesisStatementSources {
			return tooLarge()
		}
	}
	// 在每份笔记的操作预算内最小化复核项数。冲突可对每个必需槽位使用一次
	// ADD_SUPPORT，也可使用一次合并 ADD_CONFLICT；后者须复核全部观点及其冲突关系。
	totalChecks := 0
	const impossible = MaxSynthesisSemanticChecks + 1
	for noteIndex, items := range notes {
		costs := make([]int, domain.MaxSynthesisOperations+1)
		for i := 1; i < len(costs); i++ {
			costs[i] = impossible
		}
		for itemIndex, slots := range items {
			next := make([]int, len(costs))
			for i := range next {
				next[i] = impossible
			}
			for used, cost := range costs {
				if used+slots < len(next) {
					next[used+slots] = min(next[used+slots], cost+slots)
				}
				item := input.Notes[noteIndex].Revision.Items[itemIndex]
				if item.Kind == domain.SynthesisConflictItem && used+1 < len(next) {
					next[used+1] = min(next[used+1], cost+len(item.Conflict.Alternatives)+1)
				}
			}
			costs = next
		}
		best := impossible
		for _, cost := range costs {
			best = min(best, cost)
		}
		totalChecks += best
		if totalChecks > MaxSynthesisSemanticChecks {
			return tooLarge()
		}
	}

	return nil
}

func validateSynthesisDistinctSourceSupport(input SynthesisGenerationInput, applied map[foundation.ID][]domain.SynthesisItem) error {
	for _, obligation := range synthesisDistinctSourceObligations(input) {
		candidate := input.Notes[obligation.noteIndex]
		updated, found := applied[candidate.Note.ID]
		if !found {
			updated = candidate.Revision.Items
		}
		resulting := make(map[string]bool)
		for _, ref := range synthesisAssertionSourceSlots(updated[obligation.itemIndex])[obligation.slot] {
			resulting[refKey(ref)] = true
		}
		for key := range obligation.missing {
			if !resulting[key] {
				return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis omitted distinct admitted supporting source")
			}
		}
	}
	return nil
}

func synthesisAssertionSourceSlots(item domain.SynthesisItem) [][]domain.SynthesisSourceRef {
	switch item.Kind {
	case domain.SynthesisFactItem:
		return [][]domain.SynthesisSourceRef{item.Fact.Sources}
	case domain.SynthesisConflictItem:
		slots := make([][]domain.SynthesisSourceRef, len(item.Conflict.Alternatives))
		for index, statement := range item.Conflict.Alternatives {
			slots[index] = statement.Sources
		}
		return slots
	default:
		return nil
	}
}
