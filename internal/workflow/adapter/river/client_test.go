package riveradapter

import "testing"

func TestClientAndMigratorAlwaysUseWorkflowSchema(t *testing.T) {
	if _, err := NewClient(nil, nil); err == nil {
		t.Fatal("nil pool accepted by client")
	}
	if _, err := NewMigrator(nil); err == nil {
		t.Fatal("nil pool accepted by migrator")
	}
}
