package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Status is the on-disk and in-memory representation of the engine subprocess
// state. It is persisted (atomically) to state/forge.status.json so that other
// processes (the forge CLI, tooling, dashboards) can introspect the engine
// without having to probe the HTTP port.
type Status struct {
	Running   bool       `json:"running"`
	PID       int        `json:"pid,omitempty"`
	Model     string     `json:"model,omitempty"`
	Endpoint  string     `json:"endpoint,omitempty"` // "http://host:port/v1"
	BootTime  *time.Time `json:"boot_time,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	LogFile   string     `json:"log_file"`
	PIDFile   string     `json:"pid_file"`
}

// readStatusFile reads and decodes the status JSON at path. Returns
// (nil, nil) if the file does not exist — callers should treat a missing
// file as "no recorded status" rather than as an error.
func readStatusFile(path string) (*Status, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading status file %s: %w", path, err)
	}
	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decoding status file %s: %w", path, err)
	}
	return &s, nil
}

// writeStatusFile atomically writes the status to path. It marshals to JSON
// (indented for human readability), writes to a sibling .tmp file, then
// renames into place so concurrent readers never observe a partial file.
func writeStatusFile(path string, s *Status) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding status: %w", err)
	}
	return atomicWrite(path, b, 0o644)
}

// atomicWrite writes data to path via a tmp+rename dance so readers never
// see a half-written file. The tmp file is placed in the same directory as
// the target so the rename is on the same filesystem (guaranteed atomic).
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("creating tmp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on any failure path before rename.
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing tmp file %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tmp file %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("syncing tmp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing tmp file %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("renaming %s -> %s: %w", tmpName, path, err)
	}
	return nil
}
