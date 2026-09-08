package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	modelSettingsAuditResourceType = "model_settings_revision"
	localDevelopmentActor          = "local-development"
)

func validModelSettingsChange(change application.ModelSettingsChange) bool {
	if change.Action != application.ModelSettingsAuditActionUpdated || change.Revision <= 0 {
		return false
	}
	if change.ChatProvider != domain.ChatProviderDisabled && change.ChatProvider != domain.ChatProviderOpenAICompatible && change.ChatProvider != domain.ChatProviderOllama {
		return false
	}
	if change.ChatAPIStyle != domain.ChatAPIStyleChatCompletions && change.ChatAPIStyle != domain.ChatAPIStyleResponses {
		return false
	}
	switch change.EmbeddingProvider {
	case domain.EmbeddingProviderDisabled, domain.EmbeddingProviderOpenAICompatible, domain.EmbeddingProviderOllama:
	default:
		return false
	}
	return (!change.ChatKeyConfigured || change.ChatProvider == domain.ChatProviderOpenAICompatible) &&
		(!change.EmbeddingKeyConfigured || change.EmbeddingProvider == domain.EmbeddingProviderOpenAICompatible)
}

func modelSettingsAuditID(action application.ModelSettingsAuditAction, revision int64) (foundation.ID, error) {
	digest := sha256.Sum256([]byte("model-settings-audit/v1\x00" + string(action) + "\x00" + strconv.FormatInt(revision, 10)))
	raw := digest[:16]
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw)
	return foundation.ParseID(encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32])
}

func modelSettingsAuditActor(createdBy string) (auditdomain.ActorType, string, error) {
	if createdBy == localDevelopmentActor {
		return auditdomain.ActorAnonymous, localDevelopmentActor, nil
	}
	if strings.HasPrefix(createdBy, "SESSION:") {
		id, err := foundation.ParseID(strings.TrimPrefix(createdBy, "SESSION:"))
		if err != nil {
			return "", "", errors.New("model settings session actor is invalid")
		}
		return auditdomain.ActorUser, string(id), nil
	}
	return auditdomain.ActorSystem, createdBy, nil
}
