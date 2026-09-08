package postgres

import (
	"errors"
	"regexp"
	"strings"
	"time"

	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	maxStartLease    = 24 * time.Hour
	maxStartAttempts = 1000
)

var runtimeErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func validateStartLease(lease organizingapp.StartOutboxLease) error {
	if !validID(lease.ID) || !validID(lease.WorkspaceID) || !validID(lease.SnapshotID) ||
		!validID(lease.TemplateRevisionID) || !lease.TemplateKind.Valid() || strings.TrimSpace(lease.Owner) == "" ||
		len(lease.Owner) > 256 || strings.ContainsAny(lease.Owner, "\r\n\x00") || lease.AttemptCount < 1 ||
		lease.Version < 2 || lease.LeaseUntil.IsZero() {
		return errors.New("organizing start outbox lease is invalid")
	}
	return nil
}

func validRuntimeErrorCode(value string) bool {
	return value == strings.TrimSpace(value) && runtimeErrorCodePattern.MatchString(value)
}

func sameRunResultRequest(left, right domain.RunResult) bool {
	return left.WorkspaceID == right.WorkspaceID && left.RunBindingID == right.RunBindingID &&
		left.SnapshotID == right.SnapshotID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.Kind == right.Kind && left.ResultRef == right.ResultRef &&
		left.ResultHash == right.ResultHash
}

func definitionForTemplateKind(kind domain.TemplateKind) (string, int64) {
	switch kind {
	case domain.TemplateTopicArticle:
		return "organizing.topic-article", 1
	case domain.TemplateMergeDocuments:
		return "organizing.merge-documents", 1
	case domain.TemplateKnowledgeReport:
		return "organizing.knowledge-report", 1
	case domain.TemplateInterviewReview:
		return "organizing.interview-review", 1
	default:
		return "", 0
	}
}
