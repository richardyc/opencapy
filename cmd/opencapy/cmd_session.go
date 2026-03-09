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
	self, err := os.Executable()
	if err != nil {
		self = "opencapy"
	}
	proc := exec.Command(self, "daemon")
	proc.Stdout = nil
	proc.Stderr = nil
	if err := proc.Start(); err == nil {
		fmt.Println("→ daemon started in background (port", cfg.Port, ")")
		time.Sleep(300 * time.Millisecond)
	}
}

// runRoot is the default command: interactive chooser (no args) or attach-only (with name).
func runRoot(cmd *cobra.Command, args []string) error {
	ensureDaemon()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	if len(args) == 0 {
		// No args → full-screen TUI session manager.
		return runTUI(cwd)
	}

	// Explicit name → attach ONLY. Typos fail loudly; use 'opencapy new' to create.
	name := args[0]
	exists, err := tmux.SessionExists(name)
	if err != nil {
		return fmt.Errorf("check session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session %q not found — use 'opencapy new %s' to create it", name, name)
	}
	fmt.Printf("Attaching to session %q\n", name)
	return tmux.Attach(name)
}

// createSession creates and registers a new session, then attaches.
func createSession(name, cwd string) error {
	if err := tmux.NewSession(name, cwd); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	reg, err := project.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load registry: %v\n", err)
	} else if err := reg.Register(name, cwd); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not register session: %v\n", err)
	}
	fmt.Printf("Created session %q (%s)\n", name, cwd)
	return tmux.Attach(name)
}

// newHereCmd is the non-interactive create command used in editor terminal profiles
// (VSCode, Cursor). Always creates a FRESH session so each "New Terminal" tab gets
// its own session. Names: base, base-2, base-3, …
func newHereCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "here",
		Short: "Create a new session for the current directory (for editor integration)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureDaemon()
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			base := filepath.Base(cwd)
			name := base
			for i := 2; ; i++ {
				exists, err := tmux.SessionExists(name)
				if err != nil {
					return fmt.Errorf("check session: %w", err)
				}
				if !exists {
					break
				}
				name = fmt.Sprintf("%s-%d", base, i)
			}
			return createSession(name, cwd)
		},
	}
}

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "Interactive session manager (same as running opencapy with no args)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureDaemon()
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			return runTUI(cwd)
		},
	}
}

func newNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new [name]",
		Short: "Create a new session (name defaults to current directory)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
				return fmt.Errorf("session %q already exists — use 'opencapy %s' to attach", name, name)
			}
			return createSession(name, cwd)
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
