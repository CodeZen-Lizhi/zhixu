package workflow

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func decodeFinalReceipt(raw []byte) (finalReceipt, error) {
	receipt, err := decodeObject(raw, func(value finalReceipt) error {
		if value.SchemaVersion != receiptSchemaV1 || !validID(value.ResultID) || !validID(value.RunBindingID) ||
			!validID(value.SnapshotID) || (value.Kind != organizingdomain.ResultArtifact && value.Kind != organizingdomain.ResultMergeProposal) ||
			!validID(value.ResultRef) || !validHash(value.ResultHash) {
			return terminalInvalid(errors.New("organizing terminal receipt is invalid"))
		}
		return nil
	})
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Code == organizingapp.ErrorCodeResultInvalid {
			return finalReceipt{}, err
		}
		return finalReceipt{}, terminalInvalid(errors.New("organizing terminal output does not match the result receipt schema"))
	}
	return receipt, nil
}

func terminalContract(nodeKind string) (string, organizingdomain.ResultKind, bool) {
	switch nodeKind {
	case TopicArtifactNodeKind:
		return TopicArticleDefinitionKey, organizingdomain.ResultArtifact, true
	case MergeProposalNodeKind:
		return MergeDocumentsDefinitionKey, organizingdomain.ResultMergeProposal, true
	case KnowledgeReportNodeKind:
		return KnowledgeReportDefinitionKey, organizingdomain.ResultArtifact, true
	case InterviewReviewNodeKind:
		return InterviewReviewDefinitionKey, organizingdomain.ResultArtifact, true
	default:
		return "", "", false
	}
}

func terminalInvalid(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeResultInvalid, false, err)
}

func terminalUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, organizingapp.ErrorCodeRepositoryUnavailable, true, err)
}
