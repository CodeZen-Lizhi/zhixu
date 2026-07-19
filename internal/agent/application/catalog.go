package application

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxPromptTemplateBytes 是单段服务端 Prompt 模板的最大 UTF-8 字节数。
	MaxPromptTemplateBytes = 128 * 1024
	// MaxSchemaDefinitionBytes 是单个版本化 JSON Schema 的最大字节数。
	MaxSchemaDefinitionBytes = 512 * 1024
	// MaxProfileTimeout 是单次模型调用 Profile 可配置的最长 timeout。
	MaxProfileTimeout = 5 * time.Minute
)

const (
	errorCodeCatalogMissing        = "AGENT_RUNTIME_CATALOG_MISSING"
	errorCodeCatalogFrozen         = "AGENT_RUNTIME_CATALOG_FROZEN"
	errorCodeCatalogNotFrozen      = "AGENT_RUNTIME_CATALOG_NOT_FROZEN"
	errorCodeCatalogEmpty          = "AGENT_RUNTIME_CATALOG_EMPTY"
	errorCodePromptInvalid         = "AGENT_PROMPT_INVALID"
	errorCodePromptDuplicate       = "AGENT_PROMPT_DUPLICATE"
	errorCodePromptNotFound        = "AGENT_PROMPT_NOT_FOUND"
	errorCodeSchemaInvalid         = "AGENT_SCHEMA_INVALID"
	errorCodeSchemaDuplicate       = "AGENT_SCHEMA_DUPLICATE"
	errorCodeSchemaNotFound        = "AGENT_SCHEMA_NOT_FOUND"
	errorCodeProfileInvalid        = "AGENT_MODEL_PROFILE_INVALID"
	errorCodeProfileDuplicate      = "AGENT_MODEL_PROFILE_DUPLICATE"
	errorCodeProfileNotFound       = "AGENT_MODEL_PROFILE_NOT_FOUND"
	errorCodeCatalogReferenceDrift = "AGENT_RUNTIME_REFERENCE_DRIFT"
)

// OutputDecoder 对一个精确 Schema 版本执行严格 JSON 与任务领域校验。
type OutputDecoder func([]byte) (json.RawMessage, error)

// PromptDefinition 冻结一个任务在三阶段使用的服务端指令。
type PromptDefinition struct {
	Ref                domain.PromptRef
	System             string
	InitialInstruction string
	RepairInstruction  string
	ReducedInstruction string
}

// SchemaDefinition 冻结一个 JSON Schema 文档及其严格解码器。
type SchemaDefinition struct {
	Ref        domain.SchemaRef
	JSONSchema []byte
	Decode     OutputDecoder
}

// ModelProfile 冻结模型身份、单次 timeout 和输出 Token 上限。
type ModelProfile struct {
	Ref             domain.ModelProfileRef
	Model           domain.ModelRef
	Timeout         time.Duration
	MaxOutputTokens int
}

// RuntimeSnapshot 是一次运行开始前解析出的不可变版本集合。
type RuntimeSnapshot struct {
	Prompt        PromptDefinition
	Schema        SchemaDefinition
	ReducedSchema SchemaDefinition
	Profile       ModelProfile
}

type promptKey struct {
	id      string
	version string
}

type schemaKey struct {
	id      string
	version string
}

type profileKey struct {
	id      string
	version string
}

// RuntimeCatalog 保存版本化 Prompt、Schema 和模型 Profile；冻结后才允许解析。
type RuntimeCatalog struct {
	mu       sync.RWMutex
	prompts  map[promptKey]PromptDefinition
	schemas  map[schemaKey]SchemaDefinition
	profiles map[profileKey]ModelProfile
	frozen   bool
}

// NewRuntimeCatalog 创建一个尚未冻结的空目录。
func NewRuntimeCatalog() *RuntimeCatalog {
	return &RuntimeCatalog{
		prompts:  make(map[promptKey]PromptDefinition),
		schemas:  make(map[schemaKey]SchemaDefinition),
		profiles: make(map[profileKey]ModelProfile),
	}
}

// RegisterPrompt 注册一个精确 Prompt 版本，重复或冻结后注册会失败。
func (c *RuntimeCatalog) RegisterPrompt(definition PromptDefinition) error {
	if c == nil {
		return catalogError(foundation.ErrorDependencyUnavailable, errorCodeCatalogMissing, errors.New("runtime catalog is nil"))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return catalogError(foundation.ErrorVersionConflict, errorCodeCatalogFrozen, errors.New("runtime catalog is frozen"))
	}
	canonical, err := canonicalPrompt(definition)
	if err != nil {
		return err
	}
	key := promptKey{id: canonical.Ref.ID, version: canonical.Ref.Version}
	if _, exists := c.prompts[key]; exists {
		return catalogError(foundation.ErrorVersionConflict, errorCodePromptDuplicate, errors.New("prompt id and version are already registered"))
	}
	c.prompts[key] = canonical
	return nil
}

// RegisterSchema 注册一个精确 Schema 版本及严格解码器。
func (c *RuntimeCatalog) RegisterSchema(definition SchemaDefinition) error {
	if c == nil {
		return catalogError(foundation.ErrorDependencyUnavailable, errorCodeCatalogMissing, errors.New("runtime catalog is nil"))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return catalogError(foundation.ErrorVersionConflict, errorCodeCatalogFrozen, errors.New("runtime catalog is frozen"))
	}
	canonical, err := canonicalSchema(definition)
	if err != nil {
		return err
	}
	key := schemaKey{id: canonical.Ref.ID, version: canonical.Ref.Version}
	if _, exists := c.schemas[key]; exists {
		return catalogError(foundation.ErrorVersionConflict, errorCodeSchemaDuplicate, errors.New("schema id and version are already registered"))
	}
	c.schemas[key] = canonical
	return nil
}

// RegisterProfile 注册一个精确模型 Profile 版本。
func (c *RuntimeCatalog) RegisterProfile(profile ModelProfile) error {
	if c == nil {
		return catalogError(foundation.ErrorDependencyUnavailable, errorCodeCatalogMissing, errors.New("runtime catalog is nil"))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return catalogError(foundation.ErrorVersionConflict, errorCodeCatalogFrozen, errors.New("runtime catalog is frozen"))
	}
	canonical, err := canonicalProfile(profile)
	if err != nil {
		return err
	}
	key := profileKey{id: canonical.Ref.ID, version: canonical.Ref.Version}
	if _, exists := c.profiles[key]; exists {
		return catalogError(foundation.ErrorVersionConflict, errorCodeProfileDuplicate, errors.New("model profile id and version are already registered"))
	}
	c.profiles[key] = canonical
	return nil
}

// Freeze 将目录切换为只读；空目录拒绝冻结。
func (c *RuntimeCatalog) Freeze() error {
	if c == nil {
		return catalogError(foundation.ErrorDependencyUnavailable, errorCodeCatalogMissing, errors.New("runtime catalog is nil"))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return nil
	}
	if len(c.prompts) == 0 || len(c.schemas) == 0 || len(c.profiles) == 0 {
		return catalogError(foundation.ErrorDependencyUnavailable, errorCodeCatalogEmpty, errors.New("runtime catalog must contain prompt, schema, and profile definitions"))
	}
	c.frozen = true
	return nil
}

// Snapshot 按精确引用解析一次运行；不会读取 latest 或在运行中重新解析。
func (c *RuntimeCatalog) Snapshot(promptRef domain.PromptRef, schemaRef, reducedSchemaRef domain.SchemaRef, profileRef domain.ModelProfileRef) (RuntimeSnapshot, error) {
	if c == nil {
		return RuntimeSnapshot{}, catalogError(foundation.ErrorDependencyUnavailable, errorCodeCatalogMissing, errors.New("runtime catalog is nil"))
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.frozen {
		return RuntimeSnapshot{}, catalogError(foundation.ErrorDependencyUnavailable, errorCodeCatalogNotFrozen, errors.New("runtime catalog is not frozen"))
	}
	prompt, ok := c.prompts[promptKey{id: promptRef.ID, version: promptRef.Version}]
	if !ok {
		return RuntimeSnapshot{}, catalogError(foundation.ErrorNotFound, errorCodePromptNotFound, errors.New("prompt id and version are not registered"))
	}
	schema, ok := c.schemas[schemaKey{id: schemaRef.ID, version: schemaRef.Version}]
	if !ok {
		return RuntimeSnapshot{}, catalogError(foundation.ErrorNotFound, errorCodeSchemaNotFound, errors.New("schema id and version are not registered"))
	}
	reduced, ok := c.schemas[schemaKey{id: reducedSchemaRef.ID, version: reducedSchemaRef.Version}]
	if !ok {
		return RuntimeSnapshot{}, catalogError(foundation.ErrorNotFound, errorCodeSchemaNotFound, errors.New("reduced schema id and version are not registered"))
	}
	profile, ok := c.profiles[profileKey{id: profileRef.ID, version: profileRef.Version}]
	if !ok {
		return RuntimeSnapshot{}, catalogError(foundation.ErrorNotFound, errorCodeProfileNotFound, errors.New("model profile id and version are not registered"))
	}
	if prompt.Ref != promptRef || schema.Ref != schemaRef || reduced.Ref != reducedSchemaRef || profile.Ref != profileRef {
		return RuntimeSnapshot{}, catalogError(foundation.ErrorConsistencyViolation, errorCodeCatalogReferenceDrift, errors.New("resolved runtime reference differs from requested version"))
	}
	return RuntimeSnapshot{
		Prompt:        clonePrompt(prompt),
		Schema:        cloneSchema(schema),
		ReducedSchema: cloneSchema(reduced),
		Profile:       profile,
	}, nil
}

func canonicalPrompt(definition PromptDefinition) (PromptDefinition, error) {
	if err := definition.Ref.Validate(); err != nil {
		return PromptDefinition{}, catalogError(foundation.ErrorInvalidInput, errorCodePromptInvalid, err)
	}
	definition.System = strings.TrimSpace(definition.System)
	definition.InitialInstruction = strings.TrimSpace(definition.InitialInstruction)
	definition.RepairInstruction = strings.TrimSpace(definition.RepairInstruction)
	definition.ReducedInstruction = strings.TrimSpace(definition.ReducedInstruction)
	values := []string{definition.System, definition.InitialInstruction, definition.RepairInstruction, definition.ReducedInstruction}
	for _, value := range values {
		if value == "" || len(value) > MaxPromptTemplateBytes || !utf8.ValidString(value) {
			return PromptDefinition{}, catalogError(foundation.ErrorInvalidInput, errorCodePromptInvalid, errors.New("prompt template is empty, oversized, or invalid utf-8"))
		}
	}
	return definition, nil
}

func canonicalSchema(definition SchemaDefinition) (SchemaDefinition, error) {
	if err := definition.Ref.Validate(); err != nil {
		return SchemaDefinition{}, catalogError(foundation.ErrorInvalidInput, errorCodeSchemaInvalid, err)
	}
	if definition.Decode == nil {
		return SchemaDefinition{}, catalogError(foundation.ErrorInvalidInput, errorCodeSchemaInvalid, errors.New("schema document or strict decoder is invalid"))
	}
	if err := validateSchemaDocument(definition.JSONSchema); err != nil {
		return SchemaDefinition{}, catalogError(foundation.ErrorInvalidInput, errorCodeSchemaInvalid, err)
	}
	definition.JSONSchema = append([]byte(nil), definition.JSONSchema...)
	return definition, nil
}

func validateSchemaDocument(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxSchemaDefinitionBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return errors.New("schema document is empty, oversized, or invalid json")
	}
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = MaxSchemaDefinitionBytes
	if _, err := domain.DecodeStrict[map[string]json.RawMessage](raw, limits, nil); err != nil {
		return err
	}
	return nil
}

func canonicalProfile(profile ModelProfile) (ModelProfile, error) {
	if err := profile.Ref.Validate(); err != nil {
		return ModelProfile{}, catalogError(foundation.ErrorInvalidInput, errorCodeProfileInvalid, err)
	}
	if err := profile.Model.Validate(); err != nil {
		return ModelProfile{}, catalogError(foundation.ErrorInvalidInput, errorCodeProfileInvalid, err)
	}
	if profile.Timeout <= 0 || profile.Timeout > MaxProfileTimeout || profile.MaxOutputTokens <= 0 || profile.MaxOutputTokens > MaxOutputTokens {
		return ModelProfile{}, catalogError(foundation.ErrorInvalidInput, errorCodeProfileInvalid, errors.New("model profile timeout or output token limit is invalid"))
	}
	return profile, nil
}

func clonePrompt(prompt PromptDefinition) PromptDefinition {
	return prompt
}

func cloneSchema(schema SchemaDefinition) SchemaDefinition {
	cloned := schema
	cloned.JSONSchema = append([]byte(nil), schema.JSONSchema...)
	return cloned
}

func catalogError(kind foundation.ErrorKind, code string, cause error) error {
	return applicationError(kind, code, false, cause)
}
