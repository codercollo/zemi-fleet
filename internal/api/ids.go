package api

import (
	"regexp"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/validator"
)

// maxIDLength is the longest ID or code any request may carry. Every string a client sends is an ID, a code or a
// header value; none is free text, so there is nothing else to cap. The JSON body is capped at 1 MiB on top of this.
const maxIDLength = 64

// maxListItems caps how many preferred categories or areas one request may carry.
const maxListItems = 50

// ID formats. They match what the application generates (a prefix, then base32 or digits) and what the seed
// uses, and nothing else: no slashes, spaces, quotes or Unicode ever reach a query or a log line.
var (
	riderIDPattern    = regexp.MustCompile(`^rider_[0-9A-Za-z]{1,40}$`)
	orderIDPattern    = regexp.MustCompile(`^ord_[0-9A-Za-z]{1,40}$`)
	batchIDPattern    = regexp.MustCompile(`^batch_[0-9A-Za-z]{1,40}$`)
	vendorIDPattern   = regexp.MustCompile(`^ven_[0-9A-Za-z]{1,40}$`)
	areaIDPattern     = regexp.MustCompile(`^area_[0-9A-Za-z]{1,40}$`)
	categoryPattern   = regexp.MustCompile(`^[a-z][a-z_]{1,39}$`) // the categories.code CHECK
	idempotencyFormat = regexp.MustCompile(`^[\x21-\x7E]{1,64}$`) // visible ASCII, no spaces
	reasonCodeFormat  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)
)

// idFormat describes a pattern for error messages.
type idFormat struct {
	pattern *regexp.Regexp
	example string
}

var (
	riderID    = idFormat{riderIDPattern, "rider_001"}
	orderID    = idFormat{orderIDPattern, "ord_001"}
	batchID    = idFormat{batchIDPattern, "batch_01H8XYZ"}
	vendorID   = idFormat{vendorIDPattern, "ven_001"}
	areaID     = idFormat{areaIDPattern, "area_001"}
	categoryID = idFormat{categoryPattern, "electronics"}
)

// checkID records a validation error unless value is non-empty and matches the format.
func checkID(v *validator.Validator, field, value string, f idFormat) {
	switch {
	case value == "":
		v.AddError(field, "must be provided")
	case len(value) > maxIDLength:
		v.AddError(field, "must not be more than 64 characters long")
	case !f.pattern.MatchString(value):
		v.AddError(field, "has an invalid format, expected something like "+f.example)
	}
}

// pathID returns the :id path parameter if it has the right format. A malformed ID cannot exist, so it is
// answered like an unknown one (404) without touching the database.
func (s *Server) pathID(c *gin.Context, f idFormat) (string, bool) {
	id := c.Param("id")
	if !f.pattern.MatchString(id) {
		s.notFoundResponse(c)
		return "", false
	}
	return id, true
}
