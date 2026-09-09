package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const dynamicEvidenceRefPattern = `^E([1-9]|[12][0-9]|3[0-2])$`

var readSourceV4InputSchema = strings.ReplaceAll(readSourceV3InputSchema, "^E[1-3]$", dynamicEvidenceRefPattern)
var readSourceV4OutputSchema = strings.ReplaceAll(readSourceV3OutputSchema, "^E[1-3]$", dynamicEvidenceRefPattern)
var validateCitationV4OutputSchema = strings.ReplaceAll(strings.ReplaceAll(validateCitationV3OutputSchema, "^E[1-3]$", dynamicEvidenceRefPattern), `"maxItems":3`, `"maxItems":8`)

const validateCitationV4InputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["candidate_id","candidate_hash","evidence_refs"],
  "properties":{
    "candidate_id":{"type":["string","null"],"pattern":"` + uuidPattern + `"},
    "candidate_hash":{"type":["string","null"],"pattern":"` + hash64Pattern + `"},
    "evidence_refs":{"type":"array","minItems":1,"maxItems":8,"uniqueItems":true,"items":{"type":"string","pattern":"` + dynamicEvidenceRefPattern + `"}}
  },
  "oneOf":[
    {"properties":{"candidate_id":{"type":"null"},"candidate_hash":{"type":"null"}}},
    {"properties":{"candidate_id":{"type":"string"},"candidate_hash":{"type":"string"}}}
  ]
}`

func workspaceAnalysisDynamicSeeds(seeds []contractSeed) []contractSeed {
	result := make([]contractSeed, 0, 4)
	for _, seed := range seeds {
		if seed.workflow != workspaceAnalysisFlow || seed.workflowVersion != 1 {
			continue
		}
		seed.version++
		seed.workflowVersion = 2
		switch seed.name {
		case "ReadGitStatus":
		case "SearchKnowledge":
			seed.description = "Search approved workspace knowledge and bind every hit to an immutable run-global evidence reference."
			seed.outputSchemaVer = 3
		case "ReadSource":
			seed.description = "Open one model-selected run-global evidence reference from its exact immutable search receipt."
			seed.inputSchemaVer, seed.outputSchemaVer = 3, 3
			seed.inputSchema, seed.outputSchema = readSourceV4InputSchema, readSourceV4OutputSchema
			seed.decodeInput, seed.decodeOutput = decodeReadSourceV4Input, decodeReadSourceV4Output
		case "ValidateCitation":
			seed.description = "Validate previously read run-global evidence, bound to the immutable candidate when publishing."
			seed.inputSchemaVer, seed.outputSchemaVer = 3, 3
			seed.inputSchema, seed.outputSchema = validateCitationV4InputSchema, validateCitationV4OutputSchema
			seed.decodeInput, seed.decodeOutput = decodeValidateCitationV4Input, decodeValidateCitationV4Output
		default:
			continue
		}
		result = append(result, seed)
	}
	return result
}

func decodeDynamicDocument[T any](raw json.RawMessage, validate func(T) error) error {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes, limits.MaxStringBytes, limits.MaxArrayItems, limits.MaxDepth = 16*1024, 8*1024, 8, 8
	_, err := strictjson.DecodeObject(raw, limits, validate)
	return err
}

func validateReadSourceV4InputDocument(raw json.RawMessage) error {
	return decodeDynamicDocument(raw, func(v readSourceV3Input) error {
		if !domain.ValidDynamicEvidenceRef(v.EvidenceRef) {
			return invalidDocument()
		}
		return nil
	})
}
func validateReadSourceV4OutputDocument(raw json.RawMessage) error {
	return decodeDynamicDocument(raw, func(v readSourceV3Output) error {
		if !domain.ValidDynamicEvidenceRef(v.EvidenceRef) {
			return invalidDocument()
		}
		v.EvidenceRef = "E1"
		return validateReadSourceV3Output(v)
	})
}

type validateCitationV4Input struct {
	CandidateID   *string  `json:"candidate_id"`
	CandidateHash *string  `json:"candidate_hash"`
	EvidenceRefs  []string `json:"evidence_refs"`
}

func validateValidateCitationV4InputDocument(raw json.RawMessage) error {
	err := decodeDynamicDocument(raw, func(v validateCitationV4Input) error {
		if (v.CandidateID == nil) != (v.CandidateHash == nil) || v.CandidateID != nil && (!validID(*v.CandidateID) || !validHash(*v.CandidateHash, 64)) || len(v.EvidenceRefs) < 1 || len(v.EvidenceRefs) > 8 {
			return invalidDocument()
		}
		seen := map[string]bool{}
		for _, ref := range v.EvidenceRefs {
			if !domain.ValidDynamicEvidenceRef(ref) || seen[ref] {
				return invalidDocument()
			}
			seen[ref] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || len(keys) != 3 || keys["candidate_id"] == nil || keys["candidate_hash"] == nil || keys["evidence_refs"] == nil {
		return invalidDocument()
	}
	return nil
}

func validateValidateCitationV4OutputDocument(raw json.RawMessage) error {
	return decodeDynamicDocument(raw, func(v validateCitationV3Output) error {
		if len(v.Results) < 1 || len(v.Results) > 8 {
			return invalidDocument()
		}
		seen := map[string]bool{}
		for _, r := range v.Results {
			if !domain.ValidDynamicEvidenceRef(r.EvidenceRef) || seen[r.EvidenceRef] || r.Valid == nil || !validCitationReason(r.ReasonCode) || *r.Valid != (r.ReasonCode == "OK") {
				return invalidDocument()
			}
			seen[r.EvidenceRef] = true
		}
		return nil
	})
}

// WorkspaceAnalysisToolCatalogSnapshotV2 returns only the four v2 tool tuples.
func WorkspaceAnalysisToolCatalogSnapshotV2() (WorkspaceAnalysisToolCatalog, error) {
	registry, err := NewFrozenContractRegistry()
	if err != nil {
		return WorkspaceAnalysisToolCatalog{}, err
	}
	return WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry(registry)
}

// WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry validates the executable
// registry against the exact dynamic catalog, independently of legacy entries.
func WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry(registry *application.Registry) (WorkspaceAnalysisToolCatalog, error) {
	if registry == nil {
		return WorkspaceAnalysisToolCatalog{}, errors.New("workspace analysis registry is nil")
	}
	return workspaceAnalysisToolCatalogSnapshotVersion(registry, 2)
}

func dynamicDocumentDecoder(validate func(json.RawMessage) error) application.DocumentDecoder {
	return func(raw []byte) (json.RawMessage, error) {
		if err := validate(raw); err != nil {
			return nil, err
		}
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&object); err != nil {
			return nil, err
		}
		return json.Marshal(object)
	}
}

var (
	decodeReadSourceV4Input        = dynamicDocumentDecoder(validateReadSourceV4InputDocument)
	decodeReadSourceV4Output       = dynamicDocumentDecoder(validateReadSourceV4OutputDocument)
	decodeValidateCitationV4Input  = dynamicDocumentDecoder(validateValidateCitationV4InputDocument)
	decodeValidateCitationV4Output = dynamicDocumentDecoder(validateValidateCitationV4OutputDocument)
)
