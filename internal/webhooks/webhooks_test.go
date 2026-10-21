package webhooks

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

type fakeStore struct {
	d                          *Delivery
	delivered, failed, retried bool
	next                       time.Time
}

func (s *fakeStore) ClaimDue(context.Context, time.Time) (*Delivery, error) {
	d := s.d
	s.d = nil
	return d, nil
}
func (s *fakeStore) MarkDelivered(context.Context, string, time.Time) error {
	s.delivered = true
	return nil
}
func (s *fakeStore) MarkRetry(_ context.Context, _ string, next time.Time, _ int, _ int) error {
	s.retried = true
	s.next = next
	return nil
}
func (s *fakeStore) MarkFailed(context.Context, string, time.Time, int) error {
	s.failed = true
	return nil
}
func (s *fakeStore) ListFailed(context.Context, int, int) ([]FailedDelivery, error) { return nil, nil }
func (s *fakeStore) Retry(context.Context, string, time.Time) error                 { return nil }

type fakeSender struct {
	status int
	err    error
}

func (s fakeSender) Send(context.Context, Delivery) (int, error) { return s.status, s.err }

func TestDispatcherRetriesWithScheduleAndEventuallyFails(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(map[string]string{"id": "x"})
	s := &fakeStore{d: &Delivery{ID: "del_1", Event: Event{ID: "evt_1", Payload: payload}, Attempts: 0}}
	d := Dispatcher{Store: s, Sender: fakeSender{status: http.StatusBadGateway}, Now: func() time.Time { return now }, Enabled: true}
	if err := d.RunOnce(context.Background()); err != nil || !s.retried || !s.next.Equal(now.Add(time.Minute)) {
		t.Fatalf("err=%v retry=%v next=%v", err, s.retried, s.next)
	}
	s.d = &Delivery{ID: "del_1", Attempts: len(RetryAfter), Event: Event{ID: "evt_1", Payload: payload}}
	if err := d.RunOnce(context.Background()); err != nil || !s.failed {
		t.Fatalf("err=%v failed=%v", err, s.failed)
	}
}

func TestSignAndSSRFGuard(t *testing.T) {
	e := Event{ID: "evt_1", Payload: []byte(`{"ok":true}`)}
	if Sign("secret", e) == "" {
		t.Fatal("empty signature")
	}
	if err := ValidateURL("http://127.0.0.1/hook"); err == nil {
		t.Fatal("accepted non-https URL")
	}
	if err := ValidateURL("https://localhost/hook"); err == nil {
		t.Fatal("accepted loopback URL")
	}
	s := HTTPSender{Resolve: func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.8")}, nil }}
	if _, err := s.Send(context.Background(), Delivery{URL: "https://partner.example/hook", Event: e}); err == nil {
		t.Fatal("accepted a private address returned by the resolver")
	}
}
