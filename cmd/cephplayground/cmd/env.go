package cmd

import (
	"os"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print client environment variables (eval-friendly, no colors)",
	RunE:  runEnv,
}

func init() {
	rootCmd.AddCommand(envCmd)
}

func runEnv(cmd *cobra.Command, args []string) error {
	cfg := baseConfig()
	st, err := adm.LoadState(cfg)
	if err != nil {
		return err
	}
	adm.PrintEnv(os.Stdout, st)
	return nil
}
