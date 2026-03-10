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
- internal/platform/ — OS detection, LaunchAgent/systemd helpers
- Formula/opencapy.rb — homebrew formula (auto-updated by goreleaser)
- install/install.sh — Linux curl-install script

## Key decisions
- tmux IS the session registry (no separate DB)
- session-to-project mapping locked at creation time (cwd never remaps)
- WebSocket port: 7242 (hardcoded, env OPENCAPY_PORT to override)
- Config: ~/.opencapy/config.json
- CC output parsed via capture-pane + regex (clean text, no ANSI parsing)
- Session reconciler runs every 2s: diffs live `tmux list-sessions` against registry, adds/removes, broadcasts snapshot on changes
- PTY uses `tmux new-session -s ocpy_<name> -t <name>` (grouped session) so iOS has independent terminal sizing without resizing the Mac client
- `ocpy_*` sessions are internal PTY mirrors — filtered from snapshots and reconciler so they never appear in the iOS session list

## PTY design
- `internal/pty/pty.go` manages active PTY sessions
- `Open(sessionName, clientID, cols, rows, startDir)`: spawns `tmux new-session -s ocpy_<name> -t <name> -c <startDir>` with `TERM=xterm-256color` and `LANG=en_US.UTF-8`
- `startDir` is the session's project path (from registry); ensures iOS `session.projectPath` is correct
- Grouped session shares window group with target — updates from Mac flow to iOS in real-time
- On close: kills the grouped `ocpy_*` session so it doesn't linger
- PTY output forwarded via `srv.SendPTYOutput` only to the owning client

## Session reconciler (cmd_daemon.go)
- Runs every 2s, calls `tmux.ListSessions()`, skips `ocpy_*` sessions
- Adds newly-created Mac sessions to watcher + registry
- Removes killed sessions from watcher + registry
- Broadcasts snapshot to iOS on any change

## Native image paste (`paste_image` handler)
- iOS sends `{"type":"paste_image","session":"...","data":"<base64-PNG>"}`
- Daemon writes to `/tmp/opencapy_clip_<ns>.png`
- Runs `osascript -e 'set the clipboard to (read (POSIX file "...") as «class PNGf»)'` — works because daemon runs in Aqua LaunchAgent session with clipboard access
- Waits 300ms for NSPasteboard propagation, then sends `tmux send-keys -t session "\x16"` (Ctrl+V raw byte, no Enter)
- Claude Code reads the clipboard and inserts the image as a native vision block
- Sends `{"type":"image_pasted","payload":{"session":"..."}}` ack so iOS shows the context compose bar
- iOS is PNG (not JPEG) to avoid black-image artefacts from HEIC/P3 wide-gamut photos

## tmux helpers (internal/tmux/tmux.go)
- `SendKeys(session, keys)` — `tmux send-keys -t session "keys" Enter` (appends Enter)
- `SendKeyNoEnter(session, key)` — `tmux send-keys -t session "key"` (no Enter; used for C-v paste)

## Snapshot fields
Snapshot payload per session:
- `created` (ISO-8601) — from `#{session_created}`
- `last_active` (ISO-8601) — from `#{session_activity}`

## Path security
- `isPathAllowed` gates `file_write`, `file_read`, `list_dir` to registered project directories
- `/tmp` is always allowed (temporary uploads: images, etc.)

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
- `git_status` `{session}` → `git_status_result {session, branch, ahead, behind, files[], ok}`
- `git_stage` / `git_unstage` / `git_commit` / `git_diff` — git operations

## CLI design (bubbletea TUI)
- opencapy                    → full-screen TUI: list sessions, attach, create, kill
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
