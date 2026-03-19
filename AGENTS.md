# AGENTS.md — opencapy (Go daemon)

## What this is
OpenCapy daemon: Go binary that owns PTY sessions directly and streams events to an iOS app via WebSocket.
Users run `opencapy` to create/manage sessions via a bubbletea TUI. Daemon watches panes, detects Claude Code events, pushes to iOS. No tmux dependency.

## Rebuild & Deploy (Dev)

```bash
cd ~/dev/opencapy \
  && go build -o /opt/homebrew/bin/opencapy ./cmd/opencapy/ \
  && kill $(lsof -i :7242 | grep LISTEN | awk '{print $2}') \
  && sleep 2  # launchd auto-restarts via com.opencapy.daemon plist
```

**Verify:**
```bash
tail -5 /tmp/opencapy.err        # should show re-registered / Client connected
lsof -i :7242 | grep LISTEN      # new PID listening
ls ~/.opencapy/daemon.sock       # Unix socket for CLI commands
```

## Key Paths

| What | Path |
|------|------|
| Binary | `/opt/homebrew/bin/opencapy` |
| Launch agent | `~/Library/LaunchAgents/com.opencapy.daemon.plist` |
| Stdout log | `/tmp/opencapy.log` |
| Stderr log | `/tmp/opencapy.err` |
| Unix socket | `~/.opencapy/daemon.sock` |
| Claude hooks | `~/.claude/settings.json` |
| JSONL transcripts | `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl` |

## Architecture

- `cmd/opencapy/main.go` — entrypoint, cobra CLI
- `cmd/opencapy/cmd_tui.go` — bubbletea full-screen TUI (session chooser)
- `cmd/opencapy/cmd_session.go` — session create/attach/kill commands
- `cmd/opencapy/cmd_daemon.go` — daemon: session manager, watcher, WS server, socket listener
- `cmd/opencapy/cmd_shim.go` — PTY wrapper for Claude Code (hooks, chat injection, title updates)
- `internal/session/` — PTY session lifecycle (ring buffer, fan-out, Unix socket protocol)
- `internal/watcher/` — event emission (FeedOutput callback, no polling)
- `internal/fsevent/` — FSEvents (Mac) + inotify (Linux) file watcher
- `internal/ws/` — WebSocket server (coder/websocket)
- `internal/push/` — APNs push bridge
- `internal/project/` — session→project registry (cwd-locked)
- `internal/config/` — ~/.opencapy/config.json
- `internal/relay/` — relay token (LoadOrCreate, PairURL, WSURL helpers)
- `internal/platform/` — OS detection, LaunchAgent/systemd helpers
- `relay/` — Cloudflare Workers + Durable Objects relay (TypeScript, deploy with `wrangler deploy`)

## Session types

Two types, both visible to iOS:

**Daemon sessions** (`session_type: "daemon"`)
- Created by `opencapy new` or from iOS via `new_session`
- Owned by daemon: PTY lives in `internal/session/`, ring buffer 2MB
- Survive terminal close; persist until killed
- Unix socket: `{"op":"new","name":"foo","cwd":"/x","rows":50,"cols":200}`
- Attach with `opencapy attach foo`; detach with Enter then `~.`
- `OPENCAPY_SESSION=<name>` injected into PTY env so child shims register as nested

**Direct sessions** (`session_type: "direct"`)
- Created by the claude shim when user runs `claude` in any terminal
- Shim owns the PTY; daemon tracks metadata and ring buffer
- Registered via `register_session` / `reregister_session` WebSocket messages
- Hidden from list when `parentSession` is set (shim running inside a daemon session)

## Unix socket protocol (CLI ↔ daemon)

**JSON request/response (then close):**
```json
{"op":"list"}                                              → {"ok":true,"sessions":[...]}
{"op":"new","name":"foo","cwd":"/x","rows":50,"cols":200}  → {"ok":true,"name":"foo"}
{"op":"kill","name":"foo"}                                 → {"ok":true}
{"op":"input","name":"foo","data":"y\n"}                   → {"ok":true}
{"op":"attach","name":"foo","rows":50,"cols":200}          → {"ok":true} then binary mode
```

**Binary framing (after attach ack):**
```
[1 byte type][2 bytes length BE][payload]
1 = output (daemon→client)  2 = input (client→daemon)
3 = resize (4 bytes: rows u16 BE + cols u16 BE)  4 = detach (0 bytes)
```

## Shim

`cmd_shim.go` wraps Claude Code with a PTY. On startup it connects to daemon via WebSocket, registers as a session, then relays PTY I/O bidirectionally.

**Messages handled:**
- `pty_input` — raw keystroke injection to PTY
- `pty_resize` — terminal resize
- `inject_message` — bracketed paste + 50ms + Enter (avoids Ink autocomplete issue)
- `inject_image` — sends Ctrl+V (0x16) to paste clipboard content
- `event` — title updates from daemon events

**Nested sessions:** when `OPENCAPY_SESSION` is set (shim running inside a daemon session), shim sends `inside_parent: true` and `parent_session: <name>` in `register_session`. The daemon sets `parentSession` on the direct session so it's hidden from snapshots (no duplicate entries). Claude session ID persists under the parent name for `sendChatHistory`.

**Tab titles:** shim writes `ESC]0;claude · <displayName> · <status>BEL` to stderr. Stderr goes to PTY output → flows through daemon ring buffer → reaches attached CLI client automatically. When inside a daemon session, `displayName` = `OPENCAPY_SESSION` value so tabs show the outer session name, not the internal child name.

## Chat send (`handleChatSend`)

- Direct sessions or daemon sessions with attached shim → `forwardToShim("inject_message")` → shim uses bracketed paste internally (no delays needed on Go side)
- Pure daemon sessions (plain shell, no claude) → `sess.Write([]byte(text + "\n"))` directly

## Ring buffer & replay

- Ring buffer: 2MB per session (daemon sessions in `session.go`, direct sessions in `ws/server.go` `directBufMax`)
- `Subscribe()` returns full ring buffer snapshot (no terminal reset prefix — causes feedback loops)
- `sanitizeForReplay()` strips `ESC[?1049h/l` (alternate screen), `ESC[2J/3J` (erase) before sending to CLI clients — prevents hiding scrollback history on reattach
- Channel cap: 4096 chunks (16MB buffer) — prevents slow-client disconnect during large output bursts

## iOS `pane_content` (ring buffer to iOS)

- Sent base64-encoded, not raw string — avoids 6x JSON expansion from `\u001b` escaping
- `RawTerminalView.swift` decodes: `Data(base64Encoded: content)` → `tv.feed(byteArray:)`

## Terminal query filtering

`FilterTerminalQueries` (in `internal/session/client.go`) strips cursor/color queries from PTY output before writing to the outer Mac terminal. Prevents the outer terminal from responding on stdin, which would create a feedback loop where responses appear as garbage typed input. Same `FilterTerminalResponses` function used server-side for iOS `pty_input`.

## Session reconciler (cmd_daemon.go)

Runs every 2s, diffs daemon sessions against registry. Adds new sessions to watcher + registry, removes dead ones, broadcasts snapshot on changes.

## Hook routing

Shim sets `OPENCAPY_SESSION` env var → Claude Code inherits it → hook curl includes `?session=$OPENCAPY_SESSION` → daemon reads session name from query param. No CWD guessing, no race conditions with parallel sessions.

## Chat history

`parseChatHistory` reads Claude Code's JSONL transcript and returns `[]ChatTurn`. Orphan turns (no claude response, no tools) are dropped except the last (may be in-progress).

## Path security

`isPathAllowed` gates `file_write`, `file_read`, `list_dir` to registered project directories. `/tmp` always allowed.

## Session snapshot fields

- `name`, `project_path`, `last_output`, `created`, `last_active`
- `recent_events` — last 50 non-output events
- `session_type` — `"daemon"` or `"direct"`
- `last_user_message` — from JSONL transcript
- `model_name`, `context_tokens`, `max_context` — from JSONL
- `branch` — git branch at session launch

## CLI design

```
opencapy                    → full-screen TUI: list sessions, ↑↓ navigate, Enter attach, d kill, n new
opencapy <name>             → attach to named session (error if not found)
opencapy new [name]         → create new session (name defaults to cwd basename)
opencapy new [name] --claude → create session with claude directly
opencapy attach <name>      → reattach to session
opencapy kill <name>        → kill session
opencapy approve <name>     → send y\n to session
opencapy deny <name>        → send n\n to session
opencapy status             → daemon health + session count
opencapy qr                 → show pairing QR
opencapy install            → install LaunchAgent (Mac) or systemd unit (Linux)
opencapy daemon             → start daemon in foreground
opencapy update             → brew upgrade + daemon restart
opencapy version            → print version + build info
```

## iOS app companion

For iOS-specific architecture and protocol reference, see AGENTS.md in `opencapy-ios`.

**Protocol messages daemon must support:**
- `open_pty` / `pty_input` / `pty_output` / `pty_resize` / `close_pty`
- `new_session` `{mode, project_path, launch_mode}` — creates daemon PTY session
- `kill_session` `{session}`
- `paste_image` `{session, data: base64-PNG}` → sets Mac clipboard, sends C-v, acks `image_pasted`
- `send_keys` / `approve` / `deny` / `capture_pane` / `refresh_sessions`
- `chat_send` `{session, chat_content}` — routed via shim inject_message or direct PTY write
- `chat_send_image` `{session, chat_image_b64}` — clipboard paste
- `answer_question` `{session, answers}`
- `register_live_activity` `{session, token, machine}`
- `git_status` / `git_stage` / `git_unstage` / `git_commit` / `git_diff`
- `file_write` / `file_read` / `list_dir`
