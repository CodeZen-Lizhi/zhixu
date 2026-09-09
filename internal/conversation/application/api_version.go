package application

import (
	"errors"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// APIVersion is a trusted response capability, not part of a Question's identity.
// Zero retains the unrestricted internal application contract. HTTP routes must
// explicitly select one of the supported public versions.
type APIVersion int

const (
	APIVersionV1 APIVersion = 1
	APIVersionV2 APIVersion = 2

	// ErrorCodeAPIVersionUnsupported requires the caller to use a compatible API.
	ErrorCodeAPIVersionUnsupported = "CONVERSATION_API_VERSION_UNSUPPORTED"
)

// Validate rejects unsupported caller-selected response contracts before work.
func (version APIVersion) Validate() error {
	if version != 0 && version != APIVersionV1 && version != APIVersionV2 {
		return requestInvalid(errors.New("conversation API version is invalid"))
	}
	return nil
}

// CheckWorkflow uses the immutable Workflow definition, including before an
// Answer has a result and when a dynamic run publishes a v1 refusal envelope.
func (version APIVersion) CheckWorkflow(key string, definitionVersion int64) error {
	if err := version.Validate(); err != nil {
		return err
	}
	if version == 0 {
		return nil
	}
	if definitionVersion < 1 {
		return resultInconsistent(errors.New("answer workflow definition is missing"))
	}
	switch key {
	case conversationworkflow.DefinitionKey:
		// Both fixed RAG workflow versions use the original HTTP response shape.
		if definitionVersion <= conversationworkflow.DefinitionVersionV2 {
			return nil
		}
	case conversationworkflow.WorkspaceAnalysisDefinitionKey:
		if definitionVersion <= int64(version) {
			return nil
		}
	default:
		return resultInconsistent(errors.New("answer workflow definition is unsupported"))
	}
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeAPIVersionUnsupported, false,
		errors.New("answer workflow requires a newer conversation API version"))
}

// CheckTimeline verifies the public snapshot as well as the repository's
// admission check on the persisted Analysis Run definition.
func (version APIVersion) CheckTimeline(timeline conversationdomain.WorkspaceAnalysisTimeline) error {
	var definitionVersion int64
	switch timeline.SchemaVersion {
	case conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV1:
		definitionVersion = 1
	case conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV2:
		definitionVersion = 2
	default:
		return resultInconsistent(errors.New("workspace analysis timeline version is unsupported"))
	}
	return version.CheckWorkflow(conversationworkflow.WorkspaceAnalysisDefinitionKey, definitionVersion)
}
