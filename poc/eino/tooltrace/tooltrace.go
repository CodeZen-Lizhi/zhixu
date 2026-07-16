package tooltrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const operationExecute = "eino_tool.execute"

// Permission identifies one server-side capability required by a tool.
type Permission string

const (
	PermissionReadLocal     Permission = "READ_LOCAL"
	PermissionReadExternal  Permission = "READ_EXTERNAL"
	PermissionWriteProposal Permission = "WRITE_PROPOSAL"
)

// Parameter describes the project-owned subset of JSON Schema needed by this PoC.
type Parameter struct {
	Type        string
	Description string
	Required    bool
}

// Handler is the real tool implementation behind validation and authorization.
type Handler func(ctx context.Context, arguments map[string]any) (string, error)

// Definition is a project-owned tool contract. It intentionally contains no Eino types.
type Definition struct {
	Name        string
	Description string
	Permission  Permission
	Parameters  map[string]Parameter
	Timeout     time.Duration
	RateLimit   Limit
	Handler     Handler
}

// Request represents one model-requested tool call without exposing Eino messages.
type Request struct {
	CallID    string
	ToolName  string
	Arguments string
}

// Result is the project-owned result of one tool call.
type Result struct {
	CallID  string
	Content string
}

type authorizationKey struct{}

// WithPermissions adds trusted server-side capabilities to a context.
func WithPermissions(ctx context.Context, permissions ...Permission) context.Context {
	granted := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		granted[permission] = struct{}{}
	}
	return context.WithValue(ctx, authorizationKey{}, granted)
}

// Runner executes project tool requests through an Eino ToolsNode.
type Runner struct {
	runnable compose.Runnable[*schema.Message, []*schema.Message]
	handler  callbacks.Handler
}

// NewRunner builds an isolated Eino graph containing one secured ToolsNode.
func NewRunner(ctx context.Context, definitions []Definition, limiter *Limiter, sink TraceSink, redactor *Redactor) (*Runner, error) {
	if len(definitions) == 0 {
		return nil, invalidInput("at least one tool definition is required")
	}
	if limiter == nil {
		limiter = NewLimiter(systemClock{})
	}

	tools := make([]tool.BaseTool, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if err := validateDefinition(definition); err != nil {
			return nil, err
		}
		if _, exists := seen[definition.Name]; exists {
			return nil, invalidInput(fmt.Sprintf("duplicate tool name %q", definition.Name))
		}
		seen[definition.Name] = struct{}{}
		tools = append(tools, &securedTool{definition: cloneDefinition(definition), limiter: limiter})
	}

	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: tools,
		UnknownToolsHandler: func(_ context.Context, name, _ string) (string, error) {
			return "", &contract.Error{
				Kind:      contract.ErrorNotFound,
				Operation: operationExecute,
				Cause:     fmt.Errorf("tool %q is not registered", name),
			}
		},
	})
	if err != nil {
		return nil, &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: "eino_tool.new",
			Cause:     fmt.Errorf("create Eino tools node: %w", err),
		}
	}

	graph := compose.NewGraph[*schema.Message, []*schema.Message]()
	if err := graph.AddToolsNode("secured_tools", node); err != nil {
		return nil, constructionError("add tools node", err)
	}
	if err := graph.AddEdge(compose.START, "secured_tools"); err != nil {
		return nil, constructionError("connect graph start", err)
	}
	if err := graph.AddEdge("secured_tools", compose.END); err != nil {
		return nil, constructionError("connect graph end", err)
	}

	runnable, err := graph.Compile(ctx, compose.WithGraphName("zhixu_eino_tool_poc"))
	if err != nil {
		return nil, constructionError("compile tool graph", err)
	}

	return &Runner{runnable: runnable, handler: newTraceHandler(sink, redactor)}, nil
}

// Run executes a single tool call and maps all failures to project error kinds.
func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	if r == nil || r.runnable == nil {
		return Result{}, &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationExecute,
			Cause:     errors.New("tool runner is not initialized"),
		}
	}
	if strings.TrimSpace(request.CallID) == "" {
		return Result{}, invalidInput("tool call id is required")
	}
	if strings.TrimSpace(request.ToolName) == "" {
		return Result{}, invalidInput("tool name is required")
	}

	message := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: request.CallID,
			Function: schema.FunctionCall{
				Name:      request.ToolName,
				Arguments: request.Arguments,
			},
		}},
	}

	options := make([]compose.Option, 0, 1)
	if r.handler != nil {
		options = append(options, compose.WithCallbacks(r.handler))
	}
	output, err := r.runnable.Invoke(ctx, message, options...)
	if err != nil {
		return Result{}, classifyError(err)
	}
	if len(output) != 1 || output[0] == nil {
		return Result{}, &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationExecute,
			Cause:     fmt.Errorf("unexpected tool output count %d", len(output)),
		}
	}

	return Result{CallID: output[0].ToolCallID, Content: output[0].Content}, nil
}

type securedTool struct {
	definition Definition
	limiter    *Limiter
}

func (t *securedTool) Info(context.Context) (*schema.ToolInfo, error) {
	parameters := make(map[string]*schema.ParameterInfo, len(t.definition.Parameters))
	for name, parameter := range t.definition.Parameters {
		parameters[name] = &schema.ParameterInfo{
			Type:     schema.DataType(parameter.Type),
			Desc:     parameter.Description,
			Required: parameter.Required,
		}
	}
	return &schema.ToolInfo{
		Name:        t.definition.Name,
		Desc:        t.definition.Description,
		ParamsOneOf: schema.NewParamsOneOfByParams(parameters),
	}, nil
}

func (t *securedTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	parsed, err := validateArguments(arguments, t.definition.Parameters)
	if err != nil {
		return "", err
	}

	granted, _ := ctx.Value(authorizationKey{}).(map[Permission]struct{})
	if _, allowed := granted[t.definition.Permission]; !allowed {
		return "", &contract.Error{
			Kind:      contract.ErrorPermissionDenied,
			Operation: operationExecute,
			Cause:     fmt.Errorf("permission %s is required for tool %q", t.definition.Permission, t.definition.Name),
		}
	}

	trace := TraceFromContext(ctx)
	if t.definition.RateLimit.Requests > 0 && trace.RequestID == "" && trace.WorkflowRunID == "" && trace.NodeRunID == "" {
		return "", invalidInput("rate-limited tool execution requires a trace correlation id")
	}
	limitKey := strings.Join([]string{trace.RequestID, trace.WorkflowRunID, trace.NodeRunID, t.definition.Name}, ":")
	if !t.limiter.Allow(limitKey, t.definition.RateLimit) {
		return "", &contract.Error{
			Kind:      contract.ErrorRetryableFailure,
			Operation: operationExecute,
			Retryable: true,
			Cause:     fmt.Errorf("rate limit exceeded for tool %q", t.definition.Name),
		}
	}

	executionContext := ctx
	cancel := func() {}
	if t.definition.Timeout > 0 {
		executionContext, cancel = context.WithTimeout(ctx, t.definition.Timeout)
	}
	defer cancel()

	result, err := t.definition.Handler(executionContext, parsed)
	if err != nil {
		if t.definition.Permission == PermissionWriteProposal {
			var classified *contract.Error
			if errors.As(err, &classified) {
				return "", classified
			}
			return "", &contract.Error{
				Kind:      contract.ErrorManualRecovery,
				Operation: operationExecute,
				Cause:     err,
			}
		}
		return "", classifyError(err)
	}
	return result, nil
}

func cloneDefinition(definition Definition) Definition {
	cloned := definition
	cloned.Parameters = make(map[string]Parameter, len(definition.Parameters))
	for name, parameter := range definition.Parameters {
		cloned.Parameters[name] = parameter
	}
	return cloned
}

func validateDefinition(definition Definition) error {
	if strings.TrimSpace(definition.Name) == "" {
		return invalidInput("tool name is required")
	}
	if definition.Permission == "" {
		return invalidInput(fmt.Sprintf("permission is required for tool %q", definition.Name))
	}
	if definition.Handler == nil {
		return invalidInput(fmt.Sprintf("handler is required for tool %q", definition.Name))
	}
	if definition.Timeout < 0 {
		return invalidInput(fmt.Sprintf("timeout cannot be negative for tool %q", definition.Name))
	}
	for name, parameter := range definition.Parameters {
		if strings.TrimSpace(name) == "" {
			return invalidInput(fmt.Sprintf("tool %q has an empty parameter name", definition.Name))
		}
		switch parameter.Type {
		case string(schema.String), string(schema.Number), string(schema.Integer), string(schema.Boolean), string(schema.Object), string(schema.Array):
		default:
			return invalidInput(fmt.Sprintf("tool %q parameter %q has unsupported type %q", definition.Name, name, parameter.Type))
		}
	}
	if err := definition.RateLimit.validate(); err != nil {
		return invalidInput(fmt.Sprintf("tool %q rate limit: %v", definition.Name, err))
	}
	return nil
}

func validateArguments(arguments string, parameters map[string]Parameter) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()

	parsed := make(map[string]any)
	if err := decoder.Decode(&parsed); err != nil {
		return nil, &contract.Error{
			Kind:      contract.ErrorInvalidInput,
			Operation: operationExecute,
			Cause:     fmt.Errorf("decode tool arguments: %w", err),
		}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, invalidInput(err.Error())
	}
	if parsed == nil {
		return nil, invalidInput("tool arguments must be a JSON object")
	}

	for name := range parsed {
		if _, exists := parameters[name]; !exists {
			return nil, invalidInput(fmt.Sprintf("unknown tool argument %q", name))
		}
	}
	for name, parameter := range parameters {
		value, exists := parsed[name]
		if parameter.Required && (!exists || value == nil) {
			return nil, invalidInput(fmt.Sprintf("required tool argument %q is missing", name))
		}
		if exists && value != nil && !matchesType(value, parameter.Type) {
			return nil, invalidInput(fmt.Sprintf("tool argument %q must be %s", name, parameter.Type))
		}
	}
	return parsed, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("tool arguments contain multiple JSON values")
	}
	return fmt.Errorf("decode trailing tool arguments: %w", err)
}

func matchesType(value any, parameterType string) bool {
	switch parameterType {
	case string(schema.String):
		_, ok := value.(string)
		return ok
	case string(schema.Number):
		_, ok := value.(json.Number)
		return ok
	case string(schema.Integer):
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case string(schema.Boolean):
		_, ok := value.(bool)
		return ok
	case string(schema.Object):
		_, ok := value.(map[string]any)
		return ok
	case string(schema.Array):
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func invalidInput(message string) error {
	return &contract.Error{
		Kind:      contract.ErrorInvalidInput,
		Operation: operationExecute,
		Cause:     errors.New(message),
	}
}

func constructionError(action string, err error) error {
	return &contract.Error{
		Kind:      contract.ErrorNonRetryableFailure,
		Operation: "eino_tool.new",
		Cause:     fmt.Errorf("%s: %w", action, err),
	}
}

func classifyError(err error) error {
	var classified *contract.Error
	if errors.As(err, &classified) {
		return classified
	}
	if errors.Is(err, context.Canceled) {
		return &contract.Error{
			Kind:      contract.ErrorNonRetryableFailure,
			Operation: operationExecute,
			Cause:     err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &contract.Error{
			Kind:      contract.ErrorRetryableFailure,
			Operation: operationExecute,
			Retryable: true,
			Cause:     err,
		}
	}
	return &contract.Error{
		Kind:      contract.ErrorNonRetryableFailure,
		Operation: operationExecute,
		Cause:     err,
	}
}

// bindToolSchemas proves that Eino's immutable WithTools API can consume the project definitions.
func bindToolSchemas(base model.ToolCallingChatModel, definitions []Definition) (model.ToolCallingChatModel, error) {
	if base == nil {
		return nil, invalidInput("tool calling model is required")
	}
	infos := make([]*schema.ToolInfo, 0, len(definitions))
	for _, definition := range definitions {
		if err := validateDefinition(definition); err != nil {
			return nil, err
		}
		secured := &securedTool{definition: cloneDefinition(definition), limiter: NewLimiter(systemClock{})}
		info, err := secured.Info(context.Background())
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return base.WithTools(infos)
}
