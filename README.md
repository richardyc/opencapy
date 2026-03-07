# OpenCapy

<!-- TODO: migrate Homebrew formula to a separate tap repo (e.g. richardyc/homebrew-opencapy).
     GoReleaser should push Formula/opencapy.rb there instead of this repo, so branch
     protection on main here stays strict (PRs required). See .goreleaser.yaml brews.repository. -->

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

## CLI reference

```bash
opencapy                    # new session (name = cwd basename), daemon auto-starts
opencapy myproject          # new session with explicit name
opencapy ls                 # list all registered sessions
opencapy attach myproject   # reattach to existing session
opencapy kill myproject     # kill a session
opencapy approve myproject  # send approval keystroke (y + Enter)
opencapy deny myproject     # send deny keystroke (n + Enter)
opencapy status             # daemon health, connected iOS clients, registered devices
opencapy qr                 # print iOS pairing QR code (Tailscale hostname auto-detected)
opencapy daemon             # start daemon in foreground (for debugging)
opencapy install            # install as system service (LaunchAgent on Mac, systemd on Linux)
opencapy version            # print version + build info
```

## How it works

1. `opencapy` creates a tmux session with the capybara brown status bar and registers it
2. The daemon (`opencapy daemon`, runs as a system service) polls sessions every 500ms via `tmux capture-pane`
3. Detects Claude Code events via regex: approval prompts, file edits, crashes, task completion
4. Streams events to the iOS app over WebSocket (port 7242) via Tailscale (Mac) or SSH tunnel (Linux)
5. New sessions are hot-reloaded — no daemon restart required
6. When iOS app is backgrounded, push notifications are delivered via APNs

## WebSocket protocol

The daemon exposes a WebSocket server on port 7242.

### Daemon → iOS
| Message type | Description |
|---|---|
| `snapshot` | Full session list on connect |
| `event` | Claude Code event (approval/thinking/file_edit/crash/done) |
| `file_event` | File changed by Claude Code (path + content) |
| `file_tree` | Directory tree response |
| `file_content` | File read response |
| `file_write_ack` | File write confirmation |
| `pty_output` | Raw terminal bytes (base64) |
| `pong` | Heartbeat response |

### iOS → Daemon
| Message type | Description |
|---|---|
| `approve` | Send approval keystroke to session |
| `deny` | Send deny keystroke to session |
| `send_keys` | Send arbitrary keys to session |
| `register_push` | Register APNs device token |
| `list_dir` | Request directory tree for a path |
| `file_read` | Request file content |
| `file_write` | Write file content (base64) |
| `open_pty` | Open a PTY for raw terminal access |
| `pty_input` | Send bytes to PTY (base64) |
| `pty_resize` | Resize PTY (cols/rows) |
| `ping` | Heartbeat |

### HTTP endpoints
| Endpoint | Description |
|---|---|
| `GET /qr` | Pairing QR code as PNG |
| `GET /pair` | Pairing info as JSON |

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

## Requirements

- macOS 13+ or Linux (amd64/arm64)
- tmux 3.0+
- For iOS connectivity: Tailscale (Mac) or SSH access (Linux/VPS)
- For push notifications: Apple Developer Program membership

## iOS App

Coming soon on TestFlight. Source: [richardyc/opencapy-ios](https://github.com/richardyc/opencapy-ios)

## License

MIT
