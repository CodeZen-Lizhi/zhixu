package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

func (repository *GORMWorkspaceAnalysisRepository) dynamicEvidenceParticipant() (agentapplication.ScopedWorkspaceAnalysisEvidenceParticipant, error) {
	p, ok := repository.participant.(agentapplication.ScopedWorkspaceAnalysisEvidenceParticipant)
	if !ok || nilGORMToolsDependency(p) {
		return nil, gormToolsUnavailable(errors.New("dynamic evidence authority is unavailable"))
	}
	return p, nil
}

func (repository *GORMWorkspaceAnalysisRepository) dynamicToolAdmissionDenial(ctx context.Context, scope foundation.TransactionScope, snapshot agentapplication.WorkspaceAnalysisToolOperationSnapshot, definition domain.Definition) (*agentapplication.WorkspaceAnalysisAdmissionDenial, error) {
	run, operation := snapshot.Run, snapshot.Operation
	requested := agentdomain.WorkspaceAnalysisBudgetAmount{ToolCalls: 1}
	if operation.Kind == agentdomain.WorkspaceAnalysisOperationSourceRead {
		requested.SourceReads = 1
	}
	deny := func(reason agentdomain.WorkspaceAnalysisRunTerminationReason) *agentapplication.WorkspaceAnalysisAdmissionDenial {
		return &agentapplication.WorkspaceAnalysisAdmissionDenial{OperationID: operation.ID, Reason: reason, Requested: requested}
	}
	if snapshot.DatabaseNow.Add(definition.Timeout + agentdomain.WorkspaceAnalysisV2DurableCompletionMargin).After(run.DeadlineAt) {
		return deny(agentdomain.WorkspaceAnalysisRunDeadlineExceeded), nil
	}
	maxTools := run.Limits.Amount.ToolCalls
	if operation.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeDecideNext {
		maxTools--
	}
	if run.Reserved.ToolCalls+run.Settled.ToolCalls+requested.ToolCalls > maxTools || run.Reserved.SourceReads+run.Settled.SourceReads+requested.SourceReads > run.Limits.Amount.SourceReads {
		return deny(agentdomain.WorkspaceAnalysisRunBudgetExhausted), nil
	}
	if operation.Kind == agentdomain.WorkspaceAnalysisOperationKnowledgeSearch {
		bindings, err := repository.dynamicBindings(ctx, scope, run.WorkspaceID, run.WorkflowRunID, run.ID)
		if err != nil {
			return nil, err
		}
		if len(bindings) >= agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs {
			return deny(agentdomain.WorkspaceAnalysisRunBudgetExhausted), nil
		}
	}
	return nil, nil
}

func (repository *GORMWorkspaceAnalysisRepository) recoverDynamicToolAdmissionDenial(ctx context.Context, command application.AuthorizeWorkspaceAnalysisToolCallCommand, call domain.ToolCall, definition domain.Definition, arguments json.RawMessage, denial *agentapplication.WorkspaceAnalysisAdmissionDenial, commitErr error) error {
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ *gorm.DB, scope foundation.TransactionScope) error {
		_, identity, err := repository.lockExecution(callbackCtx, scope, command.Identity)
		if err != nil {
			return err
		}
		snapshot, err := repository.participant.PrepareWorkspaceAnalysisToolOperationScoped(callbackCtx, scope, agentapplication.PrepareWorkspaceAnalysisToolOperationCommand{
			Identity: identity, OperationKey: command.OperationKey, CandidateOperationID: command.OperationID,
			RequestHash: call.RequestHash, ExpectedToolCatalogHash: workspaceAnalysisCatalogHashForVersion(command.Identity.DefinitionVersion),
			Arguments: arguments, RequireExisting: true,
		})
		if err != nil {
			return err
		}
		if snapshot.Operation.ID != denial.OperationID || snapshot.Operation.Status != agentdomain.WorkspaceAnalysisOperationPending ||
			snapshot.Operation.Call != nil || snapshot.Reservation != nil {
			return consistency(errors.New("dynamic tool denial has no exact pending operation"))
		}
		actual, err := repository.dynamicToolAdmissionDenial(callbackCtx, scope, snapshot, definition)
		if err != nil {
			return err
		}
		if actual == nil || actual.Reason != denial.Reason || actual.Requested != denial.Requested {
			return consistency(errors.New("dynamic tool denial cannot be recovered from durable budgets"))
		}
		return nil
	})
	if err != nil {
		return classifyGORMTools(ctx, errors.Join(commitErr, err))
	}
	return denial
}

func (repository *GORMWorkspaceAnalysisRepository) appendDynamicSearchEvidence(ctx context.Context, scope foundation.TransactionScope, identity agentapplication.WorkspaceAnalysisToolExecutionIdentity, operation agentdomain.WorkspaceAnalysisOperation, receipt domain.ResultReceipt) error {
	p, err := repository.dynamicEvidenceParticipant()
	if err != nil {
		return err
	}
	identities, err := domain.SearchKnowledgeV3ReceiptIdentities(receipt)
	if err != nil {
		return consistency(err)
	}
	items := make([]agentapplication.WorkspaceAnalysisEvidenceSeed, len(identities))
	for i, item := range identities {
		items[i] = agentapplication.WorkspaceAnalysisEvidenceSeed{LocalEvidenceRef: item.EvidenceRef, CitationID: item.CitationID, IndexVersionID: item.IndexVersionID, ChunkID: item.ChunkID, SourceVersionID: item.SourceVersionID, SourceSpanID: item.SourceSpanID, ContentHash: item.ContentHash}
	}
	return p.AppendWorkspaceAnalysisEvidenceScoped(ctx, scope, agentapplication.AppendWorkspaceAnalysisEvidenceCommand{Identity: identity, OperationKey: operation.LogicalKey(), OperationID: operation.ID, ReceiptID: receipt.ID, ReceiptHash: receipt.OutputHash, Items: items})
}

func (repository *GORMWorkspaceAnalysisRepository) dynamicBindings(ctx context.Context, scope foundation.TransactionScope, workspaceID, workflowRunID, analysisRunID foundation.ID) ([]agentapplication.WorkspaceAnalysisEvidenceBinding, error) {
	p, err := repository.dynamicEvidenceParticipant()
	if err != nil {
		return nil, err
	}
	return p.LoadWorkspaceAnalysisEvidenceScoped(ctx, scope, agentapplication.WorkspaceAnalysisEvidenceQuery{WorkspaceID: workspaceID, WorkflowRunID: workflowRunID, AnalysisRunID: analysisRunID})
}

func validateDynamicToolQuery(q application.WorkspaceAnalysisDynamicToolQuery) error {
	if !validAuthorityIdentitySet(q.WorkspaceID, q.WorkflowRunID, q.AnalysisRunID) || q.OperationKey.AnalysisRunID != q.AnalysisRunID || q.OperationKey.Validate() != nil {
		return authorityInputError(errors.New("dynamic tool query is invalid"))
	}
	c, err := agentdomain.WorkspaceAnalysisOperationContractForKey(q.OperationKey)
	if err != nil || c.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool {
		return authorityInputError(errors.New("dynamic tool query does not identify a tool"))
	}
	return nil
}

func (repository *GORMWorkspaceAnalysisRepository) WorkspaceAnalysisDynamicSearchLimit(ctx context.Context, q application.WorkspaceAnalysisDynamicToolQuery) (int, error) {
	if ctx == nil || validateDynamicToolQuery(q) != nil || q.OperationKey.NodeKey != agentdomain.WorkspaceAnalysisOperationNodeDecideNext || q.OperationKey.Kind != agentdomain.WorkspaceAnalysisOperationKnowledgeSearch {
		return 0, authorityInputError(errors.New("dynamic search capacity query is invalid"))
	}
	limit := 0
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ *gorm.DB, scope foundation.TransactionScope) error {
		bindings, err := repository.dynamicBindings(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID, q.AnalysisRunID)
		if err != nil {
			return err
		}
		count := 0
		for _, b := range bindings {
			if b.SearchOperationKey.Ordinal < q.OperationKey.Ordinal {
				count++
			}
		}
		limit = min(5, agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs-count)
		return nil
	})
	return limit, err
}

func (repository *GORMWorkspaceAnalysisRepository) LoadWorkspaceAnalysisDynamicToolOutput(ctx context.Context, q application.WorkspaceAnalysisDynamicToolQuery) (json.RawMessage, error) {
	if ctx == nil || validateDynamicToolQuery(q) != nil {
		return nil, authorityInputError(errors.New("dynamic tool output query is invalid"))
	}
	var output json.RawMessage
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, db *gorm.DB, scope foundation.TransactionScope) error {
		run, err := repository.gormLoadWorkspaceAnalysisRunAuthority(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID)
		if err != nil {
			return err
		}
		if run.DefinitionVersion != 2 || run.AnalysisRunID != q.AnalysisRunID {
			return receiptNotFound(errors.New("dynamic tool run is absent"))
		}
		contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(q.OperationKey)
		if err != nil {
			return err
		}
		ref, ok := workspaceAnalysisToolForOperationVersion(contract, 2)
		if !ok {
			return authorityInputError(errors.New("dynamic tool is unsupported"))
		}
		operation, r, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceiptAuthority(callbackCtx, db, scope, q.WorkspaceID, q.WorkflowRunID, q.OperationKey, ref)
		if err != nil {
			return err
		}
		switch ref.Name {
		case "ReadGitStatus":
			output, err = domain.ReadGitStatusV3ReceiptModelOutput(r)
		case "SearchKnowledge":
			identities, decodeErr := domain.SearchKnowledgeV3ReceiptIdentities(r)
			if decodeErr != nil {
				return consistency(decodeErr)
			}
			bindings, loadErr := repository.dynamicBindings(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID, q.AnalysisRunID)
			if loadErr != nil {
				return loadErr
			}
			refs := map[string]string{}
			for _, b := range bindings {
				if b.SearchReceiptID == r.ID {
					if b.SearchReceiptHash != r.OutputHash || b.SearchOperationKey != q.OperationKey || b.SearchOperationID != operation.OperationID ||
						refs[b.LocalEvidenceRef] != "" || !slices.Contains(identities, dynamicLocalIdentity(b)) {
						return consistency(errors.New("dynamic search alias receipt drifted"))
					}
					refs[b.LocalEvidenceRef] = "E" + strconv.Itoa(b.ReferenceNo)
				}
			}
			output, err = domain.SearchKnowledgeV3ReceiptModelOutput(r, refs)
		case "ReadSource":
			binding, bindErr := domain.ReadSourceV4ReceiptBinding(r)
			if bindErr != nil {
				return bindErr
			}
			a, loadErr := repository.dynamicSourceAuthority(callbackCtx, db, scope, q.WorkspaceID, q.WorkflowRunID, q.AnalysisRunID, binding.Identity.EvidenceRef)
			if loadErr != nil {
				return loadErr
			}
			evidence, readErr := domain.ReadSourceV4ReceiptEvidenceForSearch(r, a.SearchReceipt)
			if readErr != nil {
				return readErr
			}
			output, err = json.Marshal(struct {
				EvidenceRef string `json:"evidence_ref"`
				Excerpt     string `json:"excerpt"`
				Truncated   bool   `json:"truncated"`
			}{evidence.EvidenceRef, evidence.Excerpt, evidence.Truncated})
		case "ValidateCitation":
			output = append(json.RawMessage(nil), r.Output...)
		default:
			return authorityInputError(errors.New("dynamic tool output is unsupported"))
		}
		return err
	})
	return output, err
}

func (repository *GORMWorkspaceAnalysisRepository) LoadReadSourceV4Authority(ctx context.Context, q application.ReadSourceV4AuthorityQuery) (application.ReadSourceV4Authority, error) {
	if ctx == nil || !validAuthorityIdentitySet(q.WorkspaceID, q.WorkflowRunID) || !domain.ValidDynamicEvidenceRef(q.EvidenceRef) {
		return application.ReadSourceV4Authority{}, authorityInputError(errors.New("dynamic source query is invalid"))
	}
	var result application.ReadSourceV4Authority
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, db *gorm.DB, scope foundation.TransactionScope) error {
		run, err := repository.gormLoadWorkspaceAnalysisRunAuthority(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID)
		if err != nil {
			return err
		}
		if run.DefinitionVersion != 2 {
			return receiptNotFound(errors.New("dynamic source run is absent"))
		}
		result, err = repository.dynamicSourceAuthority(callbackCtx, db, scope, q.WorkspaceID, q.WorkflowRunID, run.AnalysisRunID, q.EvidenceRef)
		return err
	})
	return result, err
}

func (repository *GORMWorkspaceAnalysisRepository) dynamicSourceAuthority(ctx context.Context, db *gorm.DB, scope foundation.TransactionScope, workspaceID, workflowRunID, analysisRunID foundation.ID, ref string) (application.ReadSourceV4Authority, error) {
	bindings, err := repository.dynamicBindings(ctx, scope, workspaceID, workflowRunID, analysisRunID)
	if err != nil {
		return application.ReadSourceV4Authority{}, err
	}
	for _, b := range bindings {
		if "E"+strconv.Itoa(b.ReferenceNo) != ref {
			continue
		}
		op, r, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceiptAuthority(ctx, db, scope, workspaceID, workflowRunID, b.SearchOperationKey, domain.ToolRef{Name: "SearchKnowledge", Version: 3})
		if err != nil {
			return application.ReadSourceV4Authority{}, err
		}
		if op.OperationID != b.SearchOperationID || r.ID != b.SearchReceiptID || r.OutputHash != b.SearchReceiptHash {
			return application.ReadSourceV4Authority{}, consistency(errors.New("dynamic source Search authority drifted"))
		}
		identities, err := domain.SearchKnowledgeV3ReceiptIdentities(r)
		if err != nil {
			return application.ReadSourceV4Authority{}, err
		}
		want := dynamicLocalIdentity(b)
		if !slices.Contains(identities, want) {
			return application.ReadSourceV4Authority{}, consistency(errors.New("dynamic source alias tuple drifted"))
		}
		want.EvidenceRef = ref
		return application.ReadSourceV4Authority{WorkspaceID: workspaceID, WorkflowRunID: workflowRunID, AnalysisRunID: analysisRunID, EvidenceRef: ref, SearchEvidenceRef: b.LocalEvidenceRef, SearchReceipt: r, Identity: want}, nil
	}
	return application.ReadSourceV4Authority{}, receiptNotFound(errors.New("dynamic source reference is absent"))
}

func dynamicLocalIdentity(binding agentapplication.WorkspaceAnalysisEvidenceBinding) domain.DynamicEvidenceIdentity {
	return domain.DynamicEvidenceIdentity{EvidenceRef: binding.LocalEvidenceRef, CitationID: binding.CitationID, IndexVersionID: binding.IndexVersionID, ChunkID: binding.ChunkID, SourceVersionID: binding.SourceVersionID, SourceSpanID: binding.SourceSpanID, ContentHash: binding.ContentHash}
}

func (repository *GORMWorkspaceAnalysisRepository) dynamicSynthesisEvidence(ctx context.Context, db *gorm.DB, scope foundation.TransactionScope, q application.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery) (application.WorkspaceAnalysisSynthesisEvidenceAuthority, error) {
	p, err := repository.dynamicEvidenceParticipant()
	if err != nil {
		return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
	}
	keys, err := p.LoadWorkspaceAnalysisSuccessfulReadKeysScoped(ctx, scope, agentapplication.WorkspaceAnalysisEvidenceQuery{WorkspaceID: q.WorkspaceID, WorkflowRunID: q.WorkflowRunID, AnalysisRunID: q.AnalysisRunID})
	if err != nil {
		return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
	}
	type readPair struct {
		read, search domain.ResultReceipt
		evidence     domain.ReadSourceV3ReceiptEvidence
	}
	byRef := map[string]readPair{}
	for _, key := range keys {
		r, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceipt(ctx, db, scope, q.WorkspaceID, q.WorkflowRunID, key, domain.ToolRef{Name: "ReadSource", Version: 4})
		if err != nil {
			return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
		}
		binding, err := domain.ReadSourceV4ReceiptBinding(r)
		if err != nil {
			return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
		}
		a, err := repository.dynamicSourceAuthority(ctx, db, scope, q.WorkspaceID, q.WorkflowRunID, q.AnalysisRunID, binding.Identity.EvidenceRef)
		if err != nil {
			return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
		}
		if a.Identity != binding.Identity || a.SearchEvidenceRef != binding.SearchEvidenceRef {
			return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, consistency(errors.New("dynamic read alias binding drifted"))
		}
		evidence, err := domain.ReadSourceV4ReceiptEvidenceForSearch(r, a.SearchReceipt)
		if err != nil {
			return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, err
		}
		if previous, found := byRef[evidence.EvidenceRef]; found {
			if previous.evidence != evidence {
				return application.WorkspaceAnalysisSynthesisEvidenceAuthority{}, consistency(errors.New("repeated dynamic read changed immutable evidence"))
			}
			continue
		}
		byRef[evidence.EvidenceRef] = readPair{read: r, search: a.SearchReceipt, evidence: evidence}
	}
	result := application.WorkspaceAnalysisSynthesisEvidenceAuthority{WorkspaceID: q.WorkspaceID, WorkflowRunID: q.WorkflowRunID, AnalysisRunID: q.AnalysisRunID, EvidenceRefs: make([]string, 0, len(byRef)), SearchReceipts: []domain.ResultReceipt{}, ReadSourceReceipts: []domain.ResultReceipt{}, Evidence: []domain.ReadSourceV3ReceiptEvidence{}}
	for ref := range byRef {
		result.EvidenceRefs = append(result.EvidenceRefs, ref)
	}
	sort.Slice(result.EvidenceRefs, func(i, j int) bool {
		a, _ := strconv.Atoi(result.EvidenceRefs[i][1:])
		b, _ := strconv.Atoi(result.EvidenceRefs[j][1:])
		return a < b
	})
	seenSearch := map[foundation.ID]bool{}
	for _, ref := range result.EvidenceRefs {
		pair := byRef[ref]
		if !seenSearch[pair.search.ID] {
			seenSearch[pair.search.ID] = true
			result.SearchReceipts = append(result.SearchReceipts, pair.search)
		}
		result.ReadSourceReceipts = append(result.ReadSourceReceipts, pair.read)
		result.Evidence = append(result.Evidence, pair.evidence)
	}
	return result, nil
}

func (repository *GORMWorkspaceAnalysisRepository) LoadValidateCitationV4Authority(ctx context.Context, q application.ValidateCitationV4AuthorityQuery) (application.ValidateCitationV4Authority, error) {
	if ctx == nil || !validAuthorityIdentitySet(q.WorkspaceID, q.WorkflowRunID) || len(q.EvidenceRefs) < 1 || len(q.EvidenceRefs) > agentdomain.WorkspaceAnalysisV2MaxSourceReads || (q.CandidateID == "") != (q.CandidateHash == "") {
		return application.ValidateCitationV4Authority{}, authorityInputError(errors.New("dynamic citation query is invalid"))
	}
	seen := map[string]bool{}
	for _, ref := range q.EvidenceRefs {
		if !domain.ValidDynamicEvidenceRef(ref) || seen[ref] {
			return application.ValidateCitationV4Authority{}, authorityInputError(errors.New("dynamic citation references are invalid"))
		}
		seen[ref] = true
	}
	var result application.ValidateCitationV4Authority
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, db *gorm.DB, scope foundation.TransactionScope) error {
		run, err := repository.gormLoadWorkspaceAnalysisRunAuthority(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID)
		if err != nil {
			return err
		}
		if run.DefinitionVersion != 2 {
			return receiptNotFound(errors.New("dynamic citation run is absent"))
		}
		if q.CandidateID != "" {
			candidate, err := repository.gormLoadWorkspaceAnalysisCandidateAuthority(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID, q.CandidateID)
			if err != nil {
				return err
			}
			if candidate.AnalysisRunID != run.AnalysisRunID || candidate.SchemaVersion != 2 || candidate.CandidateHash != q.CandidateHash || !slices.Equal(candidate.CitationRefs, q.EvidenceRefs) {
				return consistency(errors.New("dynamic citation candidate binding drifted"))
			}
		}
		evidence, err := repository.dynamicSynthesisEvidence(callbackCtx, db, scope, application.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery{WorkspaceID: q.WorkspaceID, WorkflowRunID: q.WorkflowRunID, AnalysisRunID: run.AnalysisRunID})
		if err != nil {
			return err
		}
		result = application.ValidateCitationV4Authority{WorkspaceID: q.WorkspaceID, WorkflowRunID: q.WorkflowRunID, AnalysisRunID: run.AnalysisRunID, CandidateID: q.CandidateID, CandidateHash: q.CandidateHash, EvidenceRefs: append([]string(nil), q.EvidenceRefs...), SearchReceipts: make([]domain.ResultReceipt, len(q.EvidenceRefs)), ReadSourceReceipts: make([]domain.ResultReceipt, len(q.EvidenceRefs))}
		searchByID := map[foundation.ID]domain.ResultReceipt{}
		for _, search := range evidence.SearchReceipts {
			searchByID[search.ID] = search
		}
		for i, ref := range q.EvidenceRefs {
			index := slices.Index(evidence.EvidenceRefs, ref)
			if index < 0 {
				return receiptNotFound(errors.New("dynamic citation evidence has not been read"))
			}
			read := evidence.ReadSourceReceipts[index]
			binding, err := domain.ReadSourceV4ReceiptBinding(read)
			if err != nil {
				return err
			}
			search, found := searchByID[binding.SearchReceiptID]
			if !found {
				return consistency(errors.New("dynamic citation Search receipt is absent"))
			}
			result.ReadSourceReceipts[i] = read
			result.SearchReceipts[i] = search
		}
		return nil
	})
	return result, err
}

func (repository *GORMWorkspaceAnalysisRepository) LoadValidateCitationV4Receipt(ctx context.Context, q application.ValidateCitationV4ReceiptQuery) (domain.ResultReceipt, error) {
	a, err := repository.LoadValidateCitationV4PublicationAuthority(ctx, application.ValidateCitationV4PublicationAuthorityQuery{WorkspaceID: q.WorkspaceID, WorkflowRunID: q.WorkflowRunID, AnalysisRunID: q.AnalysisRunID, CandidateID: q.CandidateID, CandidateHash: q.CandidateHash})
	return a.Receipt, err
}

func (repository *GORMWorkspaceAnalysisRepository) LoadValidateCitationV4PublicationAuthority(ctx context.Context, q application.ValidateCitationV4PublicationAuthorityQuery) (application.ValidateCitationV4PublicationAuthority, error) {
	if ctx == nil || !validAuthorityIdentitySet(q.WorkspaceID, q.WorkflowRunID, q.AnalysisRunID, q.CandidateID) || !canonicalAuthorityHash(q.CandidateHash) || (q.ReceiptID == "") != (q.ReceiptHash == "") || q.ReceiptID != "" && (!canonicalAuthorityID(q.ReceiptID) || !canonicalAuthorityHash(q.ReceiptHash)) {
		return application.ValidateCitationV4PublicationAuthority{}, authorityInputError(errors.New("dynamic publication query is invalid"))
	}
	var result application.ValidateCitationV4PublicationAuthority
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, db *gorm.DB, scope foundation.TransactionScope) error {
		run, err := repository.gormLoadWorkspaceAnalysisRunAuthority(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID)
		if err != nil {
			return err
		}
		if run.DefinitionVersion != 2 || run.AnalysisRunID != q.AnalysisRunID {
			return receiptNotFound(errors.New("dynamic publication run is absent"))
		}
		candidate, err := repository.gormLoadWorkspaceAnalysisCandidateAuthority(callbackCtx, scope, q.WorkspaceID, q.WorkflowRunID, q.CandidateID)
		if err != nil {
			return err
		}
		if candidate.AnalysisRunID != q.AnalysisRunID || candidate.SchemaVersion != 2 || candidate.CandidateHash != q.CandidateHash {
			return consistency(errors.New("dynamic publication candidate drifted"))
		}
		a, r, err := repository.gormLoadWorkspaceAnalysisSuccessfulReceiptAuthority(callbackCtx, db, scope, q.WorkspaceID, q.WorkflowRunID, gormWorkspaceAnalysisOperationKey(q.AnalysisRunID, agentdomain.WorkspaceAnalysisOperationNodeValidateCitations, agentdomain.WorkspaceAnalysisOperationCitationValidation, 1), domain.ToolRef{Name: "ValidateCitation", Version: 4})
		if err != nil {
			return err
		}
		if q.ReceiptID != "" && (q.ReceiptID != r.ID || q.ReceiptHash != r.OutputHash) {
			return consistency(errors.New("dynamic publication receipt drifted"))
		}
		if _, err := domain.ValidateCitationV4ReceiptResults(r, q.CandidateID, q.CandidateHash, candidate.CitationRefs); err != nil {
			return consistency(err)
		}
		result = application.ValidateCitationV4PublicationAuthority{OperationID: a.OperationID, Receipt: r}
		return nil
	})
	return result, err
}

var _ application.WorkspaceAnalysisDynamicToolAuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ application.ReadSourceV4AuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ application.ValidateCitationV4AuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ application.ValidateCitationV4ReceiptReader = (*GORMWorkspaceAnalysisRepository)(nil)
var _ application.ValidateCitationV4PublicationAuthorityReader = (*GORMWorkspaceAnalysisRepository)(nil)
