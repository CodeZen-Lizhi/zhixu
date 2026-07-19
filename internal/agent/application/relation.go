package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// MaxRelationEvidence 是一次 Relation Assessment 允许送入模型的证据总数。
	MaxRelationEvidence = knowledgedomain.MaxBatchLimit
	// MaxRelationStatementBytes 是单侧 Claim 文本允许的最大 UTF-8 字节数。
	MaxRelationStatementBytes = 16 * 1024
	// RelationAssessmentReducedSchemaID 是关系五分类 REDUCED 阶段的任务专用 Schema ID。
	RelationAssessmentReducedSchemaID = "agent.relation-assessment.reduced"
)

const (
	errorCodeRelationAnalyzerMissing    = "AGENT_RELATION_ANALYZER_MISSING"
	errorCodeRelationRequestInvalid     = "AGENT_RELATION_REQUEST_INVALID"
	errorCodeRelationEligibilityInvalid = "AGENT_RELATION_ELIGIBILITY_INVALID"
	errorCodeRelationEvidenceIneligible = "AGENT_RELATION_EVIDENCE_INELIGIBLE"
	errorCodeRelationOutputMismatch     = "AGENT_RELATION_OUTPUT_MISMATCH"
	errorCodeRelationActionUnsafe       = "AGENT_RELATION_ACTION_UNSAFE"
)

// RelationRunner 执行一次有界结构化模型运行。
type RelationRunner interface {
	// Run 必须遵守 Structured Runner 的 INITIAL、REPAIR、REDUCED 总预算。
	Run(context.Context, StructuredRunRequest) (StructuredRunResult, error)
}

// RelationKnowledgePort 是 Agent 调用 Knowledge Application/Domain 的最小只读与候选动作接缝。
type RelationKnowledgePort interface {
	// LoadFormalClaims 单批加载服务端 Confirmed/Disputed Claim 完整事实。
	LoadFormalClaims(context.Context, foundation.ID, []foundation.ID) ([]knowledgedomain.ClaimWithSources, error)
	// CheckEvidenceEligibility 批量判断正式 Evidence 资格，不得把 Active Index 当作批准事实。
	CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error)
	// MapAssessment 调用 Knowledge Domain 唯一映射并只返回候选动作，不执行写入。
	MapAssessment(knowledgedomain.RelationAssessment, knowledgedomain.NodeRef, knowledgedomain.NodeRef) (knowledgedomain.AssessmentAction, error)
}

// RelationEvidenceInput 是送入 Relation Analyzer 的未判定资格证据。
type RelationEvidenceInput struct {
	Citation agentdomain.Citation
	Excerpt  string
}

// RelationClaimInput 是 Relation Assessment 的单侧 Claim、Applicability 与证据。
type RelationClaimInput struct {
	Node          knowledgedomain.NodeRef
	Statement     string
	Applicability knowledgedomain.Applicability
	Evidence      []RelationEvidenceInput
}

// RelationAnalyzeRequest 冻结一次 Relation Assessment 的运行版本和业务输入。
type RelationAnalyzeRequest struct {
	WorkspaceID foundation.ID
	ModelRunRef foundation.ID
	Retrieval   agentdomain.RetrievalRef
	Runtime     StructuredRunRequest
	Candidate   RelationClaimInput
	Existing    *RelationClaimInput
}

// RelationAnalysisResult 返回严格五分类、资格证据和 Knowledge 候选动作。
type RelationAnalysisResult struct {
	Assessment        agentdomain.RelationAssessmentResult
	Action            knowledgedomain.AssessmentAction
	CandidateEvidence []agentdomain.Evidence
	ExistingEvidence  []agentdomain.Evidence
	StructuredRun     StructuredRunResult
}

// RelationAnalyzer 在调用模型前完成 Evidence Eligibility，并在输出后执行领域闭包校验。
type RelationAnalyzer struct {
	runner    RelationRunner
	knowledge RelationKnowledgePort
}

// NewRelationAnalyzer 创建不持有 Repository、SQL 或正式写权限的 Relation Analyzer。
func NewRelationAnalyzer(runner RelationRunner, knowledge RelationKnowledgePort) (*RelationAnalyzer, error) {
	if isNilPort(runner) || isNilPort(knowledge) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRelationAnalyzerMissing, false, errors.New("relation analyzer dependencies are incomplete"))
	}
	return &RelationAnalyzer{runner: runner, knowledge: knowledge}, nil
}

// Analyze 执行 Eligibility、结构化五分类、Applicability 对齐和 Knowledge 候选动作映射。
func (analyzer *RelationAnalyzer) Analyze(ctx context.Context, request RelationAnalyzeRequest) (RelationAnalysisResult, error) {
	if analyzer == nil || isNilPort(analyzer.runner) || isNilPort(analyzer.knowledge) {
		return RelationAnalysisResult{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRelationAnalyzerMissing, false, errors.New("relation analyzer is not initialized"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRelationAnalyzeRequest(request); err != nil {
		return RelationAnalysisResult{}, err
	}

	resolvedRequest, err := analyzer.resolveFormalExistingClaim(ctx, request)
	if err != nil {
		return RelationAnalysisResult{}, err
	}
	candidateEvidence, existingEvidence, disclosures, err := analyzer.loadEligibleEvidence(ctx, resolvedRequest)
	if err != nil {
		return RelationAnalysisResult{}, err
	}
	modelInput, err := encodeRelationModelInput(resolvedRequest, candidateEvidence, existingEvidence, disclosures)
	if err != nil {
		return RelationAnalysisResult{}, err
	}
	runRequest := request.Runtime
	runRequest.Input = modelInput
	run, err := analyzer.runner.Run(ctx, runRequest)
	if err != nil {
		return RelationAnalysisResult{}, err
	}
	assessment, err := agentdomain.DecodeRelationAssessment(run.Output, agentdomain.DefaultDecodeLimits())
	if err != nil {
		return RelationAnalysisResult{}, err
	}
	if assessment.ModelRunRef != request.ModelRunRef {
		return RelationAnalysisResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationOutputMismatch, false, errors.New("relation assessment model run reference differs from the frozen request"))
	}
	if err := validateRelationOutput(resolvedRequest, assessment.Payload, candidateEvidence, existingEvidence, disclosures); err != nil {
		return RelationAnalysisResult{}, err
	}

	target := knowledgedomain.NodeRef{}
	if resolvedRequest.Existing != nil {
		target = resolvedRequest.Existing.Node
	}
	action, err := analyzer.knowledge.MapAssessment(assessment.Payload.Assessment, resolvedRequest.Candidate.Node, target)
	if err != nil {
		return RelationAnalysisResult{}, err
	}
	if err := validateSafeAssessmentAction(assessment.Payload.Assessment, resolvedRequest.Candidate.Node, target, action); err != nil {
		return RelationAnalysisResult{}, err
	}
	return RelationAnalysisResult{
		Assessment:        assessment,
		Action:            action,
		CandidateEvidence: append([]agentdomain.Evidence(nil), candidateEvidence...),
		ExistingEvidence:  append([]agentdomain.Evidence(nil), existingEvidence...),
		StructuredRun:     cloneStructuredRunResult(run),
	}, nil
}

func (analyzer *RelationAnalyzer) resolveFormalExistingClaim(ctx context.Context, request RelationAnalyzeRequest) (RelationAnalyzeRequest, error) {
	if request.Existing == nil {
		return request, nil
	}
	claims, err := analyzer.knowledge.LoadFormalClaims(ctx, request.WorkspaceID, []foundation.ID{request.Existing.Node.ID})
	if err != nil {
		return RelationAnalyzeRequest{}, err
	}
	if len(claims) != 1 {
		return RelationAnalyzeRequest{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, errors.New("knowledge returned an incomplete formal existing claim"))
	}
	formal := claims[0]
	if formal.Claim.ID != request.Existing.Node.ID || formal.Claim.WorkspaceID != request.WorkspaceID ||
		(formal.Claim.Status != knowledgedomain.ClaimStatusConfirmed && formal.Claim.Status != knowledgedomain.ClaimStatusDisputed) ||
		knowledgedomain.ValidateClaimAggregate(formal.Claim, formal.Sources) != nil {
		return RelationAnalyzeRequest{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, errors.New("knowledge returned an invalid formal existing claim"))
	}
	if formal.Claim.Statement != request.Existing.Statement || !sameApplicability(request.Existing.Applicability.CanonicalJSON, formal.Claim.Applicability) {
		return RelationAnalyzeRequest{}, relationInvalid("existing claim statement or applicability differs from the formal knowledge fact")
	}
	allowedProvenance := make(map[knowledgedomain.ProvenanceRef]struct{}, len(formal.Sources))
	for _, source := range formal.Sources {
		allowedProvenance[source.Provenance] = struct{}{}
	}
	for _, evidence := range request.Existing.Evidence {
		if _, exists := allowedProvenance[provenanceFromCitation(evidence.Citation)]; !exists {
			return RelationAnalyzeRequest{}, applicationError(foundation.ErrorPermissionDenied, errorCodeRelationEvidenceIneligible, false, errors.New("existing evidence is not a source of the formal existing claim"))
		}
	}
	resolved := request
	resolvedExisting := *request.Existing
	resolvedExisting.Statement = formal.Claim.Statement
	resolvedExisting.Applicability = cloneApplicability(formal.Claim.Applicability)
	resolvedExisting.Evidence = append([]RelationEvidenceInput(nil), request.Existing.Evidence...)
	resolved.Existing = &resolvedExisting
	return resolved, nil
}

func validateRelationAnalyzeRequest(request RelationAnalyzeRequest) error {
	if !canonicalApplicationID(request.WorkspaceID) || !canonicalApplicationID(request.ModelRunRef) ||
		request.Retrieval.Validate() != nil || len(request.Runtime.Input) != 0 {
		return relationInvalid("relation request workspace, model run, or runtime input is invalid")
	}
	if err := request.Runtime.ProfileRef.Validate(); err != nil {
		return relationInvalid("relation request profile reference is invalid")
	}
	if err := request.Runtime.PromptRef.Validate(); err != nil {
		return relationInvalid("relation request prompt reference is invalid")
	}
	if err := request.Runtime.SchemaRef.Validate(); err != nil || request.Runtime.SchemaRef.ID != agentdomain.RelationAssessmentSchemaID || request.Runtime.SchemaRef.Version != agentdomain.OutputSchemaVersionV1 {
		return relationInvalid("relation request schema reference is invalid")
	}
	if err := request.Runtime.ReducedSchemaRef.Validate(); err != nil ||
		request.Runtime.ReducedSchemaRef.ID != RelationAssessmentReducedSchemaID || request.Runtime.ReducedSchemaRef.Version != agentdomain.OutputSchemaVersionV1 {
		return relationInvalid("relation request reduced schema reference is invalid")
	}
	if err := validateRelationClaimInput(request.WorkspaceID, request.Retrieval.IndexVersionID, request.Candidate); err != nil {
		return err
	}
	if request.Existing != nil {
		if err := validateRelationClaimInput(request.WorkspaceID, request.Retrieval.IndexVersionID, *request.Existing); err != nil {
			return err
		}
		if _, _, err := knowledgedomain.CanonicalizeRelationEndpoints(knowledgedomain.RelationComplements, request.Candidate.Node, request.Existing.Node); err != nil {
			return relationInvalid("relation candidate and existing endpoints must be distinct claims")
		}
	}
	total := len(request.Candidate.Evidence)
	if request.Existing != nil {
		total += len(request.Existing.Evidence)
	}
	if total == 0 || total > MaxRelationEvidence {
		return relationInvalid("relation evidence count is outside the supported range")
	}
	return nil
}

func validateRelationClaimInput(workspaceID, indexVersionID foundation.ID, input RelationClaimInput) error {
	if input.Node.Type != knowledgedomain.NodeTypeClaim || !canonicalApplicationID(input.Node.ID) ||
		!utf8.ValidString(input.Statement) || strings.TrimSpace(input.Statement) != input.Statement ||
		input.Statement == "" || len(input.Statement) > MaxRelationStatementBytes || len(input.Evidence) == 0 {
		return relationInvalid("relation claim identity, statement, or evidence is invalid")
	}
	if err := knowledgedomain.ValidateApplicability(input.Applicability); err != nil {
		return relationInvalid("relation claim applicability is invalid")
	}
	for _, evidence := range input.Evidence {
		if evidence.Citation.WorkspaceID != workspaceID || evidence.Citation.IndexVersionID != indexVersionID || evidence.Citation.Validate() != nil ||
			!utf8.ValidString(evidence.Excerpt) || strings.TrimSpace(evidence.Excerpt) != evidence.Excerpt || evidence.Excerpt == "" {
			return relationInvalid("relation evidence identity or excerpt is invalid")
		}
	}
	return nil
}

func (analyzer *RelationAnalyzer) loadEligibleEvidence(ctx context.Context, request RelationAnalyzeRequest) ([]agentdomain.Evidence, []agentdomain.Evidence, []agentdomain.RelationConflictDisclosure, error) {
	all := append([]RelationEvidenceInput(nil), request.Candidate.Evidence...)
	if request.Existing != nil {
		all = append(all, request.Existing.Evidence...)
	}
	provenance := make([]knowledgedomain.ProvenanceRef, 0, len(all))
	seenProvenance := make(map[knowledgedomain.ProvenanceRef]struct{}, len(all))
	seenCitations := make(map[string]struct{}, len(all))
	var indexVersionID foundation.ID
	for _, item := range all {
		if _, duplicate := seenCitations[item.Citation.ID]; duplicate {
			return nil, nil, nil, relationInvalid("relation evidence contains duplicate citation ids")
		}
		seenCitations[item.Citation.ID] = struct{}{}
		if indexVersionID == "" {
			indexVersionID = item.Citation.IndexVersionID
		} else if item.Citation.IndexVersionID != indexVersionID {
			return nil, nil, nil, relationInvalid("relation evidence crosses retrieval index versions")
		}
		ref := provenanceFromCitation(item.Citation)
		if _, duplicate := seenProvenance[ref]; duplicate {
			continue
		}
		seenProvenance[ref] = struct{}{}
		provenance = append(provenance, ref)
	}
	eligibility, err := analyzer.knowledge.CheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{
		WorkspaceID: request.WorkspaceID,
		Provenance:  provenance,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	byProvenance, err := indexEligibility(request.WorkspaceID, provenance, eligibility)
	if err != nil {
		return nil, nil, nil, err
	}
	disclosures, err := relationConflictDisclosures(eligibility)
	if err != nil {
		return nil, nil, nil, err
	}
	mapSide := func(inputs []RelationEvidenceInput, expectedOwnerID *foundation.ID) ([]agentdomain.Evidence, error) {
		mapped := make([]agentdomain.Evidence, 0, len(inputs))
		for _, input := range inputs {
			projection := byProvenance[provenanceFromCitation(input.Citation)]
			if projection.Eligibility == knowledgedomain.EvidenceIneligible {
				return nil, applicationError(foundation.ErrorPermissionDenied, errorCodeRelationEvidenceIneligible, false, errors.New("relation assessment evidence is not bound to approved knowledge"))
			}
			if expectedOwnerID != nil && !eligibilityBindsClaim(projection, *expectedOwnerID) {
				return nil, applicationError(foundation.ErrorPermissionDenied, errorCodeRelationEvidenceIneligible, false, errors.New("existing evidence eligibility is not bound to the formal existing claim"))
			}
			evidence, mapErr := agentdomain.EvidenceFromKnowledgeEligibility(input.Citation, input.Excerpt, projection)
			if mapErr != nil {
				return nil, mapErr
			}
			mapped = append(mapped, evidence)
		}
		return mapped, nil
	}
	candidate, err := mapSide(request.Candidate.Evidence, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	var existing []agentdomain.Evidence
	if request.Existing != nil {
		existing, err = mapSide(request.Existing.Evidence, &request.Existing.Node.ID)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return candidate, existing, disclosures, nil
}

func eligibilityBindsClaim(result knowledgedomain.ProvenanceEligibility, claimID foundation.ID) bool {
	for _, binding := range result.Bindings {
		if binding.OwnerType == knowledgedomain.EvidenceOwnerClaim && binding.OwnerID == claimID &&
			(binding.ClaimStatus == knowledgedomain.ClaimStatusConfirmed || binding.ClaimStatus == knowledgedomain.ClaimStatusDisputed) {
			return true
		}
	}
	return false
}

func relationConflictDisclosures(results []knowledgedomain.ProvenanceEligibility) ([]agentdomain.RelationConflictDisclosure, error) {
	type accumulated struct {
		applicability knowledgedomain.Applicability
		updatedAt     time.Time
		conflicts     map[foundation.ID]struct{}
	}
	byClaim := make(map[foundation.ID]accumulated)
	for _, result := range results {
		for _, binding := range result.Bindings {
			if binding.OwnerType != knowledgedomain.EvidenceOwnerClaim || binding.ClaimStatus != knowledgedomain.ClaimStatusDisputed {
				continue
			}
			value, exists := byClaim[binding.OwnerID]
			if exists && (value.applicability.Hash != binding.DisputedApplicability.Hash || !value.updatedAt.Equal(binding.DisputedClaimUpdatedAtUTC)) {
				return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, errors.New("disputed claim eligibility facts conflict"))
			}
			if !exists {
				value = accumulated{
					applicability: cloneApplicability(binding.DisputedApplicability),
					updatedAt:     binding.DisputedClaimUpdatedAtUTC.UTC(),
					conflicts:     make(map[foundation.ID]struct{}),
				}
			}
			for _, conflictID := range binding.ConflictIDs {
				value.conflicts[conflictID] = struct{}{}
			}
			byClaim[binding.OwnerID] = value
		}
	}
	claimIDs := make([]foundation.ID, 0, len(byClaim))
	for claimID := range byClaim {
		claimIDs = append(claimIDs, claimID)
	}
	slices.Sort(claimIDs)
	disclosures := make([]agentdomain.RelationConflictDisclosure, 0, len(claimIDs))
	for _, claimID := range claimIDs {
		value := byClaim[claimID]
		conflictIDs := make([]foundation.ID, 0, len(value.conflicts))
		for conflictID := range value.conflicts {
			conflictIDs = append(conflictIDs, conflictID)
		}
		slices.Sort(conflictIDs)
		disclosure := agentdomain.RelationConflictDisclosure{
			ClaimID: claimID, ConflictIDs: conflictIDs,
			Applicability: append(json.RawMessage(nil), value.applicability.CanonicalJSON...), UpdatedAt: value.updatedAt,
		}
		if err := disclosure.Validate(); err != nil {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, err)
		}
		disclosures = append(disclosures, disclosure)
	}
	return disclosures, nil
}

func indexEligibility(workspaceID foundation.ID, requested []knowledgedomain.ProvenanceRef, results []knowledgedomain.ProvenanceEligibility) (map[knowledgedomain.ProvenanceRef]knowledgedomain.ProvenanceEligibility, error) {
	if len(results) != len(requested) {
		return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, errors.New("knowledge returned an incomplete eligibility result"))
	}
	wanted := make(map[knowledgedomain.ProvenanceRef]struct{}, len(requested))
	for _, ref := range requested {
		wanted[ref] = struct{}{}
	}
	indexed := make(map[knowledgedomain.ProvenanceRef]knowledgedomain.ProvenanceEligibility, len(results))
	for _, result := range results {
		if result.Provenance.WorkspaceID != workspaceID || knowledgedomain.ValidateProvenanceEligibility(result) != nil {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, errors.New("knowledge returned invalid eligibility"))
		}
		if _, expected := wanted[result.Provenance]; !expected {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, errors.New("knowledge returned eligibility for an unexpected provenance"))
		}
		if _, duplicate := indexed[result.Provenance]; duplicate {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationEligibilityInvalid, false, errors.New("knowledge returned duplicate eligibility"))
		}
		indexed[result.Provenance] = cloneProvenanceEligibility(result)
	}
	return indexed, nil
}

func validateRelationOutput(
	request RelationAnalyzeRequest,
	payload agentdomain.RelationAssessmentPayload,
	candidate, existing []agentdomain.Evidence,
	disclosures []agentdomain.RelationConflictDisclosure,
) error {
	if !sameRelationConflictDisclosures(payload.ConflictDisclosures, disclosures) {
		return relationOutputMismatch("conflict disclosures differ from formal disputed knowledge")
	}
	if !sameApplicability(payload.Applicability.Candidate, request.Candidate.Applicability) {
		return relationOutputMismatch("candidate applicability differs from the frozen request")
	}
	candidateRefs := evidenceReferenceSet(candidate)
	if !referencesBelongTo(payload.CandidateEvidenceRefs, candidateRefs) {
		return relationOutputMismatch("candidate evidence reference is unknown")
	}
	if payload.Assessment == knowledgedomain.AssessmentNew {
		if request.Existing != nil || len(payload.ExistingEvidenceRefs) != 0 || len(bytes.TrimSpace(payload.Applicability.Existing)) != 0 {
			return relationOutputMismatch("new assessment contains an existing-side binding")
		}
		return nil
	}
	if request.Existing == nil {
		return relationOutputMismatch("non-new assessment has no existing claim")
	}
	if !sameApplicability(payload.Applicability.Existing, request.Existing.Applicability) {
		return relationOutputMismatch("existing applicability differs from the frozen request")
	}
	if !referencesBelongTo(payload.ExistingEvidenceRefs, evidenceReferenceSet(existing)) {
		return relationOutputMismatch("existing evidence reference is unknown")
	}
	return nil
}

func validateSafeAssessmentAction(assessment knowledgedomain.RelationAssessment, source, target knowledgedomain.NodeRef, action knowledgedomain.AssessmentAction) error {
	unsafe := func() error {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationActionUnsafe, false, errors.New("knowledge assessment mapping returned an unsafe candidate action"))
	}
	expected, err := knowledgedomain.MapAssessment(assessment, source, target, false)
	if err != nil || action.Decision != expected.Decision || action.Source != expected.Source || action.Target != expected.Target || action.OpenConflict != expected.OpenConflict ||
		!sameRelationType(action.RelationType, expected.RelationType) {
		return unsafe()
	}
	return nil
}

func sameRelationType(left, right *knowledgedomain.RelationType) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type relationModelInput struct {
	WorkspaceID         foundation.ID                            `json:"workspace_id"`
	ModelRunRef         foundation.ID                            `json:"model_run_ref"`
	ConflictDisclosures []agentdomain.RelationConflictDisclosure `json:"conflict_disclosures"`
	Candidate           relationModelClaim                       `json:"candidate"`
	Existing            *relationModelClaim                      `json:"existing,omitempty"`
}

type relationModelClaim struct {
	Node          relationModelNode      `json:"node"`
	Statement     string                 `json:"statement"`
	Applicability json.RawMessage        `json:"applicability"`
	Evidence      []agentdomain.Evidence `json:"evidence"`
}

type relationModelNode struct {
	Type knowledgedomain.NodeType `json:"type"`
	ID   foundation.ID            `json:"id"`
}

func encodeRelationModelInput(
	request RelationAnalyzeRequest,
	candidate, existing []agentdomain.Evidence,
	disclosures []agentdomain.RelationConflictDisclosure,
) ([]byte, error) {
	input := relationModelInput{
		WorkspaceID: request.WorkspaceID, ModelRunRef: request.ModelRunRef,
		ConflictDisclosures: cloneRelationConflictDisclosures(disclosures),
		Candidate: relationModelClaim{
			Node: relationModelNode{Type: request.Candidate.Node.Type, ID: request.Candidate.Node.ID}, Statement: request.Candidate.Statement,
			Applicability: append(json.RawMessage(nil), request.Candidate.Applicability.CanonicalJSON...),
			Evidence:      append([]agentdomain.Evidence(nil), candidate...),
		},
	}
	if request.Existing != nil {
		input.Existing = &relationModelClaim{
			Node: relationModelNode{Type: request.Existing.Node.Type, ID: request.Existing.Node.ID}, Statement: request.Existing.Statement,
			Applicability: append(json.RawMessage(nil), request.Existing.Applicability.CanonicalJSON...),
			Evidence:      append([]agentdomain.Evidence(nil), existing...),
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRelationRequestInvalid, false, err)
	}
	if len(encoded) > MaxStructuredInputBytes {
		return nil, relationInvalid("relation model input exceeds the structured input limit")
	}
	return encoded, nil
}

func evidenceReferenceSet(evidence []agentdomain.Evidence) map[string]struct{} {
	result := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		result[item.Citation.ID] = struct{}{}
	}
	return result
}

func referencesBelongTo(references []string, evidence map[string]struct{}) bool {
	for _, reference := range references {
		if _, exists := evidence[reference]; !exists {
			return false
		}
	}
	return true
}

func sameApplicability(raw json.RawMessage, expected knowledgedomain.Applicability) bool {
	parsed, err := knowledgedomain.ParseApplicability(raw)
	return err == nil && parsed.Hash == expected.Hash && bytes.Equal(parsed.CanonicalJSON, expected.CanonicalJSON)
}

func sameRelationConflictDisclosures(left, right []agentdomain.RelationConflictDisclosure) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftApplicability, leftErr := knowledgedomain.ParseApplicability(left[index].Applicability)
		rightApplicability, rightErr := knowledgedomain.ParseApplicability(right[index].Applicability)
		if left[index].ClaimID != right[index].ClaimID || !left[index].UpdatedAt.Equal(right[index].UpdatedAt) ||
			!slices.Equal(left[index].ConflictIDs, right[index].ConflictIDs) ||
			leftErr != nil || rightErr != nil || leftApplicability.Hash != rightApplicability.Hash ||
			!bytes.Equal(leftApplicability.CanonicalJSON, rightApplicability.CanonicalJSON) {
			return false
		}
	}
	return true
}

func provenanceFromCitation(citation agentdomain.Citation) knowledgedomain.ProvenanceRef {
	return knowledgedomain.ProvenanceRef{
		WorkspaceID: citation.WorkspaceID, SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
	}
}

func cloneProvenanceEligibility(input knowledgedomain.ProvenanceEligibility) knowledgedomain.ProvenanceEligibility {
	cloned := input
	cloned.Bindings = append([]knowledgedomain.EvidenceEligibilityBinding(nil), input.Bindings...)
	for index := range cloned.Bindings {
		cloned.Bindings[index].ConflictIDs = append([]foundation.ID(nil), input.Bindings[index].ConflictIDs...)
		cloned.Bindings[index].DisputedApplicability.CanonicalJSON = append(
			json.RawMessage(nil), input.Bindings[index].DisputedApplicability.CanonicalJSON...,
		)
	}
	return cloned
}

func cloneApplicability(input knowledgedomain.Applicability) knowledgedomain.Applicability {
	cloned := input
	cloned.CanonicalJSON = append(json.RawMessage(nil), input.CanonicalJSON...)
	return cloned
}

func cloneRelationConflictDisclosures(input []agentdomain.RelationConflictDisclosure) []agentdomain.RelationConflictDisclosure {
	cloned := make([]agentdomain.RelationConflictDisclosure, len(input))
	copy(cloned, input)
	for index := range cloned {
		cloned[index].ConflictIDs = append([]foundation.ID(nil), input[index].ConflictIDs...)
		cloned[index].Applicability = append(json.RawMessage(nil), input[index].Applicability...)
	}
	return cloned
}

func cloneStructuredRunResult(input StructuredRunResult) StructuredRunResult {
	cloned := input
	cloned.Output = append(json.RawMessage(nil), input.Output...)
	return cloned
}

func relationInvalid(message string) error {
	return applicationError(foundation.ErrorInvalidInput, errorCodeRelationRequestInvalid, false, errors.New(message))
}

func relationOutputMismatch(message string) error {
	return applicationError(foundation.ErrorConsistencyViolation, errorCodeRelationOutputMismatch, false, errors.New(message))
}
