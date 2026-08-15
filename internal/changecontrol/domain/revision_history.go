package domain

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const MaxProposalRevisionHistoryLimit = 100

var ErrProposalRevisionHistoryQueryInvalid = errors.New("proposal revision history query is invalid")

// ProposalRevisionHistoryQuery uses revision_no as a stable descending cursor.
// BeforeRevisionNo is exclusive; callers request one extra row to derive more.
type ProposalRevisionHistoryQuery struct {
	ProposalID       foundation.ID
	Limit            int
	BeforeRevisionNo *int
}

// ProposalRevisionHistoryItem is the bounded list projection. Revision content
// and base snapshot bytes are intentionally available only from detail.
type ProposalRevisionHistoryItem struct {
	ProposalID    foundation.ID
	RevisionID    foundation.ID
	RevisionNo    int
	TargetPath    string
	TargetMode    TargetMode
	BaseHash      string
	ChangeHash    string
	BaseAvailable bool
	Current       bool
	Approval      *Approval
	WorkflowRunID *foundation.ID
	CreatedAt     time.Time
}

// ProposalRevisionHistoryDetail returns one exact immutable Revision together
// with its Approval, Workflow dispatch and optional trustworthy base snapshot.
type ProposalRevisionHistoryDetail struct {
	WorkspaceID       foundation.ID
	ProposalID        foundation.ID
	CurrentRevisionID foundation.ID
	Revision          Revision
	Approval          *Approval
	WorkflowRunID     *foundation.ID
}

// ProposalRevisionHistoryRepository owns exact historical reads. It does not
// infer execution identity from the maximum revision number.
type ProposalRevisionHistoryRepository interface {
	ListProposalRevisions(context.Context, ProposalRevisionHistoryQuery) ([]ProposalRevisionHistoryItem, bool, error)
	GetProposalRevision(context.Context, foundation.ID, foundation.ID) (ProposalRevisionHistoryDetail, error)
}

func ValidateProposalRevisionHistoryQuery(query ProposalRevisionHistoryQuery) error {
	if query.ProposalID == "" || query.Limit < 1 || query.Limit > MaxProposalRevisionHistoryLimit {
		return ErrProposalRevisionHistoryQueryInvalid
	}
	if query.BeforeRevisionNo != nil && *query.BeforeRevisionNo <= 1 {
		return ErrProposalRevisionHistoryQueryInvalid
	}
	return nil
}
