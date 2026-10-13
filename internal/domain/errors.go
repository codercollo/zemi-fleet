// Package domain holds the types and errors shared by every layer. It imports nothing from the project.
package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors. The API layer maps each one to an HTTP status in a single place
// (internal/api/errors.go): not found -> 404, edit conflict and batch taken -> 409,
// rider at capacity and validation failures -> 422.
var (
	ErrNotFound        = errors.New("record not found")
	ErrEditConflict    = errors.New("edit conflict")
	ErrRiderAtCapacity = errors.New("rider is at capacity")
	ErrBatchTaken      = errors.New("batch has already been accepted")
)

// ValidationError carries per-field messages for checks that need the database
// (does this category exist?), so they cannot run in the handler.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, e.Fields[k]))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}
