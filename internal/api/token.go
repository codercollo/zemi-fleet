package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/codercollo/zemi-fleet/internal/service"
)

type tokenClaims struct {
	Subject   string   `json:"sub"`
	Scopes    []string `json:"scope"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
}
type tokenContextKey struct{}
type scopeContextKey struct{}

func tokenSubject(ctx context.Context) string {
	v, _ := ctx.Value(tokenContextKey{}).(string)
	return v
}
func tokenScopes(ctx context.Context) map[string]bool {
	v, _ := ctx.Value(scopeContextKey{}).(map[string]bool)
	return v
}

type tokenManager struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	ttl     time.Duration
}

func newTokenManager(key string, ttl time.Duration) *tokenManager {
	seed := sha256.Sum256([]byte(key))
	private := ed25519.NewKeyFromSeed(seed[:])
	return &tokenManager{private: private, public: private.Public().(ed25519.PublicKey), ttl: ttl}
}

func (m *tokenManager) issue(subject string, scopes []string, now time.Time) (string, error) {
	if subject == "" {
		return "", errors.New("token subject is required")
	}
	c := tokenClaims{Subject: subject, Scopes: scopes, IssuedAt: now.Unix(), ExpiresAt: now.Add(m.ttl).Unix()}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	header := []byte("v4.public.")
	sig := ed25519.Sign(m.private, pae(header, payload, nil, nil))
	return string(header) + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (m *tokenManager) verify(raw string, now time.Time) (tokenClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 4 || parts[0] != "v4" || parts[1] != "public" {
		return tokenClaims{}, errors.New("invalid token")
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(parts[2])
	sig, err2 := base64.RawURLEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(m.public, pae([]byte("v4.public."), payload, nil, nil), sig) {
		return tokenClaims{}, errors.New("invalid token")
	}
	var c tokenClaims
	if json.Unmarshal(payload, &c) != nil || c.Subject == "" || c.ExpiresAt <= now.Unix() || c.IssuedAt > now.Add(time.Minute).Unix() {
		return tokenClaims{}, errors.New("invalid or expired token")
	}
	return c, nil
}

func pae(parts ...[]byte) []byte {
	out := make([]byte, 8)
	for _, p := range parts {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(p)))
		out = append(out, n[:]...)
		out = append(out, p...)
	}
	return out
}

type IdentityRequest struct{ RiderID, FullName, Phone string }
type IdentityVerifier interface {
	Verify(context.Context, IdentityRequest) (string, error)
}

type developmentIdentityVerifier struct{ riders *service.RiderService }

func (v developmentIdentityVerifier) Verify(ctx context.Context, in IdentityRequest) (string, error) {
	if v.riders == nil {
		return "", errIdentityUnavailable
	}
	r, err := v.riders.Get(ctx, in.RiderID)
	if err != nil || r.FullName != in.FullName || r.Phone != in.Phone {
		return "", errIdentityUnavailable
	}
	return r.ID, nil
}

var errIdentityUnavailable = errors.New("identity verification is not configured")
