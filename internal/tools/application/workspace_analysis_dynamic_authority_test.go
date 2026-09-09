package application_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestDynamicToolAuthorizationDoesNotLogCanonicalArguments(t *testing.T) {
	const secret = "private-model-query-sentinel"
	raw := json.RawMessage(`{"query":"` + secret + `"}`)
	for _, value := range []any{
		application.AuthorizeWorkspaceAnalysisToolCallCommand{CanonicalRequest: raw},
		agentapplication.PrepareWorkspaceAnalysisToolOperationCommand{Arguments: raw},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		slog.New(slog.NewJSONHandler(&logs, nil)).Info("authorization", "command", value)
		for _, output := range []string{string(encoded), fmt.Sprintf("%v %+v %#v", value, value, value), logs.String()} {
			if strings.Contains(output, secret) {
				t.Fatal("canonical arguments leaked into a safe representation")
			}
		}
	}
}

func TestDynamicToolEvidenceNamespaceMatchesAgentBudget(t *testing.T) {
	if domain.DynamicWorkspaceAnalysisMaxEvidenceRefs != agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs {
		t.Fatal("tool reference namespace differs from the durable Agent policy")
	}
	for i := 0; i <= agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs+1; i++ {
		ref := fmt.Sprintf("E%d", i)
		if domain.ValidDynamicEvidenceRef(ref) != agentdomain.ValidWorkspaceAnalysisV2EvidenceRef(ref) {
			t.Fatalf("Agent and Tools disagree on evidence reference %q", ref)
		}
	}
}
