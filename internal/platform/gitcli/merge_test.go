package gitcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestMergeCleanGolden(t *testing.T) {
	result, err := New("").Merge(context.Background(), MergeInput{
		Base:     []byte("title\nfirst\nmiddle\nsecond\n"),
		Current:  []byte("title\nfirst current\nmiddle\nsecond\n"),
		Proposed: []byte("title\nfirst\nmiddle\nsecond proposed\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Contract != MergeContractVersion || result.Algorithm != "myers" {
		t.Fatalf("contract = %#v", result)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v", result.Conflicts)
	}
	if got, want := string(result.Candidate), "title\nfirst current\nmiddle\nsecond proposed\n"; got != want {
		t.Fatalf("candidate = %q, want %q", got, want)
	}
}

func TestMergeConflictGoldenIsStructuredAndMarkerFree(t *testing.T) {
	result, err := New("").Merge(context.Background(), MergeInput{
		Base:     []byte("title\nvalue\nend\n"),
		Current:  []byte("title\ncurrent\nend\n"),
		Proposed: []byte("title\nproposed\nend\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("conflicts = %#v", result.Conflicts)
	}
	conflict := result.Conflicts[0]
	if conflict.Ordinal != 1 || string(conflict.Current) != "current\n" || string(conflict.Base) != "value\n" || string(conflict.Proposed) != "proposed\n" {
		t.Fatalf("conflict = %#v", conflict)
	}
	if got, want := string(result.Candidate), "title\ncurrent\nend\n"; got != want {
		t.Fatalf("candidate = %q, want %q", got, want)
	}
	for _, marker := range []string{"<<<<<<<", "|||||||", "=======", ">>>>>>>"} {
		if bytes.Contains(result.Candidate, []byte(marker)) {
			t.Fatalf("candidate contains marker %q: %q", marker, result.Candidate)
		}
	}
}

func TestMergePreservesCRLFAndNoFinalNewline(t *testing.T) {
	result, err := New("").Merge(context.Background(), MergeInput{
		Base:     []byte("one\r\nmiddle\r\ntwo"),
		Current:  []byte("one current\r\nmiddle\r\ntwo"),
		Proposed: []byte("one\r\nmiddle\r\ntwo proposed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.Candidate), "one current\r\nmiddle\r\ntwo proposed"; got != want {
		t.Fatalf("candidate = %q, want %q", got, want)
	}
}

func TestMergeRejectsUnsafeInput(t *testing.T) {
	tests := []struct {
		name  string
		input MergeInput
		code  string
	}{
		{name: "invalid utf8", input: MergeInput{Base: []byte{0xff}}, code: mergeContentInvalidCode},
		{name: "nul", input: MergeInput{Base: []byte("a\x00b")}, code: mergeContentInvalidCode},
		{name: "reserved marker", input: MergeInput{Base: []byte(strings.Repeat("<", mergeMarkerSize) + " CURRENT\n")}, code: mergeContentInvalidCode},
		{name: "too large", input: MergeInput{Base: bytes.Repeat([]byte("x"), mergeInputLimit+1)}, code: mergeInputTooLargeCode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New("").Merge(context.Background(), test.input)
			assertMergeErrorCode(t, err, test.code)
		})
	}
}

func TestMergeUsesFixedCommandAndCleansPrivateTemporaryFiles(t *testing.T) {
	command := writeExecutable(t, `#!/bin/sh
test "$#" = 15 || exit 11
test "$1" = merge-file || exit 12
test "$2" = -p || exit 13
test "$3" = -q || exit 14
test "$4" = --diff3 || exit 15
test "$5" = --diff-algorithm=myers || exit 16
test "$6" = --marker-size=32 || exit 17
test "$7" = -L && test "$8" = CURRENT || exit 18
test "$9" = -L && test "${10}" = BASE || exit 19
test "${11}" = -L && test "${12}" = PROPOSED || exit 20
test "${13}" = current && test "${14}" = base && test "${15}" = proposed || exit 21
test "$(stat -f %Lp current 2>/dev/null || stat -c %a current)" = 600 || exit 22
test "$(stat -f %Lp base 2>/dev/null || stat -c %a base)" = 600 || exit 23
test "$(stat -f %Lp proposed 2>/dev/null || stat -c %a proposed)" = 600 || exit 24
test "$GIT_CONFIG_NOSYSTEM" = 1 || exit 25
test "$GIT_CONFIG_GLOBAL" = /dev/null || exit 26
pwd > "$MERGE_TEST_CWD"
cat current
`)
	path := filepath.Join(t.TempDir(), "cwd")
	t.Setenv("MERGE_TEST_CWD", path)

	result, err := New(command).Merge(context.Background(), MergeInput{Current: []byte("current\n"), Base: []byte("base\n"), Proposed: []byte("proposed\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Candidate); got != "current\n" {
		t.Fatalf("candidate = %q", got)
	}
	dir, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary directory still exists or failed unexpectedly: %v", err)
	}
}

func TestMergeMapsTimeoutAndBusy(t *testing.T) {
	command := writeExecutable(t, "#!/bin/sh\nsleep 2\n")
	client := New(command)
	input := MergeInput{Current: []byte("current\n"), Base: []byte("base\n"), Proposed: []byte("proposed\n")}
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()
	done := make(chan struct{}, 2)
	go func() { _, _ = client.Merge(ctx1, input); done <- struct{}{} }()
	go func() { _, _ = client.Merge(ctx2, input); done <- struct{}{} }()
	time.Sleep(50 * time.Millisecond)
	busyCtx, cancelBusy := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelBusy()
	_, err := client.Merge(busyCtx, input)
	assertMergeErrorCode(t, err, mergeBusyCode)
	cancel1()
	cancel2()
	<-done
	<-done

	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelTimeout()
	_, err = client.Merge(timeoutCtx, input)
	assertMergeErrorCode(t, err, mergeTimeoutCode)
}

func TestParseMergeOutputRejectsMismatchedExitCode(t *testing.T) {
	output := []byte(strings.Repeat("<", mergeMarkerSize) + " CURRENT\ncurrent\n" + strings.Repeat("|", mergeMarkerSize) + " BASE\nbase\n" + strings.Repeat("=", mergeMarkerSize) + "\nproposed\n" + strings.Repeat(">", mergeMarkerSize) + " PROPOSED\n")
	_, err := parseMergeOutput(output, 2)
	assertMergeErrorCode(t, err, mergeEngineOutputInvalidCode)
}

func assertMergeErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != want {
		t.Fatalf("error = %#v, want code %s", err, want)
	}
}
