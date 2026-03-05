package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
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

			// Load registry
			reg, err := project.Load()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not load registry: %v\n", err)
			}

			// Reconcile registry against live tmux sessions
			liveSessions, lsErr := tmux.ListSessions()
			if lsErr == nil && reg != nil {
				liveNames := make(map[string]bool)
				for _, s := range liveSessions {
					liveNames[s.Name] = true
				}
				for name := range reg.All() {
					if !liveNames[name] {
						_ = reg.Unregister(name)
					}
				}
				_ = reg.Save()
			}

			// Start watcher
			w := watcher.New(time.Duration(cfg.PollInterval) * time.Millisecond)

			// Load existing sessions into watcher
			if reg != nil {
				for name, path := range reg.All() {
					w.AddSession(name, path)
				}
			}

			go w.Start(ctx)

			// Start WebSocket server
			srv := ws.New(cfg.Port, w.Events(), reg)

			fmt.Printf("OpenCapy daemon starting on :%d\n", cfg.Port)
			fmt.Printf("Host: %s\n", platform.Hostname())

			return srv.Start(ctx)
		},
	}
}
