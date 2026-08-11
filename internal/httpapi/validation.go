package httpapi

import "github.com/go-playground/validator/v10"

var requestValidator = validator.New(validator.WithRequiredStructEnabled())

// Validate applies declarative HTTP DTO constraints after strict decoding.
func Validate(value any) error {
	return requestValidator.Struct(value)
}
