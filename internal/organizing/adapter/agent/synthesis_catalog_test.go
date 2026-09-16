package agent

import (
	"strings"
	"testing"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func TestSynthesisRuntimeCatalogKeepsV1AndRegistersAnchoredV2Prompts(t *testing.T) {
	catalog := agentapp.NewRuntimeCatalog()
	if err := RegisterSynthesisRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{
		Ref:             agentdomain.ModelProfileRef{ID: "catalog-test", Version: "v1"},
		Model:           synthesisFixtureModelRef,
		Timeout:         time.Second,
		MaxOutputTokens: 1,
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	legacy, err := catalog.Snapshot(synthesisPromptForVersion(organizingapp.SynthesisModelGenerate, organizingapp.SynthesisLegacyPromptVersion), synthesisSchema(organizingapp.SynthesisModelGenerate), synthesisSchema(organizingapp.SynthesisModelGenerate), profile.Ref)
	if err != nil {
		t.Fatal(err)
	}
	anchored, err := catalog.Snapshot(synthesisPromptForVersion(organizingapp.SynthesisModelGenerate, organizingapp.SynthesisAnchoredPromptVersion), synthesisSchema(organizingapp.SynthesisModelGenerate), synthesisSchema(organizingapp.SynthesisModelGenerate), profile.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacy.Prompt.InitialInstruction, "anchor object") || !strings.Contains(anchored.Prompt.InitialInstruction, "anchor object") || legacy.Prompt.Ref.Version != organizingapp.SynthesisLegacyPromptVersion || anchored.Prompt.Ref.Version != organizingapp.SynthesisAnchoredPromptVersion {
		t.Fatalf("legacy=%+v anchored=%+v", legacy.Prompt.Ref, anchored.Prompt.Ref)
	}
	body, err := catalog.Snapshot(synthesisPromptForVersion(organizingapp.SynthesisModelGenerate, organizingapp.SynthesisBodyPromptVersion), synthesisBodySchema(organizingapp.SynthesisModelGenerate), synthesisBodySchema(organizingapp.SynthesisModelGenerate), profile.Ref)
	if err != nil {
		t.Fatal(err)
	}
	review, err := catalog.Snapshot(synthesisPromptForVersion(organizingapp.SynthesisModelValidate, organizingapp.SynthesisBodyPromptVersion), synthesisSchema(organizingapp.SynthesisModelValidate), synthesisSchema(organizingapp.SynthesisModelValidate), profile.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Prompt.InitialInstruction, "INCLUDE_ITEM") || body.Prompt.Ref.Version != organizingapp.SynthesisBodyPromptVersion ||
		body.Schema.Ref.Version != organizingapp.SynthesisBodySchemaVersion || review.Schema.Ref.Version != organizingapp.SynthesisRuntimeVersion ||
		!strings.Contains(review.Prompt.InitialInstruction, "Included published items") {
		t.Fatalf("body=%+v review=%+v", body, review)
	}
}
