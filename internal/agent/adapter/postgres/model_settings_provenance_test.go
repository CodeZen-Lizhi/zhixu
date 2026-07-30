package postgres

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestModelRunPersistenceAndReplayIncludeModelSettingsRevision(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	run := domain.ModelRun{ModelSettingsRevision: &zero}
	arguments := modelRunArgs(run)
	if len(arguments) != 33 || arguments[5] != int64(0) {
		t.Fatalf("model run arguments=%#v", arguments)
	}
	replay := run
	positive := int64(3)
	replay.ModelSettingsRevision = &positive
	if sameModelRunBindingWithoutRetrieval(run, replay) {
		t.Fatal("model run replay accepted a different settings revision")
	}
	if nullableInt64(nil) != nil {
		t.Fatal("static model run revision did not remain SQL NULL")
	}
}
