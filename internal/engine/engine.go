// Package engine manages the vllm-metal subprocess lifecycle.
//
// It replaces the shell scripts scripts/{start,stop,status}_engine.sh with a
// Go-native operator that the `forge` CLI can call directly. The subprocess
// is spawned detached (its own process group), with stdout+stderr routed to
// state/vllm-metal.log, the PID persisted to state/vllm-metal.pid, and the
// last-known status mirrored to state/forge.status.json. All on-disk state
// files are written atomically (tmp+rename) so concurrent readers never see
// half-written files.
//
// Start() does NOT block waiting for the HTTP endpoint to become ready —
// the caller (forge) is responsible for polling /v1/models if it wants to
// gate on readiness. This keeps the engine package focused purely on
// process lifecycle.
package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jaaacki/gemma-4-mlx/internal/profile"
)

// File names inside StateDir. Kept as package-level consts so tests and
// downstream tools (forge status, dashboards) can reference them without
// reaching into the engine struct.
const (
	pidFileName    = "vllm-metal.pid"
	logFileName    = "vllm-metal.log"
	statusFileName = "forge.status.json"
)

// stopGracePeriod is how long Stop() waits for SIGTERM to take effect
// before escalating to SIGKILL. vLLM Metal can take a few seconds to
// release Metal/MLX resources, so 10s is generous-but-not-absurd.
const stopGracePeriod = 10 * time.Second

// stopPollInterval is the cadence at which Stop() polls for process death
// during the grace period.
const stopPollInterval = 200 * time.Millisecond

// Engine is the operator handle for a single vllm-metal subprocess.
// All paths are absolute; New() resolves them from a repo root.
type Engine struct {
	Root     string // repo root
	VenvPath string // absolute path to .venv-vllm-metal
	StateDir string // absolute path to state/
}

// New constructs an Engine rooted at the given repo root, wiring the
// conventional venv and state directory locations.
func New(root string) *Engine {
	return &Engine{
		Root:     root,
		VenvPath: filepath.Join(root, ".venv-vllm-metal"),
		StateDir: filepath.Join(root, "state"),
	}
}

// pidFilePath returns the absolute path to the PID file.
func (e *Engine) pidFilePath() string {
	return filepath.Join(e.StateDir, pidFileName)
}

// logFilePath returns the absolute path to the subprocess log file.
func (e *Engine) logFilePath() string {
	return filepath.Join(e.StateDir, logFileName)
}

// statusFilePath returns the absolute path to the status JSON file.
func (e *Engine) statusFilePath() string {
	return filepath.Join(e.StateDir, statusFileName)
}

// vllmBinPath returns the absolute path to the `vllm` executable inside
// the venv. This avoids relying on PATH or `source activate` semantics.
func (e *Engine) vllmBinPath() string {
	return filepath.Join(e.VenvPath, "bin", "vllm")
}

// Start spawns the vllm subprocess detached. Returns an error if an
// engine is already running, the venv is missing, or the spawn itself
// fails. See package doc for the full ordering.
func (e *Engine) Start(p *profile.Profile, extraEnv map[string]string) error {
	log := slog.With("op", "engine.Start")

	if p == nil {
		return errors.New("starting engine: profile is nil")
	}

	// 1. Ensure StateDir exists (mkdir -p semantics) — must precede the
	//    EXCL lock dance because we'll create the PID file inside it.
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		return fmt.Errorf("starting engine: creating state dir: %w", err)
	}

	// 2. Acquire an exclusive lock by O_CREATE|O_EXCL on the PID file.
	//    This closes the TOCTOU window between IsRunning() and the actual
	//    spawn: two concurrent Start() calls cannot both win the create.
	//    The placeholder "starting" string is written so a concurrent
	//    reader (e.g. forge status, IsRunning) sees the file as malformed
	//    rather than parseable — which keeps it from reporting a bogus PID.
	if err := e.acquirePIDLock(log); err != nil {
		return err
	}

	// If anything below fails before we've spawned vllm successfully, the
	// placeholder PID file must be removed so the next Start() can proceed.
	pidLockCleanup := func() {
		if rmErr := os.Remove(e.pidFilePath()); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Warn("failed to remove pid lock after start failure", "err", rmErr)
		}
	}

	// 3. Ensure the venv exists. We launch vllm via its absolute path so
	//    we don't need to "source activate"; just verifying the dir is
	//    enough.
	if _, err := os.Stat(e.VenvPath); err != nil {
		pidLockCleanup()
		return fmt.Errorf("starting engine: venv not found at %s: %w", e.VenvPath, err)
	}
	vllmBin := e.vllmBinPath()
	if _, err := os.Stat(vllmBin); err != nil {
		pidLockCleanup()
		return fmt.Errorf("starting engine: vllm binary not found at %s: %w", vllmBin, err)
	}

	// 4. Open the log file for append so each boot's output is preserved
	//    in the same file the shell scripts used. We deliberately do NOT
	//    truncate.
	logFile, err := os.OpenFile(e.logFilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		pidLockCleanup()
		return fmt.Errorf("starting engine: opening log file: %w", err)
	}
	// We pass logFile to cmd.Stdout/Stderr; the child will inherit the
	// fd, and we should close our copy after Start() returns so we don't
	// leak it in this process.
	defer logFile.Close()

	// 5. Build the command. p.VLLMArgs() is expected to include the
	//    "serve" subcommand plus model id and any --host/--port/etc.
	args := p.VLLMArgs()
	cmd := exec.Command(vllmBin, args...)

	// 6. Compose environment: os.Environ() < profile env < extraEnv.
	//    Later wins — extraEnv (caller's explicit overrides) takes
	//    priority over profile defaults, which take priority over the
	//    ambient process env.
	env := mergeEnv(os.Environ(), p.Env(), extraEnv)
	cmd.Env = env

	// 7. Wire stdout/stderr to the log file. Both streams go to the
	//    same file, matching the shell `>> "$LOG_FILE" 2>&1` behavior.
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// 8. Detach the process group so that a later SIGTERM to -pid kills
	//    the whole vllm tree (worker processes, the metal helper, etc.),
	//    not just the parent.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// 9. Spawn. cmd.Start() returns once the child is forked+exec'd; it
	//    does NOT wait for the child to exit. We never call cmd.Wait()
	//    in this process — the subprocess is meant to outlive us.
	if err := cmd.Start(); err != nil {
		pidLockCleanup()
		return fmt.Errorf("starting engine: spawning vllm: %w", err)
	}
	pid := cmd.Process.Pid

	// 10. Persist PID atomically, overwriting the placeholder via tmp+rename.
	//     If this fails after we've spawned, we try to kill the orphan so
	//     we don't leak a vllm we can't track.
	if err := atomicWrite(e.pidFilePath(), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		_ = killProcess(pid, syscall.SIGTERM)
		pidLockCleanup()
		return fmt.Errorf("starting engine: writing pid file: %w", err)
	}

	// 11. Persist initial status atomically. Best-effort — a failure
	//     here doesn't justify killing the engine, but we log loudly
	//     so callers know status.json may be stale.
	//
	//     Endpoint is derived from the profile's server.host/server.port,
	//     since those are passed as `--host`/`--port` CLI flags (see
	//     Profile.VLLMArgs) rather than env vars — deriveEndpoint() would
	//     just return defaults here.
	endpoint := fmt.Sprintf("http://%s:%d/v1", p.Server.Host, p.Server.Port)
	bootedAt := time.Now().UTC()
	status := &Status{
		Running:  true,
		PID:      pid,
		Model:    p.ModelID(),
		Endpoint: endpoint,
		BootTime: &bootedAt,
		LogFile:  e.logFilePath(),
		PIDFile:  e.pidFilePath(),
	}
	if err := writeStatusFile(e.statusFilePath(), status); err != nil {
		log.Warn("failed to write status file (engine still running)", "err", err, "pid", pid)
	}

	log.Info("engine started",
		"pid", pid,
		"model", p.ModelID(),
		"endpoint", status.Endpoint,
		"log_file", e.logFilePath(),
	)
	return nil
}

// Stop terminates the engine subprocess group (SIGTERM, escalating to
// SIGKILL after stopGracePeriod) and clears state files. Returns nil if
// there is nothing to stop (no PID file).
func (e *Engine) Stop() error {
	log := slog.With("op", "engine.Stop")

	pid, err := readPIDFile(e.pidFilePath())
	if err != nil {
		if errors.Is(err, errNoPIDFile) {
			log.Info("no pid file; nothing to stop")
			return nil
		}
		if errors.Is(err, errMalformedPIDFile) {
			log.Warn("malformed pid file; removing", "path", e.pidFilePath())
			_ = os.Remove(e.pidFilePath())
			return nil
		}
		return fmt.Errorf("stopping engine: reading pid file: %w", err)
	}

	// If the recorded pid isn't alive anymore, just clean up.
	if !processAlive(pid) {
		log.Info("recorded pid not alive; cleaning up", "pid", pid)
		_ = os.Remove(e.pidFilePath())
		_ = e.markStopped(pid, "")
		return nil
	}

	// SIGTERM the process. killProcess prefers PGID kill (forge-spawned
	// engines are group leaders, so -pid reaches the whole vllm tree),
	// but falls back to per-PID kill for engines started by the legacy
	// shell scripts (scripts/use_*.sh + nohup), where vllm is not a
	// group leader and -pid would silently ESRCH.
	log.Info("sending SIGTERM", "pid", pid)
	if err := e.killProcess(pid, syscall.SIGTERM); err != nil {
		// ESRCH means the process already exited between our check and
		// the kill — that's a benign race, just clean up.
		if !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stopping engine: SIGTERM pid=%d: %w", pid, err)
		}
	}

	// Wait up to stopGracePeriod for the process to actually exit.
	deadline := time.Now().Add(stopGracePeriod)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			break
		}
		time.Sleep(stopPollInterval)
	}

	// Escalate to SIGKILL if needed.
	if processAlive(pid) {
		log.Warn("process did not exit within grace period; sending SIGKILL",
			"pid", pid, "grace", stopGracePeriod)
		if err := e.killProcess(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stopping engine: SIGKILL pid=%d: %w", pid, err)
		}
		// Poll up to 2 s after SIGKILL: even with SIGKILL, vLLM Metal can
		// take a moment to release Metal/MLX resources and the kernel takes
		// non-zero time to reap. If the pid is still alive after that, the
		// port may still be held — surface the failure to the caller instead
		// of silently removing the PID file.
		killDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(killDeadline) {
			if !processAlive(pid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if processAlive(pid) {
			return fmt.Errorf("engine stop: SIGKILL sent but pid %d still alive after 2s", pid)
		}
	}

	// Remove PID file regardless — at this point the process is either
	// dead or unreachable, and a stale PID file would block the next
	// Start().
	if err := os.Remove(e.pidFilePath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stopping engine: removing pid file: %w", err)
	}

	// Update status.json to reflect the stop, preserving prior fields
	// like Model/Endpoint/BootTime for post-mortem inspection.
	if err := e.markStopped(pid, ""); err != nil {
		log.Warn("failed to update status file after stop", "err", err)
	}

	log.Info("engine stopped", "pid", pid)
	return nil
}

// Status returns a best-effort snapshot of the engine's state. It blends
// the persisted status.json (which is informational, written at Start())
// with a live PID check, so callers always get an accurate Running flag.
func (e *Engine) Status() (*Status, error) {
	log := slog.With("op", "engine.Status")

	persisted, err := readStatusFile(e.statusFilePath())
	if err != nil {
		// Treat a corrupt status file as "no status" rather than fatal;
		// we can still report based on the PID file.
		log.Warn("could not read status file; proceeding without it", "err", err)
		persisted = nil
	}

	running, pid := e.IsRunning()

	switch {
	case persisted != nil:
		// Always normalize the file path fields to current truth.
		persisted.LogFile = e.logFilePath()
		persisted.PIDFile = e.pidFilePath()
		if persisted.Running && !running {
			// status.json says running, but the recorded PID is dead.
			stalePID := persisted.PID
			persisted.Running = false
			persisted.LastError = "stale pid file"
			// Keep PID/Model/Endpoint/BootTime so a human can see what
			// it *was* — the dead process's metadata is still useful.
			//
			// Persist the correction so external readers (forge status
			// from a different shell, dashboards, etc.) don't keep
			// observing the lie. Best-effort only — if the write fails
			// the in-memory return is still correct.
			log.Warn("status: stale pid file, persisting corrected state", "pid", stalePID)
			if werr := writeStatusFile(e.statusFilePath(), persisted); werr != nil {
				log.Warn("status: failed to persist stale-pid correction", "err", werr)
			}
		} else if running {
			// Ensure PID in status.json matches the live PID we found
			// (handles the edge where someone hand-edited status.json
			// but the PID file is the source of truth).
			persisted.Running = true
			persisted.PID = pid
			persisted.LastError = ""
		}
		return persisted, nil

	case running:
		// No status.json but we have a live PID file. Reconstruct a
		// minimal status so callers can at least see "yes, something
		// is running" with the PID.
		return &Status{
			Running: true,
			PID:     pid,
			LogFile: e.logFilePath(),
			PIDFile: e.pidFilePath(),
		}, nil

	default:
		// Nothing running, nothing persisted.
		return &Status{
			Running: false,
			LogFile: e.logFilePath(),
			PIDFile: e.pidFilePath(),
		}, nil
	}
}

// IsRunning reports whether a live engine subprocess exists, based on the
// PID file plus a kill(pid, 0) liveness probe. It never returns an error;
// any read/parse failure is treated as "not running" (with a slog.Warn).
func (e *Engine) IsRunning() (bool, int) {
	pid, err := readPIDFile(e.pidFilePath())
	if err != nil {
		if errors.Is(err, errNoPIDFile) {
			return false, 0
		}
		slog.Warn("could not read pid file", "op", "engine.IsRunning",
			"path", e.pidFilePath(), "err", err)
		return false, 0
	}
	if !processAlive(pid) {
		return false, 0
	}
	return true, pid
}

// --- internal helpers ---------------------------------------------------

// errNoPIDFile signals that the PID file is simply absent (a normal
// "not running" condition, not an error to bubble up).
var errNoPIDFile = errors.New("pid file does not exist")

// errMalformedPIDFile signals that the PID file exists but does not
// contain a parseable integer. Callers typically warn + delete.
var errMalformedPIDFile = errors.New("pid file is malformed")

// readPIDFile reads and parses the PID file. Returns errNoPIDFile if
// missing, errMalformedPIDFile if its contents don't parse.
func readPIDFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, errNoPIDFile
		}
		return 0, fmt.Errorf("reading pid file %s: %w", path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, errMalformedPIDFile
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, errMalformedPIDFile
	}
	if pid <= 0 {
		return 0, errMalformedPIDFile
	}
	return pid, nil
}

// pidLockPlaceholder is what we write into a freshly EXCL-created PID file
// before vllm has been spawned. It deliberately doesn't parse as a number,
// so readers via readPIDFile see errMalformedPIDFile (treated as "not
// running") rather than mistaking the lock for a real PID.
const pidLockPlaceholder = "starting\n"

// acquirePIDLock atomically creates the PID file via O_CREATE|O_EXCL. This
// is the lock that closes the TOCTOU race between two concurrent Start()
// invocations.
//
// Outcomes:
//   - EXCL succeeds → caller owns the lock, must remove on failure or
//     overwrite with the real PID on success.
//   - EEXIST + existing PID is a live process → return "already running".
//   - EEXIST + existing PID file is malformed or dead → unlink and retry
//     ONCE. A second EEXIST means another Start() raced us between our
//     unlink and our retry — return "boot already in progress".
func (e *Engine) acquirePIDLock(log *slog.Logger) error {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(e.pidFilePath(),
			os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(pidLockPlaceholder)
			cerr := f.Close()
			if werr != nil {
				_ = os.Remove(e.pidFilePath())
				return fmt.Errorf("starting engine: writing pid lock placeholder: %w", werr)
			}
			if cerr != nil {
				_ = os.Remove(e.pidFilePath())
				return fmt.Errorf("starting engine: closing pid lock: %w", cerr)
			}
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("starting engine: opening pid lock: %w", err)
		}

		// EEXIST. Inspect what's there.
		//
		// Three possible contents:
		//
		//  1) A live PID — a real engine is running. Refuse.
		//  2) A dead-but-parseable PID — leftover from a crashed engine
		//     that didn't clean its PID file. Safe to clear and retry.
		//  3) The literal "starting" placeholder — ANOTHER Start() just
		//     won the EXCL race and is mid-boot. We must NOT clear this,
		//     because doing so would corrupt their lock and let two
		//     concurrent boots through (the original TOCTOU bug).
		//
		// readPIDFile returns errMalformedPIDFile for "starting", so we
		// treat malformed contents as case (3) and refuse — leaving the
		// other Start() in charge.
		existingPID, perr := readPIDFile(e.pidFilePath())
		if perr == nil && processAlive(existingPID) {
			return fmt.Errorf("engine already running (pid=%d)", existingPID)
		}
		if perr != nil {
			// Malformed contents == another Start() holds the lock.
			// Surface as a race, don't clear it.
			return errors.New("engine boot already in progress, try again")
		}
		// Parseable PID but dead — case (2). Clear and retry.
		if attempt == 0 {
			log.Info("removing stale pid file before retry",
				"path", e.pidFilePath(), "stale_pid", existingPID)
			if rmErr := os.Remove(e.pidFilePath()); rmErr != nil && !os.IsNotExist(rmErr) {
				return fmt.Errorf("starting engine: removing stale pid file: %w", rmErr)
			}
			continue
		}
		// Second EEXIST after clearing a stale PID → someone else won
		// the retry race. Don't loop forever; surface it.
		return errors.New("engine boot already in progress, try again")
	}
	return errors.New("starting engine: pid lock retries exhausted")
}

// killProcess sends sig to pid, preferring a process-group kill when pid
// is itself the group leader (the case for forge-spawned engines, which
// set Setpgid=true in SysProcAttr). When pid is NOT a group leader — the
// case for engines booted by the legacy scripts/use_*.sh wrappers, where
// `nohup vllm &` leaves vllm as a child of the original shell's pgrp —
// PGID-based kill via -pid would either ESRCH or miss the target entirely.
// In that case we fall back to a per-PID signal so forge stop can still
// terminate a shell-spawned engine cleanly.
func killProcess(pid int, sig syscall.Signal) error {
	pgid, err := syscall.Getpgid(pid)
	if err == nil && pgid == pid {
		if killErr := syscall.Kill(-pid, sig); killErr == nil {
			return nil
		}
		// Group kill failed (e.g., transient race during teardown); fall
		// through to per-PID signaling as a last-resort.
	}
	return syscall.Kill(pid, sig)
}

// killProcess is exposed as a method so callers/tests can stub it via the
// Engine receiver if needed. The free function above does the real work.
func (e *Engine) killProcess(pid int, sig syscall.Signal) error {
	return killProcess(pid, sig)
}

// processAlive returns true iff kill(pid, 0) succeeds — i.e. the process
// exists AND we have permission to signal it. EPERM is treated as alive
// (some other user's process with our pid number — extremely unlikely on
// a single-user laptop, but correct semantically).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// mergeEnv merges environment maps into the os.Environ()-style "KEY=VAL"
// slice format expected by exec.Cmd.Env. Later sources override earlier
// ones on key collision. Keys with empty values are still emitted (the
// shell scripts treat unset and empty differently; we preserve that).
func mergeEnv(base []string, overlays ...map[string]string) []string {
	merged := make(map[string]string, len(base))
	for _, kv := range base {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		merged[kv[:eq]] = kv[eq+1:]
	}
	for _, overlay := range overlays {
		for k, v := range overlay {
			merged[k] = v
		}
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

// deriveEndpoint inspects the merged env for HOST/PORT to construct the
// OpenAI-compatible endpoint string we publish in Status.Endpoint. This
// matches the convention used by start_engine.sh (HOST=127.0.0.1,
// PORT=8000 by default). If the caller passed --host/--port via VLLMArgs
// instead, this falls back to the documented defaults.
func deriveEndpoint(env []string) string {
	host := "127.0.0.1"
	port := "8000"
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		switch kv[:eq] {
		case "HOST":
			if v := kv[eq+1:]; v != "" {
				host = v
			}
		case "PORT":
			if v := kv[eq+1:]; v != "" {
				port = v
			}
		}
	}
	return fmt.Sprintf("http://%s:%s/v1", host, port)
}

// markStopped writes a status.json reflecting a stopped engine. If a
// prior status file exists, its Model/Endpoint/BootTime fields are
// preserved so humans can see what last ran. lastErr (if non-empty)
// is recorded in LastError.
func (e *Engine) markStopped(priorPID int, lastErr string) error {
	persisted, _ := readStatusFile(e.statusFilePath())
	if persisted == nil {
		persisted = &Status{}
	}
	persisted.Running = false
	persisted.PID = priorPID
	persisted.LogFile = e.logFilePath()
	persisted.PIDFile = e.pidFilePath()
	if lastErr != "" {
		persisted.LastError = lastErr
	}
	return writeStatusFile(e.statusFilePath(), persisted)
}
