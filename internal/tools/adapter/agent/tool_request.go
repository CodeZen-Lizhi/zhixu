// Package agent 将项目自有 Tool Request 契约桥接到 Agent RuntimeCatalog。
package agent

import (
	"encoding/json"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	maxToolRequestDocumentBytes = 2 * 1024 * 1024
	maxToolRequestDepth         = 8
	maxToolRequestStringBytes   = 512 * 1024
	maxToolRequestArrayItems    = 500
	maxToolRequestObjectFields  = 128
)

var toolRequestSchemaV1 = []byte(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "additionalProperties":false,
  "required":["schema_version","tool_name","arguments","reason"],
  "properties":{
    "schema_version":{"const":1},
    "tool_name":{"type":"string","pattern":"^[A-Z][A-Za-z0-9]{0,63}$"},
    "arguments":{"type":"object"},
    "reason":{"type":"string","minLength":1,"maxLength":2048}
  }
}`)

// ResultType 是 Tool Request 在 Agent Model Run 中使用的稳定结果类型。
const ResultType = agentdomain.ResultTypeToolRequest

// SchemaRef 返回 Tool Request v1 的精确 Agent Schema 引用。
func SchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: agentdomain.ToolRequestSchemaID, Version: agentdomain.OutputSchemaVersionV1}
}

// SchemaDefinition 返回可注册到 Agent RuntimeCatalog 的独立 Schema 与严格解码器。
func SchemaDefinition() agentapplication.SchemaDefinition {
	return agentapplication.SchemaDefinition{
		Ref:        SchemaRef(),
		JSONSchema: append([]byte(nil), toolRequestSchemaV1...),
		Decode:     DecodeDocument,
	}
}

// DecodeToolRequestV1 严格解码一个项目自有 Tool Request，不从模型读取任何服务端执行上下文。
func DecodeToolRequestV1(raw []byte) (toolsdomain.ToolRequestV1, error) {
	limits := strictjson.Limits{
		MaxDocumentBytes: maxToolRequestDocumentBytes,
		MaxDepth:         maxToolRequestDepth,
		MaxStringBytes:   maxToolRequestStringBytes,
		MaxArrayItems:    maxToolRequestArrayItems,
		MaxObjectFields:  maxToolRequestObjectFields,
	}
	request, err := strictjson.DecodeObject(raw, limits, func(request toolsdomain.ToolRequestV1) error {
		return request.Validate()
	})
	if err == nil {
		return request, nil
	}
	if _, ok := strictjson.KindOf(err); ok {
		return toolsdomain.ToolRequestV1{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			toolsdomain.ErrorCodeRequestInvalid,
			false,
			err,
		)
	}
	return toolsdomain.ToolRequestV1{}, err
}

// DecodeDocument 校验 Tool Request 后返回接受文档的独立原始副本，不填充默认值或改写字段。
func DecodeDocument(raw []byte) (json.RawMessage, error) {
	if _, err := DecodeToolRequestV1(raw); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), raw...), nil
}
