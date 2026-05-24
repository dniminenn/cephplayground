package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	cyan   = color.New(color.FgCyan, color.Bold)
	green  = color.New(color.FgGreen, color.Bold)
	red    = color.New(color.FgRed, color.Bold)
	yellow = color.New(color.FgYellow, color.Bold)
	white  = color.New(color.FgWhite, color.Bold)
	dim    = color.New(color.FgHiBlack)
)

var (
	flagName     string
	flagStateDir string
)

var rootCmd = &cobra.Command{
	Use:           "cephplayground",
	Short:         "Disposable Ceph cluster for application development (RGW, CephFS, RBD)",
	Version:       adm.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command. Errors are printed in red to stderr.
func Execute() {
	rootCmd.PersistentFlags().StringVar(&flagName, "name", adm.DefaultName, "playground name")
	rootCmd.PersistentFlags().StringVar(&flagStateDir, "state-dir", "", "state directory (default /tmp/cephplayground/<name>)")
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	if err := rootCmd.Execute(); err != nil {
		red.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// baseConfig builds a Config from persistent flags. Subcommands then override
// flag-specific fields before passing it into the adm package.
func baseConfig() adm.Config {
	cfg := adm.DefaultConfig(flagName)
	if flagStateDir != "" {
		cfg.StateDir = flagStateDir
	}
	adm.NormalizeConfig(&cfg)
	return cfg
}

// col left-pads s up to width with spaces (no truncation).
func col(width int, s string) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// label prints a dim, fixed-width label followed by the value.
func label(width int, name, value string) {
	if name == "" {
		fmt.Printf("  %s  %s\n", dim.Sprint(col(width, "")), value)
		return
	}
	fmt.Printf("  %s  %s\n", dim.Sprint(col(width, name)), value)
}
