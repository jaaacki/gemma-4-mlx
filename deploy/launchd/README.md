# launchd agents for the gemma-4 operator stack

Three macOS LaunchAgents that bring the full inference stack up at user login (which, on this Mac, is effectively boot — it rarely shuts down). They are dormant until you opt in with `launchctl load`.

## Warning — dormant by default

**These plists are NOT loaded automatically by any commit in this repo.** They are deployment artifacts; you must copy them into `~/Library/LaunchAgents/` and run `launchctl load` yourself for them to take effect. Phase 2 of the project ships the files but does not enable them.

## What's here

| Plist | What it runs | Owner |
|---|---|---|
| `com.gemma4.nginx.plist`  | `scripts/edge_up.sh` (Docker nginx edge), then parks on `tail -f /dev/null` so launchd has a foreground child to supervise | edge layer |
| `com.gemma4.forge.plist`  | `bin/forge boot qwen36` — supervises the vllm engine | operator layer |
| `com.gemma4.tailer.plist` | `bin/tailer` — streams nginx access log into `state/metrics.sqlite` | observability |

All three are **LaunchAgents** (per-user, run as `noonoon`), not LaunchDaemons. They:

- `RunAtLoad=true` — start the moment they are loaded
- `KeepAlive` — see "Known limitation" below; `nginx` and `tailer` set this `true`, `forge` sets it `false`
- `ThrottleInterval=30` — refuse to relaunch faster than every 30 s, so a port-already-in-use loop during transient state doesn't pin a core
- `WorkingDirectory=/Users/noonoon/Dev/gemma-4`
- `PATH=/opt/homebrew/bin:...` — so Docker, Go binaries, etc. resolve
- stdout/stderr → `/Users/noonoon/Dev/gemma-4/state/launchd/<label>.{out,err}.log`

## Known limitation — forge does not auto-restart vllm

`com.gemma4.forge.plist` sets `KeepAlive=false` on purpose. `forge boot <profile>` is a **one-shot** that spawns the detached `vllm` subprocess and exits 0; the engine itself outlives `forge`. If `KeepAlive` were `true`, launchd would keep respawning `forge boot` every 30 s and every respawn would hit "engine already running" and exit 1 — forever.

The practical consequence: **if the vllm engine crashes after `forge boot` returns, launchd will not bring it back up.** You have to manually re-run `forge boot <profile>` (or `forge swap <profile>`). A future `forge supervise` subcommand will be a long-lived foreground supervisor process that launchd *can* restart; until that lands, treat boot-time `forge boot` as best-effort startup, not a watchdog.

`nginx` and `tailer` keep `KeepAlive=true` because they are genuinely long-running foreground processes (nginx parks on `tail -f /dev/null`; the tailer's read loop never returns).

## One-time install

```bash
# Make sure the log destination exists, or launchd will silently drop output.
mkdir -p /Users/noonoon/Dev/gemma-4/state/launchd

# Copy plists into the per-user LaunchAgents directory.
cp /Users/noonoon/Dev/gemma-4/deploy/launchd/com.gemma4.*.plist ~/Library/LaunchAgents/

# Load them. -w persists the "enabled" bit across logouts.
launchctl load -w ~/Library/LaunchAgents/com.gemma4.nginx.plist
launchctl load -w ~/Library/LaunchAgents/com.gemma4.forge.plist
launchctl load -w ~/Library/LaunchAgents/com.gemma4.tailer.plist
```

## Order

Forge's vllm engine binds to `127.0.0.1:8000` and nginx forwards to it; neither needs the other to boot. So **forge and nginx can come up in any order.** The tailer, however, opens `state/nginx-access.log`, which doesn't exist until nginx has served at least once — load `com.gemma4.nginx.plist` **before** `com.gemma4.tailer.plist`, otherwise the tailer will spam retry logs until the file appears.

Recommended load order: nginx → forge → tailer.

## Status

```bash
launchctl list | grep com.gemma4
# Three lines, PID column nonzero = running. "-" = loaded but not currently running.
# Third column is the last exit status (0 = clean, nonzero = crashed last time).
```

## Logs

```bash
tail -f /Users/noonoon/Dev/gemma-4/state/launchd/forge.out.log
tail -f /Users/noonoon/Dev/gemma-4/state/launchd/forge.err.log
tail -f /Users/noonoon/Dev/gemma-4/state/launchd/tailer.out.log
tail -f /Users/noonoon/Dev/gemma-4/state/launchd/tailer.err.log
tail -f /Users/noonoon/Dev/gemma-4/state/launchd/nginx.out.log
tail -f /Users/noonoon/Dev/gemma-4/state/launchd/nginx.err.log
```

Note that vllm engine logs proper still go to `state/vllm-metal.log` via the operator scripts; the `forge.*.log` files only capture forge's own output.

## Stop / unload

```bash
launchctl unload ~/Library/LaunchAgents/com.gemma4.tailer.plist
launchctl unload ~/Library/LaunchAgents/com.gemma4.forge.plist
launchctl unload ~/Library/LaunchAgents/com.gemma4.nginx.plist
```

`unload` stops the agent immediately and prevents it from starting at next login (until you `load` again).

## Editing a plist

launchd reads the plist at `load` time and caches the result. After you edit a plist you must `unload` then `load` again:

```bash
launchctl unload ~/Library/LaunchAgents/com.gemma4.forge.plist
cp /Users/noonoon/Dev/gemma-4/deploy/launchd/com.gemma4.forge.plist ~/Library/LaunchAgents/
launchctl load   ~/Library/LaunchAgents/com.gemma4.forge.plist
```

## Caveats

- **`bin/forge` must exist before you load `com.gemma4.forge.plist`.** Coder A is building this binary; until you run `make build`, the forge agent will spawn-fail every 30 seconds and ThrottleInterval will keep `launchd` busy logging the failure. Either don't load it yet, or build first.
- **`bin/tailer` must exist before you load `com.gemma4.tailer.plist`.** Same story — Coder D's binary, `make build` first.
- **`state/nginx-access.log` must exist** for the tailer to do anything useful. Comes from nginx; load the nginx agent first.
- **The nginx plist trick**: `docker compose up -d` exits as soon as the container is detached, which launchd interprets as a crash and (because `KeepAlive=true`) restarts in a loop. The plist works around this by running `edge_up.sh` then `exec tail -f /dev/null` — gives launchd a long-lived child to supervise. **Side effect**: `launchctl unload` of the nginx agent kills the `tail`, not the Docker container. Use `scripts/edge_down.sh` to actually stop nginx.
- **PATH does not include `uv`.** If bench-driven or `uv`-managed entrypoints get scripted into a plist later, extend the `EnvironmentVariables.PATH` entry to include `~/.cargo/bin` or wherever `uv` lives.
- **LaunchAgents only fire at login**, not at hard boot. If this Mac is set to auto-login the `noonoon` user, behavior is "boot-to-running"; if not, the stack waits for an interactive login.
- These are **per-user agents** (`~/Library/LaunchAgents/`), not system daemons (`/Library/LaunchDaemons/`). Do not move them under `/Library/`; the ProgramArguments hard-code paths under `/Users/noonoon` and assume the `noonoon` user's environment.
