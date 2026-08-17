package gitcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const defaultCommandOutputLimit = 4 << 20

var (
	errCommandOutputLimit      = errors.New("git command output exceeds limit")
	errCommandIndexFileUnsafe  = errors.New("git command index file is unsafe")
	errCommandConfigHomeUnsafe = errors.New("git command config home is unsafe")
)

// commandOptions 描述受限 Git 命令执行策略；隔离目录必须由进程创建并独占。
type commandOptions struct {
	ReadOnly          bool
	Stdin             io.Reader
	MaxOutputBytes    int
	IndexFile         string
	IsolatedConfigDir string
}

// commandResult 保留命令的有界输出和退出码，供同包 Git Adapter 做稳定语义解析。
type commandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// runCommand 在固定 Git 全局参数和清理后的环境中执行受控子命令。
func (c Client) runCommand(ctx context.Context, rootPath string, options commandOptions, args ...string) (commandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.IndexFile != "" && (!filepath.IsAbs(options.IndexFile) || filepath.Clean(options.IndexFile) != options.IndexFile || strings.ContainsAny(options.IndexFile, "\x00\r\n")) {
		return commandResult{ExitCode: -1}, errCommandIndexFileUnsafe
	}
	if options.IsolatedConfigDir != "" {
		info, err := os.Lstat(options.IsolatedConfigDir)
		if err != nil || !filepath.IsAbs(options.IsolatedConfigDir) || filepath.Clean(options.IsolatedConfigDir) != options.IsolatedConfigDir ||
			strings.ContainsAny(options.IsolatedConfigDir, "\x00\r\n") || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return commandResult{ExitCode: -1}, errCommandConfigHomeUnsafe
		}
	}
	limit := options.MaxOutputBytes
	if limit <= 0 {
		limit = defaultCommandOutputLimit
	}

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := newBoundedBuffer(limit, cancel)
	stderr := newBoundedBuffer(limit, cancel)
	commandArgs := []string{
		"--no-pager",
		"--literal-pathspecs",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.quotePath=true",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "diff.compactionHeuristic=false",
		"-c", "diff.suppressBlankEmpty=false",
		"-c", "log.showSignature=false",
		"-c", "color.ui=false",
		"-C", rootPath,
	}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(runContext, c.executable, commandArgs...)
	command.Env = commandEnvironmentWithConfig(options.ReadOnly, options.IndexFile, options.IsolatedConfigDir)
	command.Stdin = options.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	result := commandResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: commandExitCode(runErr),
	}
	if runErr == nil && !stdout.Exceeded() && !stderr.Exceeded() {
		return result, nil
	}

	commandErr := &commandError{
		err:         runErr,
		stdout:      strings.TrimSpace(string(result.Stdout)),
		stderr:      strings.TrimSpace(string(result.Stderr)),
		exitCode:    result.ExitCode,
		outputLimit: stdout.Exceeded() || stderr.Exceeded(),
	}
	if commandErr.outputLimit {
		commandErr.err = errCommandOutputLimit
	} else if contextErr := ctx.Err(); contextErr != nil {
		commandErr.err = contextErr
		commandErr.contextErr = contextErr
	}
	return result, commandErr
}

func commandEnvironment(readOnly bool, indexFile string) []string {
	return commandEnvironmentWithConfig(readOnly, indexFile, "")
}

func commandEnvironmentWithConfig(readOnly bool, indexFile, isolatedConfigDir string) []string {
	environment := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if strings.HasPrefix(name, "GIT_") || name == "LC_ALL" || name == "LANG" ||
			isolatedConfigDir != "" && (name == "HOME" || name == "XDG_CONFIG_HOME") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment,
		"LC_ALL=C",
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_EDITOR=:",
		"GIT_SEQUENCE_EDITOR=:",
	)
	if readOnly {
		environment = append(environment, "GIT_OPTIONAL_LOCKS=0")
	}
	if indexFile != "" {
		environment = append(environment, "GIT_INDEX_FILE="+indexFile)
	}
	if isolatedConfigDir != "" {
		environment = append(environment,
			"HOME="+isolatedConfigDir,
			"XDG_CONFIG_HOME="+isolatedConfigDir,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_ATTR_NOSYSTEM=1",
		)
	}
	return environment
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type commandError struct {
	err         error
	stdout      string
	stderr      string
	exitCode    int
	outputLimit bool
	contextErr  error
}

func (e *commandError) Error() string { return "git command failed" }
func (e *commandError) Unwrap() error { return e.err }

// ExitCode 返回 Git 进程退出码；未能启动或被取消时返回 -1。
func (e *commandError) ExitCode() int { return e.exitCode }

// Stdout 返回供内部诊断和恢复判定使用的有界标准输出。
func (e *commandError) Stdout() string { return e.stdout }

// Stderr 返回供内部诊断和稳定错误分类使用的有界标准错误。
func (e *commandError) Stderr() string { return e.stderr }

// OutputLimitExceeded 表示命令因输出超过调用方上限而中止。
func (e *commandError) OutputLimitExceeded() bool { return e.outputLimit }

// ContextErr 返回调用上下文的取消或超时原因。
func (e *commandError) ContextErr() error { return e.contextErr }

type boundedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func newBoundedBuffer(limit int, cancel context.CancelFunc) *boundedBuffer {
	return &boundedBuffer{limit: limit, cancel: cancel}
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded {
		return 0, errCommandOutputLimit
	}
	remaining := b.limit - b.buffer.Len()
	if remaining >= len(value) {
		return b.buffer.Write(value)
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(value[:remaining])
	}
	b.exceeded = true
	if b.cancel != nil {
		b.cancel()
	}
	return len(value), errCommandOutputLimit
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func (b *boundedBuffer) Exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}
