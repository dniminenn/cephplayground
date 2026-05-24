package cmd

import (
	"fmt"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the binary version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(adm.Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
