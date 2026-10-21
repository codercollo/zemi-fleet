// Package webhooks contains the provider-independent delivery policy for
// signed outbox events. Database adapters can implement Store without changing
// retry, signing, or SSRF rules.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Event struct {
	ID      string
	Type    string
	Payload json.RawMessage
}
type Delivery struct {
	ID            string
	Event         Event
	URL           string
	Secret        string
	Attempts      int
	NextAttemptAt time.Time
}
type FailedDelivery struct {
	ID, EventID, EventType, SubscriptionID, ClientName string
	Attempts                                           int
	LastStatus                                         *int
	NextAttemptAt                                      time.Time
}
type Store interface {
	ClaimDue(context.Context, time.Time) (*Delivery, error)
	MarkDelivered(context.Context, string, time.Time) error
	MarkRetry(context.Context, string, time.Time, int, int) error
	MarkFailed(context.Context, string, time.Time, int) error
	ListFailed(context.Context, int, int) ([]FailedDelivery, error)
	Retry(context.Context, string, time.Time) error
}
type Sender interface {
	Send(context.Context, Delivery) (int, error)
}

var RetryAfter = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 24 * time.Hour}

type Dispatcher struct {
	Store   Store
	Sender  Sender
	Now     func() time.Time
	Enabled bool
	// Record is called only after the delivery state change commits.
	Record func(string)
}

func (d Dispatcher) RunOnce(ctx context.Context) error {
	if !d.Enabled {
		return nil
	}
	now := d.Now()
	item, err := d.Store.ClaimDue(ctx, now)
	if err != nil || item == nil {
		return err
	}
	status, sendErr := d.Sender.Send(ctx, *item)
	if sendErr == nil && status >= 200 && status < 300 {
		err := d.Store.MarkDelivered(ctx, item.ID, now)
		if err == nil && d.Record != nil {
			d.Record("delivered")
		}
		return err
	}
	if sendErr == nil && !IsRetryableStatus(status) {
		err := d.Store.MarkFailed(ctx, item.ID, now, status)
		if err == nil && d.Record != nil {
			d.Record("failed")
		}
		return err
	}
	if item.Attempts >= len(RetryAfter) {
		err := d.Store.MarkFailed(ctx, item.ID, now, status)
		if err == nil && d.Record != nil {
			d.Record("failed")
		}
		return err
	}
	err = d.Store.MarkRetry(ctx, item.ID, now.Add(RetryAfter[item.Attempts]), item.Attempts+1, status)
	if err == nil && d.Record != nil {
		d.Record("retry")
	}
	return err
}

type HTTPSender struct{ Client *http.Client }

func (s HTTPSender) Send(ctx context.Context, d Delivery) (int, error) {
	if err := ValidateURL(d.URL); err != nil {
		return 0, err
	}
	sig := Sign(d.Secret, d.Event)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(d.Event.Payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Zemi-Event-ID", d.Event.ID)
	req.Header.Set("X-Zemi-Signature", sig)
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

func Sign(secret string, e Event) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(e.Payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("webhook URL must use https")
	}
	if strings.EqualFold(u.Hostname(), "localhost") || isBlockedIP(net.ParseIP(u.Hostname())) {
		return errors.New("webhook URL resolves to a private or local address")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("resolve webhook host: %w", err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return errors.New("webhook URL resolves to a private or local address")
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
func IsRetryableStatus(status int) bool {
	return status == 0 || status == http.StatusRequestTimeout || status >= 500
}
func NormaliseClientName(s string) bool { return strings.TrimSpace(s) != "" }
