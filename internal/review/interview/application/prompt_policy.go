package application

import (
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

// initialQuestionPrompt applies the frozen difficulty without adding facts beyond
// the selected Claim and its validated Evidence binding.
func initialQuestionPrompt(config domain.Config, material domain.Material) (string, error) {
	if err := domain.ValidateConfig(config); err != nil {
		return "", err
	}
	if err := domain.ValidateMaterial(material); err != nil {
		return "", err
	}
	switch config.Difficulty {
	case domain.DifficultyFoundation:
		return fmt.Sprintf("As a %s, restate the core claim in your own words: %s", config.Role, material.Statement), nil
	case domain.DifficultyIntermediate:
		return fmt.Sprintf("As a %s, explain this claim and the conditions under which it applies: %s", config.Role, material.Statement), nil
	case domain.DifficultyAdvanced:
		return fmt.Sprintf("As a %s, explain this claim and identify the boundaries or limitations supported by its existing evidence: %s", config.Role, material.Statement), nil
	default:
		return "", domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview difficulty is invalid")
	}
}

// followUpQuestionPrompt consumes only the latest score's gap category. It does
// not quote missing answer-point terms, so the follow-up cannot reveal an answer.
func followUpQuestionPrompt(config domain.Config, score domain.Score, followUpNo int) (string, error) {
	if err := domain.ValidateConfig(config); err != nil {
		return "", err
	}
	if followUpNo < 1 {
		return "", domain.InvalidError(domain.ErrorCodeQuestionInvalid, "interview follow-up number is invalid")
	}
	gap := scoreGapCategory(score)
	switch config.Difficulty {
	case domain.DifficultyFoundation:
		return fmt.Sprintf("Follow-up %d: %s Restate the core claim in your own words, without adding facts beyond the existing claim and evidence.", followUpNo, gap), nil
	case domain.DifficultyIntermediate:
		return fmt.Sprintf("Follow-up %d: %s Explain the applicable conditions more precisely, using only the existing claim and evidence.", followUpNo, gap), nil
	case domain.DifficultyAdvanced:
		return fmt.Sprintf("Follow-up %d: %s Explain a boundary or limitation already supported by the existing evidence, without introducing unsupported assumptions.", followUpNo, gap), nil
	default:
		return "", domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview difficulty is invalid")
	}
}

func scoreGapCategory(score domain.Score) string {
	switch {
	case len(score.Errors) > 0:
		return "The latest score identified an answer error."
	case len(score.Omissions) > 0:
		return "The latest score identified an evidence-backed omission."
	case score.Boundaries.Value < 0.65:
		return "The latest score identified a boundary gap."
	default:
		return "The latest score identified an evidence-backed coverage gap."
	}
}
