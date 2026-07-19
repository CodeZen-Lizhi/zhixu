package domain

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	testWorkspaceID     foundation.ID = "10000000-0000-4000-8000-000000000001"
	testIndexVersionID  foundation.ID = "10000000-0000-4000-8000-000000000002"
	testChunkID         foundation.ID = "10000000-0000-4000-8000-000000000003"
	testSourceVersionID foundation.ID = "10000000-0000-4000-8000-000000000004"
	testSourceSpanID    foundation.ID = "10000000-0000-4000-8000-000000000005"
	testModelRunID      foundation.ID = "10000000-0000-4000-8000-000000000006"
	testClaimID         foundation.ID = "10000000-0000-4000-8000-000000000007"
)

func TestRelationAssessmentPayloadReusesKnowledgeAssessment(t *testing.T) {
	payload := validRelationAssessmentPayload()
	var assessment knowledgedomain.RelationAssessment = payload.Assessment
	if assessment != knowledgedomain.AssessmentComplementary {
		t.Fatalf("assessment=%s", assessment)
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("validate relation assessment: %v", err)
	}

	result := RelationAssessmentResult{
		ResultType: ResultTypeRelationAssessment, SchemaID: RelationAssessmentSchemaID,
		SchemaVersion: OutputSchemaVersionV1, ModelRunRef: testModelRunID, Payload: payload,
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("validate relation result: %v", err)
	}
}

func TestNewRelationAssessmentJSONRoundTripOmitsExistingApplicability(t *testing.T) {
	payload := validRelationAssessmentPayload()
	payload.Assessment = knowledgedomain.AssessmentNew
	payload.ExistingEvidenceRefs = []string{}
	payload.Applicability.Existing = nil
	payload.Applicability.Summary = ""
	result := RelationAssessmentResult{
		ResultType: ResultTypeRelationAssessment, SchemaID: RelationAssessmentSchemaID,
		SchemaVersion: OutputSchemaVersionV1, ModelRunRef: testModelRunID, Payload: payload,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"existing":null`)) {
		t.Fatalf("NEW result serialized a null existing applicability: %s", encoded)
	}
	decoded, err := DecodeRelationAssessment(encoded, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("decode NEW relation assessment round-trip: %v", err)
	}
	if decoded.Payload.Assessment != knowledgedomain.AssessmentNew {
		t.Fatalf("assessment=%s", decoded.Payload.Assessment)
	}
}

func TestRelationAssessmentRejectsUnsupportedOrIncompleteClassifications(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RelationAssessmentPayload)
	}{
		{name: "unknown assessment", mutate: func(value *RelationAssessmentPayload) { value.Assessment = "SIMILAR" }},
		{name: "missing candidate evidence", mutate: func(value *RelationAssessmentPayload) { value.CandidateEvidenceRefs = nil }},
		{name: "missing existing evidence", mutate: func(value *RelationAssessmentPayload) { value.ExistingEvidenceRefs = nil }},
		{name: "invalid applicability", mutate: func(value *RelationAssessmentPayload) { value.Applicability.Existing = json.RawMessage(`[]`) }},
		{name: "duplicate evidence", mutate: func(value *RelationAssessmentPayload) { value.ExistingEvidenceRefs = []string{"old-1", "old-1"} }},
		{name: "low confidence without uncertainty", mutate: func(value *RelationAssessmentPayload) {
			value.Assessment = knowledgedomain.AssessmentLowConfidence
			value.UncertaintyReasons = nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validRelationAssessmentPayload()
			test.mutate(&value)
			if err := value.Validate(); errorCode(err) != ErrorCodeRelationAssessmentInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}

	newPayload := validRelationAssessmentPayload()
	newPayload.Assessment = knowledgedomain.AssessmentNew
	newPayload.ExistingEvidenceRefs = []string{}
	newPayload.Applicability.Existing = nil
	newPayload.Applicability.Summary = ""
	if err := newPayload.Validate(); err != nil {
		t.Fatalf("valid NEW rejected: %v", err)
	}
}

func TestRAGAnswerEnforcesAssertionCitationClosureAndConflictDisclosure(t *testing.T) {
	payload := validAnswerPayload()
	if err := payload.Validate(); err != nil {
		t.Fatalf("validate answer: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RAGAnswerPayload)
	}{
		{name: "fact without citation", mutate: func(value *RAGAnswerPayload) { value.Assertions[0].CitationIDs = nil }},
		{name: "unknown citation", mutate: func(value *RAGAnswerPayload) { value.Assertions[0].CitationIDs = []string{"missing"} }},
		{name: "duplicate assertion", mutate: func(value *RAGAnswerPayload) { value.Assertions = append(value.Assertions, value.Assertions[0]) }},
		{name: "one conflict side", mutate: func(value *RAGAnswerPayload) { value.ConflictPositions = value.ConflictPositions[:1] }},
		{name: "conflict without summary", mutate: func(value *RAGAnswerPayload) { value.ConflictSummary = "" }},
		{name: "duplicate conflict claim", mutate: func(value *RAGAnswerPayload) { value.ConflictPositions[1].ClaimID = value.ConflictPositions[0].ClaimID }},
		{name: "cross workspace citation", mutate: func(value *RAGAnswerPayload) { value.Citations[1].WorkspaceID = "10000000-0000-4000-8000-000000000014" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validAnswerPayload()
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid answer accepted")
			}
		})
	}
}

func TestRAGAnswerV2AddsBoundTopicsAndFollowUpsWithoutChangingV1(t *testing.T) {
	payload := validAnswerPayloadV2()
	result := RAGAnswerResultV2{
		ResultType: ResultTypeRAGAnswer, SchemaID: RAGAnswerSchemaID,
		SchemaVersion: OutputSchemaVersionV2, ModelRunRef: testModelRunID, Payload: payload,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRAGAnswerV2(encoded, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("decode rag answer v2: %v", err)
	}
	if len(decoded.Payload.RelatedTopics) != 1 || len(decoded.Payload.FollowUpQuestions) != 2 {
		t.Fatalf("decoded v2 payload=%#v", decoded.Payload)
	}
	if _, err := DecodeRAGAnswer(encoded, DefaultDecodeLimits()); err == nil {
		t.Fatal("v1 decoder accepted v2-only fields")
	}

	tests := []struct {
		name   string
		mutate func(*RAGAnswerPayloadV2)
	}{
		{name: "missing related topic", mutate: func(value *RAGAnswerPayloadV2) { value.RelatedTopics = nil }},
		{name: "topic without citation", mutate: func(value *RAGAnswerPayloadV2) { value.RelatedTopics[0].CitationIDs = nil }},
		{name: "topic with unknown citation", mutate: func(value *RAGAnswerPayloadV2) { value.RelatedTopics[0].CitationIDs = []string{"missing"} }},
		{name: "duplicate topic", mutate: func(value *RAGAnswerPayloadV2) {
			value.RelatedTopics = append(value.RelatedTopics, value.RelatedTopics[0])
		}},
		{name: "missing follow up", mutate: func(value *RAGAnswerPayloadV2) { value.FollowUpQuestions = nil }},
		{name: "too many follow ups", mutate: func(value *RAGAnswerPayloadV2) {
			value.FollowUpQuestions = []string{"one?", "two?", "three?", "four?", "five?", "six?"}
		}},
		{name: "duplicate follow up", mutate: func(value *RAGAnswerPayloadV2) { value.FollowUpQuestions = []string{"next?", "next?"} }},
		{name: "nul follow up", mutate: func(value *RAGAnswerPayloadV2) { value.FollowUpQuestions = []string{"next?\x00"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validAnswerPayloadV2()
			test.mutate(&value)
			if err := value.Validate(); errorCode(err) != ErrorCodeAnswerInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestEvidenceRequiresConflictBindingOnlyForDisputed(t *testing.T) {
	evidence := Evidence{Citation: testCitation(), Excerpt: "supported text", Eligibility: knowledgedomain.EvidenceEligible}
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	evidence.Eligibility = knowledgedomain.EvidenceEligibleWithConflict
	if err := evidence.Validate(); errorCode(err) != ErrorCodeEvidenceInvalid {
		t.Fatalf("missing conflict err=%v", err)
	}
	evidence.ConflictIDs = []foundation.ID{testClaimID}
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	evidence.Eligibility = knowledgedomain.EvidenceIneligible
	if err := evidence.Validate(); errorCode(err) != ErrorCodeEvidenceInvalid {
		t.Fatalf("ineligible conflict err=%v", err)
	}
	evidence.Eligibility = knowledgedomain.EvidenceEligibleWithConflict
	evidence.ConflictIDs = []foundation.ID{
		"10000000-0000-4000-8000-000000000015",
		"10000000-0000-4000-8000-000000000014",
	}
	if err := evidence.Validate(); errorCode(err) != ErrorCodeEvidenceInvalid {
		t.Fatalf("unordered conflicts err=%v", err)
	}
}

func TestEvidenceFromKnowledgeEligibilityPreservesConflictAndProvenance(t *testing.T) {
	conflictID := foundation.ID("10000000-0000-4000-8000-000000000012")
	disputedApplicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{"environment":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	eligibility := knowledgedomain.ProvenanceEligibility{
		Provenance:  knowledgedomain.ProvenanceRef{WorkspaceID: testWorkspaceID, SourceVersionID: testSourceVersionID, SourceSpanID: testSourceSpanID},
		Eligibility: knowledgedomain.EvidenceEligibleWithConflict,
		Bindings: []knowledgedomain.EvidenceEligibilityBinding{{
			OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: testClaimID,
			EvidenceID: "10000000-0000-4000-8000-000000000013", ClaimStatus: knowledgedomain.ClaimStatusDisputed,
			SupportType: knowledgedomain.ClaimSupportSupports, ConflictIDs: []foundation.ID{conflictID},
			DisputedApplicability: disputedApplicability, DisputedClaimUpdatedAtUTC: time.Unix(1, 0).UTC(),
		}},
	}
	evidence, err := EvidenceFromKnowledgeEligibility(testCitation(), "supported text", eligibility)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Eligibility != knowledgedomain.EvidenceEligibleWithConflict || len(evidence.ConflictIDs) != 1 || evidence.ConflictIDs[0] != conflictID {
		t.Fatalf("evidence=%#v", evidence)
	}

	crossWorkspace := testCitation()
	crossWorkspace.WorkspaceID = "10000000-0000-4000-8000-000000000014"
	if _, err := EvidenceFromKnowledgeEligibility(crossWorkspace, "text", eligibility); errorCode(err) != ErrorCodeEvidenceInvalid {
		t.Fatalf("cross workspace err=%v", err)
	}
}

func TestRefusalCoversEveryStableReason(t *testing.T) {
	reasons := []RefusalReasonCode{
		RefusalNoRelevantEvidence, RefusalUnapprovedEvidenceOnly, RefusalCitationUnresolvable,
		RefusalEvidenceInsufficient, RefusalExternalFactUnauthorized, RefusalConflictNotConditionable,
		RefusalValidationExhausted,
	}
	for _, reason := range reasons {
		payload := RefusalPayload{
			ReasonCode: reason, Summary: "cannot publish", RetrievalScope: "approved knowledge",
			MissingRequirements: []string{"supporting evidence"}, SuggestedActions: []string{"provide evidence"},
		}
		if err := payload.Validate(); err != nil {
			t.Fatalf("reason=%s err=%v", reason, err)
		}
	}
	invalid := RefusalPayload{ReasonCode: "OTHER", Summary: "x", RetrievalScope: "x", MissingRequirements: []string{"x"}}
	if err := invalid.Validate(); errorCode(err) != ErrorCodeRefusalInvalid {
		t.Fatalf("err=%v", err)
	}
}

func TestFaithfulnessReviewPassedMustMatchEveryVerdict(t *testing.T) {
	payload := FaithfulnessReviewPayload{
		Passed: true,
		Items: []FaithfulnessReviewItem{
			{AssertionID: "a-1", Verdict: FaithfulnessSupported, CitationIDs: []string{"cite-1"}, Reason: "span supports assertion"},
			{AssertionID: "a-2", Verdict: FaithfulnessInferenceDisclosed, CitationIDs: []string{}, Reason: "explicit inference"},
		},
		Summary: "all assertions are publishable",
	}
	if err := payload.Validate(); err != nil {
		t.Fatal(err)
	}
	payload.Items[0].Verdict = FaithfulnessUnsupported
	if err := payload.Validate(); errorCode(err) != ErrorCodeFaithfulnessReviewInvalid {
		t.Fatalf("contradictory passed err=%v", err)
	}
	payload.Passed = false
	if err := payload.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeStrictTaskEnvelopeRejectsUnknownDuplicateAndEnum(t *testing.T) {
	valid := RefusalResult{
		ResultType: ResultTypeRefusal, SchemaID: RefusalSchemaID, SchemaVersion: OutputSchemaVersionV1,
		ModelRunRef: testModelRunID,
		Payload: RefusalPayload{
			ReasonCode: RefusalEvidenceInsufficient, Summary: "not enough", RetrievalScope: "approved",
			MissingRequirements: []string{"coverage"}, SuggestedActions: []string{"add evidence"},
		},
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict(raw, DefaultDecodeLimits(), RefusalResult.Validate); err != nil {
		t.Fatalf("valid refusal rejected: %v", err)
	}

	invalid := []string{
		string(raw[:len(raw)-1]) + `,"unknown":true}`,
		`{"result_type":"refusal","result_type":"refusal","schema_id":"agent.refusal","schema_version":"v1","model_run_ref":"10000000-0000-4000-8000-000000000006","payload":{"reason_code":"EVIDENCE_INSUFFICIENT","summary":"x","retrieval_scope":"x","missing_requirements":["x"],"suggested_actions":[]}}`,
		`{"result_type":"refusal","schema_id":"agent.refusal","schema_version":"v1","model_run_ref":"10000000-0000-4000-8000-000000000006","payload":{"reason_code":"OTHER","summary":"x","retrieval_scope":"x","missing_requirements":["x"],"suggested_actions":[]}}`,
	}
	for _, candidate := range invalid {
		if _, err := DecodeStrict([]byte(candidate), DefaultDecodeLimits(), RefusalResult.Validate); err == nil {
			t.Fatalf("invalid refusal accepted: %s", candidate)
		}
	}
}

func TestTaskDecodersRejectNullForRequiredArrayFields(t *testing.T) {
	relation := RelationAssessmentResult{
		ResultType: ResultTypeRelationAssessment, SchemaID: RelationAssessmentSchemaID, SchemaVersion: OutputSchemaVersionV1,
		ModelRunRef: testModelRunID, Payload: func() RelationAssessmentPayload {
			payload := validRelationAssessmentPayload()
			payload.Assessment = knowledgedomain.AssessmentNew
			payload.ExistingEvidenceRefs = []string{}
			payload.Applicability.Existing = nil
			payload.Applicability.Summary = ""
			return payload
		}(),
	}
	relationJSON, _ := json.Marshal(relation)
	for _, field := range []string{"existing_evidence_refs", "conflict_disclosures", "uncertainty_reasons"} {
		needle := []byte(`"` + field + `":[]`)
		candidate := bytes.Replace(relationJSON, needle, []byte(`"`+field+`":null`), 1)
		if bytes.Equal(candidate, relationJSON) {
			t.Fatalf("fixture lacks empty array field %s: %s", field, relationJSON)
		}
		if _, err := DecodeRelationAssessment(candidate, DefaultDecodeLimits()); err == nil {
			t.Fatalf("relation decoder accepted null %s", field)
		}
	}

	answer := RAGAnswerResult{
		ResultType: ResultTypeRAGAnswer, SchemaID: RAGAnswerSchemaID, SchemaVersion: OutputSchemaVersionV1,
		ModelRunRef: testModelRunID, Payload: validAnswerPayload(),
	}
	answerJSON, _ := json.Marshal(answer)
	answerJSON = bytes.Replace(answerJSON, []byte(`"citation_ids":[]`), []byte(`"citation_ids":null`), 1)
	if _, err := DecodeRAGAnswer(answerJSON, DefaultDecodeLimits()); err == nil {
		t.Fatal("answer decoder accepted null inference citation_ids")
	}

	review := FaithfulnessReviewResult{
		ResultType: ResultTypeFaithfulnessReview, SchemaID: FaithfulnessReviewSchemaID, SchemaVersion: OutputSchemaVersionV1,
		ModelRunRef: testModelRunID, Payload: FaithfulnessReviewPayload{
			Passed: true, Items: []FaithfulnessReviewItem{{
				AssertionID: "a-1", Verdict: FaithfulnessInferenceDisclosed, CitationIDs: []string{}, Reason: "explicit inference",
			}}, Summary: "safe",
		},
	}
	reviewJSON, _ := json.Marshal(review)
	reviewJSON = bytes.Replace(reviewJSON, []byte(`"citation_ids":[]`), []byte(`"citation_ids":null`), 1)
	if _, err := DecodeFaithfulnessReview(reviewJSON, DefaultDecodeLimits()); err == nil {
		t.Fatal("faithfulness decoder accepted null citation_ids")
	}
}

func validRelationAssessmentPayload() RelationAssessmentPayload {
	return RelationAssessmentPayload{
		Assessment:            knowledgedomain.AssessmentComplementary,
		CandidateEvidenceRefs: []string{"new-1"},
		ExistingEvidenceRefs:  []string{"old-1"},
		ConflictDisclosures:   []RelationConflictDisclosure{},
		Applicability: ApplicabilityComparison{
			Candidate: json.RawMessage(`{"environment":"prod"}`), Existing: json.RawMessage(`{"environment":"prod"}`), Summary: "same environment",
		},
		Reason: "adds deployment detail", ConfidenceFactors: []string{"both spans are explicit"}, UncertaintyReasons: []string{},
	}
}

func validAnswerPayload() RAGAnswerPayload {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	secondCitation := testCitation()
	secondCitation.ID = "cite-2"
	secondCitation.ChunkID = "10000000-0000-4000-8000-000000000008"
	secondCitation.SourceVersionID = "10000000-0000-4000-8000-000000000009"
	secondCitation.SourceSpanID = "10000000-0000-4000-8000-000000000010"
	return RAGAnswerPayload{
		Conclusion: "The sources describe two conditional positions.",
		Assertions: []Assertion{
			{ID: "a-1", Text: "Position A applies in production.", Kind: AssertionFactual, CitationIDs: []string{"cite-1"}},
			{ID: "a-2", Text: "The final choice needs user context.", Kind: AssertionModelInference, CitationIDs: []string{}},
		},
		Citations: []Citation{testCitation(), secondCitation},
		ConflictPositions: []ConflictPosition{
			{ClaimID: testClaimID, Position: "use A", Applicability: json.RawMessage(`{"environment":"prod"}`), CitationIDs: []string{"cite-1"}, UpdatedAt: now},
			{ClaimID: "10000000-0000-4000-8000-000000000011", Position: "use B", Applicability: json.RawMessage(`{"environment":"dev"}`), CitationIDs: []string{"cite-2"}, UpdatedAt: now},
		},
		ConflictSummary: "The positions apply to different environments.",
	}
}

func validAnswerPayloadV2() RAGAnswerPayloadV2 {
	return RAGAnswerPayloadV2{
		RAGAnswerPayload: validAnswerPayload(),
		RelatedTopics: []RelatedTopic{{
			TopicID: "10000000-0000-4000-8000-000000000016",
			Name:    "Runtime safety", CitationIDs: []string{"cite-1"},
		}},
		FollowUpQuestions: []string{"Which condition applies to production?", "What evidence would resolve the choice?"},
	}
}

func testCitation() Citation {
	return Citation{
		ID: "cite-1", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexVersionID,
		ChunkID: testChunkID, SourceVersionID: testSourceVersionID, SourceSpanID: testSourceSpanID,
	}
}
