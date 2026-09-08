package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestWorkspaceAnalysisBuiltinDefinitionReturnsOnlyCanonicalExactContracts(t *testing.T) {
	for _, ref := range []toolsdomain.ToolRef{workspaceAnalysisSearchRef, workspaceAnalysisSourceRef, workspaceAnalysisCitationRef} {
		definition, err := workspaceAnalysisBuiltinDefinition(ref)
		if err != nil {
			t.Fatalf("definition %s@%d: %v", ref.Name, ref.Version, err)
		}
		if definition.Ref != ref || definition.ResultPersistencePolicy != toolsdomain.ResultPersistenceCanonical ||
			definition.DefinitionHash == "" {
			t.Fatalf("definition=%+v", definition)
		}
	}
	if _, err := workspaceAnalysisBuiltinDefinition(toolsdomain.ToolRef{Name: "ReadSource", Version: 99}); err == nil {
		t.Fatal("unknown workspace analysis definition was accepted")
	}
}

func TestWorkspaceAnalysisAuthorityReadersRejectUntrustedIdentityBeforeDatabase(t *testing.T) {
	repository := &GORMWorkspaceAnalysisRepository{}
	if _, err := repository.LoadSearchKnowledgeV2Receipt(context.Background(), "not-an-id", authorityTestID(2)); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("search error=%v", err)
	}
	validSearchPublicationQuery := toolsapplication.SearchKnowledgeV2PublicationAuthorityQuery{
		WorkspaceID: authorityTestID(1), WorkflowRunID: authorityTestID(2), AnalysisRunID: authorityTestID(3),
		ReceiptID: authorityTestID(4), ReceiptHash: strings.Repeat("a", 64),
	}
	invalidSearchPublicationQuery := validSearchPublicationQuery
	invalidSearchPublicationQuery.ReceiptID = invalidSearchPublicationQuery.AnalysisRunID
	if _, err := repository.LoadSearchKnowledgeV2PublicationAuthority(
		context.Background(), invalidSearchPublicationQuery,
	); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("search publication reused identity error=%v", err)
	}
	invalidSearchPublicationQuery = validSearchPublicationQuery
	invalidSearchPublicationQuery.ReceiptHash = strings.Repeat("A", 64)
	if _, err := repository.LoadSearchKnowledgeV2PublicationAuthority(
		context.Background(), invalidSearchPublicationQuery,
	); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("search publication hash error=%v", err)
	}
	if _, err := repository.LoadSearchKnowledgeV2PublicationAuthority(
		context.Background(), validSearchPublicationQuery,
	); authorityErrorCode(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("valid search publication query did not reach repository dependency: %v", err)
	}
	if _, err := repository.LoadValidateCitationV3Authority(context.Background(), toolsapplication.ValidateCitationV3AuthorityQuery{
		WorkspaceID: authorityTestID(1), WorkflowRunID: authorityTestID(2), CandidateID: "not-an-id",
	}); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("citation error=%v", err)
	}
	if _, err := repository.LoadWorkspaceAnalysisSynthesisEvidence(
		context.Background(),
		toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery{
			WorkspaceID: authorityTestID(1), WorkflowRunID: authorityTestID(2), AnalysisRunID: "not-an-id",
		},
	); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("synthesis evidence error=%v", err)
	}
	if _, err := repository.LoadValidateCitationV3Receipt(
		context.Background(),
		toolsapplication.ValidateCitationV3ReceiptQuery{
			WorkspaceID: authorityTestID(1), WorkflowRunID: authorityTestID(2),
			AnalysisRunID: authorityTestID(3), CandidateID: authorityTestID(4), CandidateHash: "not-a-hash",
		},
	); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("citation receipt error=%v", err)
	}
	if _, err := repository.LoadValidateCitationV3Receipt(
		context.Background(),
		toolsapplication.ValidateCitationV3ReceiptQuery{
			WorkspaceID: authorityTestID(1), WorkflowRunID: authorityTestID(2),
			AnalysisRunID: authorityTestID(3), CandidateID: authorityTestID(3), CandidateHash: strings.Repeat("a", 64),
		},
	); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("citation receipt reused identity error=%v", err)
	}
	validPublicationQuery := toolsapplication.ValidateCitationV3PublicationAuthorityQuery{
		WorkspaceID: authorityTestID(1), WorkflowRunID: authorityTestID(2),
		AnalysisRunID: authorityTestID(3), CandidateID: authorityTestID(4),
		CandidateHash: strings.Repeat("a", 64), ReceiptID: authorityTestID(5),
		ReceiptHash: strings.Repeat("b", 64),
	}
	invalidPublicationQueries := []toolsapplication.ValidateCitationV3PublicationAuthorityQuery{
		func() toolsapplication.ValidateCitationV3PublicationAuthorityQuery {
			query := validPublicationQuery
			query.ReceiptID = "not-an-id"
			return query
		}(),
		func() toolsapplication.ValidateCitationV3PublicationAuthorityQuery {
			query := validPublicationQuery
			query.ReceiptID = query.CandidateID
			return query
		}(),
		func() toolsapplication.ValidateCitationV3PublicationAuthorityQuery {
			query := validPublicationQuery
			query.ReceiptHash = strings.Repeat("B", 64)
			return query
		}(),
	}
	for index, query := range invalidPublicationQueries {
		if _, err := repository.LoadValidateCitationV3PublicationAuthority(
			context.Background(), query,
		); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
			t.Fatalf("publication query %d error=%v", index, err)
		}
	}
	if _, err := repository.LoadValidateCitationV3PublicationAuthority(
		nil, validPublicationQuery,
	); authorityErrorCode(err) != toolsdomain.ErrorCodeResultReceiptInvalid {
		t.Fatalf("nil publication context error=%v", err)
	}
	if _, err := repository.LoadValidateCitationV3PublicationAuthority(
		context.Background(), validPublicationQuery,
	); authorityErrorCode(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("valid publication query did not reach repository dependency: %v", err)
	}
}

func TestValidAuthorityIdentitySetRequiresCanonicalDistinctIDs(t *testing.T) {
	if !validAuthorityIdentitySet(authorityTestID(1), authorityTestID(2), authorityTestID(3)) {
		t.Fatal("canonical distinct identities were rejected")
	}
	if validAuthorityIdentitySet(authorityTestID(1), authorityTestID(1)) {
		t.Fatal("duplicate identities were accepted")
	}
	if validAuthorityIdentitySet(authorityTestID(1), "not-an-id") {
		t.Fatal("non-canonical identity was accepted")
	}
}

func TestValidateWorkspaceAnalysisOperationReceiptBindingRequiresExactClosure(t *testing.T) {
	operationID, callID, receiptID := authorityTestID(1), authorityTestID(2), authorityTestID(3)
	hash := strings.Repeat("a", 64)
	receipt := toolsdomain.ResultReceipt{ID: receiptID, ToolCallID: callID, OutputHash: hash}
	if err := validateWorkspaceAnalysisOperationReceiptBinding(operationID, callID, receiptID, hash, receipt); err != nil {
		t.Fatalf("exact operation receipt closure was rejected: %v", err)
	}

	tests := []struct {
		name        string
		operationID foundation.ID
		callID      foundation.ID
		resultID    foundation.ID
		resultHash  string
		receipt     toolsdomain.ResultReceipt
	}{
		{name: "reused operation identity", operationID: callID, callID: callID, resultID: receiptID, resultHash: hash, receipt: receipt},
		{name: "different call", operationID: operationID, callID: authorityTestID(4), resultID: receiptID, resultHash: hash, receipt: receipt},
		{name: "different result", operationID: operationID, callID: callID, resultID: authorityTestID(4), resultHash: hash, receipt: receipt},
		{name: "different hash", operationID: operationID, callID: callID, resultID: receiptID, resultHash: strings.Repeat("b", 64), receipt: receipt},
		{name: "non canonical hash", operationID: operationID, callID: callID, resultID: receiptID, resultHash: strings.Repeat("A", 64), receipt: receipt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateWorkspaceAnalysisOperationReceiptBinding(
				test.operationID, test.callID, test.resultID, test.resultHash, test.receipt,
			); authorityErrorCode(err) != ErrorCodePersistenceConsistency {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func authorityErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func authorityTestID(ordinal int) foundation.ID {
	return foundation.ID("93000000-0000-4000-8000-00000000000" + string(rune('0'+ordinal)))
}
