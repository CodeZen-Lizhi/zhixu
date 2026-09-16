package agent

import (
	"testing"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestAnchorCatalogFreezesStrictProviderOutput(t *testing.T) {
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterAnchorRecommendationRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "anchor-test", Version: "v1"}, Model: synthesisFixtureModelRef, Timeout: time.Second, MaxOutputTokens: 512}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot(agentdomain.PromptRef{ID: agentdomain.AnchorRecommendationSchemaID, Version: "v1"}, agentdomain.SchemaRef{ID: agentdomain.AnchorRecommendationSchemaID, Version: "v1"}, agentdomain.SchemaRef{ID: agentdomain.AnchorRecommendationSchemaID, Version: "v1"}, profile.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = snapshot.Schema.Decode([]byte(`{"recommendation":null,"no_recommendation":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = snapshot.Schema.Decode([]byte(`{"recommendation":null,"no_recommendation":false}`)); err == nil {
		t.Fatal("contradictory no-result accepted")
	}
}
