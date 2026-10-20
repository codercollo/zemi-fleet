package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTokenManagerIssuesPasetoAndRejectsTamperingAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	m := newTokenManager(strings.Repeat("s", 32), 15*time.Minute)
	token, err := m.issue("rider_001", []string{"batches:accept"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "v4.public.") {
		t.Fatalf("token prefix %q", token)
	}
	claims, err := m.verify(token, now)
	if err != nil || claims.Subject != "rider_001" || claims.Scopes[0] != "batches:accept" {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	bad := token[:len(token)-1] + "x"
	if _, err := m.verify(bad, now); err == nil {
		t.Fatal("tampered token was accepted")
	}
	if _, err := m.verify(token, now.Add(16*time.Minute)); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestTokenContextHelpers(t *testing.T) {
	ctx := context.WithValue(context.Background(), tokenContextKey{}, "rider_001")
	ctx = context.WithValue(ctx, scopeContextKey{}, map[string]bool{"riders:write": true})
	if tokenSubject(ctx) != "rider_001" || !tokenScopes(ctx)["riders:write"] {
		t.Fatal("token context not decoded")
	}
}
