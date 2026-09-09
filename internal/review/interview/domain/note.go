package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// QuestionSourceKind separates formal Claim evidence from a published note's
// original sources. The empty value remains the historical CLAIM wire format.
type QuestionSourceKind string

const (
	QuestionSourceClaim        QuestionSourceKind = "CLAIM"
	QuestionSourceNoteRevision QuestionSourceKind = "NOTE_REVISION"
	MaxNoteFollowUps                              = 3
)

// NoteRevisionRef freezes the exact published owner identity. It is not a Claim
// and does not assert that raw source spans passed the formal Citation verifier.
type NoteRevisionRef struct {
	WorkspaceID       foundation.ID `json:"workspace_id"`
	NoteID            foundation.ID `json:"note_id"`
	RevisionID        foundation.ID `json:"revision_id"`
	RevisionNo        int64         `json:"revision_no"`
	DocumentID        foundation.ID `json:"document_id"`
	ArticleRevisionID foundation.ID `json:"article_revision_id"`
	ArticleRevisionNo int64         `json:"article_revision_no"`
	ContentHash       string        `json:"content_hash"`
	ProjectionHash    string        `json:"projection_hash"`
	Title             string        `json:"title"`
}

func (ref NoteRevisionRef) Validate() error {
	if !validID(ref.WorkspaceID) || !validID(ref.NoteID) || !validID(ref.RevisionID) || !validID(ref.DocumentID) || !validID(ref.ArticleRevisionID) ||
		ref.RevisionNo < 1 || ref.ArticleRevisionNo < 1 || !validHash(ref.ContentHash) || !validHash(ref.ProjectionHash) || !validRequiredText(ref.Title, 512) {
		return invalid(ErrorCodeEvidenceInvalid, "interview note revision binding is invalid")
	}
	return nil
}

// NoteRevisionFromSnapshot accepts only an already owner-verified snapshot.
func NoteRevisionFromSnapshot(snapshot organizingdomain.SynthesisNoteSnapshot) (NoteRevisionRef, error) {
	if err := snapshot.Validate(); err != nil {
		return NoteRevisionRef{}, err
	}
	ref := NoteRevisionRef{WorkspaceID: snapshot.WorkspaceID, NoteID: snapshot.NoteID, RevisionID: snapshot.RevisionID, RevisionNo: snapshot.RevisionNo,
		DocumentID: snapshot.DocumentID, ArticleRevisionID: snapshot.ArticleRevisionID, ArticleRevisionNo: snapshot.ArticleRevisionNo,
		ContentHash: snapshot.ContentHash, ProjectionHash: snapshot.ProjectionHash, Title: snapshot.Title}
	return ref, ref.Validate()
}

// NoteQuestionSource binds one factual/conflict/gap item to its retained raw
// source tuples; current note changes never rewrite this frozen material.
type NoteQuestionSource struct {
	Revision NoteRevisionRef                       `json:"revision"`
	ItemID   foundation.ID                         `json:"item_id"`
	ItemKind organizingdomain.SynthesisItemKind    `json:"item_kind"`
	Sources  []organizingdomain.SynthesisSourceRef `json:"sources"`
}

func (source NoteQuestionSource) Validate(workspaceID foundation.ID) error {
	if source.Revision.Validate() != nil || source.Revision.WorkspaceID != workspaceID || !validID(source.ItemID) || len(source.Sources) > organizingdomain.MaxSynthesisSources ||
		(source.ItemKind != organizingdomain.SynthesisFactItem && source.ItemKind != organizingdomain.SynthesisConflictItem && source.ItemKind != organizingdomain.SynthesisGapItem) {
		return invalid(ErrorCodeEvidenceInvalid, "interview note item binding is invalid")
	}
	if source.ItemKind != organizingdomain.SynthesisGapItem && len(source.Sources) == 0 {
		return invalid(ErrorCodeEvidenceInvalid, "interview note item has no original sources")
	}
	for _, ref := range source.Sources {
		if err := ref.Validate(); err != nil || ref.Source.WorkspaceID != workspaceID {
			return invalid(ErrorCodeEvidenceInvalid, "interview original source is invalid")
		}
	}
	return nil
}

// NoteFollowUpCondition is a closed selection policy over the existing scorer.
// It is generated in advance; it never executes model supplied code or rules.
type NoteFollowUpCondition string

const (
	NoteFollowUpLowCoverage    NoteFollowUpCondition = "LOW_COVERAGE"
	NoteFollowUpLowCorrectness NoteFollowUpCondition = "LOW_CORRECTNESS"
	NoteFollowUpLowBoundaries  NoteFollowUpCondition = "LOW_BOUNDARIES"
)

type NoteFollowUp struct {
	Condition    NoteFollowUpCondition `json:"condition"`
	Prompt       string                `json:"prompt"`
	AnswerPoints []string              `json:"answer_points"`
}

func (followUp NoteFollowUp) Validate() error {
	if (followUp.Condition != NoteFollowUpLowCoverage && followUp.Condition != NoteFollowUpLowCorrectness && followUp.Condition != NoteFollowUpLowBoundaries) ||
		!validRequiredText(followUp.Prompt, maxQuestionBytes) || len(followUp.AnswerPoints) == 0 || len(followUp.AnswerPoints) > maxAnswerPoints || !validUniqueText(followUp.AnswerPoints, maxAnswerPointBytes) {
		return invalid(ErrorCodeQuestionInvalid, "interview note follow-up plan is invalid")
	}
	return nil
}

func (followUp NoteFollowUp) Matches(score Score) bool {
	switch followUp.Condition {
	case NoteFollowUpLowCoverage:
		return score.Coverage.Value < 0.65
	case NoteFollowUpLowCorrectness:
		return score.Correctness.Value < 0.65
	case NoteFollowUpLowBoundaries:
		return score.Boundaries.Value < 0.65
	default:
		return false
	}
}

func validateQuestionSource(question Question) error {
	if question.SourceKind != QuestionSourceNoteRevision {
		if (question.SourceKind != "" && question.SourceKind != QuestionSourceClaim) || !validID(question.ClaimID) || !validIDPtr(question.TopicID) ||
			question.NoteSource != nil || len(question.FollowUpPlan) != 0 || len(question.Evidence) == 0 {
			return invalid(ErrorCodeEvidenceInvalid, "interview claim source union is invalid")
		}
		return nil
	}
	if question.ClaimID != "" || question.TopicID != nil || len(question.Evidence) != 0 || question.NoteSource == nil || question.NoteSource.Validate(question.WorkspaceID) != nil ||
		len(question.FollowUpPlan) > MaxNoteFollowUps || question.FollowUpNo > len(question.FollowUpPlan) {
		return invalid(ErrorCodeEvidenceInvalid, "interview note source union is invalid")
	}
	for _, followUp := range question.FollowUpPlan {
		if err := followUp.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ValidateScoreForQuestion preserves the Claim-only verifier and uses a separate
// exact binding for note scores, including gaps with no original source span.
func ValidateScoreForQuestion(score Score, question Question) error {
	if question.SourceKind != QuestionSourceNoteRevision {
		return ValidateScore(score, question.Evidence)
	}
	if validateQuestionSource(question) != nil || score.SchemaVersion != ScoreSchemaVersion || len(score.Evidence) != 0 ||
		!SameNoteSource(score.NoteSource, question.NoteSource) || len(score.Errors) > maxFeedbackItems || len(score.Omissions) > maxFeedbackItems ||
		!validUniqueText(score.Errors, maxFeedbackItemBytes) || !validUniqueText(score.Omissions, maxFeedbackItemBytes) {
		return invalid(ErrorCodeScoreInvalid, "interview note score binding is invalid")
	}
	for _, dimension := range []ScoreDimension{score.Correctness, score.Coverage, score.Boundaries, score.Clarity} {
		if invalidNumber(dimension.Value) || dimension.Value < 0 || dimension.Value > 1 || !validRequiredText(dimension.Rationale, maxFeedbackItemBytes) {
			return invalid(ErrorCodeScoreInvalid, "interview note score dimension is invalid")
		}
	}
	return nil
}

func SameNoteSource(left, right *NoteQuestionSource) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Revision != right.Revision || left.ItemID != right.ItemID || left.ItemKind != right.ItemKind || len(left.Sources) != len(right.Sources) {
		return false
	}
	for index := range left.Sources {
		if left.Sources[index] != right.Sources[index] {
			return false
		}
	}
	return true
}

func CloneNoteSource(source *NoteQuestionSource) *NoteQuestionSource {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.Sources = append([]organizingdomain.SynthesisSourceRef{}, source.Sources...)
	return &cloned
}

func CloneNoteFollowUps(plan []NoteFollowUp) []NoteFollowUp {
	if plan == nil {
		return nil
	}
	cloned := append([]NoteFollowUp{}, plan...)
	for index := range cloned {
		cloned[index].AnswerPoints = append([]string(nil), cloned[index].AnswerPoints...)
	}
	return cloned
}

func ComputeNoteQuestionFingerprint(config Config, source NoteQuestionSource, prompt string, points []string, plan []NoteFollowUp, questionNo, followUpNo int) (string, error) {
	if ValidateConfig(config) != nil || config.Scope.NoteRevision == nil || *config.Scope.NoteRevision != source.Revision || source.Validate(source.Revision.WorkspaceID) != nil ||
		!validRequiredText(prompt, maxQuestionBytes) || len(points) == 0 || !validUniqueText(points, maxAnswerPointBytes) || questionNo < 1 || followUpNo < 0 || len(plan) > MaxNoteFollowUps {
		return "", invalid(ErrorCodeQuestionInvalid, "interview note question fingerprint input is invalid")
	}
	encoded, err := json.Marshal(struct {
		Config     Config             `json:"config"`
		Source     NoteQuestionSource `json:"source"`
		Prompt     string             `json:"prompt"`
		Points     []string           `json:"points"`
		Plan       []NoteFollowUp     `json:"plan"`
		QuestionNo int                `json:"question_no"`
		FollowUpNo int                `json:"follow_up_no"`
	}{config, source, prompt, points, plan, questionNo, followUpNo})
	if err != nil {
		return "", invalid(ErrorCodeQuestionInvalid, "interview note question cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateNoteFinding(finding Finding) error {
	if finding.SourceKind != QuestionSourceNoteRevision || finding.ClaimID != "" || finding.TopicID != nil || len(finding.Evidence) != 0 || finding.NoteSource == nil ||
		finding.NoteSource.Validate(finding.NoteSource.Revision.WorkspaceID) != nil || !validRequiredText(finding.Detail, maxPathRationaleBytes) {
		return invalid(ErrorCodeReportInvalid, "interview note finding is invalid")
	}
	return nil
}

func validateReportNoteSources(report Report) error {
	if len(report.NoteSources) > maxQuestionCount {
		return invalid(ErrorCodeReportInvalid, "interview report has too many note sources")
	}
	for _, source := range report.NoteSources {
		if source.Validate(report.WorkspaceID) != nil || len(report.Evidence) != 0 {
			return invalid(ErrorCodeReportInvalid, "interview report note source is invalid")
		}
	}
	for _, group := range [][]Finding{report.Strengths, report.Gaps, report.Expression} {
		for _, finding := range group {
			if len(report.NoteSources) == 0 {
				if finding.NoteSource != nil {
					return invalid(ErrorCodeReportInvalid, "interview note finding has no report source")
				}
				continue
			}
			matched := false
			for _, source := range report.NoteSources {
				if SameNoteSource(&source, finding.NoteSource) {
					matched = true
					break
				}
			}
			if !matched {
				return invalid(ErrorCodeReportInvalid, "interview note finding source drifted")
			}
		}
	}
	return nil
}

func validateNotePathStepSource(step PathStep) error {
	if step.SourceKind != QuestionSourceNoteRevision || step.ClaimID != "" || step.TopicID != nil || step.SourceVersionID != "" || step.SourceSpanID != "" || step.EvidenceHash != "" ||
		step.NoteSource == nil || step.NoteSource.Validate(step.WorkspaceID) != nil {
		return invalid(ErrorCodePathInvalid, "interview note learning step source is invalid")
	}
	return nil
}
