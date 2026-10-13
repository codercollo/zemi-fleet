package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxBodyBytes caps request bodies at 1 MiB.
const maxBodyBytes = 1_048_576

// envelope wraps single-key responses in a named key, e.g. {"batches": [...]}. Responses with several
// keys use a struct instead, because a map is encoded in alphabetical key order and the brief's order matters.
// Let's Go Further ch. 3.3.
type envelope map[string]any

// writeJSON is used instead of c.JSON so every response is formatted the same way.
// Let's Go Further ch. 3.2.
func writeJSON(c *gin.Context, status int, data any, headers http.Header) error {
	js, err := json.Marshal(data)
	if err != nil {
		return err
	}
	js = append(js, '\n')

	for key, values := range headers {
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(status)
	_, err = c.Writer.Write(js)
	return err
}

// readJSON decodes the request body into dst and turns decoder failures into
// readable messages. Let's Go Further ch. 4.2-4.4.
func readJSON(c *gin.Context, dst any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)

	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var syntaxErr *json.SyntaxError
		var unmarshalTypeErr *json.UnmarshalTypeError
		var invalidUnmarshalErr *json.InvalidUnmarshalError
		var maxBytesErr *http.MaxBytesError

		switch {
		case errors.As(err, &syntaxErr):
			return fmt.Errorf("body contains badly-formed JSON (at character %d)", syntaxErr.Offset)
		case errors.Is(err, io.ErrUnexpectedEOF):
			return errors.New("body contains badly-formed JSON")
		case errors.As(err, &unmarshalTypeErr):
			if unmarshalTypeErr.Field != "" {
				return fmt.Errorf("body contains incorrect JSON type for field %q", unmarshalTypeErr.Field)
			}
			return fmt.Errorf("body contains incorrect JSON type (at character %d)", unmarshalTypeErr.Offset)
		case errors.Is(err, io.EOF):
			return errors.New("body must not be empty")
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.TrimPrefix(err.Error(), "json: unknown field ")
			return fmt.Errorf("body contains unknown key %s", field)
		case errors.As(err, &maxBytesErr):
			return fmt.Errorf("body must not be larger than %d bytes", maxBytesErr.Limit)
		case errors.As(err, &invalidUnmarshalErr):
			// A programmer error (dst was not a non-nil pointer), not a client one.
			panic(err)
		default:
			return err
		}
	}

	// A second Decode must hit EOF, otherwise the body held extra JSON.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must only contain a single JSON value")
	}
	return nil
}
