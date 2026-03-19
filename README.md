# OpenCapy

**Your machines, mirrored. Code from anywhere.**

OpenCapy is a lightweight daemon + iOS app that lets you monitor and control Claude Code sessions from your iPhone. Claude Code runs on your Mac or Linux VM. You watch it, approve prompts, browse changed files, and drop into a full interactive terminal — all from your phone.

## Install

### Mac
```bash
brew tap richardyc/opencapy
brew install opencapy
opencapy install   # installs LaunchAgent, auto-starts daemon at login
```

### Linux
```bash
curl -fsSL https://github.com/richardyc/opencapy/releases/latest/download/opencapy_linux_amd64.tar.gz | tar xz
sudo mv opencapy /usr/local/bin/
opencapy install        # installs user systemd service, starts daemon, injects shell hook
source ~/.bashrc        # or open a new terminal
```

> Swap `amd64` for `arm64` if needed. Check with `uname -m` (`x86_64` = amd64, `aarch64` = arm64).
>
> The daemon runs as a **user systemd service** (`~/.config/systemd/user/`) — no `sudo` required after the binary is in place.

## Quick start

```bash
# 1. Start a session (daemon auto-starts if not running)
cd ~/myproject
opencapy              # opens full-screen TUI session chooser

# 2. Inside the session, run Claude Code
claude

# 3. Pair your iPhone
opencapy qr           # shows QR code — scan with the iOS app
```

The iOS app connects via **relay** (default, works everywhere — no VPN needed), Tailscale, or SSH tunnel. Sessions appear automatically within 2 seconds of creation — no daemon restart needed.

## iOS App

Download on the App Store or TestFlight. Source: [richardyc/opencapy-ios](https://github.com/richardyc/opencapy-ios)

**Features:**
- Real-time interactive terminal (PTY) with full ANSI color and Unicode rendering
- Approve/deny Claude Code prompts with one tap
- Native image paste — pick a photo on iOS, it appears inline in Claude Code via clipboard (no file path, no confirmation dialog)
- File browser and editor — browse, view diffs, and edit files directly
- Git source control — stage/unstage, commit, view diffs
- Event timeline — approval prompts, task completion, crashes
- Chat mode — Cursor-style conversation view with message bubbles, tool summaries, thinking indicator
- Voice input — on-device speech recognition, no API key needed
- Push notifications and lock screen Live Activities when app is backgrounded
- Session list with tinted glass cards, last user message preview, model name display
- Create and delete sessions from iOS
- Auto-reconnects when network/VPN comes back
- **Relay connection** — scan QR, done. No VPN, no port forwarding, works with any existing VPN
- Tailscale and SSH tunnel also supported for direct/private connections

## Connection methods

| Method | Setup | Works with existing VPN | Direct / private |
|--------|-------|------------------------|-----------------|
| **Relay** (default) | Scan QR — done | ✅ Any VPN | Routes via relay.opencapy.dev |
| **Tailscale** | Install Tailscale on both devices | ⚠️ May conflict | ✅ Direct WireGuard |
| **SSH** | Expose SSH port, add iOS public key | ✅ | ✅ Direct tunnel |

The relay is a Cloudflare Durable Objects WebSocket broker. No ports are opened on your machine. The pairing token (`~/.opencapy/relay_token.json`) is 256-bit random and is generated once — keep it private.

## CLI reference

```bash
opencapy                    # full-screen TUI session chooser (↑↓ navigate, Enter attach, d kill, n new)
opencapy [name]             # attach to named session (error if not found; use 'new' to create)
opencapy new [name]         # create new session (name defaults to cwd basename)
opencapy new [name] --claude # create session with claude directly
opencapy attach [name]      # reattach to existing session
opencapy kill [name]        # kill a session
opencapy approve [name]     # send approval keystroke (y + Enter)
opencapy deny [name]        # send deny keystroke (n + Enter)
opencapy status             # daemon health, connected iOS clients, session count
opencapy qr                 # print iOS pairing QR code
opencapy daemon             # start daemon in foreground (for debugging)
opencapy install            # install as system service (LaunchAgent on Mac, systemd on Linux)
opencapy update             # upgrade via brew and restart daemon
opencapy version            # print version + build info
```

**Session persistence:** `opencapy new` creates a daemon-owned PTY session. Detach with Enter then `~.` — the session keeps running. Reattach later with `opencapy attach <name>` or from the TUI.

## How it works

1. `opencapy new` creates a daemon-owned PTY session registered in `~/.opencapy/sessions.json`
2. The daemon owns the PTY directly — no tmux required
3. The shim (`opencapy shim -- claude`) wraps Claude Code to intercept hook events and stream PTY output to the daemon
4. Events stream to the iOS app over WebSocket (port 7242): approvals, tool executions, done, crashes
5. PTY output is buffered in a 2MB ring buffer per session and streamed live to iOS
6. For approval events, the daemon sends the tool context so iOS shows the exact permission being requested
7. Native image paste: iOS sends PNG bytes → daemon writes to `/tmp`, sets Mac clipboard via `osascript`, sends Ctrl+V to the session → Claude Code pastes the image inline
8. Chat messages from iOS are injected via bracketed paste (direct shim channel) — no timing hacks
9. The daemon reads Claude Code's JSONL transcript to provide chat history and model/token metadata
10. When the iOS app is backgrounded, push notifications and lock screen Live Activities fire via APNs

## Session detach and reattach

```bash
# Detach from any session without killing it
Enter then ~.

# Reattach later
opencapy attach myproject
```

Sessions survive SSH disconnects, terminal window closes, and daemon restarts.

## WebSocket protocol

The daemon exposes a WebSocket server on port 7242.

### Daemon → iOS
| Message type | Description |
|---|---|
| `snapshot` | Full session list on connect (name, projectPath, lastOutput, created, lastActive, recentEvents, lastUserMessage) |
| `event` | Claude Code event (approval/thinking/file_edit/crash/done/output) |
| `file_event` | File changed by Claude Code (path, content, deleted flag) |
| `file_tree` | Directory tree response |
| `file_content` | File read response (path, content base64, size) |
| `file_write_ack` | File write confirmation |
| `pane_content` | Scrollback history (base64-encoded PTY bytes) |
| `pty_output` | Raw terminal bytes (base64, sent only to owning client) |
| `session_created` | New session created via iOS request |
| `image_pasted` | Clipboard set and Ctrl+V sent |
| `chat_history` | Parsed JSONL transcript as user→assistant turns |
| `pong` | Heartbeat response |
| `error` | Error message |

### iOS → Daemon
| Message type | Description |
|---|---|
| `approve` | Send approval keystroke to session |
| `deny` | Send deny keystroke to session |
| `send_keys` | Send arbitrary keys to session (appends Enter) |
| `kill_session` | Kill session and unregister |
| `refresh_sessions` | Request fresh snapshot (requesting client only) |
| `paste_image` | PNG bytes (base64); daemon sets Mac clipboard + sends Ctrl+V |
| `register_push` | Register APNs device token |
| `new_session` | Create session (mode, project_path, launch_mode) |
| `list_dir` | Request directory tree for a path |
| `file_read` | Request file content |
| `file_write` | Write file content (base64) |
| `capture_pane` | Fetch PTY scrollback history |
| `open_pty` | Subscribe iOS client to session PTY output |
| `pty_input` | Send bytes to PTY (base64) |
| `pty_resize` | Resize PTY (cols/rows) |
| `close_pty` | Unsubscribe from PTY output |
| `chat_send` | Send chat message (injected via shim bracketed paste or direct PTY write) |
| `chat_send_image` | Send PNG image (base64) to session (sets clipboard, Ctrl+V) |
| `answer_question` | Answer AskUserQuestion prompt (session, answers map) |
| `request_chat_history` | Request parsed JSONL transcript |
| `register_live_activity` | Register per-activity APNs token (session, token, machine) |
| `ping` | Heartbeat (30s interval) |

### HTTP endpoints
| Endpoint | Description |
|---|---|
| `GET /qr` | Pairing QR code as PNG |
| `GET /pair` | Pairing info as JSON |
| `GET /health` | Daemon status and connected client count |

## APNs push notifications

Push notifications (lock screen alerts when app is backgrounded) require an Apple Developer
account ($99/yr) and a one-time credential setup.

### Getting your credentials

1. Sign in to [developer.apple.com](https://developer.apple.com) → **Certificates, Identifiers & Profiles → Keys**
2. Click **+** → enable **Apple Push Notifications service (APNs)** → Continue → Register
3. **Download** the `.p8` file (only available once — save it somewhere safe)
4. Note the **Key ID** (10 chars) shown next to the key
5. Your **Team ID** (10 chars) is shown in the top-right corner of the developer portal

### Option A — Relay mode (recommended, zero per-user config)

If you deploy the relay, store credentials as Cloudflare secrets once:

```bash
cd relay
wrangler secret put APNS_KEY_P8    # paste the full .p8 file content
wrangler secret put APNS_KEY_ID    # e.g. ABC1234DEF
wrangler secret put APNS_TEAM_ID   # e.g. XYZ9876543
wrangler deploy
```

### Option B — Direct mode (embedded at build time)

```bash
cp internal/push/credentials_release.go.template \
   internal/push/credentials_release.go
# Edit credentials_release.go — fill in KeyID, TeamID, paste .p8 content
go build -tags release ./...
```

`credentials_release.go` is gitignored. Release binaries should be built with `-tags release`.

### Option C — config.json (advanced / self-hosted)

```json
{
  "port": 7242,
  "apns": {
    "key_path": "~/.opencapy/AuthKey_XXXXXXXXXX.p8",
    "key_id": "XXXXXXXXXX",
    "team_id": "YYYYYYYYYY",
    "bundle_id": "dev.opencapy.app",
    "production": true
  }
}
```

Without any of the above, the daemon runs fine — push notifications are disabled, all other features work.

## Requirements

- macOS 13+ or Linux (amd64/arm64)
- For push notifications: Apple Developer Program membership

## Building a client

OpenCapy is an open protocol — anyone can build a client that connects to the daemon. The WebSocket message format is documented in the [WebSocket protocol](#websocket-protocol) section above.

## License

MIT
