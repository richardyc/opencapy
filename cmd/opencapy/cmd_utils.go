package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
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
			// Always run `brew update` first so the local tap cache is fresh,
			// then attempt the upgrade. Capture stdout/stderr to detect whether
			// a real upgrade happened (brew exits 0 and prints nothing on
			// "already latest"; it prints the new version when it upgrades).
			fmt.Println("Checking for updates…")
			exec.Command("brew", "update").Run() //nolint:errcheck

			upgradeOut, _ := exec.Command("brew", "upgrade", "richardyc/opencapy/opencapy").CombinedOutput()
			upgraded := !strings.Contains(string(upgradeOut), "already installed")

			if !upgraded {
				newBin, _ := exec.LookPath("opencapy")
				ver, _ := exec.Command(newBin, "version").Output()
				fmt.Printf("Already on the latest version. %s", ver)
				return nil
			}

			// Brew link may fail if a manually-installed binary occupies the path.
			// Force the symlink so the new version is active.
			if strings.Contains(string(upgradeOut), "brew link") {
				exec.Command("brew", "link", "--overwrite", "opencapy").Run() //nolint:errcheck
			}

			fmt.Println(strings.TrimSpace(string(upgradeOut)))

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
			// Print version from the installed binary on disk (not this process,
			// which may be the pre-upgrade binary still in memory).
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

			if err := ensureTmux(); err != nil {
				return fmt.Errorf("tmux required: %w", err)
			}

			if platform.IsMacOS() {
				fmt.Println("Installing LaunchAgent...")
				if err := platform.InstallLaunchAgent(binaryPath); err != nil {
					return fmt.Errorf("install LaunchAgent: %w", err)
				}
				fmt.Println("LaunchAgent installed and loaded.")
				fmt.Println("The daemon will start automatically on login.")
				promptDefaultTerminal()
				return nil
			}

			if platform.IsLinux() {
				fmt.Println("Installing systemd service...")
				if err := platform.InstallSystemd(binaryPath); err != nil {
					return fmt.Errorf("install systemd: %w", err)
				}
				fmt.Println("systemd service installed and enabled.")
				fmt.Println("Start with: sudo systemctl start opencapy")
				promptDefaultTerminal()
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

// promptDefaultTerminal asks the user if they want opencapy to auto-start when
// opening a new terminal window, and patches their shell profile if they agree.
func promptDefaultTerminal() {
	fmt.Println()
	fmt.Print("Make opencapy your default terminal? (auto-attaches tmux on open) [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		return
	}
	installDefaultTerminal()
}

// installDefaultTerminal patches the user's shell profile to auto-launch opencapy
// whenever a new interactive terminal opens (but not inside tmux, VSCode, or SSH).
func installDefaultTerminal() {
	// When run with sudo, target the real user's home and shell, not root's.
	home, _ := os.UserHomeDir()
	shell := os.Getenv("SHELL")
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			home = u.HomeDir
			if s := loginShell(u.Username); s != "" {
				shell = s
			}
		}
	}

	// Pick the right profile file(s) based on the current shell.
	var profiles []string
	switch {
	case strings.Contains(shell, "zsh"):
		profiles = []string{filepath.Join(home, ".zshrc")}
	case strings.Contains(shell, "bash"):
		profiles = []string{filepath.Join(home, ".bashrc")}
		if runtime.GOOS == "darwin" {
			profiles = append(profiles, filepath.Join(home, ".bash_profile"))
		}
	default:
		// Unknown shell — try both common profiles.
		profiles = []string{
			filepath.Join(home, ".zshrc"),
			filepath.Join(home, ".bashrc"),
		}
	}

	const marker = "# opencapy: auto-attach"
	const snippet = `
# opencapy: auto-attach to tmux on terminal open
# Remove this block to disable. Re-run 'opencapy install' to re-add.
if [ -z "$TMUX" ] && [ -z "$VSCODE_INJECTION" ] && [ -z "$SSH_CONNECTION" ] && [ -t 1 ] && command -v opencapy >/dev/null 2>&1; then
  exec opencapy
fi`

	patched := false
	for _, p := range profiles {
		// Skip if already installed.
		data, _ := os.ReadFile(p)
		if strings.Contains(string(data), marker) {
			fmt.Printf("  ✓ %s — already configured\n", p)
			patched = true
			continue
		}
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", p, err)
			continue
		}
		_, werr := fmt.Fprintln(f, snippet)
		f.Close()
		if werr != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", p, werr)
			continue
		}
		fmt.Printf("  ✓ %s — opencapy set as default terminal\n", p)
		patched = true
	}

	if patched {
		fmt.Println()
		fmt.Println("Restart your terminal (or run: source ~/.zshrc) to activate.")
		fmt.Println("To undo, remove the '# opencapy: auto-attach' block from your shell profile.")
	}
}

// ensureTmux checks that tmux is available, and installs it if not.
func ensureTmux() error {
	if _, err := exec.LookPath("tmux"); err == nil {
		return nil // already installed
	}

	fmt.Println("tmux not found — installing...")

	// Detect package manager and build install command.
	type pm struct {
		bin  string
		args []string
	}
	var mgr *pm
	switch {
	case runtime.GOOS == "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			mgr = &pm{"brew", []string{"install", "tmux"}}
		}
	default:
		for _, candidate := range []pm{
			{"apt-get", []string{"install", "-y", "tmux"}},
			{"dnf", []string{"install", "-y", "tmux"}},
			{"yum", []string{"install", "-y", "tmux"}},
			{"pacman", []string{"-S", "--noconfirm", "tmux"}},
			{"zypper", []string{"install", "-y", "tmux"}},
		} {
			if _, err := exec.LookPath(candidate.bin); err == nil {
				mgr = &pm{candidate.bin, candidate.args}
				break
			}
		}
	}

	if mgr == nil {
		return fmt.Errorf("could not find a supported package manager — please install tmux manually and re-run")
	}

	cmd := exec.Command(mgr.bin, mgr.args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install tmux via %s: %w", mgr.bin, err)
	}

	// Verify install succeeded.
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux still not found after install — please install manually")
	}
	fmt.Println("tmux installed successfully.")
	return nil
}

// loginShell returns the login shell for the given username by reading /etc/passwd.
func loginShell(username string) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 7)
		if len(fields) == 7 && fields[0] == username {
			return fields[6]
		}
	}
	return ""
}
