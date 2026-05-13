// Package accesslog parses lines of nginx's json_combined access log.
//
// The nginx config that emits these lines lives at deploy/nginx/nginx.conf.
// Two non-obvious wire-format details that the parser handles:
//
//  1. nginx writes $request_time / $upstream_response_time in SECONDS as a
//     JSON number (e.g. 0.123). This package multiplies by 1000 to produce
//     milliseconds in the Record struct.
//  2. $upstream_response_time is a literal "-" (no quotes around the dash
//     in nginx's default escaping, but our log_format wraps it in quotes —
//     see the comment in nginx.conf) when there was no upstream request,
//     e.g. a 404 served by nginx itself. We treat that as "no upstream"
//     and leave UpstreamMs at zero.
package accesslog

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Record is one parsed access-log entry.
//
// DurationMs and UpstreamMs are in milliseconds (the wire format is
// seconds; the parser does the conversion). UpstreamMs is 0 when nginx
// served the request itself.
type Record struct {
	Time       time.Time `json:"time"`
	RemoteAddr string    `json:"remote_addr"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	BytesSent  int64     `json:"bytes_sent"`
	DurationMs float64   `json:"duration_ms"`
	UpstreamMs float64   `json:"upstream_ms,omitempty"`
}

// rawLine mirrors the on-wire JSON shape from nginx's json_combined log_format.
// duration_ms arrives as a JSON number of seconds; upstream_ms arrives as a
// quoted string so the literal "-" sentinel survives JSON parsing.
type rawLine struct {
	Time       string  `json:"time"`
	RemoteAddr string  `json:"remote_addr"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Status     int     `json:"status"`
	BytesSent  int64   `json:"bytes_sent"`
	DurationS  float64 `json:"duration_ms"` // seconds on the wire
	UpstreamS  string  `json:"upstream_ms"` // seconds-as-string or "-"
}

// ParseLine parses a single JSON access-log line into a *Record.
//
// Returns an error on:
//   - empty input
//   - malformed JSON
//   - unparseable time field (must be RFC3339, which $time_iso8601 satisfies)
//   - unparseable upstream_ms numeric (the "-" sentinel is NOT an error)
//
// Missing optional fields parse to zero values without error.
func ParseLine(line []byte) (*Record, error) {
	if len(line) == 0 {
		return nil, fmt.Errorf("accesslog: empty line")
	}

	var raw rawLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("accesslog: json decode: %w", err)
	}

	rec := &Record{
		RemoteAddr: raw.RemoteAddr,
		Method:     raw.Method,
		Path:       raw.Path,
		Status:     raw.Status,
		BytesSent:  raw.BytesSent,
		DurationMs: raw.DurationS * 1000.0,
	}

	if raw.Time != "" {
		t, err := time.Parse(time.RFC3339, raw.Time)
		if err != nil {
			return nil, fmt.Errorf("accesslog: parse time %q: %w", raw.Time, err)
		}
		rec.Time = t
	}

	// upstream_ms: "-" means "no upstream"; anything else should be a decimal
	// seconds value. Empty string also treated as no upstream (defensive).
	switch raw.UpstreamS {
	case "", "-":
		// leave UpstreamMs zero
	default:
		secs, err := strconv.ParseFloat(raw.UpstreamS, 64)
		if err != nil {
			return nil, fmt.Errorf("accesslog: parse upstream_ms %q: %w", raw.UpstreamS, err)
		}
		rec.UpstreamMs = secs * 1000.0
	}

	return rec, nil
}
