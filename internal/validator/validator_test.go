package validator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidator(t *testing.T) {
	t.Run("new validator is valid", func(t *testing.T) {
		assert.True(t, New().Valid())
	})

	t.Run("check records error only when not ok", func(t *testing.T) {
		v := New()
		v.Check(true, "a", "should not appear")
		v.Check(false, "b", "bad b")

		assert.False(t, v.Valid())
		assert.NotContains(t, v.Errors, "a")
		assert.Equal(t, "bad b", v.Errors["b"])
	})

	t.Run("first error for a key wins", func(t *testing.T) {
		v := New()
		v.AddError("k", "first")
		v.AddError("k", "second")

		assert.Equal(t, "first", v.Errors["k"])
	})
}

func TestPermittedValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"present", "pending", true},
		{"absent", "bogus", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PermittedValue(tc.value, "pending", "assigned"))
		})
	}
}

func TestUnique(t *testing.T) {
	tests := []struct {
		name   string
		values []int
		want   bool
	}{
		{"empty", nil, true},
		{"distinct", []int{1, 2, 3}, true},
		{"duplicate", []int{1, 2, 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Unique(tc.values))
		})
	}
}

func TestBetween(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  bool
	}{
		{"inside", 0, true},
		{"lower bound", -90, true},
		{"upper bound", 90, true},
		{"below", -90.1, false},
		{"above", 90.1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Between(tc.value, -90, 90))
		})
	}
}
