package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestProposalRiskLevelContractIsExactAndKnowledgeChangeIsHigh(t *testing.T) {
	for _, level := range []ProposalRiskLevel{ProposalRiskLevelCritical, ProposalRiskLevelHigh, ProposalRiskLevelMedium, ProposalRiskLevelLow} {
		if parsed, err := ValidateProposalRiskLevelForType(ProposalTypeFilePatch, level); err != nil || parsed != level {
			t.Fatalf("file patch risk level %q parsed=%q err=%v", level, parsed, err)
		}
	}
	for _, level := range []ProposalRiskLevel{"", "high", " HIGH ", "UNKNOWN"} {
		if _, err := ValidateProposalRiskLevelForType(ProposalTypeFilePatch, level); !errors.Is(err, ErrProposalRiskLevelInvalid) {
			t.Fatalf("file patch risk level %q error=%v", level, err)
		}
	}
	if parsed, err := ValidateProposalRiskLevelForType(ProposalTypeKnowledgeChange, ProposalRiskLevelHigh); err != nil || parsed != ProposalRiskLevelHigh {
		t.Fatalf("knowledge risk level parsed=%q err=%v", parsed, err)
	}
	for _, level := range []ProposalRiskLevel{ProposalRiskLevelCritical, ProposalRiskLevelMedium, ProposalRiskLevelLow} {
		if _, err := ValidateProposalRiskLevelForType(ProposalTypeKnowledgeChange, level); !errors.Is(err, ErrProposalRiskLevelInvalid) {
			t.Fatalf("knowledge risk level %q error=%v", level, err)
		}
	}
	if _, err := ValidateProposalRiskLevelForType(ProposalType("unknown"), ProposalRiskLevelHigh); !errors.Is(err, ErrProposalTypeInvalid) {
		t.Fatalf("unknown proposal type error=%v", err)
	}
}

func TestFilePatchRequestHashV1GoldenAndV2RiskLevelBinding(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	baseHash := strings.Repeat("a", 64)
	v1 := ComputeRequestHash(workspaceID, "docs/a.md", baseHash, "first\r\nsecond\n", "verified evidence", "low", "restore file")
	const wantV1 = "3718c0e5d5d7a50bce686bb43d5c58a559c4a1bb46095b72e4de378ddad6251d"
	if v1 != wantV1 {
		t.Fatalf("file patch v1 request hash = %s, want %s", v1, wantV1)
	}

	low, err := ComputeRequestHashWithRiskLevel(workspaceID, "docs/a.md", baseHash, "first\r\nsecond\n", "verified evidence", ProposalRiskLevelLow, "low", "restore file")
	if err != nil {
		t.Fatal(err)
	}
	high, err := ComputeRequestHashWithRiskLevel(workspaceID, "docs/a.md", baseHash, "first\r\nsecond\n", "verified evidence", ProposalRiskLevelHigh, "low", "restore file")
	if err != nil {
		t.Fatal(err)
	}
	if low == high || low == v1 || high == v1 {
		t.Fatalf("file patch request hashes do not bind schema and risk level: v1=%s low=%s high=%s", v1, low, high)
	}
}
