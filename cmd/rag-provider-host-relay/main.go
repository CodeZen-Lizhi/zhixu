// Command rag-provider-host-relay starts the loopback-only Chat relay used by real-provider smoke tests.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

const (
	envRelayListenAddress = "ZHIXU_RAG_PROVIDER_HOST_RELAY_LISTEN_ADDR"
	envRelayConfigPath    = "ZHIXU_RAG_PROVIDER_HOST_RELAY_CONFIG_PATH"
	maxRelayConfigBytes   = int64(4 << 10)
	relayShutdownTimeout  = 5 * time.Second
)

type relayConfigFile struct {
	ChatBaseURL string `json:"chat_base_url"`
}

func main() {
	listenAddress, err := relayListenAddress(os.LookupEnv)
	if err != nil {
		failRelay("rag provider host relay configuration failed")
	}
	configPath, err := requiredEnvironment(os.LookupEnv, envRelayConfigPath)
	if err != nil {
		failRelay("rag provider host relay configuration failed")
	}
	config, err := readRelayConfig(configPath)
	if err != nil {
		failRelay("rag provider host relay configuration failed")
	}
	handler, err := platformmodels.NewRAGSmokeChatRelay(config.ChatBaseURL)
	if err != nil {
		failRelay("rag provider host relay configuration failed")
	}
	listener, err := net.Listen("tcp4", listenAddress)
	if err != nil {
		failRelay("rag provider host relay failed")
	}
	defer listener.Close()

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second}
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	serverError := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); !errors.Is(serveErr, http.ErrServerClosed) {
			serverError <- serveErr
		}
	}()
	fmt.Fprintln(os.Stdout, "rag provider host relay started")
	select {
	case <-stop.Done():
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), relayShutdownTimeout)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			failRelay("rag provider host relay shutdown failed")
		}
	case <-serverError:
		failRelay("rag provider host relay failed")
	}
}

func failRelay(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func relayListenAddress(lookup func(string) (string, bool)) (string, error) {
	address, err := requiredEnvironment(lookup, envRelayListenAddress)
	if err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return "", errors.New("invalid relay address")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort > 65535 {
		return "", errors.New("invalid relay port")
	}
	return address, nil
}

func requiredEnvironment(lookup func(string) (string, bool), name string) (string, error) {
	value, present := lookup(name)
	if !present || value == "" {
		return "", errors.New("required environment is missing")
	}
	return value, nil
}

func readRelayConfig(configPath string) (relayConfigFile, error) {
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	current, err := os.Lstat(configPath)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) || !os.SameFile(opened, current) {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxRelayConfigBytes+1))
	if err != nil || int64(len(contents)) > maxRelayConfigBytes {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	return decodeRelayConfig(contents)
}

func decodeRelayConfig(contents []byte) (relayConfigFile, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	var config relayConfigFile
	seenChatBaseURL := false
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || key != "chat_base_url" || seenChatBaseURL {
			return relayConfigFile{}, errors.New("invalid relay config file")
		}
		if err := decoder.Decode(&config.ChatBaseURL); err != nil {
			return relayConfigFile{}, errors.New("invalid relay config file")
		}
		seenChatBaseURL = true
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || !seenChatBaseURL || config.ChatBaseURL == "" {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return relayConfigFile{}, errors.New("invalid relay config file")
	}
	return config, nil
}
