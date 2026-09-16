package domain

import "testing"

func TestAnchorScopeRequiresPurposeAndDistinctTopics(t *testing.T) {
	scope := AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"面试复习"}, Description: "Redis 面试复习"}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	scope.Topics = append(scope.Topics, "Redis")
	if scope.Validate() == nil {
		t.Fatal("duplicate topic accepted")
	}
	scope.Topics = []string{"Redis"}
	scope.Audiences = nil
	if scope.Validate() == nil {
		t.Fatal("scope without purpose accepted")
	}
}
