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

	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/operability"

	"gopkg.in/yaml.v3"
)

const (
	defaultHTTPAddr                   = "127.0.0.1:8080"
	defaultVersion                    = "dev"
	defaultPingTimeout                = 2 * time.Second
	defaultHealthInterval             = 15 * time.Second
	defaultShutdownTimeout            = 10 * time.Second
	defaultMaxConns                   = int32(10)
	defaultMinConns                   = int32(1)
	defaultWorkerQueue                = "workflow"
	defaultWorkerMaxWorkers           = 4
	defaultWorkerJobTimeout           = 15 * time.Minute
	defaultWorkerRescueStuckJobsAfter = 30 * time.Minute
	defaultWorkflowLeaseDuration      = 2 * time.Minute
	defaultWorkflowHeartbeatInterval  = 30 * time.Second
	defaultWorkerSoftStopTimeout      = 30 * time.Second
	defaultWorkerHardStopTimeout      = 60 * time.Second
	defaultWorkerHealthAddr           = "0.0.0.0:8081"
)

// TelemetryMode controls whether telemetry export is disabled or required for
// process readiness.
type TelemetryMode string

const (
	// TelemetryModeDisabled prevents exporter construction.
	TelemetryModeDisabled TelemetryMode = "disabled"
	// TelemetryModeOptional enables export without making exporter availability
	// a readiness requirement.
	TelemetryModeOptional TelemetryMode = "optional"
	// TelemetryModeRequired makes exporter availability a readiness requirement.
	TelemetryModeRequired TelemetryMode = "required"
)

// Config contains process settings, including connection secrets. Callers must
// use String or GoString rather than serializing the struct for diagnostics.
type Config struct {
	AppName                    string        `yaml:"app_name"`
	Version                    string        `yaml:"version"`
	Environment                string        `yaml:"environment"`
	HTTPAddr                   string        `yaml:"http_addr"`
	DatabaseURL                string        `yaml:"database_url"`
	DatabaseHost               string        `yaml:"database_host"`
	DatabasePort               string        `yaml:"database_port"`
	DatabaseName               string        `yaml:"database_name"`
	DatabaseUser               string        `yaml:"database_user"`
	DatabasePassword           string        `yaml:"database_password"`
	DatabaseMaxConns           int32         `yaml:"database_max_conns"`
	DatabaseMinConns           int32         `yaml:"database_min_conns"`
	DatabasePingTimeout        time.Duration `yaml:"database_ping_timeout"`
	HealthInterval             time.Duration `yaml:"health_interval"`
	ShutdownTimeout            time.Duration `yaml:"shutdown_timeout"`
	WebAssetsDir               string        `yaml:"web_assets_dir"`
	WorkerQueue                string        `yaml:"worker_queue"`
	WorkerMaxWorkers           int           `yaml:"worker_max_workers"`
	WorkerJobTimeout           time.Duration `yaml:"worker_job_timeout"`
	WorkerRescueStuckJobsAfter time.Duration `yaml:"worker_rescue_stuck_jobs_after"`
	WorkflowLeaseDuration      time.Duration `yaml:"workflow_lease"`
	WorkflowHeartbeatInterval  time.Duration `yaml:"workflow_heartbeat"`
	WorkerSoftStopTimeout      time.Duration `yaml:"worker_soft_stop_timeout"`
	WorkerHardStopTimeout      time.Duration `yaml:"worker_hard_stop_timeout"`
	WorkerHealthAddr           string        `yaml:"worker_health_addr"`
	TelemetryMode              TelemetryMode `yaml:"telemetry_mode"`
	TelemetryEndpoint          string        `yaml:"telemetry_endpoint"`
}

// Defaults returns safe non-sensitive defaults. It intentionally leaves the
// database URL empty so a process cannot silently connect to an unknown DB.
func Defaults() Config {
	return Config{
		AppName:                    "zhixu",
		Version:                    defaultVersion,
		Environment:                "development",
		HTTPAddr:                   defaultHTTPAddr,
		DatabaseMaxConns:           defaultMaxConns,
		DatabaseMinConns:           defaultMinConns,
		DatabasePingTimeout:        defaultPingTimeout,
		HealthInterval:             defaultHealthInterval,
		ShutdownTimeout:            defaultShutdownTimeout,
		WorkerQueue:                defaultWorkerQueue,
		WorkerMaxWorkers:           defaultWorkerMaxWorkers,
		WorkerJobTimeout:           defaultWorkerJobTimeout,
		WorkerRescueStuckJobsAfter: defaultWorkerRescueStuckJobsAfter,
		WorkflowLeaseDuration:      defaultWorkflowLeaseDuration,
		WorkflowHeartbeatInterval:  defaultWorkflowHeartbeatInterval,
		WorkerSoftStopTimeout:      defaultWorkerSoftStopTimeout,
		WorkerHardStopTimeout:      defaultWorkerHardStopTimeout,
		WorkerHealthAddr:           defaultWorkerHealthAddr,
		TelemetryMode:              TelemetryModeDisabled,
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
	AppName                    *string        `yaml:"app_name"`
	Version                    *string        `yaml:"version"`
	Environment                *string        `yaml:"environment"`
	HTTPAddr                   *string        `yaml:"http_addr"`
	DatabaseURL                *string        `yaml:"database_url"`
	DatabaseHost               *string        `yaml:"database_host"`
	DatabasePort               *string        `yaml:"database_port"`
	DatabaseName               *string        `yaml:"database_name"`
	DatabaseUser               *string        `yaml:"database_user"`
	DatabasePassword           *string        `yaml:"database_password"`
	DatabaseMaxConns           *int32         `yaml:"database_max_conns"`
	DatabaseMinConns           *int32         `yaml:"database_min_conns"`
	DatabasePingTimeout        *string        `yaml:"database_ping_timeout"`
	HealthInterval             *string        `yaml:"health_interval"`
	ShutdownTimeout            *string        `yaml:"shutdown_timeout"`
	WebAssetsDir               *string        `yaml:"web_assets_dir"`
	WorkerQueue                *string        `yaml:"worker_queue"`
	WorkerMaxWorkers           *int           `yaml:"worker_max_workers"`
	WorkerJobTimeout           *string        `yaml:"worker_job_timeout"`
	WorkerRescueStuckJobsAfter *string        `yaml:"worker_rescue_stuck_jobs_after"`
	WorkflowLeaseDuration      *string        `yaml:"workflow_lease"`
	WorkflowHeartbeatInterval  *string        `yaml:"workflow_heartbeat"`
	WorkerSoftStopTimeout      *string        `yaml:"worker_soft_stop_timeout"`
	WorkerHardStopTimeout      *string        `yaml:"worker_hard_stop_timeout"`
	WorkerHealthAddr           *string        `yaml:"worker_health_addr"`
	TelemetryMode              *TelemetryMode `yaml:"telemetry_mode"`
	TelemetryEndpoint          *string        `yaml:"telemetry_endpoint"`
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
	if raw.WorkerQueue != nil {
		cfg.WorkerQueue = *raw.WorkerQueue
	}
	if raw.WorkerMaxWorkers != nil {
		cfg.WorkerMaxWorkers = *raw.WorkerMaxWorkers
	}
	if raw.WorkerHealthAddr != nil {
		cfg.WorkerHealthAddr = *raw.WorkerHealthAddr
	}
	if raw.TelemetryMode != nil {
		cfg.TelemetryMode = *raw.TelemetryMode
	}
	if raw.TelemetryEndpoint != nil {
		cfg.TelemetryEndpoint = *raw.TelemetryEndpoint
	}
	for name, value := range map[string]*string{
		"database_ping_timeout":          raw.DatabasePingTimeout,
		"health_interval":                raw.HealthInterval,
		"shutdown_timeout":               raw.ShutdownTimeout,
		"worker_job_timeout":             raw.WorkerJobTimeout,
		"worker_rescue_stuck_jobs_after": raw.WorkerRescueStuckJobsAfter,
		"workflow_lease":                 raw.WorkflowLeaseDuration,
		"workflow_heartbeat":             raw.WorkflowHeartbeatInterval,
		"worker_soft_stop_timeout":       raw.WorkerSoftStopTimeout,
		"worker_hard_stop_timeout":       raw.WorkerHardStopTimeout,
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
		case "worker_job_timeout":
			cfg.WorkerJobTimeout = parsed
		case "worker_rescue_stuck_jobs_after":
			cfg.WorkerRescueStuckJobsAfter = parsed
		case "workflow_lease":
			cfg.WorkflowLeaseDuration = parsed
		case "workflow_heartbeat":
			cfg.WorkflowHeartbeatInterval = parsed
		case "worker_soft_stop_timeout":
			cfg.WorkerSoftStopTimeout = parsed
		case "worker_hard_stop_timeout":
			cfg.WorkerHardStopTimeout = parsed
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
	if err := operability.ValidateRiverOptions(c.WorkerQueue, c.WorkerMaxWorkers, c.WorkerJobTimeout, c.WorkerRescueStuckJobsAfter, c.WorkerSoftStopTimeout); err != nil {
		return err
	}
	if c.WorkflowLeaseDuration <= 0 {
		return errors.New("workflow_lease must be positive")
	}
	if c.WorkflowHeartbeatInterval <= 0 {
		return errors.New("workflow_heartbeat must be positive")
	}
	if c.WorkflowHeartbeatInterval > (c.WorkflowLeaseDuration-time.Nanosecond)/3 {
		return errors.New("workflow_heartbeat must be less than one third of workflow_lease")
	}
	if c.WorkerHardStopTimeout <= 0 {
		return errors.New("worker_hard_stop_timeout must be positive")
	}
	if c.WorkerSoftStopTimeout >= c.WorkerHardStopTimeout {
		return errors.New("worker_soft_stop_timeout must be less than worker_hard_stop_timeout")
	}
	if err := validateListenAddress(c.WorkerHealthAddr); err != nil {
		return err
	}
	if err := c.validateTelemetry(); err != nil {
		return err
	}
	return nil
}

func validateListenAddress(address string) error {
	_, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return errors.New("worker_health_addr must be a valid host:port address")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return errors.New("worker_health_addr port must be between 1 and 65535")
	}
	return nil
}

func (c Config) validateTelemetry() error {
	switch c.TelemetryMode {
	case TelemetryModeDisabled:
		if strings.TrimSpace(c.TelemetryEndpoint) != "" {
			return errors.New("telemetry_endpoint must be empty when telemetry_mode is disabled")
		}
		return nil
	case TelemetryModeOptional, TelemetryModeRequired:
		if strings.TrimSpace(c.TelemetryEndpoint) == "" {
			return errors.New("telemetry_endpoint is required when telemetry_mode is optional or required")
		}
		endpoint, err := url.Parse(c.TelemetryEndpoint)
		if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
			return errors.New("telemetry_endpoint must be a valid absolute URL")
		}
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return errors.New("telemetry_endpoint scheme must be http or https")
		}
		return nil
	default:
		return errors.New("telemetry_mode must be disabled, optional, or required")
	}
}

// ValidateDatabase returns the configuration error that prevents a real DB
// readiness check. A URL is parsed here so malformed configuration is a
// startup/configuration failure rather than a retryable dependency outage.
func (c Config) ValidateDatabase() error {
	if strings.TrimSpace(c.DatabaseURL) != "" {
		if _, err := url.Parse(c.DatabaseURL); err != nil {
			return errors.New("database_url is invalid")
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

// String returns a non-sensitive summary suitable for diagnostics. Connection
// credentials and exporter endpoints are intentionally omitted.
func (c Config) String() string {
	return fmt.Sprintf(
		"Config{AppName:%q Version:%q Environment:%q HTTPAddr:%q DatabaseConfigured:%t DatabaseMaxConns:%d DatabaseMinConns:%d DatabasePingTimeout:%s HealthInterval:%s ShutdownTimeout:%s WebAssetsDir:%q WorkerQueue:%q WorkerMaxWorkers:%d WorkerJobTimeout:%s WorkerRescueStuckJobsAfter:%s WorkflowLeaseDuration:%s WorkflowHeartbeatInterval:%s WorkerSoftStopTimeout:%s WorkerHardStopTimeout:%s WorkerHealthAddr:%q TelemetryMode:%q TelemetryConfigured:%t}",
		c.AppName,
		c.Version,
		c.Environment,
		c.HTTPAddr,
		strings.TrimSpace(c.DatabaseURL) != "" || strings.TrimSpace(c.DatabaseHost) != "",
		c.DatabaseMaxConns,
		c.DatabaseMinConns,
		c.DatabasePingTimeout,
		c.HealthInterval,
		c.ShutdownTimeout,
		c.WebAssetsDir,
		c.WorkerQueue,
		c.WorkerMaxWorkers,
		c.WorkerJobTimeout,
		c.WorkerRescueStuckJobsAfter,
		c.WorkflowLeaseDuration,
		c.WorkflowHeartbeatInterval,
		c.WorkerSoftStopTimeout,
		c.WorkerHardStopTimeout,
		c.WorkerHealthAddr,
		c.TelemetryMode,
		strings.TrimSpace(c.TelemetryEndpoint) != "",
	)
}

// GoString applies the same secret-safe representation to %#v formatting.
func (c Config) GoString() string {
	return c.String()
}

func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	values := map[string]*string{
		"ZHIXU_APP_NAME":           &cfg.AppName,
		"ZHIXU_VERSION":            &cfg.Version,
		"ZHIXU_ENVIRONMENT":        &cfg.Environment,
		"ZHIXU_HTTP_ADDR":          &cfg.HTTPAddr,
		"ZHIXU_DATABASE_URL":       &cfg.DatabaseURL,
		"ZHIXU_DATABASE_HOST":      &cfg.DatabaseHost,
		"ZHIXU_DATABASE_PORT":      &cfg.DatabasePort,
		"ZHIXU_DATABASE_NAME":      &cfg.DatabaseName,
		"ZHIXU_DATABASE_USER":      &cfg.DatabaseUser,
		"ZHIXU_DATABASE_PASSWORD":  &cfg.DatabasePassword,
		"ZHIXU_WEB_ASSETS_DIR":     &cfg.WebAssetsDir,
		"ZHIXU_WORKER_QUEUE":       &cfg.WorkerQueue,
		"ZHIXU_WORKER_HEALTH_ADDR": &cfg.WorkerHealthAddr,
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
	if value, ok := lookup("ZHIXU_WORKER_MAX_WORKERS"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse %s: %w", "ZHIXU_WORKER_MAX_WORKERS", err)
		}
		cfg.WorkerMaxWorkers = parsed
	}
	for key, target := range map[string]*time.Duration{
		"ZHIXU_DATABASE_PING_TIMEOUT":     &cfg.DatabasePingTimeout,
		"ZHIXU_HEALTH_INTERVAL":           &cfg.HealthInterval,
		"ZHIXU_SHUTDOWN_TIMEOUT":          &cfg.ShutdownTimeout,
		"ZHIXU_WORKER_JOB_TIMEOUT":        &cfg.WorkerJobTimeout,
		"ZHIXU_WORKER_RESCUE_STUCK_AFTER": &cfg.WorkerRescueStuckJobsAfter,
		"ZHIXU_WORKFLOW_LEASE":            &cfg.WorkflowLeaseDuration,
		"ZHIXU_WORKFLOW_HEARTBEAT":        &cfg.WorkflowHeartbeatInterval,
		"ZHIXU_WORKER_SOFT_STOP_TIMEOUT":  &cfg.WorkerSoftStopTimeout,
		"ZHIXU_WORKER_HARD_STOP_TIMEOUT":  &cfg.WorkerHardStopTimeout,
	} {
		if value, ok := lookup(key); ok {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("parse %s: %w", key, err)
			}
			*target = parsed
		}
	}
	if value, ok := lookup("ZHIXU_TELEMETRY_MODE"); ok {
		cfg.TelemetryMode = TelemetryMode(value)
		if cfg.TelemetryMode == TelemetryModeDisabled {
			cfg.TelemetryEndpoint = ""
		}
	}
	if cfg.TelemetryMode != TelemetryModeDisabled {
		if value, ok := lookup("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
			cfg.TelemetryEndpoint = value
		}
	}
	return nil
}
