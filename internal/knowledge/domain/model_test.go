package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateTopicFreezesCanonicalAliasesAndLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	topic := Topic{
		ID: uuid(1), WorkspaceID: uuid(2), Name: "Go 语言", NormalizedName: "go 语言",
		Description: "运行时 与 工具链", Aliases: []TopicAlias{{Name: "Golang", NormalizedName: "golang"}},
		Status: TopicStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidateTopic(topic); err != nil {
		t.Fatalf("valid topic rejected: %v", err)
	}

	duplicateAlias := topic
	duplicateAlias.Aliases = append(duplicateAlias.Aliases, TopicAlias{Name: "GOLANG", NormalizedName: "golang"})
	if err := ValidateTopic(duplicateAlias); err == nil {
		t.Fatal("duplicate normalized aliases must be rejected")
	}
	merged := topic
	merged.Status = TopicStatusMerged
	if err := ValidateTopic(merged); err == nil {
		t.Fatal("merged topic without redirect must be rejected")
	}
	target := uuid(3)
	merged.MergedIntoTopicID = &target
	if err := ValidateTopic(merged); err != nil {
		t.Fatalf("merged topic with target rejected: %v", err)
	}
	activeWithTarget := topic
	activeWithTarget.MergedIntoTopicID = &target
	if err := ValidateTopic(activeWithTarget); err == nil {
		t.Fatal("active topic cannot carry merged target")
	}
}

func TestValidateTopicTransitionOnlyRetiresActiveTopic(t *testing.T) {
	allowed := map[[2]TopicStatus]bool{
		{TopicStatusActive, TopicStatusMerged}:     true,
		{TopicStatusActive, TopicStatusDeprecated}: true,
	}
	statuses := []TopicStatus{TopicStatusActive, TopicStatusMerged, TopicStatusDeprecated, TopicStatus("unknown")}
	for _, from := range statuses {
		for _, to := range statuses {
			err := ValidateTopicTransition(from, to)
			if allowed[[2]TopicStatus{from, to}] != (err == nil) {
				t.Fatalf("transition %s -> %s mismatch: %v", from, to, err)
			}
		}
	}
}

func TestClaimStateMachineCoversFrozenMatrix(t *testing.T) {
	allowed := map[[2]ClaimStatus]bool{
		{ClaimStatusSuggested, ClaimStatusConfirmed}:  true,
		{ClaimStatusSuggested, ClaimStatusInvalid}:    true,
		{ClaimStatusConfirmed, ClaimStatusDisputed}:   true,
		{ClaimStatusConfirmed, ClaimStatusSuperseded}: true,
		{ClaimStatusConfirmed, ClaimStatusDeprecated}: true,
		{ClaimStatusConfirmed, ClaimStatusInvalid}:    true,
		{ClaimStatusDisputed, ClaimStatusConfirmed}:   true,
		{ClaimStatusDisputed, ClaimStatusSuperseded}:  true,
		{ClaimStatusDisputed, ClaimStatusDeprecated}:  true,
		{ClaimStatusDisputed, ClaimStatusInvalid}:     true,
	}
	statuses := []ClaimStatus{ClaimStatusSuggested, ClaimStatusConfirmed, ClaimStatusDisputed, ClaimStatusSuperseded, ClaimStatusDeprecated, ClaimStatusInvalid, ClaimStatus("unknown")}
	for _, from := range statuses {
		for _, to := range statuses {
			err := ValidateClaimTransition(from, to)
			if allowed[[2]ClaimStatus{from, to}] != (err == nil) {
				t.Fatalf("transition %s -> %s mismatch: %v", from, to, err)
			}
		}
	}
}

func TestValidateClaimAggregateRequiresSupportingSourceForConfirmedClaim(t *testing.T) {
	claim := validClaim(t)
	claim.Status = ClaimStatusConfirmed
	if err := ValidateClaimAggregate(claim, nil); err == nil {
		t.Fatal("confirmed claim without source must fail closed")
	}
	refuting := validClaimSource(t, claim, ClaimSupportRefutes)
	if err := ValidateClaimAggregate(claim, []ClaimSource{refuting}); err == nil {
		t.Fatal("refuting source alone cannot confirm claim")
	}
	supporting := validClaimSource(t, claim, ClaimSupportSupports)
	if err := ValidateClaimAggregate(claim, []ClaimSource{refuting, supporting}); err != nil {
		t.Fatalf("supporting source should confirm claim: %v", err)
	}
}

func TestClaimFingerprintUsesFoldedStatementAndApplicability(t *testing.T) {
	app, err := ParseApplicability(json.RawMessage(`{"scope":"runtime"}`))
	if err != nil {
		t.Fatal(err)
	}
	first := ComputeClaimFingerprint(uuid(2), "Straße ist schnell", app)
	second := ComputeClaimFingerprint(uuid(2), "STRASSE IST SCHNELL", app)
	if first != second || !validSHA256(first) {
		t.Fatalf("claim fingerprint must be stable under case fold: %q %q", first, second)
	}
	if first == ComputeClaimFingerprint(uuid(3), "STRASSE IST SCHNELL", app) {
		t.Fatal("workspace must bind claim fingerprint")
	}
}

func TestValidateClaimRejectsTamperedCanonicalFieldsAndConfidence(t *testing.T) {
	claim := validClaim(t)
	claim.NormalizedStatement = "different"
	if err := ValidateClaim(claim); err == nil {
		t.Fatal("tampered normalized statement must fail")
	}
	claim = validClaim(t)
	nan := math.NaN()
	claim.ConfidenceScore = &nan
	if err := ValidateClaim(claim); err == nil {
		t.Fatal("non-finite confidence must fail")
	}
	claim = validClaim(t)
	claim.ConfidenceFactors = json.RawMessage(`{"b":1,"a":2}`)
	if err := ValidateClaim(claim); err == nil {
		t.Fatal("non-canonical confidence factors must fail")
	}
}

func TestProvenanceConfirmationAndClaimSourceFailClosed(t *testing.T) {
	claim := validClaim(t)
	source := validClaimSource(t, claim, ClaimSupportSupports)
	if err := ValidateClaimSource(source, claim.Applicability); err != nil {
		t.Fatalf("valid source rejected: %v", err)
	}
	tampered := source
	tampered.Provenance.WorkspaceID = uuid(99)
	if err := ValidateClaimSource(tampered, claim.Applicability); err == nil {
		t.Fatal("cross-workspace provenance must fail")
	}
	tampered = source
	tampered.EvidenceHash = "0" + source.EvidenceHash[1:]
	if err := ValidateClaimSource(tampered, claim.Applicability); err == nil {
		t.Fatal("tampered evidence hash must fail")
	}

	for _, confirmation := range []Confirmation{
		{},
		{Method: ConfirmationUserApproval},
		{Method: ConfirmationMethod("MODEL"), Reference: "run-1"},
	} {
		var classified *foundation.Error
		if err := ValidateConfirmation(confirmation); !errors.As(err, &classified) || classified.Code != ErrorCodeConfirmationInvalid {
			t.Fatalf("invalid confirmation %#v was accepted: %v", confirmation, err)
		}
	}
	if err := ValidateConfirmation(Confirmation{Method: ConfirmationSourceDerived, Reference: "source-derived:v1"}); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
}

func validClaim(t *testing.T) Claim {
	t.Helper()
	app, err := ParseApplicability(json.RawMessage(`{"region":"cn"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	statement, normalized, err := NormalizeStatement("Go 适用于服务端开发")
	if err != nil {
		t.Fatal(err)
	}
	claim := Claim{
		ID: uuid(10), WorkspaceID: uuid(2), Statement: statement, NormalizedStatement: normalized,
		Applicability: app, Status: ClaimStatusSuggested, ConfidenceFactors: json.RawMessage(`{}`),
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	claim.Fingerprint = ComputeClaimFingerprint(claim.WorkspaceID, claim.NormalizedStatement, claim.Applicability)
	return claim
}

func validClaimSource(t *testing.T, claim Claim, support ClaimSupportType) ClaimSource {
	t.Helper()
	source := ClaimSource{
		ID: uuid(20), WorkspaceID: claim.WorkspaceID, ClaimID: claim.ID,
		Provenance:  ProvenanceRef{WorkspaceID: claim.WorkspaceID, SourceVersionID: uuid(21), SourceSpanID: uuid(22)},
		SupportType: support, Reason: "原文直接陈述", CreatedAt: claim.CreatedAt,
	}
	source.EvidenceHash = ComputeClaimSourceEvidenceHash(source, claim.Applicability)
	return source
}

func uuid(seed int) foundation.ID {
	return foundation.ID("00000000-0000-4000-8000-" + leftPad12(seed))
}

func leftPad12(value int) string {
	const digits = "000000000000"
	text := ""
	for value > 0 {
		text = string(rune('0'+value%10)) + text
		value /= 10
	}
	if text == "" {
		text = "0"
	}
	return digits[:12-len(text)] + text
}
