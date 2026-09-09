package organizinghttp

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const synthesisResultInvalid = "SYNTHESIS_HTTP_RESULT_INCONSISTENT"

type synthesisNoteResponse struct {
	ID                foundation.ID            `json:"id"`
	WorkspaceID       foundation.ID            `json:"workspace_id"`
	DocumentID        foundation.ID            `json:"document_id"`
	TopicKey          string                   `json:"topic_key"`
	Title             string                   `json:"title"`
	Aliases           []string                 `json:"aliases"`
	CurrentRevisionID *string                  `json:"current_revision_id"`
	Version           int64                    `json:"version"`
	Status            domain.SynthesisStatus   `json:"status"`
	WorkflowRunID     *string                  `json:"workflow_run_id"`
	Failure           *domain.SynthesisFailure `json:"failure"`
	CreatedAt         string                   `json:"created_at"`
	UpdatedAt         string                   `json:"updated_at"`
}

type synthesisRevisionSummaryResponse struct {
	ID                foundation.ID `json:"id"`
	RevisionNo        int64         `json:"revision_no"`
	ArticleRevisionID foundation.ID `json:"article_revision_id"`
	ArticleRevisionNo int64         `json:"article_revision_no"`
	ContentHash       string        `json:"content_hash"`
	CreatedAt         string        `json:"created_at"`
}

type synthesisRevisionResponse struct {
	ID                foundation.ID          `json:"id"`
	WorkspaceID       foundation.ID          `json:"workspace_id"`
	NoteID            foundation.ID          `json:"note_id"`
	DocumentID        foundation.ID          `json:"document_id"`
	ArticleRevisionID foundation.ID          `json:"article_revision_id"`
	RevisionNo        int64                  `json:"revision_no"`
	ArticleRevisionNo int64                  `json:"article_revision_no"`
	ParentRevisionID  *string                `json:"parent_revision_id"`
	Title             string                 `json:"title"`
	ContentHash       string                 `json:"content_hash"`
	ProjectionHash    string                 `json:"projection_hash"`
	Items             []domain.SynthesisItem `json:"items"`
	CreatedAt         string                 `json:"created_at"`
}

type synthesisPublicationResponse struct {
	RevisionID         foundation.ID `json:"revision_id"`
	ArticleRevisionID  foundation.ID `json:"article_revision_id"`
	ProposalID         foundation.ID `json:"proposal_id"`
	ProposalRevisionID foundation.ID `json:"proposal_revision_id"`
	ContentHash        string        `json:"content_hash"`
}

type synthesisNoteSummaryResponse struct {
	Note              synthesisNoteResponse             `json:"note"`
	CurrentRevision   *synthesisRevisionSummaryResponse `json:"current_revision"`
	PublishedRevision *synthesisRevisionSummaryResponse `json:"published_revision"`
	Publication       *synthesisPublicationResponse     `json:"publication"`
	ItemCount         int                               `json:"item_count"`
	ConflictCount     int                               `json:"conflict_count"`
	GapCount          int                               `json:"gap_count"`
	OpenGapCount      int                               `json:"open_gap_count"`
}

type synthesisProcessingResponse struct {
	ID              foundation.ID                 `json:"id"`
	WorkspaceID     foundation.ID                 `json:"workspace_id"`
	SourceVersionID foundation.ID                 `json:"source_version_id"`
	WorkflowRunID   *string                       `json:"workflow_run_id"`
	Status          app.SynthesisProcessingStatus `json:"status"`
	RevisionIDs     []foundation.ID               `json:"revision_ids"`
	Failure         *domain.SynthesisFailure      `json:"failure"`
	Version         int64                         `json:"version"`
	CreatedAt       string                        `json:"created_at"`
	UpdatedAt       string                        `json:"updated_at"`
	CompletedAt     *string                       `json:"completed_at"`
}

type synthesisNoteDetailResponse struct {
	WorkspaceID       foundation.ID                 `json:"workspace_id"`
	Note              synthesisNoteResponse         `json:"note"`
	CurrentRevision   *synthesisRevisionResponse    `json:"current_revision"`
	PublishedRevision *synthesisRevisionResponse    `json:"published_revision"`
	Publication       *synthesisPublicationResponse `json:"publication"`
	LatestProcessing  *synthesisProcessingResponse  `json:"latest_processing"`
}

func synthesisInvalidResult() error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, synthesisResultInvalid, false, errors.New("synthesis response binding is invalid"))
}

func toSynthesisNote(note domain.SynthesisNote, workspaceID foundation.ID) (synthesisNoteResponse, error) {
	if note.Validate() != nil || note.WorkspaceID != workspaceID {
		return synthesisNoteResponse{}, synthesisInvalidResult()
	}
	return synthesisNoteResponse{ID: note.ID, WorkspaceID: note.WorkspaceID, DocumentID: note.DocumentID,
		TopicKey: note.TopicKey, Title: note.Title, Aliases: append([]string{}, note.Aliases...),
		CurrentRevisionID: optionalID(note.CurrentRevisionID), Version: note.Version, Status: note.Status,
		WorkflowRunID: optionalID(note.WorkflowRunID), Failure: note.Failure,
		CreatedAt: formatTime(note.CreatedAt), UpdatedAt: formatTime(note.UpdatedAt)}, nil
}

func toSynthesisRevisionSummary(revision *app.SynthesisRevisionSummary) (*synthesisRevisionSummaryResponse, error) {
	if revision == nil {
		return nil, nil
	}
	if !validResponseID(revision.ID) || !validResponseID(revision.ArticleRevisionID) || revision.RevisionNo < 1 ||
		revision.RevisionNo > domain.MaxSynthesisRevisionNo || revision.ArticleRevisionNo < 1 || !synthesisValidHash(revision.ContentHash) || revision.CreatedAt.IsZero() {
		return nil, synthesisInvalidResult()
	}
	return &synthesisRevisionSummaryResponse{ID: revision.ID, RevisionNo: revision.RevisionNo,
		ArticleRevisionID: revision.ArticleRevisionID, ArticleRevisionNo: revision.ArticleRevisionNo,
		ContentHash: revision.ContentHash, CreatedAt: formatTime(revision.CreatedAt)}, nil
}

func synthesisValidHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func synthesisSummary(revision domain.SynthesisRevision) app.SynthesisRevisionSummary {
	return app.SynthesisRevisionSummary{ID: revision.ID, RevisionNo: revision.RevisionNo, ArticleRevisionID: revision.ArticleRevisionID,
		ArticleRevisionNo: revision.ArticleRevisionNo, ContentHash: revision.ContentHash, CreatedAt: revision.CreatedAt}
}

func toSynthesisPublication(publication *app.SynthesisPublicationRef, revision *synthesisRevisionSummaryResponse) (*synthesisPublicationResponse, error) {
	if publication == nil {
		return nil, nil
	}
	if revision == nil || publication.RevisionID != revision.ID || publication.ArticleRevisionID != revision.ArticleRevisionID ||
		publication.ContentHash != revision.ContentHash || !validResponseID(publication.ProposalID) || !validResponseID(publication.ProposalRevisionID) {
		return nil, synthesisInvalidResult()
	}
	return &synthesisPublicationResponse{RevisionID: publication.RevisionID, ArticleRevisionID: publication.ArticleRevisionID,
		ProposalID: publication.ProposalID, ProposalRevisionID: publication.ProposalRevisionID, ContentHash: publication.ContentHash}, nil
}

func toSynthesisNoteSummary(value app.SynthesisNoteSummary, workspaceID foundation.ID) (synthesisNoteSummaryResponse, error) {
	note, err := toSynthesisNote(value.Note, workspaceID)
	if err != nil {
		return synthesisNoteSummaryResponse{}, err
	}
	current, err := toSynthesisRevisionSummary(value.CurrentRevision)
	if err != nil {
		return synthesisNoteSummaryResponse{}, err
	}
	published, err := toSynthesisRevisionSummary(value.PublishedRevision)
	if err != nil {
		return synthesisNoteSummaryResponse{}, err
	}
	if (current == nil) != (note.CurrentRevisionID == nil) || current != nil && string(current.ID) != *note.CurrentRevisionID ||
		value.ItemCount < 0 || value.ItemCount > domain.MaxSynthesisItems || value.ConflictCount < 0 || value.GapCount < 0 ||
		value.ConflictCount+value.GapCount > value.ItemCount || value.OpenGapCount < 0 || value.OpenGapCount > value.GapCount ||
		current == nil && (published != nil || value.ItemCount != 0) || published != nil && published.RevisionNo > current.RevisionNo {
		return synthesisNoteSummaryResponse{}, synthesisInvalidResult()
	}
	if current != nil && published != nil && current.ID == published.ID && *current != *published {
		return synthesisNoteSummaryResponse{}, synthesisInvalidResult()
	}
	publication, err := toSynthesisPublication(value.Publication, current)
	if err != nil {
		return synthesisNoteSummaryResponse{}, err
	}
	return synthesisNoteSummaryResponse{Note: note, CurrentRevision: current, PublishedRevision: published, Publication: publication,
		ItemCount: value.ItemCount, ConflictCount: value.ConflictCount, GapCount: value.GapCount, OpenGapCount: value.OpenGapCount}, nil
}

func toSynthesisRevision(value *domain.SynthesisRevision, workspaceID, noteID foundation.ID) (*synthesisRevisionResponse, error) {
	if value == nil {
		return nil, nil
	}
	if value.Validate() != nil || value.WorkspaceID != workspaceID || value.NoteID != noteID {
		return nil, synthesisInvalidResult()
	}
	items := make([]domain.SynthesisItem, len(value.Items))
	for index, item := range value.Items {
		items[index] = item
		if item.Fact != nil {
			fact := *item.Fact
			fact.Sources = append([]domain.SynthesisSourceRef{}, fact.Sources...)
			items[index].Fact = &fact
		}
		if item.Conflict != nil {
			conflict := *item.Conflict
			conflict.Alternatives = append([]domain.SynthesisStatement{}, conflict.Alternatives...)
			for alternative := range conflict.Alternatives {
				conflict.Alternatives[alternative].Sources = append([]domain.SynthesisSourceRef{}, conflict.Alternatives[alternative].Sources...)
			}
			items[index].Conflict = &conflict
		}
		if item.Gap != nil {
			gap := *item.Gap
			gap.Sources = append([]domain.SynthesisSourceRef{}, gap.Sources...)
			if gap.Resolution != nil {
				resolution := *gap.Resolution
				resolution.Sources = append([]domain.SynthesisSourceRef{}, resolution.Sources...)
				gap.Resolution = &resolution
			}
			items[index].Gap = &gap
		}
	}
	return &synthesisRevisionResponse{ID: value.ID, WorkspaceID: value.WorkspaceID, NoteID: value.NoteID, DocumentID: value.DocumentID,
		ArticleRevisionID: value.ArticleRevisionID, RevisionNo: value.RevisionNo, ArticleRevisionNo: value.ArticleRevisionNo,
		ParentRevisionID: optionalID(value.ParentRevisionID), Title: value.Title, ContentHash: value.ContentHash,
		ProjectionHash: value.Hash, Items: items, CreatedAt: formatTime(value.CreatedAt)}, nil
}

func toSynthesisProcessing(value app.SynthesisProcessing, workspaceID foundation.ID) (synthesisProcessingResponse, error) {
	if !validResponseID(value.ID) || value.SourceEvent.Validate() != nil || value.SourceEvent.Source.WorkspaceID != workspaceID ||
		!value.Status.Valid() || value.Version < 1 || value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) ||
		value.WorkflowRunID != "" && !validResponseID(value.WorkflowRunID) || len(value.RevisionIDs) > app.MaxSynthesisGeneratedNotes {
		return synthesisProcessingResponse{}, synthesisInvalidResult()
	}
	terminal := value.Status != app.SynthesisProcessingPending && value.Status != app.SynthesisProcessingRunning
	failed := value.Status == app.SynthesisProcessingFailed || value.Status == app.SynthesisProcessingRecoveryRequired
	if failed != (value.Failure != nil) || value.Failure != nil && !validErrorCode(value.Failure.Code) ||
		value.Status == app.SynthesisProcessingRecoveryRequired && value.Failure.Retryable ||
		terminal != (value.CompletedAt != nil) || value.CompletedAt != nil && (value.CompletedAt.Before(value.CreatedAt) || value.CompletedAt.After(value.UpdatedAt)) ||
		value.Status == app.SynthesisProcessingRunning && value.WorkflowRunID == "" ||
		value.Status == app.SynthesisProcessingSucceeded && len(value.RevisionIDs) == 0 ||
		(value.Status == app.SynthesisProcessingNoChange || value.Status == app.SynthesisProcessingSkipped) && len(value.RevisionIDs) != 0 {
		return synthesisProcessingResponse{}, synthesisInvalidResult()
	}
	seen := make(map[foundation.ID]bool, len(value.RevisionIDs))
	for _, id := range value.RevisionIDs {
		if !validResponseID(id) || seen[id] {
			return synthesisProcessingResponse{}, synthesisInvalidResult()
		}
		seen[id] = true
	}
	return synthesisProcessingResponse{ID: value.ID, WorkspaceID: workspaceID, SourceVersionID: value.SourceEvent.Source.SourceVersionID,
		WorkflowRunID: optionalID(value.WorkflowRunID), Status: value.Status, RevisionIDs: append([]foundation.ID{}, value.RevisionIDs...),
		Failure: value.Failure, Version: value.Version, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt),
		CompletedAt: synthesisOptionalTime(value.CompletedAt)}, nil
}

func synthesisOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func toSynthesisDetail(value app.SynthesisNoteDetail, workspaceID, noteID foundation.ID) (synthesisNoteDetailResponse, error) {
	note, err := toSynthesisNote(value.Note, workspaceID)
	if err != nil || value.Note.ID != noteID {
		return synthesisNoteDetailResponse{}, synthesisInvalidResult()
	}
	current, err := toSynthesisRevision(value.CurrentRevision, workspaceID, noteID)
	if err != nil {
		return synthesisNoteDetailResponse{}, err
	}
	published, err := toSynthesisRevision(value.PublishedRevision, workspaceID, noteID)
	if err != nil {
		return synthesisNoteDetailResponse{}, err
	}
	if (current == nil) != (note.CurrentRevisionID == nil) || current != nil && (string(current.ID) != *note.CurrentRevisionID || current.DocumentID != note.DocumentID) ||
		published != nil && (current == nil || published.DocumentID != note.DocumentID || published.RevisionNo > current.RevisionNo) {
		return synthesisNoteDetailResponse{}, synthesisInvalidResult()
	}
	if current != nil && published != nil && current.ID == published.ID && current.ProjectionHash != published.ProjectionHash {
		return synthesisNoteDetailResponse{}, synthesisInvalidResult()
	}
	var summary *synthesisRevisionSummaryResponse
	if value.CurrentRevision != nil {
		ref := synthesisSummary(*value.CurrentRevision)
		summary, err = toSynthesisRevisionSummary(&ref)
		if err != nil {
			return synthesisNoteDetailResponse{}, err
		}
	}
	publication, err := toSynthesisPublication(value.Publication, summary)
	if err != nil {
		return synthesisNoteDetailResponse{}, err
	}
	var processing *synthesisProcessingResponse
	if value.LatestProcessing != nil {
		projection, err := toSynthesisProcessing(*value.LatestProcessing, workspaceID)
		if err != nil {
			return synthesisNoteDetailResponse{}, err
		}
		processing = &projection
	}
	return synthesisNoteDetailResponse{WorkspaceID: workspaceID, Note: note, CurrentRevision: current, PublishedRevision: published,
		Publication: publication, LatestProcessing: processing}, nil
}
