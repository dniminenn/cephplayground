package cmd

import (
	"os"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a shell in the playground container",
	RunE:  runShell,
}

func init() {
	rootCmd.AddCommand(shellCmd)
}

func runShell(cmd *cobra.Command, args []string) error {
	cfg := baseConfig()
	st, err := adm.LoadState(cfg)
	if err != nil {
		return err
	}
	c := adm.ContainerShell(st)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
