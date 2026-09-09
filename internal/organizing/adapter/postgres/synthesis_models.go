package postgres

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/lib/pq"
)

type synthesisNoteModel struct {
	ID                string           `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       string           `gorm:"column:workspace_id;type:uuid"`
	DocumentID        string           `gorm:"column:document_id;type:uuid"`
	TopicKey          string           `gorm:"column:topic_key"`
	Title             string           `gorm:"column:title"`
	Aliases           pq.StringArray   `gorm:"column:aliases;type:text[]"`
	CurrentRevisionID string           `gorm:"column:current_revision_id;type:uuid"`
	Version           int64            `gorm:"column:version"`
	Status            string           `gorm:"column:status"`
	WorkflowRunID     *string          `gorm:"column:workflow_run_id;type:uuid"`
	Failure           *organizingJSONB `gorm:"column:failure;type:jsonb"`
	CreatedAt         time.Time        `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt         time.Time        `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (synthesisNoteModel) TableName() string { return "organizing.synthesis_note" }

const synthesisNoteColumns = "id,workspace_id,document_id,topic_key,title,aliases,current_revision_id,version,status,workflow_run_id,failure,created_at,updated_at"

func (model synthesisNoteModel) domain() (domain.SynthesisNote, error) {
	note := domain.SynthesisNote{ID: foundation.ID(model.ID), WorkspaceID: foundation.ID(model.WorkspaceID), DocumentID: foundation.ID(model.DocumentID), TopicKey: model.TopicKey, Title: model.Title, Aliases: append([]string{}, model.Aliases...), CurrentRevisionID: foundation.ID(model.CurrentRevisionID), Version: model.Version, Status: domain.SynthesisStatus(model.Status), CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC()}
	if model.WorkflowRunID != nil {
		note.WorkflowRunID = foundation.ID(*model.WorkflowRunID)
	}
	if model.Failure != nil {
		if err := json.Unmarshal(*model.Failure, &note.Failure); err != nil {
			return note, synthesisConsistency("stored synthesis failure is invalid")
		}
	}
	if err := note.Validate(); err != nil {
		return domain.SynthesisNote{}, synthesisConsistency("stored synthesis note is invalid")
	}
	return note, nil
}

type synthesisRevisionModel struct {
	ID                string          `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       string          `gorm:"column:workspace_id;type:uuid"`
	NoteID            string          `gorm:"column:note_id;type:uuid"`
	DocumentID        string          `gorm:"column:document_id;type:uuid"`
	ArticleRevisionID string          `gorm:"column:article_revision_id;type:uuid"`
	RevisionNo        int64           `gorm:"column:revision_no"`
	ArticleRevisionNo int64           `gorm:"column:article_revision_no"`
	ParentRevisionID  *string         `gorm:"column:parent_revision_id;type:uuid"`
	Title             string          `gorm:"column:title"`
	RendererVersion   string          `gorm:"column:renderer_version"`
	ContentHash       string          `gorm:"column:content_hash"`
	ProjectionHash    string          `gorm:"column:projection_hash"`
	Items             organizingJSONB `gorm:"column:items;type:jsonb"`
	Delta             organizingJSONB `gorm:"column:delta;type:jsonb"`
	ItemCount         int             `gorm:"column:item_count"`
	ConflictCount     int             `gorm:"column:conflict_count"`
	GapCount          int             `gorm:"column:gap_count"`
	OpenGapCount      int             `gorm:"column:open_gap_count"`
	SourceEventID     string          `gorm:"column:source_event_id;type:uuid"`
	WorkflowRunID     string          `gorm:"column:workflow_run_id;type:uuid"`
	ModelRunID        string          `gorm:"column:model_run_id;type:uuid"`
	CreatedAt         time.Time       `gorm:"column:created_at;autoCreateTime:false"`
}

func (synthesisRevisionModel) TableName() string { return "organizing.synthesis_revision" }

const synthesisRevisionColumns = "id,workspace_id,note_id,document_id,article_revision_id,revision_no,article_revision_no,parent_revision_id,title,renderer_version,content_hash,projection_hash,items,delta,item_count,conflict_count,gap_count,open_gap_count,source_event_id,workflow_run_id,model_run_id,created_at"
const synthesisRevisionSummaryColumns = "id,workspace_id,note_id,document_id,article_revision_id,revision_no,article_revision_no,content_hash,item_count,conflict_count,gap_count,open_gap_count,created_at"

func (model synthesisRevisionModel) domain() (domain.SynthesisRevision, error) {
	revision := domain.SynthesisRevision{ID: foundation.ID(model.ID), WorkspaceID: foundation.ID(model.WorkspaceID), NoteID: foundation.ID(model.NoteID), DocumentID: foundation.ID(model.DocumentID), ArticleRevisionID: foundation.ID(model.ArticleRevisionID), RevisionNo: model.RevisionNo, ArticleRevisionNo: model.ArticleRevisionNo, Title: model.Title, RendererVersion: model.RendererVersion, ContentHash: model.ContentHash, Hash: model.ProjectionHash, SourceEventID: foundation.ID(model.SourceEventID), WorkflowRunID: foundation.ID(model.WorkflowRunID), ModelRunID: foundation.ID(model.ModelRunID), CreatedAt: model.CreatedAt.UTC()}
	if model.ParentRevisionID != nil {
		revision.ParentRevisionID = foundation.ID(*model.ParentRevisionID)
	}
	if json.Unmarshal(model.Items, &revision.Items) != nil || json.Unmarshal(model.Delta, &revision.Delta) != nil || revision.Validate() != nil {
		return domain.SynthesisRevision{}, synthesisConsistency("stored synthesis revision is invalid")
	}
	return revision, nil
}

func synthesisRevisionRecord(revision domain.SynthesisRevision) (synthesisRevisionModel, error) {
	items, err := json.Marshal(revision.Items)
	if err != nil {
		return synthesisRevisionModel{}, err
	}
	delta, err := json.Marshal(revision.Delta)
	if err != nil {
		return synthesisRevisionModel{}, err
	}
	model := synthesisRevisionModel{ID: string(revision.ID), WorkspaceID: string(revision.WorkspaceID), NoteID: string(revision.NoteID), DocumentID: string(revision.DocumentID), ArticleRevisionID: string(revision.ArticleRevisionID), RevisionNo: revision.RevisionNo, ArticleRevisionNo: revision.ArticleRevisionNo, Title: revision.Title, RendererVersion: revision.RendererVersion, ContentHash: revision.ContentHash, ProjectionHash: revision.Hash, Items: organizingJSONB(items), Delta: organizingJSONB(delta), ItemCount: len(revision.Items), SourceEventID: string(revision.SourceEventID), WorkflowRunID: string(revision.WorkflowRunID), ModelRunID: string(revision.ModelRunID), CreatedAt: revision.CreatedAt}
	if revision.ParentRevisionID != "" {
		parent := string(revision.ParentRevisionID)
		model.ParentRevisionID = &parent
	}
	for _, item := range revision.Items {
		switch item.Kind {
		case domain.SynthesisConflictItem:
			model.ConflictCount++
		case domain.SynthesisGapItem:
			model.GapCount++
			if item.Gap.Resolution == nil {
				model.OpenGapCount++
			}
		}
	}
	return model, nil
}

type synthesisSourceModel struct {
	WorkspaceID       string `gorm:"column:workspace_id;type:uuid"`
	RevisionID        string `gorm:"column:revision_id;type:uuid;primaryKey"`
	NoteID            string `gorm:"column:note_id;type:uuid"`
	SourceID          string `gorm:"column:source_id;type:uuid"`
	SourceVersionID   string `gorm:"column:source_version_id;type:uuid;primaryKey"`
	ContentArtifactID string `gorm:"column:content_artifact_id;type:uuid"`
	ParseProjectionID string `gorm:"column:parse_projection_id;type:uuid;primaryKey"`
	SourceSpanID      string `gorm:"column:source_span_id;type:uuid;primaryKey"`
	ContentHash       string `gorm:"column:content_hash"`
	ExcerptHash       string `gorm:"column:excerpt_hash"`
	Title             string `gorm:"column:title"`
}

func (synthesisSourceModel) TableName() string { return "organizing.synthesis_revision_source" }

type synthesisTopicModel struct {
	WorkspaceID string `gorm:"column:workspace_id;type:uuid;primaryKey"`
	TopicKey    string `gorm:"column:topic_key;primaryKey"`
	NoteID      string `gorm:"column:note_id;type:uuid"`
}

func (synthesisTopicModel) TableName() string { return "organizing.synthesis_topic_key" }

type synthesisApplyModel struct {
	WorkspaceID   string          `gorm:"column:workspace_id;type:uuid;primaryKey"`
	ProcessingID  string          `gorm:"column:processing_id;type:uuid;primaryKey"`
	ProcessingKey string          `gorm:"column:processing_key"`
	SourceEventID string          `gorm:"column:source_event_id;type:uuid"`
	WorkflowRunID string          `gorm:"column:workflow_run_id;type:uuid"`
	ModelRunID    string          `gorm:"column:model_run_id;type:uuid"`
	RequestHash   string          `gorm:"column:request_hash"`
	OutputHash    string          `gorm:"column:output_hash"`
	BindingHash   string          `gorm:"column:binding_hash"`
	Changed       bool            `gorm:"column:changed"`
	Result        organizingJSONB `gorm:"column:result;type:jsonb"`
	CreatedAt     time.Time       `gorm:"column:created_at;autoCreateTime:false"`
}

func (synthesisApplyModel) TableName() string { return "organizing.synthesis_apply_receipt" }

const synthesisApplyColumns = "workspace_id,processing_id,processing_key,source_event_id,workflow_run_id,model_run_id,request_hash,output_hash,binding_hash,changed,result,created_at"

func (model synthesisApplyModel) result() (organizingapp.SynthesisApplyResult, error) {
	var result organizingapp.SynthesisApplyResult
	if json.Unmarshal(model.Result, &result) != nil || organizingapp.ValidateSynthesisApplyResult(result) != nil || string(result.ProcessingID) != model.ProcessingID || result.Changed != model.Changed || result.Replayed {
		return result, synthesisConsistency("stored synthesis receipt is invalid")
	}
	result.Replayed = true
	return result, nil
}

func synthesisConsistency(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeSynthesisConsistency, false, errors.New(message))
}
func synthesisConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSynthesisVersionConflict, true, errors.New(message))
}
func synthesisNotFound() error {
	return foundation.NewError(foundation.ErrorNotFound, organizingapp.ErrorCodeSynthesisNotFound, false, errors.New("synthesis note was not found"))
}
