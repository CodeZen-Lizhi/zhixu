package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const stateSelect = `SELECT
	a.id::text,a.workspace_id::text,a.artifact_type,a.title,a.status,a.scope_definition,a.source_coverage,a.current_revision_id::text,a.version,a.created_at,a.updated_at,
	r.id::text,r.artifact_id::text,r.revision_no,r.outline,r.sections,r.created_by_type,r.generation_metadata,r.content_hash,r.created_at,r.coverage,r.missing,r.conflicts,r.content_markdown,r.provenance
	FROM learning.artifact a
	JOIN learning.artifact_revision r ON r.id=a.current_revision_id AND r.workspace_id=a.workspace_id AND r.artifact_id=a.id`

const revisionSelect = `SELECT
	r.id::text,r.artifact_id::text,r.revision_no,r.outline,r.sections,r.created_by_type,r.generation_metadata,r.content_hash,r.created_at,r.coverage,r.missing,r.conflicts,r.content_markdown,r.provenance
	FROM learning.artifact_revision r`

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type stateScanner interface{ Scan(...any) error }

func loadState(ctx context.Context, db queryer, workspaceID, artifactID foundation.ID, lock bool) (artifactapp.State, error) {
	return loadStateWithVisibility(ctx, db, workspaceID, artifactID, lock, false)
}

func loadVisibleState(ctx context.Context, db queryer, workspaceID, artifactID foundation.ID) (artifactapp.State, error) {
	return loadStateWithVisibility(ctx, db, workspaceID, artifactID, false, true)
}

func loadStateWithVisibility(ctx context.Context, db queryer, workspaceID, artifactID foundation.ID, lock, hideHeld bool) (artifactapp.State, error) {
	sql := stateSelect + ` WHERE a.workspace_id=$1 AND a.id=$2 AND a.domain_schema_version='artifact/v1' AND r.domain_schema_version='artifact-revision/v1'`
	if hideHeld {
		sql += ` AND NOT EXISTS (
			SELECT 1 FROM learning.artifact_visibility_hold h
			WHERE h.workspace_id=a.workspace_id AND h.artifact_id=a.id
		)`
	}
	if lock {
		sql += ` FOR UPDATE OF a`
	}
	state, err := scanState(db.QueryRow(ctx, sql, string(workspaceID), string(artifactID)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return artifactapp.State{}, err
		}
		return artifactapp.State{}, classify(err)
	}
	return state, nil
}

func loadRevision(ctx context.Context, db queryer, workspaceID, artifactID, revisionID foundation.ID) (domain.Revision, error) {
	revision, err := scanRevision(db.QueryRow(ctx, revisionSelect+`
		WHERE r.workspace_id=$1 AND r.artifact_id=$2 AND r.id=$3 AND r.domain_schema_version='artifact-revision/v1'`,
		string(workspaceID), string(artifactID), string(revisionID)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Revision{}, err
		}
		return domain.Revision{}, classify(err)
	}
	return revision, nil
}

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

func insertRevision(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, revision domain.Revision) error {
	coverage := coverageFromSections(revision.Sections)
	metadata := any(nil)
	if revision.Metadata != nil {
		metadata = marshalJSON(revision.Metadata)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO learning.artifact_revision(
			id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,
			domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
		) VALUES($1,$2,$3,$4,'SNAPSHOT',$5,$6,$7,'[]'::jsonb,'[]'::jsonb,$8,$9,$10,$11,$12,$13,$14)`,
		string(revision.ID), string(revision.ArtifactID), string(workspaceID), revision.RevisionNo, marshalJSON(revision.Outline), marshalJSON(revision.Sections), marshalJSON(coverage), markdownFromSections(revision.Sections), marshalJSON(map[string]string{"schema_version": artifactRevisionSchemaVersion}), artifactRevisionSchemaVersion, revision.ContentHash, string(revision.CreatedBy), metadata, revision.CreatedAt.UTC())
	if err != nil {
		return classify(err)
	}
	return nil
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

type receiptDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadReceipt(ctx context.Context, db receiptDB, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	var raw []byte
	err := db.QueryRow(ctx, `SELECT response FROM learning.artifact_command WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return commandReceipt{}, false, nil
	}
	if err != nil {
		return commandReceipt{}, false, classify(err)
	}
	var receipt commandReceipt
	if err := decodeJSON(raw, &receipt); err != nil {
		return commandReceipt{}, false, inconsistent(fmt.Errorf("decode artifact command receipt: %w", err))
	}
	if err := receipt.validate(); err != nil {
		return commandReceipt{}, false, err
	}
	return receipt, true, nil
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

func insertReceipt(ctx context.Context, tx pgx.Tx, binding artifactapp.CommandBinding, result artifactapp.CommandResult) (artifactapp.CommandResult, error) {
	receipt := commandReceipt{SchemaVersion: artifactReceiptSchemaVersion, WorkspaceID: binding.WorkspaceID, ArtifactID: result.State.Artifact.ID, IdempotencyKey: binding.IdempotencyKey, RequestHash: binding.RequestHash, CommandType: binding.CommandType, ExpectedVersion: binding.ExpectedVersion, Result: result}
	if err := receipt.validate(); err != nil {
		return artifactapp.CommandResult{}, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO learning.artifact_command(workspace_id,idempotency_key,request_hash,command_type,artifact_id,artifact_version,response,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, string(binding.WorkspaceID), binding.IdempotencyKey, binding.RequestHash, string(binding.CommandType), string(result.State.Artifact.ID), result.CommandVersion, marshalJSON(receipt), result.State.Artifact.UpdatedAt.UTC())
	if err != nil {
		return artifactapp.CommandResult{}, classify(err)
	}
	return result, nil
}

func insertExport(ctx context.Context, tx pgx.Tx, record artifactapp.ExportRecord) error {
	_, err := tx.Exec(ctx, `INSERT INTO learning.artifact_export(id,workspace_id,artifact_id,revision_id,artifact_version,revision_no,revision_hash,output_path,output_hash,output_size,exported_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, string(record.ID), string(record.WorkspaceID), string(record.ArtifactID), string(record.RevisionID), record.ArtifactVersion, record.RevisionNo, record.RevisionHash, record.OutputPath, record.OutputHash, record.OutputSize, record.ExportedAt.UTC())
	if err != nil {
		return classify(err)
	}
	return nil
}

func insertPublication(ctx context.Context, tx pgx.Tx, record artifactapp.PublicationRecord) error {
	_, err := tx.Exec(ctx, `INSERT INTO learning.artifact_publication(artifact_id,workspace_id,revision_id,artifact_version,revision_no,content_hash,proposal_id,idempotency_key,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(record.ArtifactID), string(record.WorkspaceID), string(record.RevisionID), record.ArtifactVersion, record.RevisionNo, record.ContentHash, string(record.ProposalID), record.IdempotencyKey, record.CreatedAt.UTC())
	if err != nil {
		return classify(err)
	}
	return nil
}
