package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	yaml "go.yaml.in/yaml/v3"
)

type envGate uint8

const (
	envGateAlways envGate = iota
	envGateEmbedding
	envGateChat
	envGateTelemetry
)

type envSpec struct {
	configKey string
	envKey    string
	apiOnly   bool
	gate      envGate
}

type sourceMetadata struct {
	presentKeys map[string]struct{}
}

func newSourceMetadata() sourceMetadata {
	return sourceMetadata{presentKeys: make(map[string]struct{})}
}

func (m *sourceMetadata) markPresent(key string) {
	m.presentKeys[key] = struct{}{}
}

func (m sourceMetadata) isPresent(key string) bool {
	_, ok := m.presentKeys[key]
	return ok
}

type configLoader struct {
	v        *viper.Viper
	validate *configValidator
	lookup   func(string) (string, bool)
	options  loadOptions
	fields   map[string]reflect.Type
	metadata sourceMetadata
}

// Selectors are applied first because dependent Secret lookup is itself part
// of the configuration security boundary.
var selectorEnvSpecs = []envSpec{
	{configKey: "auth_mode", envKey: "ZHIXU_AUTH_MODE"},
	{configKey: "tool_runtime_mode", envKey: "ZHIXU_TOOL_RUNTIME_MODE"},
	{configKey: "web_fetch_mode", envKey: "ZHIXU_WEB_FETCH_MODE"},
	{configKey: "model_settings_mode", envKey: "ZHIXU_MODEL_SETTINGS_MODE"},
	{configKey: "chat_provider", envKey: "ZHIXU_CHAT_PROVIDER"},
	{configKey: "embedding_provider", envKey: "ZHIXU_EMBEDDING_PROVIDER"},
	{configKey: "telemetry_mode", envKey: "ZHIXU_TELEMETRY_MODE"},
}

var removedImplementationSelectorEnvNames = [...]string{
	"ZHIXU_CHAT_IMPLEMENTATION",
	"ZHIXU_EMBEDDING_IMPLEMENTATION",
	"ZHIXU_STRUCTURED_SCHEDULER_RAG",
	"ZHIXU_STRUCTURED_SCHEDULER_RELATION",
	"ZHIXU_STRUCTURED_SCHEDULER_ARTIFACT",
	"ZHIXU_STRUCTURED_SCHEDULER_CAPTURE",
	"ZHIXU_STRUCTURED_SCHEDULER_ORGANIZING",
}

var valueEnvSpecs = []envSpec{
	{configKey: "app_name", envKey: "ZHIXU_APP_NAME"},
	{configKey: "version", envKey: "ZHIXU_VERSION"},
	{configKey: "environment", envKey: "ZHIXU_ENVIRONMENT"},
	{configKey: "http_addr", envKey: "ZHIXU_HTTP_ADDR"},
	{configKey: "database_url", envKey: "ZHIXU_DATABASE_URL"},
	{configKey: "database_host", envKey: "ZHIXU_DATABASE_HOST"},
	{configKey: "database_port", envKey: "ZHIXU_DATABASE_PORT"},
	{configKey: "database_name", envKey: "ZHIXU_DATABASE_NAME"},
	{configKey: "database_user", envKey: "ZHIXU_DATABASE_USER"},
	{configKey: "database_password", envKey: "ZHIXU_DATABASE_PASSWORD"},
	{configKey: "web_assets_dir", envKey: "ZHIXU_WEB_ASSETS_DIR"},
	{configKey: "worker_queue", envKey: "ZHIXU_WORKER_QUEUE"},
	{configKey: "worker_health_addr", envKey: "ZHIXU_WORKER_HEALTH_ADDR"},
	{configKey: "model_settings_key_file", envKey: "ZHIXU_MODEL_SETTINGS_KEY_FILE"},
	{configKey: "git_sync_key_file", envKey: "ZHIXU_GIT_SYNC_KEY_FILE"},
	{configKey: "model_settings_rollout_id", envKey: "ZHIXU_MODEL_SETTINGS_ROLLOUT_ID"},

	{configKey: "embedding_base_url", envKey: "ZHIXU_EMBEDDING_BASE_URL", gate: envGateEmbedding},
	{configKey: "embedding_api_key", envKey: "ZHIXU_EMBEDDING_API_KEY", gate: envGateEmbedding},
	{configKey: "embedding_model", envKey: "ZHIXU_EMBEDDING_MODEL", gate: envGateEmbedding},
	{configKey: "chat_base_url", envKey: "ZHIXU_CHAT_BASE_URL", gate: envGateChat},
	{configKey: "chat_api_key", envKey: "ZHIXU_CHAT_API_KEY", gate: envGateChat},
	{configKey: "chat_model", envKey: "ZHIXU_CHAT_MODEL", gate: envGateChat},
	{configKey: "chat_model_version", envKey: "ZHIXU_CHAT_MODEL_VERSION", gate: envGateChat},
	{configKey: "chat_adapter_version", envKey: "ZHIXU_CHAT_ADAPTER_VERSION", gate: envGateChat},
	{configKey: "chat_api_style", envKey: "ZHIXU_CHAT_API_STYLE"},

	{configKey: "auth_bootstrap_token", envKey: "ZHIXU_AUTH_BOOTSTRAP_TOKEN", apiOnly: true},
	{configKey: "review_question_ref_key", envKey: "ZHIXU_REVIEW_QUESTION_REF_KEY", apiOnly: true},
	{configKey: "auth_allowed_origins", envKey: "ZHIXU_AUTH_ALLOWED_ORIGINS"},
	{configKey: "auth_secure_cookie", envKey: "ZHIXU_AUTH_SECURE_COOKIE"},
	{configKey: "model_settings_prepared", envKey: "ZHIXU_MODEL_SETTINGS_PREPARED"},
	{configKey: "workspace_analysis_api_enabled", envKey: "ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED"},
	{configKey: "workspace_analysis_worker_enabled", envKey: "ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED"},
	{configKey: "web_fetch_allowed_content_types", envKey: "ZHIXU_WEB_FETCH_ALLOWED_CONTENT_TYPES"},
	{configKey: "embedding_normalization", envKey: "ZHIXU_EMBEDDING_NORMALIZATION"},
	{configKey: "embedding_distance_metric", envKey: "ZHIXU_EMBEDDING_DISTANCE_METRIC"},

	{configKey: "database_max_conns", envKey: "ZHIXU_DATABASE_MAX_CONNS"},
	{configKey: "database_min_conns", envKey: "ZHIXU_DATABASE_MIN_CONNS"},
	{configKey: "worker_max_workers", envKey: "ZHIXU_WORKER_MAX_WORKERS"},
	{configKey: "workspace_analysis_config_revision", envKey: "ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION"},
	{configKey: "reindex_dispatch_batch_size", envKey: "ZHIXU_REINDEX_DISPATCH_BATCH_SIZE"},
	{configKey: "web_fetch_max_redirects", envKey: "ZHIXU_WEB_FETCH_MAX_REDIRECTS"},
	{configKey: "web_fetch_max_url_bytes", envKey: "ZHIXU_WEB_FETCH_MAX_URL_BYTES"},
	{configKey: "web_fetch_max_text_bytes", envKey: "ZHIXU_WEB_FETCH_MAX_TEXT_BYTES"},
	{configKey: "web_fetch_max_resolved_ips", envKey: "ZHIXU_WEB_FETCH_MAX_RESOLVED_IPS"},
	{configKey: "embedding_dimensions", envKey: "ZHIXU_EMBEDDING_DIMENSIONS", gate: envGateEmbedding},
	{configKey: "embedding_max_batch_size", envKey: "ZHIXU_EMBEDDING_MAX_BATCH_SIZE"},
	{configKey: "embedding_max_input_bytes", envKey: "ZHIXU_EMBEDDING_MAX_INPUT_BYTES"},
	{configKey: "retrieval_rrf_k", envKey: "ZHIXU_RETRIEVAL_RRF_K"},
	{configKey: "retrieval_rrf_lexical_candidate_limit", envKey: "ZHIXU_RETRIEVAL_RRF_LEXICAL_CANDIDATE_LIMIT"},
	{configKey: "retrieval_rrf_vector_candidate_limit", envKey: "ZHIXU_RETRIEVAL_RRF_VECTOR_CANDIDATE_LIMIT"},
	{configKey: "retrieval_rrf_fused_candidate_limit", envKey: "ZHIXU_RETRIEVAL_RRF_FUSED_CANDIDATE_LIMIT"},
	{configKey: "retrieval_rrf_rerank_candidate_limit", envKey: "ZHIXU_RETRIEVAL_RRF_RERANK_CANDIDATE_LIMIT"},
	{configKey: "embedding_max_response_bytes", envKey: "ZHIXU_EMBEDDING_MAX_RESPONSE_BYTES"},
	{configKey: "embedding_max_batch_input_bytes", envKey: "ZHIXU_EMBEDDING_MAX_BATCH_INPUT_BYTES"},
	{configKey: "chat_max_request_bytes", envKey: "ZHIXU_CHAT_MAX_REQUEST_BYTES"},
	{configKey: "chat_max_response_bytes", envKey: "ZHIXU_CHAT_MAX_RESPONSE_BYTES"},
	{configKey: "web_fetch_max_response_header_bytes", envKey: "ZHIXU_WEB_FETCH_MAX_RESPONSE_HEADER_BYTES"},
	{configKey: "web_fetch_max_body_bytes", envKey: "ZHIXU_WEB_FETCH_MAX_BODY_BYTES"},

	{configKey: "database_ping_timeout", envKey: "ZHIXU_DATABASE_PING_TIMEOUT"},
	{configKey: "graph_query_timeout", envKey: "ZHIXU_GRAPH_QUERY_TIMEOUT"},
	{configKey: "health_interval", envKey: "ZHIXU_HEALTH_INTERVAL"},
	{configKey: "shutdown_timeout", envKey: "ZHIXU_SHUTDOWN_TIMEOUT"},
	{configKey: "worker_job_timeout", envKey: "ZHIXU_WORKER_JOB_TIMEOUT"},
	{configKey: "worker_rescue_stuck_jobs_after", envKey: "ZHIXU_WORKER_RESCUE_STUCK_AFTER"},
	{configKey: "workflow_lease", envKey: "ZHIXU_WORKFLOW_LEASE"},
	{configKey: "workflow_heartbeat", envKey: "ZHIXU_WORKFLOW_HEARTBEAT"},
	{configKey: "reindex_dispatch_poll_interval", envKey: "ZHIXU_REINDEX_DISPATCH_POLL_INTERVAL"},
	{configKey: "reindex_dispatch_error_backoff", envKey: "ZHIXU_REINDEX_DISPATCH_ERROR_BACKOFF"},
	{configKey: "reindex_lease_duration", envKey: "ZHIXU_REINDEX_LEASE_DURATION"},
	{configKey: "reindex_heartbeat_interval", envKey: "ZHIXU_REINDEX_HEARTBEAT_INTERVAL"},
	{configKey: "embedding_timeout", envKey: "ZHIXU_EMBEDDING_TIMEOUT"},
	{configKey: "chat_timeout", envKey: "ZHIXU_CHAT_TIMEOUT"},
	{configKey: "web_fetch_timeout", envKey: "ZHIXU_WEB_FETCH_TIMEOUT"},
	{configKey: "web_fetch_response_header_timeout", envKey: "ZHIXU_WEB_FETCH_RESPONSE_HEADER_TIMEOUT"},
	{configKey: "web_fetch_tls_handshake_timeout", envKey: "ZHIXU_WEB_FETCH_TLS_HANDSHAKE_TIMEOUT"},
	{configKey: "worker_soft_stop_timeout", envKey: "ZHIXU_WORKER_SOFT_STOP_TIMEOUT"},
	{configKey: "worker_hard_stop_timeout", envKey: "ZHIXU_WORKER_HARD_STOP_TIMEOUT"},
	{configKey: "auth_session_ttl", envKey: "ZHIXU_AUTH_SESSION_TTL"},
	{configKey: "auth_api_token_ttl", envKey: "ZHIXU_AUTH_API_TOKEN_TTL"},

	{configKey: "telemetry_endpoint", envKey: "OTEL_EXPORTER_OTLP_ENDPOINT", gate: envGateTelemetry},
}

func newConfigLoader(lookup func(string) (string, bool), options loadOptions) (*configLoader, error) {
	fields, err := configFieldTypes()
	if err != nil {
		return nil, err
	}
	configValidation, err := newConfigValidator()
	if err != nil {
		return nil, err
	}
	loader := &configLoader{
		v:        viper.New(),
		validate: configValidation,
		lookup:   lookup,
		options:  options,
		fields:   fields,
		metadata: newSourceMetadata(),
	}
	if err := loader.registerDefaults(Defaults()); err != nil {
		return nil, err
	}
	return loader, nil
}

func (l *configLoader) load(path string) (Config, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config file: %w", err)
		}
		metadata, err := inspectYAML(data, l.fields)
		if err != nil {
			return Config{}, fmt.Errorf("parse config file: %w", err)
		}
		l.metadata = metadata
		l.v.SetConfigType("yaml")
		if err := l.v.ReadConfig(bytes.NewReader(data)); err != nil {
			return Config{}, fmt.Errorf("parse config file: %w", err)
		}
	}
	if err := l.rejectRemovedImplementationSelectors(); err != nil {
		return Config{}, err
	}

	if !l.options.consumeAPISecrets {
		l.v.Set("auth_bootstrap_token", "")
		l.v.Set("review_question_ref_key", "")
	}
	if err := l.applySelectors(); err != nil {
		return Config{}, err
	}
	selectorConfig, err := l.decode()
	if err != nil {
		return Config{}, err
	}
	l.clearDisabledValues(selectorConfig)
	if err := l.applyValues(selectorConfig); err != nil {
		return Config{}, err
	}
	result, err := l.decode()
	if err != nil {
		return Config{}, err
	}
	result.reviewQuestionRefKeyExplicit = l.options.consumeAPISecrets && l.metadata.isPresent("review_question_ref_key")

	if !l.options.consumeAPISecrets {
		clearAPISecrets(&result)
	} else if err := materializeReviewQuestionRefKey(&result); err != nil {
		return Config{}, err
	}
	return result, result.validateWith(l.validate, l.options.validateAuth)
}

func (l *configLoader) rejectRemovedImplementationSelectors() error {
	for _, key := range removedImplementationSelectorEnvNames {
		if _, ok := l.lookup(key); ok {
			return fmt.Errorf("%s is no longer supported; Eino is the only AI runtime", key)
		}
	}
	return nil
}

func (l *configLoader) registerDefaults(defaults Config) error {
	value := reflect.ValueOf(defaults)
	typeOfConfig := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := typeOfConfig.Field(i)
		key := yamlFieldName(field)
		if key == "" {
			continue
		}
		defaultValue := value.Field(i).Interface()
		if values, ok := defaultValue.([]string); ok {
			defaultValue = append([]string(nil), values...)
		}
		l.v.SetDefault(key, defaultValue)
	}
	return nil
}

func (l *configLoader) applySelectors() error {
	for _, spec := range selectorEnvSpecs {
		value, ok := l.lookup(spec.envKey)
		if !ok {
			continue
		}
		parsed, err := l.parseEnvironmentValue(spec, value)
		if err != nil {
			return err
		}
		l.v.Set(spec.configKey, parsed)
	}
	return nil
}

func (l *configLoader) clearDisabledValues(current Config) {
	if current.ChatProvider == ChatProviderDisabled {
		l.v.Set("chat_base_url", "")
		l.v.Set("chat_api_key", "")
		l.v.Set("chat_model", "")
		l.v.Set("chat_model_version", "")
	}
	if current.EmbeddingProvider == EmbeddingProviderDisabled {
		l.v.Set("embedding_base_url", "")
		l.v.Set("embedding_api_key", "")
		l.v.Set("embedding_model", "")
		l.v.Set("embedding_dimensions", int32(0))
	}
	if current.TelemetryMode == TelemetryModeDisabled {
		l.v.Set("telemetry_endpoint", "")
	}
}

func (l *configLoader) applyValues(current Config) error {
	for _, spec := range valueEnvSpecs {
		if spec.apiOnly && !l.options.consumeAPISecrets {
			continue
		}
		if !envGateAllows(spec.gate, current) {
			continue
		}
		value, ok := l.lookup(spec.envKey)
		if !ok {
			continue
		}
		parsed, err := l.parseEnvironmentValue(spec, value)
		if err != nil {
			return err
		}
		l.v.Set(spec.configKey, parsed)
		if spec.configKey == "review_question_ref_key" {
			l.metadata.markPresent(spec.configKey)
		}
	}
	return nil
}

func envGateAllows(gate envGate, cfg Config) bool {
	switch gate {
	case envGateAlways:
		return true
	case envGateEmbedding:
		return cfg.EmbeddingProvider != EmbeddingProviderDisabled
	case envGateChat:
		return cfg.ChatProvider != ChatProviderDisabled
	case envGateTelemetry:
		return cfg.TelemetryMode != TelemetryModeDisabled
	default:
		return false
	}
}

func (l *configLoader) parseEnvironmentValue(spec envSpec, raw string) (any, error) {
	fieldType, ok := l.fields[spec.configKey]
	if !ok {
		return nil, fmt.Errorf("config loader: unknown registry key %q", spec.configKey)
	}
	switch spec.configKey {
	case "auth_allowed_origins":
		return parseAuthOrigins(raw)
	case "web_fetch_allowed_content_types":
		return parseWebFetchContentTypes(raw)
	}
	if fieldType == reflect.TypeFor[time.Duration]() {
		value, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: invalid duration", spec.envKey)
		}
		return value, nil
	}
	switch fieldType.Kind() {
	case reflect.String:
		return reflect.ValueOf(raw).Convert(fieldType).Interface(), nil
	case reflect.Bool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: invalid boolean", spec.envKey)
		}
		return value, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(raw, 10, fieldType.Bits())
		if err != nil {
			return nil, fmt.Errorf("parse %s: invalid integer", spec.envKey)
		}
		converted := reflect.New(fieldType).Elem()
		converted.SetInt(value)
		return converted.Interface(), nil
	default:
		return nil, fmt.Errorf("config loader: unsupported environment type for %s", spec.configKey)
	}
}

func (l *configLoader) decode() (Config, error) {
	var cfg Config
	err := l.v.UnmarshalExact(&cfg, func(decoder *mapstructure.DecoderConfig) {
		decoder.TagName = "yaml"
		decoder.IgnoreUntaggedFields = true
		decoder.WeaklyTypedInput = false
		decoder.ZeroFields = true
		decoder.MatchName = func(mapKey, fieldName string) bool {
			return mapKey == fieldName
		}
		decoder.DecodeHook = strictDurationDecodeHook
	})
	if err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

func strictDurationDecodeHook(from reflect.Type, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to != reflect.TypeFor[time.Duration]() {
		return data, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(data.(string)))
	if err != nil {
		return nil, errors.New("invalid duration")
	}
	return value, nil
}

func configFieldTypes() (map[string]reflect.Type, error) {
	result := make(map[string]reflect.Type)
	typeOfConfig := reflect.TypeFor[Config]()
	for i := 0; i < typeOfConfig.NumField(); i++ {
		field := typeOfConfig.Field(i)
		key := yamlFieldName(field)
		if key == "" {
			continue
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("config loader: duplicate YAML key %q", key)
		}
		result[key] = field.Type
	}
	return result, nil
}

func yamlFieldName(field reflect.StructField) string {
	if !field.IsExported() {
		return ""
	}
	tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
	if tag == "" || tag == "-" {
		return ""
	}
	return tag
}

func inspectYAML(data []byte, fields map[string]reflect.Type) (sourceMetadata, error) {
	// Viper omits unknown null and empty-map leaves from AllKeys, so inspect the
	// first document's shape before Viper remains responsible for value merging.
	metadata := newSourceMetadata()
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return metadata, io.EOF
		}
		return metadata, err
	}
	if len(document.Content) == 0 {
		return metadata, nil
	}
	root := dereferenceYAMLNode(document.Content[0])
	if root.Kind == yaml.ScalarNode && root.ShortTag() == "!!null" {
		return metadata, nil
	}
	if root.Kind != yaml.MappingNode {
		return metadata, errors.New("configuration root must be a mapping")
	}
	if err := inspectYAMLMapping(root, fields, make(map[*yaml.Node]bool)); err != nil {
		return metadata, err
	}
	var effective map[string]any
	if err := root.Decode(&effective); err != nil {
		return metadata, err
	}
	if value, ok := effective["review_question_ref_key"]; ok && value != nil {
		metadata.markPresent("review_question_ref_key")
	}
	return metadata, nil
}

func inspectYAMLMapping(node *yaml.Node, fields map[string]reflect.Type, visiting map[*yaml.Node]bool) error {
	node = dereferenceYAMLNode(node)
	if node.Kind != yaml.MappingNode {
		return errors.New("YAML merge value must be a mapping or sequence of mappings")
	}
	if visiting[node] {
		return errors.New("cyclic YAML alias")
	}
	visiting[node] = true
	defer delete(visiting, node)

	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := dereferenceYAMLNode(node.Content[i])
		valueNode := node.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			return errors.New("configuration keys must be scalars")
		}
		key := keyNode.Value
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("mapping key %q already defined", key)
		}
		seen[key] = struct{}{}
		if key == "<<" {
			if err := inspectYAMLMerge(valueNode, fields, visiting); err != nil {
				return err
			}
			continue
		}
		fieldType, ok := fields[key]
		if !ok {
			return fmt.Errorf("field %s not found in type config.Config", key)
		}
		if err := inspectYAMLValue(key, valueNode, fieldType); err != nil {
			return err
		}
	}
	return nil
}

func inspectYAMLMerge(node *yaml.Node, fields map[string]reflect.Type, visiting map[*yaml.Node]bool) error {
	node = dereferenceYAMLNode(node)
	switch node.Kind {
	case yaml.MappingNode:
		return inspectYAMLMapping(node, fields, visiting)
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := inspectYAMLMapping(child, fields, visiting); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("YAML merge value must be a mapping or sequence of mappings")
	}
}

func inspectYAMLValue(key string, node *yaml.Node, fieldType reflect.Type) error {
	node = dereferenceYAMLNode(node)
	if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null" {
		return nil
	}
	if fieldType == reflect.TypeFor[time.Duration]() {
		if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" {
			return fmt.Errorf("field %s must be a duration string", key)
		}
		return nil
	}
	switch fieldType.Kind() {
	case reflect.String:
		if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" {
			return fmt.Errorf("field %s must be a string", key)
		}
	case reflect.Bool:
		if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!bool" {
			return fmt.Errorf("field %s must be a boolean", key)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if node.Kind != yaml.ScalarNode {
			return fmt.Errorf("field %s must be an integer", key)
		}
		switch node.ShortTag() {
		case "!!int":
			target := reflect.New(fieldType)
			if err := node.Decode(target.Interface()); err != nil {
				return fmt.Errorf("field %s must be an integer within range", key)
			}
		case "!!float":
			value, ok := new(big.Rat).SetString(node.Value)
			if !ok || !value.IsInt() || !signedIntegerFits(value.Num(), fieldType.Bits()) {
				return fmt.Errorf("field %s must be an integer within range", key)
			}
		default:
			return fmt.Errorf("field %s must be an integer", key)
		}
	case reflect.Slice:
		if fieldType.Elem().Kind() != reflect.String || node.Kind != yaml.SequenceNode {
			return fmt.Errorf("field %s must be a string list", key)
		}
		for _, child := range node.Content {
			child = dereferenceYAMLNode(child)
			if child.Kind != yaml.ScalarNode || child.ShortTag() != "!!str" {
				return fmt.Errorf("field %s must be a string list", key)
			}
		}
	}
	return nil
}

func signedIntegerFits(value *big.Int, bits int) bool {
	limit := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	minimum := new(big.Int).Neg(new(big.Int).Set(limit))
	return value.Cmp(minimum) >= 0 && value.Cmp(limit) < 0
}

func dereferenceYAMLNode(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}

func clearAPISecrets(cfg *Config) {
	cfg.AuthBootstrapToken = ""
	cfg.ReviewQuestionRefKey = ""
	cfg.reviewQuestionRefKeyExplicit = false
}
