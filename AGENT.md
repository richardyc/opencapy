# OpenCapy Daemon — Developer Guide

## Code Principles

- Less code, not more. Delete dead code immediately.
- No unnecessary if/else — flatten logic, prefer early returns.
- No hardcoded values — pass data through env vars, query params, or function args.
- One source of truth per concept — don't duplicate state across multiple lookups.

## Rebuild & Deploy (Dev)

**Important**: `lsof -ti :7242` returns ALL processes on that port (daemon + shims). Use `grep LISTEN` to target only the daemon process.

```bash
cd ~/dev/opencapy \
  && go build -o /opt/homebrew/bin/opencapy ./cmd/opencapy/ \
  && kill $(lsof -i :7242 | grep LISTEN | awk '{print $2}') \
  && sleep 2 \
  && launchctl kickstart gui/$(id -u)/com.opencapy.daemon
```

**Verify:**
```bash
tail -5 /tmp/opencapy.err        # should show re-registered / Client connected
lsof -i :7242 | grep LISTEN      # new PID listening
```

**If hooks changed** (e.g. `cmd_init.go` hook command), update `~/.claude/settings.json` too — either re-run `opencapy install` or edit manually. Running sessions must be restarted to pick up new env vars.

## Key Paths

| What | Path |
|------|------|
| Binary | `/opt/homebrew/bin/opencapy` |
| Launch agent | `~/Library/LaunchAgents/com.opencapy.daemon.plist` |
| Stdout log | `/tmp/opencapy.log` |
| Stderr log | `/tmp/opencapy.err` |
| Claude hooks | `~/.claude/settings.json` |
| JSONL transcripts | `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl` |

## Architecture

- `cmd/opencapy/` — CLI entry point (daemon, shim, init, session subcommands)
- `internal/ws/server.go` — WebSocket server, snapshot building, chat history parsing, hook handler
- `internal/watcher/` — Pane watcher (detects approval, crash, done, running events)
- `internal/tmux/` — tmux session management

### Hook Routing

Shim → sets `OPENCAPY_SESSION` env var → Claude Code inherits it → hook curl includes `?session=$OPENCAPY_SESSION` → daemon reads session name from query param. No CWD guessing, no race conditions with parallel sessions.

### Daemon ↔ iOS Protocol

Snapshots are sent on connect and on `refresh_sessions`. Key fields:
- `name`, `project_path`, `last_output`, `created`, `last_active`
- `recent_events` — last 50 watcher events (approval/crash/done/running)
- `session_type` — "tmux" or "direct"
- `last_user_message` — last user input from JSONL transcript
- `model_name`, `context_tokens`, `max_context` — model info from JSONL
- `branch` — git branch at session launch
