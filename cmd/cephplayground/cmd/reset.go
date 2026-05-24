package cmd

import (
	"fmt"
	"os"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Destroy then launch again",
	RunE:  runReset,
}

func init() {
	rootCmd.AddCommand(resetCmd)
}

func runReset(cmd *cobra.Command, args []string) error {
	cfg := baseConfig()
	if err := adm.Destroy(os.Stdout, os.Stderr, cfg, false); err != nil && !adm.IsMissingStateErr(err) {
		fmt.Fprintf(os.Stderr, "reset destroy phase: %v\n", err)
	}
	return runLaunch(launchCmd, args)
}
