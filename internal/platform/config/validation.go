package config

import (
	"errors"
	"strings"

	"github.com/go-playground/validator/v10"
)

type configValidator struct {
	engine *validator.Validate
}

func newConfigValidator() (*configValidator, error) {
	validate := validator.New(validator.WithRequiredStructEnabled())
	if err := validate.RegisterValidation("notblank", func(field validator.FieldLevel) bool {
		return strings.TrimSpace(field.Field().String()) != ""
	}); err != nil {
		return nil, errors.New("initialize config validator")
	}
	return &configValidator{engine: validate}, nil
}

func (v *configValidator) validate(cfg Config) error {
	if err := v.engine.Struct(cfg); err != nil {
		var validationErrors validator.ValidationErrors
		if !errors.As(err, &validationErrors) {
			return errors.New("validate config fields")
		}
		failed := make(map[string]struct{}, len(validationErrors))
		for _, fieldError := range validationErrors {
			failed[fieldError.StructField()] = struct{}{}
		}
		for _, rule := range primitiveValidationErrors {
			for _, field := range rule.fields {
				if _, ok := failed[field]; ok {
					return errors.New(rule.message)
				}
			}
		}
		return errors.New("validate config fields")
	}
	return nil
}

var primitiveValidationErrors = []struct {
	fields  []string
	message string
}{
	{fields: []string{"AppName"}, message: "app_name must not be empty"},
	{fields: []string{"Version"}, message: "version must not be empty"},
	{fields: []string{"HTTPAddr"}, message: "http_addr must not be empty"},
	{fields: []string{"DatabaseMaxConns", "DatabaseMinConns"}, message: "database pool sizes must not be negative"},
	{fields: []string{"DatabasePingTimeout"}, message: "database_ping_timeout must be positive"},
	{fields: []string{"GraphQueryTimeout"}, message: "graph_query_timeout must be positive"},
	{fields: []string{"HealthInterval"}, message: "health_interval must be positive"},
	{fields: []string{"ShutdownTimeout"}, message: "shutdown_timeout must be positive"},
}
