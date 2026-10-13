package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaskPhone(t *testing.T) {
	tests := []struct{ in, want string }{
		{"+254712345678", "+2547****5678"},
		{"+254722000001", "+2547****0001"},
		{"", ""},
		{"+2547", "*****"},
		{"123456789", "*********"},   // exactly head+tail characters: nothing in the middle to show, hide it all
		{"1234567890", "12345*7890"}, // one hidden character
		{"short", "*****"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, MaskPhone(tc.in))
			assert.Len(t, MaskPhone(tc.in), len(tc.in), "the length is preserved")
		})
	}
}

func TestScrubPhones(t *testing.T) {
	assert.Equal(t,
		`ERROR: duplicate key value violates unique constraint "riders_phone_key" Key (phone)=(+2547****0001) already exists`,
		ScrubPhones(`ERROR: duplicate key value violates unique constraint "riders_phone_key" Key (phone)=(+254722000001) already exists`))
	assert.Equal(t, "from +2547****5678 to +2547****0001", ScrubPhones("from +254712345678 to +254722000001"))
	assert.Equal(t, "no numbers here", ScrubPhones("no numbers here"))
	assert.Equal(t, "+25471234567", ScrubPhones("+25471234567"), "too short to be a phone number")
	assert.Equal(t, "", ScrubPhones(""))
}

func TestRequestIDInContext(t *testing.T) {
	assert.Equal(t, "", RequestID(context.Background()))
	assert.Equal(t, "abc-123", RequestID(WithRequestID(context.Background(), "abc-123")))
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(line, &m), string(line))
		out = append(out, m)
	}
	return out
}

func TestContextHandlerStampsTheRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewContextHandler(slog.NewJSONHandler(&buf, nil)))
	ctx := WithRequestID(context.Background(), "req-42")

	logger.InfoContext(ctx, "with an id", "k", "v")
	logger.Info("without an id")
	logger.With("static", 1).WithGroup("g").InfoContext(ctx, "derived loggers keep the wrapper")

	lines := decodeLines(t, &buf)
	require.Len(t, lines, 3)
	assert.Equal(t, "req-42", lines[0]["request_id"])
	assert.NotContains(t, lines[1], "request_id")
	assert.Equal(t, "req-42", lines[2]["g"].(map[string]any)["request_id"], "added inside the open group, as slog does")
	assert.Equal(t, float64(1), lines[2]["static"])
}

// CWE-117: whatever a client sends, it is encoded inside one attribute value and cannot start a new log line.
func TestHostileValuesCannotForgeALogLine(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewContextHandler(slog.NewJSONHandler(&buf, nil)))
	hostile := "ok\n{\"level\":\"ERROR\",\"msg\":\"forged\"}\r\n"

	logger.Info("request failed", "error", hostile, "route", "x\ty")

	lines := decodeLines(t, &buf)
	require.Len(t, lines, 1, "one line, however many newlines the value contains")
	assert.Equal(t, "request failed", lines[0]["msg"])
	assert.Equal(t, hostile, lines[0]["error"])
}
