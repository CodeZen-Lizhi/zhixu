package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

const (
	questionRefPrefix      = "review-question/v2."
	minQuestionRefKeyBytes = 32
)

// questionRefIssuer 使用部署共享私钥签发不暴露 Card fingerprint 的答题快照引用。
type questionRefIssuer struct {
	// key 是 API 实例共享的 HMAC 私钥副本。
	key []byte
}

// questionRefBinding 是题面与调度快照的规范签名输入。
type questionRefBinding struct {
	// WorkspaceID 绑定题目所属 Workspace。
	WorkspaceID foundation.ID `json:"workspace_id"`
	// SessionID 绑定实际签发题目的 Review Session。
	SessionID foundation.ID `json:"session_id"`
	// DeckID 绑定题目所属 Deck。
	DeckID foundation.ID `json:"deck_id"`
	// CardID 绑定当前 Card。
	CardID foundation.ID `json:"card_id"`
	// CardVersion 绑定用户实际看到的 Card 版本。
	CardVersion int64 `json:"card_version"`
	// CardFingerprint 绑定评分所需的完整 Card 内容。
	CardFingerprint string `json:"card_fingerprint"`
	// ScheduleVersion 绑定 due 查询返回的调度快照。
	ScheduleVersion int64 `json:"schedule_version"`
}

// newQuestionRefIssuer 创建使用部署共享私钥的题目引用签发器。
func newQuestionRefIssuer(key string) (*questionRefIssuer, error) {
	if len(key) < minQuestionRefKeyBytes || key != strings.TrimSpace(key) {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review question reference key is invalid")
	}
	return &questionRefIssuer{key: append([]byte(nil), key...)}, nil
}

// issue 为当前 Session、Card 与 Schedule 快照生成 opaque 引用。
func (issuer *questionRefIssuer) issue(sessionID foundation.ID, card domain.Card, schedule domain.Schedule) (string, error) {
	signature, err := issuer.sign(questionRefBindingFor(sessionID, card, schedule))
	if err != nil {
		return "", err
	}
	return questionRefPrefix + base64.RawURLEncoding.EncodeToString(signature), nil
}

// verify 在评分前验证 opaque 引用仍匹配当前 Session 与题目快照。
func (issuer *questionRefIssuer) verify(value string, sessionID foundation.ID, card domain.Card, schedule domain.Schedule) error {
	if issuer == nil || len(value) <= len(questionRefPrefix) || value[:len(questionRefPrefix)] != questionRefPrefix {
		return staleQuestionRef()
	}
	provided, err := base64.RawURLEncoding.DecodeString(value[len(questionRefPrefix):])
	if err != nil {
		return staleQuestionRef()
	}
	expected, err := issuer.sign(questionRefBindingFor(sessionID, card, schedule))
	if err != nil {
		return err
	}
	if !hmac.Equal(provided, expected) {
		return staleQuestionRef()
	}
	return nil
}

// sign 对规范化快照绑定执行 HMAC-SHA256。
func (issuer *questionRefIssuer) sign(binding questionRefBinding) ([]byte, error) {
	if issuer == nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review question reference issuer is unavailable")
	}
	payload, err := json.Marshal(binding)
	if err != nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review question reference cannot be encoded")
	}
	mac := hmac.New(sha256.New, issuer.key)
	if _, err := mac.Write(payload); err != nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review question reference cannot be signed")
	}
	return mac.Sum(nil), nil
}

// questionRefBindingFor 将会话与领域快照投影为唯一签名输入。
func questionRefBindingFor(sessionID foundation.ID, card domain.Card, schedule domain.Schedule) questionRefBinding {
	return questionRefBinding{
		WorkspaceID: card.WorkspaceID, SessionID: sessionID, DeckID: card.DeckID, CardID: card.ID,
		CardVersion: card.Version, CardFingerprint: card.Fingerprint, ScheduleVersion: schedule.Version,
	}
}

// staleQuestionRef 返回稳定的旧题冲突。
func staleQuestionRef() error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeQuestionStale, false, errors.New("review question no longer matches the current session, card, and schedule"))
}
