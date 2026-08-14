package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

func TestRAGAnswerMetadataBindsExactStreamAndCannotSupplyConclusion(t *testing.T) {
	finalText := "Final\nanswer."
	sum := sha256.Sum256([]byte(finalText))
	answer := validAnswerPayloadV2()
	metadata := RAGAnswerMetadataResult{
		ResultType: ResultTypeRAGAnswerMetadata, SchemaID: RAGAnswerMetadataSchemaID,
		SchemaVersion: OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: RAGAnswerMetadataPayload{
			AnswerSHA256: hex.EncodeToString(sum[:]), Assertions: answer.Assertions, Citations: answer.Citations,
			ConflictPositions: answer.ConflictPositions, ConflictSummary: answer.ConflictSummary,
			RelatedTopics: answer.RelatedTopics, FollowUpQuestions: answer.FollowUpQuestions,
		},
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRAGAnswerMetadata(encoded, DefaultDecodeLimits())
	if err != nil {
		t.Fatal(err)
	}
	composed, err := decoded.ComposeRAGAnswerV2(finalText)
	if err != nil {
		t.Fatal(err)
	}
	if composed.Payload.Conclusion != finalText || composed.ModelRunRef != testModelRunID {
		t.Fatalf("composed=%+v", composed)
	}
	if _, err := decoded.ComposeRAGAnswerV2("Final answer."); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("hash mismatch err=%v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	payload := document["payload"].(map[string]any)
	payload["conclusion"] = "model must not regenerate this"
	withConclusion, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRAGAnswerMetadata(withConclusion, DefaultDecodeLimits()); err == nil {
		t.Fatal("metadata decoder accepted a model-supplied conclusion")
	}
}

func TestRAGAnswerMetadataV2ComposesExactStreamWithoutModelOwnedBindingFields(t *testing.T) {
	finalText := "Final\nanswer."
	answer := validAnswerPayloadV2()
	metadata := RAGAnswerMetadataResultV2{
		ResultType: ResultTypeRAGAnswerMetadata, SchemaID: RAGAnswerMetadataSchemaID,
		SchemaVersion: OutputSchemaVersionV2,
		Payload: RAGAnswerMetadataPayloadV2{
			Assertions: []RAGAnswerMetadataAssertionV2{
				{ID: answer.Assertions[0].ID, Text: answer.Assertions[0].Text, Kind: AssertionFactual, EvidenceRefs: []string{"E1"}},
				{ID: answer.Assertions[1].ID, Text: answer.Assertions[1].Text, Kind: AssertionModelInference, EvidenceRefs: []string{}},
			},
			ConflictPositions: []RAGAnswerMetadataConflictPositionV2{
				{ConflictRef: "C1", Position: answer.ConflictPositions[0].Position},
				{ConflictRef: "C2", Position: answer.ConflictPositions[1].Position},
			},
			ConflictSummary: answer.ConflictSummary, RelatedTopicRefs: []string{"T1"},
			FollowUpQuestions: answer.FollowUpQuestions,
		},
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRAGAnswerMetadataV2(encoded, DefaultDecodeLimits())
	if err != nil {
		t.Fatal(err)
	}
	composed, err := decoded.ComposeRAGAnswerV2(testModelRunID, finalText, validMetadataBindingsV2())
	if err != nil {
		t.Fatal(err)
	}
	answer.Conclusion = finalText
	if composed.Payload.Conclusion != finalText || composed.ModelRunRef != testModelRunID ||
		!bytes.Equal(mustJSON(t, composed.Payload), mustJSON(t, answer)) {
		t.Fatalf("composed=%+v", composed)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"model_run_ref"`), []byte(`"conclusion"`), []byte(`"answer_sha256"`), []byte(`"citations"`),
		[]byte(`"citation_ids"`), []byte(`"workspace_id"`), []byte(`"source_span_id"`), []byte(`"topic_id"`),
		[]byte(`"claim_id"`), []byte(`"updated_at"`), []byte(testWorkspaceID), []byte(testClaimID),
	} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("metadata v2 contains forbidden server-owned value %q: %s", forbidden, encoded)
		}
	}

	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	payload := document["payload"].(map[string]any)
	for _, forbidden := range []string{"conclusion", "answer_sha256", "citations", "related_topics"} {
		payload[forbidden] = "model must not own this field"
		invalidDocument, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, decodeErr := DecodeRAGAnswerMetadataV2(invalidDocument, DefaultDecodeLimits()); decodeErr == nil {
			t.Fatalf("metadata v2 decoder accepted %s", forbidden)
		}
		delete(payload, forbidden)
	}
	document["model_run_ref"] = testModelRunID
	withModelRun, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRAGAnswerMetadataV2(withModelRun, DefaultDecodeLimits()); err == nil {
		t.Fatal("metadata v2 decoder accepted model_run_ref")
	}

	unknownEvidence := decoded
	unknownEvidence.Payload.Assertions = append([]RAGAnswerMetadataAssertionV2(nil), decoded.Payload.Assertions...)
	unknownEvidence.Payload.Assertions[0].EvidenceRefs = []string{"E3"}
	if _, err := unknownEvidence.ComposeRAGAnswerV2(testModelRunID, finalText, validMetadataBindingsV2()); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("unknown evidence err=%v", err)
	}
	reservedAssertion := decoded
	reservedAssertion.Payload.Assertions = append([]RAGAnswerMetadataAssertionV2(nil), decoded.Payload.Assertions...)
	reservedAssertion.Payload.Assertions[0].ID = "@answer/conclusion"
	if err := reservedAssertion.Validate(); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("reserved assertion identity err=%v", err)
	}
	outOfRangeEvidence := decoded
	outOfRangeEvidence.Payload.Assertions = append([]RAGAnswerMetadataAssertionV2(nil), decoded.Payload.Assertions...)
	outOfRangeEvidence.Payload.Assertions[0].EvidenceRefs = []string{"E501"}
	if err := outOfRangeEvidence.Validate(); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("out-of-range evidence ref err=%v", err)
	}
	outOfRangeTopic := decoded
	outOfRangeTopic.Payload.RelatedTopicRefs = []string{"T51"}
	if err := outOfRangeTopic.Validate(); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("out-of-range topic ref err=%v", err)
	}
	unknownConflict := decoded
	unknownConflict.Payload.ConflictPositions = append([]RAGAnswerMetadataConflictPositionV2(nil), decoded.Payload.ConflictPositions...)
	unknownConflict.Payload.ConflictPositions[1].ConflictRef = "C3"
	if _, err := unknownConflict.ComposeRAGAnswerV2(testModelRunID, finalText, validMetadataBindingsV2()); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("incomplete conflict coverage err=%v", err)
	}
	unknownTopic := decoded
	unknownTopic.Payload.RelatedTopicRefs = []string{"T2"}
	if _, err := unknownTopic.ComposeRAGAnswerV2(testModelRunID, finalText, validMetadataBindingsV2()); errorCode(err) != ErrorCodeAnswerInvalid {
		t.Fatalf("unknown topic err=%v", err)
	}
}

func TestRAGAnswerMetadataRefusalV2InjectsModelRunOnlyAfterStrictDecode(t *testing.T) {
	modelOutput := RAGAnswerMetadataRefusalResultV2{
		ResultType: ResultTypeRefusal, SchemaID: RAGAnswerMetadataRefusalSchemaID, SchemaVersion: OutputSchemaVersionV2,
		Payload: RefusalPayload{
			ReasonCode: RefusalValidationExhausted, Summary: "Metadata validation could not be completed.",
			RetrievalScope: "approved workspace evidence", MissingRequirements: []string{"valid bounded metadata"},
			SuggestedActions: []string{"retry with the approved evidence set"},
		},
	}
	raw := mustJSON(t, modelOutput)
	if bytes.Contains(raw, []byte(`"model_run_ref"`)) || bytes.Contains(raw, []byte(testModelRunID)) {
		t.Fatalf("metadata refusal leaked model run identity: %s", raw)
	}
	decoded, err := DecodeRAGAnswerMetadataRefusalV2(raw, DefaultDecodeLimits())
	if err != nil {
		t.Fatal(err)
	}
	refusal, err := decoded.ComposeRefusal(testModelRunID)
	if err != nil {
		t.Fatal(err)
	}
	if refusal.ModelRunRef != testModelRunID || refusal.SchemaID != RefusalSchemaID || refusal.SchemaVersion != OutputSchemaVersionV1 ||
		!bytes.Equal(mustJSON(t, refusal.Payload), mustJSON(t, modelOutput.Payload)) {
		t.Fatalf("refusal=%+v", refusal)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["model_run_ref"] = testModelRunID
	withIdentity := mustJSON(t, document)
	if _, err := DecodeRAGAnswerMetadataRefusalV2(withIdentity, DefaultDecodeLimits()); err == nil {
		t.Fatal("metadata refusal accepted model_run_ref")
	}
	if _, err := decoded.ComposeRefusal(""); err == nil {
		t.Fatal("metadata refusal accepted an invalid server model run binding")
	}
	for _, mutate := range []func(*RAGAnswerMetadataRefusalResultV2){
		func(value *RAGAnswerMetadataRefusalResultV2) {
			value.Payload.MissingRequirements = []string{"missing", "missing"}
		},
		func(value *RAGAnswerMetadataRefusalResultV2) {
			value.Payload.SuggestedActions = []string{"retry", "retry"}
		},
	} {
		duplicate := modelOutput
		mutate(&duplicate)
		if _, err := DecodeRAGAnswerMetadataRefusalV2(mustJSON(t, duplicate), DefaultDecodeLimits()); errorCode(err) != ErrorCodeRefusalInvalid {
			t.Fatalf("metadata refusal accepted duplicate list: %v", err)
		}
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

func validMetadataBindingsV2() RAGAnswerMetadataBindings {
	answer := validAnswerPayloadV2()
	return RAGAnswerMetadataBindings{
		Evidence: []RAGAnswerMetadataEvidenceBinding{
			{Ref: "E1", Citation: answer.Citations[0]},
			{Ref: "E2", Citation: answer.Citations[1]},
		},
		Conflicts: []RAGAnswerMetadataConflictBinding{
			{Ref: "C1", ClaimID: answer.ConflictPositions[0].ClaimID, Applicability: answer.ConflictPositions[0].Applicability, CitationIDs: answer.ConflictPositions[0].CitationIDs, UpdatedAt: answer.ConflictPositions[0].UpdatedAt},
			{Ref: "C2", ClaimID: answer.ConflictPositions[1].ClaimID, Applicability: answer.ConflictPositions[1].Applicability, CitationIDs: answer.ConflictPositions[1].CitationIDs, UpdatedAt: answer.ConflictPositions[1].UpdatedAt},
		},
		RelatedTopics: []RAGAnswerMetadataTopicBinding{{Ref: "T1", Topic: answer.RelatedTopics[0]}},
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testCitation() Citation {
	return Citation{
		ID: "cite-1", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexVersionID,
		ChunkID: testChunkID, SourceVersionID: testSourceVersionID, SourceSpanID: testSourceSpanID,
	}
}
