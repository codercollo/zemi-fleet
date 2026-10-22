package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/domain"
)

func auditLines(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func TestAudit_BatchAcceptedIsLogged(t *testing.T) {
	h, store, logs := newStoreHandlerWithLogs(t)
	store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).Return(acceptedBatch("rider_002"), false, nil)

	rec := send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`, "X-Request-ID", "audit-accept-1")

	require.Equal(t, http.StatusOK, rec.Code)
	lines := auditLines(t, logs, "batch accepted")
	require.Len(t, lines, 1)
	assert.Equal(t, "INFO", lines[0]["level"])
	assert.Equal(t, "rider_002", lines[0]["rider_id"])
	assert.EqualValues(t, 2, lines[0]["orders"])
	assert.Equal(t, "audit-accept-1", lines[0]["request_id"], "the audit line can be tied to the request")
	assert.NotEmpty(t, lines[0]["batch_id"])
}

func TestAudit_ConflictAndCapacityRejectionAreLogged(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		wantMsg string
	}{
		{"lost the race (409)", fmt.Errorf("accept: %w", domain.ErrBatchTaken), http.StatusConflict, "batch accept conflict"},
		{"over capacity (422)", fmt.Errorf("accept: %w", domain.ErrRiderAtCapacity), http.StatusUnprocessableEntity, "batch accept rejected, rider at capacity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, store, logs := newStoreHandlerWithLogs(t)
			store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).Return(domain.Batch{}, false, tc.err)

			rec := send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`, "X-Request-ID", "audit-2")

			require.Equal(t, tc.status, rec.Code)
			lines := auditLines(t, logs, tc.wantMsg)
			require.Len(t, lines, 1)
			assert.Equal(t, "INFO", lines[0]["level"], "an expected business outcome, not an error")
			assert.Equal(t, "batch_X1", lines[0]["batch_id"])
			assert.Equal(t, "rider_002", lines[0]["rider_id"])
			assert.Equal(t, "audit-2", lines[0]["request_id"])
			assert.Empty(t, auditLines(t, logs, "batch accepted"))
		})
	}
}

func TestAudit_OtherAcceptFailuresAreNotAuditLines(t *testing.T) {
	for _, err := range []error{domain.ErrNotFound, domain.ErrRiderUnavailable} {
		h, store, logs := newStoreHandlerWithLogs(t)
		store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).Return(domain.Batch{}, false, err)

		send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`)

		for _, msg := range []string{"batch accepted", "batch accept conflict", "batch accept rejected, rider at capacity"} {
			assert.Empty(t, auditLines(t, logs, msg), "%v must not produce %q", err, msg)
		}
	}
}

// A malformed ID can never reach a log line as text: it is rejected first, and attributes are encoded anyway.
func TestAudit_ValuesAreAttributesNotMessageText(t *testing.T) {
	h, _, logs := newStoreHandlerWithLogs(t)

	send(t, h, http.MethodPost, acceptPath("batch_1"), `{"rider_id":"rider_1\n{\"msg\":\"forged\"}"}`)

	assert.NotContains(t, logs.String(), "forged\"}\n")
	for _, raw := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m), "each line is valid JSON: %s", raw)
		assert.Equal(t, "request", m["msg"])
	}
}
