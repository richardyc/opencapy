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
	"github.com/richardyc/opencapy/internal/session"
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

			// Create session manager
			sessMgr := session.NewManager()

			// Start watcher (event-driven via FeedOutput callback)
			w := watcher.New()

			// Load existing sessions into watcher
			if reg != nil {
				for name, path := range reg.All() {
					w.AddSession(name, path)
				}
			}

			go w.Start(ctx)

			// Start Unix socket for CLI communication.
			// ListenSocket removes stale sockets on startup — no defer cleanup
			// needed here, as a launchd-restarted daemon would race against it.
			sockPath := session.SocketPath()
			defer sessMgr.KillAll()
			go func() {
				if err := sessMgr.ListenSocket(ctx, sockPath); err != nil {
					fmt.Fprintf(os.Stderr, "warning: socket listener: %v\n", err)
				}
			}()

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

			// Start WebSocket server
			srv := ws.New(cfg.Port, w, reg, pushReg, sessMgr)

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

			// Session reconciler: every 2 s, sync daemon sessions with registry
			// and broadcast snapshot to iOS when anything changed.
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						daemonSessions := sessMgr.List()
						daemonMap := make(map[string]string, len(daemonSessions))
						for _, s := range daemonSessions {
							daemonMap[s.Name] = s.Cwd
						}

						changed := false

						// Add daemon sessions not yet in watcher/registry.
						for name, cwd := range daemonMap {
							if !w.HasSession(name) {
								w.AddSession(name, cwd)
								if fw != nil {
									_ = fw.AddProject(cwd)
								}
								if reg != nil {
									_ = reg.Register(name, cwd)
									_ = reg.Save()
								}
								changed = true
							}
						}

						// Remove dead daemon sessions from registry.
						// Skip direct sessions — they are managed by the shim.
						if reg != nil {
							for name := range reg.All() {
								if _, alive := daemonMap[name]; !alive {
									if srv.IsDirectSession(name) {
										continue
									}
									w.RemoveSession(name)
									_ = reg.Unregister(name)
									changed = true
								}
							}
							if changed {
								_ = reg.Save()
							}
						}

						if changed {
							srv.BroadcastSnapshot()
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
