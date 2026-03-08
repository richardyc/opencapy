package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/richardyc/opencapy/internal/config"
	"github.com/richardyc/opencapy/internal/platform"
	"github.com/richardyc/opencapy/internal/tmux"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon health and connected iOS devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			port := strconv.Itoa(cfg.Port)

			// Check if daemon is running by trying to connect to the port
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 2*time.Second)
			if err != nil {
				fmt.Printf("Daemon:   NOT RUNNING (port %s)\n", port)
				fmt.Printf("Host:     %s\n", platform.Hostname())
				return nil
			}
			conn.Close()

			fmt.Printf("Daemon:   RUNNING (port %s)\n", port)
			fmt.Printf("Host:     %s\n", platform.Hostname())

			// Show sessions
			sessions, err := tmux.ListSessions()
			if err == nil {
				fmt.Printf("Sessions: %d\n", len(sessions))
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
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			hostname, isTailscale := platform.TailscaleHostname()
			if isTailscale {
				fmt.Printf("Tailscale address detected: %s\n", hostname)
			} else {
				fmt.Printf("Tailscale not running — using hostname: %s (install Tailscale for best connectivity)\n", hostname)
			}

			url := fmt.Sprintf("opencapy://%s:%d", hostname, cfg.Port)

			fmt.Println("Connect your iOS device to this machine:")
			fmt.Println()

			qr, err := qrcode.New(url, qrcode.Medium)
			if err != nil {
				// Fallback to just printing URL
				fmt.Printf("  %s\n", url)
				return nil
			}
			fmt.Println(qr.ToSmallString(false))
			fmt.Printf("  %s\n", url)
			return nil
		},
	}
}

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Upgrade opencapy to the latest version and restart the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Upgrading opencapy via Homebrew…")
			upgrade := exec.Command("brew", "upgrade", "opencapy")
			upgrade.Stdout = os.Stdout
			upgrade.Stderr = os.Stderr
			if err := upgrade.Run(); err != nil {
				return fmt.Errorf("brew upgrade: %w", err)
			}

			fmt.Println("\nRestarting daemon…")
			home, _ := os.UserHomeDir()
			plistPath := home + "/Library/LaunchAgents/com.opencapy.daemon.plist"

			if platform.IsMacOS() {
				if _, err := os.Stat(plistPath); err == nil {
					// Installed as LaunchAgent — unload/load handles the restart.
					exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck
					if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
						return fmt.Errorf("launchctl load: %w", err)
					}
				} else {
					// Not a service — kill old daemon and spawn new one detached.
					exec.Command("pkill", "-f", "opencapy daemon").Run() //nolint:errcheck
					time.Sleep(300 * time.Millisecond)
					binaryPath, _ := os.Executable()
					daemon := exec.Command(binaryPath, "daemon")
					daemon.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
					daemon.Stdout = nil
					daemon.Stderr = nil
					daemon.Stdin = nil
					if err := daemon.Start(); err != nil {
						return fmt.Errorf("start daemon: %w", err)
					}
				}
				return verifyDaemon()
			}

			if platform.IsLinux() {
				if err := exec.Command("sudo", "systemctl", "restart", "opencapy").Run(); err != nil {
					return fmt.Errorf("systemctl restart: %w", err)
				}
				return verifyDaemon()
			}

			return fmt.Errorf("unsupported platform — restart the daemon manually")
		},
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
			// Print new version from binary.
			binaryPath, _ := os.Executable()
			out, _ := exec.Command(binaryPath, "version").Output()
			fmt.Printf("Running: %s", out)
			return nil
		}
		fmt.Print(".")
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start within 8s — run `opencapy daemon` manually")
}

func newInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install daemon as system service (LaunchAgent on Mac, systemd on Linux)",
		RunE: func(cmd *cobra.Command, args []string) error {
			vscode, _ := cmd.Flags().GetBool("vscode")
			if vscode {
				return installVSCode()
			}

			binaryPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find binary path: %w", err)
			}

			if platform.IsMacOS() {
				fmt.Println("Installing LaunchAgent...")
				if err := platform.InstallLaunchAgent(binaryPath); err != nil {
					return fmt.Errorf("install LaunchAgent: %w", err)
				}
				fmt.Println("LaunchAgent installed and loaded.")
				fmt.Println("The daemon will start automatically on login.")
				return nil
			}

			if platform.IsLinux() {
				fmt.Println("Installing systemd service...")
				if err := platform.InstallSystemd(binaryPath); err != nil {
					return fmt.Errorf("install systemd: %w", err)
				}
				fmt.Println("systemd service installed and enabled.")
				fmt.Println("Start with: sudo systemctl start opencapy")
				return nil
			}

			return fmt.Errorf("unsupported platform")
		},
	}
	cmd.Flags().Bool("vscode", false, "Configure VSCode/Cursor terminal to use opencapy automatically")
	return cmd
}

// editorSettingsPaths returns candidate settings.json paths for known editors.
func editorSettingsPaths() []string {
	home, _ := os.UserHomeDir()
	var paths []string
	if runtime.GOOS == "darwin" {
		base := filepath.Join(home, "Library", "Application Support")
		paths = []string{
			filepath.Join(base, "Code", "User", "settings.json"),
			filepath.Join(base, "Cursor", "User", "settings.json"),
			filepath.Join(base, "VSCodium", "User", "settings.json"),
		}
	} else {
		cfg := filepath.Join(home, ".config")
		paths = []string{
			filepath.Join(cfg, "Code", "User", "settings.json"),
			filepath.Join(cfg, "Cursor", "User", "settings.json"),
			filepath.Join(cfg, "VSCodium", "User", "settings.json"),
		}
	}
	return paths
}

// platformKey returns the VSCode OS-specific settings key (osx, linux, windows).
func platformKey() string {
	switch runtime.GOOS {
	case "darwin":
		return "osx"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

func installVSCode() error {
	paths := editorSettingsPaths()

	// Find which editors are installed.
	var found []string
	for _, p := range paths {
		if _, err := os.Stat(filepath.Dir(p)); err == nil {
			found = append(found, p)
		}
	}

	if len(found) == 0 {
		printVSCodeSnippet()
		return nil
	}

	ok := false
	for _, settingsPath := range found {
		editor := filepath.Base(filepath.Dir(filepath.Dir(settingsPath)))
		if err := patchVSCodeSettings(settingsPath); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", editor, settingsPath, err)
		} else {
			fmt.Printf("  ✓ %s — opencapy set as default terminal\n", editor)
			ok = true
		}
	}

	if ok {
		fmt.Println("\nRestart your editor and every new terminal will open inside opencapy.")
		fmt.Println("Tip: 'opencapy here' is the underlying command — use it in any editor that supports custom terminal profiles.")
	}
	return nil
}

func patchVSCodeSettings(path string) error {
	osKey := platformKey()
	profilesKey := "terminal.integrated.profiles." + osKey
	defaultKey := "terminal.integrated.defaultProfile." + osKey

	// Read existing settings (create empty file if missing).
	var raw map[string]interface{}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		raw = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &raw); err != nil {
			// Likely JSONC with comments — print snippet instead.
			fmt.Printf("  ! Could not parse %s (may contain comments).\n", path)
			fmt.Println("    Add this manually:")
			printVSCodeSnippet()
			return nil
		}
	}

	// Merge opencapy profile into existing profiles map.
	profiles, _ := raw[profilesKey].(map[string]interface{})
	if profiles == nil {
		profiles = make(map[string]interface{})
	}
	profiles["opencapy"] = map[string]interface{}{
		"path": "bash",
		"args": []string{"-c", "opencapy here"},
		"icon": "terminal-tmux",
	}
	raw[profilesKey] = profiles
	raw[defaultKey] = "opencapy"

	out, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func printVSCodeSnippet() {
	osKey := platformKey()
	fmt.Printf(`
Add to your editor's settings.json (Cmd+Shift+P → "Open User Settings JSON"):

    "terminal.integrated.profiles.%s": {
        "opencapy": {
            "path": "bash",
            "args": ["-c", "opencapy here"],
            "icon": "terminal-tmux"
        }
    },
    "terminal.integrated.defaultProfile.%s": "opencapy"

`, osKey, osKey)
}
