package application

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

const deterministicScorerVersion = "review-deterministic/v1"

// DeterministicScorer is the built-in trusted scorer. It derives every score
// field from server-owned answer points and card evidence, never client JSON.
// It is deliberately simple and reproducible while a model-backed adapter is
// deferred behind the same Scorer port.
type DeterministicScorer struct{}

var _ Scorer = DeterministicScorer{}

// NewDeterministicScorer returns the frozen first-party scoring implementation.
func NewDeterministicScorer() DeterministicScorer { return DeterministicScorer{} }

// Version identifies the fixed scoring rules used by this implementation.
func (DeterministicScorer) Version() string { return deterministicScorerVersion }

// Score evaluates coverage against the card's server-owned answer points.
func (DeterministicScorer) Score(ctx context.Context, input ScoreInput) (domain.Score, error) {
	if err := ctx.Err(); err != nil {
		return domain.Score{}, err
	}
	if err := domain.ValidateCard(input.Card); err != nil {
		return domain.Score{}, err
	}
	if input.Rating < domain.RatingAgain || input.Rating > domain.RatingEasy ||
		len(input.UserAnswer) > domain.MaxUserAnswerBytes || !utf8.ValidString(input.UserAnswer) || strings.ContainsRune(input.UserAnswer, '\x00') {
		return domain.Score{}, errors.New("deterministic scorer input is invalid")
	}

	normalizedAnswer := normalizeScoreText(input.UserAnswer)
	coverageTotal := 0.0
	omissions := make([]string, 0)
	for _, point := range input.Card.AnswerPoints {
		coverage := answerPointCoverage(normalizedAnswer, point)
		coverageTotal += coverage
		if coverage < 1 {
			omissions = append(omissions, "未覆盖答案要点："+point)
		}
	}
	coverage := coverageTotal / float64(len(input.Card.AnswerPoints))
	if normalizedAnswer == "" {
		coverage = 0
	}

	clarity := scoreClarity(input.UserAnswer)
	boundaries := scoreBoundaries(normalizedAnswer, coverage)
	errorsList := make([]string, 0, 1)
	if normalizedAnswer == "" {
		errorsList = append(errorsList, "未提供可评分回答")
	} else if coverage == 0 {
		errorsList = append(errorsList, "回答未匹配任何已验证答案要点")
	}

	score := domain.Score{
		SchemaVersion: domain.ScoreSchemaVersionV1,
		Correctness: domain.ScoreDimension{
			Value:     coverage,
			Rationale: scoreRationale("正确性", coverage),
		},
		Coverage: domain.ScoreDimension{
			Value:     coverage,
			Rationale: scoreRationale("覆盖度", coverage),
		},
		Boundaries: domain.ScoreDimension{
			Value:     boundaries,
			Rationale: scoreRationale("边界说明", boundaries),
		},
		Clarity: domain.ScoreDimension{
			Value:     clarity,
			Rationale: scoreRationale("表达清晰度", clarity),
		},
		Confidence: domain.ScoreDimension{
			Value:     ratingConfidence(input.Rating),
			Rationale: "由用户提交的自评难度映射，未参与正确性计算",
		},
		Errors:    errorsList,
		Omissions: omissions,
		Evidence:  append([]domain.EvidenceBinding(nil), input.Card.Evidence...),
	}
	if err := domain.ValidateScore(score); err != nil {
		return domain.Score{}, err
	}
	return score, nil
}

func answerPointCoverage(normalizedAnswer, point string) float64 {
	normalizedPoint := normalizeScoreText(point)
	if normalizedAnswer == "" || normalizedPoint == "" {
		return 0
	}
	if strings.Contains(normalizedAnswer, normalizedPoint) {
		return 1
	}
	wanted := scoreTokenSet(normalizedPoint)
	if len(wanted) == 0 {
		return 0
	}
	actual := scoreTokenSet(normalizedAnswer)
	matched := 0
	for token := range wanted {
		if _, ok := actual[token]; ok {
			matched++
		}
	}
	return float64(matched) / float64(len(wanted))
}

func normalizeScoreText(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			builder.WriteRune(character)
		} else {
			builder.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func scoreTokenSet(value string) map[string]struct{} {
	tokens := strings.Fields(value)
	result := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if token != "" {
			result[token] = struct{}{}
		}
	}
	return result
}

func scoreClarity(answer string) float64 {
	characters := utf8.RuneCountInString(strings.TrimSpace(answer))
	if characters == 0 {
		return 0
	}
	return math.Min(1, 0.2+float64(characters)/40)
}

func scoreBoundaries(normalizedAnswer string, coverage float64) float64 {
	if normalizedAnswer == "" {
		return 0
	}
	value := coverage * 0.8
	for _, marker := range []string{"仅", "除非", "如果", "当", "only", "unless", "if", "when"} {
		if strings.Contains(normalizedAnswer, marker) {
			value += 0.2
			break
		}
	}
	return math.Min(1, value)
}

func ratingConfidence(rating domain.Rating) float64 {
	return float64(rating) / float64(domain.RatingEasy)
}

func scoreRationale(label string, value float64) string {
	if value >= 0.99 {
		return label + "由全部服务器答案要点覆盖"
	}
	if value == 0 {
		return label + "未从用户回答中匹配到服务器答案要点"
	}
	return label + "由服务器答案要点的部分覆盖计算"
}
