// Package workspace 把服务端 Workspace Git Inspector 适配为只读 Tool Executor。
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	readGitStatusName          = "ReadGitStatus"
	maxGitStatusInputBytes     = 64 * 1024
	errorCodeGitInputInvalid   = "TOOL_READ_GIT_STATUS_INPUT_INVALID"
	errorCodeGitResultInvalid  = "TOOL_READ_GIT_STATUS_RESULT_INVALID"
	errorCodeGitInspectorEmpty = "TOOL_GIT_INSPECTOR_UNAVAILABLE"
)

type gitInspector interface {
	CaptureApprovalSnapshot(context.Context, foundation.ID) (changecontroldomain.GitSnapshot, error)
}

// ReadGitStatusExecutor 通过现有严格 Git Inspector 读取当前 Workspace 的 clean attached HEAD。
//
// 现有 Inspector 对 dirty、冲突、detached、隐藏 index 和危险 filter 均 fail closed，
// 因此本 Executor 只会发布可证明为 clean 的零计数状态，不推测脏仓库的分类计数。
type ReadGitStatusExecutor struct{ inspector gitInspector }

// NewReadGitStatusExecutor 创建只接收 Workspace ID 的 Git 状态 Executor。
func NewReadGitStatusExecutor(inspector gitInspector) (*ReadGitStatusExecutor, error) {
	if nilDependency(inspector) {
		return nil, gitDependencyError(errors.New("git inspector is required"))
	}
	return &ReadGitStatusExecutor{inspector: inspector}, nil
}

// Execute 不接受 path、cwd、command 或 Git args，只使用可信执行身份中的 Workspace ID。
func (executor *ReadGitStatusExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.inspector) {
		return toolsapplication.ExecutorResult{}, gitDependencyError(errors.New("git inspector is unavailable"))
	}
	if request.Tool.Name != readGitStatusName || request.Tool.Version != 1 {
		return toolsapplication.ExecutorResult{}, gitInputError(errors.New("git status executor request references the wrong tool"))
	}
	if err := request.Identity.Validate(); err != nil {
		return toolsapplication.ExecutorResult{}, gitInputError(err)
	}
	if err := decodeEmptyInput(request.Arguments); err != nil {
		return toolsapplication.ExecutorResult{}, gitInputError(err)
	}
	snapshot, err := executor.inspector.CaptureApprovalSnapshot(ctx, request.Identity.WorkspaceID)
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := validateSnapshot(request.Identity.WorkspaceID, snapshot); err != nil {
		return toolsapplication.ExecutorResult{}, gitResultError(err)
	}
	zero := 0
	clean := true
	output := readGitStatusOutput{
		Branch:         snapshot.Branch,
		Head:           strings.ToLower(snapshot.Head),
		ObjectFormat:   string(snapshot.ObjectFormat),
		Clean:          &clean,
		StagedCount:    &zero,
		UnstagedCount:  &zero,
		UntrackedCount: &zero,
		ConflictCount:  &zero,
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return toolsapplication.ExecutorResult{}, gitResultError(err)
	}
	return toolsapplication.ExecutorResult{
		Output:    raw,
		ResultRef: "git-head:" + strings.ToLower(snapshot.Head),
	}, nil
}

// LoadResultReceipt 重新读取服务端 Workspace HEAD；状态漂移会由持久 output/hash/ref 比较 fail closed。
func (executor *ReadGitStatusExecutor) LoadResultReceipt(ctx context.Context, request toolsapplication.ExecutorRequest, call toolsdomain.ToolCall) (toolsapplication.ExecutorResult, error) {
	if call.Status != toolsdomain.CallSucceeded || call.Tool == nil || call.Tool.Name != readGitStatusName || call.Tool.Version != 1 || call.ResultRef == "" {
		return toolsapplication.ExecutorResult{}, gitResultError(errors.New("persisted git status receipt binding is invalid"))
	}
	return executor.Execute(ctx, request)
}

type readGitStatusInput struct{}

type readGitStatusOutput struct {
	Branch         string `json:"branch"`
	Head           string `json:"head"`
	ObjectFormat   string `json:"object_format"`
	Clean          *bool  `json:"clean"`
	StagedCount    *int   `json:"staged_count"`
	UnstagedCount  *int   `json:"unstaged_count"`
	UntrackedCount *int   `json:"untracked_count"`
	ConflictCount  *int   `json:"conflict_count"`
}

func decodeEmptyInput(raw []byte) error {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxGitStatusInputBytes
	limits.MaxDepth = 1
	limits.MaxStringBytes = maxGitStatusInputBytes
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 1
	_, err := strictjson.DecodeObject[readGitStatusInput](raw, limits, nil)
	return err
}

func validateSnapshot(workspaceID foundation.ID, snapshot changecontroldomain.GitSnapshot) error {
	if !snapshot.Clean || snapshot.WorkspaceID != workspaceID || snapshot.Branch == "" || len(snapshot.Branch) > 255 ||
		!utf8.ValidString(snapshot.Branch) || strings.ContainsAny(snapshot.Branch, "\x00\r\n") {
		return errors.New("git inspector returned an invalid workspace or branch binding")
	}
	if err := changecontroldomain.ValidateGitSnapshotBinding(workspaceID, snapshot.Head, snapshot); err != nil {
		return err
	}
	return nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func gitDependencyError(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeGitInspectorEmpty, false, cause)
}

func gitInputError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeGitInputInvalid, false, cause)
}

func gitResultError(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeGitResultInvalid, false, cause)
}

var _ toolsapplication.Executor = (*ReadGitStatusExecutor)(nil)
var _ toolsapplication.ResultReceiptLoader = (*ReadGitStatusExecutor)(nil)
