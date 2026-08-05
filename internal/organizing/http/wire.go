package organizinghttp

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type evidenceResponse struct {
	IndexVersionID  string `json:"index_version_id"`
	ChunkID         string `json:"chunk_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	ContentHash     string `json:"content_hash"`
	ExcerptHash     string `json:"excerpt_hash"`
}

type materialReferenceResponse struct {
	Kind               string  `json:"kind"`
	SourceVersionID    *string `json:"source_version_id,omitempty"`
	DocumentID         *string `json:"document_id,omitempty"`
	ArticleRevisionID  *string `json:"article_revision_id,omitempty"`
	ClaimID            *string `json:"claim_id,omitempty"`
	CollectionID       *string `json:"collection_id,omitempty"`
	OriginCollectionID *string `json:"origin_collection_id,omitempty"`
	ProfileRevisionID  *string `json:"profile_revision_id,omitempty"`
	Version            int64   `json:"version"`
	ContentHash        *string `json:"content_hash,omitempty"`
	QueryHash          *string `json:"query_hash,omitempty"`
	ReadModelRevision  *string `json:"read_model_revision,omitempty"`
}

type materialResponse struct {
	ID           string                    `json:"id"`
	DraftID      string                    `json:"draft_id"`
	WorkspaceID  string                    `json:"workspace_id"`
	Kind         string                    `json:"kind"`
	Title        string                    `json:"title"`
	Reasons      []string                  `json:"reasons"`
	Origin       string                    `json:"origin"`
	Availability string                    `json:"availability"`
	Score        float64                   `json:"score"`
	Selected     bool                      `json:"selected"`
	Position     int                       `json:"position"`
	Reference    materialReferenceResponse `json:"reference"`
	Evidence     []evidenceResponse        `json:"evidence"`
	CreatedAt    string                    `json:"created_at"`
}

type draftResponse struct {
	ID                  string             `json:"id"`
	WorkspaceID         string             `json:"workspace_id"`
	Intent              string             `json:"intent"`
	Status              string             `json:"status"`
	TemplateRevisionID  *string            `json:"template_revision_id"`
	ConfirmedSnapshotID *string            `json:"confirmed_snapshot_id"`
	Version             int64              `json:"version"`
	Materials           []materialResponse `json:"materials"`
	CreatedAt           string             `json:"created_at"`
	UpdatedAt           string             `json:"updated_at"`
}

type draftCommandResponse struct {
	Draft    draftResponse `json:"draft"`
	Replayed bool          `json:"replayed"`
}

type materialSearchReferenceResponse struct {
	Kind              string  `json:"kind"`
	SourceVersionID   *string `json:"source_version_id,omitempty"`
	DocumentID        *string `json:"document_id,omitempty"`
	ArticleRevisionID *string `json:"article_revision_id,omitempty"`
	ClaimID           *string `json:"claim_id,omitempty"`
	CollectionID      *string `json:"collection_id,omitempty"`
}

type materialSearchItemResponse struct {
	WorkspaceID  string                          `json:"workspace_id"`
	Kind         string                          `json:"kind"`
	Title        string                          `json:"title"`
	Availability string                          `json:"availability"`
	Reference    materialSearchReferenceResponse `json:"reference"`
}

type materialSearchPageResponse struct {
	WorkspaceID string                       `json:"workspace_id"`
	Items       []materialSearchItemResponse `json:"items"`
}

type snapshotMaterialResponse struct {
	Position  int                       `json:"position"`
	Reference materialReferenceResponse `json:"reference"`
	Evidence  []evidenceResponse        `json:"evidence"`
}

type snapshotResponse struct {
	ID                 string                     `json:"id"`
	WorkspaceID        string                     `json:"workspace_id"`
	DraftID            string                     `json:"draft_id"`
	DraftVersion       int64                      `json:"draft_version"`
	TemplateID         string                     `json:"template_id"`
	TemplateRevisionID string                     `json:"template_revision_id"`
	TemplateHash       string                     `json:"template_hash"`
	Intent             string                     `json:"intent"`
	Materials          []snapshotMaterialResponse `json:"materials"`
	CanonicalHash      string                     `json:"canonical_hash"`
	CreatedAt          string                     `json:"created_at"`
}

type confirmResponse struct {
	Draft          draftResponse    `json:"draft"`
	Snapshot       snapshotResponse `json:"snapshot"`
	DispatchStatus string           `json:"dispatch_status"`
	Replayed       bool             `json:"replayed"`
}

type materialPolicyResponse struct {
	AllowedKinds []string `json:"allowed_kinds"`
	MinMaterials int      `json:"min_materials"`
	MaxMaterials int      `json:"max_materials"`
}

type templateSectionResponse struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Required bool   `json:"required"`
}

type presentationPolicyResponse struct {
	Audience        string `json:"audience"`
	Language        string `json:"language"`
	Tone            string `json:"tone"`
	Length          string `json:"length"`
	IncludeCode     bool   `json:"include_code"`
	IncludeExamples bool   `json:"include_examples"`
	IncludeFAQ      bool   `json:"include_faq"`
}

type outputDefaultsResponse struct {
	Directory       string `json:"directory"`
	FilenamePattern string `json:"filename_pattern"`
}

type templateDeclarationResponse struct {
	SchemaVersion          string                     `json:"schema_version"`
	Kind                   string                     `json:"kind"`
	Name                   string                     `json:"name"`
	Description            string                     `json:"description"`
	Materials              materialPolicyResponse     `json:"materials"`
	Sections               []templateSectionResponse  `json:"sections"`
	Presentation           presentationPolicyResponse `json:"presentation"`
	Output                 outputDefaultsResponse     `json:"output"`
	AdditionalInstructions string                     `json:"additional_instructions"`
}

type templateRevisionResponse struct {
	ID            string                      `json:"id"`
	TemplateID    string                      `json:"template_id"`
	WorkspaceID   *string                     `json:"workspace_id"`
	RevisionNo    int64                       `json:"revision_no"`
	Kind          string                      `json:"kind"`
	SchemaVersion string                      `json:"schema_version"`
	CanonicalHash string                      `json:"canonical_hash"`
	Declaration   templateDeclarationResponse `json:"declaration"`
	CreatedAt     string                      `json:"created_at"`
}

type templateResponse struct {
	ID                string                   `json:"id"`
	WorkspaceID       *string                  `json:"workspace_id"`
	Key               string                   `json:"key"`
	Name              string                   `json:"name"`
	Description       string                   `json:"description"`
	BuiltIn           bool                     `json:"built_in"`
	Kind              string                   `json:"kind"`
	CurrentRevisionID string                   `json:"current_revision_id"`
	Version           int64                    `json:"version"`
	CurrentRevision   templateRevisionResponse `json:"current_revision"`
	CreatedAt         string                   `json:"created_at"`
	UpdatedAt         string                   `json:"updated_at"`
}

type templatePageResponse struct {
	WorkspaceID string             `json:"workspace_id"`
	Items       []templateResponse `json:"items"`
}

type templateCommandResponse struct {
	Template templateResponse `json:"template"`
	Replayed bool             `json:"replayed"`
}

type runBindingResponse struct {
	ID                string `json:"id"`
	WorkspaceID       string `json:"workspace_id"`
	SnapshotID        string `json:"snapshot_id"`
	WorkflowRunID     string `json:"workflow_run_id"`
	DefinitionKey     string `json:"definition_key"`
	DefinitionVersion int64  `json:"definition_version"`
	CreatedAt         string `json:"created_at"`
}

type runResultResponse struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	RunBindingID  string `json:"run_binding_id"`
	SnapshotID    string `json:"snapshot_id"`
	WorkflowRunID string `json:"workflow_run_id"`
	NodeRunID     string `json:"node_run_id"`
	Kind          string `json:"kind"`
	ResultRef     string `json:"result_ref"`
	ResultHash    string `json:"result_hash"`
	CreatedAt     string `json:"created_at"`
}

type runResponse struct {
	SnapshotID     string              `json:"snapshot_id"`
	DispatchStatus string              `json:"dispatch_status"`
	WorkflowStatus *string             `json:"workflow_status"`
	Retryable      bool                `json:"retryable"`
	AttemptCount   int                 `json:"attempt_count"`
	LastErrorCode  *string             `json:"last_error_code"`
	Binding        *runBindingResponse `json:"binding"`
	Result         *runResultResponse  `json:"result"`
	UpdatedAt      string              `json:"updated_at"`
}

func toDraftCommandResponse(result organizingapp.DraftResult, workspaceID, draftID foundation.ID) (draftCommandResponse, error) {
	draft, err := toDraftResponse(result.Draft, workspaceID, draftID)
	return draftCommandResponse{Draft: draft, Replayed: result.Replayed}, err
}

func toDraftResponse(draft domain.Draft, workspaceID, draftID foundation.ID) (draftResponse, error) {
	if err := draft.Validate(); err != nil || draft.WorkspaceID != workspaceID || (draftID != "" && draft.ID != draftID) {
		return draftResponse{}, resultInvalid("application returned an invalid Draft binding")
	}
	response := draftResponse{ID: string(draft.ID), WorkspaceID: string(draft.WorkspaceID), Intent: draft.Intent,
		Status: string(draft.Status), TemplateRevisionID: optionalID(draft.TemplateRevisionID),
		ConfirmedSnapshotID: optionalID(draft.ConfirmedSnapshotID), Version: draft.Version,
		Materials: make([]materialResponse, 0, len(draft.Materials)), CreatedAt: formatTime(draft.CreatedAt), UpdatedAt: formatTime(draft.UpdatedAt)}
	for _, material := range draft.Materials {
		mapped, err := toMaterialResponse(material, workspaceID, draft.ID)
		if err != nil {
			return draftResponse{}, err
		}
		response.Materials = append(response.Materials, mapped)
	}
	return response, nil
}

func toMaterialSearchPageResponse(
	page organizingapp.MaterialSearchPage,
	workspaceID foundation.ID,
	query string,
	kind domain.MaterialKind,
	limit int,
) (materialSearchPageResponse, error) {
	if page.WorkspaceID != workspaceID || page.Query != query || page.Kind != kind || page.Items == nil || len(page.Items) > limit {
		return materialSearchPageResponse{}, resultInvalid("application returned an invalid material search page")
	}
	response := materialSearchPageResponse{WorkspaceID: string(workspaceID), Items: make([]materialSearchItemResponse, len(page.Items))}
	seen := make(map[string]struct{}, len(page.Items))
	for index, item := range page.Items {
		reference, identity, err := toMaterialSearchReferenceResponse(item.Selector)
		if err != nil || item.Selector.Kind != kind || item.Title == "" || item.Title != strings.TrimSpace(item.Title) ||
			!utf8.ValidString(item.Title) || len([]byte(item.Title)) > 512 || containsControl(item.Title) || !item.Availability.Valid() {
			return materialSearchPageResponse{}, resultInvalid("application returned an invalid material search binding")
		}
		if _, duplicate := seen[identity]; duplicate {
			return materialSearchPageResponse{}, resultInvalid("application returned duplicate material search identities")
		}
		seen[identity] = struct{}{}
		response.Items[index] = materialSearchItemResponse{WorkspaceID: string(workspaceID), Kind: string(kind),
			Title: item.Title, Availability: string(item.Availability), Reference: reference}
	}
	return response, nil
}

func toMaterialSearchReferenceResponse(selector organizingapp.MaterialSelector) (materialSearchReferenceResponse, string, error) {
	response := materialSearchReferenceResponse{Kind: string(selector.Kind), SourceVersionID: optionalID(selector.SourceVersionID),
		DocumentID: optionalID(selector.DocumentID), ArticleRevisionID: optionalID(selector.ArticleRevisionID),
		ClaimID: optionalID(selector.ClaimID), CollectionID: optionalID(selector.CollectionID)}
	switch selector.Kind {
	case domain.MaterialSourceVersion:
		if !validResponseID(selector.SourceVersionID) || selector.DocumentID != "" || selector.ArticleRevisionID != "" || selector.ClaimID != "" || selector.CollectionID != "" {
			return materialSearchReferenceResponse{}, "", resultInvalid("material search Source identity is invalid")
		}
		return response, string(selector.Kind) + ":" + string(selector.SourceVersionID), nil
	case domain.MaterialDocumentRevision:
		if !validResponseID(selector.DocumentID) || !validResponseID(selector.ArticleRevisionID) || selector.SourceVersionID != "" || selector.ClaimID != "" || selector.CollectionID != "" {
			return materialSearchReferenceResponse{}, "", resultInvalid("material search Document identity is invalid")
		}
		return response, string(selector.Kind) + ":" + string(selector.DocumentID) + ":" + string(selector.ArticleRevisionID), nil
	case domain.MaterialClaim:
		if !validResponseID(selector.ClaimID) || selector.SourceVersionID != "" || selector.DocumentID != "" || selector.ArticleRevisionID != "" || selector.CollectionID != "" {
			return materialSearchReferenceResponse{}, "", resultInvalid("material search Claim identity is invalid")
		}
		return response, string(selector.Kind) + ":" + string(selector.ClaimID), nil
	case domain.MaterialSmartCollection:
		if !validResponseID(selector.CollectionID) || selector.SourceVersionID != "" || selector.DocumentID != "" || selector.ArticleRevisionID != "" || selector.ClaimID != "" {
			return materialSearchReferenceResponse{}, "", resultInvalid("material search Collection identity is invalid")
		}
		return response, string(selector.Kind) + ":" + string(selector.CollectionID), nil
	default:
		return materialSearchReferenceResponse{}, "", resultInvalid("material search kind is invalid")
	}
}

func toMaterialResponse(material domain.DraftMaterial, workspaceID, draftID foundation.ID) (materialResponse, error) {
	if err := material.Validate(); err != nil || material.DraftID != draftID {
		return materialResponse{}, resultInvalid("application returned an invalid Draft material binding")
	}
	reference, evidence, err := toMaterialReferenceResponse(material.Ref)
	if err != nil {
		return materialResponse{}, err
	}
	reasons := make([]string, len(material.Reasons))
	for index, reason := range material.Reasons {
		reasons[index] = string(reason)
	}
	return materialResponse{ID: string(material.ID), DraftID: string(material.DraftID), WorkspaceID: string(workspaceID),
		Kind: string(material.Ref.Kind), Title: material.Title, Reasons: reasons, Origin: string(material.Origin),
		Availability: string(material.Availability), Score: material.Score, Selected: material.Selected,
		Position: material.Position, Reference: reference, Evidence: evidence, CreatedAt: formatTime(material.CreatedAt)}, nil
}

func toMaterialReferenceResponse(reference domain.MaterialRef) (materialReferenceResponse, []evidenceResponse, error) {
	canonical, err := domain.CanonicalMaterialRef(reference)
	if err != nil {
		return materialReferenceResponse{}, nil, resultInvalid("application returned an invalid material reference")
	}
	response := materialReferenceResponse{Kind: string(canonical.Kind), Version: canonical.Version,
		SourceVersionID: optionalID(canonical.SourceVersionID), DocumentID: optionalID(canonical.DocumentID),
		ArticleRevisionID: optionalID(canonical.ArticleRevisionID), ClaimID: optionalID(canonical.ClaimID),
		CollectionID: optionalID(canonical.CollectionID), OriginCollectionID: optionalID(canonical.OriginCollectionID),
		ProfileRevisionID: optionalID(canonical.ProfileRevisionID), ContentHash: optionalString(canonical.ContentHash),
		QueryHash: optionalString(canonical.QueryHash), ReadModelRevision: optionalString(canonical.ReadModelRevision)}
	evidence := make([]evidenceResponse, len(canonical.Evidence))
	for index, item := range canonical.Evidence {
		if err := item.Validate(); err != nil {
			return materialReferenceResponse{}, nil, resultInvalid("application returned invalid material Evidence")
		}
		evidence[index] = evidenceResponse{IndexVersionID: string(item.IndexVersionID), ChunkID: string(item.ChunkID),
			SourceVersionID: string(item.SourceVersionID), SourceSpanID: string(item.SourceSpanID),
			ContentHash: item.ContentHash, ExcerptHash: item.ExcerptHash}
	}
	return response, evidence, nil
}

func toConfirmResponse(result organizingapp.ConfirmResult, workspaceID, draftID foundation.ID, expectedVersion int64, templateRevisionID foundation.ID) (confirmResponse, error) {
	if !validResponseID(result.OutboxID) || result.Draft.Version != expectedVersion+1 || result.Draft.Status != domain.DraftConfirmed ||
		result.Snapshot.DraftVersion != expectedVersion || result.Snapshot.TemplateRevisionID != templateRevisionID ||
		result.Draft.TemplateRevisionID != templateRevisionID || result.Draft.ConfirmedSnapshotID != result.Snapshot.ID {
		return confirmResponse{}, resultInvalid("application returned an invalid confirmation binding")
	}
	draft, err := toDraftResponse(result.Draft, workspaceID, draftID)
	if err != nil {
		return confirmResponse{}, err
	}
	snapshot, err := toSnapshotResponse(result.Snapshot, workspaceID, result.Snapshot.ID)
	if err != nil || result.Snapshot.DraftID != draftID {
		return confirmResponse{}, resultInvalid("application returned a cross-bound confirmation Snapshot")
	}
	return confirmResponse{Draft: draft, Snapshot: snapshot, DispatchStatus: string(organizingapp.StartPending), Replayed: result.Replayed}, nil
}

func toSnapshotResponse(snapshot domain.Snapshot, workspaceID, snapshotID foundation.ID) (snapshotResponse, error) {
	if err := snapshot.Validate(); err != nil || snapshot.WorkspaceID != workspaceID || snapshot.ID != snapshotID {
		return snapshotResponse{}, resultInvalid("application returned an invalid Snapshot binding")
	}
	response := snapshotResponse{ID: string(snapshot.ID), WorkspaceID: string(snapshot.WorkspaceID), DraftID: string(snapshot.DraftID),
		DraftVersion: snapshot.DraftVersion, TemplateID: string(snapshot.TemplateID), TemplateRevisionID: string(snapshot.TemplateRevisionID),
		TemplateHash: snapshot.TemplateHash, Intent: snapshot.Intent, Materials: make([]snapshotMaterialResponse, len(snapshot.Materials)),
		CanonicalHash: snapshot.Hash, CreatedAt: formatTime(snapshot.CreatedAt)}
	for index, material := range snapshot.Materials {
		reference, evidence, err := toMaterialReferenceResponse(material)
		if err != nil {
			return snapshotResponse{}, err
		}
		response.Materials[index] = snapshotMaterialResponse{Position: index, Reference: reference, Evidence: evidence}
	}
	return response, nil
}

func toTemplatePageResponse(page organizingapp.TemplatePage, workspaceID foundation.ID, limit int) (templatePageResponse, error) {
	if page.Items == nil || len(page.Items) > limit {
		return templatePageResponse{}, resultInvalid("application returned an invalid Template page")
	}
	response := templatePageResponse{WorkspaceID: string(workspaceID), Items: make([]templateResponse, 0, len(page.Items))}
	seen := make(map[foundation.ID]struct{}, len(page.Items))
	for _, detail := range page.Items {
		if _, duplicate := seen[detail.Template.ID]; duplicate {
			return templatePageResponse{}, resultInvalid("application returned duplicate Template identities")
		}
		seen[detail.Template.ID] = struct{}{}
		item, err := toTemplateResponse(detail, workspaceID, "")
		if err != nil {
			return templatePageResponse{}, err
		}
		response.Items = append(response.Items, item)
	}
	return response, nil
}

func toTemplateCommandResponse(result organizingapp.TemplateResult, workspaceID, templateID foundation.ID) (templateCommandResponse, error) {
	template, err := toTemplateResponse(result.Detail, workspaceID, templateID)
	return templateCommandResponse{Template: template, Replayed: result.Replayed}, err
}

func toTemplateResponse(detail organizingapp.TemplateDetail, workspaceID, templateID foundation.ID) (templateResponse, error) {
	template := detail.Template
	revision := detail.Revision
	if err := template.Validate(); err != nil || template.ID != revision.TemplateID || template.CurrentRevisionID != revision.ID ||
		template.Kind != revision.Declaration.Kind || (templateID != "" && template.ID != templateID) {
		return templateResponse{}, resultInvalid("application returned an invalid Template binding")
	}
	if err := revision.Validate(template.Owner); err != nil {
		return templateResponse{}, resultInvalid("application returned an invalid Template Revision")
	}
	if template.Owner == domain.TemplateCustom && (template.WorkspaceID != workspaceID || revision.WorkspaceID != workspaceID) {
		return templateResponse{}, resultInvalid("application returned a cross-workspace custom Template")
	}
	if template.Owner == domain.TemplateBuiltIn && (template.WorkspaceID != "" || revision.WorkspaceID != "") {
		return templateResponse{}, resultInvalid("application returned an invalid built-in Template owner")
	}
	currentRevision := toTemplateRevisionResponse(revision)
	builtIn := template.Owner == domain.TemplateBuiltIn
	key := "custom-" + string(template.ID)
	if builtIn {
		key = strings.ToLower(strings.ReplaceAll(string(template.Kind), "_", "-"))
	}
	return templateResponse{ID: string(template.ID), WorkspaceID: optionalID(template.WorkspaceID), Key: key,
		Name: revision.Declaration.Name, Description: revision.Declaration.Description, BuiltIn: builtIn, Kind: string(template.Kind),
		CurrentRevisionID: string(template.CurrentRevisionID), Version: template.Version, CurrentRevision: currentRevision,
		CreatedAt: formatTime(template.CreatedAt), UpdatedAt: formatTime(template.UpdatedAt)}, nil
}

func toTemplateRevisionResponse(revision domain.TemplateRevision) templateRevisionResponse {
	declaration := revision.Declaration
	allowedKinds := make([]string, len(declaration.Materials.AllowedKinds))
	for index, kind := range declaration.Materials.AllowedKinds {
		allowedKinds[index] = string(kind)
	}
	sections := make([]templateSectionResponse, len(declaration.Sections))
	for index, section := range declaration.Sections {
		sections[index] = templateSectionResponse{Key: section.Key, Title: section.Title, Required: section.Required}
	}
	declarationResponse := templateDeclarationResponse{SchemaVersion: declaration.SchemaVersion, Kind: string(declaration.Kind),
		Name: declaration.Name, Description: declaration.Description,
		Materials: materialPolicyResponse{AllowedKinds: allowedKinds, MinMaterials: declaration.Materials.MinMaterials, MaxMaterials: declaration.Materials.MaxMaterials},
		Sections:  sections, Presentation: presentationPolicyResponse{Audience: declaration.Presentation.Audience,
			Language: declaration.Presentation.Language, Tone: declaration.Presentation.Tone, Length: string(declaration.Presentation.Length),
			IncludeCode: declaration.Presentation.IncludeCode, IncludeExamples: declaration.Presentation.IncludeExamples,
			IncludeFAQ: declaration.Presentation.IncludeFAQ}, Output: outputDefaultsResponse{Directory: declaration.Output.Directory,
			FilenamePattern: declaration.Output.FilenamePattern}, AdditionalInstructions: declaration.AdditionalInstructions}
	return templateRevisionResponse{ID: string(revision.ID), TemplateID: string(revision.TemplateID), WorkspaceID: optionalID(revision.WorkspaceID),
		RevisionNo: revision.RevisionNo, Kind: string(declaration.Kind), SchemaVersion: declaration.SchemaVersion,
		CanonicalHash: revision.DeclarationHash, Declaration: declarationResponse, CreatedAt: formatTime(revision.CreatedAt)}
}

func validateRunProjection(view organizingapp.RunProjection, workspaceID, snapshotID foundation.ID) error {
	if view.WorkspaceID != workspaceID || view.SnapshotID != snapshotID || !view.Valid() || validateRunErrorCode(view.LastErrorCode) != nil {
		return resultInvalid("run reader returned an invalid Workspace or Snapshot binding")
	}
	switch view.Status {
	case organizingapp.StartPending:
		if view.Binding != nil || view.Result != nil {
			return resultInvalid("pending dispatch contains a Run binding")
		}
	case organizingapp.StartPoisoned:
		if view.Binding != nil || view.Result != nil || view.LastErrorCode == "" {
			return resultInvalid("poisoned dispatch fields are inconsistent")
		}
	case organizingapp.StartStarted:
		if view.Binding == nil {
			return resultInvalid("started dispatch does not contain an exact Run binding")
		}
		if _, err := toRunBindingResponse(*view.Binding, workspaceID, snapshotID); err != nil {
			return err
		}
		if view.Result != nil {
			if _, err := toRunResultResponse(*view.Result, *view.Binding); err != nil {
				return err
			}
		}
	default:
		return resultInvalid("run reader returned an unknown dispatch status")
	}
	return nil
}

func toRunResponse(view organizingapp.RunProjection, workflowRun *workflowdomain.Run, workspaceID, snapshotID foundation.ID) (runResponse, error) {
	if err := validateRunProjection(view, workspaceID, snapshotID); err != nil {
		return runResponse{}, err
	}
	response := runResponse{SnapshotID: string(snapshotID), DispatchStatus: string(view.Status),
		Retryable: view.Status == organizingapp.StartPending, AttemptCount: view.AttemptCount,
		LastErrorCode: optionalString(view.LastErrorCode), UpdatedAt: formatTime(view.UpdatedAt)}
	if view.Status != organizingapp.StartStarted {
		if workflowRun != nil {
			return runResponse{}, resultInvalid("non-started dispatch contains a Workflow projection")
		}
		return response, nil
	}
	if workflowRun == nil || workflowRun.ID != view.Binding.WorkflowRunID || workflowRun.WorkspaceID != workspaceID || !validWorkflowStatus(workflowRun.Status) {
		return runResponse{}, resultInvalid("workflow reader returned an invalid Workspace, Run, or status binding")
	}
	if (workflowRun.Status == workflowdomain.RunStatusSucceeded) != (view.Result != nil) {
		return runResponse{}, resultInvalid("Workflow status and Organizing result projection are inconsistent")
	}
	workflowStatus := string(workflowRun.Status)
	response.WorkflowStatus = &workflowStatus
	binding, err := toRunBindingResponse(*view.Binding, workspaceID, snapshotID)
	if err != nil {
		return runResponse{}, err
	}
	response.Binding = &binding
	if view.Result != nil {
		result, err := toRunResultResponse(*view.Result, *view.Binding)
		if err != nil {
			return runResponse{}, err
		}
		response.Result = &result
	}
	return response, nil
}

func sameRunBinding(left, right domain.RunBinding) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.SnapshotID == right.SnapshotID &&
		left.WorkflowRunID == right.WorkflowRunID && left.DefinitionKey == right.DefinitionKey &&
		left.DefinitionVersion == right.DefinitionVersion && left.CreatedAt.Equal(right.CreatedAt)
}

func validWorkflowStatus(status workflowdomain.RunStatus) bool {
	switch status {
	case workflowdomain.RunStatusPending, workflowdomain.RunStatusRunning, workflowdomain.RunStatusWaitingForHuman,
		workflowdomain.RunStatusRetryWait, workflowdomain.RunStatusPaused, workflowdomain.RunStatusSucceeded,
		workflowdomain.RunStatusFailed, workflowdomain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func toRunBindingResponse(binding domain.RunBinding, workspaceID, snapshotID foundation.ID) (runBindingResponse, error) {
	if err := binding.Validate(); err != nil || binding.WorkspaceID != workspaceID || binding.SnapshotID != snapshotID {
		return runBindingResponse{}, resultInvalid("run reader returned an invalid Workflow binding")
	}
	return runBindingResponse{ID: string(binding.ID), WorkspaceID: string(binding.WorkspaceID), SnapshotID: string(binding.SnapshotID),
		WorkflowRunID: string(binding.WorkflowRunID), DefinitionKey: binding.DefinitionKey,
		DefinitionVersion: binding.DefinitionVersion, CreatedAt: formatTime(binding.CreatedAt)}, nil
}

func toRunResultResponse(result domain.RunResult, binding domain.RunBinding) (runResultResponse, error) {
	if err := result.Validate(); err != nil || result.WorkspaceID != binding.WorkspaceID || result.RunBindingID != binding.ID ||
		result.SnapshotID != binding.SnapshotID || result.WorkflowRunID != binding.WorkflowRunID {
		return runResultResponse{}, resultInvalid("run reader returned an invalid result binding")
	}
	return runResultResponse{ID: string(result.ID), WorkspaceID: string(result.WorkspaceID), RunBindingID: string(result.RunBindingID),
		SnapshotID: string(result.SnapshotID), WorkflowRunID: string(result.WorkflowRunID), NodeRunID: string(result.NodeRunID),
		Kind: string(result.Kind), ResultRef: string(result.ResultRef), ResultHash: result.ResultHash, CreatedAt: formatTime(result.CreatedAt)}, nil
}

func optionalID(value foundation.ID) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func validResponseID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validErrorCode(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for index, character := range value {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' && index > 0 || character == '_' && index > 0 {
			continue
		}
		return false
	}
	return true
}

func validateRunErrorCode(value string) error {
	if value != "" && !validErrorCode(value) {
		return fmt.Errorf("run error code is invalid")
	}
	return nil
}
