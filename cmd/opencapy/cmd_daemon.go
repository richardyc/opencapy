package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/richardyc/opencapy/internal/config"
	"github.com/richardyc/opencapy/internal/fsevent"
	"github.com/richardyc/opencapy/internal/platform"
	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/push"
	ptymanager "github.com/richardyc/opencapy/internal/pty"
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

			// Load push registry
			pushReg, pushErr := push.Load(filepath.Join(os.Getenv("HOME"), ".opencapy"))
			if pushErr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not load push registry: %v\n", pushErr)
			}

			// Initialise APNs client (graceful fallback if not configured)
			if pushReg != nil {
				if err := pushReg.InitAPNs(cfg.APNs); err != nil {
					fmt.Fprintf(os.Stderr, "warning: APNs init: %v\n", err)
				}
			}

			// Create PTY manager
			ptyMgr := ptymanager.NewManager()

			// Start WebSocket server
			srv := ws.New(cfg.Port, w.Events(), reg, pushReg, ptyMgr)

			// Forward PTY output events to the owning WebSocket client only
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case out := <-ptyMgr.Events():
						srv.SendPTYOutput(out)
					}
				}
			}()

			// Start file watcher
			fw, fwErr := fsevent.New()
			if fwErr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not start file watcher: %v\n", fwErr)
			} else {
				// Add all known project paths
				var projectPaths []string
				if reg != nil {
					projectPaths = reg.AllProjects()
				}
				for _, projectPath := range projectPaths {
					if err := fw.AddProject(projectPath); err != nil {
						fmt.Fprintf(os.Stderr, "warning: could not watch project %s: %v\n", projectPath, err)
					}
				}
				go fw.Start(ctx)

				// Forward file events to WebSocket clients
				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						case ev := <-fw.Events():
							srv.BroadcastFileEvent(ev)
						}
					}
				}()
			}

			// Hot-reload: watch sessions.json every 2s, add new sessions dynamically.
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						fresh, err := project.Load()
						if err != nil {
							continue
						}
						for name, path := range fresh.All() {
							if !w.HasSession(name) {
								w.AddSession(name, path)
								if fw != nil {
									_ = fw.AddProject(path)
								}
								if reg != nil {
									_ = reg.Register(name, path)
								}
								// Notify connected iOS clients of the new session.
								srv.BroadcastSnapshot()
							}
						}
					}
				}
			}()

			fmt.Printf("OpenCapy daemon starting on :%d\n", cfg.Port)
			fmt.Printf("Host: %s\n", platform.Hostname())

			return srv.Start(ctx)
		},
	}
}
