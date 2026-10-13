package api

import (
	"net/http"

	"github.com/codercollo/zemi-fleet/internal/config"
)

// testAPIKey is the key every test client sends. testAPIKeys is the matching configuration (client name "tests").
const testAPIKey = "test-api-key-0123456789abcdef"

var testAPIKeys = []config.APIKey{config.NewAPIKey("tests", testAPIKey)}

// authed adds the valid test key to a request and returns it.
func authed(req *http.Request) *http.Request {
	req.Header.Set(apiKeyHeader, testAPIKey)
	return req
}

// getAuthed is http.Get with the valid test key, for tests that talk to a real listener.
func getAuthed(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(authed(req))
}
