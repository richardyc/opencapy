package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	tmpl := template.Must(template.New("plist").Parse(plistTemplate))
	if err := tmpl.Execute(f, struct{ BinaryPath string }{binaryPath}); err != nil {
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

[Install]
WantedBy=multi-user.target
`

// InstallSystemd writes the unit file and enables it.
func InstallSystemd(binaryPath string) error {
	unitPath := "/etc/systemd/system/opencapy.service"

	f, err := os.Create(unitPath)
	if err != nil {
		return fmt.Errorf("create unit file: %w", err)
	}
	defer f.Close()

	tmpl := template.Must(template.New("systemd").Parse(systemdTemplate))
	if err := tmpl.Execute(f, struct{ BinaryPath string }{binaryPath}); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	cmd := exec.Command("systemctl", "enable", "opencapy")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
