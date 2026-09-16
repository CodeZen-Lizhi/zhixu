package application

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisProcessingKey 使目标身份独立于溯源种子。
// 重试使用同一目标，两个目标可以使用同一个原始来源事件。
func SynthesisProcessingKey(event domain.SynthesisSourceReady, goalID foundation.ID) (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	if goalID == "" {
		return event.ProcessingKey()
	}
	if !validID(goalID) || event.Fusion != nil {
		return "", goalSelectionInvalid()
	}
	sum := sha256.Sum256([]byte(string(event.Source.WorkspaceID) + ":" + string(goalID)))
	return "synthesis-goal:" + hex.EncodeToString(sum[:]), nil
}

// SynthesisExecutionKey 保留全部旧键，并为正文刷新提供独立命名空间。
// 请求所属模块唯一绑定目标与发布。
func SynthesisExecutionKey(event domain.SynthesisSourceReady, goalID, bodyRefreshID foundation.ID) (string, error) {
	if bodyRefreshID == "" {
		return SynthesisProcessingKey(event, goalID)
	}
	if event.Validate() != nil || event.Fusion != nil || goalID != "" || !validID(bodyRefreshID) {
		return "", invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis refresh execution identity is invalid")
	}
	sum := sha256.Sum256([]byte(string(event.Source.WorkspaceID) + ":" + string(bodyRefreshID)))
	return "synthesis-body-refresh:" + hex.EncodeToString(sum[:]), nil
}
