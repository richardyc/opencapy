package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

			// Reconcile registry against live tmux sessions.
			// Also auto-register any tmux session not yet in the registry (using its Cwd)
			// so that file browsing works for sessions started outside of opencapy.
			liveSessions, lsErr := tmux.ListSessions()
			if lsErr == nil && reg != nil {
				liveNames := make(map[string]bool)
				for _, s := range liveSessions {
					liveNames[s.Name] = true
					if _, ok := reg.GetProject(s.Name); !ok && s.Cwd != "" {
						_ = reg.Register(s.Name, s.Cwd)
					}
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

			// Apply tmux scroll bindings (1 line/event vs default 5, Magic Trackpad fix)
			tmux.ApplyScrollConfig()

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
			srv := ws.New(cfg.Port, w, reg, pushReg, ptyMgr)

			// Forward PTY output events to the owning WebSocket client.
			// For direct (non-tmux) sessions also feed output into the watcher
			// so event detection (approval/crash/done) works without tmux polling.
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case out := <-ptyMgr.Events():
						srv.SendPTYOutput(out)
						if out.Data != nil && srv.IsDirectSession(out.Session) {
							w.Feed(out.Session, string(out.Data))
						}
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

			// Session reconciler: every 2 s, diff live tmux sessions against the
			// registry.  Adds sessions created on Mac, removes sessions killed on Mac,
			// and broadcasts a snapshot to iOS whenever anything changed.
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						live, err := tmux.ListSessions()
						if err != nil {
							continue
						}
						liveMap := make(map[string]string, len(live))
						for _, s := range live {
							// Skip internal PTY mirror sessions (ocpy_*).
							if strings.HasPrefix(s.Name, "ocpy_") {
								continue
							}
							liveMap[s.Name] = s.Cwd
						}

						changed := false

						// Add sessions that exist in tmux but not in the registry/watcher.
						for name, cwd := range liveMap {
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

						// Remove sessions that are in the registry but no longer in tmux.
						// Skip direct sessions — they are managed by the shim, not tmux.
						if reg != nil {
							for name := range reg.All() {
								if _, alive := liveMap[name]; !alive {
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
