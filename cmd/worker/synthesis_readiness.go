package main

import (
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	interviewworkflow "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/workflow"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// The real executors remain registered when no model is configured, allowing
// durable work to report an unavailable capability and accept an explicit retry.
func synthesisWorkflowReadiness(components workerComponents) bool {
	if components.goalGenerations == nil || components.goalSelectionExecutor == nil || components.goalSelections == nil || components.synthesisExecutor == nil || components.anchorRecommendationExecutor == nil || components.anchorRecommendations == nil || components.synthesisSources == nil ||
		components.synthesisInterview.executor == nil || components.synthesisInterview.model == nil ||
		components.executors == nil || components.definitions == nil {
		return false
	}
	definitions := organizingworkflow.SynthesisRegisteredDefinitions()
	definitions = append(definitions, organizingworkflow.AnchorRecommendationDefinitions()...)
	definitions = append(definitions, organizingworkflow.GoalSelectionDefinitions()...)
	interview, err := interviewworkflow.NotePreparationDefinition()
	if err != nil {
		return false
	}
	definitions = append(definitions, interview)
	for _, expected := range definitions {
		hash, err := workflowapplication.ComputeCanonicalGraphHash(expected.Graph)
		if err != nil || expected.GraphHash != "" && expected.GraphHash != hash {
			return false
		}
		actual, err := components.definitions.Resolve(expected.Key, expected.Version)
		if err != nil || actual.GraphHash != hash || actual.InputSchemaVersion != expected.InputSchemaVersion {
			return false
		}
		for _, node := range expected.Graph.Nodes {
			if _, err := components.executors.Resolve(node.Kind, node.InputSchemaVersion); err != nil {
				return false
			}
		}
	}
	return true
}
