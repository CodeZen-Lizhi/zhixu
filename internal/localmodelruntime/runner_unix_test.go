//go:build unix

package localmodelruntime

import (
	"context"
	"reflect"
	"testing"
)

func TestExecChildRunnerUsesFixedCommandAndEnvironment(t *testing.T) {
	if ollamaExecutable != "/usr/bin/ollama" || ollamaChildHost != "127.0.0.1:11435" {
		t.Fatalf("executable=%q host=%q", ollamaExecutable, ollamaChildHost)
	}
	wantEnvironment := []string{
		"HOME=/var/lib/zhixu/ollama",
		"OLLAMA_HOST=127.0.0.1:11435",
		"OLLAMA_MODELS=/var/lib/zhixu/ollama/.ollama/models",
		"OLLAMA_NO_CLOUD=true",
		"OLLAMA_NOPRUNE=true",
		"OLLAMA_VULKAN=0",
		"PATH=/usr/bin:/bin",
	}
	if !reflect.DeepEqual(ollamaChildEnvironment, wantEnvironment) {
		t.Fatalf("child environment = %#v", ollamaChildEnvironment)
	}
	command := newOllamaCommand()
	if command.Path != ollamaExecutable || !reflect.DeepEqual(command.Args, []string{ollamaExecutable, "serve"}) || command.Dir != ollamaHome {
		t.Fatalf("command path=%q args=%q dir=%q", command.Path, command.Args, command.Dir)
	}
	if !reflect.DeepEqual(command.Env, wantEnvironment) || command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("command env=%#v sysproc=%+v", command.Env, command.SysProcAttr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ExecChildRunner{}).Start(ctx); err != context.Canceled {
		t.Fatalf("Start(cancelled) error = %v", err)
	}
}
