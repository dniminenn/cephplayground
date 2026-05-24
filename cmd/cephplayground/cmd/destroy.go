package cmd

import (
	"fmt"
	"os"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var destroyDryRun bool

var destroyCmd = &cobra.Command{
	Use:     "destroy",
	Aliases: []string{"down"},
	Short:   "Stop and remove the playground state",
	RunE:    runDestroy,
}

func init() {
	destroyCmd.Flags().BoolVar(&destroyDryRun, "dry-run", false, "print commands without executing")
	rootCmd.AddCommand(destroyCmd)
}

func runDestroy(cmd *cobra.Command, args []string) error {
	cfg := baseConfig()
	if err := adm.Destroy(os.Stdout, os.Stderr, cfg, destroyDryRun); err != nil {
		return err
	}
	if destroyDryRun {
		yellow.Printf("  would remove %s\n", cfg.StateDir)
		return nil
	}
	fmt.Println()
	green.Printf("  Removed  %s\n\n", cfg.StateDir)
	return nil
}
