package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalizeConversationCreateRequestOwnsTitleAndHash(t *testing.T) {
	title := "  Runtime recovery  "
	request := ConversationCreateRequest{WorkspaceID: testWorkspaceID, Title: &title}

	canonical, err := CanonicalizeConversationCreateRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Title == nil || *canonical.Title != "Runtime recovery" {
		t.Fatalf("canonical=%#v", canonical)
	}
	if title != "  Runtime recovery  " || canonical.Title == request.Title {
		t.Fatal("canonicalization mutated or retained caller-owned title")
	}

	hash, err := ComputeConversationCreateRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "85c598c7db48a2ae253c1e3b0e09f9937d5bae44a637caa274eb1c5ecaa6b65f" {
		t.Fatalf("conversation create request hash=%s", hash)
	}
	equivalentTitle := "Runtime recovery"
	equivalentHash, err := ComputeConversationCreateRequestHash(ConversationCreateRequest{
		WorkspaceID: testWorkspaceID,
		Title:       &equivalentTitle,
	})
	if err != nil || equivalentHash != hash {
		t.Fatalf("equivalent hash=%s err=%v", equivalentHash, err)
	}
	differentTitle := "Incident recovery"
	differentHash, err := ComputeConversationCreateRequestHash(ConversationCreateRequest{
		WorkspaceID: testWorkspaceID,
		Title:       &differentTitle,
	})
	if err != nil || differentHash == hash {
		t.Fatalf("different hash=%s err=%v", differentHash, err)
	}

	untitled, err := CanonicalizeConversationCreateRequest(ConversationCreateRequest{WorkspaceID: testWorkspaceID})
	if err != nil || untitled.Title != nil {
		t.Fatalf("untitled=%#v err=%v", untitled, err)
	}
}

func TestCanonicalizeConversationCreateRequestRejectsUnsafeBoundaries(t *testing.T) {
	validTitle := "valid"
	base := ConversationCreateRequest{WorkspaceID: testWorkspaceID, Title: &validTitle}
	invalidUTF8 := string([]byte{'t', 0xff})
	tests := []struct {
		name   string
		mutate func(*ConversationCreateRequest)
	}{
		{name: "invalid workspace", mutate: func(value *ConversationCreateRequest) { value.WorkspaceID = "bad" }},
		{name: "empty title", mutate: func(value *ConversationCreateRequest) { title := "  "; value.Title = &title }},
		{name: "invalid utf8", mutate: func(value *ConversationCreateRequest) { value.Title = &invalidUTF8 }},
		{name: "nul title", mutate: func(value *ConversationCreateRequest) { title := "unsafe\x00title"; value.Title = &title }},
		{name: "oversized title", mutate: func(value *ConversationCreateRequest) {
			title := strings.Repeat("x", maxConversationTitleBytes+1)
			value.Title = &title
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			_, err := CanonicalizeConversationCreateRequest(value)
			var typed *foundation.Error
			if !errors.As(err, &typed) || typed.Code != ErrorCodeConversationInvalid || typed.Kind != foundation.ErrorInvalidInput {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
