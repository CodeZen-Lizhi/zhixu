package application

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCollectionCursorRoundTripAndStale(t *testing.T) {
	codec, err := NewCursorCodec(bytes.Repeat([]byte{0x31}, collectionCursorKeySize))
	if err != nil {
		t.Fatal(err)
	}
	cursor := ResultCursor{Scope: CursorScopeSaved, WorkspaceID: foundation.ID("11111111-1111-4111-8111-111111111111"), CollectionID: foundation.ID("22222222-2222-4222-8222-222222222222"), CollectionVersion: 2, QueryHash: strings.Repeat("a", 64), SortHash: strings.Repeat("b", 64), Limit: 25, RevisionHash: strings.Repeat("c", 64), LastObjectType: "CLAIM", LastID: foundation.ID("33333333-3333-4333-8333-333333333333")}
	raw, err := codec.Encode(cursor)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(raw, CursorBinding{Scope: cursor.Scope, WorkspaceID: cursor.WorkspaceID, CollectionID: cursor.CollectionID, CollectionVersion: cursor.CollectionVersion, QueryHash: cursor.QueryHash, SortHash: cursor.SortHash, Limit: cursor.Limit, RevisionHash: cursor.RevisionHash})
	if err != nil || !reflect.DeepEqual(decoded, cursor) {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if _, err := codec.Decode(raw, CursorBinding{Scope: cursor.Scope, WorkspaceID: cursor.WorkspaceID, CollectionID: cursor.CollectionID, CollectionVersion: cursor.CollectionVersion, QueryHash: cursor.QueryHash, SortHash: cursor.SortHash, Limit: cursor.Limit, RevisionHash: strings.Repeat("d", 64)}); err == nil || !strings.Contains(err.Error(), "COLLECTION_CURSOR_STALE") {
		t.Fatalf("stale cursor err=%v", err)
	}
}

func TestCollectionCursorTamperAndRestartInvalid(t *testing.T) {
	key := bytes.Repeat([]byte{0x41}, collectionCursorKeySize)
	codec, _ := NewCursorCodec(key)
	cursor := ResultCursor{Scope: CursorScopeSaved, WorkspaceID: foundation.ID("11111111-1111-4111-8111-111111111111"), CollectionID: foundation.ID("22222222-2222-4222-8222-222222222222"), CollectionVersion: 1, QueryHash: strings.Repeat("a", 64), SortHash: strings.Repeat("b", 64), Limit: 10, RevisionHash: strings.Repeat("c", 64), LastObjectType: "TOPIC", LastID: foundation.ID("33333333-3333-4333-8333-333333333333")}
	raw, _ := codec.Encode(cursor)
	if _, err := codec.Decode(raw+"x", CursorBinding{}); err == nil {
		t.Fatal("tampered cursor accepted")
	}
	other, _ := NewCursorCodec(bytes.Repeat([]byte{0x42}, collectionCursorKeySize))
	if _, err := other.Decode(raw, CursorBinding{}); err == nil {
		t.Fatal("cursor accepted after process-key change")
	}
}

func TestCollectionPreviewCursorBindsCanonicalQueryWithoutCollection(t *testing.T) {
	codec, err := NewCursorCodec(bytes.Repeat([]byte{0x51}, collectionCursorKeySize))
	if err != nil {
		t.Fatal(err)
	}
	cursor := ResultCursor{Scope: CursorScopePreview, WorkspaceID: foundation.ID("11111111-1111-4111-8111-111111111111"), QueryHash: strings.Repeat("a", 64), SortHash: strings.Repeat("b", 64), Limit: 25, RevisionHash: strings.Repeat("c", 64), LastObjectType: "CLAIM", LastID: foundation.ID("33333333-3333-4333-8333-333333333333")}
	raw, err := codec.Encode(cursor)
	if err != nil {
		t.Fatal(err)
	}
	binding := CursorBinding{Scope: CursorScopePreview, WorkspaceID: cursor.WorkspaceID, QueryHash: cursor.QueryHash, SortHash: cursor.SortHash, Limit: cursor.Limit, RevisionHash: cursor.RevisionHash}
	decoded, err := codec.Decode(raw, binding)
	if err != nil || !reflect.DeepEqual(decoded, cursor) {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	binding.QueryHash = strings.Repeat("d", 64)
	if _, err := codec.Decode(raw, binding); err == nil {
		t.Fatal("preview cursor accepted for another canonical query")
	}
}
