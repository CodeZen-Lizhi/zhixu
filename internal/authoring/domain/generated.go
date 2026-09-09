package domain

import (
	"errors"
	"math"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// GeneratedOriginKind identifies an internal owner of generated Article Revisions.
type GeneratedOriginKind string

const (
	// GeneratedOriginSynthesisNote is the governed continuous-note owner.
	GeneratedOriginSynthesisNote GeneratedOriginKind = "SYNTHESIS_NOTE"
	// ErrorCodeGeneratedInvalid rejects malformed internal generation commands.
	ErrorCodeGeneratedInvalid = "AUTHORING_GENERATED_REVISION_INVALID"
	// ErrorCodeGeneratedOriginConflict rejects another owner or a changed baseline.
	ErrorCodeGeneratedOriginConflict = "AUTHORING_GENERATED_ORIGIN_CONFLICT"
	// ErrorCodeGeneratedPublicationBusy preserves a publication already being prepared or applied.
	ErrorCodeGeneratedPublicationBusy = "AUTHORING_GENERATED_PUBLICATION_BUSY"
)

// GeneratedRevisionRequest freezes a server-rendered revision and its semantic projection.
// It deliberately has no actor, Source Version, status, or Git commit input.
type GeneratedRevisionRequest struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	ArticleRevisionID       foundation.ID
	OriginKind              GeneratedOriginKind
	OriginID                foundation.ID
	OriginRevisionID        foundation.ID
	ProjectionHash          string
	ExpectedDocumentVersion int64
	ParentRevisionID        foundation.ID
	RevisionNo              int
	Title                   string
	TargetPath              string
	Content                 string
}

// Validate rejects noncanonical renderings, unknown owners, and incomplete version identities.
func (request GeneratedRevisionRequest) Validate() error {
	title, titleErr := CanonicalizeTitle(request.Title)
	target, pathErr := CanonicalizeTargetPath(request.TargetPath)
	if !validID(request.WorkspaceID) || !validID(request.DocumentID) || !validID(request.ArticleRevisionID) ||
		request.OriginKind != GeneratedOriginSynthesisNote || !validID(request.OriginID) || !validID(request.OriginRevisionID) ||
		!validHash(request.ProjectionHash) || request.ExpectedDocumentVersion < 0 || request.ExpectedDocumentVersion == math.MaxInt64 ||
		request.RevisionNo < 1 || request.RevisionNo > MaxRevisionNo ||
		titleErr != nil || title != request.Title || pathErr != nil || target != request.TargetPath ||
		!validBody(request.Content, false) || CanonicalizeMarkdown(request.Content) != request.Content {
		return invalid(ErrorCodeGeneratedInvalid, "generated revision request is invalid")
	}
	if (request.RevisionNo == 1 && (request.ExpectedDocumentVersion != 0 || request.ParentRevisionID != "")) ||
		(request.RevisionNo > 1 && (request.ExpectedDocumentVersion < 1 || !validID(request.ParentRevisionID))) ||
		request.ParentRevisionID == request.ArticleRevisionID {
		return invalid(ErrorCodeGeneratedInvalid, "generated revision baseline is invalid")
	}
	return nil
}

// ComputeGeneratedRevisionRequestHash binds the complete internal command without copying its body.
func ComputeGeneratedRevisionRequestHash(request GeneratedRevisionRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	return requestHash("generated-article-revision/v1", struct {
		WorkspaceID             foundation.ID       `json:"workspace_id"`
		DocumentID              foundation.ID       `json:"document_id"`
		ArticleRevisionID       foundation.ID       `json:"article_revision_id"`
		OriginKind              GeneratedOriginKind `json:"origin_kind"`
		OriginID                foundation.ID       `json:"origin_id"`
		OriginRevisionID        foundation.ID       `json:"origin_revision_id"`
		ProjectionHash          string              `json:"projection_hash"`
		ExpectedDocumentVersion int64               `json:"expected_document_version"`
		ParentRevisionID        foundation.ID       `json:"parent_revision_id"`
		RevisionNo              int                 `json:"revision_no"`
		Title                   string              `json:"title"`
		TargetPath              string              `json:"target_path"`
		ContentHash             string              `json:"content_hash"`
		ContentBytes            int                 `json:"content_bytes"`
	}{request.WorkspaceID, request.DocumentID, request.ArticleRevisionID, request.OriginKind,
		request.OriginID, request.OriginRevisionID, request.ProjectionHash, request.ExpectedDocumentVersion,
		request.ParentRevisionID, request.RevisionNo, request.Title, request.TargetPath,
		ComputeContentHash(request.Content), len(request.Content)})
}

// DefaultGeneratedTargetPath stays directly under the existing Workspace root.
// Safe Writeback still verifies target absence and the real parent directory.
func DefaultGeneratedTargetPath(originID foundation.ID) (string, error) {
	if !validID(originID) {
		return "", invalid(ErrorCodeGeneratedInvalid, "generated document origin is invalid")
	}
	return "synthesis-" + string(originID) + ".md", nil
}

// PrepareGeneratedRevision appends an AGENT revision without touching a Working Draft.
// The repository separately proves that the Document and parent share this immutable origin.
func PrepareGeneratedRevision(request GeneratedRevisionRequest, existing *Document, parent *ArticleRevision, at time.Time) (Document, ArticleRevision, error) {
	if err := request.Validate(); err != nil {
		return Document{}, ArticleRevision{}, err
	}
	at = canonicalTime(at)
	if at.IsZero() {
		return Document{}, ArticleRevision{}, invalid(ErrorCodeGeneratedInvalid, "generated revision time is invalid")
	}
	document := Document{ID: request.DocumentID, WorkspaceID: request.WorkspaceID,
		CanonicalPath: request.TargetPath, Title: request.Title, Lifecycle: DocumentDraft,
		Version: 1, CreatedAt: at, UpdatedAt: at}
	if request.ExpectedDocumentVersion == 0 {
		if existing != nil || parent != nil {
			return Document{}, ArticleRevision{}, generatedOriginConflict("new generated document already has history")
		}
	} else {
		if existing == nil || parent == nil || existing.Validate() != nil || parent.Validate() != nil ||
			existing.ID != request.DocumentID || existing.WorkspaceID != request.WorkspaceID ||
			existing.Version != request.ExpectedDocumentVersion || existing.CanonicalPath != request.TargetPath ||
			(existing.Lifecycle != DocumentDraft && existing.Lifecycle != DocumentPublished) ||
			parent.ID != request.ParentRevisionID || parent.DocumentID != request.DocumentID ||
			parent.WorkspaceID != request.WorkspaceID || parent.RevisionNo+1 != request.RevisionNo ||
			parent.CreatedByType != "AGENT" || parent.SourceVersionID != "" ||
			(parent.Status != RevisionDraft && parent.Status != RevisionPublished) ||
			at.Before(existing.UpdatedAt) || at.Before(parent.CreatedAt) {
			return Document{}, ArticleRevision{}, generatedOriginConflict("generated document baseline changed")
		}
		document = *existing
		document.Title = request.Title
		document.Version++
		document.UpdatedAt = at
	}
	revision := ArticleRevision{ID: request.ArticleRevisionID, WorkspaceID: request.WorkspaceID,
		DocumentID: request.DocumentID, ParentRevisionID: request.ParentRevisionID, RevisionNo: request.RevisionNo,
		Content: request.Content, ContentHash: ComputeContentHash(request.Content), Status: RevisionDraft,
		OptimizationMode: "NONE", CreatedByType: "AGENT", CreatedAt: at}
	if err := document.Validate(); err != nil {
		return Document{}, ArticleRevision{}, err
	}
	if err := revision.Validate(); err != nil {
		return Document{}, ArticleRevision{}, err
	}
	return document, revision, nil
}

// GeneratedPublicationRetirementRequest selects one generated candidate and its exact pending publication.
type GeneratedPublicationRetirementRequest struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	ArticleRevisionID       foundation.ID
	PublicationID           foundation.ID
	OriginKind              GeneratedOriginKind
	OriginID                foundation.ID
	OriginRevisionID        foundation.ID
	ProjectionHash          string
	ExpectedDocumentVersion int64
	ExpectedProposalVersion int64
}

// Validate requires the internal semantic identity as well as both aggregate versions.
func (request GeneratedPublicationRetirementRequest) Validate() error {
	if !validID(request.WorkspaceID) || !validID(request.DocumentID) || !validID(request.ArticleRevisionID) ||
		!validID(request.PublicationID) || request.OriginKind != GeneratedOriginSynthesisNote ||
		!validID(request.OriginID) || !validID(request.OriginRevisionID) || !validHash(request.ProjectionHash) ||
		request.ExpectedDocumentVersion < 1 || request.ExpectedProposalVersion < 1 || request.ExpectedProposalVersion == math.MaxInt64 {
		return invalid(ErrorCodeGeneratedInvalid, "generated publication retirement request is invalid")
	}
	return nil
}

// ComputeGeneratedPublicationRetirementRequestHash binds retirement without inventing an Approval.
func ComputeGeneratedPublicationRetirementRequestHash(request GeneratedPublicationRetirementRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	return requestHash("generated-publication-retirement/v1", request)
}

func generatedOriginConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeGeneratedOriginConflict, false, errors.New(message))
}
