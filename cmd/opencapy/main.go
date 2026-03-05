package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/richardyc/opencapy/internal/config"
	"github.com/richardyc/opencapy/internal/platform"
	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/tmux"
	"github.com/richardyc/opencapy/internal/watcher"
	"github.com/richardyc/opencapy/internal/ws"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "opencapy [name]",
		Short: "Your machines, mirrored. Code from anywhere.",
		Long:  "OpenCapy — tmux session manager with iOS mirroring via WebSocket.",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runRoot,
	}

	rootCmd.AddCommand(
		newLsCmd(),
		newAttachCmd(),
		newKillCmd(),
		newStatusCmd(),
		newDaemonCmd(),
		newQRCmd(),
		newInstallCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runRoot(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	name := filepath.Base(cwd)
	if len(args) > 0 {
		name = args[0]
	}

	exists, err := tmux.SessionExists(name)
	if err != nil {
		return fmt.Errorf("check session: %w", err)
	}

	if exists {
		fmt.Printf("Attaching to existing session %q\n", name)
		return tmux.Attach(name)
	}

	// Create new session
	if err := tmux.NewSession(name, cwd); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// Register in project registry
	reg, err := project.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load registry: %v\n", err)
	} else {
		if err := reg.Register(name, cwd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not register session: %v\n", err)
		}
	}

	fmt.Printf("Created session %q (%s)\n", name, cwd)
	return tmux.Attach(name)
}

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List all tmux sessions with project info",
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := tmux.ListSessions()
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}

			if len(sessions) == 0 {
				fmt.Println("No active sessions.")
				return nil
			}

			reg, _ := project.Load()

			fmt.Printf("%-20s %-40s %-8s %s\n", "SESSION", "PROJECT", "WINDOWS", "CREATED")
			fmt.Printf("%-20s %-40s %-8s %s\n", "-------", "-------", "-------", "-------")

			for _, s := range sessions {
				projectPath := s.Cwd
				if reg != nil {
					if p, ok := reg.GetProject(s.Name); ok {
						projectPath = p
					}
				}

				// Check for CC activity
				output, _ := tmux.CapturePaneOutput(s.Name, 5)
				status := ""
				if output != "" {
					events := watcher.DetectEvents(s.Name, output)
					if len(events) > 0 {
						status = " [" + string(events[len(events)-1].Type) + "]"
					}
				}

				fmt.Printf("%-20s %-40s %-8d %s%s\n",
					s.Name,
					projectPath,
					s.Windows,
					s.Created.Format("2006-01-02 15:04"),
					status,
				)
			}
			return nil
		},
	}
}

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to a tmux session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return tmux.Attach(args[0])
		},
	}
}

func newKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <name>",
		Short: "Kill a tmux session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if err := tmux.KillSession(name); err != nil {
				return fmt.Errorf("kill session: %w", err)
			}

			// Remove from registry
			reg, err := project.Load()
			if err == nil {
				_ = reg.Unregister(name)
			}

			fmt.Printf("Killed session %q\n", name)
			return nil
		},
	}
}

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

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Start the daemon in foreground (WebSocket server + pane watcher)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle signals
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Println("\nShutting down...")
				cancel()
			}()

			// Start watcher
			w := watcher.New(time.Duration(cfg.PollInterval) * time.Millisecond)

			// Load existing sessions into watcher
			reg, err := project.Load()
			if err == nil {
				for name, path := range reg.All() {
					w.AddSession(name, path)
				}
			}

			go w.Start(ctx)

			// Start WebSocket server
			srv := ws.New(cfg.Port, w.Events())

			fmt.Printf("OpenCapy daemon starting on :%d\n", cfg.Port)
			fmt.Printf("Host: %s\n", platform.Hostname())

			return srv.Start(ctx)
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
			fmt.Printf("  %s\n", url)
			fmt.Println()
			fmt.Println("Open the OpenCapy iOS app and enter this URL,")
			fmt.Println("or scan the QR code (coming soon).")
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
