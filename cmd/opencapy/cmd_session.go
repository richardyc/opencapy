package main

import (
	"bufio"
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
	ensureDaemon()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	if len(args) == 0 {
		// No args: create or attach for the current directory (main dev workflow).
		return createOrAttach(filepath.Base(cwd), cwd)
	}

	// With an explicit name: attach ONLY. A typo should fail, not silently create a session.
	name := args[0]
	exists, err := tmux.SessionExists(name)
	if err != nil {
		return fmt.Errorf("check session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session %q not found\n  → run 'opencapy new %s' to create it", name, name)
	}
	fmt.Printf("Attaching to session %q\n", name)
	return tmux.Attach(name)
}

// createOrAttach creates session `name` in `cwd` if it doesn't exist, then attaches.
func createOrAttach(name, cwd string) error {
	exists, err := tmux.SessionExists(name)
	if err != nil {
		return fmt.Errorf("check session: %w", err)
	}
	if exists {
		fmt.Printf("Attaching to existing session %q\n", name)
		return tmux.Attach(name)
	}

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

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "Interactive session manager (attach or kill)",
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := tmux.ListSessions()
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			if len(sessions) == 0 {
				fmt.Println("No active sessions. Create one with 'opencapy new'.")
				return nil
			}

			reg, _ := project.Load()

			// Inside tmux: use the native tmux chooser (C-b s equivalent).
			// Supports arrow keys, Enter=attach, x=kill, q=quit.
			if os.Getenv("TMUX") != "" {
				return exec.Command("tmux", "choose-session").Run()
			}

			// Outside tmux: use fzf if available.
			if _, err := exec.LookPath("fzf"); err == nil {
				return fzfChooser(sessions, reg)
			}

			// Fallback: numbered list.
			return numberedChooser(sessions, reg)
		},
	}
}

func newNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new [name]",
		Short: "Create a new session (defaults to current directory name)",
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
		},
	}
}

// fzfChooser pipes sessions through fzf and attaches to the selection.
func fzfChooser(sessions []tmux.Session, reg *project.Registry) error {
	var sb strings.Builder
	for _, s := range sessions {
		path := s.Cwd
		if reg != nil {
			if p, ok := reg.GetProject(s.Name); ok {
				path = p
			}
		}
		fmt.Fprintf(&sb, "%s\t%s\n", s.Name, path)
	}

	fzf := exec.Command("fzf",
		"--height=40%", "--reverse", "--no-multi",
		"--prompt=opencapy > ",
		"--header=Enter: attach   Ctrl-C: cancel",
		"--delimiter=\t", "--with-nth=1,2",
	)
	fzf.Stdin = strings.NewReader(sb.String())
	fzf.Stderr = os.Stderr

	out, err := fzf.Output()
	if err != nil {
		return nil // user cancelled
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil
	}
	name := strings.SplitN(line, "\t", 2)[0]
	return tmux.Attach(name)
}

// numberedChooser shows a numbered list and attaches to the chosen session.
func numberedChooser(sessions []tmux.Session, reg *project.Registry) error {
	fmt.Println("Active sessions:")
	for i, s := range sessions {
		path := s.Cwd
		if reg != nil {
			if p, ok := reg.GetProject(s.Name); ok {
				path = p
			}
		}
		fmt.Printf("  [%d] %-20s  %s\n", i+1, s.Name, path)
	}
	fmt.Print("\nAttach to (number or name, Enter to cancel): ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return nil
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "" || input == "q" {
		return nil
	}

	if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= len(sessions) {
		return tmux.Attach(sessions[n-1].Name)
	}
	for _, s := range sessions {
		if s.Name == input {
			return tmux.Attach(s.Name)
		}
	}
	return fmt.Errorf("session %q not found", input)
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
