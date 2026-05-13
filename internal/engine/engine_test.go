package engine

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNew_WiresPaths verifies the constructor builds the conventional
// VenvPath and StateDir from the repo root.
func TestNew_WiresPaths(t *testing.T) {
	root := "/tmp/fake-repo-root"
	e := New(root)
	if e.Root != root {
		t.Errorf("Root = %q, want %q", e.Root, root)
	}
	wantVenv := filepath.Join(root, ".venv-vllm-metal")
	if e.VenvPath != wantVenv {
		t.Errorf("VenvPath = %q, want %q", e.VenvPath, wantVenv)
	}
	wantState := filepath.Join(root, "state")
	if e.StateDir != wantState {
		t.Errorf("StateDir = %q, want %q", e.StateDir, wantState)
	}
}

// TestEngine_DerivedPaths verifies the internal pidFilePath/logFilePath/
// statusFilePath/vllmBinPath helpers compose under StateDir/VenvPath.
func TestEngine_DerivedPaths(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if got, want := e.pidFilePath(), filepath.Join(root, "state", "vllm-metal.pid"); got != want {
		t.Errorf("pidFilePath = %q, want %q", got, want)
	}
	if got, want := e.logFilePath(), filepath.Join(root, "state", "vllm-metal.log"); got != want {
		t.Errorf("logFilePath = %q, want %q", got, want)
	}
	if got, want := e.statusFilePath(), filepath.Join(root, "state", "forge.status.json"); got != want {
		t.Errorf("statusFilePath = %q, want %q", got, want)
	}
	if got, want := e.vllmBinPath(), filepath.Join(root, ".venv-vllm-metal", "bin", "vllm"); got != want {
		t.Errorf("vllmBinPath = %q, want %q", got, want)
	}
}

// TestIsRunning_NoPIDFile: a fresh repo with no PID file must report
// not-running with pid=0.
func TestIsRunning_NoPIDFile(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	// StateDir doesn't even exist — that's a valid "not running" case.
	running, pid := e.IsRunning()
	if running {
		t.Errorf("IsRunning = true, want false (no pid file)")
	}
	if pid != 0 {
		t.Errorf("IsRunning pid = %d, want 0", pid)
	}
}

// TestIsRunning_GibberishPIDFile: a PID file that doesn't parse as an
// integer must be treated as "not running" — no panic, no error.
func TestIsRunning_GibberishPIDFile(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(e.pidFilePath(), []byte("not-a-number\n"), 0o644); err != nil {
		t.Fatalf("write gibberish pid: %v", err)
	}
	running, pid := e.IsRunning()
	if running {
		t.Errorf("IsRunning = true, want false (gibberish pid)")
	}
	if pid != 0 {
		t.Errorf("IsRunning pid = %d, want 0", pid)
	}
}

// TestIsRunning_EmptyPIDFile: an empty PID file is malformed and must
// be treated as not running.
func TestIsRunning_EmptyPIDFile(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(e.pidFilePath(), []byte(""), 0o644); err != nil {
		t.Fatalf("write empty pid: %v", err)
	}
	running, pid := e.IsRunning()
	if running || pid != 0 {
		t.Errorf("IsRunning = (%v, %d), want (false, 0) for empty pid file", running, pid)
	}
}

// TestIsRunning_DeadPID: a PID file pointing to a process that does not
// exist must be treated as not running.
func TestIsRunning_DeadPID(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	// PID 1 always exists on Unix; we want a definitely-dead one.
	// Spawn a short-lived child, capture its pid, wait for it to die.
	deadPID := spawnAndReap(t)
	if err := os.WriteFile(e.pidFilePath(), []byte(strconv.Itoa(deadPID)+"\n"), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	running, pid := e.IsRunning()
	if running {
		t.Errorf("IsRunning = true for dead pid %d, want false", deadPID)
	}
	if pid != 0 {
		t.Errorf("IsRunning pid = %d, want 0", pid)
	}
}

// TestIsRunning_LivePID: the current test process is definitely alive,
// so a PID file pointing at os.Getpid() must report running.
func TestIsRunning_LivePID(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	self := os.Getpid()
	if err := os.WriteFile(e.pidFilePath(), []byte(strconv.Itoa(self)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	running, pid := e.IsRunning()
	if !running {
		t.Errorf("IsRunning = false for live pid %d, want true", self)
	}
	if pid != self {
		t.Errorf("IsRunning pid = %d, want %d", pid, self)
	}
}

// TestStatus_JSONRoundTrip: marshal a Status, unmarshal it, and verify
// all fields survive — this guards against accidental json-tag changes
// breaking on-disk compatibility.
func TestStatus_JSONRoundTrip(t *testing.T) {
	bt := time.Date(2026, 5, 13, 12, 34, 56, 0, time.UTC)
	orig := Status{
		Running:   true,
		PID:       12345,
		Model:     "Qwen/Qwen3-0.6B",
		Endpoint:  "http://127.0.0.1:8000/v1",
		BootTime:  &bt,
		LastError: "",
		LogFile:   "/repo/state/vllm-metal.log",
		PIDFile:   "/repo/state/vllm-metal.pid",
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Status
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Running != orig.Running {
		t.Errorf("Running mismatch: got %v, want %v", got.Running, orig.Running)
	}
	if got.PID != orig.PID {
		t.Errorf("PID mismatch: got %d, want %d", got.PID, orig.PID)
	}
	if got.Model != orig.Model {
		t.Errorf("Model mismatch: got %q, want %q", got.Model, orig.Model)
	}
	if got.Endpoint != orig.Endpoint {
		t.Errorf("Endpoint mismatch: got %q, want %q", got.Endpoint, orig.Endpoint)
	}
	if got.BootTime == nil || !got.BootTime.Equal(*orig.BootTime) {
		t.Errorf("BootTime mismatch: got %v, want %v", got.BootTime, orig.BootTime)
	}
	if got.LogFile != orig.LogFile {
		t.Errorf("LogFile mismatch: got %q, want %q", got.LogFile, orig.LogFile)
	}
	if got.PIDFile != orig.PIDFile {
		t.Errorf("PIDFile mismatch: got %q, want %q", got.PIDFile, orig.PIDFile)
	}
}

// TestStatus_OmitEmpty: zero-valued optional fields must not appear in
// the serialized form (we rely on this for the "minimal stopped" status).
func TestStatus_OmitEmpty(t *testing.T) {
	s := Status{
		Running: false,
		LogFile: "/x/log",
		PIDFile: "/x/pid",
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, field := range []string{"pid", "model", "endpoint", "boot_time", "last_error"} {
		if strings.Contains(out, "\""+field+"\"") {
			t.Errorf("zero-valued field %q should be omitted; got: %s", field, out)
		}
	}
}

// TestAtomicWrite_FinalFileAppears: the tmp+rename dance must result in
// a target file containing exactly the requested bytes, with no leftover
// .tmp siblings in the directory.
func TestAtomicWrite_FinalFileAppears(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "thing.json")
	payload := []byte(`{"hello":"world"}`)
	if err := atomicWrite(target, payload, 0o644); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("target contents = %q, want %q", got, payload)
	}
	// No tmp leftovers.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, ent := range entries {
		if strings.Contains(ent.Name(), ".tmp.") {
			t.Errorf("leftover tmp file: %s", ent.Name())
		}
	}
	// Mode is set as requested.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

// TestAtomicWrite_CreatesParentDir: atomicWrite should mkdir -p the
// parent directory rather than failing on a missing intermediate.
func TestAtomicWrite_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deeper", "thing.json")
	if err := atomicWrite(target, []byte("ok"), 0o644); err != nil {
		t.Fatalf("atomicWrite (nested): %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected target to exist: %v", err)
	}
}

// TestAtomicWrite_Overwrite: calling atomicWrite twice should replace
// the existing file, not append or fail.
func TestAtomicWrite_Overwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "thing.json")
	if err := atomicWrite(target, []byte("first"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := atomicWrite(target, []byte("second"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("after overwrite got %q, want %q", got, "second")
	}
}

// TestWriteAndReadStatusFile: round-trip through the on-disk JSON form.
func TestWriteAndReadStatusFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge.status.json")
	bt := time.Now().UTC().Truncate(time.Second)
	orig := &Status{
		Running:  true,
		PID:      4242,
		Model:    "m",
		Endpoint: "http://127.0.0.1:8000/v1",
		BootTime: &bt,
		LogFile:  "/log",
		PIDFile:  "/pid",
	}
	if err := writeStatusFile(path, orig); err != nil {
		t.Fatalf("writeStatusFile: %v", err)
	}
	got, err := readStatusFile(path)
	if err != nil {
		t.Fatalf("readStatusFile: %v", err)
	}
	if got == nil {
		t.Fatal("readStatusFile returned nil after write")
	}
	if got.PID != orig.PID || got.Model != orig.Model || got.Endpoint != orig.Endpoint {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, orig)
	}
	if got.BootTime == nil || !got.BootTime.Equal(*orig.BootTime) {
		t.Errorf("BootTime mismatch: got %v want %v", got.BootTime, orig.BootTime)
	}
}

// TestReadStatusFile_Missing: reading a non-existent status file must
// return (nil, nil) — callers treat absence as "no status recorded".
func TestReadStatusFile_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")
	got, err := readStatusFile(path)
	if err != nil {
		t.Fatalf("readStatusFile on missing: unexpected err %v", err)
	}
	if got != nil {
		t.Errorf("readStatusFile on missing: got %+v, want nil", got)
	}
}

// TestStatus_NoFilesPresent: with no state files at all, Status() must
// return a Running=false stub (not an error).
func TestStatus_NoFilesPresent(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	s, err := e.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Running {
		t.Errorf("Running = true, want false")
	}
	if s.LogFile == "" || s.PIDFile == "" {
		t.Errorf("LogFile/PIDFile should always be populated, got %+v", s)
	}
}

// TestStatus_StalePIDInStatusFile: status.json says Running=true with a
// PID, but that PID is dead. Status() must flip Running to false and
// surface LastError, while preserving the historical Model field.
func TestStatus_StalePIDInStatusFile(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	deadPID := spawnAndReap(t)
	bt := time.Now().UTC().Add(-time.Hour)
	stale := &Status{
		Running:  true,
		PID:      deadPID,
		Model:    "Qwen/Qwen3-0.6B",
		Endpoint: "http://127.0.0.1:8000/v1",
		BootTime: &bt,
		LogFile:  e.logFilePath(),
		PIDFile:  e.pidFilePath(),
	}
	if err := writeStatusFile(e.statusFilePath(), stale); err != nil {
		t.Fatalf("writeStatusFile: %v", err)
	}
	if err := os.WriteFile(e.pidFilePath(), []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	s, err := e.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Running {
		t.Errorf("Running = true, want false (stale)")
	}
	if s.LastError != "stale pid file" {
		t.Errorf("LastError = %q, want %q", s.LastError, "stale pid file")
	}
	if s.Model != "Qwen/Qwen3-0.6B" {
		t.Errorf("Model not preserved across stale-flip; got %q", s.Model)
	}
}

// TestStatus_LivePIDNoStatusFile: a PID file pointing at a live process
// with no status.json should yield a reconstructed Running=true status.
func TestStatus_LivePIDNoStatusFile(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	self := os.Getpid()
	if err := os.WriteFile(e.pidFilePath(), []byte(strconv.Itoa(self)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	s, err := e.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !s.Running {
		t.Errorf("Running = false, want true (live pid no status)")
	}
	if s.PID != self {
		t.Errorf("PID = %d, want %d", s.PID, self)
	}
}

// TestStop_NoPIDFile: stopping when nothing is running must be a no-op
// returning nil.
func TestStop_NoPIDFile(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := e.Stop(); err != nil {
		t.Errorf("Stop on empty state: %v", err)
	}
}

// TestStop_MalformedPIDFile: a garbage PID file should be silently
// cleared, not panicked over.
func TestStop_MalformedPIDFile(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(e.pidFilePath(), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if err := e.Stop(); err != nil {
		t.Errorf("Stop on malformed pid: %v", err)
	}
	if _, err := os.Stat(e.pidFilePath()); !os.IsNotExist(err) {
		t.Errorf("malformed pid file should be removed; stat err=%v", err)
	}
}

// TestStop_DeadPID: stopping when the PID file points to a dead process
// should clean up without error and remove the PID file.
func TestStop_DeadPID(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	deadPID := spawnAndReap(t)
	if err := os.WriteFile(e.pidFilePath(), []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if err := e.Stop(); err != nil {
		t.Errorf("Stop on dead pid: %v", err)
	}
	if _, err := os.Stat(e.pidFilePath()); !os.IsNotExist(err) {
		t.Errorf("pid file should be removed after stop")
	}
}

// TestMergeEnv: later overlays must win on key collision; base env
// without an '=' is dropped silently.
func TestMergeEnv(t *testing.T) {
	base := []string{"FOO=base", "BAR=base", "MALFORMED"}
	a := map[string]string{"BAR": "a", "BAZ": "a"}
	b := map[string]string{"BAZ": "b", "QUX": "b"}
	got := mergeEnv(base, a, b)
	// Convert to map for easy assertion.
	m := map[string]string{}
	for _, kv := range got {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			t.Errorf("malformed kv in merged env: %q", kv)
			continue
		}
		m[kv[:eq]] = kv[eq+1:]
	}
	want := map[string]string{
		"FOO": "base",
		"BAR": "a", // overlay a beats base
		"BAZ": "b", // overlay b beats overlay a
		"QUX": "b",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("mergeEnv[%q] = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["MALFORMED"]; ok {
		t.Errorf("malformed base entry should not appear in merged env")
	}
}

// TestDeriveEndpoint: respects HOST/PORT, falls back to defaults.
func TestDeriveEndpoint(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"defaults", []string{}, "http://127.0.0.1:8000/v1"},
		{"host override", []string{"HOST=0.0.0.0"}, "http://0.0.0.0:8000/v1"},
		{"port override", []string{"PORT=9000"}, "http://127.0.0.1:9000/v1"},
		{"both", []string{"HOST=10.0.0.1", "PORT=9001"}, "http://10.0.0.1:9001/v1"},
		{"empty host ignored", []string{"HOST="}, "http://127.0.0.1:8000/v1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveEndpoint(tc.env); got != tc.want {
				t.Errorf("deriveEndpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAcquirePIDLock_Concurrent verifies the EXCL-based PID lock in
// Start() closes the TOCTOU window: exactly one of N concurrent
// acquirePIDLock attempts wins; the rest report "already running" or
// "boot already in progress".
//
// We exercise the lock directly (not Engine.Start, which would try to
// exec vllm), since the lock is the part doing the race-safety work.
func TestAcquirePIDLock_Concurrent(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	const N = 5
	type result struct {
		err error
	}
	var wg sync.WaitGroup
	results := make(chan result, N)
	start := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := e.acquirePIDLock(logger)
			results <- result{err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	wins := 0
	losses := 0
	for r := range results {
		if r.err == nil {
			wins++
		} else {
			losses++
			// Loss reason must be either "already running" or
			// "boot already in progress" — never some unrelated I/O error.
			msg := r.err.Error()
			if !strings.Contains(msg, "already running") &&
				!strings.Contains(msg, "boot already in progress") {
				t.Errorf("unexpected loss error: %v", r.err)
			}
		}
	}
	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d (losses=%d)", wins, losses)
	}
	if losses != N-1 {
		t.Errorf("expected %d losers, got %d", N-1, losses)
	}

	// The PID file should exist after the winning acquire, containing
	// the placeholder rather than a parseable PID.
	b, err := os.ReadFile(e.pidFilePath())
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if !strings.HasPrefix(string(b), "starting") {
		t.Errorf("pid file should contain 'starting' placeholder, got %q", string(b))
	}
}

// TestAcquirePIDLock_StaleFileRecovers: a stale PID file (dead PID) must
// be cleared and the lock should be acquired on the retry.
func TestAcquirePIDLock_StaleFileRecovers(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	deadPID := spawnAndReap(t)
	if err := os.WriteFile(e.pidFilePath(), []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatalf("write stale pid: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := e.acquirePIDLock(logger); err != nil {
		t.Fatalf("acquirePIDLock with stale file: %v", err)
	}
	// After acquire, the file should hold the placeholder, not the
	// dead pid.
	b, _ := os.ReadFile(e.pidFilePath())
	if !strings.HasPrefix(string(b), "starting") {
		t.Errorf("after stale recovery want placeholder, got %q", string(b))
	}
}

// TestAcquirePIDLock_LivePIDRefuses: a PID file pointing to a live process
// must cause acquire to fail with "already running".
func TestAcquirePIDLock_LivePIDRefuses(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	self := os.Getpid()
	if err := os.WriteFile(e.pidFilePath(), []byte(strconv.Itoa(self)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := e.acquirePIDLock(logger)
	if err == nil {
		t.Fatal("acquirePIDLock should have refused a live PID")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("want 'already running', got %v", err)
	}
}

// spawnAndReap runs /bin/sh -c true (or similar short-lived command)
// and waits for it to exit, returning the now-dead PID. Useful for tests
// that need a definitely-dead PID without racing PID reuse.
//
// Note: PID reuse is technically possible between Wait() and the test's
// use of the returned pid, but on macOS/Linux PID space is large enough
// that this is reliable for a single-test window. If a test ever flakes
// here, increase the spread by spawning several short-lived processes
// before returning the first one's pid.
func spawnAndReap(t *testing.T) int {
	t.Helper()
	// Use exec.Command (fork+exec) — never a bare fork from Go, which
	// is unsafe with the Go runtime.
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn /bin/sh: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		// Non-zero exit is fine; we asked for exit 0 but any termination
		// makes the pid dead. Don't fail the test for benign Wait errors.
		t.Logf("wait on helper pid %d: %v", pid, err)
	}
	// At this point pid has been reaped — it's no longer signalable.
	if processAlive(pid) {
		t.Fatalf("expected pid %d to be dead after wait", pid)
	}
	return pid
}
