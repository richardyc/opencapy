package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/richardyc/opencapy/internal/config"
	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/session"
	"github.com/spf13/cobra"
)

// defaultShell returns the user's shell from $SHELL, falling back to /bin/sh.
func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// ensureDaemon checks if the daemon is running and starts it in the background if not.
// Strips CLAUDECODE from the daemon's environment so it can spawn claude sessions
// even when ensureDaemon is called from inside a Claude Code session.
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
	proc.Env = filteredEnv()
	proc.Stdout = nil
	proc.Stderr = nil
	if err := proc.Start(); err == nil {
		fmt.Println("→ daemon started in background (port", cfg.Port, ")")
		time.Sleep(300 * time.Millisecond)
	}
}

// filteredEnv returns os.Environ() with CLAUDECODE removed so daemon processes
// can spawn claude without triggering the "nested session" guard.
func filteredEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			env = append(env, e)
		}
	}
	return env
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

	// Explicit name → attach ONLY.
	name := args[0]
	if !session.SessionExists(name) {
		return fmt.Errorf("session %q not found — use 'opencapy new %s' to create it", name, name)
	}
	fmt.Printf("Attaching to session %q\n", name)
	cols, rows := session.GetTerminalSize()
	return session.Attach(name, rows, cols)
}

// createSession creates and registers a new session, then attaches.
func createSession(name, cwd string, command string, args []string) error {
	cols, rows := session.GetTerminalSize()
	if _, err := session.CreateSession(name, cwd, command, args, rows, cols); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	reg, err := project.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load registry: %v\n", err)
	} else if err := reg.Register(name, cwd); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not register session: %v\n", err)
	}
	fmt.Printf("Created session %q (%s)\n", name, cwd)
	return session.Attach(name, rows, cols)
}

func newNewCmd() *cobra.Command {
	var claudeFlag bool
	cmd := &cobra.Command{
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
			if session.SessionExists(name) {
				return fmt.Errorf("session %q already exists — use 'opencapy %s' to attach", name, name)
			}
			command := defaultShell()
			var cmdArgs []string
			if claudeFlag {
				command = "claude"
				cmdArgs = nil
			}
			return createSession(name, cwd, command, cmdArgs)
		},
	}
	cmd.Flags().BoolVar(&claudeFlag, "claude", false, "Start claude directly in the session")
	return cmd
}

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to an existing session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureDaemon()
			cols, rows := session.GetTerminalSize()
			return session.Attach(args[0], rows, cols)
		},
	}
}

func newKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <name>",
		Short: "Kill a session by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureDaemon()
			name := args[0]
			if err := session.KillSession(name); err != nil {
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
			ensureDaemon()
			return session.SendInput(args[0], "y\n")
		},
	}
}

func newDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny <session>",
		Short: "Send deny keystroke to a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureDaemon()
			return session.SendInput(args[0], "n\n")
		},
	}
}
