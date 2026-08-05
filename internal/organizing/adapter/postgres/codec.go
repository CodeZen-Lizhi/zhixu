package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/jackc/pgx/v5"
)

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type scanner interface{ Scan(...any) error }

const draftSelect = `
	d.id::text,d.workspace_id::text,d.intent,d.status,
	COALESCE(d.template_revision_id::text,''),COALESCE(d.confirmed_snapshot_id::text,''),
	d.version,d.created_at,d.updated_at`

const draftMaterialSelect = `
	m.id::text,m.workspace_id::text,m.draft_id::text,m.draft_version,m.position,m.kind,
	COALESCE(m.source_version_id::text,''),COALESCE(m.document_id::text,''),
	COALESCE(m.article_revision_id::text,''),COALESCE(m.claim_id::text,''),
	COALESCE(m.collection_id::text,''),COALESCE(m.origin_collection_id::text,''),
	COALESCE(m.profile_revision_id::text,''),m.material_version,
	COALESCE(m.content_hash,''),COALESCE(m.query_hash,''),COALESCE(m.read_model_revision,''),
	m.evidence,m.title,m.reasons,m.origin,m.availability,m.score,m.selected,m.created_at`

const snapshotSelect = `
	s.id::text,s.workspace_id::text,s.draft_id::text,s.draft_version,
	s.template_id::text,s.template_revision_id::text,s.template_hash,s.intent,s.snapshot_hash,s.created_at`

const snapshotMaterialSelect = `
	m.id::text,m.workspace_id::text,m.snapshot_id::text,m.position,m.kind,
	COALESCE(m.source_version_id::text,''),COALESCE(m.document_id::text,''),
	COALESCE(m.article_revision_id::text,''),COALESCE(m.claim_id::text,''),
	COALESCE(m.collection_id::text,''),COALESCE(m.origin_collection_id::text,''),
	COALESCE(m.profile_revision_id::text,''),m.material_version,
	COALESCE(m.content_hash,''),COALESCE(m.query_hash,''),COALESCE(m.read_model_revision,''),m.evidence`

const templateDetailSelect = `
	t.id::text,COALESCE(t.workspace_id::text,''),t.owner,t.kind,t.current_revision_id::text,t.version,
	t.created_at,t.updated_at,r.id::text,r.template_id::text,COALESCE(r.workspace_id::text,''),r.owner,
	r.revision_no,r.kind,r.schema_version,r.declaration,r.declaration_hash,r.created_at`

const historicalTemplateDetailSelect = `
	t.id::text,COALESCE(t.workspace_id::text,''),t.owner,t.kind,r.id::text,r.revision_no,
	t.created_at,r.created_at,r.id::text,r.template_id::text,COALESCE(r.workspace_id::text,''),r.owner,
	r.revision_no,r.kind,r.schema_version,r.declaration,r.declaration_hash,r.created_at`

type draftMaterialRow struct {
	id, workspaceID, draftID string
	draftVersion             int64
	position                 int
	kind                     string
	sourceVersionID          string
	documentID               string
	articleRevisionID        string
	claimID                  string
	collectionID             string
	originCollectionID       string
	profileRevisionID        string
	materialVersion          int64
	contentHash              string
	queryHash                string
	readModelRevision        string
	evidence                 []byte
	title                    string
	reasons                  []string
	origin                   string
	availability             string
	score                    float64
	selected                 bool
	createdAt                time.Time
}

func scanDraft(scanner scanner) (domain.Draft, error) {
	var (
		draft               domain.Draft
		status              string
		templateRevisionID  string
		confirmedSnapshotID string
	)
	if err := scanner.Scan(&draft.ID, &draft.WorkspaceID, &draft.Intent, &status, &templateRevisionID, &confirmedSnapshotID,
		&draft.Version, &draft.CreatedAt, &draft.UpdatedAt); err != nil {
		return domain.Draft{}, err
	}
	draft.Status = domain.DraftStatus(status)
	draft.TemplateRevisionID = foundation.ID(templateRevisionID)
	draft.ConfirmedSnapshotID = foundation.ID(confirmedSnapshotID)
	draft.CreatedAt = draft.CreatedAt.UTC()
	draft.UpdatedAt = draft.UpdatedAt.UTC()
	draft.Materials = []domain.DraftMaterial{}
	return draft, nil
}

func scanDraftMaterial(scanner scanner) (domain.DraftMaterial, error) {
	var row draftMaterialRow
	if err := scanner.Scan(&row.id, &row.workspaceID, &row.draftID, &row.draftVersion, &row.position, &row.kind,
		&row.sourceVersionID, &row.documentID, &row.articleRevisionID, &row.claimID, &row.collectionID,
		&row.originCollectionID, &row.profileRevisionID, &row.materialVersion, &row.contentHash, &row.queryHash,
		&row.readModelRevision, &row.evidence, &row.title, &row.reasons, &row.origin, &row.availability,
		&row.score, &row.selected, &row.createdAt); err != nil {
		return domain.DraftMaterial{}, err
	}
	reference, err := decodeMaterialRef(row.kind, row.sourceVersionID, row.documentID, row.articleRevisionID,
		row.claimID, row.collectionID, row.originCollectionID, row.profileRevisionID, row.materialVersion,
		row.contentHash, row.queryHash, row.readModelRevision, row.evidence)
	if err != nil {
		return domain.DraftMaterial{}, err
	}
	reasons := make([]domain.SuggestionReasonCode, len(row.reasons))
	for index, reason := range row.reasons {
		reasons[index] = domain.SuggestionReasonCode(reason)
	}
	material := domain.DraftMaterial{
		ID: foundation.ID(row.id), DraftID: foundation.ID(row.draftID), Ref: reference,
		Title: row.title, Reasons: reasons, Origin: domain.MaterialOrigin(row.origin),
		Availability: domain.MaterialAvailability(row.availability), Score: row.score,
		Selected: row.selected, Position: row.position, CreatedAt: row.createdAt.UTC(),
	}
	materialRefWorkspace := foundation.ID(row.workspaceID)
	if err := material.Validate(); err != nil || !validID(materialRefWorkspace) {
		return domain.DraftMaterial{}, inconsistent(fmt.Errorf("stored draft material is invalid: %w", err))
	}
	return material, nil
}

func scanSnapshot(scanner scanner) (domain.Snapshot, error) {
	var snapshot domain.Snapshot
	if err := scanner.Scan(&snapshot.ID, &snapshot.WorkspaceID, &snapshot.DraftID, &snapshot.DraftVersion,
		&snapshot.TemplateID, &snapshot.TemplateRevisionID, &snapshot.TemplateHash, &snapshot.Intent,
		&snapshot.Hash, &snapshot.CreatedAt); err != nil {
		return domain.Snapshot{}, err
	}
	snapshot.CreatedAt = snapshot.CreatedAt.UTC()
	snapshot.Materials = []domain.MaterialRef{}
	return snapshot, nil
}

type snapshotMaterialRow struct {
	id, workspaceID, snapshotID string
	position                    int
	kind                        string
	sourceVersionID             string
	documentID                  string
	articleRevisionID           string
	claimID                     string
	collectionID                string
	originCollectionID          string
	profileRevisionID           string
	materialVersion             int64
	contentHash                 string
	queryHash                   string
	readModelRevision           string
	evidence                    []byte
}

func scanSnapshotMaterial(scanner scanner) (domain.MaterialRef, int, string, string, error) {
	var row snapshotMaterialRow
	if err := scanner.Scan(&row.id, &row.workspaceID, &row.snapshotID, &row.position, &row.kind,
		&row.sourceVersionID, &row.documentID, &row.articleRevisionID, &row.claimID, &row.collectionID,
		&row.originCollectionID, &row.profileRevisionID, &row.materialVersion, &row.contentHash, &row.queryHash,
		&row.readModelRevision, &row.evidence); err != nil {
		return domain.MaterialRef{}, 0, "", "", err
	}
	reference, err := decodeMaterialRef(row.kind, row.sourceVersionID, row.documentID, row.articleRevisionID,
		row.claimID, row.collectionID, row.originCollectionID, row.profileRevisionID, row.materialVersion,
		row.contentHash, row.queryHash, row.readModelRevision, row.evidence)
	if err != nil {
		return domain.MaterialRef{}, 0, "", "", err
	}
	if err := reference.Validate(); err != nil {
		return domain.MaterialRef{}, 0, "", "", inconsistent(fmt.Errorf("stored snapshot material is invalid: %w", err))
	}
	return reference, row.position, row.workspaceID, row.snapshotID, nil
}

func decodeMaterialRef(kind, sourceVersionID, documentID, articleRevisionID, claimID, collectionID,
	originCollectionID, profileRevisionID string, version int64, contentHash, queryHash, readModelRevision string,
	evidenceJSON []byte) (domain.MaterialRef, error) {
	evidence, err := decodeEvidence(evidenceJSON)
	if err != nil {
		return domain.MaterialRef{}, inconsistent(fmt.Errorf("decode material evidence: %w", err))
	}
	ref := domain.MaterialRef{
		Kind: domain.MaterialKind(kind), SourceVersionID: foundation.ID(sourceVersionID),
		DocumentID: foundation.ID(documentID), ArticleRevisionID: foundation.ID(articleRevisionID),
		ClaimID: foundation.ID(claimID), CollectionID: foundation.ID(collectionID),
		OriginCollectionID: foundation.ID(originCollectionID), ProfileRevisionID: foundation.ID(profileRevisionID),
		Version: version, ContentHash: contentHash, QueryHash: queryHash,
		ReadModelRevision: readModelRevision, Evidence: evidence,
	}
	return ref, nil
}

func decodeEvidence(encoded []byte) ([]domain.EvidenceRef, error) {
	if len(encoded) == 0 || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return nil, errors.New("material evidence must be a JSON array")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var evidence []domain.EvidenceRef
	if err := decoder.Decode(&evidence); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("material evidence contains trailing JSON")
	}
	if len(evidence) == 0 {
		return []domain.EvidenceRef{}, nil
	}
	return evidence, nil
}

func encodeEvidence(evidence []domain.EvidenceRef) ([]byte, error) {
	if len(evidence) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(evidence)
}

func encodeDeclaration(declaration domain.TemplateDeclaration) ([]byte, error) {
	return json.Marshal(declaration)
}

func decodeDeclaration(encoded []byte) (domain.TemplateDeclaration, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var declaration domain.TemplateDeclaration
	if err := decoder.Decode(&declaration); err != nil {
		return domain.TemplateDeclaration{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.TemplateDeclaration{}, errors.New("template declaration contains trailing JSON")
	}
	return declaration, nil
}

func scanTemplateDetail(scanner scanner) (organizingapp.TemplateDetail, error) {
	var (
		detail                                       organizingapp.TemplateDetail
		templateWorkspace, revisionWorkspace         string
		templateOwner, templateKind, revisionOwner   string
		revisionKind, schemaVersion, declarationHash string
		declaration                                  []byte
	)
	if err := scanner.Scan(&detail.Template.ID, &templateWorkspace, &templateOwner, &templateKind,
		&detail.Template.CurrentRevisionID, &detail.Template.Version, &detail.Template.CreatedAt,
		&detail.Template.UpdatedAt, &detail.Revision.ID, &detail.Revision.TemplateID,
		&revisionWorkspace, &revisionOwner, &detail.Revision.RevisionNo, &revisionKind,
		&schemaVersion, &declaration, &declarationHash, &detail.Revision.CreatedAt); err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	detail.Template.WorkspaceID = foundation.ID(templateWorkspace)
	detail.Template.Owner = domain.TemplateOwner(templateOwner)
	detail.Template.Kind = domain.TemplateKind(templateKind)
	detail.Revision.WorkspaceID = foundation.ID(revisionWorkspace)
	decodedDeclaration, err := decodeDeclaration(declaration)
	if err != nil {
		return organizingapp.TemplateDetail{}, inconsistent(fmt.Errorf("decode template declaration: %w", err))
	}
	detail.Revision.Declaration = decodedDeclaration
	if revisionOwner != templateOwner || revisionKind != string(detail.Revision.Declaration.Kind) ||
		schemaVersion != detail.Revision.Declaration.SchemaVersion {
		return organizingapp.TemplateDetail{}, inconsistent(errors.New("stored template revision discriminators are inconsistent"))
	}
	detail.Revision.DeclarationHash = declarationHash
	detail.Template.CreatedAt = detail.Template.CreatedAt.UTC()
	detail.Template.UpdatedAt = detail.Template.UpdatedAt.UTC()
	detail.Revision.CreatedAt = detail.Revision.CreatedAt.UTC()
	if err := detail.Template.Validate(); err != nil {
		return organizingapp.TemplateDetail{}, inconsistent(fmt.Errorf("stored template is invalid: %w", err))
	}
	if err := detail.Revision.Validate(detail.Template.Owner); err != nil {
		return organizingapp.TemplateDetail{}, inconsistent(fmt.Errorf("stored template revision is invalid: %w", err))
	}
	return detail, nil
}

// receiptRow is deliberately response-identity-only; bodies and generated
// content remain owned by Draft/Snapshot/Template facts.
type receiptRow struct {
	workspaceID, key, requestHash, commandType, aggregateID string
	expectedVersion                                         int64
	draftID                                                 string
	resultDraftVersion                                      int64
	snapshotID, outboxID                                    string
	templateID                                              string
	resultTemplateVersion                                   int64
	templateRevisionID                                      string
	createdAt                                               time.Time
}

const receiptSelect = `
	r.workspace_id::text,r.idempotency_key,r.request_hash,r.command_type,r.aggregate_id::text,
	r.expected_version,COALESCE(r.draft_id::text,''),COALESCE(r.result_draft_version,0),
	COALESCE(r.snapshot_id::text,''),COALESCE(r.outbox_id::text,''),COALESCE(r.template_id::text,''),
	COALESCE(r.result_template_version,0),COALESCE(r.template_revision_id::text,''),r.created_at`

func scanReceipt(scanner scanner) (receiptRow, error) {
	var row receiptRow
	if err := scanner.Scan(&row.workspaceID, &row.key, &row.requestHash, &row.commandType, &row.aggregateID,
		&row.expectedVersion, &row.draftID, &row.resultDraftVersion, &row.snapshotID, &row.outboxID,
		&row.templateID, &row.resultTemplateVersion, &row.templateRevisionID, &row.createdAt); err != nil {
		return receiptRow{}, err
	}
	row.createdAt = row.createdAt.UTC()
	return row, nil
}

func (row receiptRow) matches(binding organizingapp.CommandBinding) error {
	if row.requestHash != binding.RequestHash || row.commandType != binding.CommandType || row.expectedVersion != binding.ExpectedVersion ||
		(!serverGeneratedAggregateCommand(binding.CommandType) && row.aggregateID != string(binding.AggregateID)) {
		return idempotencyConflict(errors.New("organizing command key is bound to a different request"))
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func canonicalTime(value time.Time) time.Time { return value.UTC().Truncate(time.Microsecond) }
