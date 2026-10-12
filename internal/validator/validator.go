package validator

import (
	"cmp"
	"slices"
)

// Validator collects per-field validation errors.
// Let's Go Further ch. 4.5 (adapted: generics replace the book's per-type helpers).
type Validator struct {
	Errors map[string]string
}

// New returns a Validator with an empty error map.
func New() *Validator {
	return &Validator{Errors: make(map[string]string)}
}

// Valid reports whether no errors were recorded.
func (v *Validator) Valid() bool {
	return len(v.Errors) == 0
}

// AddError records a message for key unless one already exists, so the
// first failure for a field wins.
func (v *Validator) AddError(key, message string) {
	if _, exists := v.Errors[key]; !exists {
		v.Errors[key] = message
	}
}

// Check records the error only when ok is false.
func (v *Validator) Check(ok bool, key, message string) {
	if !ok {
		v.AddError(key, message)
	}
}

// PermittedValue reports whether value is one of permitted.
func PermittedValue[T comparable](value T, permitted ...T) bool {
	return slices.Contains(permitted, value)
}

// Unique reports whether every value in the slice is distinct.
func Unique[T comparable](values []T) bool {
	seen := make(map[T]struct{}, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			return false
		}
		seen[v] = struct{}{}
	}
	return true
}

// Between reports whether min <= value <= max (inclusive).
func Between[T cmp.Ordered](value, min, max T) bool {
	return value >= min && value <= max
}
