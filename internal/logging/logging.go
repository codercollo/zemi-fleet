// Package logging holds the small pieces structured logging needs: the request ID carried in a context, a slog
// handler that stamps it on every record, and masking of phone numbers.
//
// The rule behind all of it (CWE-117, log injection): a log MESSAGE is always a fixed string written in the code,
// and anything a client controls (paths, IDs, headers, error text) goes into slog ATTRIBUTES, which the handler
// encodes (quotes, newlines and control characters are escaped), so a crafted value cannot forge a log line.
package logging

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

type requestIDKey struct{}

// WithRequestID returns a context carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID in ctx, or "" if there is none.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ContextHandler wraps a slog.Handler and adds a "request_id" attribute to every record whose context carries one,
// so any code that logs with the request's context (InfoContext, WarnContext, ...) is traceable without passing
// the ID around by hand.
type ContextHandler struct {
	slog.Handler
}

// NewContextHandler wraps h.
func NewContextHandler(h slog.Handler) *ContextHandler { return &ContextHandler{Handler: h} }

// Handle implements slog.Handler.
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler, keeping the wrapper.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler, keeping the wrapper.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithGroup(name)}
}

// maskChar replaces the hidden digits of a masked phone number.
const maskChar = "*"

// Digits kept visible at each end of a phone number: "+2547" (the country code and the first digit of the network
// prefix) and the last four.
const (
	phoneKeepHead = 5
	phoneKeepTail = 4
)

// MaskPhone hides the middle of a phone number for logs: +254712345678 becomes +2547****5678. Anything too short to
// keep both ends visible is masked completely.
func MaskPhone(phone string) string {
	if len(phone) <= phoneKeepHead+phoneKeepTail {
		return strings.Repeat(maskChar, len(phone))
	}
	middle := len(phone) - phoneKeepHead - phoneKeepTail
	return phone[:phoneKeepHead] + strings.Repeat(maskChar, middle) + phone[len(phone)-phoneKeepTail:]
}

// phonePattern matches a Kenyan number in international form, the only format the API accepts.
var phonePattern = regexp.MustCompile(`\+254[0-9]{9}`)

// ScrubPhones masks every phone number found inside free text, such as a database error ("Key (phone)=(+254722000001)
// already exists"), before the text is logged.
func ScrubPhones(text string) string {
	return phonePattern.ReplaceAllStringFunc(text, MaskPhone)
}
