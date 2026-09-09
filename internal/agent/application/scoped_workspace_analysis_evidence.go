package application

import (
	"context"
	"errors"
	"log/slog"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisEvidenceSeed carries the immutable local Search binding.
type WorkspaceAnalysisEvidenceSeed struct {
	LocalEvidenceRef string        `json:"-"`
	CitationID       string        `json:"-"`
	IndexVersionID   foundation.ID `json:"-"`
	ChunkID          foundation.ID `json:"-"`
	SourceVersionID  foundation.ID `json:"-"`
	SourceSpanID     foundation.ID `json:"-"`
	ContentHash      string        `json:"-"`
}

// WorkspaceAnalysisEvidenceBinding retains a global alias for the lifetime of
// one analysis run, even when later searches return the same underlying source.
type WorkspaceAnalysisEvidenceBinding struct {
	WorkspaceAnalysisEvidenceSeed
	ReferenceNo        int                                  `json:"-"`
	SearchOperationKey domain.WorkspaceAnalysisOperationKey `json:"-"`
	SearchOperationID  foundation.ID                        `json:"-"`
	SearchReceiptID    foundation.ID                        `json:"-"`
	SearchReceiptHash  string                               `json:"-"`
}

func (WorkspaceAnalysisEvidenceSeed) String() string {
	return "WorkspaceAnalysisEvidenceSeed{redacted}"
}
func (s WorkspaceAnalysisEvidenceSeed) GoString() string { return s.String() }
func (WorkspaceAnalysisEvidenceSeed) LogValue() slog.Value {
	return slog.StringValue("evidence_seed:redacted")
}
func (WorkspaceAnalysisEvidenceBinding) String() string {
	return "WorkspaceAnalysisEvidenceBinding{redacted}"
}
func (b WorkspaceAnalysisEvidenceBinding) GoString() string { return b.String() }
func (WorkspaceAnalysisEvidenceBinding) LogValue() slog.Value {
	return slog.StringValue("evidence_binding:redacted")
}

type AppendWorkspaceAnalysisEvidenceCommand struct {
	Identity     WorkspaceAnalysisToolExecutionIdentity
	OperationKey domain.WorkspaceAnalysisOperationKey
	OperationID  foundation.ID
	ReceiptID    foundation.ID
	ReceiptHash  string
	Items        []WorkspaceAnalysisEvidenceSeed
}

func (c AppendWorkspaceAnalysisEvidenceCommand) Validate() error {
	if c.Identity.Validate() != nil || c.Identity.DefinitionVersion != 2 || c.OperationKey.Validate() != nil || c.OperationKey.NodeKey != domain.WorkspaceAnalysisOperationNodeDecideNext || c.OperationKey.Kind != domain.WorkspaceAnalysisOperationKnowledgeSearch || c.Identity.NodeKey != c.OperationKey.NodeKey || validateWorkspaceAnalysisModelQueryIDs(c.Identity.WorkspaceID, c.Identity.WorkflowRunID, c.OperationKey.AnalysisRunID, c.OperationID, c.ReceiptID) != nil || !canonicalWorkspaceAnalysisSHA256(c.ReceiptHash) || c.Items == nil || len(c.Items) > 5 {
		return workspaceAnalysisToolInvalid(errors.New("dynamic evidence append binding is invalid"))
	}
	for i, item := range c.Items {
		if item.LocalEvidenceRef != string([]byte{'E', byte('1' + i)}) || validateWorkspaceAnalysisModelQueryIDs(item.IndexVersionID, item.ChunkID, item.SourceVersionID, item.SourceSpanID) != nil || !canonicalWorkspaceAnalysisSHA256(item.ContentHash) || len(item.CitationID) < 1 || len(item.CitationID) > 128 {
			return workspaceAnalysisToolInvalid(errors.New("dynamic evidence identity is invalid"))
		}
	}
	return nil
}

type WorkspaceAnalysisEvidenceQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
}

type ScopedWorkspaceAnalysisEvidenceParticipant interface {
	AppendWorkspaceAnalysisEvidenceScoped(context.Context, foundation.TransactionScope, AppendWorkspaceAnalysisEvidenceCommand) error
	LoadWorkspaceAnalysisEvidenceScoped(context.Context, foundation.TransactionScope, WorkspaceAnalysisEvidenceQuery) ([]WorkspaceAnalysisEvidenceBinding, error)
	LoadWorkspaceAnalysisSuccessfulReadKeysScoped(context.Context, foundation.TransactionScope, WorkspaceAnalysisEvidenceQuery) ([]domain.WorkspaceAnalysisOperationKey, error)
}
