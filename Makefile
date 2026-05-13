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
        bench profiles clean

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

clean:
	rm -rf $(BIN_DIR)
	rm -f $(STATE_DIR)/*.pid $(STATE_DIR)/*.log $(STATE_DIR)/*.json
