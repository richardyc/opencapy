# Capy Mac — Product Requirements Document

> A native macOS terminal app for managing Claude Code sessions, forked from [cmux](https://github.com/manaflow-ai/cmux) and integrated with the opencapy daemon.

## Table of Contents

1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Why Fork cmux](#why-fork-cmux)
4. [What We Keep from cmux](#what-we-keep-from-cmux)
5. [What We Remove from cmux](#what-we-remove-from-cmux)
6. [What We Add](#what-we-add)
7. [Daemon Changes (opencapy Go)](#daemon-changes-opencapy-go)
8. [Protocol — No Breaking Changes](#protocol--no-breaking-changes)
9. [Build System](#build-system)
10. [Rebranding Checklist](#rebranding-checklist)
11. [Licensing](#licensing)
12. [Monetization](#monetization)
13. [MVP Milestones](#mvp-milestones)
14. [Open Questions](#open-questions)

---

## Overview

**Capy Mac** replaces tmux as the session manager for opencapy. Instead of users running `opencapy new myproject` to spawn a tmux session, they open Capy.app — a native macOS terminal with GPU-accelerated rendering, split panes, and a sidebar showing all Claude Code sessions with real-time status.

**User flow today (tmux):**
```
User → opencapy new myproject → tmux session → claude inside tmux → daemon monitors via capture-pane
```

**User flow with Capy Mac:**
```
User → opens Capy.app → new terminal pane (libghostty) → claude via shim → daemon gets direct PTY stream
```

The iOS app, relay, and daemon protocol remain unchanged. Capy Mac is a new frontend that replaces tmux as the session host.

---

## Architecture

```
┌─────────────────────────────────────────────┐
│  Capy.app (macOS)                           │
│  ┌─────────┐  ┌──────────────────────────┐  │
│  │ Sidebar  │  │  Terminal Panes           │  │
│  │          │  │  (libghostty / Metal)     │  │
│  │ session1 │  │                           │  │
│  │ session2 │  │  Each pane runs a shell   │  │
│  │ session3 │  │  with claude() shim       │  │
│  │          │  │                           │  │
│  │ Claude   │  │  Approval banner overlay  │  │
│  │ status   │  │                           │  │
│  └─────────┘  └──────────────────────────┘  │
└──────────────────────┬──────────────────────┘
                       │ (same machine)
          ┌────────────┴────────────┐
          │  opencapy daemon        │
          │  ws://127.0.0.1:7242    │
          │                         │
          │  • session snapshots    │
          │  • event stream         │
          │  • file ops             │
          │  • git ops              │
          │  • push notifications   │
          └────────────┬────────────┘
                       │ (relay / tailscale / LAN)
          ┌────────────┴────────────┐
          │  Capy iOS app           │
          │  (unchanged)            │
          └─────────────────────────┘
```

**Key insight:** The daemon already supports "direct sessions" (shim-owned PTY, no tmux). Every `claude` invocation inside Capy.app goes through the shim, which registers with the daemon as a direct session. The daemon doesn't need to know about tmux at all.

---

## Why Fork cmux

### Why not build from scratch?
- libghostty integration is non-trivial: Zig build toolchain, C API bridging, Metal rendering, custom scroll curves, input handling
- cmux has ~6000 files and months of polish on workspace management, split panes, session persistence
- Building a macOS terminal from scratch is 3-6 months; forking cmux is 3-4 weeks to MVP

### Why not use cmux as-is and integrate via socket API?
- "Install Capy, but first install cmux" is a bad user experience
- Can't control the brand, onboarding, or roadmap
- cmux's socket API is powerful but adds an unnecessary IPC layer when we can modify the source directly

### Why not SwiftTerm instead of libghostty?
- SwiftTerm uses CoreText (CPU rendering) — no Metal/GPU support (open issue since Jan 2022)
- For a primary desktop terminal, GPU rendering matters: heavily styled Claude Code output, long scrollback
- SwiftTerm is great for iOS (remote terminal client with limited output) but not for a desktop terminal app
- Every serious macOS terminal being built in 2025-2026 uses libghostty: cmux, Echo, fantastty, Supacode, Calyx, Commander

### License
- cmux is AGPL-3.0 — forking, rebranding, and commercial use are explicitly permitted
- Constraint: the macOS app fork must remain open source
- This is fine — we monetize the iOS app and relay, not the macOS app

---

## What We Keep from cmux

### Core Terminal Engine
- **libghostty integration** (`GhosttyTerminalView.swift`) — GPU-accelerated terminal rendering via Metal
- **Ghostty fork submodule** (`manaflow-ai/ghostty`) — we re-fork this as `opencapy/ghostty` with their 7 patches
- **GhosttyKit xcframework** build pipeline (`scripts/setup.sh`)
- **Bridging header** (`cmux-Bridging-Header.h`) for Swift ↔ C API

### Layout & Window Management
- **Bonsplit** (`vendor/bonsplit/`) — horizontal/vertical split pane library
- **Workspace model** (`Workspace.swift`) — window → workspace → pane → surface hierarchy
- **TabManager** (`TabManager.swift`) — workspace collection management
- **ContentView** (`ContentView.swift`) — main SwiftUI layout

### Session Persistence
- **SessionPersistence.swift** — save/restore window layouts, scrollback, working directories
- 8-second autosave interval, ANSI-safe truncation, per-bundle-ID storage
- Max 400KB scrollback per session, 12 windows, 128 workspaces

### AppKit Shell
- **AppDelegate.swift** — window management, lifecycle
- **Entitlements** — hardened runtime, JIT (required for Metal)

### Auto-Update
- **Sparkle framework** — keep it, point appcast to our GitHub releases
- Ed25519 signed appcast, daily check interval

### Build & Release
- **Xcode project** (`GhosttyTabs.xcodeproj`) — rename to `Capy.xcodeproj`
- **Release workflow** (`release.yml`) — codesign, notarize, DMG, appcast
- **Version bumping** (`scripts/bump-version.sh`)

---

## What We Remove from cmux

### Browser Engine (~3000+ lines)
- `BrowserWindowPortal.swift` — WKWebView integration
- All `browser.*` socket commands
- Agent browser API (ported from vercel-labs/agent-browser)
- **Why:** We don't need in-app browser; Claude Code operates in terminal

### Remote SSH Daemon (~2000+ lines)
- `daemon/remote/` — cmuxd-remote (Go binary)
- `proxy.open/close/write` RPC
- SSH relay, reverse-forward TCP
- **Why:** We have our own relay infrastructure

### Analytics & Telemetry
- `PostHogAnalytics.swift` — delete entirely (API key `phc_opOVu7oFzR9wD3I6ZahFGOV2h3mqGpl5EHyQvmHciDP`)
- `SentryHelper.swift` — delete entirely
- `SentrySDK.start()` call in AppDelegate
- Telemetry settings UI in SettingsView
- **Why:** We don't want to send data to cmux's PostHog/Sentry

### cmux Socket API (partial removal)
- Keep basic workspace/surface commands for automation
- Remove: `browser.*`, `proxy.*`, `debug.*` (most of 11K-line CLI)
- **Why:** Trim to what we actually use

### Claude Code Hook Integration (replace)
- `ClaudeCodeIntegrationSettings.swift` — their hooks approach
- **Why:** We have our own hook system via the opencapy daemon (`POST /hooks/claude`)

### Notification System (replace)
- `TerminalNotificationStore.swift`, `NotificationsPage.swift`, `NotificationRow.swift`
- OSC 9/99/777 terminal sequence handlers
- **Why:** Replace with opencapy event stream (approval, done, crash, running)

---

## What We Add

### 1. Daemon Connection (WebSocket Client)

New file: `Services/DaemonConnection.swift`

Connect to `ws://127.0.0.1:7242/ws` on app launch. Handle:

| Inbound (daemon → app) | Purpose |
|---|---|
| `snapshot` | Session list with Claude metadata (model, tokens, branch, events) |
| `event` | Real-time events (approval, done, crash, running, thinking) |
| `pty_output` | Terminal output for sessions opened from iOS |
| `pong` | Heartbeat response |

| Outbound (app → daemon) | Purpose |
|---|---|
| `approve` / `deny` | Respond to approval prompts |
| `refresh_sessions` | Request fresh snapshot |
| `ping` | Heartbeat (30s) |
| `new_session` | Create session from app (replaces tmux creation) |

### 2. Sidebar Enrichment

Enhance the workspace sidebar with daemon data:

```
┌──────────────────────┐
│ ● myproject          │  ← green dot = running
│   claude-sonnet-4    │  ← model name from snapshot
│   ████████░░ 78%     │  ← context usage bar
│   main               │  ← git branch
│                      │
│ ⚠ api-refactor       │  ← yellow = waiting for approval
│   claude-opus-4      │
│   ██████████ 95%     │
│   feat/api-v2        │
│                      │
│ ✓ bugfix-auth        │  ← checkmark = done
│   Completed 2m ago   │
└──────────────────────┘
```

Data source: `SessionSnapshot` from daemon's `snapshot` message:
- `ModelName` — Claude model
- `ContextTokens` / `MaxContext` — usage bar
- `Branch` — git branch
- `RecentEvents` — status indicator (approval/running/done/crash)
- `LastActive` — relative timestamp

### 3. Approval UI

Floating banner when daemon sends `event.type == "approval"`:

```
┌─────────────────────────────────────────────┐
│  🔒 Claude wants to: Edit server.go         │
│                                              │
│  [Approve]  [Deny]  [Always Allow]           │
└─────────────────────────────────────────────┘
```

Sends `approve` or `deny` message to daemon. Same UX as iOS app's approval popup.

### 4. Session Creation Flow

"New Session" button or Cmd+N:
1. Opens new workspace/pane in Capy.app (libghostty terminal)
2. Shell starts with `claude()` shim function loaded (from `~/.opencapy/init.sh`)
3. User types `claude` → shim registers with daemon → session appears in sidebar + iOS
4. Optionally: auto-launch claude on new workspace (configurable)

### 5. Menu Bar Integration

```
┌─ 🐹 Capy ──────────────────┐
│  3 active sessions          │
│  1 waiting for approval     │
│  ─────────────────────────  │
│  myproject (running)     →  │
│  api-refactor (approval) →  │
│  bugfix-auth (done)      →  │
│  ─────────────────────────  │
│  New Session         ⌘N     │
│  Open Capy           ⌘O     │
│  Preferences         ⌘,     │
│  Quit                ⌘Q     │
└─────────────────────────────┘
```

### 6. Shim Auto-Install

On first launch, Capy.app ensures:
- `~/.opencapy/init.sh` exists (shell integration)
- `source ~/.opencapy/init.sh` is in `~/.zshrc` / `~/.bashrc`
- opencapy daemon is running (LaunchAgent)
- Same setup that `opencapy install` does today, but triggered from the GUI

---

## Daemon Changes (opencapy Go)

> **Note:** Phase 0 (Ditch tmux) handles the major daemon rewrite. The changes below are **additional** Capy Mac-specific changes on top of Phase 0.

After Phase 0, the daemon owns PTYs natively via `internal/session/`. tmux is gone. The remaining Capy Mac changes are minimal.

### Session Creation via Capy.app

After Phase 0, `new_session` creates a daemon-owned PTY session directly. For Capy Mac, we add one routing check:

```go
case "new_session":
    if s.hasCapyMacClient() {
        // Forward to Capy.app — it creates the workspace + terminal pane
        // The shim inside the pane will register as a direct session
        s.forwardToCapyMac("create_workspace", map[string]any{
            "name": name, "cwd": cwd, "launch_mode": msg.LaunchMode,
        })
    } else {
        // Default: daemon creates PTY session directly (Phase 0 path)
        s.sessionManager.Create(name, cwd, launchMode)
    }
```

### Routing Simplification

After Phase 0, all sessions use the same code path — no more tmux vs direct branching:

```go
// After Phase 0: one path for all sessions
s.sessionManager.Input(msg.Session, data)
```

### New Protocol Messages (Daemon ↔ Capy.app)

Capy.app connects to the daemon on the same WebSocket as iOS. It identifies itself with a new client type:

```json
{"type": "hello", "client_type": "mac", "version": "1.0.0"}
```

New outbound messages (daemon → Capy.app):

| Type | Purpose |
|---|---|
| `create_workspace` | iOS requested a new session — Capy.app should open a workspace |

New inbound messages (Capy.app → daemon):

| Type | Purpose |
|---|---|
| `workspace_created` | Workspace opened, shim will register shortly |
| `workspace_closed` | User closed workspace in Capy.app |

These are minimal — most communication still flows through the existing shim protocol.

---

## Protocol — No Breaking Changes

The existing protocol between daemon and iOS is **unchanged**:

| Message | Status |
|---|---|
| `snapshot` | Same — includes both tmux and direct sessions |
| `event` | Same — hooks are universal (not tmux-specific) |
| `pty_output` / `pty_input` | Same — direct session PTY stream |
| `approve` / `deny` | Same — routed to shim |
| `send_keys` | Same — routed to shim |
| `open_pty` / `close_pty` | Same — subscribes to ring buffer |
| `list_dir` / `file_read` / `file_write` | Same — filesystem ops |
| `git_*` | Same — git ops |
| `capture_pane` | Same — reads from ring buffer instead of tmux capture-pane |
| `chat_send` / `chat_send_image` | Same — injected via shim |
| `new_session` | Enhanced — delegates to Capy.app if available |

**iOS app requires zero changes.**

---

## Build System

### Prerequisites
- **Zig 0.15.2** — required for building libghostty (version-locked per Ghostty release)
- **Xcode** (full, not just CLI tools) — macOS + iOS SDKs
- **Homebrew**: `brew install gettext` (libghostty dependency)

### Build Steps

```bash
# 1. Clone with submodules
git clone --recursive https://github.com/opencapy/capy-mac.git
cd capy-mac

# 2. Build GhosttyKit xcframework (cached by commit SHA)
./scripts/setup.sh
# → Builds zig, produces GhosttyKit.xcframework (universal arm64+x86_64)
# → Cached at ~/.cache/capy/ghosttykit/<SHA>/

# 3. Build app (Debug)
./scripts/reload.sh --tag dev

# 4. Build app (Release)
xcodebuild -project Capy.xcodeproj -scheme Capy -configuration Release

# 5. Create DMG
create-dmg --volname "Capy" --icon "Capy.app" 175 120 \
  --hide-extension "Capy.app" --app-drop-link 425 120 \
  "Capy.dmg" "build/"

# 6. Notarize
xcrun notarytool submit Capy.dmg --keychain-profile "capy" --wait
xcrun stapler staple Capy.dmg
```

### CI/CD (GitHub Actions)

Adapt cmux's `release.yml`:
1. Build GhosttyKit (or download cached xcframework)
2. `xcodebuild` with Release config
3. Codesign with Developer ID certificate
4. Notarize with `xcrun notarytool`
5. Package DMG with `create-dmg`
6. Generate Sparkle appcast
7. Upload to GitHub release

### Distribution Channels
- **GitHub Releases** — DMG download (primary)
- **Homebrew Cask** — `brew tap opencapy/tap && brew install --cask capy`
- **Sparkle auto-update** — in-app update checks against appcast.xml

---

## Rebranding Checklist

### Bundle & Identity
- [ ] `PRODUCT_BUNDLE_IDENTIFIER`: `com.cmuxterm.app` → `com.opencapy.mac`
- [ ] `PRODUCT_NAME`: `cmux` → `Capy`
- [ ] `MARKETING_VERSION`: reset to `0.1.0`
- [ ] `CURRENT_PROJECT_VERSION`: reset to `1`
- [ ] Xcode project: `GhosttyTabs.xcodeproj` → `Capy.xcodeproj`
- [ ] Xcode scheme: `cmux` → `Capy`

### Visual Assets
- [ ] App icon: replace with Capy capybara icon
- [ ] `Assets.xcassets/` — all image assets
- [ ] DMG background image

### Code References
- [ ] Socket paths: `/tmp/cmux-*` → `/tmp/capy-*`
- [ ] Config dir: `~/.config/cmux/` → `~/.config/capy/` (or `~/.opencapy/`)
- [ ] Session persistence: `session-com.cmuxterm.app.json` → `session-com.opencapy.mac.json`
- [ ] Environment variables: `CMUX_*` → `CAPY_*`
- [ ] User defaults keys: grep for `cmux` in UserDefaults

### Scripts
- [ ] `scripts/reload.sh` — update bundle ID, app name, paths
- [ ] `scripts/bump-version.sh` — update for new project
- [ ] `release.yml` — update signing identity, notarization credentials, asset names

### Sparkle
- [ ] `SUFeedURL` → `https://github.com/opencapy/capy-mac/releases/latest/download/appcast.xml`
- [ ] Generate new Ed25519 keypair for appcast signing
- [ ] `SUPublicEDKey` → new public key

### Legal
- [ ] Add AGPL-3.0 license file
- [ ] Add attribution: "Includes code derived from cmux by manaflow-ai (AGPL-3.0)"
- [ ] Preserve original copyright notices in modified files

---

## Licensing

```
Capy Mac is licensed under AGPL-3.0.

It includes code derived from:
- cmux (https://github.com/manaflow-ai/cmux) — AGPL-3.0
- libghostty/Ghostty (https://github.com/ghostty-org/ghostty) — MIT

The following opencapy components are NOT derived from cmux and are separately licensed:
- opencapy daemon (Go) — [your license]
- Capy iOS app — proprietary, closed source
- Relay service — proprietary, closed source
```

### AGPL Compliance
- Capy Mac source code must be publicly available (GitHub)
- Users who receive the binary can request the source
- Modifications must be shared under AGPL
- **This does NOT affect:** iOS app, relay, daemon (separate programs, network boundary)

---

## Monetization

| Component | License | Price |
|---|---|---|
| Capy Mac (macOS app) | AGPL, open source | Free |
| opencapy daemon | Open source | Free |
| Capy iOS app | Proprietary | Paid (App Store) |
| Relay service | Proprietary | Paid subscription |

The macOS app is the **free acquisition channel**. Users discover Capy Mac, use it for free, then pay for mobile access (iOS) and remote access (relay).

---

## MVP Milestones

### Phase 0: Ditch tmux — Native PTY Daemon (Week 0-1)

> **Goal:** Replace all tmux usage with a native Go PTY session manager inside the daemon. After this phase, `tmux` is no longer a dependency. The CLI (`opencapy new/attach/list/kill`) works identically from the user's perspective. iOS app requires zero changes.

#### Why First

- tmux's virtual screen causes iOS issues (escape code injection, mouse conflicts, scroll corruption) that are unfixable
- Every later phase (Capy Mac, daemon cleanup) benefits from tmux being gone
- The shim (`cmd_shim.go`, 448 lines) already proves the pattern works — we're promoting it to the daemon

#### Architecture

```
Before:  CLI → tmux server → PTY → claude
         daemon polls tmux capture-pane every 100ms

After:   CLI → daemon Unix socket → PTY → claude (direct, real-time)
         iOS → daemon WebSocket → same PTY (fan-out)
```

The daemon becomes the PTY owner (like tmux server, but purpose-built):

```
~/.opencapy/daemon.sock   ← CLI (list/attach/new/kill via Unix socket)
:7242/ws                  ← iOS + Capy Mac (existing WebSocket)

Session {
    ptmx     *os.File       // master PTY fd — never closed on client disconnect
    cmd      *exec.Cmd      // claude process
    ring     *RingBuffer    // last 256KB scrollback for reconnect replay
    clients  map[string]*Client  // fan-out: CLI + iOS simultaneously
}
```

#### New Files

| File | Lines | Purpose |
|---|---|---|
| `internal/session/session.go` | ~120 | Session struct, PTY lifecycle, read loop, fan-out to clients |
| `internal/session/manager.go` | ~100 | Session registry, create/kill/list, Unix socket listener |
| `internal/session/ring.go` | ~40 | Ring buffer (256KB) for scrollback replay on reconnect |
| `internal/session/protocol.go` | ~50 | Wire protocol: JSON handshake → binary framing (ttyd-style) |
| `internal/session/client.go` | ~40 | Client abstraction (CLI raw socket vs WebSocket) |
| **Total new** | **~350** | |

#### Wire Protocol (CLI ↔ Daemon over Unix socket)

**Phase 1: JSON handshake**
```json
→ {"op":"attach","name":"myproject","rows":50,"cols":200}
← {"ok":true}
```

**Phase 2: Binary framing** (after handshake ACK)
```
[1 byte type][payload]
'i' = input        (client → daemon: keystrokes)
'o' = output       (daemon → client: PTY bytes)
'r' = resize       (client → daemon: JSON {rows, cols})
's' = snapshot     (daemon → client: ring buffer replay on attach)
```

No `0xFF` magic bytes — single-byte ASCII prefix avoids collision with real terminal data.

**Detach:** `~.` after newline (SSH convention) — CLI intercepts this locally, sends nothing to PTY.

#### Modified Files

| File | Change |
|---|---|
| `cmd/opencapy/cmd_session.go` | `new/attach/kill/list` talk to daemon Unix socket instead of tmux |
| `cmd/opencapy/cmd_daemon.go` | Start session manager, remove tmux reconciliation loop |
| `cmd/opencapy/cmd_tui.go` | TUI queries daemon session list instead of `tmux.ListSessions()` |
| `internal/ws/server.go` | Route `send_keys/approve/deny/capture_pane/new_session/kill_session` to session manager instead of tmux |
| `internal/watcher/watcher.go` | Subscribe to session manager output events instead of polling `capture-pane` |

#### Deleted Files

| File | Lines | Why |
|---|---|---|
| `internal/tmux/tmux.go` | 257 | All 12 tmux functions — no longer needed |
| `internal/pty/pty.go` | 298 | Grouped tmux sessions (`ocpy_*`) — no longer needed |
| **Total deleted** | **555** | |

#### Net Code Change: ~350 new − 555 deleted = **−205 lines** (less code than before)

#### Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| PTY ownership | Daemon (not shim) | Fewer processes, simpler architecture. Daemon restart kills sessions — acceptable (tmux server crash does the same) |
| Reconnect replay | `\033c` reset + ring buffer | Simple, proven. VT state machine is future Phase 5 nice-to-have |
| Multi-client resize | Primary client (first attacher) owns resize | iOS adapts to whatever size the PTY is. Avoids resize thrashing |
| Backpressure | Per-client bounded channel; slow client gets disconnected | Never block PTY read loop. Disconnected client reconnects and gets clean replay |
| Detach key | `~.` after newline | SSH convention, familiar to developers |
| Shim coexistence | Keep shim for Capy Mac | Capy Mac panes own their own PTY via shim; daemon-owned PTYs are for CLI users |

#### External Packages (Already in go.mod)

| Package | Purpose |
|---|---|
| `github.com/creack/pty` | PTY create/resize (already used by shim) |
| `golang.org/x/term` | Raw terminal mode for CLI attach |
| `gorilla/websocket` | Already used for iOS WebSocket |
| `net` stdlib | Unix socket |

**No new dependencies.** All packages already in the project.

#### Tasks

- [ ] Implement `internal/session/` package (session.go, manager.go, ring.go, protocol.go, client.go)
- [ ] Integrate session manager into daemon startup (`cmd_daemon.go`)
- [ ] Migrate CLI commands to use daemon Unix socket (`cmd_session.go`)
- [ ] Migrate TUI to query daemon (`cmd_tui.go`)
- [ ] Migrate WebSocket handlers to use session manager (`server.go`)
- [ ] Convert watcher from polling to event-driven (`watcher.go`)
- [ ] Delete `internal/tmux/tmux.go` and `internal/pty/pty.go`
- [ ] Test: `opencapy new foo` → attach → detach → reattach → kill
- [ ] Test: iOS connects, sees sessions, sends input, receives output
- [ ] Test: works over SSH (Linux server, no tmux installed)
- [ ] Remove tmux from install requirements

---

### Phase 1: Fork & Strip (Week 2)

> **Prerequisite:** Phase 0 complete — tmux is gone, daemon owns PTYs natively.

- [ ] Fork cmux repo as `opencapy/capy-mac`
- [ ] Verify build works: `setup.sh` → `xcodebuild` → app launches
- [ ] Remove browser engine (`BrowserWindowPortal.swift`, all `browser.*` commands)
- [ ] Remove remote SSH daemon (`daemon/remote/`)
- [ ] Remove PostHog + Sentry (`PostHogAnalytics.swift`, `SentryHelper.swift`)
- [ ] Remove cmux notification system (replace later with daemon events)
- [ ] Trim CLI to essentials (remove `browser.*`, `proxy.*`, `debug.*` commands)
- [ ] Rebrand: bundle ID, app name, icons, config paths

### Phase 2: Daemon Integration (Week 3)
- [ ] Add `DaemonConnection.swift` — WebSocket client to `ws://127.0.0.1:7242/ws`
- [ ] Parse `snapshot` messages — populate sidebar with Claude metadata
- [ ] Parse `event` messages — update session status indicators in real-time
- [ ] Sidebar shows: session name, model, context usage, branch, status
- [ ] Handle daemon disconnect/reconnect gracefully

### Phase 3: Approval UI & Session Management (Week 4)
- [ ] Approval banner overlay when `event.type == "approval"`
- [ ] Send `approve` / `deny` to daemon on button press
- [ ] "New Session" (Cmd+N) creates workspace with shim-enabled shell
- [ ] Ensure `~/.opencapy/init.sh` is sourced in new terminal shells
- [ ] Auto-start daemon (LaunchAgent) if not running on app launch

### Phase 4: Polish & Ship (Week 5)
- [ ] Menu bar icon with session summary
- [ ] First-launch onboarding (install daemon, shell integration)
- [ ] DMG packaging + notarization
- [ ] Homebrew cask formula
- [ ] Sparkle auto-update with new keypair
- [ ] README, screenshots, landing page content

---

## Open Questions

1. **Ghostty fork management** — Do we re-fork from `manaflow-ai/ghostty` (includes their 7 patches) or fork from upstream `ghostty-org/ghostty` and cherry-pick? Re-forking is faster; upstream fork is cleaner long-term.

2. **Socket API retention** — How much of cmux's socket API do we keep? Minimal (workspace.create, surface.split) or full? Could be useful for scripting/automation.

3. **Dual-pane mode** — Should Capy.app support a "chat mode" panel (like the iOS EventStreamView) alongside the terminal? Or terminal-only for MVP?

4. ~~**tmux deprecation timeline**~~ — **Resolved: Phase 0 removes tmux entirely before Capy Mac work begins.**

5. **Ghostty config compatibility** — cmux reads `~/.config/ghostty/config` for themes/fonts. Do we keep this (users who already use Ghostty get their config for free) or use our own config?

6. **iOS handoff** — Should Capy Mac support "Continue on iPhone" / "Continue on Mac" via Handoff? Nice-to-have but not MVP.

7. **User login integration** — Login system is being built separately. How does it integrate with Capy Mac? Probably: login in app → stores auth token → daemon uses token for relay auth.

8. **Notification strategy** — macOS native notifications when Claude finishes/needs approval? Menu bar badge? Both?
