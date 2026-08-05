package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCursorCodecRoundTripAndTamperRejection(t *testing.T) {
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	payload := cursorPayload{
		Version: 1, WorkspaceID: "20000000-0000-4000-8000-000000000001",
		DocumentID: "20000000-0000-4000-8000-000000000002", Path: "notes/java-ai.md",
		Branch: "main", Head: strings.Repeat("a", 40), Limit: 30, Offset: 30,
		LastCommit: strings.Repeat("b", 40),
	}
	encoded, err := codec.encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.decode(encoded)
	if err != nil || decoded != payload {
		t.Fatalf("decode()=(%+v,%v)", decoded, err)
	}
	parts := strings.Split(encoded, ".")
	parts[0] = "e30"
	_, err = codec.decode(strings.Join(parts, "."))
	assertHistoryErrorCode(t, err, ErrorCodeCursorInvalid)
	_, err = codec.decode(encoded + ".extra")
	assertHistoryErrorCode(t, err, ErrorCodeCursorInvalid)
	_, err = codec.decode(strings.Repeat("x", 4097))
	assertHistoryErrorCode(t, err, ErrorCodeCursorInvalid)
}

func TestCursorCodecRejectsInvalidPayloadAndWeakKey(t *testing.T) {
	if codec, err := NewCursorCodec([]byte("short")); err == nil || codec != nil {
		t.Fatalf("NewCursorCodec(short)=(%v,%v)", codec, err)
	}
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	invalid := cursorPayload{
		Version: 1, WorkspaceID: "20000000-0000-4000-8000-000000000001",
		DocumentID: "20000000-0000-4000-8000-000000000002", Path: ".git/config.md",
		Branch: "main", Head: strings.Repeat("a", 40), Limit: 30, Offset: 30,
		LastCommit: strings.Repeat("b", 40),
	}
	_, err = codec.encode(invalid)
	assertHistoryErrorCode(t, err, ErrorCodeInvalid)
}

func assertHistoryErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v, want code %s", err, code)
	}
}
