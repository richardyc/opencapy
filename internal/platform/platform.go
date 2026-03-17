package platform

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// IsLinux returns true if the current OS is Linux.
func IsLinux() bool {
	return runtime.GOOS == "linux"
}

// IsMacOS returns true if the current OS is macOS.
func IsMacOS() bool {
	return runtime.GOOS == "darwin"
}

// Hostname returns the system hostname.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// tailscaleStatus is a minimal struct for parsing tailscale status --json output.
type tailscaleStatus struct {
	Self struct {
		DNSName string `json:"DNSName"`
	} `json:"Self"`
}

// TailscaleHostname returns the Tailscale MagicDNS hostname (trimmed trailing dot)
// and true if Tailscale is running. Falls back to Hostname() with a warning if not.
func TailscaleHostname() (string, bool) {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		log.Printf("Tailscale not available (%v) — falling back to system hostname", err)
		return Hostname(), false
	}

	var status tailscaleStatus
	if err := json.Unmarshal(out, &status); err != nil || status.Self.DNSName == "" {
		log.Printf("Tailscale status parse error (%v) — falling back to system hostname", err)
		return Hostname(), false
	}

	hostname := strings.TrimSuffix(status.Self.DNSName, ".")
	return hostname, true
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.opencapy.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>daemon</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
        <key>TMPDIR</key>
        <string>{{.TmpDir}}</string>
        <key>HOME</key>
        <string>{{.HomeDir}}</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/opencapy.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/opencapy.err</string>
</dict>
</plist>
`

// InstallLaunchAgent writes the plist and loads it via launchctl.
func InstallLaunchAgent(binaryPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	plistPath := filepath.Join(plistDir, "com.opencapy.daemon.plist")

	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("create plist: %w", err)
	}
	defer f.Close()

	tmpDir := os.Getenv("TMPDIR")
	if tmpDir == "" {
		// Derive from getconf on macOS when TMPDIR is not set.
		if out, err := exec.Command("getconf", "DARWIN_USER_TEMP_DIR").Output(); err == nil {
			tmpDir = strings.TrimSpace(string(out))
		}
	}
	if tmpDir == "" {
		tmpDir = "/tmp"
	}

	homeDir, _ := os.UserHomeDir()

	tmpl := template.Must(template.New("plist").Parse(plistTemplate))
	if err := tmpl.Execute(f, struct {
		BinaryPath string
		TmpDir     string
		HomeDir    string
	}{binaryPath, tmpDir, homeDir}); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Unload first (ignore error if not loaded)
	exec.Command("launchctl", "unload", plistPath).Run()

	cmd := exec.Command("launchctl", "load", plistPath)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

const systemdTemplate = `[Unit]
Description=OpenCapy Daemon
After=network.target

[Service]
Type=simple
ExecStart={{.BinaryPath}} daemon
Restart=always
RestartSec=5
Environment=HOME={{.HomeDir}}
Environment=PATH=/usr/local/bin:/usr/bin:/bin

[Install]
WantedBy=default.target
`

// InstallSystemd writes a user-level systemd unit file and enables + starts it.
// Using the user instance (systemctl --user) means no sudo is required and the
// service runs with the correct HOME / environment for the calling user.
func InstallSystemd(binaryPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	unitPath := filepath.Join(unitDir, "opencapy.service")
	f, err := os.Create(unitPath)
	if err != nil {
		return fmt.Errorf("create unit file: %w", err)
	}
	defer f.Close()

	tmpl := template.Must(template.New("systemd").Parse(systemdTemplate))
	if err := tmpl.Execute(f, struct{ BinaryPath, HomeDir string }{binaryPath, home}); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	// enable + start in one step so the daemon is live immediately.
	cmd := exec.Command("systemctl", "--user", "enable", "--now", "opencapy")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	// Allow the service to survive when the user's login session ends
	// (e.g. SSH disconnect). Ignore errors — loginctl may not be available.
	exec.Command("loginctl", "enable-linger").Run() //nolint:errcheck
	return nil
}

// SystemdUnitPath returns the path to the user-level opencapy systemd unit file.
func SystemdUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "opencapy.service")
}
