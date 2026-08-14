//go:build unix

package localmodelruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

const (
	ollamaExecutable = "/usr/bin/ollama"
	ollamaHome       = "/var/lib/zhixu/ollama"
	ollamaModels     = "/var/lib/zhixu/ollama/.ollama/models"
	ollamaChildHost  = "127.0.0.1:11435"
)

var ollamaChildEnvironment = []string{
	"HOME=" + ollamaHome,
	"OLLAMA_HOST=" + ollamaChildHost,
	"OLLAMA_MODELS=" + ollamaModels,
	"OLLAMA_NO_CLOUD=true",
	"OLLAMA_NOPRUNE=true",
	"OLLAMA_VULKAN=0",
	"PATH=/usr/bin:/bin",
}

// ExecChildRunner starts the fixed Ollama executable without a shell.
type ExecChildRunner struct{}

// Start starts /usr/bin/ollama serve in its own process group with an explicit
// environment allowlist. The context is checked before start but does not own
// process cancellation; Manager.Stop owns the TERM/grace/KILL protocol.
func (ExecChildRunner) Start(ctx context.Context) (Child, error) {
	if ctx == nil {
		return nil, errors.New("local model runtime child context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	command := newOllamaCommand()
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &execChild{command: command}, nil
}

func newOllamaCommand() *exec.Cmd {
	command := exec.Command(ollamaExecutable, "serve")
	command.Dir = ollamaHome
	command.Env = append([]string(nil), ollamaChildEnvironment...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command
}

type execChild struct {
	command *exec.Cmd
}

func (child *execChild) PID() int {
	if child == nil || child.command == nil || child.command.Process == nil {
		return 0
	}
	return child.command.Process.Pid
}

func (child *execChild) SignalTerm() error {
	if child.PID() <= 0 {
		return ErrChildUnavailable
	}
	if err := child.command.Process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return ErrChildUnavailable
		}
		return err
	}
	return nil
}

func (child *execChild) KillProcessGroup() error {
	pid := child.PID()
	if pid <= 0 {
		return ErrChildUnavailable
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ErrChildUnavailable
		}
		return err
	}
	return nil
}

func (child *execChild) Wait() error {
	if child == nil || child.command == nil {
		return ErrChildUnavailable
	}
	return child.command.Wait()
}
