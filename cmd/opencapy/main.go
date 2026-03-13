package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "opencapy",
		Short: "Monitor your Claude sessions from iOS",
		Long:  "OpenCapy — run claude in any terminal and monitor it from your iPhone.\nGet started: opencapy install",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runRoot,
	}

	// Hide the auto-generated completion command — not relevant for our users.
	rootCmd.CompletionOptions.HiddenDefaultCmd = true

	// Grouped help output.
	rootCmd.AddGroup(
		&cobra.Group{ID: "setup", Title: "Setup:"},
		&cobra.Group{ID: "run", Title: "Run:"},
		&cobra.Group{ID: "tmux", Title: "tmux (optional):"},
	)

	// Setup group
	installCmd := newInstallCmd()
	installCmd.GroupID = "setup"
	uninstallCmd := newUninstallCmd()
	uninstallCmd.GroupID = "setup"
	initCmd := newInitCmd()
	initCmd.GroupID = "setup"
	qrCmd := newQRCmd()
	qrCmd.GroupID = "setup"

	// Run group
	daemonCmd := newDaemonCmd()
	daemonCmd.GroupID = "run"
	statusCmd := newStatusCmd()
	statusCmd.GroupID = "run"
	updateCmd := newUpdateCmd()
	updateCmd.GroupID = "run"
	versionCmd := &cobra.Command{
		Use:     "version",
		Short:   "Print version info",
		GroupID: "run",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("opencapy %s (commit %s, built %s)\n", version, commit, date)
		},
	}

	// tmux group
	newCmd := newNewCmd()
	newCmd.GroupID = "tmux"
	attachCmd := newAttachCmd()
	attachCmd.GroupID = "tmux"
	killCmd := newKillCmd()
	killCmd.GroupID = "tmux"
	approveCmd := newApproveCmd()
	approveCmd.GroupID = "tmux"
	denyCmd := newDenyCmd()
	denyCmd.GroupID = "tmux"

	rootCmd.AddCommand(
		// Setup
		installCmd, uninstallCmd, initCmd, qrCmd,
		// Run
		daemonCmd, statusCmd, updateCmd, versionCmd,
		// tmux
		newCmd, attachCmd, killCmd, approveCmd, denyCmd,
		// Hidden — invoked by the claude shell function
		newShimCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
