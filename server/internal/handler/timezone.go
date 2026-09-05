package handler

import (
	"strings"
	"time"
)

// normalizeIANATimezone accepts only timezone names that can be shared with
// browser Intl APIs. Go treats "Local" as a special location, but it is not an
// IANA name and JavaScript rejects it in Intl.DateTimeFormat.
func normalizeIANATimezone(value string) (string, bool) {
	timezone := strings.TrimSpace(value)
	if timezone == "" || strings.EqualFold(timezone, "local") {
		return "", false
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", false
	}
	return timezone, true
}
