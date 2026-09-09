package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func workspaceAnalysisAuthorizedArguments(c application.AuthorizeWorkspaceAnalysisToolCallCommand) (json.RawMessage, error) {
	if c.Identity.DefinitionVersion != 2 {
		return nil, nil
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 64 * 1024
	limits.MaxDepth = 8
	limits.MaxArrayItems = 8
	request, err := strictjson.DecodeObject[struct {
		SchemaVersion int              `json:"schema_version"`
		ToolName      string           `json:"tool_name"`
		ToolVersion   int64            `json:"tool_version"`
		InputSchema   domain.SchemaRef `json:"input_schema"`
		Arguments     json.RawMessage  `json:"arguments"`
		Reason        string           `json:"reason"`
	}](c.CanonicalRequest, limits, nil)
	if err != nil {
		return nil, authorityInputError(errors.New("dynamic tool request document is invalid"))
	}
	digest := sha256.Sum256(c.CanonicalRequest)
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, c.CanonicalRequest) || request.SchemaVersion != 1 || c.Call.Tool == nil || request.ToolName != c.Call.Tool.Name || request.ToolVersion != c.Call.Tool.Version || c.Call.InputSchema == nil || request.InputSchema != *c.Call.InputSchema || hex.EncodeToString(digest[:]) != c.Call.RequestHash || int64(len(c.CanonicalRequest)) != c.Call.RequestBytes {
		return nil, authorityInputError(errors.New("dynamic tool request hash binding differs"))
	}
	registry, err := catalog.NewFrozenContractRegistry()
	if err != nil {
		return nil, err
	}
	contract, err := registry.ResolveContract(*c.Call.Tool)
	if err != nil {
		return nil, err
	}
	arguments, err := contract.DecodeInput(request.Arguments)
	if err != nil || !bytes.Equal(arguments, request.Arguments) {
		return nil, authorityInputError(errors.New("dynamic tool arguments are not canonical"))
	}
	return arguments, nil
}
