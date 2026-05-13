package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jaaacki/gemma-4-mlx/internal/accesslog"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "metrics.sqlite")
}

func TestOpen_CreatesDB(t *testing.T) {
	path := tempDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Schema should be in place; an empty query against requests must succeed.
	row := s.db.QueryRow("SELECT count(*) FROM requests")
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatalf("query requests: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

func TestInsert_RoundTrip(t *testing.T) {
	s, err := Open(tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	rec := &accesslog.Record{
		Time:       time.Date(2026, 5, 13, 15, 4, 5, 0, time.UTC),
		RemoteAddr: "127.0.0.1",
		Method:     "POST",
		Path:       "/v1/chat/completions",
		Status:     200,
		BytesSent:  1234,
		DurationMs: 123.4,
		UpstreamMs: 120.0,
	}
	if err := s.Insert(rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var (
		method      string
		status      int
		durationMs  float64
		upstreamMs  float64
	)
	row := s.db.QueryRow("SELECT method, status, duration_ms, upstream_ms FROM requests LIMIT 1")
	if err := row.Scan(&method, &status, &durationMs, &upstreamMs); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if method != "POST" || status != 200 || durationMs != 123.4 || upstreamMs != 120.0 {
		t.Errorf("got method=%q status=%d duration=%v upstream=%v", method, status, durationMs, upstreamMs)
	}
}

func TestInsertBatch_RoundTrip(t *testing.T) {
	s, err := Open(tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 5, 13, 15, 4, 5, 0, time.UTC)
	var batch []*accesslog.Record
	for i := 0; i < 10; i++ {
		batch = append(batch, &accesslog.Record{
			Time:       base.Add(time.Duration(i) * time.Second),
			Method:     "GET",
			Path:       "/healthz",
			Status:     200,
			DurationMs: float64(i),
		})
	}
	if err := s.InsertBatch(batch); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	row := s.db.QueryRow("SELECT count(*) FROM requests")
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 10 {
		t.Errorf("count = %d, want 10", n)
	}
}

func TestInsertBatch_Empty(t *testing.T) {
	s, err := Open(tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.InsertBatch(nil); err != nil {
		t.Errorf("InsertBatch(nil): %v", err)
	}
	if err := s.InsertBatch([]*accesslog.Record{}); err != nil {
		t.Errorf("InsertBatch([]): %v", err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	s, err := Open(tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	// Closing a nil receiver should also not panic.
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}
