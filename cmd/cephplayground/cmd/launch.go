package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/dniminenn/cephplayground/internal/adm"
	"github.com/spf13/cobra"
)

var launchOpts struct {
	runtime         string
	image           string
	ceph            string
	osdSize         string
	osdMemoryTarget string
	rgwPort         int
	region          string
	accessKey       string
	secretKey       string
	uid             string
	services        string
	privileged      bool
	dryRun          bool
}

var launchCmd = &cobra.Command{
	Use:     "launch",
	Aliases: []string{"up"},
	Short:   "Create and start the playground",
	RunE:    runLaunch,
}

func init() {
	defaults := adm.DefaultConfig(adm.DefaultName)
	launchCmd.Flags().StringVar(&launchOpts.runtime, "runtime", "auto", "auto, docker, or podman")
	launchCmd.Flags().StringVar(&launchOpts.image, "image", defaults.Image, "Ceph container image (full path); overridden by --ceph")
	launchCmd.Flags().StringVar(&launchOpts.ceph, "ceph", "", "Ceph release shortcut: tag (v17..v20) or codename (pacific|quincy|reef|squid|tentacle)")
	launchCmd.Flags().StringVar(&launchOpts.osdSize, "osd-size", "16GiB", "tmpfs-backed OSD image size")
	launchCmd.Flags().StringVar(&launchOpts.osdMemoryTarget, "osd-memory-target", "1GiB", "Ceph osd_memory_target")
	launchCmd.Flags().IntVar(&launchOpts.rgwPort, "rgw-port", defaults.RGWPort, "host port for RGW")
	launchCmd.Flags().StringVar(&launchOpts.region, "region", defaults.Region, "S3 region to advertise to clients")
	launchCmd.Flags().StringVar(&launchOpts.accessKey, "access-key", defaults.AccessKey, "S3 access key")
	launchCmd.Flags().StringVar(&launchOpts.secretKey, "secret-key", defaults.SecretKey, "S3 secret key")
	launchCmd.Flags().StringVar(&launchOpts.uid, "uid", defaults.S3UID, "RGW user id")
	launchCmd.Flags().StringVar(&launchOpts.services, "services", strings.Join(defaults.Services, ","), "comma-separated subset of rgw,cephfs,rbd")
	launchCmd.Flags().BoolVar(&launchOpts.privileged, "privileged", false, "run container privileged instead of passing only the OSD loop device")
	launchCmd.Flags().BoolVar(&launchOpts.dryRun, "dry-run", false, "print commands without executing")
	rootCmd.AddCommand(launchCmd)
}

func runLaunch(cmd *cobra.Command, args []string) error {
	cfg := baseConfig()
	cfg.Runtime = launchOpts.runtime
	cfg.Image = launchOpts.image
	cfg.RGWPort = launchOpts.rgwPort
	cfg.Region = launchOpts.region
	cfg.AccessKey = launchOpts.accessKey
	cfg.SecretKey = launchOpts.secretKey
	cfg.S3UID = launchOpts.uid
	cfg.Privileged = launchOpts.privileged
	cfg.DryRun = launchOpts.dryRun

	services, err := adm.ParseServices(launchOpts.services)
	if err != nil {
		return err
	}
	cfg.Services = services

	if launchOpts.ceph != "" {
		cfg.Image = adm.ResolveCephImage(launchOpts.ceph)
	}

	cfg.OSDSizeBytes, err = adm.ParseSize(launchOpts.osdSize)
	if err != nil {
		return fmt.Errorf("invalid --osd-size: %w", err)
	}
	cfg.OSDMemoryTarget, err = adm.ParseSize(launchOpts.osdMemoryTarget)
	if err != nil {
		return fmt.Errorf("invalid --osd-memory-target: %w", err)
	}

	fmt.Println()
	cyan.Printf("  Launching %s\n", cfg.ContainerName)
	fmt.Println()
	label(14, "image", white.Sprint(cfg.Image))
	label(14, "services", white.Sprint(strings.Join(cfg.Services, ", ")))
	label(14, "state dir", white.Sprint(cfg.StateDir))
	label(14, "osd size", white.Sprint(adm.FormatSize(cfg.OSDSizeBytes)))
	if !cfg.DryRun {
		fmt.Println()
		dim.Println("  starting cluster, this takes 20-40s...")
	}
	fmt.Println()

	st, err := adm.Launch(os.Stdout, os.Stderr, cfg)
	if err != nil {
		return err
	}
	if cfg.DryRun {
		yellow.Printf("  dry run complete\n")
		fmt.Println()
		return nil
	}

	green.Printf("  Ready  %s\n", st.ContainerName)
	fmt.Println()
	if st.HasService(adm.ServiceRGW) {
		label(14, "RGW", white.Sprint(st.Endpoint))
	}
	if st.HasService(adm.ServiceCephFS) {
		label(14, "CephFS", white.Sprintf("%s (client.%s)", st.CephFSName, st.CephFSClient))
	}
	if st.HasService(adm.ServiceRBD) {
		label(14, "RBD", white.Sprintf("%s/%s (client.%s)", st.RBDPool, st.RBDImage, st.RBDClient))
	}
	fmt.Println()
	dim.Println("  client variables (eval to load):")
	fmt.Println()
	adm.PrintEnv(os.Stdout, st)
	fmt.Println()
	dim.Printf("  reprint with: cephplayground env --name %s\n\n", cfg.Name)
	return nil
}
