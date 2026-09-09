package application

import (
	"context"
	"encoding/json"
	"log/slog"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// WorkspaceAnalysisDynamicToolQuery identifies a logical operation, never the
// most recent tool call. It is safe to reconstruct on a replacement Worker.
type WorkspaceAnalysisDynamicToolQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	OperationKey  agentdomain.WorkspaceAnalysisOperationKey
}

type WorkspaceAnalysisDynamicToolAuthorityReader interface {
	LoadWorkspaceAnalysisDynamicToolOutput(context.Context, WorkspaceAnalysisDynamicToolQuery) (json.RawMessage, error)
	WorkspaceAnalysisDynamicSearchLimit(context.Context, WorkspaceAnalysisDynamicToolQuery) (int, error)
}

type ReadSourceV4AuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	EvidenceRef   string
}

// ReadSourceV4Authority contains the original Search receipt and both aliases.
// Consumers must validate the receipt and compare its immutable identity tuple.
type ReadSourceV4Authority struct {
	WorkspaceID       foundation.ID                  `json:"-"`
	WorkflowRunID     foundation.ID                  `json:"-"`
	AnalysisRunID     foundation.ID                  `json:"-"`
	EvidenceRef       string                         `json:"-"`
	SearchEvidenceRef string                         `json:"-"`
	SearchReceipt     domain.ResultReceipt           `json:"-"`
	Identity          domain.DynamicEvidenceIdentity `json:"-"`
}

func (ReadSourceV4Authority) String() string     { return "ReadSourceV4Authority{redacted}" }
func (a ReadSourceV4Authority) GoString() string { return a.String() }
func (ReadSourceV4Authority) LogValue() slog.Value {
	return slog.StringValue("source_authority:redacted")
}

type ReadSourceV4AuthorityReader interface {
	LoadReadSourceV4Authority(context.Context, ReadSourceV4AuthorityQuery) (ReadSourceV4Authority, error)
}

type ValidateCitationV4AuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	CandidateID   foundation.ID
	CandidateHash string
	EvidenceRefs  []string
}

// ValidateCitationV4Authority binds each requested global reference to its own
// Search and Read receipt. Empty candidate identity denotes an in-loop check.
type ValidateCitationV4Authority struct {
	WorkspaceID        foundation.ID          `json:"-"`
	WorkflowRunID      foundation.ID          `json:"-"`
	AnalysisRunID      foundation.ID          `json:"-"`
	CandidateID        foundation.ID          `json:"-"`
	CandidateHash      string                 `json:"-"`
	EvidenceRefs       []string               `json:"-"`
	SearchReceipts     []domain.ResultReceipt `json:"-"`
	ReadSourceReceipts []domain.ResultReceipt `json:"-"`
}

func (ValidateCitationV4Authority) String() string     { return "ValidateCitationV4Authority{redacted}" }
func (a ValidateCitationV4Authority) GoString() string { return a.String() }
func (ValidateCitationV4Authority) LogValue() slog.Value {
	return slog.StringValue("citation_authority:redacted")
}

type ValidateCitationV4AuthorityReader interface {
	LoadValidateCitationV4Authority(context.Context, ValidateCitationV4AuthorityQuery) (ValidateCitationV4Authority, error)
}

type ValidateCitationV4ReceiptQuery = ValidateCitationV3ReceiptQuery
type ValidateCitationV4PublicationAuthorityQuery = ValidateCitationV3PublicationAuthorityQuery
type ValidateCitationV4PublicationAuthority = ValidateCitationV3PublicationAuthority

type ValidateCitationV4ReceiptReader interface {
	LoadValidateCitationV4Receipt(context.Context, ValidateCitationV4ReceiptQuery) (domain.ResultReceipt, error)
}
type ValidateCitationV4PublicationAuthorityReader interface {
	LoadValidateCitationV4PublicationAuthority(context.Context, ValidateCitationV4PublicationAuthorityQuery) (ValidateCitationV4PublicationAuthority, error)
}
