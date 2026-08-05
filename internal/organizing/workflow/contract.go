// Package workflow binds immutable Organizing snapshots to server-owned Workflow definitions.
package workflow

import (
	"time"

	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// DefinitionVersion freezes the first Organizing Workflow contracts.
	DefinitionVersion int64 = 1
	// InputSchemaVersion freezes the minimal snapshot identity input.
	InputSchemaVersion = 1
	// OutputSchemaVersion freezes the redacted stage and result receipts.
	OutputSchemaVersion = 1

	// TopicArticleDefinitionKey is the fixed topic article workflow.
	TopicArticleDefinitionKey = "organizing.topic-article"
	// MergeDocumentsDefinitionKey is the fixed merge workflow.
	MergeDocumentsDefinitionKey = "organizing.merge-documents"
	// KnowledgeReportDefinitionKey is the fixed knowledge report workflow.
	KnowledgeReportDefinitionKey = "organizing.knowledge-report"
	// InterviewReviewDefinitionKey is the fixed interview review workflow.
	InterviewReviewDefinitionKey = "organizing.interview-review"

	// TopicOutlineNodeKind builds the reviewable topic outline.
	TopicOutlineNodeKind = "organizing.topic.outline"
	// TopicOutlineApprovalNodeKind creates the durable outline review task.
	TopicOutlineApprovalNodeKind = "organizing.topic.outline-approval"
	// TopicArtifactNodeKind writes the approved topic Artifact.
	TopicArtifactNodeKind = "organizing.topic.artifact"
	// MergeCompareNodeKind writes the comparison Artifact shown before confirmation.
	MergeCompareNodeKind = "organizing.merge.compare"
	// MergeConfirmationNodeKind creates the durable merge confirmation task.
	MergeConfirmationNodeKind = "organizing.merge.confirmation"
	// MergeProposalNodeKind writes the confirmed CREATE_ONLY Proposal.
	MergeProposalNodeKind = "organizing.merge.proposal"
	// KnowledgeReportNodeKind writes a knowledge report Artifact.
	KnowledgeReportNodeKind = "organizing.report.artifact"
	// InterviewReviewNodeKind writes an interview review Artifact.
	InterviewReviewNodeKind = "organizing.interview.artifact"
)

var organizingRetryPolicy = workflowdomain.RetryPolicy{
	MaxRetries: 3,
	BaseDelay:  time.Second,
	MaxDelay:   30 * time.Second,
}

// RegisteredDefinitions returns all immutable, server-owned Organizing workflows.
func RegisteredDefinitions() []workflowdomain.RegisteredDefinition {
	read := []workflowdomain.Permission{workflowdomain.PermissionReadLocal}
	writeCandidate := []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal}
	node := func(key, kind string, dependencies []string, permissions []workflowdomain.Permission) workflowdomain.NodeDefinition {
		return workflowdomain.NodeDefinition{
			Key: key, Kind: kind, Dependencies: dependencies,
			InputSchemaVersion: InputSchemaVersion, OutputSchemaVersion: OutputSchemaVersion,
			RetryPolicy: organizingRetryPolicy, RequiredPermissions: permissions,
		}
	}
	return []workflowdomain.RegisteredDefinition{
		{
			Key: TopicArticleDefinitionKey, Version: DefinitionVersion, InputSchemaVersion: InputSchemaVersion,
			Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{
				node(TopicOutlineNodeKind, TopicOutlineNodeKind, nil, read),
				node(TopicOutlineApprovalNodeKind, TopicOutlineApprovalNodeKind, []string{TopicOutlineNodeKind}, read),
				node(TopicArtifactNodeKind, TopicArtifactNodeKind, []string{TopicOutlineApprovalNodeKind}, writeCandidate),
			}},
		},
		{
			Key: MergeDocumentsDefinitionKey, Version: DefinitionVersion, InputSchemaVersion: InputSchemaVersion,
			Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{
				node(MergeCompareNodeKind, MergeCompareNodeKind, nil, writeCandidate),
				node(MergeConfirmationNodeKind, MergeConfirmationNodeKind, []string{MergeCompareNodeKind}, read),
				node(MergeProposalNodeKind, MergeProposalNodeKind, []string{MergeConfirmationNodeKind}, writeCandidate),
			}},
		},
		{
			Key: KnowledgeReportDefinitionKey, Version: DefinitionVersion, InputSchemaVersion: InputSchemaVersion,
			Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{
				node(KnowledgeReportNodeKind, KnowledgeReportNodeKind, nil, writeCandidate),
			}},
		},
		{
			Key: InterviewReviewDefinitionKey, Version: DefinitionVersion, InputSchemaVersion: InputSchemaVersion,
			Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{
				node(InterviewReviewNodeKind, InterviewReviewNodeKind, nil, writeCandidate),
			}},
		},
	}
}

// ExecutorNodeKinds returns every registry key implemented by Executor.
func ExecutorNodeKinds() []string {
	return []string{
		TopicOutlineNodeKind,
		TopicOutlineApprovalNodeKind,
		TopicArtifactNodeKind,
		MergeCompareNodeKind,
		MergeConfirmationNodeKind,
		MergeProposalNodeKind,
		KnowledgeReportNodeKind,
		InterviewReviewNodeKind,
	}
}
