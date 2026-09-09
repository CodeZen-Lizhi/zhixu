package domain

import "encoding/json"

func (revision SynthesisRevision) Validate() error {
	hash, err := ComputeSynthesisRevisionHash(revision)
	if err != nil {
		return err
	}
	if !isHash(revision.Hash) || hash != revision.Hash {
		return invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision projection hash is invalid")
	}
	return nil
}

// ComputeSynthesisRevisionHash validates the exact rendered ArticleRevision
// content and hashes all immutable projection fields, ignoring only Hash itself.
func ComputeSynthesisRevisionHash(revision SynthesisRevision) (string, error) {
	if !validID(revision.ID) || !validID(revision.WorkspaceID) || !validID(revision.NoteID) || !validID(revision.DocumentID) ||
		!validID(revision.ArticleRevisionID) || revision.RevisionNo < 1 || revision.RevisionNo > MaxSynthesisRevisionNo ||
		revision.ArticleRevisionNo < 1 || revision.ArticleRevisionNo > MaxSynthesisRevisionNo ||
		(revision.RevisionNo == 1 && revision.ParentRevisionID != "") ||
		(revision.RevisionNo > 1 && (!validID(revision.ParentRevisionID) || revision.ParentRevisionID == revision.ID)) ||
		revision.RendererVersion != SynthesisRendererVersion || !isHash(revision.ContentHash) ||
		!validID(revision.SourceEventID) || !validID(revision.WorkflowRunID) || !validID(revision.ModelRunID) ||
		revision.CreatedAt.IsZero() || canonicalTime(revision.CreatedAt) != revision.CreatedAt ||
		len(revision.Items) == 0 || len(revision.Delta.Operations) == 0 {
		return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision binding is incomplete")
	}
	if err := revision.Delta.Validate(revision.WorkspaceID); err != nil {
		return "", err
	}
	content, err := RenderSynthesisMarkdown(revision.WorkspaceID, revision.NoteID, revision.Title, revision.Items)
	if err != nil {
		return "", err
	}
	if synthesisHash([]byte(content)) != revision.ContentHash {
		return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision does not match its article content hash")
	}
	revision.Hash = ""
	encoded, err := json.Marshal(struct {
		Schema   string            `json:"schema"`
		Revision SynthesisRevision `json:"revision"`
	}{SynthesisRevisionSchema, revision})
	if err != nil {
		return "", invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis revision cannot be encoded")
	}
	return synthesisHash(encoded), nil
}

// SynthesisSnapshotFromRevision deep-copies an exact validated revision. This
// pure conversion makes no claim about publication; the owner reader must prove
// published status before exposing it as interview material.
func SynthesisSnapshotFromRevision(revision SynthesisRevision) (SynthesisNoteSnapshot, error) {
	if err := revision.Validate(); err != nil {
		return SynthesisNoteSnapshot{}, err
	}
	return SynthesisNoteSnapshot{
		WorkspaceID: revision.WorkspaceID, NoteID: revision.NoteID, RevisionID: revision.ID, RevisionNo: revision.RevisionNo,
		DocumentID: revision.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, ArticleRevisionNo: revision.ArticleRevisionNo,
		ContentHash: revision.ContentHash, ProjectionHash: revision.Hash, Title: revision.Title, RendererVersion: revision.RendererVersion,
		Items: cloneSynthesisItems(revision.Items),
	}, nil
}

func (snapshot SynthesisNoteSnapshot) Validate() error {
	if !validID(snapshot.WorkspaceID) || !validID(snapshot.NoteID) || !validID(snapshot.RevisionID) || !validID(snapshot.DocumentID) ||
		!validID(snapshot.ArticleRevisionID) || snapshot.RevisionNo < 1 || snapshot.RevisionNo > MaxSynthesisRevisionNo ||
		snapshot.ArticleRevisionNo < 1 || snapshot.ArticleRevisionNo > MaxSynthesisRevisionNo || !isHash(snapshot.ContentHash) ||
		!isHash(snapshot.ProjectionHash) || snapshot.RendererVersion != SynthesisRendererVersion || len(snapshot.Items) == 0 {
		return invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis note snapshot binding is incomplete")
	}
	content, err := RenderSynthesisMarkdown(snapshot.WorkspaceID, snapshot.NoteID, snapshot.Title, snapshot.Items)
	if err != nil {
		return err
	}
	if synthesisHash([]byte(content)) != snapshot.ContentHash {
		return invalid(ErrorCodeSynthesisRevisionInvalid, "synthesis note snapshot content changed")
	}
	return nil
}
