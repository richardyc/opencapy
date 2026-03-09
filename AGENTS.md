# AGENTS.md — opencapy (Go daemon)

## What this is
OpenCapy daemon: Go binary that watches tmux sessions and streams events to an iOS app via WebSocket.
Users run `opencapy` to create/manage tmux sessions. Daemon watches panes, detects CC events, pushes to iOS.

## Architecture
- cmd/opencapy/main.go — entrypoint, cobra CLI
- internal/tmux/ — session lifecycle + capture-pane
- internal/watcher/ — 500ms poll loop, CC event detection
- internal/fsevent/ — FSEvents (Mac) + inotify (Linux) file watcher
- internal/ws/ — WebSocket server (coder/websocket)
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

## iOS app companion
The opencapy-ios app connects to this daemon. For iOS-specific architecture, settled UI design,
and the full WebSocket protocol reference, see AGENTS.md in the opencapy-ios repository.

Relevant protocol messages this daemon must support:
- `open_pty` / `pty_input` / `pty_output` / `pty_resize` / `close_pty` — PTY multiplexing
- `file_write` `{path, content: base64}` — write arbitrary bytes (used for JPEG uploads from iOS)
- `file_write_ack` `{path, ok}` — write confirmation
- `send_keys` / `approve` / `deny` / `capture_pane` / `list_dir` / `file_read` — existing ops
- `git_status` `{session}` → `git_status_result {session, branch, ahead, behind, files[], ok}`
- `git_stage` `{session, path}` → `git_status_result` (updated)
- `git_unstage` `{session, path}` → `git_status_result` (updated)
- `git_commit` `{session, message}` → `git_status_result` (after commit)
- `git_diff` `{session, path, staged}` → `git_diff_result {path, before, after, ok}`

## Path security
- `isPathAllowed` gates `file_write`, `file_read`, `list_dir` to registered project directories
- `/tmp` is always allowed (temporary uploads: images, etc.)

## CLI design (like tmux — bare command does the thing)
- opencapy                    → new session, name = current dir basename
- opencapy <name>             → new session with name
- opencapy ls                 → list sessions + status
- opencapy attach <name>      → reattach to session
- opencapy kill <name>        → kill session
- opencapy status             → daemon health + connected iOS devices
- opencapy qr                 → show Tailscale pairing QR
- opencapy install            → install LaunchAgent (Mac) or systemd unit (Linux)
- opencapy daemon             → start daemon in foreground (LaunchAgent calls this)
