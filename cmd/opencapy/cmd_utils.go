package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/richardyc/opencapy/internal/config"
	"github.com/richardyc/opencapy/internal/platform"
	"github.com/richardyc/opencapy/internal/relay"
	"github.com/richardyc/opencapy/internal/tmux"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon health and connected sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			port := strconv.Itoa(cfg.Port)
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 2*time.Second)
			if err != nil {
				fmt.Printf("Daemon:   NOT RUNNING (port %s)\n", port)
				fmt.Printf("Host:     %s\n", platform.Hostname())
				return nil
			}
			conn.Close()
			fmt.Printf("Daemon:   RUNNING (port %s)\n", port)
			fmt.Printf("Host:     %s\n", platform.Hostname())
			if sessions, err := tmux.ListSessions(); err == nil && len(sessions) > 0 {
				fmt.Printf("tmux:     %d session(s)\n", len(sessions))
			}
			return nil
		},
	}
}

func newQRCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "qr",
		Short: "Show connection QR code for iOS pairing",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := relay.LoadOrCreate()
			if err != nil {
				return fmt.Errorf("relay token: %w", err)
			}
			hostname := platform.Hostname()
			pairURL := relay.PairURL(token, hostname, relay.DefaultRelayURL)
			fmt.Println("Scan with the OpenCapy iOS app to pair:")
			fmt.Println()
			qr, err := qrcode.New(pairURL, qrcode.Medium)
			if err != nil {
				fmt.Printf("  %s\n", pairURL)
				return nil
			}
			fmt.Println(qr.ToSmallString(false))
			fmt.Printf("  Machine: %s\n", hostname)
			fmt.Printf("  Relay:   %s\n", relay.DefaultRelayURL)
			fmt.Println()
			fmt.Println("Don't have the app? Download OpenCapy on the App Store.")
			return nil
		},
	}
}

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Upgrade opencapy to the latest version and restart the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Checking for updates…")

			// Prefer Homebrew when available; fall back to direct download otherwise.
			if isBrewInstall() {
				exec.Command("brew", "update").Run() //nolint:errcheck
				upgradeOut, _ := exec.Command("brew", "upgrade", "richardyc/opencapy/opencapy").CombinedOutput()
				if strings.Contains(string(upgradeOut), "already installed") {
					newBin, _ := exec.LookPath("opencapy")
					ver, _ := exec.Command(newBin, "version").Output()
					fmt.Printf("Already on the latest version. %s", ver)
					return nil
				}
				if strings.Contains(string(upgradeOut), "brew link") {
					exec.Command("brew", "link", "--overwrite", "opencapy").Run() //nolint:errcheck
				}
				fmt.Println(strings.TrimSpace(string(upgradeOut)))
			} else {
				if err := selfUpdate(); err != nil {
					return err
				}
			}

			fmt.Println("\nRestarting daemon…")

			home, _ := os.UserHomeDir()
			plistPath := home + "/Library/LaunchAgents/com.opencapy.daemon.plist"

			if platform.IsMacOS() {
				cfg, _ := config.Load()
				port := 7242
				if cfg != nil {
					port = cfg.Port
				}
				if _, err := os.Stat(plistPath); err == nil {
					exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck
					killAndWait(port)
					if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
						return fmt.Errorf("launchctl load: %w", err)
					}
				} else {
					killAndWait(port)
					binaryPath, _ := os.Executable()
					daemon := exec.Command(binaryPath, "daemon")
					daemon.Env = filteredEnv()
					daemon.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
					if err := daemon.Start(); err != nil {
						return fmt.Errorf("start daemon: %w", err)
					}
				}
				return verifyDaemon()
			}

			if platform.IsLinux() {
				// Kill the daemon — systemd's Restart=always brings it back with the new binary.
				// Avoids needing to invoke systemctl --user from a potentially wrong user context.
				cfg, _ := config.Load()
				port := 7242
				if cfg != nil {
					port = cfg.Port
				}
				killAndWait(port)
				return verifyDaemon()
			}

			return fmt.Errorf("unsupported platform — restart the daemon manually")
		},
	}
}

// isBrewInstall returns true when the running binary lives inside a Homebrew prefix.
func isBrewInstall() bool {
	bin, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(bin, "Cellar") || strings.Contains(bin, "homebrew") || strings.Contains(bin, "linuxbrew")
}

// selfUpdate downloads the latest release from GitHub and replaces the running binary.
func selfUpdate() error {
	const repo = "richardyc/opencapy"

	// Resolve latest tag via GitHub API.
	out, err := exec.Command("curl", "-sf",
		"https://api.github.com/repos/"+repo+"/releases/latest").Output()
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(out, &release); err != nil || release.TagName == "" {
		return fmt.Errorf("parse release info: %w", err)
	}

	tarball := fmt.Sprintf("opencapy_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, release.TagName, tarball)

	fmt.Printf("Downloading %s…\n", release.TagName)

	// Download to a temp dir.
	tmp, err := os.MkdirTemp("", "opencapy-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	archive := filepath.Join(tmp, "opencapy.tar.gz")
	if err := exec.Command("curl", "-sL", url, "-o", archive).Run(); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := exec.Command("tar", "-xzf", archive, "-C", tmp).Run(); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Replace the running binary using Go's own I/O — avoids cross-filesystem
	// cp failures and doesn't require cp to be in PATH.
	dest, err := os.Executable()
	if err != nil {
		return err
	}
	// Resolve symlinks so we write to the real file (e.g. Homebrew Cellar path).
	if resolved, err := filepath.EvalSymlinks(dest); err == nil {
		dest = resolved
	}
	newBin := filepath.Join(tmp, "opencapy")
	if err := replaceFile(newBin, dest); err != nil {
		return fmt.Errorf("cannot replace %s: %w\n  re-run with: sudo opencapy update", dest, err)
	}
	fmt.Printf("✓ Updated to %s\n", release.TagName)
	return nil
}

// replaceFile atomically replaces dest with src. Writes to a temp file beside
// dest first, then renames — ensuring the old binary is never half-overwritten.
func replaceFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Write new binary next to dest so rename is atomic (same filesystem).
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".opencapy-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // cleaned up on failure; no-op after rename

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	return os.Rename(tmpPath, dest)
}

// killAndWait kills all running opencapy daemon processes and blocks until the
// port is free (up to 3s). Prevents "address already in use" when restarting.
func killAndWait(port int) {
	exec.Command("pkill", "-f", "opencapy daemon").Run() //nolint:errcheck
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return // port is free
		}
		conn.Close()
		time.Sleep(200 * time.Millisecond)
	}
}

// verifyDaemon waits for the daemon port to be reachable and prints version info.
func verifyDaemon() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	port := strconv.Itoa(cfg.Port)
	fmt.Print("Waiting for daemon")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			fmt.Println(" ✔︎")
			newBin, err := exec.LookPath("opencapy")
			if err != nil {
				newBin, _ = os.Executable()
			}
			out, _ := exec.Command(newBin, "version").Output()
			fmt.Printf("Running: %s", out)
			return nil
		}
		fmt.Print(".")
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start within 8s — run `opencapy daemon` manually")
}

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install daemon as system service and inject the claude shell hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			binaryPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find binary path: %w", err)
			}

			if platform.IsMacOS() {
				cfg, _ := config.Load()
				port := 7242
				if cfg != nil {
					port = cfg.Port
				}
				// Kill any stale daemon first so launchctl load gets a clean start.
				killAndWait(port)
				if err := platform.InstallLaunchAgent(binaryPath); err != nil {
					return fmt.Errorf("install LaunchAgent: %w", err)
				}
				fmt.Println("✓ Daemon installed — starts automatically on login")
			} else if platform.IsLinux() {
				if err := platform.InstallSystemd(binaryPath); err != nil {
					return fmt.Errorf("install systemd: %w", err)
				}
				fmt.Println("✓ Daemon installed and started (systemd --user)")
			} else {
				return fmt.Errorf("unsupported platform")
			}

			injectShellIntegration()
			injectClaudeHooks()

			fmt.Println()
			fmt.Println("Done! Open a new terminal, then run: claude")
			fmt.Println("To pair your iPhone:  opencapy qr")
			patchVSCodeTabTitle()
			return nil
		},
	}
}

// patchVSCodeTabTitle adds terminal.integrated.tabs.title = "${sequence}" to
// VS Code / Cursor settings.json if either editor is detected. Skips silently
// if no editor is found or the file can't be parsed (e.g. JSONC with comments).
func patchVSCodeTabTitle() {
	home, _ := os.UserHomeDir()
	var candidates []string
	if runtime.GOOS == "darwin" {
		base := filepath.Join(home, "Library", "Application Support")
		candidates = []string{
			filepath.Join(base, "Code", "User", "settings.json"),
			filepath.Join(base, "Cursor", "User", "settings.json"),
			filepath.Join(base, "VSCodium", "User", "settings.json"),
		}
	} else {
		base := filepath.Join(home, ".config")
		candidates = []string{
			filepath.Join(base, "Code", "User", "settings.json"),
			filepath.Join(base, "Cursor", "User", "settings.json"),
			filepath.Join(base, "VSCodium", "User", "settings.json"),
		}
	}

	const key = "terminal.integrated.tabs.title"
	const val = "${sequence}"
	patched := false

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // editor not installed
		}
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err != nil {
			// JSONC with comments — can't safely patch; print manual tip instead
			fmt.Printf("\n  %s detected — add to settings.json manually:\n", filepath.Base(filepath.Dir(filepath.Dir(path))))
			fmt.Printf(`    "%s": "%s"`+"\n", key, val)
			patched = true
			continue
		}
		if v, ok := settings[key]; ok && v == val {
			fmt.Printf("✓ %s tab titles already configured\n", filepath.Base(filepath.Dir(filepath.Dir(path))))
			patched = true
			continue
		}
		settings[key] = val
		out, err := json.MarshalIndent(settings, "", "    ")
		if err != nil {
			continue
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not write %s: %v\n", path, err)
			continue
		}
		fmt.Printf("✓ %s tab titles configured for accurate session names\n", filepath.Base(filepath.Dir(filepath.Dir(path))))
		patched = true
	}

	if !patched {
		// No editor detected — print the tip for future reference
		fmt.Println()
		fmt.Println("VS Code/Cursor: add to settings.json for accurate tab titles:")
		fmt.Printf(`  "%s": "%s"`+"\n", key, val)
	}
}

func newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Revoke all iOS pairings (broadcasts to connected devices, resets relay token)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load()
			port := 7242
			if cfg != nil {
				port = cfg.Port
			}
			resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/unlink", port), "", nil)
			if err != nil {
				return fmt.Errorf("daemon not running — start it first with: opencapy daemon")
			}
			resp.Body.Close()
			fmt.Println("✓ All iOS devices unlinked")
			fmt.Println("  Run: opencapy qr   # to re-pair your iPhone")
			return nil
		},
	}
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove opencapy daemon service and shell hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()

			// Stop and remove LaunchAgent / systemd service.
			plistPath := home + "/Library/LaunchAgents/com.opencapy.daemon.plist"
			if _, err := os.Stat(plistPath); err == nil {
				exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck
				os.Remove(plistPath)
				fmt.Println("✓ LaunchAgent removed")
			}
			// Remove user systemd service (current install method).
			userUnitPath := platform.SystemdUnitPath()
			if _, err := os.Stat(userUnitPath); err == nil {
				exec.Command("systemctl", "--user", "disable", "--now", "opencapy").Run() //nolint:errcheck
				os.Remove(userUnitPath)
				exec.Command("systemctl", "--user", "daemon-reload").Run() //nolint:errcheck
				fmt.Println("✓ systemd user service removed")
			}
			// Also clean up legacy system-level service if present.
			sysUnitPath := "/etc/systemd/system/opencapy.service"
			if _, err := os.Stat(sysUnitPath); err == nil {
				exec.Command("sudo", "systemctl", "disable", "--now", "opencapy").Run() //nolint:errcheck
				os.Remove(sysUnitPath)
				fmt.Println("✓ systemd system service removed")
			}

			// Kill any running daemon.
			cfg, _ := config.Load()
			port := 7242
			if cfg != nil {
				port = cfg.Port
			}
			killAndWait(port)
			fmt.Println("✓ Daemon stopped")

			// Remove shell integration and Claude Code hooks.
			removeShellIntegration(home)
			removeClaudeHooks()

			fmt.Println()
			fmt.Println("opencapy uninstalled. Your claude installation is unchanged.")
			fmt.Println()
			fmt.Println("Remaining (your data — safe to delete manually if wanted):")
			if h, err := os.UserHomeDir(); err == nil {
				fmt.Printf("  %s  (relay token, config, session registry)\n", h+"/.opencapy")
			}
			fmt.Println()
			fmt.Println("To remove the binary: brew uninstall opencapy")
			return nil
		},
	}
}
