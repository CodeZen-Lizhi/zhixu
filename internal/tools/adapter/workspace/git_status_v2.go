package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	readGitStatusV2Version       = int64(2)
	maxGitStatusV2InputBytes     = 4 * 1024
	maxGitStatusAggregateChanges = 100_000
)

var readGitStatusV2Ref = toolsdomain.ToolRef{Name: readGitStatusName, Version: readGitStatusV2Version}

type gitStatusAggregateInspector interface {
	Inspect(context.Context, foundation.ID) (gitcli.StatusAggregate, error)
}

// ReadGitStatusV2Executor 将固定 Git 命令产生的脏状态聚合适配为 Workspace Analysis Tool。
type ReadGitStatusV2Executor struct {
	inspector gitStatusAggregateInspector
}

// NewReadGitStatusV2Executor 创建不接受路径、cwd 或 Git 参数的只读 Executor。
func NewReadGitStatusV2Executor(inspector gitStatusAggregateInspector) (*ReadGitStatusV2Executor, error) {
	if nilDependency(inspector) {
		return nil, gitDependencyError(errors.New("git status aggregate inspector is required"))
	}
	return &ReadGitStatusV2Executor{inspector: inspector}, nil
}

// Execute 只从可信执行身份读取 Workspace，并返回不含文件名和 porcelain 正文的聚合。
func (executor *ReadGitStatusV2Executor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	return executor.execute(ctx, request, readGitStatusV2Ref)
}

func (executor *ReadGitStatusV2Executor) execute(ctx context.Context, request toolsapplication.ExecutorRequest, ref toolsdomain.ToolRef) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.inspector) {
		return toolsapplication.ExecutorResult{}, gitDependencyError(errors.New("git status aggregate inspector is unavailable"))
	}
	if ctx == nil || request.Tool != ref {
		return toolsapplication.ExecutorResult{}, gitInputError(errors.New("git status executor request is invalid"))
	}
	if err := request.Identity.Validate(); err != nil {
		return toolsapplication.ExecutorResult{}, gitInputError(err)
	}
	if err := decodeGitStatusV2Input(request.Arguments); err != nil {
		return toolsapplication.ExecutorResult{}, gitInputError(err)
	}

	aggregate, err := executor.inspector.Inspect(ctx, request.Identity.WorkspaceID)
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := validateGitStatusAggregate(request.Identity.WorkspaceID, aggregate); err != nil {
		return toolsapplication.ExecutorResult{}, gitResultError(err)
	}
	clean := aggregate.Clean
	staged := aggregate.StagedCount
	unstaged := aggregate.UnstagedCount
	untracked := aggregate.UntrackedCount
	conflicts := aggregate.ConflictCount
	output, err := json.Marshal(readGitStatusV2Output{
		Branch: aggregate.Branch, Clean: &clean, ConflictCount: &conflicts, Head: strings.ToLower(aggregate.Head),
		ObjectFormat: aggregate.ObjectFormat, StagedCount: &staged, UnstagedCount: &unstaged, UntrackedCount: &untracked,
	})
	if err != nil {
		return toolsapplication.ExecutorResult{}, gitResultError(err)
	}
	return toolsapplication.ExecutorResult{Output: output}, nil
}

// 字段声明顺序与 canonical JSON key 顺序一致，便于直接测试和故障排查。
type readGitStatusV2Output struct {
	Branch         string `json:"branch"`
	Clean          *bool  `json:"clean"`
	ConflictCount  *int   `json:"conflict_count"`
	Head           string `json:"head"`
	ObjectFormat   string `json:"object_format"`
	StagedCount    *int   `json:"staged_count"`
	UnstagedCount  *int   `json:"unstaged_count"`
	UntrackedCount *int   `json:"untracked_count"`
}

func decodeGitStatusV2Input(raw []byte) error {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxGitStatusV2InputBytes
	limits.MaxDepth = 1
	limits.MaxStringBytes = maxGitStatusV2InputBytes
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 1
	_, err := strictjson.DecodeObject[readGitStatusInput](raw, limits, nil)
	return err
}

func validateGitStatusAggregate(workspaceID foundation.ID, aggregate gitcli.StatusAggregate) error {
	if aggregate.WorkspaceID != workspaceID || aggregate.Branch == "" || len(aggregate.Branch) > 255 ||
		!utf8.ValidString(aggregate.Branch) || strings.ContainsAny(aggregate.Branch, "\x00\r\n") {
		return errors.New("git status aggregate workspace or branch binding is invalid")
	}
	headBytes := 0
	switch aggregate.ObjectFormat {
	case gitcli.StatusObjectFormatSHA1:
		headBytes = 40
	case gitcli.StatusObjectFormatSHA256:
		headBytes = 64
	default:
		return errors.New("git status aggregate object format is invalid")
	}
	if !lowerHexString(aggregate.Head, headBytes) {
		return errors.New("git status aggregate head is invalid")
	}
	counts := []int{aggregate.StagedCount, aggregate.UnstagedCount, aggregate.UntrackedCount, aggregate.ConflictCount}
	changes := 0
	for _, count := range counts {
		if count < 0 || count > maxGitStatusAggregateChanges {
			return errors.New("git status aggregate count is invalid")
		}
		changes += count
	}
	if aggregate.Clean != (changes == 0) {
		return errors.New("git status aggregate clean flag is invalid")
	}
	return nil
}

func lowerHexString(value string, exactBytes int) bool {
	if len(value) != exactBytes {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

var _ toolsapplication.Executor = (*ReadGitStatusV2Executor)(nil)
