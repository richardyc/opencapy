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
		// No args → interactive chooser. Includes a "new session here" option.
		return runChooser(cwd)
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

// runChooser shows the interactive session picker and handles creation.
// The special "[+] new" entry creates a session named after cwd.
func runChooser(cwd string) error {
	sessions, _ := tmux.ListSessions()
	reg, _ := project.Load()

	// fzf path
	if _, err := exec.LookPath("fzf"); err == nil {
		return fzfChooser(sessions, reg, cwd)
	}
	// Fallback: numbered list
	return numberedChooser(sessions, reg, cwd)
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

// newTag is the sentinel value used in the chooser to represent "create new session".
const newTag = "\x00new"

// fzfChooser shows a fzf menu with a "[+] new" option at the top.
func fzfChooser(sessions []tmux.Session, reg *project.Registry, cwd string) error {
	cwdName := filepath.Base(cwd)

	var sb strings.Builder
	// "New session" entry first — tab-separated so field {1} is the sentinel.
	fmt.Fprintf(&sb, "%s\t[+] new  %s  (%s)\n", newTag, cwdName, cwd)
	for _, s := range sessions {
		path := s.Cwd
		if reg != nil {
			if p, ok := reg.GetProject(s.Name); ok {
				path = p
			}
		}
		fmt.Fprintf(&sb, "%s\t%s  %s\n", s.Name, s.Name, path)
	}

	fzf := exec.Command("fzf",
		"--height=40%", "--reverse", "--no-multi",
		"--prompt=opencapy > ",
		"--header=Enter: attach/new   Ctrl-C: cancel",
		"--delimiter=\t",
		"--with-nth=2", // show only the display column
	)
	fzf.Stdin = strings.NewReader(sb.String())
	fzf.Stderr = os.Stderr

	out, err := fzf.Output()
	if err != nil {
		return nil // cancelled
	}

	// The output is the display column; we need the key (first field of original input).
	// fzf's --with-nth hides the first field but output is still the full original line.
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil
	}
	key := strings.SplitN(line, "\t", 2)[0]

	if key == newTag {
		return createSession(cwdName, cwd)
	}
	return tmux.Attach(key)
}

// numberedChooser shows a numbered list with "[n]ew" option at the top.
func numberedChooser(sessions []tmux.Session, reg *project.Registry, cwd string) error {
	cwdName := filepath.Base(cwd)

	fmt.Printf("  [n] new   Create session %q (%s)\n", cwdName, cwd)
	for i, s := range sessions {
		path := s.Cwd
		if reg != nil {
			if p, ok := reg.GetProject(s.Name); ok {
				path = p
			}
		}
		fmt.Printf("  [%d] %-20s  %s\n", i+1, s.Name, path)
	}
	fmt.Print("\nChoice (n=new, number, or session name): ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return nil
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "" || input == "q" {
		return nil
	}
	if input == "n" || input == "new" {
		return createSession(cwdName, cwd)
	}
	if idx, err := strconv.Atoi(input); err == nil && idx >= 1 && idx <= len(sessions) {
		return tmux.Attach(sessions[idx-1].Name)
	}
	for _, s := range sessions {
		if s.Name == input {
			return tmux.Attach(s.Name)
		}
	}
	return fmt.Errorf("session %q not found", input)
}

// newHereCmd is the non-interactive create-or-attach command used in editor
// terminal profiles (VSCode, Cursor). It never shows a chooser.
func newHereCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "here",
		Short: "Create or attach to a session for the current directory (for editor integration)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureDaemon()
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			name := filepath.Base(cwd)
			exists, err := tmux.SessionExists(name)
			if err != nil {
				return fmt.Errorf("check session: %w", err)
			}
			if exists {
				return tmux.Attach(name)
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
			return runChooser(cwd)
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
