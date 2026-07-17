package riveradapter

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestNodeJobArgsHasStableContractAndUniqueDimensions(t *testing.T) {
	id := foundation.ID("10000000-0000-4000-8000-000000000001")
	args, err := NewNodeJobArgs(id, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := args.Kind(); got != NodeJobKind {
		t.Fatalf("kind = %q, want %q", got, NodeJobKind)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema_version":1,"node_run_id":"10000000-0000-4000-8000-000000000001","dispatch_no":7}`
	if string(encoded) != want {
		t.Fatalf("encoded args = %s, want %s", encoded, want)
	}
	if err := ValidateNodeJobArgs(args); err != nil {
		t.Fatal(err)
	}
	typ := reflect.TypeOf(args)
	if typ.NumField() != 3 {
		t.Fatalf("NodeJobArgs has %d fields, want exactly 3", typ.NumField())
	}
	if typ.Field(0).Tag.Get("river") != "" || typ.Field(1).Tag.Get("river") != "unique" || typ.Field(2).Tag.Get("river") != "unique" {
		t.Fatalf("unique field tags changed: %#v %#v %#v", typ.Field(0).Tag, typ.Field(1).Tag, typ.Field(2).Tag)
	}
}

func TestNodeJobArgsRejectsSecretsPathsAndInvalidIdentity(t *testing.T) {
	tests := []NodeJobArgs{
		{SchemaVersion: 2, NodeRunID: foundation.ID("10000000-0000-4000-8000-000000000001"), DispatchNo: 1},
		{SchemaVersion: 1, NodeRunID: foundation.ID("not-a-uuid"), DispatchNo: 1},
		{SchemaVersion: 1, NodeRunID: foundation.ID("10000000-0000-4000-8000-000000000001"), DispatchNo: 0},
	}
	for _, args := range tests {
		if err := ValidateNodeJobArgs(args); err == nil {
			t.Fatalf("ValidateNodeJobArgs(%+v) unexpectedly succeeded", args)
		}
	}
}
