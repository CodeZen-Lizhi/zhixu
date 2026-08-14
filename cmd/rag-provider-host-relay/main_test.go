package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelayListenAddressRequiresExactIPv4Loopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "127.0.0.1:11434"} {
		if _, err := relayListenAddress(testEnvironment(map[string]string{envRelayListenAddress: address})); err != nil {
			t.Fatalf("address=%q err=%v", address, err)
		}
	}
	for _, address := range []string{"localhost:11434", "127.0.0.2:11434", "0.0.0.0:11434", "[::1]:11434", "127.0.0.1:70000"} {
		if _, err := relayListenAddress(testEnvironment(map[string]string{envRelayListenAddress: address})); err == nil {
			t.Fatalf("address unexpectedly accepted: %q", address)
		}
	}
}

func TestReadRelayConfigRequiresPrivateRegularStrictJSON(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "relay.json")
	writeConfig := func(t *testing.T, contents string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, `{"chat_base_url":"https://chat.example.test/gateway"}`, 0o600)
	config, err := readRelayConfig(path)
	if err != nil || config.ChatBaseURL != "https://chat.example.test/gateway" {
		t.Fatalf("config=%#v err=%v", config, err)
	}

	for name, testCase := range map[string]struct {
		contents string
		mode     os.FileMode
	}{
		"unknown field":      {`{"chat_base_url":"https://chat.example.test","api_key":"forbidden"}`, 0o600},
		"duplicate field":    {`{"chat_base_url":"https://first.example.test","chat_base_url":"https://second.example.test"}`, 0o600},
		"missing URL":        {`{}`, 0o600},
		"multiple documents": {`{"chat_base_url":"https://chat.example.test"}{}`, 0o600},
		"public permissions": {`{"chat_base_url":"https://chat.example.test"}`, 0o644},
		"too large":          {`{"chat_base_url":"https://chat.example.test","padding":"` + strings.Repeat("x", int(maxRelayConfigBytes)) + `"}`, 0o600},
	} {
		t.Run(name, func(t *testing.T) {
			writeConfig(t, testCase.contents, testCase.mode)
			if _, err := readRelayConfig(path); err == nil {
				t.Fatal("expected config rejection")
			}
		})
	}
}

func TestReadRelayConfigRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"chat_base_url":"https://chat.example.test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "relay.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readRelayConfig(path); err == nil {
		t.Fatal("symlink config was accepted")
	}
}

func testEnvironment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
