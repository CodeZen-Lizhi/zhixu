package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const revisionSelect = `SELECT
	r.id::text,r.artifact_id::text,r.revision_no,r.outline,r.sections,r.created_by_type,r.generation_metadata,r.content_hash,r.created_at,r.coverage,r.missing,r.conflicts,r.content_markdown,r.provenance
	FROM learning.artifact_revision r`

type stateScanner interface{ Scan(...any) error }

type persistedRevision struct {
	revision                              domain.Revision
	creator, contentMarkdown              string
	outline, sections, metadata, coverage []byte
	missing, conflicts, provenance        []byte
}

func (persisted *persistedRevision) scanTargets() []any {
	return []any{
		&persisted.revision.ID, &persisted.revision.ArtifactID, &persisted.revision.RevisionNo,
		&persisted.outline, &persisted.sections, &persisted.creator, &persisted.metadata,
		&persisted.revision.ContentHash, &persisted.revision.CreatedAt, &persisted.coverage,
		&persisted.missing, &persisted.conflicts, &persisted.contentMarkdown, &persisted.provenance,
	}
}

func scanState(scanner stateScanner) (artifactapp.State, error) {
	var (
		artifact       domain.Artifact
		status         string
		sourceCoverage []byte
		persisted      persistedRevision
	)
	targets := []any{
		&artifact.ID, &artifact.WorkspaceID, &artifact.Type, &artifact.Title, &status, &artifact.ScopeDefinition, &sourceCoverage, &artifact.CurrentRevisionID, &artifact.Version, &artifact.CreatedAt, &artifact.UpdatedAt,
	}
	targets = append(targets, persisted.scanTargets()...)
	if err := scanner.Scan(targets...); err != nil {
		return artifactapp.State{}, err
	}
	// pgx decodes timestamptz using the connection's location. Domain snapshots
	// are canonical UTC values, and Transition compares an unchanged Revision
	// structurally when no new revision is created.
	artifact.CreatedAt = artifact.CreatedAt.UTC()
	artifact.UpdatedAt = artifact.UpdatedAt.UTC()
	artifact.Status = domain.Status(status)
	if err := decodeJSON(sourceCoverage, &artifact.SourceCoverage); err != nil {
		return artifactapp.State{}, inconsistent(fmt.Errorf("decode artifact source coverage: %w", err))
	}
	revision, persistedCoverage, err := decodePersistedRevision(persisted)
	if err != nil {
		return artifactapp.State{}, err
	}
	expectedCoverage := coverageFromSections(revision.Sections)
	if !reflect.DeepEqual(persistedCoverage, expectedCoverage) || !reflect.DeepEqual(artifact.SourceCoverage, expectedCoverage) {
		return artifactapp.State{}, inconsistent(errors.New("artifact coverage does not match current revision"))
	}
	state := artifactapp.State{Artifact: artifact, Revision: revision}
	if err := artifactapp.ValidateState(state); err != nil {
		return artifactapp.State{}, inconsistent(fmt.Errorf("validate persisted artifact state: %w", err))
	}
	return state, nil
}

func scanRevision(scanner stateScanner) (domain.Revision, error) {
	var persisted persistedRevision
	if err := scanner.Scan(persisted.scanTargets()...); err != nil {
		return domain.Revision{}, err
	}
	revision, _, err := decodePersistedRevision(persisted)
	return revision, err
}

func decodePersistedRevision(persisted persistedRevision) (domain.Revision, []domain.Coverage, error) {
	revision := persisted.revision
	revision.CreatedAt = revision.CreatedAt.UTC()
	revision.CreatedBy = domain.CreatorType(persisted.creator)
	if err := decodeJSON(persisted.outline, &revision.Outline); err != nil {
		return domain.Revision{}, nil, inconsistent(fmt.Errorf("decode artifact outline: %w", err))
	}
	if err := decodeJSON(persisted.sections, &revision.Sections); err != nil {
		return domain.Revision{}, nil, inconsistent(fmt.Errorf("decode artifact sections: %w", err))
	}
	// The v1 JSON contract omits empty DocumentSources.  Domain validation
	// treats nil and an explicit empty collection identically, but persisted
	// state comparisons must be deterministic across a write/read round trip.
	for index := range revision.Sections {
		if revision.Sections[index].DocumentSources == nil {
			revision.Sections[index].DocumentSources = []domain.DocumentSource{}
		}
	}
	if len(persisted.metadata) > 0 && string(persisted.metadata) != "null" {
		var value domain.GenerationMetadata
		if err := decodeJSON(persisted.metadata, &value); err != nil {
			return domain.Revision{}, nil, inconsistent(fmt.Errorf("decode artifact generation metadata: %w", err))
		}
		revision.Metadata = &value
	}
	var coverage []domain.Coverage
	if err := decodeJSON(persisted.coverage, &coverage); err != nil {
		return domain.Revision{}, nil, inconsistent(fmt.Errorf("decode artifact revision coverage: %w", err))
	}
	if err := validateDiscardedJSON(persisted.missing, persisted.conflicts, persisted.provenance); err != nil {
		return domain.Revision{}, nil, err
	}
	if persisted.contentMarkdown == "" && len(revision.Sections) > 0 {
		return domain.Revision{}, nil, inconsistent(errors.New("artifact revision content markdown is missing"))
	}
	if !reflect.DeepEqual(coverage, coverageFromSections(revision.Sections)) {
		return domain.Revision{}, nil, inconsistent(errors.New("artifact revision coverage does not match sections"))
	}
	if err := domain.ValidateRevision(revision); err != nil {
		return domain.Revision{}, nil, inconsistent(fmt.Errorf("validate persisted artifact revision: %w", err))
	}
	return revision, coverage, nil
}

func coverageFromSections(sections []domain.Section) []domain.Coverage {
	coverage := make([]domain.Coverage, len(sections))
	for index, section := range sections {
		coverage[index] = section.Coverage
		if section.Coverage.Gaps != nil {
			coverage[index].Gaps = make([]domain.Gap, len(section.Coverage.Gaps))
			copy(coverage[index].Gaps, section.Coverage.Gaps)
		}
	}
	return coverage
}

func validateDiscardedJSON(missing, conflicts, provenance []byte) error {
	var values []any
	if err := decodeJSON(missing, &values); err != nil {
		return inconsistent(fmt.Errorf("decode artifact missing facts: %w", err))
	}
	if err := decodeJSON(conflicts, &values); err != nil {
		return inconsistent(fmt.Errorf("decode artifact conflicts: %w", err))
	}
	var object map[string]any
	if err := decodeJSON(provenance, &object); err != nil {
		return inconsistent(fmt.Errorf("decode artifact provenance: %w", err))
	}
	return nil
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func marshalJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("artifact persistence attempted to encode validated state: %v", err))
	}
	return encoded
}

type revisionCitationSelector struct {
	sourceVersionID foundation.ID
	sourceSpanID    foundation.ID
}

func revisionCitationSelectors(revision domain.Revision) []revisionCitationSelector {
	selectorsByKey := make(map[string]revisionCitationSelector)
	for _, section := range revision.Sections {
		for _, citation := range section.Citations {
			key := string(citation.SourceVersionID) + "\x00" + string(citation.SourceSpanID)
			selectorsByKey[key] = revisionCitationSelector{
				sourceVersionID: citation.SourceVersionID,
				sourceSpanID:    citation.SourceSpanID,
			}
		}
	}
	if len(selectorsByKey) == 0 {
		return []revisionCitationSelector{}
	}

	keys := make([]string, 0, len(selectorsByKey))
	for key := range selectorsByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selectors := make([]revisionCitationSelector, len(keys))
	for index, key := range keys {
		selectors[index] = selectorsByKey[key]
	}
	return selectors
}

func citationSelectorIdentity(artifactID, revisionID, sourceVersionID, sourceSpanID foundation.ID) string {
	return string(artifactID) + "\x00" + string(revisionID) + "\x00" + string(sourceVersionID) + "\x00" + string(sourceSpanID)
}

func markdownFromSections(sections []domain.Section) string {
	if len(sections) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, section := range sections {
		builder.WriteString("## ")
		builder.WriteString(section.Title)
		builder.WriteString("\n\n")
		builder.WriteString(section.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

type commandReceipt struct {
	SchemaVersion   string                    `json:"schema_version"`
	WorkspaceID     foundation.ID             `json:"workspace_id"`
	ArtifactID      foundation.ID             `json:"artifact_id"`
	IdempotencyKey  string                    `json:"idempotency_key"`
	RequestHash     string                    `json:"request_hash"`
	CommandType     artifactapp.CommandType   `json:"command_type"`
	ExpectedVersion int64                     `json:"expected_version"`
	Result          artifactapp.CommandResult `json:"result"`
}

func (receipt commandReceipt) matches(binding artifactapp.CommandBinding) error {
	artifactMismatch := binding.ArtifactID != "" && receipt.ArtifactID != binding.ArtifactID
	if binding.CommandType == artifactapp.CommandPlan {
		// PLAN generates ArtifactID after the request identity is fixed. A concurrent
		// loser must replay the winner even if it generated a different candidate ID.
		artifactMismatch = false
	}
	if receipt.WorkspaceID != binding.WorkspaceID || receipt.IdempotencyKey != binding.IdempotencyKey || receipt.RequestHash != binding.RequestHash || receipt.CommandType != binding.CommandType || receipt.ExpectedVersion != binding.ExpectedVersion || artifactMismatch {
		return idempotencyConflict(errors.New("artifact idempotency key is bound to another request"))
	}
	return nil
}

func (receipt commandReceipt) validate() error {
	if receipt.SchemaVersion != artifactReceiptSchemaVersion || !validID(receipt.WorkspaceID) || !validID(receipt.ArtifactID) || receipt.IdempotencyKey == "" || !validHash(receipt.RequestHash) || receipt.CommandType == "" || receipt.ExpectedVersion < 0 || receipt.Result.Replayed || receipt.Result.RequestHash != receipt.RequestHash || receipt.Result.CommandType != receipt.CommandType || receipt.Result.CommandVersion != receipt.ExpectedVersion+1 || receipt.Result.State.Artifact.ID != receipt.ArtifactID || receipt.Result.State.Artifact.WorkspaceID != receipt.WorkspaceID {
		return inconsistent(errors.New("artifact command receipt binding is invalid"))
	}
	if err := artifactapp.ValidateState(receipt.Result.State); err != nil {
		return inconsistent(fmt.Errorf("artifact command receipt state is invalid: %w", err))
	}
	if receipt.Result.Export != nil && artifactapp.ValidateExportRecord(*receipt.Result.Export) != nil {
		return inconsistent(errors.New("artifact command receipt export is invalid"))
	}
	if receipt.Result.Publication != nil && artifactapp.ValidatePublicationRecord(*receipt.Result.Publication) != nil {
		return inconsistent(errors.New("artifact command receipt publication is invalid"))
	}
	return nil
}

func commandResult(state artifactapp.State, binding artifactapp.CommandBinding, export *artifactapp.ExportRecord, publication *artifactapp.PublicationRecord) artifactapp.CommandResult {
	return artifactapp.CommandResult{State: state, CommandVersion: state.Artifact.Version, RequestHash: binding.RequestHash, CommandType: binding.CommandType, Export: export, Publication: publication}
}
