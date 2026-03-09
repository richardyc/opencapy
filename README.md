# OpenCapy

**Your machines, mirrored. Code from anywhere.**

OpenCapy is a lightweight daemon + iOS app that lets you monitor and control Claude Code sessions from your iPhone. Claude Code runs on your Mac or Linux VM. You watch it, approve prompts, browse changed files, and drop into a terminal — all from your phone.

## Install

### Mac
```bash
brew tap richardyc/opencapy https://github.com/richardyc/opencapy
brew install opencapy
opencapy install   # installs LaunchAgent, auto-starts daemon at login
```

### Linux
```bash
curl -fsSL https://github.com/richardyc/opencapy/releases/latest/download/opencapy_linux_amd64.tar.gz | tar xz
sudo mv opencapy /usr/local/bin/
sudo opencapy install   # installs systemd service
```

> Swap `amd64` for `arm64` if needed. Check with `uname -m` (`x86_64` = amd64, `aarch64` = arm64).

## Quick start

```bash
# 1. Start a session (daemon auto-starts if not running)
cd ~/myproject
opencapy              # name defaults to current directory

# 2. Inside the session, run Claude Code
claude

# 3. Pair your iPhone
opencapy qr           # shows QR code — scan with the iOS app
```

The iOS app connects via Tailscale (Mac) or SSH tunnel (Linux/VPS). Sessions appear automatically within 2 seconds of creation — no daemon restart needed.

## iOS App

Download on the App Store or TestFlight. Source: [richardyc/opencapy-ios](https://github.com/richardyc/opencapy-ios)

**Features:**
- Real-time terminal mirror with full ANSI color rendering
- Approve/deny Claude Code prompts with one tap
- File browser and editor — browse, view diffs, and edit files directly
- Full interactive terminal (PTY) for running commands
- Event timeline — approval prompts, task completion, crashes
- Push notifications and lock screen Live Activities when app is backgrounded
- Connects via Tailscale (zero-config on Mac) or SSH tunnel (Linux/VPS)

## CLI reference

```bash
opencapy                    # interactive session chooser (fzf or numbered list)
opencapy [name]             # new session with name (or cwd basename if omitted)
opencapy attach [name]      # reattach to existing session
opencapy here               # new session per terminal tab (for VSCode/Cursor profiles)
opencapy ls                 # list all registered sessions
opencapy kill [name]        # kill a session
opencapy approve [name]     # send approval keystroke (y + Enter)
opencapy deny [name]        # send deny keystroke (n + Enter)
opencapy status             # daemon health, connected iOS clients, registered devices
opencapy qr                 # print iOS pairing QR code (Tailscale hostname auto-detected)
opencapy daemon             # start daemon in foreground (for debugging)
opencapy install            # install as system service (LaunchAgent on Mac, systemd on Linux)
opencapy install --vscode   # also configure VSCode/Cursor terminal profile to use opencapy here
opencapy update             # upgrade via brew and restart daemon
opencapy version            # print version + build info
```

## How it works

1. `opencapy [name]` creates a tmux session with a capybara brown status bar and registers it in `~/.opencapy/sessions.json`
2. The daemon polls every 500ms via `tmux capture-pane`, detecting Claude Code events by regex:
   - **Approval:** matches `do you want to proceed`, `[y/n]`, `❯ 1. yes`
   - **Crash:** matches `Traceback`, `panic:`, `fatal error:`
   - **Done:** matches `task complete`
3. Events stream to the iOS app over WebSocket (port 7242), along with live pane output (last 15 lines, 1s cooldown)
4. For approval events, the daemon scans a wider 50-line window to extract the `⏺ ToolName(...)` context shown in the iOS prompt card
5. New sessions are hot-reloaded every 2s — no daemon restart required
6. All tmux sessions (including those created outside opencapy) are auto-registered at daemon startup
7. When the iOS app is backgrounded, push notifications are delivered via APNs

## iOS connectivity

**Tailscale (recommended for Mac):**
Connect via your Tailscale hostname (e.g. `richard-mbp.tail12345.ts.net`) — no port forwarding needed. Run `opencapy qr` and scan with the iOS app to pair automatically.

**SSH tunnel (Linux/VPS):**
The iOS app generates an Ed25519 keypair, you add the public key to `~/.ssh/authorized_keys`, and the app creates a local port forward to the daemon. Private keys are stored in the iOS Keychain.

## WebSocket protocol

The daemon exposes a WebSocket server on port 7242.

### Daemon → iOS
| Message type | Description |
|---|---|
| `snapshot` | Full session list on connect (name, projectPath, lastOutput, recentEvents) |
| `event` | Claude Code event (approval/thinking/file_edit/crash/done) |
| `file_event` | File changed by Claude Code (path, content, deleted flag) |
| `file_tree` | Directory tree response (depth 3, excludes .git, node_modules, *.key, *.pem) |
| `file_content` | File read response (path, content base64, size — max 1MB) |
| `file_write_ack` | File write confirmation |
| `pane_content` | Scrollback history capture (300 lines by default) |
| `pty_output` | Raw terminal bytes (base64, sent only to owning client) |
| `session_created` | New session created via iOS request |
| `pong` | Heartbeat response |
| `error` | Error message |

### iOS → Daemon
| Message type | Description |
|---|---|
| `approve` | Send approval keystroke to session |
| `deny` | Send deny keystroke to session |
| `send_keys` | Send arbitrary keys to session |
| `register_push` | Register APNs device token |
| `new_session` | Create session (mode: "chat" launches claude, "terminal" opens shell) |
| `list_dir` | Request directory tree for a path |
| `file_read` | Request file content |
| `file_write` | Write file content (base64) |
| `capture_pane` | Fetch pane scrollback history |
| `open_pty` | Open a PTY for raw terminal access |
| `pty_input` | Send bytes to PTY (base64) |
| `pty_resize` | Resize PTY (cols/rows) |
| `close_pty` | Close PTY |
| `ping` | Heartbeat (30s interval) |

### HTTP endpoints
| Endpoint | Description |
|---|---|
| `GET /qr` | Pairing QR code as PNG |
| `GET /pair` | Pairing info as JSON |
| `GET /health` | Daemon status and connected client count |

## APNs push notifications

Push notifications require an Apple Developer account ($99/yr). Configure in `~/.opencapy/config.json`:

```json
{
  "port": 7242,
  "apns": {
    "key_path": "~/.opencapy/AuthKey_XXXXXXXXXX.p8",
    "key_id": "XXXXXXXXXX",
    "team_id": "YYYYYYYYYY",
    "bundle_id": "dev.opencapy.app",
    "production": false
  }
}
```

Without this config the daemon runs fine — push notifications are disabled, all other features work.

Notifications are only sent when no iOS clients are connected (i.e. app is backgrounded):
- **Approval needed** — Claude Code is waiting for your input
- **Session crashed** — with error detail
- **Task complete** — Claude Code finished

## Requirements

- macOS 13+ or Linux (amd64/arm64)
- tmux 3.0+
- For iOS connectivity: Tailscale (Mac) or SSH access (Linux/VPS)
- For push notifications: Apple Developer Program membership

## Building a client

OpenCapy is an open protocol — anyone can build a client that connects to the daemon. The WebSocket message format is documented in the [WebSocket protocol](#websocket-protocol) section above.

> **TODO:** publish a formal `PROTOCOL.md` once the protocol stabilizes.

## License

MIT
