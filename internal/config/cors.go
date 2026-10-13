package config

import (
	"fmt"
	"net/url"
	"strings"
)

// CORSConfig holds the cross-origin settings (Let's Go Further ch. 18).
type CORSConfig struct {
	// TrustedOrigins are the exact origins (scheme + host + port, no path) whose browser code may call the API.
	// Empty (the default) means no CORS headers are ever sent.
	TrustedOrigins []string `mapstructure:"CORS_TRUSTED_ORIGINS"`
}

func (c CORSConfig) validate() []error {
	var errs []error
	for _, o := range c.TrustedOrigins {
		if err := validateOrigin(o); err != nil {
			errs = append(errs, fmt.Errorf("CORS_TRUSTED_ORIGINS entry %q %w", o, err))
		}
	}
	return errs
}

// validateOrigin accepts only what a browser sends in an Origin header: a lowercase http or https scheme, a host and an
// optional port, nothing else. Anything looser (a wildcard, a path, a trailing slash) would never match, or would match
// more than intended, so it is refused at startup instead.
func validateOrigin(o string) error {
	if strings.Contains(o, "*") {
		return fmt.Errorf("must not contain a wildcard: list each origin exactly")
	}
	if o != strings.ToLower(o) || o != strings.TrimSpace(o) {
		return fmt.Errorf("must be lowercase with no surrounding spaces")
	}
	u, err := url.Parse(o)
	if err != nil {
		return fmt.Errorf("is not a valid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("must include a host")
	}
	if o != u.Scheme+"://"+u.Host {
		return fmt.Errorf("must be only scheme://host[:port], with no path, query, fragment, user info or trailing slash")
	}
	return nil
}
