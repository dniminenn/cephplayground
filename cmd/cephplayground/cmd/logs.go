package cmd

import (
	"os"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Follow container logs",
	RunE:  runLogs,
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", true, "follow logs")
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	cfg := baseConfig()
	st, err := adm.LoadState(cfg)
	if err != nil {
		return err
	}
	c := adm.ContainerLogs(st, logsFollow)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
