package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
)

func TestM8InterviewMemoryCapabilityMatrix(t *testing.T) {
	const (
		workspaceID = "92000000-0000-4000-8000-000000000001"
		memoryID    = "92000000-0000-4000-8000-000000000002"
		sessionID   = "92000000-0000-4000-8000-000000000003"
		pathID      = "92000000-0000-4000-8000-000000000004"
		stepID      = "92000000-0000-4000-8000-000000000005"
	)
	tests := []struct {
		name   string
		method string
		path   string
		want   []capability.Capability
	}{
		{name: "list memories", method: http.MethodGet, path: "/api/v1/memories?workspace_id=" + workspaceID, want: []capability.Capability{capability.ReadLocal}},
		{name: "get memory", method: http.MethodGet, path: "/api/v1/memories/" + memoryID + "?workspace_id=" + workspaceID, want: []capability.Capability{capability.ReadLocal}},
		{name: "get interview", method: http.MethodGet, path: "/api/v1/review/interviews/" + sessionID + "?workspace_id=" + workspaceID, want: []capability.Capability{capability.ReadLocal}},
		{name: "create memory", method: http.MethodPost, path: "/api/v1/memories", want: []capability.Capability{capability.WriteProposal}},
		{name: "confirm memory", method: http.MethodPost, path: "/api/v1/memories/" + memoryID + "/confirm", want: []capability.Capability{capability.WriteProposal}},
		{name: "edit memory", method: http.MethodPut, path: "/api/v1/memories/" + memoryID, want: []capability.Capability{capability.WriteProposal}},
		{name: "pause memory", method: http.MethodPost, path: "/api/v1/memories/" + memoryID + "/pause", want: []capability.Capability{capability.WriteProposal}},
		{name: "resume memory", method: http.MethodPost, path: "/api/v1/memories/" + memoryID + "/resume", want: []capability.Capability{capability.WriteProposal}},
		{name: "delete memory", method: http.MethodDelete, path: "/api/v1/memories/" + memoryID, want: []capability.Capability{capability.WriteProposal}},
		{name: "start interview", method: http.MethodPost, path: "/api/v1/review/interviews", want: []capability.Capability{capability.WriteProposal}},
		{name: "submit interview turn", method: http.MethodPost, path: "/api/v1/review/interviews/" + sessionID + "/turns", want: []capability.Capability{capability.WriteProposal}},
		{name: "complete interview", method: http.MethodPost, path: "/api/v1/review/interviews/" + sessionID + "/complete", want: []capability.Capability{capability.WriteProposal}},
		{name: "create interview memory candidate", method: http.MethodPost, path: "/api/v1/review/interviews/" + sessionID + "/learning-paths/" + pathID + "/steps/" + stepID + "/memory-candidate", want: []capability.Capability{capability.WriteProposal}},
		{name: "update learning path status", method: http.MethodPut, path: "/api/v1/review/learning-paths/" + pathID + "/status", want: []capability.Capability{capability.WriteProposal}},
		{name: "update learning path step", method: http.MethodPut, path: "/api/v1/review/learning-paths/" + pathID + "/steps/" + stepID, want: []capability.Capability{capability.WriteProposal}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := RequiredCapabilities(request); !slices.Equal(got, test.want) {
				t.Fatalf("capabilities=%q want=%q", got, test.want)
			}
		})
	}
}
