package gitcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MergeContractVersion is the versioned, byte-deterministic merge contract.
	MergeContractVersion = "git-merge-file/diff3/myers/marker32/v1"
	mergeInputLimit      = 1 << 20
	mergeStdoutLimit     = 4 << 20
	mergeStderrLimit     = 64 << 10
	mergeTimeout         = 5 * time.Second
	mergeConcurrency     = 2
	mergeConflictLimit   = 1024
	mergeMarkerSize      = 32
)

const (
	mergeInputTooLargeCode       = "PROPOSAL_REVISION_INPUT_TOO_LARGE"
	mergeContentInvalidCode      = "PROPOSAL_MERGE_CONTENT_INVALID"
	mergeResultTooLargeCode      = "PROPOSAL_MERGE_RESULT_TOO_LARGE"
	mergeBusyCode                = "PROPOSAL_MERGE_BUSY"
	mergeTimeoutCode             = "PROPOSAL_MERGE_TIMEOUT"
	mergeTemporaryStorageCode    = "PROPOSAL_MERGE_TEMPORARY_STORAGE_UNAVAILABLE"
	mergeEngineUnavailableCode   = "PROPOSAL_MERGE_ENGINE_UNAVAILABLE"
	mergeEngineUnsupportedCode   = "PROPOSAL_MERGE_ENGINE_UNSUPPORTED"
	mergeEngineOutputInvalidCode = "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID"
)

var (
	// These sentinels are useful to callers that need to distinguish resource
	// failures without depending on Git's process or diagnostic types.
	ErrMergeInputTooLarge  = errors.New(mergeInputTooLargeCode)
	ErrMergeContentInvalid = errors.New(mergeContentInvalidCode)
	ErrMergeResultTooLarge = errors.New(mergeResultTooLargeCode)
	ErrMergeBusy           = errors.New(mergeBusyCode)
	ErrMergeTimeout        = errors.New(mergeTimeoutCode)
)

// MergeInput contains the exact UTF-8 bytes for one three-way merge.
// Current is the file currently in the Workspace; Proposed is the old
// Proposal content and Base is the Proposal's immutable base snapshot.
type MergeInput struct {
	Current  []byte
	Base     []byte
	Proposed []byte
}

// MergeConflict is one structured diff3 conflict. The section bytes retain
// their original line endings and never contain Git marker lines.
type MergeConflict struct {
	Ordinal  int
	Current  []byte
	Base     []byte
	Proposed []byte
}

// MergeResult is a marker-free candidate plus the structured conflicts found
// by the fixed merge contract.
type MergeResult struct {
	Candidate []byte
	Conflicts []MergeConflict
	Algorithm string
	Contract  string
}

// ThreeWayMerger is the deliberately narrow domain-facing merge port.
type ThreeWayMerger interface {
	Merge(context.Context, MergeInput) (MergeResult, error)
}

var _ ThreeWayMerger = Client{}

type mergeSemaphore struct {
	ch chan struct{}
}

func newMergeSemaphore() *mergeSemaphore {
	return &mergeSemaphore{ch: make(chan struct{}, mergeConcurrency)}
}

func (c Client) mergeSemaphore() *mergeSemaphore {
	if c.mergeGate != nil {
		return c.mergeGate
	}
	return newMergeSemaphore()
}

// Merge executes only the fixed, repository-free git merge-file plumbing
// command. It never receives a repository path or caller-provided arguments.
func (c Client) Merge(ctx context.Context, input MergeInput) (result MergeResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateMergeInput(input); err != nil {
		return MergeResult{}, err
	}
	mergeCtx, cancel := context.WithTimeout(ctx, mergeTimeout)
	defer cancel()

	gate := c.mergeSemaphore()
	select {
	case gate.ch <- struct{}{}:
		defer func() { <-gate.ch }()
	case <-mergeCtx.Done():
		return MergeResult{}, mergeResourceError(ErrMergeBusy, mergeBusyCode, true)
	}

	dir, err := os.MkdirTemp("", "zhixu-merge-")
	if err != nil {
		return MergeResult{}, mergeStorageError(err)
	}
	cleanup := func() error { return os.RemoveAll(dir) }
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil && err == nil {
			result = MergeResult{}
			err = mergeStorageError(cleanupErr)
		}
	}()
	if err := os.Chmod(dir, 0o700); err != nil {
		return MergeResult{}, mergeStorageError(err)
	}
	for name, content := range map[string][]byte{
		"current": input.Current, "base": input.Base, "proposed": input.Proposed,
	} {
		if err := writeMergeFile(filepath.Join(dir, name), content); err != nil {
			return MergeResult{}, mergeStorageError(err)
		}
	}

	command := exec.CommandContext(mergeCtx, c.executable,
		"merge-file", "-p", "-q", "--diff3", "--diff-algorithm=myers", "--marker-size=32",
		"-L", "CURRENT", "-L", "BASE", "-L", "PROPOSED", "current", "base", "proposed")
	command.Dir = dir
	configureRemoteCommandCancellation(command)
	command.Env = mergeCommandEnvironment()
	stdout := newBoundedBuffer(mergeStdoutLimit, cancel)
	stderr := newBoundedBuffer(mergeStderrLimit, cancel)
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	output := stdout.Bytes()
	if stdout.Exceeded() || stderr.Exceeded() {
		return MergeResult{}, mergeResourceError(ErrMergeResultTooLarge, mergeResultTooLargeCode, false)
	}
	if contextErr := mergeCtx.Err(); contextErr != nil {
		if errors.Is(contextErr, context.DeadlineExceeded) {
			return MergeResult{}, mergeResourceError(ErrMergeTimeout, mergeTimeoutCode, true)
		}
		return MergeResult{}, mergeResourceError(contextErr, mergeTimeoutCode, true)
	}
	if runErr != nil {
		var execErr *exec.Error
		if errors.As(runErr, &execErr) {
			return MergeResult{}, mergeResourceError(runErr, mergeEngineUnavailableCode, false)
		}
		code := commandExitCode(runErr)
		if code < 0 || code >= 128 {
			if code == 129 {
				return MergeResult{}, mergeResourceError(nil, mergeEngineUnsupportedCode, false)
			}
			return MergeResult{}, mergeResourceError(nil, mergeEngineOutputInvalidCode, false)
		}
		if code == 0 {
			return MergeResult{}, mergeResourceError(nil, mergeEngineOutputInvalidCode, false)
		}
		result, parseErr := parseMergeOutput(output, code)
		if parseErr != nil {
			return MergeResult{}, parseErr
		}
		return result, nil
	}
	result, parseErr := parseMergeOutput(output, 0)
	if parseErr != nil {
		return MergeResult{}, parseErr
	}
	return result, nil
}

// MergeText is an explicit alias for callers that name the operation in
// terms of its text-only side effect boundary.
func (c Client) MergeText(ctx context.Context, input MergeInput) (MergeResult, error) {
	return c.Merge(ctx, input)
}

func validateMergeInput(input MergeInput) error {
	for _, content := range [][]byte{input.Current, input.Base, input.Proposed} {
		if len(content) > mergeInputLimit {
			return mergeResourceError(ErrMergeInputTooLarge, mergeInputTooLargeCode, false)
		}
		if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
			return mergeResourceError(ErrMergeContentInvalid, mergeContentInvalidCode, false)
		}
		if containsReservedMarker(content) {
			return mergeResourceError(ErrMergeContentInvalid, mergeContentInvalidCode, false)
		}
	}
	return nil
}

func writeMergeFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func mergeCommandEnvironment() []string {
	env := commandEnvironment(true, "")
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func mergeResourceError(cause error, code string, retryable bool) error {
	kind := foundation.ErrorNonRetryableFailure
	switch code {
	case mergeInputTooLargeCode, mergeContentInvalidCode:
		kind = foundation.ErrorInvalidInput
	case mergeBusyCode, mergeTimeoutCode, mergeTemporaryStorageCode:
		kind = foundation.ErrorRetryableFailure
	case mergeEngineUnavailableCode, mergeEngineUnsupportedCode:
		kind = foundation.ErrorDependencyUnavailable
	}
	return foundation.NewError(kind, code, retryable, cause)
}

func mergeStorageError(cause error) error {
	return foundation.NewError(foundation.ErrorRetryableFailure, mergeTemporaryStorageCode, true, cause)
}

type mergeParseState uint8

const (
	mergeNormal mergeParseState = iota
	mergeCurrent
	mergeBase
	mergeProposed
)

func parseMergeOutput(output []byte, exitCode int) (MergeResult, error) {
	if len(output) > mergeStdoutLimit {
		return MergeResult{}, mergeResourceError(ErrMergeResultTooLarge, mergeResultTooLargeCode, false)
	}
	if bytes.IndexByte(output, 0) >= 0 || !utf8.Valid(output) {
		return MergeResult{}, mergeResourceError(nil, mergeEngineOutputInvalidCode, false)
	}
	var candidate bytes.Buffer
	conflicts := make([]MergeConflict, 0)
	state := mergeNormal
	var current, base, proposed bytes.Buffer
	for offset := 0; offset < len(output); {
		line, raw, next := mergeLine(output, offset)
		kind := mergeMarkerKind(line)
		switch state {
		case mergeNormal:
			if kind == markerCurrent {
				state = mergeCurrent
				current.Reset()
				base.Reset()
				proposed.Reset()
			} else if kind == markerBase || kind == markerSeparator || kind == markerProposed {
				return MergeResult{}, mergeResourceError(nil, mergeEngineOutputInvalidCode, false)
			} else {
				candidate.Write(raw)
			}
		case mergeCurrent:
			if kind == markerBase {
				state = mergeBase
			} else {
				current.Write(raw)
			}
		case mergeBase:
			if kind == markerSeparator {
				state = mergeProposed
			} else {
				base.Write(raw)
			}
		case mergeProposed:
			if kind == markerProposed {
				if len(conflicts) >= mergeConflictLimit {
					return MergeResult{}, mergeResourceError(ErrMergeResultTooLarge, mergeResultTooLargeCode, false)
				}
				candidate.Write(current.Bytes())
				conflicts = append(conflicts, MergeConflict{Ordinal: len(conflicts) + 1, Current: bytes.Clone(current.Bytes()), Base: bytes.Clone(base.Bytes()), Proposed: bytes.Clone(proposed.Bytes())})
				state = mergeNormal
			} else {
				proposed.Write(raw)
			}
		}
		offset = next
	}
	if state != mergeNormal {
		return MergeResult{}, mergeResourceError(nil, mergeEngineOutputInvalidCode, false)
	}
	if len(candidate.Bytes()) > mergeInputLimit {
		return MergeResult{}, mergeResourceError(ErrMergeResultTooLarge, mergeResultTooLargeCode, false)
	}
	if (exitCode == 0 && len(conflicts) != 0) || (exitCode > 0 && (len(conflicts) == 0 || (exitCode < 127 && len(conflicts) != exitCode) || (exitCode == 127 && len(conflicts) < 127))) {
		return MergeResult{}, mergeResourceError(nil, mergeEngineOutputInvalidCode, false)
	}
	return MergeResult{Candidate: bytes.Clone(candidate.Bytes()), Conflicts: conflicts, Algorithm: "myers", Contract: MergeContractVersion}, nil
}

type markerKind uint8

const (
	markerNone markerKind = iota
	markerCurrent
	markerBase
	markerSeparator
	markerProposed
)

func mergeMarkerKind(line []byte) markerKind {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if bytes.Equal(line, append(bytes.Repeat([]byte{'<'}, mergeMarkerSize), []byte(" CURRENT")...)) {
		return markerCurrent
	}
	if bytes.Equal(line, append(bytes.Repeat([]byte{'|'}, mergeMarkerSize), []byte(" BASE")...)) {
		return markerBase
	}
	if bytes.Equal(line, bytes.Repeat([]byte{'='}, mergeMarkerSize)) {
		return markerSeparator
	}
	if bytes.Equal(line, append(bytes.Repeat([]byte{'>'}, mergeMarkerSize), []byte(" PROPOSED")...)) {
		return markerProposed
	}
	return markerNone
}

func containsReservedMarker(content []byte) bool {
	for offset := 0; offset < len(content); {
		line, _, next := mergeLine(content, offset)
		if mergeMarkerKind(line) != markerNone {
			return true
		}
		offset = next
	}
	return false
}

func mergeLine(content []byte, offset int) (line, raw []byte, next int) {
	if offset >= len(content) {
		return nil, nil, offset
	}
	end := bytes.IndexByte(content[offset:], '\n')
	if end < 0 {
		return content[offset:], content[offset:], len(content)
	}
	end += offset
	return content[offset:end], content[offset : end+1], end + 1
}
