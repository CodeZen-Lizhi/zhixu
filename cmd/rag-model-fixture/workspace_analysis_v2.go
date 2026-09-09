package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func isWorkspaceAnalysisLoopFixtureRequest(request chatRequest) bool {
	input, err := lastTaskInput(request.Messages)
	return err == nil && input["schema_version"] == float64(2) &&
		hasExactAnyKeys(input, "schema_version", "untrusted_data", "question", "history", "answer_depth", "output_format")
}

func serveWorkspaceAnalysisLoopFixture(w http.ResponseWriter, request chatRequest, modelVersion string) error {
	if request.Stream || request.ResponseFormat.Type != "" || request.ResponseFormat.JSONSchema.Strict || request.StreamOptions != nil ||
		request.MaxTokens != 512 || !hasDefaultAutoToolChoice(request.ToolChoice) || len(request.Tools) != 4 {
		return fixtureError("dynamic_loop_contract_invalid", "unsupported dynamic loop contract")
	}
	expected := map[string]string{"ReadGitStatus": "", "SearchKnowledge": "query", "ReadSource": "evidence_ref", "ValidateCitation": "evidence_refs"}
	for _, tool := range request.Tools {
		parameter, found := expected[tool.Function.Name]
		var schema map[string]any
		if !found || tool.Type != "function" || json.Unmarshal(tool.Function.Parameters, &schema) != nil || schema["type"] != "object" || schema["additionalProperties"] != false {
			return fixtureError("dynamic_loop_tools_invalid", "unsupported dynamic loop tools")
		}
		properties, ok := schema["properties"].(map[string]any)
		required, requiredOK := schema["required"].([]any)
		if parameter == "" {
			// Eino omits empty properties/required when serializing the Git
			// tool. Their absence still permits only an empty object because
			// additionalProperties is false.
			_, hasProperties := schema["properties"]
			_, hasRequired := schema["required"]
			if hasProperties && !ok || hasRequired && !requiredOK || len(properties) != 0 || len(required) != 0 {
				return fixtureError("dynamic_loop_tool_schema_invalid", "dynamic loop tool arguments drifted")
			}
		} else if !ok || !requiredOK || !hasExactAnyKeys(properties, parameter) || len(required) != 1 || required[0] != parameter {
			return fixtureError("dynamic_loop_tool_schema_invalid", "dynamic loop tool arguments drifted")
		}
		delete(expected, tool.Function.Name)
	}
	input, err := lastTaskInput(request.Messages)
	if err != nil || !hasExactAnyKeys(input, "schema_version", "untrusted_data", "question", "history", "answer_depth", "output_format") ||
		input["schema_version"] != float64(2) || input["untrusted_data"] != true || !fixtureBoundedString(input["question"]) || workspaceAnalysisInputLeaksIdentity(input) {
		return fixtureError("dynamic_loop_input_invalid", "dynamic loop input is invalid")
	}
	if len(request.Messages) < 2 || len(request.Messages) > 26 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" || (len(request.Messages)-2)%2 != 0 {
		return fixtureError("dynamic_loop_transcript_invalid", "dynamic loop transcript is invalid")
	}
	var lastName string
	var lastResult map[string]any
	searchCount := 0
	readRefs := []string{}
	seenReads := map[string]bool{}
	for index := 2; index < len(request.Messages); index += 2 {
		assistant, result := request.Messages[index], request.Messages[index+1]
		if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 || result.Role != "tool" || len(result.ToolCalls) != 0 ||
			assistant.ToolCalls[0].ID != "decision-"+strconv.Itoa(index/2) || result.ToolCallID != assistant.ToolCalls[0].ID {
			return fixtureError("dynamic_loop_exchange_unbound", "dynamic loop exchange is unbound")
		}
		lastName = assistant.ToolCalls[0].Function.Name
		var content string
		if json.Unmarshal(result.Content, &content) != nil || json.Unmarshal([]byte(content), &lastResult) != nil || workspaceAnalysisInputLeaksIdentity(lastResult) {
			return fixtureError("dynamic_loop_tool_output_invalid", "dynamic loop tool output is invalid")
		}
		switch lastName {
		case "SearchKnowledge":
			searchCount++
		case "ReadSource":
			ref, ok := lastResult["evidence_ref"].(string)
			if !ok || !fixtureDynamicEvidenceRef(ref) {
				return fixtureError("dynamic_loop_source_invalid", "dynamic loop source reference is invalid")
			}
			seenReads[ref] = true
			if lastResult["truncated"] == false {
				readRefs = append(readRefs, ref)
			}
		case "ReadGitStatus", "ValidateCitation":
		default:
			return fixtureError("dynamic_loop_tool_unknown", "dynamic loop transcript contains an unknown tool")
		}
	}
	ordinal := (len(request.Messages)-2)/2 + 1
	question := strings.ToLower(input["question"].(string))
	extended := strings.Contains(question, "extended") || strings.Contains(question, "追加检索") || strings.Contains(question, "two searches")
	if strings.Contains(question, "budget-loop") {
		return writeWorkspaceAnalysisFixtureTool(w, modelVersion, ordinal, "ReadGitStatus", map[string]any{})
	}
	query := "approved recovery"
	var plan map[string]any
	if json.Unmarshal([]byte(workspaceAnalysisPlannerFixtureResponse(input)), &plan) == nil {
		if rewrites, ok := plan["r"].([]any); ok && len(rewrites) > 0 {
			if value, ok := rewrites[0].(string); ok {
				query = value
			}
		}
	}
	switch lastName {
	case "":
		if !extended {
			return writeWorkspaceAnalysisFixtureTool(w, modelVersion, ordinal, "ReadGitStatus", map[string]any{})
		}
		return writeWorkspaceAnalysisFixtureTool(w, modelVersion, ordinal, "SearchKnowledge", map[string]any{"query": query})
	case "ReadGitStatus":
		return writeWorkspaceAnalysisFixtureTool(w, modelVersion, ordinal, "SearchKnowledge", map[string]any{"query": query})
	case "SearchKnowledge":
		items, ok := lastResult["items"].([]any)
		if !ok {
			return fixtureError("dynamic_loop_search_invalid", "dynamic loop search result is invalid")
		}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				return fixtureError("dynamic_loop_search_invalid", "dynamic loop search item is invalid")
			}
			ref, ok := item["evidence_ref"].(string)
			if !ok || !fixtureDynamicEvidenceRef(ref) {
				return fixtureError("dynamic_loop_search_invalid", "dynamic loop search reference is invalid")
			}
			if !seenReads[ref] {
				return writeWorkspaceAnalysisFixtureTool(w, modelVersion, ordinal, "ReadSource", map[string]any{"evidence_ref": ref})
			}
		}
	case "ReadSource":
		if extended && searchCount == 1 {
			return writeWorkspaceAnalysisFixtureTool(w, modelVersion, ordinal, "SearchKnowledge", map[string]any{"query": query + " evidence"})
		}
		if len(readRefs) > 0 {
			return writeWorkspaceAnalysisFixtureTool(w, modelVersion, ordinal, "ValidateCitation", map[string]any{"evidence_refs": readRefs})
		}
	case "ValidateCitation":
	}
	writeCompletion(w, modelVersion, `{"action":"finish","query":null,"evidence_ref":null,"evidence_refs":null}`, nil, "stop")
	return nil
}

func writeWorkspaceAnalysisFixtureTool(w http.ResponseWriter, modelVersion string, ordinal int, name string, args any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	call := toolCall{ID: "fixture-dynamic-" + strconv.Itoa(ordinal), Type: "function"}
	call.Function.Name, call.Function.Arguments = name, string(raw)
	writeCompletion(w, modelVersion, "", []toolCall{call}, "tool_calls")
	return nil
}

func fixtureDynamicEvidenceRef(ref string) bool {
	if len(ref) < 2 || len(ref) > 3 || ref[0] != 'E' {
		return false
	}
	n, err := strconv.Atoi(ref[1:])
	return err == nil && n >= 1 && n <= 32 && ref == "E"+strconv.Itoa(n)
}

func workspaceAnalysisFixtureEvidenceRefsV2(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) < 1 || len(values) > 8 {
		return nil, fixtureError("workspace_analysis_evidence_refs_invalid", "workspace analysis evidence refs are invalid")
	}
	refs := make([]string, len(values))
	seen := map[string]bool{}
	for index, value := range values {
		ref, ok := value.(string)
		if !ok || !fixtureDynamicEvidenceRef(ref) || seen[ref] {
			return nil, fixtureError("workspace_analysis_evidence_refs_invalid", "workspace analysis evidence refs are invalid")
		}
		seen[ref] = true
		refs[index] = ref
	}
	return refs, nil
}

func isWorkspaceAnalysisCandidateProviderSchemaV2(raw json.RawMessage) bool {
	return isWorkspaceAnalysisCandidateProviderSchemaVersion(raw, "2", func(raw json.RawMessage) bool {
		refs := make([]string, 32)
		for index := range refs {
			refs[index] = "E" + strconv.Itoa(index+1)
		}
		var actual map[string]any
		if json.Unmarshal(raw, &actual) != nil {
			return false
		}
		canonical, _ := json.Marshal(actual)
		return jsonRawEquals(canonical, map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": refs}})
	})
}

func workspaceAnalysisCandidateFixtureInputV2(input map[string]any) ([]string, error) {
	if !hasExactAnyKeys(input, "schema_version", "untrusted_data", "question", "history", "answer_depth", "output_format", "git_status", "searches", "evidence") ||
		input["schema_version"] != float64(2) || input["untrusted_data"] != true || !fixtureBoundedString(input["question"]) || workspaceAnalysisInputLeaksIdentity(input) {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis v2 candidate input is invalid")
	}
	if _, ok := input["history"].([]any); !ok {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis v2 history is invalid")
	}
	if input["git_status"] != nil {
		if _, ok := input["git_status"].(map[string]any); !ok {
			return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis v2 Git status is invalid")
		}
	}
	searches, ok := input["searches"].([]any)
	if !ok || len(searches) < 1 || len(searches) > 12 {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis v2 searches are invalid")
	}
	for _, raw := range searches {
		search, ok := raw.(map[string]any)
		if !ok || !hasExactAnyKeys(search, "effective_mode", "hit_count", "degradation_codes") || !fixtureBoundedString(search["effective_mode"]) {
			return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis v2 search summary is invalid")
		}
		count, ok := search["hit_count"].(float64)
		if !ok || count < 1 || count > 5 || count != float64(int(count)) {
			return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis v2 search summary is invalid")
		}
		if _, ok := search["degradation_codes"].([]any); !ok {
			return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis v2 search degradations are invalid")
		}
	}
	evidence, ok := input["evidence"].([]any)
	if !ok || len(evidence) < 1 || len(evidence) > 8 {
		return nil, fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis v2 evidence is invalid")
	}
	refs := make([]any, len(evidence))
	for index, raw := range evidence {
		item, ok := raw.(map[string]any)
		if !ok || !hasExactAnyKeys(item, "evidence_ref", "excerpt", "truncated") || item["truncated"] != false || !fixtureBoundedExcerpt(item["excerpt"]) {
			return nil, fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis v2 evidence item is invalid")
		}
		refs[index] = item["evidence_ref"]
	}
	return workspaceAnalysisFixtureEvidenceRefsV2(refs)
}

func serveWorkspaceAnalysisCandidateStreamV2(w http.ResponseWriter, request chatRequest, modelVersion string, refs []string) error {
	input, err := lastTaskInput(request.Messages)
	if err != nil {
		return err
	}
	verifiedRefs, err := workspaceAnalysisCandidateFixtureInputV2(input)
	if err != nil {
		return err
	}
	if len(refs) != len(verifiedRefs) {
		return fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis v2 evidence references drifted")
	}
	for index, ref := range refs {
		if ref != verifiedRefs[index] {
			return fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis v2 evidence references drifted")
		}
	}
	evidence := input["evidence"].([]any)
	excerpt := evidence[0].(map[string]any)["excerpt"].(string)
	body := "The supplied source states: " + strings.TrimSpace(excerpt) + " [" + refs[0] + "]."
	document, err := json.Marshal(map[string]any{"result_type": "workspace_analysis_candidate", "schema_id": "agent.workspace-analysis-candidate", "schema_version": "2",
		"payload": map[string]any{"answer_markdown": body, "citation_refs": []string{refs[0]}, "proposal_suggestion": nil}})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/event-stream")
	frames := []any{
		map[string]any{"id": "stream-workspace-analysis-v2", "object": "chat.completion.chunk", "created": 1, "model": modelVersion, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(document)}, "finish_reason": "stop"}}},
		map[string]any{"id": "stream-workspace-analysis-v2", "object": "chat.completion.chunk", "created": 1, "model": modelVersion, "choices": []any{}, "usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}},
	}
	for _, frame := range frames {
		raw, _ := json.Marshal(frame)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	_, err = io.WriteString(w, "data: [DONE]\n\n")
	return err
}
