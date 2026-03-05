package main

import (
	"os"

	"github.com/spf13/cobra"
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
		newAttachCmd(),
		newKillCmd(),
		newApproveCmd(),
		newDenyCmd(),
		newStatusCmd(),
		newDaemonCmd(),
		newQRCmd(),
		newInstallCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
