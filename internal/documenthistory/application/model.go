package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// DocumentSnapshot is the Authoring-owned identity needed by file History.
type DocumentSnapshot struct {
	ID                         foundation.ID
	WorkspaceID                foundation.ID
	CanonicalPath              string
	Title                      string
	Lifecycle                  string
	CurrentPublishedRevisionID foundation.ID
	Version                    int64
}

// DocumentReader reads one Document and batch-enriches Git commits without owning them.
type DocumentReader interface {
	GetDocument(context.Context, foundation.ID, foundation.ID) (DocumentSnapshot, error)
	MapCommits(context.Context, foundation.ID, foundation.ID, string, []string) ([]domain.CommitMapping, error)
}

// GitHistoryReader is the narrow, read-only Git history boundary.
type GitHistoryReader interface {
	Inspect(context.Context, foundation.ID, string) (domain.RepositoryState, error)
	List(context.Context, foundation.ID, string, int, int) (domain.CommitPage, error)
	Compare(context.Context, foundation.ID, string, string, string) (domain.Diff, error)
	ReadBlob(context.Context, foundation.ID, string, string) (domain.Blob, error)
}

// RestoreProposalReceipt is the durable result of creating a governed restore Proposal.
type RestoreProposalReceipt struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	ProposalID              foundation.ID
	ProposalRevisionID      foundation.ID
	TargetCommit            string
	ExpectedHead            string
	ExpectedDocumentVersion int64
	PreviewHash             string
	ProposalType            string
	Status                  string
	ChangeHash              string
	Replayed                bool
}

// RestoreProposalCreator creates the typed Change Control proposal after server-side preview verification.
type RestoreProposalCreator interface {
	CreateRestoreDocumentProposal(context.Context, CreateRestoreProposalRecord) (RestoreProposalReceipt, error)
}

// RestoreProposalLookup enables response-loss replay before mutable Git facts are re-read.
type RestoreProposalLookup interface {
	FindRestoreProposal(context.Context, foundation.ID, string) (RestoreProposalReceipt, bool, error)
}

// CreateRestoreProposalRecord contains only server-derived file content and immutable request bindings.
type CreateRestoreProposalRecord struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	TargetPath              string
	TargetCommit            string
	ExpectedHead            string
	ExpectedDocumentVersion int64
	PreviewHash             string
	CurrentContentHash      string
	TargetContentHash       string
	TargetContent           string
	IdempotencyKey          string
}

// HistoryQuery describes a bounded Document history page.
type HistoryQuery struct {
	WorkspaceID foundation.ID
	DocumentID  foundation.ID
	Cursor      string
	Limit       int
}

// CompareQuery identifies two readable versions of one Document path.
type CompareQuery struct {
	WorkspaceID foundation.ID
	DocumentID  foundation.ID
	Left        string
	Right       string
}

// PreviewCommand requests a server-derived restore preview for one current-branch commit.
type PreviewCommand struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	TargetCommit            string
	ExpectedDocumentVersion int64
}

// RestoreCommand creates or replays one typed restore Proposal.
type RestoreCommand struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	TargetCommit            string
	ExpectedHead            string
	ExpectedDocumentVersion int64
	PreviewHash             string
	IdempotencyKey          string
}

// Clock is retained as a narrow test seam for future history projections.
type Clock interface{ Now() time.Time }
