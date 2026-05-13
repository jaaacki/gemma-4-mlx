// Command tailer follows nginx's JSON access log and writes per-request facts
// to SQLite. It's the observability arm of the vllm-metal evaluation rig —
// the synthetic bench in bench/ tells us what the engine can do; this tells
// us what real traffic actually looks like through the edge.
//
// Defaults assume the rest of the rig: nginx writes JSON to
// state/nginx-access.log (via the volume mount in deploy/nginx/docker-compose.yml),
// and we persist to state/metrics.sqlite.
//
// Lifecycle: tail from current EOF (never replay history on restart), batch
// inserts on size-or-time, flush+close cleanly on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/nxadm/tail"

	"github.com/jaaacki/gemma-4-mlx/internal/accesslog"
	"github.com/jaaacki/gemma-4-mlx/internal/store"
)

type config struct {
	logPath       string
	dbPath        string
	batchSize     int
	batchInterval time.Duration
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("tailer", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we print our own usage on error

	var cfg config
	fs.StringVar(&cfg.logPath, "log-path", "state/nginx-access.log", "path to nginx JSON access log")
	fs.StringVar(&cfg.dbPath, "db-path", "state/metrics.sqlite", "path to SQLite metrics database")
	fs.IntVar(&cfg.batchSize, "batch-size", 100, "flush after this many buffered records")
	fs.DurationVar(&cfg.batchInterval, "batch-interval", time.Second, "flush at least this often even if batch-size not reached")

	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("parse flags: %w", err)
	}
	if cfg.batchSize < 1 {
		return cfg, fmt.Errorf("--batch-size must be >= 1, got %d", cfg.batchSize)
	}
	if cfg.batchInterval <= 0 {
		return cfg, fmt.Errorf("--batch-interval must be > 0, got %v", cfg.batchInterval)
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		logger.Error("invalid flags", "err", err)
		os.Exit(2)
	}

	if err := run(cfg, logger); err != nil {
		logger.Error("tailer exited with error", "err", err)
		os.Exit(1)
	}
}

func run(cfg config, logger *slog.Logger) error {
	logger.Info("tailer starting",
		"log_path", cfg.logPath,
		"db_path", cfg.dbPath,
		"batch_size", cfg.batchSize,
		"batch_interval", cfg.batchInterval,
	)

	st, err := store.Open(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			logger.Error("close store", "err", cerr)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Wait for the log file to exist (nginx may not have started yet). The
	// nxadm/tail library can be told to follow non-existent files, but we
	// want a clear log line on each retry so an operator can see we're
	// waiting and not silently broken.
	if err := waitForFile(ctx, cfg.logPath, logger); err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Info("shutdown requested before log file appeared")
			return nil
		}
		return fmt.Errorf("wait for log file: %w", err)
	}

	t, err := tail.TailFile(cfg.logPath, tail.Config{
		// ReOpen handles rotation: if logrotate moves the file aside, follow
		// the new inode. Follow keeps the read going past EOF.
		ReOpen:    true,
		Follow:    true,
		MustExist: true,
		// Start at end-of-file so a restart doesn't replay history.
		Location: &tail.SeekInfo{Offset: 0, Whence: io.SeekEnd},
		// We do our own logging.
		Logger: tail.DiscardingLogger,
	})
	if err != nil {
		return fmt.Errorf("tail open %q: %w", cfg.logPath, err)
	}
	defer func() {
		if cerr := t.Stop(); cerr != nil {
			logger.Error("tail stop", "err", cerr)
		}
		t.Cleanup()
	}()

	return pump(ctx, t, st, cfg, logger)
}

// waitForFile blocks until path exists or ctx is canceled. Logs once per
// poll iteration so a stuck tailer is obvious.
func waitForFile(ctx context.Context, path string, logger *slog.Logger) error {
	const initial = 500 * time.Millisecond
	const maxBackoff = 5 * time.Second
	backoff := initial

	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %q: %w", path, err)
		}
		logger.Info("log file not present yet; waiting", "path", path, "retry_in", backoff)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// pump consumes lines from t, parses them, batches Records, and flushes to
// st on size-or-time. Returns when ctx is canceled or the tail channel closes;
// either way it flushes any pending records first.
func pump(ctx context.Context, t *tail.Tail, st *store.Store, cfg config, logger *slog.Logger) error {
	buf := make([]*accesslog.Record, 0, cfg.batchSize)
	ticker := time.NewTicker(cfg.batchInterval)
	defer ticker.Stop()

	var flushMu sync.Mutex
	flush := func(reason string) {
		flushMu.Lock()
		defer flushMu.Unlock()
		if len(buf) == 0 {
			return
		}
		n := len(buf)
		if err := st.InsertBatch(buf); err != nil {
			logger.Error("flush batch", "reason", reason, "records", n, "err", err)
			// Drop the batch — keeping it around risks unbounded growth if
			// the DB is wedged. Telemetry loss is acceptable; OOM is not.
		} else {
			logger.Debug("flushed batch", "reason", reason, "records", n)
		}
		buf = buf[:0]
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutdown signal received; flushing")
			flush("shutdown")
			return nil

		case <-ticker.C:
			flush("interval")

		case line, ok := <-t.Lines:
			if !ok {
				logger.Info("tail channel closed; flushing")
				flush("tail-closed")
				return nil
			}
			if line.Err != nil {
				logger.Warn("tail line error", "err", line.Err)
				continue
			}
			if line.Text == "" {
				continue
			}
			rec, err := accesslog.ParseLine([]byte(line.Text))
			if err != nil {
				logger.Warn("parse access-log line", "err", err, "line", truncate(line.Text, 200))
				continue
			}
			buf = append(buf, rec)
			if len(buf) >= cfg.batchSize {
				flush("size")
			}
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
