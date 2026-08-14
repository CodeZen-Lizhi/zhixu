package models

import (
	"strings"
	"testing"
)

func TestAppendEmbeddingPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		baseURL           string
		requestPath       string
		allowLoopbackHTTP bool
		want              string
	}{
		{name: "openai service root", baseURL: "https://provider.example", requestPath: "/v1/embeddings", want: "https://provider.example/v1/embeddings"},
		{name: "openai service root trailing slash", baseURL: "https://provider.example/", requestPath: "/v1/embeddings", want: "https://provider.example/v1/embeddings"},
		{name: "openai gateway subpath", baseURL: "https://provider.example/gateway", requestPath: "/v1/embeddings", want: "https://provider.example/gateway/v1/embeddings"},
		{name: "openai gateway subpath trailing slash", baseURL: "https://provider.example/gateway/", requestPath: "/v1/embeddings", want: "https://provider.example/gateway/v1/embeddings"},
		{name: "openai api root", baseURL: "https://provider.example/v1", requestPath: "/v1/embeddings", want: "https://provider.example/v1/embeddings"},
		{name: "openai prefixed api root trailing slash", baseURL: "https://provider.example/gateway/v1/", requestPath: "/v1/embeddings", want: "https://provider.example/gateway/v1/embeddings"},
		{name: "openai exact endpoint", baseURL: "https://provider.example/v1/embeddings", requestPath: "/v1/embeddings", want: "https://provider.example/v1/embeddings"},
		{name: "openai prefixed exact endpoint trailing slash", baseURL: "https://provider.example/gateway/v1/embeddings/", requestPath: "/v1/embeddings", want: "https://provider.example/gateway/v1/embeddings"},
		{name: "ollama service root", baseURL: "http://127.0.0.1:11434", requestPath: "/api/embed", allowLoopbackHTTP: true, want: "http://127.0.0.1:11434/api/embed"},
		{name: "ollama gateway subpath", baseURL: "http://127.0.0.1:11434/gateway/", requestPath: "/api/embed", allowLoopbackHTTP: true, want: "http://127.0.0.1:11434/gateway/api/embed"},
		{name: "ollama v1 subpath", baseURL: "http://127.0.0.1:11434/gateway/v1/", requestPath: "/api/embed", allowLoopbackHTTP: true, want: "http://127.0.0.1:11434/gateway/v1/api/embed"},
		{name: "ollama exact endpoint", baseURL: "http://127.0.0.1:11434/api/embed/", requestPath: "/api/embed", allowLoopbackHTTP: true, want: "http://127.0.0.1:11434/api/embed"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			baseURL, _, err := parseEmbeddingBaseURL(test.baseURL, test.allowLoopbackHTTP)
			if err != nil {
				t.Fatal(err)
			}
			got := appendEmbeddingPath(baseURL, test.requestPath)
			if got != test.want {
				t.Fatalf("appendEmbeddingPath() = %q, want %q", got, test.want)
			}
			if test.requestPath == "/v1/embeddings" && strings.Count(got, "/v1/embeddings") != 1 {
				t.Fatalf("OpenAI-compatible endpoint must contain exactly one /v1/embeddings suffix: %q", got)
			}
		})
	}
}
