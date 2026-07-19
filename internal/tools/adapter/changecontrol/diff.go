// Package changecontrol 提供不访问 Workspace、文件系统或 Git 的纯函数 Tool Adapter。
package changecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	calculateDiffName       = "CalculateDiff"
	maxDiffInputBytes       = 1024 * 1024
	maxDiffSideBytes        = 512 * 1024
	maxDiffOutputBytes      = 1024 * 1024
	errorCodeDiffInput      = "TOOL_CALCULATE_DIFF_INPUT_INVALID"
	errorCodeDiffOutput     = "TOOL_CALCULATE_DIFF_OUTPUT_INVALID"
	errorCodeDiffOutputSize = "TOOL_OUTPUT_TOO_LARGE"
)

// CalculateDiffExecutor 计算有界、确定性的文本统一 Diff。
type CalculateDiffExecutor struct{}

// NewCalculateDiffExecutor 创建无外部依赖的纯函数 Diff Executor。
func NewCalculateDiffExecutor() *CalculateDiffExecutor {
	return &CalculateDiffExecutor{}
}

// Execute 只读取 typed arguments，不访问 Workspace、路径、命令或 Git 状态。
func (executor *CalculateDiffExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if executor == nil {
		return toolsapplication.ExecutorResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "TOOL_CALCULATE_DIFF_UNAVAILABLE", false, errors.New("diff executor is unavailable"))
	}
	if request.Tool.Name != calculateDiffName || request.Tool.Version != 1 || request.Identity.Validate() != nil {
		return toolsapplication.ExecutorResult{}, diffInputError(errors.New("diff executor request binding is invalid"))
	}
	if err := ctx.Err(); err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	input, err := decodeDiffInput(request.Arguments)
	if err != nil {
		return toolsapplication.ExecutorResult{}, diffInputError(err)
	}
	beforeHash := sha256.Sum256([]byte(*input.Before))
	afterHash := sha256.Sum256([]byte(*input.After))
	changed := beforeHash != afterHash
	patch := ""
	if changed {
		patch, err = replacementPatch(ctx, *input.Before, *input.After)
		if err != nil {
			return toolsapplication.ExecutorResult{}, err
		}
	}
	output := calculateDiffOutput{
		Changed:    boolPointer(changed),
		BeforeHash: hex.EncodeToString(beforeHash[:]),
		AfterHash:  hex.EncodeToString(afterHash[:]),
		Patch:      stringPointer(patch),
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return toolsapplication.ExecutorResult{}, diffOutputError(errorCodeDiffOutput, err)
	}
	if len(raw) > maxDiffOutputBytes {
		return toolsapplication.ExecutorResult{}, diffOutputError(errorCodeDiffOutputSize, errors.New("diff output exceeds byte limit"))
	}
	return toolsapplication.ExecutorResult{
		Output:    raw,
		ResultRef: "diff:" + output.BeforeHash + ":" + output.AfterHash,
	}, nil
}

// LoadResultReceipt 以相同 canonical 输入重算纯函数 Diff；不会访问文件、Git、数据库或网络。
func (executor *CalculateDiffExecutor) LoadResultReceipt(ctx context.Context, request toolsapplication.ExecutorRequest, call toolsdomain.ToolCall) (toolsapplication.ExecutorResult, error) {
	if call.Status != toolsdomain.CallSucceeded || call.Tool == nil || call.Tool.Name != calculateDiffName || call.Tool.Version != 1 || call.ResultRef == "" {
		return toolsapplication.ExecutorResult{}, diffOutputError(errorCodeDiffOutput, errors.New("persisted diff receipt binding is invalid"))
	}
	return executor.Execute(ctx, request)
}

type calculateDiffInput struct {
	Before *string `json:"before"`
	After  *string `json:"after"`
}

type calculateDiffOutput struct {
	Changed    *bool   `json:"changed"`
	BeforeHash string  `json:"before_hash"`
	AfterHash  string  `json:"after_hash"`
	Patch      *string `json:"patch"`
}

func decodeDiffInput(raw []byte) (calculateDiffInput, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxDiffInputBytes
	limits.MaxDepth = 2
	limits.MaxStringBytes = maxDiffSideBytes
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 2
	return strictjson.DecodeObject(raw, limits, func(value calculateDiffInput) error {
		if value.Before == nil || value.After == nil || !validDiffText(*value.Before) || !validDiffText(*value.After) {
			return errors.New("diff input is missing, oversized, or invalid")
		}
		return nil
	})
}

func replacementPatch(ctx context.Context, before, after string) (string, error) {
	var builder strings.Builder
	estimated := len(before) + len(after) + 128
	if estimated > maxDiffOutputBytes {
		estimated = maxDiffOutputBytes
	}
	builder.Grow(estimated)
	builder.WriteString("--- before\n+++ after\n")
	builder.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", lineCount(before), lineCount(after)))
	if err := appendPatchSide(ctx, &builder, '-', before); err != nil {
		return "", err
	}
	if err := appendPatchSide(ctx, &builder, '+', after); err != nil {
		return "", err
	}
	if builder.Len() > maxDiffOutputBytes {
		return "", diffOutputError(errorCodeDiffOutputSize, errors.New("diff patch exceeds byte limit"))
	}
	return builder.String(), nil
}

func appendPatchSide(ctx context.Context, builder *strings.Builder, prefix byte, value string) error {
	if value == "" {
		return nil
	}
	start := 0
	lineNo := 0
	for start < len(value) {
		if lineNo%128 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		relative := strings.IndexByte(value[start:], '\n')
		end := len(value)
		terminated := false
		if relative >= 0 {
			end = start + relative + 1
			terminated = true
		}
		builder.WriteByte(prefix)
		builder.WriteString(value[start:end])
		if !terminated {
			builder.WriteString("\n\\ No newline at end of file\n")
		}
		if builder.Len() > maxDiffOutputBytes {
			return diffOutputError(errorCodeDiffOutputSize, errors.New("diff patch exceeds byte limit"))
		}
		start = end
		lineNo++
	}
	return nil
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	count := strings.Count(value, "\n")
	if !strings.HasSuffix(value, "\n") {
		count++
	}
	return count
}

func validDiffText(value string) bool {
	return len(value) <= maxDiffSideBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func boolPointer(value bool) *bool {
	result := value
	return &result
}

func stringPointer(value string) *string {
	result := value
	return &result
}

func diffInputError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeDiffInput, false, cause)
}

func diffOutputError(code string, cause error) error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, cause)
}

var _ toolsapplication.Executor = (*CalculateDiffExecutor)(nil)
var _ toolsapplication.ResultReceiptLoader = (*CalculateDiffExecutor)(nil)
