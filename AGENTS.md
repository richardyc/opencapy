# AGENTS.md — opencapy (Go daemon)

## What this is
OpenCapy daemon: Go binary that watches tmux sessions and streams events to an iOS app via WebSocket.
Users run `opencapy` to create/manage tmux sessions via a bubbletea full-screen TUI. Daemon watches panes, detects CC events, pushes to iOS.

## Architecture
- cmd/opencapy/main.go — entrypoint, cobra CLI
- cmd/opencapy/cmd_tui.go — bubbletea full-screen TUI (session chooser)
- cmd/opencapy/cmd_session.go — session create/attach/kill commands
- cmd/opencapy/cmd_daemon.go — daemon command: watcher, reconciler, WS server
- internal/tmux/ — session lifecycle + capture-pane; SendKeys, SendKeyNoEnter
- internal/watcher/ — 500ms poll loop, CC event detection
- internal/fsevent/ — FSEvents (Mac) + inotify (Linux) file watcher
- internal/ws/ — WebSocket server (coder/websocket)
- internal/pty/ — PTY manager: grouped tmux sessions for iOS terminal
- internal/push/ — APNs push bridge
- internal/project/ — session->project registry (cwd-locked)
- internal/config/ — ~/.opencapy/config.json
- internal/relay/ — relay token (LoadOrCreate, PairURL, WSURL helpers)
- internal/platform/ — OS detection, LaunchAgent/systemd helpers
- relay/ — Cloudflare Workers + Durable Objects relay (TypeScript, deploy with `wrangler deploy`)
- Formula/opencapy.rb — homebrew formula lives in richardyc/homebrew-opencapy (separate tap repo, auto-updated by goreleaser on release)
- install/install.sh — Linux curl-install script

## Key decisions
- tmux IS the session registry (no separate DB)
- session-to-project mapping locked at creation time (cwd never remaps)
- WebSocket port: 7242 (hardcoded, env OPENCAPY_PORT to override)
- **Relay**: default pairing method. Token at `~/.opencapy/relay_token.json` (256-bit, generated once). QR encodes `opencapy://pair?type=relay&token=<hex>&relay=wss://relay.opencapy.dev&name=<hostname>`. Tailscale/SSH kept as advanced options.
- Relay token exposed via `/pair` JSON endpoint and `/qr` PNG endpoint
- Config: ~/.opencapy/config.json
- CC output parsed via capture-pane + regex (clean text, no ANSI parsing)
- Session reconciler runs every 2s: diffs live `tmux list-sessions` against registry, adds/removes, broadcasts snapshot on changes
- PTY uses `tmux new-session -s ocpy_<name> -t <name>` (grouped session) so iOS has independent terminal sizing without resizing the Mac client
- `ocpy_*` sessions are internal PTY mirrors — filtered from snapshots and reconciler so they never appear in the iOS session list

## Shim
The shim (`cmd/opencapy/cmd_shim.go`) is a PTY wrapper around Claude Code that connects back to the daemon via WebSocket. It handles:
- `pty_input` — raw keystroke injection
- `pty_resize` — terminal resize
- `inject_message` — writes chat text to the PTY (used by `chat_send` for direct sessions)
- `inject_image` — sends Ctrl+V to paste clipboard content (used by `chat_send_image`)
- `event` — title updates from daemon events

### Shim tmux dedup
When the shim runs inside a tmux session (`TMUX` env set), it sends `inside_tmux: true` in `register_session` / `reregister_session`. The daemon finds the parent tmux session by cwd and sets `parentTmux` on the direct session entry. Sessions with `parentTmux` are hidden from the snapshot (no duplicate entries). The claude session ID is persisted in the registry under the tmux session name so `sendChatHistory` resolves the JSONL path for the parent.

## Chat history
`parseChatHistory` reads Claude Code's JSONL transcript (`~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`) and returns `[]ChatTurn` with user text, claude text, tool count, tool names, model, stop reason, cost, and duration. `sendChatHistory` broadcasts parsed turns to all connected clients (no subscriber tracking — one-person project, 1-2 clients max).

**Orphan turn filter:** After parsing, turns with no Claude response (`ClaudeText == ""` and `ToolCount == 0`) are dropped — except the last turn (might be in-progress). These orphan turns come from user interrupts (`[Request interrupted by user]`) or abandoned messages. Purely structural check, no string matching.

## Chat send (`handleChatSend` / `handleChatSendImage`)
- `chat_send`: injects user text into the Claude Code PTY. For direct sessions, sends `inject_message` to the shim + Escape+Enter submit via `pty_input` (300ms + 100ms delays). For tmux sessions, uses `tmux send-keys` + Escape+Enter.
- `chat_send_image`: writes PNG to `/tmp`, sets Mac clipboard via `osascript`, sends Ctrl+V via `pty_input` (same flow as `paste_image`).
- `answer_question`: sends structured answers for `AskUserQuestion` prompts via `pendingAnswers` channel.

## Hook events
`toolSummary(toolName, toolInput)` extracts a short description from PreToolUse hooks — checks `file_path`, `command`, `pattern`, `query`, `url` keys, basenames file paths, truncates to 50 chars. Emitted as event content so iOS shows "Read server.go" instead of generic "Working".

## PTY design
- `internal/pty/pty.go` manages active PTY sessions
- `Open(sessionName, clientID, cols, rows, startDir)`: two-step creation:
  1. `tmux new-session -d -s ocpy_<name> -t <name> -c <startDir>` (detached, synchronous) to create the grouped session
  2. Apply styling: `set-option status-style bg=#7B5B3A,fg=#F5E6D3` and disable mouse: `set-option mouse off`
  3. `tmux attach-session -t ocpy_<name>` opens the actual PTY
- Mouse is explicitly disabled on the grouped session to prevent tmux from emitting mouse-tracking escape sequences that would intercept iOS UIScrollView pan gestures in SwiftTerm
- `startDir` is the session's project path (from registry); ensures iOS `session.projectPath` is correct
- Grouped session shares window group with target — updates from Mac flow to iOS in real-time
- On close: kills the grouped `ocpy_*` session so it doesn't linger
- On PTY start failure: cleans up the already-created grouped session
- PTY output forwarded via `srv.SendPTYOutput` only to the owning client

## Session reconciler (cmd_daemon.go)
- Runs every 2s, calls `tmux.ListSessions()`, skips `ocpy_*` sessions
- Adds newly-created Mac sessions to watcher + registry
- Removes killed sessions from watcher + registry
- Broadcasts snapshot to iOS on any change

## Session creation (`handleNewSession` in internal/ws/server.go)
- `new_session` handler expands a leading `~` in the `cwd` field before passing it to tmux (tmux's `-c` flag does not perform tilde expansion itself)
- `snapshotSessions` uses `CapturePaneOutput(name, 20, true)` — ANSI preserved for snapshots
- `capture_pane` handler uses `CapturePaneOutput(name, lines, false)` — plain text for SwiftTerm scrollback history

## Native image paste (`paste_image` handler)
- iOS sends `{"type":"paste_image","session":"...","data":"<base64-PNG>"}`
- Daemon writes to `/tmp/opencapy_clip_<ns>.png`
- Runs `osascript -e 'set the clipboard to (read (POSIX file "...") as «class PNGf»)'` — works because daemon runs in Aqua LaunchAgent session with clipboard access
- Waits 300ms for NSPasteboard propagation, then sends `tmux send-keys -t session "\x16"` (Ctrl+V raw byte, no Enter)
- Claude Code reads the clipboard and inserts the image as a native vision block
- Sends `{"type":"image_pasted","payload":{"session":"..."}}` ack; iOS should refocus the terminal (no compose bar shown)
- Improved error acks: decode failures, file write errors, and osascript failures each send an error ack back to iOS
- iOS sends PNG (not JPEG) to avoid black-image artefacts from HEIC/P3 wide-gamut photos

## tmux helpers (internal/tmux/tmux.go)
- `SendKeys(session, keys)` — `tmux send-keys -t session "keys" Enter` (appends Enter)
- `SendKeyNoEnter(session, key)` — `tmux send-keys -t session "key"` (no Enter; used for C-v paste)
- `CapturePaneOutput(sessionName string, lines int, withEscape bool)` — runs `tmux capture-pane -p -t session -S -<lines>`; `withEscape=true` adds `-e` flag to preserve ANSI escape sequences (used for watcher polling and snapshots); `withEscape=false` returns plain text (used for `capture_pane` / SwiftTerm scrollback history — ANSI escape sequences cause cursor positioning that prevents scrollback lines from accumulating)

## Snapshot fields
Snapshot payload per session:
- `created` (ISO-8601) — from `#{session_created}`
- `last_active` (ISO-8601) — from `#{session_activity}`

## Path security
- `isPathAllowed` gates `file_write`, `file_read`, `list_dir` to registered project directories
- `/tmp` is always allowed (temporary uploads: images, etc.)

## Live Activity push (internal/push/push.go)
- `LiveActivityContentState` struct mirrors the Swift `ContentState`: `SessionName`, `MachineName`, `WorkingDirectory`, `Status`, `LastOutput`, `NeedsApproval`, `ApprovalContent`
- `(*Registry).SendLiveActivity(activityToken string, state LiveActivityContentState)` — sends an ActivityKit push via APNs to update a live activity by its per-activity push token; works when the iOS app is backgrounded or on the lock screen
- `liveActivityTokens map[string]liveActivityEntry` in `internal/ws/server.go` maps session name → (APNs token, machineName), protected by a mutex
- `register_live_activity` WebSocket handler stores the per-activity token: `{"type":"register_live_activity","session":"...","token":"<apns-token>","machine":"<machine-name>"}`
- `broadcastLoop` in `internal/ws/server.go`: after normal event broadcast, checks `liveActivityTokens` and calls `push.SendLiveActivity` for approval, done, and crash events
- WebSocket read limit increased to 20MB (was default 32KB — too small for base64 image payloads)

## iOS app companion
The opencapy-ios app connects to this daemon. For iOS-specific architecture, settled UI design,
and the full WebSocket protocol reference, see AGENTS.md in the opencapy-ios repository.

Relevant protocol messages this daemon must support:
- `open_pty` / `pty_input` / `pty_output` / `pty_resize` / `close_pty` — PTY multiplexing
- `paste_image` `{session, data: base64-PNG}` → sets Mac clipboard, sends C-v to tmux, acks `image_pasted`
- `kill_session` `{session}` — kill tmux session, unregister, broadcast snapshot
- `refresh_sessions` — send fresh snapshot to requesting client only
- `file_write` `{path, content: base64}` — write arbitrary bytes
- `file_write_ack` `{path, ok}` — write confirmation
- `send_keys` / `approve` / `deny` / `capture_pane` / `list_dir` / `file_read` — existing ops
- `chat_send` `{session, chat_content}` — inject chat message via shim/tmux + Escape+Enter submit
- `chat_send_image` `{session, chat_image_b64}` — clipboard paste image into Claude Code
- `answer_question` `{session, answers}` — structured AskUserQuestion response
- `register_live_activity` `{session, token, machine}` — store per-activity APNs token for live activity updates
- `git_status` `{session}` → `git_status_result {session, branch, ahead, behind, files[], ok}`
- `git_stage` / `git_unstage` / `git_commit` / `git_diff` — git operations

## CLI design (bubbletea TUI)
- opencapy                    → full-screen TUI: list sessions, attach, create, kill; sessions sorted by `LastActive` descending (most recently active first)
- opencapy <name>             → attach to named session (error if not found)
- opencapy new [name]         → create new session (name defaults to cwd basename)
- opencapy ls                 → same as bare opencapy
- opencapy attach <name>      → reattach to session
- opencapy kill <name>        → kill session
- opencapy here               → new session per terminal tab (VSCode/Cursor profile use)
- opencapy status             → daemon health + connected iOS devices
- opencapy qr                 → show Tailscale pairing QR
- opencapy install            → install LaunchAgent (Mac) or systemd unit (Linux)
- opencapy daemon             → start daemon in foreground (LaunchAgent calls this)
- opencapy update             → brew upgrade + daemon restart
