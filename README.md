# OpenCapy

**Your machines, mirrored. Code from anywhere.**

OpenCapy is a mobile-native agentic code editor. Claude Code runs on your Mac or Linux VM. You direct it from your phone — approve prompts from your lock screen, view file diffs, monitor training runs on your Dynamic Island.

## Install

### Mac
```bash
brew tap richardyc/opencapy https://github.com/richardyc/opencapy
brew install opencapy
opencapy install   # installs LaunchAgent, starts daemon at login
```

### Linux
```bash
curl -fsSL https://get.opencapy.dev | sh
opencapy daemon &  # or: sudo systemctl enable --now opencapy
```

## Usage

```bash
opencapy                    # new session (name = current dir)
opencapy myproject          # new session with name
opencapy ls                 # list all sessions
opencapy attach myproject   # reattach to session
opencapy kill myproject     # kill session
opencapy approve myproject  # send approval keystroke
opencapy deny myproject     # send deny keystroke
opencapy status             # daemon health + connected iOS devices
opencapy qr                 # show iOS pairing QR code
opencapy daemon             # start daemon in foreground
opencapy install            # install as system service (LaunchAgent / systemd)
opencapy version            # print version
```

## How it works

1. `opencapy` creates a tmux session and registers it
2. `opencapy daemon` watches all sessions via `tmux capture-pane` every 500ms
3. Claude Code approval prompts, file edits, crashes detected via regex
4. Events streamed to iOS app via WebSocket over Tailscale (Mac) or SSH tunnel (Linux)
5. iOS app shows live session mirror, approval cards, file diffs, Dynamic Island progress

## Requirements

- macOS 13+ or Linux (amd64/arm64)
- tmux 3.0+
- For iOS: Tailscale (Mac) or SSH access (Linux)

## iOS App

Coming soon — TestFlight beta Month 3.
