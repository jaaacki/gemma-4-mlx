# Makefile for the gemma-4-mlx operator layer.
#
# `forge` is the Go CLI under cmd/forge/. `tailer` is a sibling binary owned
# by Coder D, expected under cmd/tailer/. Both compile into bin/.

GO        ?= go
BIN_DIR   := bin
FORGE_BIN := $(BIN_DIR)/forge
TAILER_BIN := $(BIN_DIR)/tailer

STATE_DIR := state

.PHONY: build forge tailer \
        boot-qwen boot-gemma stop status \
        swap-qwen swap-gemma \
        bench profiles clean force-clean

# --- build ----------------------------------------------------------------

build: forge tailer

forge: $(FORGE_BIN)

tailer: $(TAILER_BIN)

$(FORGE_BIN): | $(BIN_DIR)
	$(GO) build -o $(FORGE_BIN) ./cmd/forge

$(TAILER_BIN): | $(BIN_DIR)
	$(GO) build -o $(TAILER_BIN) ./cmd/tailer

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

# --- engine lifecycle (wraps forge) ---------------------------------------

boot-qwen: $(FORGE_BIN)
	$(FORGE_BIN) boot qwen36

boot-gemma: $(FORGE_BIN)
	$(FORGE_BIN) boot gemma4

stop: $(FORGE_BIN)
	$(FORGE_BIN) stop

status: $(FORGE_BIN)
	$(FORGE_BIN) status

swap-qwen: $(FORGE_BIN)
	$(FORGE_BIN) swap qwen36

swap-gemma: $(FORGE_BIN)
	$(FORGE_BIN) swap gemma4

profiles: $(FORGE_BIN)
	$(FORGE_BIN) profiles

# --- bench ----------------------------------------------------------------
# bench/ harness is Python; this target just prints the canonical invocation.

bench:
	@echo "uv run python -m bench.harness <args>"

# --- housekeeping ---------------------------------------------------------
# `clean` is safe: it refuses to wipe state while an engine is running, so an
# operator who forgets to `make stop` first doesn't orphan a vllm subprocess
# that still holds Metal/MLX memory and port 8000. Use `force-clean` for a
# true reset (intentionally destructive — it stops the engine itself).

clean:
	@if [ -f $(STATE_DIR)/vllm-metal.pid ] && kill -0 $$(cat $(STATE_DIR)/vllm-metal.pid 2>/dev/null) 2>/dev/null; then \
		echo "ERROR: engine is running (pid=$$(cat $(STATE_DIR)/vllm-metal.pid)). Run 'make stop' first, or 'make force-clean' to wipe anyway."; \
		exit 1; \
	fi
	rm -rf $(BIN_DIR)
	rm -f $(STATE_DIR)/*.pid $(STATE_DIR)/*.log $(STATE_DIR)/*.json
	rm -f bench/results/*.jsonl bench/results/*.json
	rm -f $(STATE_DIR)/metrics.sqlite $(STATE_DIR)/metrics.sqlite-shm $(STATE_DIR)/metrics.sqlite-wal
	rm -rf $(STATE_DIR)/launchd/*.log

force-clean:
	-./$(FORGE_BIN) stop 2>/dev/null || true
	rm -rf $(BIN_DIR)
	rm -f $(STATE_DIR)/*.pid $(STATE_DIR)/*.log $(STATE_DIR)/*.json
	rm -f bench/results/*.jsonl bench/results/*.json
	rm -f $(STATE_DIR)/metrics.sqlite $(STATE_DIR)/metrics.sqlite-shm $(STATE_DIR)/metrics.sqlite-wal
	rm -rf $(STATE_DIR)/launchd/*.log
