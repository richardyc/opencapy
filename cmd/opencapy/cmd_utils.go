package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
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

			hostname := platform.Hostname()
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

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install daemon as system service (LaunchAgent on Mac, systemd on Linux)",
		RunE: func(cmd *cobra.Command, args []string) error {
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
}
