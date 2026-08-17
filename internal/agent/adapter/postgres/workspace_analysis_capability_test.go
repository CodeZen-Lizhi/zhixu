package postgres

import (
	"strings"
	"testing"
	"time"
)

func TestWorkspaceAnalysisCapabilitySQLUsesDatabaseClockAndExactContract(t *testing.T) {
	for name, statement := range map[string]string{
		"advertise": workspaceAnalysisCapabilityAdvertiseSQL,
		"heartbeat": workspaceAnalysisCapabilityHeartbeatSQL,
		"release":   workspaceAnalysisCapabilityReleaseSQL,
		"ready":     workspaceAnalysisCapabilityReadySQL,
	} {
		if !strings.Contains(statement, "clock_timestamp()") {
			t.Fatalf("%s statement does not use database time: %s", name, statement)
		}
	}
	for _, required := range []string{
		"definition_hash", "tool_catalog_hash", "policy_version", "config_revision", "released_at IS NULL",
	} {
		if !strings.Contains(workspaceAnalysisCapabilityReadySQL, required) {
			t.Fatalf("readiness SQL is missing %q", required)
		}
	}
	if got := workspaceAnalysisCapabilityInterval(30 * time.Second); got != "30000000 microseconds" {
		t.Fatalf("interval=%q", got)
	}
}
