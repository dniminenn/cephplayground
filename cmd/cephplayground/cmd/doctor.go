package cmd

import (
	"fmt"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check host prerequisites",
	RunE:  runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println()
	cyan.Println("  Host check")
	fmt.Println()
	for _, c := range adm.Doctor() {
		switch c.Status {
		case "ok":
			label(10, c.Name, fmt.Sprintf("%s %s", green.Sprint("ok"), dim.Sprint(c.Detail)))
		case "missing":
			label(10, c.Name, fmt.Sprintf("%s %s", red.Sprint("missing"), dim.Sprint(c.Detail)))
		case "warn":
			label(10, c.Name, fmt.Sprintf("%s %s", yellow.Sprint("warn"), dim.Sprint(c.Detail)))
		}
	}
	fmt.Println()
	return nil
}
