package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/richardyc/opencapy/internal/config"
	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/tmux"
	"github.com/richardyc/opencapy/internal/watcher"
	"github.com/spf13/cobra"
)

// ensureDaemon checks if the daemon is running and starts it in the background if not.
func ensureDaemon() {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	addr := "127.0.0.1:" + strconv.Itoa(cfg.Port)
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err == nil {
		conn.Close()
		return // already running
	}
	// Not running — launch in background.
	self, err := os.Executable()
	if err != nil {
		self = "opencapy"
	}
	proc := exec.Command(self, "daemon")
	proc.Stdout = nil
	proc.Stderr = nil
	if err := proc.Start(); err == nil {
		fmt.Println("→ daemon started in background (port", cfg.Port, ")")
		// Brief pause so daemon is ready before we attach.
		time.Sleep(300 * time.Millisecond)
	}
}

func runRoot(cmd *cobra.Command, args []string) error {
	// Auto-start daemon if not already running.
	ensureDaemon()

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

func newApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <session>",
		Short: "Send approval keystroke to a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return tmux.SendKeys(args[0], "y")
		},
	}
}

func newDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny <session>",
		Short: "Send deny keystroke to a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return tmux.SendKeys(args[0], "n")
		},
	}
}
