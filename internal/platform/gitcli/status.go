// Package gitcli implements project-owned Git interfaces with argument-safe
// Git CLI invocations.
package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const defaultExecutable = "git"

// Client reads repository state through the Git CLI without changing files.
type Client struct {
	executable string
}

// New creates a Git CLI client. An empty executable uses git from PATH.
func New(executable string) Client {
	if strings.TrimSpace(executable) == "" {
		executable = defaultExecutable
	}
	return Client{executable: executable}
}

// Status returns the branch, HEAD and dirty state for the repository containing
// rootPath. A directory outside any repository returns Present=false.
func (c Client) Status(ctx context.Context, rootPath string) (domain.GitStatus, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return domain.GitStatus{}, foundation.NewError(foundation.ErrorInvalidInput, "GIT_ROOT_REQUIRED", false, errors.New("git root is required"))
	}

	repositoryPath, err := c.output(ctx, rootPath, "rev-parse", "--show-toplevel")
	if err != nil {
		if isMissingRepository(err) {
			return domain.GitStatus{}, nil
		}
		return domain.GitStatus{}, classify("GIT_STATUS_FAILED", err)
	}
	repositoryPath, err = filepath.Abs(repositoryPath)
	if err != nil {
		return domain.GitStatus{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "GIT_STATUS_FAILED", false, fmt.Errorf("canonicalize repository root: %w", err))
	}
	repositoryPath, err = filepath.EvalSymlinks(repositoryPath)
	if err != nil {
		return domain.GitStatus{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "GIT_STATUS_FAILED", false, fmt.Errorf("resolve repository root: %w", err))
	}
	repositoryPath = filepath.Clean(repositoryPath)

	branch, err := c.output(ctx, rootPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil && !isExitCode(err, 1) {
		return domain.GitStatus{}, classify("GIT_STATUS_FAILED", err)
	}
	head, err := c.output(ctx, rootPath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		if !isUnknownRevision(err) {
			return domain.GitStatus{}, classify("GIT_STATUS_FAILED", err)
		}
		head = ""
	}
	porcelain, err := c.outputBytes(ctx, rootPath, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return domain.GitStatus{}, classify("GIT_STATUS_FAILED", err)
	}

	return domain.GitStatus{
		Present:        true,
		RepositoryPath: repositoryPath,
		Branch:         branch,
		Head:           head,
		Dirty:          len(porcelain) > 0,
	}, nil
}

// Initialize 创建缺失的 Git 仓库并返回初始化后的只读基线。
func (c Client) Initialize(ctx context.Context, rootPath string) (domain.GitStatus, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return domain.GitStatus{}, foundation.NewError(foundation.ErrorInvalidInput, "GIT_ROOT_REQUIRED", false, errors.New("git root is required"))
	}
	status, err := c.Status(ctx, rootPath)
	if err != nil {
		return domain.GitStatus{}, err
	}
	if status.Present {
		return status, nil
	}
	if _, err := c.output(ctx, rootPath, "init"); err != nil {
		return domain.GitStatus{}, classify("GIT_INITIALIZE_FAILED", err)
	}
	status, err = c.Status(ctx, rootPath)
	if err != nil {
		return domain.GitStatus{}, err
	}
	if !status.Present {
		return domain.GitStatus{}, foundation.NewError(foundation.ErrorConsistencyViolation, "GIT_INITIALIZATION_INCOMPLETE", false, errors.New("git init did not create a repository"))
	}
	return status, nil
}

func (c Client) output(ctx context.Context, rootPath string, args ...string) (string, error) {
	value, err := c.outputBytes(ctx, rootPath, args...)
	return strings.TrimSpace(string(value)), err
}

func (c Client) outputBytes(ctx context.Context, rootPath string, args ...string) ([]byte, error) {
	commandArgs := []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-C", rootPath,
	}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, c.executable, commandArgs...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return nil, &commandError{err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.Bytes(), nil
}

type commandError struct {
	err    error
	stderr string
}

func (e *commandError) Error() string { return "git command failed" }
func (e *commandError) Unwrap() error { return e.err }

func isMissingRepository(err error) bool {
	var commandErr *commandError
	return errors.As(err, &commandErr) && strings.Contains(commandErr.stderr, "not a git repository")
}

func isUnknownRevision(err error) bool {
	var commandErr *commandError
	if !errors.As(err, &commandErr) {
		return false
	}
	return strings.Contains(commandErr.stderr, "Needed a single revision") || strings.Contains(commandErr.stderr, "unknown revision")
}

func isExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}

func classify(code string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, "GIT_STATUS_TIMEOUT", true, err)
	}
	var pathErr *exec.Error
	if errors.As(err, &pathErr) || errors.Is(err, os.ErrNotExist) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "GIT_COMMAND_UNAVAILABLE", false, err)
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}

var _ domain.GitStatusReader = Client{}
var _ domain.GitInitializer = Client{}
