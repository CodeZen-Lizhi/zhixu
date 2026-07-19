package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestToolDefaultsAreExplicitlyDisabledAndBounded(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if cfg.ToolRuntimeMode != ToolModeDisabled || cfg.WebFetchMode != ToolModeDisabled {
		t.Fatalf("tool capabilities must default to disabled: %s", cfg)
	}
	if cfg.WebFetchTimeout != 30*time.Second || cfg.WebFetchResponseHeaderTimeout != 10*time.Second || cfg.WebFetchTLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("unexpected web fetch timeout defaults: %s", cfg)
	}
	if cfg.WebFetchMaxRedirects != 5 || cfg.WebFetchMaxURLBytes != 8*1024 || cfg.WebFetchMaxResponseHeaderBytes != 64*1024 ||
		cfg.WebFetchMaxBodyBytes != 2*1024*1024 || cfg.WebFetchMaxTextBytes != 512*1024 || cfg.WebFetchMaxResolvedIPs != 16 {
		t.Fatalf("unexpected web fetch resource defaults: %s", cfg)
	}
	if !reflect.DeepEqual(cfg.WebFetchAllowedContentTypes, []string{"text/plain", "text/html"}) {
		t.Fatalf("unexpected content type defaults: %#v", cfg.WebFetchAllowedContentTypes)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}

	first := Defaults()
	second := Defaults()
	first.WebFetchAllowedContentTypes[0] = "application/json"
	if second.WebFetchAllowedContentTypes[0] != "text/plain" {
		t.Fatal("Defaults returned aliased web fetch content types")
	}
}

func TestLoadToolConfigFromYAMLThenEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`tool_runtime_mode: enabled
web_fetch_mode: disabled
web_fetch_timeout: 40s
web_fetch_response_header_timeout: 12s
web_fetch_tls_handshake_timeout: 13s
web_fetch_max_redirects: 4
web_fetch_max_url_bytes: 9000
web_fetch_max_response_header_bytes: 70000
web_fetch_max_body_bytes: 3000000
web_fetch_max_text_bytes: 600000
web_fetch_max_resolved_ips: 18
web_fetch_allowed_content_types:
  - text/html
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ZHIXU_TOOL_RUNTIME_MODE":                   "enabled",
		"ZHIXU_WEB_FETCH_MODE":                      "enabled",
		"ZHIXU_WEB_FETCH_TIMEOUT":                   "45s",
		"ZHIXU_WEB_FETCH_RESPONSE_HEADER_TIMEOUT":   "14s",
		"ZHIXU_WEB_FETCH_TLS_HANDSHAKE_TIMEOUT":     "15s",
		"ZHIXU_WEB_FETCH_MAX_REDIRECTS":             "6",
		"ZHIXU_WEB_FETCH_MAX_URL_BYTES":             "10000",
		"ZHIXU_WEB_FETCH_MAX_RESPONSE_HEADER_BYTES": "80000",
		"ZHIXU_WEB_FETCH_MAX_BODY_BYTES":            "4000000",
		"ZHIXU_WEB_FETCH_MAX_TEXT_BYTES":            "700000",
		"ZHIXU_WEB_FETCH_MAX_RESOLVED_IPS":          "20",
		"ZHIXU_WEB_FETCH_ALLOWED_CONTENT_TYPES":     "text/plain,text/html",
	}
	cfg, err := LoadWithLookup(path, func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolRuntimeMode != ToolModeEnabled || cfg.WebFetchMode != ToolModeEnabled || cfg.WebFetchTimeout != 45*time.Second ||
		cfg.WebFetchResponseHeaderTimeout != 14*time.Second || cfg.WebFetchTLSHandshakeTimeout != 15*time.Second ||
		cfg.WebFetchMaxRedirects != 6 || cfg.WebFetchMaxURLBytes != 10000 || cfg.WebFetchMaxResponseHeaderBytes != 80000 ||
		cfg.WebFetchMaxBodyBytes != 4000000 || cfg.WebFetchMaxTextBytes != 700000 || cfg.WebFetchMaxResolvedIPs != 20 ||
		!reflect.DeepEqual(cfg.WebFetchAllowedContentTypes, []string{"text/plain", "text/html"}) {
		t.Fatalf("tool config not loaded: %s", cfg)
	}
}

func TestLoadRejectsInvalidToolModesFromEnvironment(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		key  string
		want string
	}{
		{key: "ZHIXU_TOOL_RUNTIME_MODE", want: "tool_runtime_mode"},
		{key: "ZHIXU_WEB_FETCH_MODE", want: "web_fetch_mode"},
	} {
		test := test
		t.Run(test.key, func(t *testing.T) {
			t.Parallel()
			_, err := LoadWithLookup("", func(candidate string) (string, bool) {
				if candidate == test.key {
					return "automatic", true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestValidateToolModesAndWebFetchBounds(t *testing.T) {
	t.Parallel()
	enabled := func() Config {
		cfg := Defaults()
		cfg.ToolRuntimeMode = ToolModeEnabled
		cfg.WebFetchMode = ToolModeEnabled
		return cfg
	}
	if err := enabled().Validate(); err != nil {
		t.Fatalf("enabled bounded config rejected: %v", err)
	}

	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{name: "tool mode", change: func(cfg *Config) { cfg.ToolRuntimeMode = "auto" }, want: "tool_runtime_mode"},
		{name: "web mode", change: func(cfg *Config) { cfg.WebFetchMode = "auto" }, want: "web_fetch_mode"},
		{name: "web without runtime", change: func(cfg *Config) { cfg.WebFetchMode = ToolModeEnabled }, want: "tool_runtime_mode"},
		{name: "timeout zero", change: func(cfg *Config) { cfg.WebFetchTimeout = 0 }, want: "web_fetch_timeout"},
		{name: "timeout high", change: func(cfg *Config) { cfg.WebFetchTimeout = 2*time.Minute + time.Nanosecond }, want: "web_fetch_timeout"},
		{name: "header zero", change: func(cfg *Config) { cfg.WebFetchResponseHeaderTimeout = 0 }, want: "web_fetch_response_header_timeout"},
		{name: "header high", change: func(cfg *Config) { cfg.WebFetchResponseHeaderTimeout = 30*time.Second + time.Nanosecond }, want: "web_fetch_response_header_timeout"},
		{name: "tls zero", change: func(cfg *Config) { cfg.WebFetchTLSHandshakeTimeout = 0 }, want: "web_fetch_tls_handshake_timeout"},
		{name: "tls high", change: func(cfg *Config) { cfg.WebFetchTLSHandshakeTimeout = 30*time.Second + time.Nanosecond }, want: "web_fetch_tls_handshake_timeout"},
		{name: "redirect negative", change: func(cfg *Config) { cfg.WebFetchMaxRedirects = -1 }, want: "web_fetch_max_redirects"},
		{name: "redirect high", change: func(cfg *Config) { cfg.WebFetchMaxRedirects = 21 }, want: "web_fetch_max_redirects"},
		{name: "url zero", change: func(cfg *Config) { cfg.WebFetchMaxURLBytes = 0 }, want: "web_fetch_max_url_bytes"},
		{name: "url high", change: func(cfg *Config) { cfg.WebFetchMaxURLBytes = 64*1024 + 1 }, want: "web_fetch_max_url_bytes"},
		{name: "header bytes zero", change: func(cfg *Config) { cfg.WebFetchMaxResponseHeaderBytes = 0 }, want: "web_fetch_max_response_header_bytes"},
		{name: "header bytes high", change: func(cfg *Config) { cfg.WebFetchMaxResponseHeaderBytes = 2*1024*1024 + 1 }, want: "web_fetch_max_response_header_bytes"},
		{name: "body zero", change: func(cfg *Config) { cfg.WebFetchMaxBodyBytes = 0 }, want: "web_fetch_max_body_bytes"},
		{name: "body high", change: func(cfg *Config) { cfg.WebFetchMaxBodyBytes = 32*1024*1024 + 1 }, want: "web_fetch_max_body_bytes"},
		{name: "text zero", change: func(cfg *Config) { cfg.WebFetchMaxTextBytes = 0 }, want: "web_fetch_max_text_bytes"},
		{name: "text above body", change: func(cfg *Config) { cfg.WebFetchMaxTextBytes = int(cfg.WebFetchMaxBodyBytes) + 1 }, want: "web_fetch_max_text_bytes"},
		{name: "resolved zero", change: func(cfg *Config) { cfg.WebFetchMaxResolvedIPs = 0 }, want: "web_fetch_max_resolved_ips"},
		{name: "resolved high", change: func(cfg *Config) { cfg.WebFetchMaxResolvedIPs = 65 }, want: "web_fetch_max_resolved_ips"},
		{name: "content empty", change: func(cfg *Config) { cfg.WebFetchAllowedContentTypes = nil }, want: "web_fetch_allowed_content_types"},
		{name: "content unsupported", change: func(cfg *Config) { cfg.WebFetchAllowedContentTypes = []string{"application/json"} }, want: "web_fetch_allowed_content_types"},
		{name: "content whitespace", change: func(cfg *Config) { cfg.WebFetchAllowedContentTypes = []string{" text/plain"} }, want: "web_fetch_allowed_content_types"},
		{name: "content duplicate", change: func(cfg *Config) { cfg.WebFetchAllowedContentTypes = []string{"text/plain", "text/plain"} }, want: "web_fetch_allowed_content_types"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			test.change(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestToolEnvironmentParseErrorsAreStableAndSecretSafe(t *testing.T) {
	t.Parallel()
	const secret = "tool-config-secret-canary"
	keys := []string{
		"ZHIXU_WEB_FETCH_TIMEOUT",
		"ZHIXU_WEB_FETCH_RESPONSE_HEADER_TIMEOUT",
		"ZHIXU_WEB_FETCH_TLS_HANDSHAKE_TIMEOUT",
		"ZHIXU_WEB_FETCH_MAX_REDIRECTS",
		"ZHIXU_WEB_FETCH_MAX_URL_BYTES",
		"ZHIXU_WEB_FETCH_MAX_RESPONSE_HEADER_BYTES",
		"ZHIXU_WEB_FETCH_MAX_BODY_BYTES",
		"ZHIXU_WEB_FETCH_MAX_TEXT_BYTES",
		"ZHIXU_WEB_FETCH_MAX_RESOLVED_IPS",
		"ZHIXU_WEB_FETCH_ALLOWED_CONTENT_TYPES",
	}
	for _, key := range keys {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			_, err := LoadWithLookup("", func(candidate string) (string, bool) {
				if candidate == key {
					return secret, true
				}
				return "", false
			})
			if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), key) {
				t.Fatalf("key=%s error=%v", key, err)
			}
		})
	}
}

func TestToolYAMLParseErrorsAreStableAndSecretSafe(t *testing.T) {
	t.Parallel()
	const secret = "tool-yaml-secret-canary"
	for _, field := range []string{"web_fetch_timeout", "web_fetch_response_header_timeout", "web_fetch_tls_handshake_timeout"} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(field+": "+secret+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadWithLookup(path, func(string) (string, bool) { return "", false })
			if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), field) {
				t.Fatalf("field=%s error=%v", field, err)
			}
		})
	}
}

func TestToolConfigFormattingDoesNotInventNetworkExceptions(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.ToolRuntimeMode = ToolModeEnabled
	cfg.WebFetchMode = ToolModeEnabled
	for _, formatted := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		for _, expected := range []string{
			"ToolRuntimeMode:\"enabled\"",
			"WebFetchMode:\"enabled\"",
			"WebFetchTimeout:30s",
			"WebFetchMaxRedirects:5",
			"WebFetchAllowedContentTypeCount:2",
		} {
			if !strings.Contains(formatted, expected) {
				t.Fatalf("formatted config omitted %q: %s", expected, formatted)
			}
		}
		for _, forbidden := range []string{"Proxy", "PrivateNetwork", "AllowedPrivate", "TrustedCIDR"} {
			if strings.Contains(formatted, forbidden) {
				t.Fatalf("formatted config exposed unsupported exception field %q: %s", forbidden, formatted)
			}
		}
	}
	configType := reflect.TypeOf(cfg)
	for index := 0; index < configType.NumField(); index++ {
		name := configType.Field(index).Name
		for _, forbidden := range []string{"Proxy", "Private", "CIDR"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("unsupported network exception field %q", name)
			}
		}
	}
}
