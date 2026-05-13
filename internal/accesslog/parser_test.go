package accesslog

import (
	"testing"
	"time"
)

func TestParseLine_Normal(t *testing.T) {
	line := []byte(`{"time":"2026-05-13T15:04:05+00:00","remote_addr":"127.0.0.1","method":"POST","path":"/v1/chat/completions","status":200,"bytes_sent":1234,"duration_ms":0.123,"upstream_ms":"0.120"}`)

	rec, err := ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.RemoteAddr != "127.0.0.1" {
		t.Errorf("remote_addr = %q, want %q", rec.RemoteAddr, "127.0.0.1")
	}
	if rec.Method != "POST" {
		t.Errorf("method = %q, want POST", rec.Method)
	}
	if rec.Path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", rec.Path)
	}
	if rec.Status != 200 {
		t.Errorf("status = %d, want 200", rec.Status)
	}
	if rec.BytesSent != 1234 {
		t.Errorf("bytes_sent = %d, want 1234", rec.BytesSent)
	}
	// 0.123s * 1000 = 123ms
	if got, want := rec.DurationMs, 123.0; got != want {
		t.Errorf("duration_ms = %v, want %v", got, want)
	}
	if got, want := rec.UpstreamMs, 120.0; got != want {
		t.Errorf("upstream_ms = %v, want %v", got, want)
	}
	want := time.Date(2026, 5, 13, 15, 4, 5, 0, time.UTC)
	if !rec.Time.Equal(want) {
		t.Errorf("time = %v, want %v", rec.Time, want)
	}
}

func TestParseLine_NoUpstream(t *testing.T) {
	// nginx serves a 404 without contacting upstream → upstream_response_time
	// is literal "-".
	line := []byte(`{"time":"2026-05-13T15:04:05+00:00","remote_addr":"10.0.0.1","method":"GET","path":"/nope","status":404,"bytes_sent":162,"duration_ms":0.000,"upstream_ms":"-"}`)

	rec, err := ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Status != 404 {
		t.Errorf("status = %d, want 404", rec.Status)
	}
	if rec.UpstreamMs != 0 {
		t.Errorf("upstream_ms = %v, want 0 (no upstream)", rec.UpstreamMs)
	}
	if rec.DurationMs != 0 {
		t.Errorf("duration_ms = %v, want 0", rec.DurationMs)
	}
}

func TestParseLine_Malformed(t *testing.T) {
	cases := []struct {
		name string
		line []byte
	}{
		{"empty", []byte("")},
		{"not json", []byte("hello world")},
		{"truncated", []byte(`{"time":"2026-05-13T15:04:05+00:00","status":`)},
		{"bad time", []byte(`{"time":"yesterday","status":200,"upstream_ms":"-"}`)},
		{"bad upstream", []byte(`{"time":"2026-05-13T15:04:05+00:00","status":200,"upstream_ms":"not-a-number"}`)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLine(tc.line); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestParseLine_MissingFields(t *testing.T) {
	// Only status + upstream_ms sentinel present. Other fields should
	// parse to zero values without error.
	line := []byte(`{"status":500,"upstream_ms":"-"}`)
	rec, err := ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Status != 500 {
		t.Errorf("status = %d, want 500", rec.Status)
	}
	if rec.RemoteAddr != "" || rec.Method != "" || rec.Path != "" {
		t.Errorf("expected empty string fields, got %+v", rec)
	}
	if !rec.Time.IsZero() {
		t.Errorf("expected zero time, got %v", rec.Time)
	}
	if rec.UpstreamMs != 0 || rec.DurationMs != 0 {
		t.Errorf("expected zero ms fields, got duration=%v upstream=%v", rec.DurationMs, rec.UpstreamMs)
	}
}
