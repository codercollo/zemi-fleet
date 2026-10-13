package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DemoAPIKey is the key docker-compose.yml falls back to so the reviewer path works out of the box, and the one in
// the README and the Postman environment. It is PUBLISHED, so production refuses to start with it (CWE-798).
const DemoAPIKey = "zemi-demo-key-not-for-production-0001"

// Key length floors: a development key may be short, anything deployed must be long enough to resist guessing.
const (
	minAPIKeyLenDevelopment = 16
	minAPIKeyLenDeployed    = 32
	maxAPIKeyNameLen        = 32
)

// apiKeyNamePattern is what a client name may look like: 1 to 32 characters of [a-z0-9_-].
var apiKeyNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// APIKey is one client allowed to call the API. Only the SHA-256 hash of the key is kept; the raw key exists only
// while API_KEYS is being parsed.
type APIKey struct {
	Name string
	Hash [sha256.Size]byte
}

// HashAPIKey returns the SHA-256 of a key. Hashing both sides to a fixed length lets the middleware compare in
// constant time whatever the length of what the client sent.
func HashAPIKey(key string) [sha256.Size]byte { return sha256.Sum256([]byte(key)) }

// NewAPIKey builds an APIKey from a name and a raw key (for hand-built configs in tests).
func NewAPIKey(name, key string) APIKey { return APIKey{Name: name, Hash: HashAPIKey(key)} }

// Fingerprint is the first 8 hex characters of the SHA-256 of a key: enough to tell two keys apart in a log, far too
// little to recover or guess one. It is what failed-authentication log lines carry instead of the key.
func Fingerprint(key string) string {
	h := HashAPIKey(key)
	return hex.EncodeToString(h[:4])
}

// parseAPIKeys turns "name=key,name=key" into hashed keys and reports every problem it finds. env decides the
// minimum key length and whether the published demo keys are refused.
func parseAPIKeys(raw, env string) ([]APIKey, []error) {
	var errs []error
	minLen := minAPIKeyLenDevelopment
	if env == EnvStaging || env == EnvProduction {
		minLen = minAPIKeyLenDeployed
	}

	var keys []APIKey
	names := map[string]bool{}
	hashes := map[[sha256.Size]byte]bool{}
	for i, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		// Cut at the first "=": a key may itself contain "=" (base64 padding).
		name, key, ok := strings.Cut(pair, "=")
		if !ok {
			// Never echo the entry: without an "=" it is probably a bare key.
			errs = append(errs, fmt.Errorf("API_KEYS entry %d must look like name=key", i+1))
			continue
		}
		if !apiKeyNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("API_KEYS name %q must be 1-%d characters of a-z, 0-9, _ or -", name, maxAPIKeyNameLen))
			continue
		}
		if len(key) < minLen {
			errs = append(errs, fmt.Errorf("API_KEYS key for %q must be at least %d characters in %s", name, minLen, env))
			continue
		}
		if env == EnvProduction && key == DemoAPIKey {
			errs = append(errs, fmt.Errorf("API_KEYS key for %q is the published demo key and is refused in production", name))
		}
		h := HashAPIKey(key)
		if names[name] {
			errs = append(errs, fmt.Errorf("API_KEYS name %q is used twice", name))
		}
		if hashes[h] {
			errs = append(errs, fmt.Errorf("API_KEYS key for %q duplicates another key", name))
		}
		names[name], hashes[h] = true, true
		keys = append(keys, APIKey{Name: name, Hash: h})
	}
	if len(keys) == 0 && len(errs) == 0 {
		errs = append(errs, errors.New("API_KEYS is required: at least one name=key pair"))
	}
	return keys, errs
}
