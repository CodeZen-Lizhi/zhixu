// Package config loads and validates process configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultHTTPAddr        = "127.0.0.1:8080"
	defaultVersion         = "dev"
	defaultPingTimeout     = 2 * time.Second
	defaultHealthInterval  = 15 * time.Second
	defaultShutdownTimeout = 10 * time.Second
	defaultMaxConns        = int32(10)
	defaultMinConns        = int32(1)
)

// Config contains non-secret process settings. DatabaseURL is supplied by the
// environment or a local YAML file and is never emitted by logging code.
type Config struct {
	AppName             string        `yaml:"app_name"`
	Version             string        `yaml:"version"`
	Environment         string        `yaml:"environment"`
	HTTPAddr            string        `yaml:"http_addr"`
	DatabaseURL         string        `yaml:"database_url"`
	DatabaseHost        string        `yaml:"database_host"`
	DatabasePort        string        `yaml:"database_port"`
	DatabaseName        string        `yaml:"database_name"`
	DatabaseUser        string        `yaml:"database_user"`
	DatabasePassword    string        `yaml:"database_password"`
	DatabaseMaxConns    int32         `yaml:"database_max_conns"`
	DatabaseMinConns    int32         `yaml:"database_min_conns"`
	DatabasePingTimeout time.Duration `yaml:"database_ping_timeout"`
	HealthInterval      time.Duration `yaml:"health_interval"`
	ShutdownTimeout     time.Duration `yaml:"shutdown_timeout"`
	WebAssetsDir        string        `yaml:"web_assets_dir"`
}

// Defaults returns safe non-sensitive defaults. It intentionally leaves the
// database URL empty so a process cannot silently connect to an unknown DB.
func Defaults() Config {
	return Config{
		AppName:             "zhixu",
		Version:             defaultVersion,
		Environment:         "development",
		HTTPAddr:            defaultHTTPAddr,
		DatabaseMaxConns:    defaultMaxConns,
		DatabaseMinConns:    defaultMinConns,
		DatabasePingTimeout: defaultPingTimeout,
		HealthInterval:      defaultHealthInterval,
		ShutdownTimeout:     defaultShutdownTimeout,
	}
}

// Load reads an optional YAML file and then applies environment overrides.
// Environment values are intentionally explicit and validated rather than
// silently falling back when malformed.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		if err := applyYAMLFile(path, &cfg); err != nil {
			return cfg, err
		}
	}
	if err := applyEnv(&cfg, os.LookupEnv); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// LoadWithLookup is exposed for deterministic unit tests without mutating the
// process environment.
func LoadWithLookup(path string, lookup func(string) (string, bool)) (Config, error) {
	cfg := Defaults()
	if path != "" {
		if err := applyYAMLFile(path, &cfg); err != nil {
			return cfg, err
		}
	}
	if err := applyEnv(&cfg, lookup); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// fileConfig keeps YAML duration values as strings so their parsing is
// explicit and consistent across yaml.v3 versions. Pointer fields preserve
// defaults when a YAML key is omitted.
type fileConfig struct {
	AppName             *string `yaml:"app_name"`
	Version             *string `yaml:"version"`
	Environment         *string `yaml:"environment"`
	HTTPAddr            *string `yaml:"http_addr"`
	DatabaseURL         *string `yaml:"database_url"`
	DatabaseHost        *string `yaml:"database_host"`
	DatabasePort        *string `yaml:"database_port"`
	DatabaseName        *string `yaml:"database_name"`
	DatabaseUser        *string `yaml:"database_user"`
	DatabasePassword    *string `yaml:"database_password"`
	DatabaseMaxConns    *int32  `yaml:"database_max_conns"`
	DatabaseMinConns    *int32  `yaml:"database_min_conns"`
	DatabasePingTimeout *string `yaml:"database_ping_timeout"`
	HealthInterval      *string `yaml:"health_interval"`
	ShutdownTimeout     *string `yaml:"shutdown_timeout"`
	WebAssetsDir        *string `yaml:"web_assets_dir"`
}

func applyYAMLFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	var raw fileConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}
	if raw.AppName != nil {
		cfg.AppName = *raw.AppName
	}
	if raw.Version != nil {
		cfg.Version = *raw.Version
	}
	if raw.Environment != nil {
		cfg.Environment = *raw.Environment
	}
	if raw.HTTPAddr != nil {
		cfg.HTTPAddr = *raw.HTTPAddr
	}
	if raw.DatabaseURL != nil {
		cfg.DatabaseURL = *raw.DatabaseURL
	}
	if raw.DatabaseHost != nil {
		cfg.DatabaseHost = *raw.DatabaseHost
	}
	if raw.DatabasePort != nil {
		cfg.DatabasePort = *raw.DatabasePort
	}
	if raw.DatabaseName != nil {
		cfg.DatabaseName = *raw.DatabaseName
	}
	if raw.DatabaseUser != nil {
		cfg.DatabaseUser = *raw.DatabaseUser
	}
	if raw.DatabasePassword != nil {
		cfg.DatabasePassword = *raw.DatabasePassword
	}
	if raw.DatabaseMaxConns != nil {
		cfg.DatabaseMaxConns = *raw.DatabaseMaxConns
	}
	if raw.DatabaseMinConns != nil {
		cfg.DatabaseMinConns = *raw.DatabaseMinConns
	}
	if raw.WebAssetsDir != nil {
		cfg.WebAssetsDir = *raw.WebAssetsDir
	}
	for name, value := range map[string]*string{
		"database_ping_timeout": raw.DatabasePingTimeout,
		"health_interval":       raw.HealthInterval,
		"shutdown_timeout":      raw.ShutdownTimeout,
	} {
		if value == nil {
			continue
		}
		parsed, err := time.ParseDuration(strings.TrimSpace(*value))
		if err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		switch name {
		case "database_ping_timeout":
			cfg.DatabasePingTimeout = parsed
		case "health_interval":
			cfg.HealthInterval = parsed
		case "shutdown_timeout":
			cfg.ShutdownTimeout = parsed
		}
	}
	return nil
}

// Validate checks process settings that must be valid before starting a
// server. An empty DatabaseURL is allowed so API readiness can report a real
// degraded state; Worker startup separately calls ValidateDatabase.
func (c Config) Validate() error {
	if strings.TrimSpace(c.AppName) == "" {
		return errors.New("app_name must not be empty")
	}
	if strings.TrimSpace(c.Version) == "" {
		return errors.New("version must not be empty")
	}
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return errors.New("http_addr must not be empty")
	}
	if c.DatabaseMaxConns < 0 || c.DatabaseMinConns < 0 {
		return errors.New("database pool sizes must not be negative")
	}
	if c.DatabaseMaxConns > 0 && c.DatabaseMinConns > c.DatabaseMaxConns {
		return errors.New("database_min_conns must not exceed database_max_conns")
	}
	if c.DatabasePingTimeout <= 0 {
		return errors.New("database_ping_timeout must be positive")
	}
	if c.HealthInterval <= 0 {
		return errors.New("health_interval must be positive")
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("shutdown_timeout must be positive")
	}
	return nil
}

// ValidateDatabase returns the configuration error that prevents a real DB
// readiness check. A URL is parsed here so malformed configuration is a
// startup/configuration failure rather than a retryable dependency outage.
func (c Config) ValidateDatabase() error {
	if strings.TrimSpace(c.DatabaseURL) != "" {
		if _, err := url.Parse(c.DatabaseURL); err != nil {
			return fmt.Errorf("database_url is invalid: %w", err)
		}
		return nil
	}
	if strings.TrimSpace(c.DatabaseHost) == "" || strings.TrimSpace(c.DatabaseName) == "" || strings.TrimSpace(c.DatabaseUser) == "" || c.DatabasePassword == "" {
		return errors.New("database_url or database_host/database_name/database_user/database_password is not configured")
	}
	return nil
}

// DatabaseConnectionString returns the explicit URL or safely builds one from
// discrete connection settings so reserved characters in passwords are escaped.
func (c Config) DatabaseConnectionString() (string, error) {
	if strings.TrimSpace(c.DatabaseURL) != "" {
		if err := c.ValidateDatabase(); err != nil {
			return "", err
		}
		return c.DatabaseURL, nil
	}
	if err := c.ValidateDatabase(); err != nil {
		return "", err
	}
	port := strings.TrimSpace(c.DatabasePort)
	if port == "" {
		port = "5432"
	}
	databaseURL := &url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(strings.TrimSpace(c.DatabaseHost), port),
		Path:   "/" + strings.TrimSpace(c.DatabaseName),
	}
	databaseURL.User = url.UserPassword(strings.TrimSpace(c.DatabaseUser), c.DatabasePassword)
	query := databaseURL.Query()
	query.Set("sslmode", "disable")
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String(), nil
}

func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	values := map[string]*string{
		"ZHIXU_APP_NAME":          &cfg.AppName,
		"ZHIXU_VERSION":           &cfg.Version,
		"ZHIXU_ENVIRONMENT":       &cfg.Environment,
		"ZHIXU_HTTP_ADDR":         &cfg.HTTPAddr,
		"ZHIXU_DATABASE_URL":      &cfg.DatabaseURL,
		"ZHIXU_DATABASE_HOST":     &cfg.DatabaseHost,
		"ZHIXU_DATABASE_PORT":     &cfg.DatabasePort,
		"ZHIXU_DATABASE_NAME":     &cfg.DatabaseName,
		"ZHIXU_DATABASE_USER":     &cfg.DatabaseUser,
		"ZHIXU_DATABASE_PASSWORD": &cfg.DatabasePassword,
		"ZHIXU_WEB_ASSETS_DIR":    &cfg.WebAssetsDir,
	}
	for key, target := range values {
		if value, ok := lookup(key); ok {
			*target = value
		}
	}

	if value, ok := lookup("ZHIXU_DATABASE_MAX_CONNS"); ok {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return fmt.Errorf("parse %s: %w", "ZHIXU_DATABASE_MAX_CONNS", err)
		}
		cfg.DatabaseMaxConns = int32(parsed)
	}
	if value, ok := lookup("ZHIXU_DATABASE_MIN_CONNS"); ok {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return fmt.Errorf("parse %s: %w", "ZHIXU_DATABASE_MIN_CONNS", err)
		}
		cfg.DatabaseMinConns = int32(parsed)
	}
	for key, target := range map[string]*time.Duration{
		"ZHIXU_DATABASE_PING_TIMEOUT": &cfg.DatabasePingTimeout,
		"ZHIXU_HEALTH_INTERVAL":       &cfg.HealthInterval,
		"ZHIXU_SHUTDOWN_TIMEOUT":      &cfg.ShutdownTimeout,
	} {
		if value, ok := lookup(key); ok {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("parse %s: %w", key, err)
			}
			*target = parsed
		}
	}
	return nil
}
