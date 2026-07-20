package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

func TestCursorCodecRoundTripBindsCompleteResultWindow(t *testing.T) {
	codec := mustGraphCursorCodec(t, 'a')
	cursor := graphCursor(t)
	raw, err := codec.Encode(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxEncodedCursorBytes || strings.Contains(raw, string(cursor.WorkspaceID)) {
		t.Fatalf("cursor size or opacity is invalid: %q", raw)
	}
	decoded, err := codec.Decode(raw, bindingOf(cursor))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != cursor {
		t.Fatalf("decoded cursor = %#v, want %#v", decoded, cursor)
	}
}

func TestCursorCodecRejectsTamperRestartAndCrossQueryBindings(t *testing.T) {
	codec := mustGraphCursorCodec(t, 'a')
	cursor := graphCursor(t)
	raw, err := codec.Encode(cursor)
	if err != nil {
		t.Fatal(err)
	}

	assertGraphCursorError(t, codec, raw[:len(raw)-1]+"A", bindingOf(cursor), foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)
	assertGraphCursorError(t, mustGraphCursorCodec(t, 'b'), raw, bindingOf(cursor), foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)

	bindings := []CursorBinding{bindingOf(cursor), bindingOf(cursor), bindingOf(cursor), bindingOf(cursor)}
	bindings[0].QueryKind = "relation_evidence"
	bindings[1].WorkspaceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	bindings[2].CanonicalRequestHash = strings.Repeat("c", 64)
	bindings[3].Limit++
	for _, binding := range bindings {
		assertGraphCursorError(t, codec, raw, binding, foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)
	}
}

func TestCursorCodecClassifiesResultDriftAsStale(t *testing.T) {
	codec := mustGraphCursorCodec(t, 'a')
	cursor := graphCursor(t)
	raw, err := codec.Encode(cursor)
	if err != nil {
		t.Fatal(err)
	}
	binding := bindingOf(cursor)
	binding.ResultFingerprint = strings.Repeat("d", 64)
	assertGraphCursorError(t, codec, raw, binding, foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale)
}

func TestCursorCodecRejectsInvalidFieldsAndOversizedInput(t *testing.T) {
	codec := mustGraphCursorCodec(t, 'a')
	valid := graphCursor(t)
	invalid := []ResultWindowCursor{valid, valid, valid, valid, valid, valid, valid}
	invalid[0].QueryKind = "Global"
	invalid[1].WorkspaceID = "bad"
	invalid[2].CanonicalRequestHash = strings.Repeat("A", 64)
	invalid[3].ResultFingerprint = "short"
	invalid[4].Limit = 0
	invalid[5].Offset = 0
	invalid[6].Limit = graphdomain.MaxLimit + 1
	for _, cursor := range invalid {
		_, err := codec.Encode(cursor)
		assertClassifiedCursorError(t, err, foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)
	}
	assertGraphCursorError(t, codec, strings.Repeat("x", maxEncodedCursorBytes+1), bindingOf(valid), foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)
}

func TestCursorCodecRequiresExactKeyAndFailsClosedWithoutEntropy(t *testing.T) {
	zero := &CursorCodec{}
	_, err := zero.Encode(graphCursor(t))
	assertClassifiedCursorError(t, err, foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)
	_, err = zero.Decode("opaque", bindingOf(graphCursor(t)))
	assertClassifiedCursorError(t, err, foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)

	for _, size := range []int{0, cursorSigningKeyBytes - 1, cursorSigningKeyBytes + 1} {
		codec, err := NewCursorCodec(make([]byte, size))
		if codec != nil {
			t.Fatalf("key size %d unexpectedly created codec", size)
		}
		assertClassifiedCursorError(t, err, foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable)
	}
	codec, err := newRandomCursorCodec(graphFailingRandom{})
	if codec != nil {
		t.Fatal("entropy failure unexpectedly created codec")
	}
	assertClassifiedCursorError(t, err, foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable)
}

func TestHashCanonicalCursorValueIsDeterministic(t *testing.T) {
	first, err := hashCanonicalCursorValue(struct {
		Workspace string   `json:"workspace"`
		Filters   []string `json:"filters"`
	}{Workspace: "one", Filters: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashCanonicalCursorValue(struct {
		Workspace string   `json:"workspace"`
		Filters   []string `json:"filters"`
	}{Workspace: "one", Filters: []string{"a", "b"}})
	if err != nil || first != second || !canonicalCursorHash(first) {
		t.Fatalf("hashes = %q/%q, err=%v", first, second, err)
	}
}

func graphCursor(t *testing.T) ResultWindowCursor {
	t.Helper()
	requestHash, err := hashCanonicalCursorValue(struct {
		Status string `json:"status"`
	}{Status: "CONFIRMED"})
	if err != nil {
		t.Fatal(err)
	}
	resultHash, err := hashCanonicalCursorValue([]string{"topic-a", "topic-b"})
	if err != nil {
		t.Fatal(err)
	}
	return ResultWindowCursor{
		QueryKind: "global", WorkspaceID: "11111111-1111-4111-8111-111111111111",
		CanonicalRequestHash: requestHash, ResultFingerprint: resultHash, Limit: 25, Offset: 25,
	}
}

func bindingOf(cursor ResultWindowCursor) CursorBinding {
	return CursorBinding{
		QueryKind: cursor.QueryKind, WorkspaceID: cursor.WorkspaceID,
		CanonicalRequestHash: cursor.CanonicalRequestHash, ResultFingerprint: cursor.ResultFingerprint,
		Limit: cursor.Limit,
	}
}

func mustGraphCursorCodec(t *testing.T, fill byte) *CursorCodec {
	t.Helper()
	codec, err := NewCursorCodec(bytesOfGraph(fill, cursorSigningKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func assertGraphCursorError(t *testing.T, codec *CursorCodec, raw string, binding CursorBinding, kind foundation.ErrorKind, code string) {
	t.Helper()
	_, err := codec.Decode(raw, binding)
	assertClassifiedCursorError(t, err, kind, code)
}

func assertClassifiedCursorError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("expected %s/%s, got %v", kind, code, err)
	}
}

func bytesOfGraph(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

type graphFailingRandom struct{}

func (graphFailingRandom) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
