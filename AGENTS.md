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
