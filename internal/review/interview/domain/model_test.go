package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateMaterialRequiresConfirmedClaimSupportsEvidence(t *testing.T) {
	material := testMaterial()
	if err := ValidateMaterial(material); err != nil {
		t.Fatalf("valid material rejected: %v", err)
	}
	material.Evidence[0].SupportType = "REFUTES"
	if err := ValidateMaterial(material); err == nil {
		t.Fatal("REFUTES evidence must not enter interview material")
	}
}

func TestValidateEvidenceRefRequiresFrozenIndexTuple(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvidenceRef)
	}{
		{name: "missing index version", mutate: func(value *EvidenceRef) { value.IndexVersionID = "" }},
		{name: "missing chunk", mutate: func(value *EvidenceRef) { value.ChunkID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := testMaterial().Evidence[0]
			test.mutate(&evidence)
			if err := ValidateEvidenceRef(evidence); err == nil {
				t.Fatalf("invalid frozen evidence accepted: %+v", evidence)
			}
		})
	}
}

func TestCanonicalConfigSortsScopeWithoutChangingMeaning(t *testing.T) {
	config := testConfig()
	second := testID(2)
	config.Scope.ClaimIDs = []foundation.ID{second, config.Scope.ClaimIDs[0]}
	canonical, err := CanonicalConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Scope.ClaimIDs[0] != config.Scope.ClaimIDs[1] || canonical.Scope.ClaimIDs[1] != second {
		t.Fatalf("scope was not canonicalized: %+v", canonical.Scope.ClaimIDs)
	}
}

func TestValidateReportAndLearningPathRequireArtifactBindings(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	material := testMaterial()
	report := Report{
		ID: testID(10), WorkspaceID: testID(20), SessionID: testID(30), SchemaVersion: ReportSchemaVersion,
		Summary:  ReportSummary{QuestionsTotal: 1, AnsweredTotal: 1, Correctness: 1, Coverage: 1, Boundaries: 1, Clarity: 1},
		Evidence: material.Evidence, CreatedAt: now,
	}
	if err := ValidateReport(report); err == nil {
		t.Fatal("report without an INTERVIEW_DOC binding must fail")
	}
	report.Artifact = ArtifactBinding{Kind: "INTERVIEW_DOC", ArtifactID: testID(40), RevisionID: testID(41), ArtifactVersion: 1}
	if err := ValidateReport(report); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	path := LearningPath{ID: testID(50), WorkspaceID: report.WorkspaceID, SessionID: report.SessionID, ReportID: report.ID, Status: PathStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := ValidateLearningPath(path); err == nil {
		t.Fatal("path without a LEARNING_PATH binding must fail")
	}
	path.Artifact = ArtifactBinding{Kind: "LEARNING_PATH", ArtifactID: testID(60), RevisionID: testID(61), ArtifactVersion: 1}
	if err := ValidateLearningPath(path); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
}

func TestAllPathStepsTerminal(t *testing.T) {
	testCases := []struct {
		name  string
		steps []PathStep
		want  bool
	}{
		{name: "no actionable steps", want: true},
		{name: "completed and skipped", steps: []PathStep{{Status: StepStatusCompleted}, {Status: StepStatusSkipped}}, want: true},
		{name: "pending step", steps: []PathStep{{Status: StepStatusPending}}, want: false},
		{name: "in-progress step", steps: []PathStep{{Status: StepStatusInProgress}}, want: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := AllPathStepsTerminal(testCase.steps); got != testCase.want {
				t.Fatalf("AllPathStepsTerminal() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func testConfig() Config {
	return Config{SchemaVersion: SchemaVersion, Role: "backend engineer", Scope: Scope{ClaimIDs: []foundation.ID{testID(1)}}, Difficulty: DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 1, MaxFollowUps: 1}
}

func testMaterial() Material {
	claimID := testID(1)
	return Material{ClaimID: claimID, ClaimStatus: "CONFIRMED", Statement: "A confirmed claim needs supporting evidence.", AnswerPoints: []string{"A confirmed claim needs supporting evidence."}, Evidence: []EvidenceRef{{SchemaVersion: EvidenceSchemaVersion, ClaimID: claimID, IndexVersionID: testID(5), ChunkID: testID(6), SourceVersionID: testID(3), SourceSpanID: testID(4), EvidenceHash: strings.Repeat("a", 64), SupportType: "SUPPORTS"}}}
}

func testID(value int) foundation.ID {
	parsed, err := foundation.ParseID(fmt.Sprintf("00000000-0000-4000-8000-%012x", value))
	if err != nil {
		panic(err)
	}
	return parsed
}
