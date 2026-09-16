package agent

import (
	"testing"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestGoalSelectionCatalogUsesStrictPointSelectionSchema(t *testing.T) {
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterGoalSelectionRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "goal-test", Version: "v1"}, Model: synthesisFixtureModelRef, Timeout: time.Second, MaxOutputTokens: 512}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	ref := agentdomain.SchemaRef{ID: GoalSelectionSchemaID, Version: "v1"}
	snapshot, err := catalog.Snapshot(agentdomain.PromptRef{ID: ref.ID, Version: ref.Version}, ref, ref, profile.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Schema.Decode([]byte(`{"selections":[],"explanation":"No relevant knowledge points in this batch."}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Schema.Decode([]byte(`{"selections":[{"point":"P033","reason":"unknown"}],"explanation":"invalid"}`)); err == nil {
		t.Fatal("out-of-contract point accepted")
	}
}
