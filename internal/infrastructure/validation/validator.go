package validation

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/enzom/jungle-gaming/internal/application"
)

func New() (application.Validator, error) {
	validate := validator.New(validator.WithRequiredStructEnabled())
	if err := validate.RegisterValidation("notblank", func(field validator.FieldLevel) bool {
		return strings.TrimSpace(field.Field().String()) != ""
	}); err != nil {
		return nil, fmt.Errorf("register notblank validation: %w", err)
	}
	return validate, nil
}
