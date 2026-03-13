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
		Use:   "opencapy [name]",
		Short: "Your machines, mirrored. Code from anywhere.",
		Long:  "OpenCapy — tmux session manager with iOS mirroring via WebSocket.",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runRoot,
	}

	rootCmd.AddCommand(
		newLsCmd(),
		newNewCmd(),
		newHereCmd(),
		newAttachCmd(),
		newKillCmd(),
		newApproveCmd(),
		newDenyCmd(),
		newStatusCmd(),
		newDaemonCmd(),
		newQRCmd(),
		newInstallCmd(),
		newUpdateCmd(),
		newShimCmd(),
		&cobra.Command{
			Use:   "version",
			Short: "Print version info",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Printf("opencapy %s (commit %s, built %s)\n", version, commit, date)
			},
		},
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
