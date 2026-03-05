package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/tmux"
	"github.com/richardyc/opencapy/internal/watcher"
	"github.com/spf13/cobra"
)

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
