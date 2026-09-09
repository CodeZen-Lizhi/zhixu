package application

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

// DeterministicScorer 是不依赖模型 Provider 的证据受限评分器。
// 它只比对 Question 的冻结 AnswerPoints 与用户回答，绝不生成新的知识结论。
type DeterministicScorer struct{}

// Version 返回固定评分器版本，供 Turn 和报告可解释性使用。
func (DeterministicScorer) Version() string { return "interview-deterministic/v2" }

// Score 从冻结答案要点计算覆盖、正确性、边界与表达评分。
func (DeterministicScorer) Score(ctx context.Context, input ScoreInput) (domain.Score, error) {
	if err := ctx.Err(); err != nil {
		return domain.Score{}, domain.UnavailableError(domain.ErrorCodeScorerUnavailable, "interview scoring context is unavailable")
	}
	if err := domain.ValidateQuestion(input.Question); err != nil {
		return domain.Score{}, err
	}
	answerTerms := termSet(input.UserAnswer)
	expected := expectedTerms(input.Question.AnswerPoints)
	matched, missing := matchedTerms(expected, answerTerms)
	coverage := ratio(len(matched), len(expected))
	correctness := coverage
	if strings.TrimSpace(input.UserAnswer) == "" {
		correctness = 0
	}
	boundaries := boundaryScore(input.UserAnswer)
	clarity := clarityScore(input.UserAnswer)
	if input.Question.NoteSource != nil && len(input.Question.AnswerPoints) == 1 && input.Question.AnswerPoints[0] == UnresolvedNoteGapPoint && acknowledgesMissingNoteEvidence(input.UserAnswer) {
		correctness, coverage, boundaries = 1, 1, 1
		missing = nil
	}
	feedback := scorerFeedbackFor(personalizationFrom(input.PersonalContext))
	if input.Question.NoteSource != nil {
		feedback.correctness = "Matched frozen note answer points using deterministic term rules; no model grading was performed."
		feedback.emptyAnswer = "No answer was provided for the frozen note item."
	}
	errors := make([]string, 0, 1)
	if strings.TrimSpace(input.UserAnswer) == "" {
		errors = append(errors, feedback.emptyAnswer)
	}
	omissions := make([]string, 0, min(len(missing), 8))
	for _, term := range missing[:min(len(missing), 8)] {
		omissions = append(omissions, feedback.missingPrefix+term)
	}
	score := domain.Score{
		SchemaVersion: domain.ScoreSchemaVersion,
		Correctness:   dimension(correctness, feedback.correctness),
		Coverage:      dimension(coverage, feedback.coverage),
		Boundaries:    dimension(boundaries, feedback.boundaries),
		Clarity:       dimension(clarity, feedback.clarity),
		Errors:        errors,
		Omissions:     omissions,
		Evidence:      append([]domain.EvidenceRef(nil), input.Question.Evidence...),
		NoteSource:    domain.CloneNoteSource(input.Question.NoteSource),
	}
	if input.Question.NoteSource != nil {
		score.Evidence = []domain.EvidenceRef{}
	}
	if err := domain.ValidateScoreForQuestion(score, input.Question); err != nil {
		return domain.Score{}, err
	}
	return score, nil
}

type answerFeedbackStyle uint8

const (
	answerFeedbackDefault answerFeedbackStyle = iota
	answerFeedbackConcise
)

type practiceFocus uint8

const (
	practiceFocusDefault practiceFocus = iota
	practiceFocusBoundaries
)

type scorerPersonalization struct {
	answerStyle answerFeedbackStyle
	focus       practiceFocus
}

type scorerFeedback struct {
	correctness   string
	coverage      string
	boundaries    string
	clarity       string
	emptyAnswer   string
	missingPrefix string
}

func personalizationFrom(context PersonalContext) scorerPersonalization {
	profile := scorerPersonalization{}
	for _, document := range context.Preferences {
		value, ok := singleStringDocument(document)
		if ok && value["answer_style"] == "concise" {
			profile.answerStyle = answerFeedbackConcise
		}
	}
	for _, document := range context.Context {
		value, ok := singleStringDocument(document)
		if ok && value["goal"] == "practice-boundaries" {
			profile.focus = practiceFocusBoundaries
		}
	}
	return profile
}

func singleStringDocument(document []byte) (map[string]string, bool) {
	value, err := strictjson.DecodeObject[map[string]string](document, strictjson.Limits{
		MaxDocumentBytes: maxContextEntryBytes,
		MaxDepth:         2,
		MaxStringBytes:   4096,
		MaxArrayItems:    0,
		MaxObjectFields:  2,
	}, nil)
	return value, err == nil && len(value) == 1
}

func scorerFeedbackFor(profile scorerPersonalization) scorerFeedback {
	feedback := scorerFeedback{
		correctness:   "Matched frozen claim answer points.",
		coverage:      "Measured coverage of frozen answer-point terms.",
		boundaries:    "Detected explicit conditions or limits in the answer.",
		clarity:       "Measured basic answer structure without model inference.",
		emptyAnswer:   "No answer was provided for the evidence-backed claim.",
		missingPrefix: "Missing coverage for: ",
	}
	if profile.answerStyle == answerFeedbackConcise {
		feedback.correctness = "Frozen answer-point match."
		feedback.coverage = "Frozen term coverage."
		feedback.boundaries = "Explicit condition coverage."
		feedback.clarity = "Basic answer structure."
		feedback.emptyAnswer = "No answer provided."
		feedback.missingPrefix = "Missing: "
	}
	if profile.focus == practiceFocusBoundaries {
		feedback.boundaries = "Boundary practice focused on explicit conditions and limits."
	}
	return feedback
}

func expectedTerms(points []string) []string {
	values := make(map[string]struct{})
	for _, point := range points {
		for term := range termSet(point) {
			values[term] = struct{}{}
		}
	}
	terms := make([]string, 0, len(values))
	for term := range values {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

func termSet(value string) map[string]struct{} {
	values := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len([]rune(token)) < 2 && !containsHan(token) {
			continue
		}
		if _, stop := scorerStopWords[token]; !stop {
			values[token] = struct{}{}
		}
	}
	return values
}

func matchedTerms(expected []string, answer map[string]struct{}) ([]string, []string) {
	matched := make([]string, 0, len(expected))
	missing := make([]string, 0, len(expected))
	for _, term := range expected {
		if _, found := answer[term]; found {
			matched = append(matched, term)
		} else {
			missing = append(missing, term)
		}
	}
	return matched, missing
}

func boundaryScore(answer string) float64 {
	lower := strings.ToLower(answer)
	for _, marker := range []string{"if ", "when ", "unless ", "only ", "depends", "condition", "limit", "边界", "条件", "限制", "除非", "仅当"} {
		if strings.Contains(lower, marker) {
			return 1
		}
	}
	if strings.TrimSpace(answer) == "" {
		return 0
	}
	return 0.4
}

func clarityScore(answer string) float64 {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return 0
	}
	words := len(termSet(trimmed))
	value := 0.35
	if words >= 3 {
		value += 0.3
	}
	if strings.ContainsAny(trimmed, ".!?。！？;；") {
		value += 0.2
	}
	if len([]rune(trimmed)) >= 40 {
		value += 0.15
	}
	if value > 1 {
		return 1
	}
	return value
}

func dimension(value float64, rationale string) domain.ScoreDimension {
	return domain.ScoreDimension{Value: value, Rationale: rationale}
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func containsHan(value string) bool {
	for _, runeValue := range value {
		if unicode.Is(unicode.Han, runeValue) {
			return true
		}
	}
	return false
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var scorerStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "as": {}, "at": {}, "be": {}, "by": {}, "for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {}, "that": {}, "the": {}, "to": {}, "with": {},
}

// This is an explicit vocabulary rule, not semantic or model grading.
func acknowledgesMissingNoteEvidence(answer string) bool {
	lower := strings.ToLower(strings.TrimSpace(answer))
	for _, marker := range []string{"不足以判断", "无法判断", "不能判断", "需补充资料", "需要补充资料", "证据不足", "insufficient evidence", "not enough evidence", "cannot determine", "need more information"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
