package application

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func (s *Service) newNoteFollowUp(session domain.Session, parent domain.Question, score domain.Score, now time.Time) (*domain.Question, error) {
	// Each planned step is a conditional continuation of this question chain.
	// A condition that is satisfied by the answer ends the chain instead of
	// inserting a generic question. FollowUpNo remains contiguous and durable.
	if index := parent.FollowUpNo; index < len(parent.FollowUpPlan) {
		planned := parent.FollowUpPlan[index]
		if !planned.Matches(score) {
			return nil, nil
		}
		id, err := s.newID()
		if err != nil {
			return nil, err
		}
		parentID := parent.ID
		followUp := &domain.Question{ID: id, WorkspaceID: session.WorkspaceID, SessionID: session.ID,
			QuestionNo: parent.QuestionNo, FollowUpNo: index + 1, ParentQuestionID: &parentID,
			SourceKind: domain.QuestionSourceNoteRevision, NoteSource: domain.CloneNoteSource(parent.NoteSource), FollowUpPlan: domain.CloneNoteFollowUps(parent.FollowUpPlan),
			Prompt: planned.Prompt, AnswerPoints: append([]string(nil), planned.AnswerPoints...), Evidence: []domain.EvidenceRef{}, Status: domain.QuestionStatusPending, CreatedAt: now}
		followUp.Fingerprint, err = domain.ComputeNoteQuestionFingerprint(session.Config, *followUp.NoteSource, followUp.Prompt, followUp.AnswerPoints, followUp.FollowUpPlan, followUp.QuestionNo, followUp.FollowUpNo)
		if err != nil {
			return nil, err
		}
		return followUp, domain.ValidateQuestion(*followUp)
	}
	return nil, nil
}

func findingForQuestion(question domain.Question, detail string) domain.Finding {
	finding := domain.Finding{ClaimID: question.ClaimID, TopicID: cloneID(question.TopicID), Detail: detail, Evidence: append([]domain.EvidenceRef(nil), question.Evidence...)}
	if question.NoteSource != nil {
		finding.SourceKind, finding.NoteSource = domain.QuestionSourceNoteRevision, domain.CloneNoteSource(question.NoteSource)
		finding.Evidence = []domain.EvidenceRef{}
		finding.Detail = strings.ReplaceAll(strings.ReplaceAll(detail, "claim", "note item"), "supporting evidence", "original sources")
	}
	return finding
}

func noteDocuments(sources []domain.NoteQuestionSource) []domain.NoteRevisionRef {
	if len(sources) == 0 {
		return nil
	}
	return []domain.NoteRevisionRef{sources[0].Revision}
}

// Only server generated UUID/hash tuples enter these URLs; no model URL or
// Markdown is interpolated. Source text is fetched on demand by the note owner.
func writeNoteSourceLinks(body *strings.Builder, source domain.NoteQuestionSource) {
	ref := source.Revision
	base := "/authoring/notes/" + string(ref.NoteID)
	fmt.Fprintf(body, "  - [Frozen note revision %d](%s?revision_id=%s)\n", ref.RevisionNo, base, ref.RevisionID)
	for _, original := range source.Sources {
		query := url.Values{"revision_id": {string(ref.RevisionID)}, "source_id": {string(original.Source.SourceID)}, "source_version_id": {string(original.Source.SourceVersionID)},
			"content_artifact_id": {string(original.Source.ContentArtifactID)}, "parse_projection_id": {string(original.Source.ParseProjectionID)}, "source_span_id": {string(original.SourceSpanID)},
			"content_hash": {original.Source.ContentHash}, "excerpt_hash": {original.ExcerptHash}}
		fmt.Fprintf(body, "  - [Original source](%s?%s)\n", base, query.Encode())
	}
}
