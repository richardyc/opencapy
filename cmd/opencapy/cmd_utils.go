package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
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
			exec.Command("brew", "update").Run() //nolint:errcheck

			upgradeOut, _ := exec.Command("brew", "upgrade", "richardyc/opencapy/opencapy").CombinedOutput()
			upgraded := !strings.Contains(string(upgradeOut), "already installed")

			if !upgraded {
				newBin, _ := exec.LookPath("opencapy")
				ver, _ := exec.Command(newBin, "version").Output()
				fmt.Printf("Already on the latest version. %s", ver)
				return nil
			}

			if strings.Contains(string(upgradeOut), "brew link") {
				exec.Command("brew", "link", "--overwrite", "opencapy").Run() //nolint:errcheck
			}
			fmt.Println(strings.TrimSpace(string(upgradeOut)))
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
				if err := exec.Command("sudo", "systemctl", "restart", "opencapy").Run(); err != nil {
					return fmt.Errorf("systemctl restart: %w", err)
				}
				return verifyDaemon()
			}

			return fmt.Errorf("unsupported platform — restart the daemon manually")
		},
	}
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
				fmt.Println("✓ Daemon installed (systemd)")
				fmt.Println("  Start now: sudo systemctl start opencapy")
			} else {
				return fmt.Errorf("unsupported platform")
			}

			injectShellIntegration()

			fmt.Println()
			fmt.Println("Done! Open a new terminal, then run: claude")
			fmt.Println("To pair your iPhone:  opencapy qr")
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
			unitPath := "/etc/systemd/system/opencapy.service"
			if _, err := os.Stat(unitPath); err == nil {
				exec.Command("sudo", "systemctl", "disable", "--now", "opencapy").Run() //nolint:errcheck
				os.Remove(unitPath)
				fmt.Println("✓ systemd service removed")
			}

			// Kill any running daemon.
			cfg, _ := config.Load()
			port := 7242
			if cfg != nil {
				port = cfg.Port
			}
			killAndWait(port)
			fmt.Println("✓ Daemon stopped")

			// Remove shell integration.
			removeShellIntegration(home)

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
