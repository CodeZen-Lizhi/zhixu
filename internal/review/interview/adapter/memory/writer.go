// Package memory maps Interview-owned facts to the Memory application boundary.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

// CandidateCreator 是 Interview 创建待确认 Memory 所需的最小应用命令面。
type CandidateCreator interface {
	CreateCandidate(context.Context, memoryapp.CreateCandidateCommand) (memoryapp.CommandResult, error)
}

// Writer 将已持久化的 Interview 学习步骤映射为可信 GOAL Candidate。
type Writer struct{ creator CandidateCreator }

// NewWriter 创建固定单用户 owner 与 Interview provenance 的 Candidate Writer。
func NewWriter(creator CandidateCreator) (*Writer, error) {
	if nilDependency(creator) {
		return nil, interviewdomain.UnavailableError(interviewdomain.ErrorCodeDependencyUnavailable, "interview memory candidate dependency is unavailable")
	}
	return &Writer{creator: creator}, nil
}

// CreateCandidate 只创建 CANDIDATE；确认仍由 Memory 用户命令独立完成。
func (writer *Writer) CreateCandidate(ctx context.Context, request interviewapp.MemoryCandidateRequest) (interviewapp.MemoryCandidateResult, error) {
	if writer == nil || nilDependency(writer.creator) {
		return interviewapp.MemoryCandidateResult{}, interviewdomain.UnavailableError(interviewdomain.ErrorCodeDependencyUnavailable, "interview memory candidate dependency is unavailable")
	}
	content, err := validateCandidateRequest(request)
	if err != nil {
		return interviewapp.MemoryCandidateResult{}, err
	}
	owner := memorydomain.SingleUserOwner()
	source := memorydomain.Source{Type: memorydomain.SourceInterview, Ref: candidateSourceRef(request.SessionID, request.PathID, request.StepID)}
	taskScopeID := request.SessionID
	result, err := writer.creator.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID: request.WorkspaceID,
		Owner:       owner,
		Type:        memorydomain.TypeGoal,
		Content:     content,
		Source:      source,
		TaskScopeID: &taskScopeID,
		// 客户端 key 保留命令身份；服务端 provenance 单独收敛同一步骤的 Candidate identity。
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return interviewapp.MemoryCandidateResult{}, err
	}
	memory := result.Memory
	if memorydomain.ValidateMemory(memory) != nil || memory.WorkspaceID != request.WorkspaceID || memory.Owner != owner ||
		memory.Type != memorydomain.TypeGoal || !bytes.Equal(memory.Content, content) || memory.Source != source ||
		memory.TaskScopeID == nil || *memory.TaskScopeID != request.SessionID || memory.Status != memorydomain.StatusCandidate ||
		memory.Version != 1 || memory.ExpiresAt != nil {
		return interviewapp.MemoryCandidateResult{}, interviewdomain.InvalidError(interviewdomain.ErrorCodePersistenceInvalid, "interview memory candidate result is inconsistent")
	}
	return interviewapp.MemoryCandidateResult{MemoryID: memory.ID, Replayed: result.Replayed}, nil
}

func validateCandidateRequest(request interviewapp.MemoryCandidateRequest) ([]byte, error) {
	if !validID(request.WorkspaceID) || !validID(request.SessionID) || !validID(request.PathID) || !validID(request.StepID) ||
		memorydomain.ValidateIdempotencyKey(request.IdempotencyKey) != nil {
		return nil, interviewdomain.InvalidError(interviewdomain.ErrorCodePathInvalid, "interview memory candidate request is invalid")
	}
	content, err := memorydomain.CanonicalContent(request.Content)
	if err != nil || !bytes.Equal(content, request.Content) {
		return nil, interviewdomain.InvalidError(interviewdomain.ErrorCodePathInvalid, "interview memory candidate content is invalid")
	}
	return content, nil
}

func candidateSourceRef(sessionID, pathID, stepID foundation.ID) string {
	return fmt.Sprintf("interview:%s:learning-path:%s:step:%s", sessionID, pathID, stepID)
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
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

var _ interviewapp.MemoryCandidateWriter = (*Writer)(nil)
