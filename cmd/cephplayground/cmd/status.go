package cmd

import (
	"fmt"
	"strings"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show state and probe the RGW endpoint",
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg := baseConfig()
	st, err := adm.LoadState(cfg)
	if err != nil {
		return err
	}

	fmt.Println()
	cyan.Printf("  Playground %s\n", st.Name)
	fmt.Println()
	label(14, "container", white.Sprint(st.ContainerName))
	label(14, "runtime", white.Sprint(st.Runtime))
	label(14, "image", white.Sprint(st.Image))
	label(14, "services", white.Sprint(strings.Join(st.Services, ", ")))
	label(14, "state dir", white.Sprint(st.StateDir))
	label(14, "osd image", white.Sprintf("%s (%s)", st.OSDImage, adm.FormatSize(st.OSDSizeBytes)))
	if st.LoopDevice != "" {
		label(14, "loop", white.Sprint(st.LoopDevice))
	}
	if st.HasService(adm.ServiceRGW) {
		label(14, "endpoint", white.Sprint(st.Endpoint))
		status, err := adm.ProbeRGW(st.Endpoint)
		if err != nil {
			label(14, "rgw probe", red.Sprint(err.Error()))
		} else {
			label(14, "rgw probe", green.Sprintf("HTTP %s", status))
		}
	}
	fmt.Println()
	return nil
}
