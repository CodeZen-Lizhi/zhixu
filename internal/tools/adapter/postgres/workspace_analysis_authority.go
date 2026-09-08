package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

var (
	workspaceAnalysisSearchRef = toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}
	workspaceAnalysisSourceRef = toolsdomain.ToolRef{Name: "ReadSource", Version: 3}
)

func validateWorkspaceAnalysisOperationReceiptBinding(
	operationID foundation.ID,
	callID foundation.ID,
	resultID foundation.ID,
	resultHash string,
	receipt toolsdomain.ResultReceipt,
) error {
	if !validAuthorityIdentitySet(operationID, callID, resultID) || !canonicalAuthorityHash(resultHash) ||
		receipt.ID != resultID || receipt.OutputHash != resultHash || receipt.ToolCallID != callID {
		return consistency(errors.New("workspace analysis operation result binding is invalid"))
	}
	return nil
}

func workspaceAnalysisBuiltinDefinition(ref toolsdomain.ToolRef) (toolsdomain.Definition, error) {
	contracts, err := catalog.Contracts()
	if err != nil {
		return toolsdomain.Definition{}, consistency(errors.New("workspace analysis catalog is invalid"))
	}
	for _, contract := range contracts {
		if contract.Definition.Ref == ref {
			if contract.Definition.ResultPersistencePolicy != toolsdomain.ResultPersistenceCanonical {
				return toolsdomain.Definition{}, consistency(errors.New("workspace analysis receipt definition is not canonical"))
			}
			return contract.Definition, nil
		}
	}
	return toolsdomain.Definition{}, consistency(errors.New("workspace analysis receipt definition is unavailable"))
}

func canonicalAuthorityID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func canonicalAuthorityHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validAuthorityIdentitySet(ids ...foundation.ID) bool {
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalAuthorityID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func authorityInputError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, toolsdomain.ErrorCodeResultReceiptInvalid, false, cause)
}
