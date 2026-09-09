package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	ErrorCodeSynthesisInvalid         = "SYNTHESIS_NOTE_INVALID"
	ErrorCodeSynthesisRevisionInvalid = "SYNTHESIS_REVISION_INVALID"
	ErrorCodeSynthesisSourceInvalid   = "SYNTHESIS_SOURCE_INVALID"
	ErrorCodeSynthesisDeltaInvalid    = "SYNTHESIS_DELTA_INVALID"
	ErrorCodeSynthesisVersionConflict = "SYNTHESIS_VERSION_CONFLICT"
)

// CanonicalSynthesisTopicKey normalizes a bounded, textual knowledge-point key.
// The key is not a path, and must never be used as an unchecked writeback target.
func CanonicalSynthesisTopicKey(value string) (string, error) {
	canonical, ok := canonicalText(value, MaxSynthesisTopicKeyBytes, false)
	if !ok || !synthesisSingleLine(canonical) {
		return "", invalid(ErrorCodeSynthesisInvalid, "synthesis topic key is invalid")
	}
	canonical = strings.ToLower(strings.Join(strings.Fields(canonical), " "))
	if len(canonical) > MaxSynthesisTopicKeyBytes {
		return "", invalid(ErrorCodeSynthesisInvalid, "synthesis topic key is too long")
	}
	return canonical, nil
}

func (status SynthesisStatus) Valid() bool {
	switch status {
	case SynthesisQueued, SynthesisGenerating, SynthesisPendingApproval, SynthesisReady,
		SynthesisFailed, SynthesisConflict, SynthesisCapabilityUnavailable, SynthesisRecoveryRequired:
		return true
	default:
		return false
	}
}

func (failure SynthesisFailure) Validate() error {
	if len(failure.Code) == 0 || len(failure.Code) > 128 {
		return invalid(ErrorCodeSynthesisInvalid, "synthesis failure code is invalid")
	}
	for _, character := range failure.Code {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return invalid(ErrorCodeSynthesisInvalid, "synthesis failure code is invalid")
		}
	}
	return nil
}

// ValidateSynthesisTopic checks metadata shared by a persisted note and a new
// server-bound generation result, without inventing temporary resource IDs.
func ValidateSynthesisTopic(topicKey, title string, aliases []string) error {
	key, err := CanonicalSynthesisTopicKey(topicKey)
	if err != nil || key != topicKey || !synthesisText(title, MaxSynthesisTitleBytes, false) || len(aliases) > MaxSynthesisAliases {
		return invalid(ErrorCodeSynthesisInvalid, "synthesis topic metadata is invalid")
	}
	seen := map[string]bool{topicKey: true}
	for _, alias := range aliases {
		canonical, err := CanonicalSynthesisTopicKey(alias)
		if err != nil || canonical != alias || seen[canonical] {
			return invalid(ErrorCodeSynthesisInvalid, "synthesis note aliases are invalid")
		}
		seen[canonical] = true
	}
	return nil
}

func (note SynthesisNote) Validate() error {
	if ValidateSynthesisTopic(note.TopicKey, note.Title, note.Aliases) != nil ||
		!validID(note.ID) || !validID(note.WorkspaceID) || !validID(note.DocumentID) || note.Version < 1 || !note.Status.Valid() ||
		(note.CurrentRevisionID != "" && !validID(note.CurrentRevisionID)) || (note.WorkflowRunID != "" && !validID(note.WorkflowRunID)) ||
		note.CreatedAt.IsZero() || canonicalTime(note.CreatedAt) != note.CreatedAt ||
		canonicalTime(note.UpdatedAt) != note.UpdatedAt || note.UpdatedAt.Before(note.CreatedAt) {
		return invalid(ErrorCodeSynthesisInvalid, "synthesis note binding is invalid")
	}
	failed := note.Status == SynthesisFailed || note.Status == SynthesisConflict ||
		note.Status == SynthesisCapabilityUnavailable || note.Status == SynthesisRecoveryRequired
	if failed != (note.Failure != nil) || (note.Failure != nil && note.Failure.Validate() != nil) ||
		(note.Status == SynthesisRecoveryRequired && note.Failure.Retryable) ||
		(note.Status == SynthesisGenerating && note.WorkflowRunID == "") ||
		((note.Status == SynthesisPendingApproval || note.Status == SynthesisReady) && note.CurrentRevisionID == "") {
		return invalid(ErrorCodeSynthesisInvalid, "synthesis note state is inconsistent")
	}
	return nil
}

func (source SynthesisSourceVersion) Validate() error {
	if !validID(source.WorkspaceID) || !validID(source.SourceID) || !validID(source.SourceVersionID) ||
		!validID(source.ContentArtifactID) || !validID(source.ParseProjectionID) || !isHash(source.ContentHash) {
		return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis original source binding is incomplete")
	}
	return nil
}

func (reference SynthesisSourceRef) Validate() error {
	if reference.Source.Validate() != nil || !validID(reference.SourceSpanID) || !isHash(reference.ExcerptHash) ||
		!synthesisText(reference.Title, MaxSynthesisTitleBytes, false) {
		return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source span binding is invalid")
	}
	return nil
}

// IdentityKey identifies the full immutable tuple, excluding its display title.
func (reference SynthesisSourceRef) IdentityKey() (string, error) {
	if err := reference.Validate(); err != nil {
		return "", err
	}
	return synthesisSourceKey(reference), nil
}

func (event SynthesisSourceReady) Validate() error {
	if !validID(event.ID) || !validID(event.IngestionAttemptID) || event.Source.Validate() != nil ||
		event.ProcessorVersion != SynthesisProcessorVersion || event.CreatedAt.IsZero() || canonicalTime(event.CreatedAt) != event.CreatedAt {
		return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source-ready event is invalid")
	}
	return nil
}

// ProcessingKey deduplicates successful ingestion attempts and transport
// deliveries of the same exact input without discarding parser-version changes.
func (event SynthesisSourceReady) ProcessingKey() (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Source    SynthesisSourceVersion `json:"source"`
		Processor string                 `json:"processor"`
	}{event.Source, event.ProcessorVersion})
	if err != nil {
		return "", invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source-ready binding cannot be encoded")
	}
	return "synthesis:" + synthesisHash(encoded), nil
}

func (item SynthesisItem) Validate(workspaceID foundation.ID) error {
	if !validID(workspaceID) || !validID(item.ID) {
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis item identity is invalid")
	}
	switch item.Kind {
	case SynthesisFactItem:
		if item.Fact == nil || item.Conflict != nil || item.Gap != nil {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis fact union is invalid")
		}
		return validateSynthesisStatement(workspaceID, *item.Fact)
	case SynthesisConflictItem:
		if item.Fact != nil || item.Conflict == nil || item.Gap != nil ||
			!synthesisText(item.Conflict.Subject, MaxSynthesisTextBytes, false) ||
			len(item.Conflict.Alternatives) < 2 || len(item.Conflict.Alternatives) > MaxSynthesisAlternatives {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis conflict union is invalid")
		}
		seen := make(map[string]bool, len(item.Conflict.Alternatives))
		for _, alternative := range item.Conflict.Alternatives {
			if err := validateSynthesisStatement(workspaceID, alternative); err != nil {
				return err
			}
			key := synthesisStatementKey(alternative)
			if seen[key] {
				return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis conflict alternatives are duplicated")
			}
			seen[key] = true
		}
	case SynthesisGapItem:
		if item.Fact != nil || item.Conflict != nil || item.Gap == nil ||
			!synthesisText(item.Gap.Question, MaxSynthesisTextBytes, false) ||
			!synthesisText(item.Gap.Context, MaxSynthesisContextBytes, true) {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis gap union is invalid")
		}
		if err := validateSynthesisSources(workspaceID, item.Gap.Sources, false); err != nil {
			return err
		}
		if item.Gap.Resolution != nil {
			return validateSynthesisStatement(workspaceID, *item.Gap.Resolution)
		}
	default:
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis item kind is unknown")
	}
	return nil
}

// ValidateSynthesisItems validates one ordered projection, including immutable
// owner identities shared between references. Empty input is the initial base.
func ValidateSynthesisItems(workspaceID foundation.ID, items []SynthesisItem) error {
	if !validID(workspaceID) || len(items) > MaxSynthesisItems {
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis item collection is invalid")
	}
	ids := make(map[foundation.ID]bool, len(items))
	semantics := make(map[string]bool, len(items))
	var references []SynthesisSourceRef
	for _, item := range items {
		if err := item.Validate(workspaceID); err != nil {
			return err
		}
		key := synthesisItemKey(item)
		if ids[item.ID] || semantics[key] {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis items are duplicated")
		}
		ids[item.ID], semantics[key] = true, true
		references = append(references, item.SourceReferences()...)
	}
	uniqueSources := make(map[string]bool, len(references))
	for _, reference := range references {
		uniqueSources[synthesisSourceKey(reference)] = true
	}
	if len(uniqueSources) > MaxSynthesisSources {
		return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis projection source count exceeds limit")
	}
	return validateSynthesisSourceBindings(references)
}

func (delta SynthesisDelta) Validate(workspaceID foundation.ID) error {
	if !validID(workspaceID) || len(delta.Operations) > MaxSynthesisOperations {
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis delta exceeds its limits")
	}
	for _, operation := range delta.Operations {
		if err := operation.Validate(workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func (operation SynthesisOperation) Validate(workspaceID foundation.ID) error {
	if !validID(workspaceID) {
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis operation scope is invalid")
	}
	switch operation.Kind {
	case SynthesisAddFact, SynthesisAddConflict, SynthesisAddGap:
		if operation.Item == nil || operation.TargetItemID != "" || operation.AlternativeIndex != nil ||
			len(operation.Sources) != 0 || operation.Resolution != nil {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis add operation shape is invalid")
		}
		expectedKind := map[SynthesisOperationKind]SynthesisItemKind{
			SynthesisAddFact: SynthesisFactItem, SynthesisAddConflict: SynthesisConflictItem, SynthesisAddGap: SynthesisGapItem,
		}[operation.Kind]
		if operation.Item.Kind != expectedKind || (operation.Item.Gap != nil && operation.Item.Gap.Resolution != nil) {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis add operation kind is invalid")
		}
		return operation.Item.Validate(workspaceID)
	case SynthesisAddSupport:
		if operation.Item != nil || !validID(operation.TargetItemID) || operation.Resolution != nil ||
			(operation.AlternativeIndex != nil && (*operation.AlternativeIndex < 0 || *operation.AlternativeIndex >= MaxSynthesisAlternatives)) {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis support operation shape is invalid")
		}
		return validateSynthesisSources(workspaceID, operation.Sources, true)
	case SynthesisResolveGap:
		if operation.Item != nil || !validID(operation.TargetItemID) || operation.AlternativeIndex != nil ||
			len(operation.Sources) != 0 || operation.Resolution == nil {
			return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis resolution operation shape is invalid")
		}
		return validateSynthesisStatement(workspaceID, *operation.Resolution)
	default:
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis operation kind is unknown")
	}
}

func validateSynthesisStatement(workspaceID foundation.ID, statement SynthesisStatement) error {
	if !synthesisText(statement.Text, MaxSynthesisTextBytes, false) || !synthesisText(statement.Applicability, MaxSynthesisContextBytes, true) {
		return invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis statement text is invalid")
	}
	return validateSynthesisSources(workspaceID, statement.Sources, true)
}

func validateSynthesisSources(workspaceID foundation.ID, references []SynthesisSourceRef, required bool) error {
	if len(references) > MaxSynthesisStatementSources || (required && len(references) == 0) {
		return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis statement source count is invalid")
	}
	seen := make(map[string]bool, len(references))
	for _, reference := range references {
		if reference.Validate() != nil || reference.Source.WorkspaceID != workspaceID {
			return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source scope is invalid")
		}
		key := synthesisSourceKey(reference)
		if seen[key] {
			return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis statement source is duplicated")
		}
		seen[key] = true
	}
	return nil
}

func validateSynthesisSourceBindings(references []SynthesisSourceRef) error {
	versions := make(map[foundation.ID]SynthesisSourceVersion)
	artifacts := make(map[foundation.ID]SynthesisSourceVersion)
	projections := make(map[foundation.ID]SynthesisSourceVersion)
	spans := make(map[foundation.ID]SynthesisSourceRef)
	for _, reference := range references {
		if previous, found := versions[reference.Source.SourceVersionID]; found &&
			(previous.WorkspaceID != reference.Source.WorkspaceID || previous.SourceID != reference.Source.SourceID ||
				previous.ContentArtifactID != reference.Source.ContentArtifactID || previous.ContentHash != reference.Source.ContentHash) {
			return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source version binding changed")
		}
		versions[reference.Source.SourceVersionID] = reference.Source
		if previous, found := artifacts[reference.Source.ContentArtifactID]; found &&
			(previous.WorkspaceID != reference.Source.WorkspaceID || previous.ContentHash != reference.Source.ContentHash) {
			return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis original content artifact binding changed")
		}
		artifacts[reference.Source.ContentArtifactID] = reference.Source
		if previous, found := projections[reference.Source.ParseProjectionID]; found &&
			(previous.WorkspaceID != reference.Source.WorkspaceID || previous.ContentArtifactID != reference.Source.ContentArtifactID) {
			return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis parser projection binding changed")
		}
		projections[reference.Source.ParseProjectionID] = reference.Source
		if previous, found := spans[reference.SourceSpanID]; found &&
			(previous.Source.WorkspaceID != reference.Source.WorkspaceID || previous.Source.ContentArtifactID != reference.Source.ContentArtifactID ||
				previous.Source.ParseProjectionID != reference.Source.ParseProjectionID || previous.ExcerptHash != reference.ExcerptHash) {
			return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source span binding changed")
		}
		spans[reference.SourceSpanID] = reference
	}
	return nil
}

// ResolveSynthesisSourceLabels restores authoritative tuples only from the
// exact owner-verified request catalog. It preserves the Provider's source order.
func ResolveSynthesisSourceLabels(workspaceID foundation.ID, labels []string, catalog []SynthesisLabelledSource) ([]SynthesisSourceRef, error) {
	if !validID(workspaceID) || len(catalog) > MaxSynthesisSources || len(labels) > MaxSynthesisStatementSources {
		return nil, invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source label input is invalid")
	}
	byLabel := make(map[string]SynthesisSourceRef, len(catalog))
	seenSources := make(map[string]bool, len(catalog))
	all := make([]SynthesisSourceRef, 0, len(catalog))
	for index, entry := range catalog {
		if entry.Label != fmt.Sprintf("S%03d", index+1) || entry.Reference.Validate() != nil || entry.Reference.Source.WorkspaceID != workspaceID {
			return nil, invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source catalog is invalid")
		}
		key := synthesisSourceKey(entry.Reference)
		if seenSources[key] {
			return nil, invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source catalog contains duplicate references")
		}
		seenSources[key] = true
		byLabel[entry.Label] = entry.Reference
		all = append(all, entry.Reference)
	}
	if err := validateSynthesisSourceBindings(all); err != nil {
		return nil, err
	}
	result := make([]SynthesisSourceRef, 0, len(labels))
	seenLabels := make(map[string]bool, len(labels))
	for _, label := range labels {
		reference, found := byLabel[label]
		if !found || seenLabels[label] {
			return nil, invalid(ErrorCodeSynthesisSourceInvalid, "synthesis source label is unknown or repeated")
		}
		seenLabels[label] = true
		result = append(result, reference)
	}
	return result, nil
}

func synthesisSourceKey(reference SynthesisSourceRef) string {
	return strings.Join([]string{string(reference.Source.WorkspaceID), string(reference.Source.SourceID),
		string(reference.Source.SourceVersionID), string(reference.Source.ContentArtifactID), string(reference.Source.ParseProjectionID),
		string(reference.SourceSpanID), reference.Source.ContentHash, reference.ExcerptHash}, ":")
}

func synthesisStatementKey(statement SynthesisStatement) string {
	return statement.Text + "\x00" + statement.Applicability
}

func synthesisItemKey(item SynthesisItem) string {
	switch item.Kind {
	case SynthesisFactItem:
		return string(item.Kind) + "\x00" + synthesisStatementKey(*item.Fact)
	case SynthesisConflictItem:
		parts := []string{string(item.Kind), item.Conflict.Subject}
		alternatives := make([]string, 0, len(item.Conflict.Alternatives))
		for _, alternative := range item.Conflict.Alternatives {
			alternatives = append(alternatives, synthesisStatementKey(alternative))
		}
		sort.Strings(alternatives)
		parts = append(parts, alternatives...)
		return strings.Join(parts, "\x00")
	case SynthesisGapItem:
		return string(item.Kind) + "\x00" + item.Gap.Question + "\x00" + item.Gap.Context
	default:
		return ""
	}
}

// SourceReferences returns an independent list of an item's original source
// tuples. It is a projection, not a replacement for Validate or owner checks.
func (item SynthesisItem) SourceReferences() []SynthesisSourceRef {
	var references []SynthesisSourceRef
	switch item.Kind {
	case SynthesisFactItem:
		if item.Fact != nil {
			references = append(references, item.Fact.Sources...)
		}
	case SynthesisConflictItem:
		if item.Conflict != nil {
			for _, alternative := range item.Conflict.Alternatives {
				references = append(references, alternative.Sources...)
			}
		}
	case SynthesisGapItem:
		if item.Gap != nil {
			references = append(references, item.Gap.Sources...)
			if item.Gap.Resolution != nil {
				references = append(references, item.Gap.Resolution.Sources...)
			}
		}
	}
	return references
}

func synthesisText(value string, maxBytes int, allowEmpty bool) bool {
	canonical, ok := canonicalText(value, maxBytes, allowEmpty)
	return ok && canonical == value && synthesisSingleLine(value)
}

func synthesisSingleLine(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || character == '\u2028' || character == '\u2029' {
			return false
		}
	}
	return true
}

func synthesisHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
