package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateEmbeddingVersionAcceptsCompleteImmutableBinding(t *testing.T) {
	t.Parallel()

	version := validEmbeddingVersion()
	if err := ValidateEmbeddingVersion(version); err != nil {
		t.Fatalf("ValidateEmbeddingVersion() error = %v", err)
	}
}

func TestValidateEmbeddingVersionRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*EmbeddingVersion)
	}{
		{name: "missing id", mutate: func(v *EmbeddingVersion) { v.ID = "" }},
		{name: "missing provider", mutate: func(v *EmbeddingVersion) { v.Provider = "" }},
		{name: "missing adapter name", mutate: func(v *EmbeddingVersion) { v.AdapterName = "" }},
		{name: "missing adapter version", mutate: func(v *EmbeddingVersion) { v.AdapterVersion = "" }},
		{name: "missing model", mutate: func(v *EmbeddingVersion) { v.Model = "" }},
		{name: "zero dimensions", mutate: func(v *EmbeddingVersion) { v.Dimensions = 0 }},
		{name: "negative dimensions", mutate: func(v *EmbeddingVersion) { v.Dimensions = -1 }},
		{name: "unknown normalization", mutate: func(v *EmbeddingVersion) { v.Normalization = "unit" }},
		{name: "unknown distance", mutate: func(v *EmbeddingVersion) { v.DistanceMetric = "manhattan" }},
		{name: "non canonical hash", mutate: func(v *EmbeddingVersion) { v.ConfigHash = strings.Repeat("A", 64) }},
		{name: "missing created at", mutate: func(v *EmbeddingVersion) { v.CreatedAt = time.Time{} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := validEmbeddingVersion()
			test.mutate(&version)
			err := ValidateEmbeddingVersion(version)
			if err == nil {
				t.Fatalf("invalid version accepted: %#v", version)
			}
			assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeEmbeddingVersionInvalid)
		})
	}
}

func TestSameEmbeddingBindingCoversEveryVectorAffectingField(t *testing.T) {
	t.Parallel()

	base := validEmbeddingVersion()
	replay := base
	replay.ID = "10000000-0000-4000-8000-000000000099"
	replay.CreatedAt = replay.CreatedAt.Add(time.Hour)
	if !SameEmbeddingBinding(base, replay) {
		t.Fatal("record identity fields changed immutable binding equality")
	}

	mutations := []func(*EmbeddingVersion){
		func(v *EmbeddingVersion) { v.Provider = "local" },
		func(v *EmbeddingVersion) { v.AdapterName = "other-adapter" },
		func(v *EmbeddingVersion) { v.AdapterVersion = "v2" },
		func(v *EmbeddingVersion) { v.Model = "other-model" },
		func(v *EmbeddingVersion) { v.Dimensions++ },
		func(v *EmbeddingVersion) { v.Normalization = NormalizationNone },
		func(v *EmbeddingVersion) { v.DistanceMetric = DistanceEuclidean },
		func(v *EmbeddingVersion) { v.ConfigHash = strings.Repeat("b", 64) },
	}
	for index, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if SameEmbeddingBinding(base, candidate) {
			t.Errorf("mutation %d was not detected", index)
		}
	}
}

func validEmbeddingVersion() EmbeddingVersion {
	return EmbeddingVersion{
		ID:             "10000000-0000-4000-8000-000000000001",
		Provider:       "openai-compatible",
		AdapterName:    "eino-openai",
		AdapterVersion: "v1",
		Model:          "text-embedding-model",
		Dimensions:     3,
		Normalization:  NormalizationL2,
		DistanceMetric: DistanceCosine,
		ConfigHash:     strings.Repeat("a", 64),
		CreatedAt:      time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
	}
}
