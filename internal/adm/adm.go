package adm

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	appName      = "cephplayground"
	stateVersion = 2
	markerName   = ".cephplayground"
	defaultName  = "rgw"
	defaultImage = "quay.io/ceph/ceph:v19"
)

//go:embed assets/*
var assets embed.FS

type CLI struct {
	out io.Writer
	err io.Writer
}

type Config struct {
	Name            string
	StateDir        string
	ContainerName   string
	OSDImage        string
	OSDSizeBytes    int64
	OSDMemoryTarget int64
	Runtime         string
	Image           string
	RGWPort         int
	Region          string
	AccessKey       string
	SecretKey       string
	S3UID           string
	Privileged      bool
	DryRun          bool
}

type State struct {
	Version         int       `json:"version"`
	Name            string    `json:"name"`
	StateDir        string    `json:"state_dir"`
	ContainerName   string    `json:"container_name"`
	Runtime         string    `json:"runtime"`
	Image           string    `json:"image"`
	LoopDevice      string    `json:"loop_device"`
	OSDImage        string    `json:"osd_image"`
	OSDSizeBytes    int64     `json:"osd_size_bytes"`
	OSDMemoryTarget int64     `json:"osd_memory_target"`
	RGWPort         int       `json:"rgw_port"`
	Endpoint        string    `json:"endpoint"`
	Region          string    `json:"region"`
	AccessKey       string    `json:"access_key"`
	SecretKey       string    `json:"secret_key"`
	CreatedAt       time.Time `json:"created_at"`
}

type Runner struct {
	Out    io.Writer
	Err    io.Writer
	DryRun bool
}

func Main(args []string, out, err io.Writer) int {
	c := &CLI{out: out, err: err}
	if len(args) == 0 {
		c.usage()
		return 2
	}

	var runErr error
	switch args[0] {
	case "launch", "up":
		runErr = c.launch(args[1:])
	case "destroy", "down":
		runErr = c.destroy(args[1:])
	case "reset":
		runErr = c.reset(args[1:])
	case "status":
		runErr = c.status(args[1:])
	case "env":
		runErr = c.env(args[1:])
	case "shell":
		runErr = c.shell(args[1:])
	case "logs":
		runErr = c.logs(args[1:])
	case "doctor":
		runErr = c.doctor(args[1:])
	case "help", "-h", "--help":
		c.usage()
		return 0
	default:
		fmt.Fprintf(err, "unknown command %q\n\n", args[0])
		c.usage()
		return 2
	}
	if runErr != nil {
		fmt.Fprintf(err, "error: %v\n", runErr)
		return 1
	}
	return 0
}

func (c *CLI) usage() {
	fmt.Fprintf(c.out, `%s runs a disposable, tmpfs-backed Ceph RGW playground.

Usage:
  %s <command> [flags]

Commands:
  launch          Create and start the playground
  destroy         Stop and remove the playground state
  reset           Destroy then launch again
  status          Show state and probe the RGW endpoint
  env             Print AWS-compatible environment variables
  shell           Open a shell in the playground container
  logs            Follow container logs
  doctor          Check host prerequisites

Most commands accept --name and --state-dir. Defaults use /tmp/%s/<name>.
`, appName, appName, appName)
}

func defaultConfig(name string) Config {
	base := filepath.Join(os.TempDir(), appName, name)
	return Config{
		Name:            name,
		StateDir:        base,
		ContainerName:   "cephplay-" + sanitizeName(name),
		OSDImage:        filepath.Join(base, "osd0.img"),
		OSDSizeBytes:    16 * 1024 * 1024 * 1024,
		OSDMemoryTarget: 1024 * 1024 * 1024,
		Runtime:         "auto",
		Image:           defaultImage,
		RGWPort:         7480,
		Region:          "us-east-1",
		AccessKey:       "play",
		SecretKey:       "playsecret",
		S3UID:           "play",
	}
}

func addCommonFlags(fs *flag.FlagSet, cfg *Config) {
	fs.StringVar(&cfg.Name, "name", cfg.Name, "playground name")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "state directory")
}

func (c *CLI) launch(args []string) error {
	cfg := defaultConfig(defaultName)
	size := "16GiB"
	mem := "1GiB"
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	fs.SetOutput(c.err)
	addCommonFlags(fs, &cfg)
	fs.StringVar(&cfg.Runtime, "runtime", cfg.Runtime, "auto, docker, or podman")
	fs.StringVar(&cfg.Image, "image", cfg.Image, "Ceph container image")
	fs.StringVar(&size, "osd-size", size, "tmpfs-backed OSD image size")
	fs.StringVar(&mem, "osd-memory-target", mem, "Ceph osd_memory_target")
	fs.IntVar(&cfg.RGWPort, "rgw-port", cfg.RGWPort, "host port forwarded to RGW")
	fs.StringVar(&cfg.Region, "region", cfg.Region, "S3 region to advertise to clients")
	fs.StringVar(&cfg.AccessKey, "access-key", cfg.AccessKey, "S3 access key")
	fs.StringVar(&cfg.SecretKey, "secret-key", cfg.SecretKey, "S3 secret key")
	fs.StringVar(&cfg.S3UID, "uid", cfg.S3UID, "RGW user id")
	fs.BoolVar(&cfg.Privileged, "privileged", false, "run container privileged instead of passing only the OSD loop device")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print commands without executing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizeConfig(&cfg)

	var err error
	cfg.OSDSizeBytes, err = parseSize(size)
	if err != nil {
		return fmt.Errorf("invalid --osd-size: %w", err)
	}
	cfg.OSDMemoryTarget, err = parseSize(mem)
	if err != nil {
		return fmt.Errorf("invalid --osd-memory-target: %w", err)
	}
	if cfg.RGWPort <= 0 || cfg.RGWPort > 65535 {
		return fmt.Errorf("invalid --rgw-port %d", cfg.RGWPort)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.S3UID == "" {
		return errors.New("access key, secret key, and uid must be non-empty")
	}
	if cfg.Region == "" {
		return errors.New("region must be non-empty")
	}

	runtimeName, err := resolveRuntime(cfg.Runtime)
	if err != nil {
		return err
	}
	cfg.Runtime = runtimeName

	if !cfg.DryRun && os.Geteuid() != 0 {
		return errors.New("launch must run as root because it creates a loop device")
	}

	r := &Runner{Out: c.out, Err: c.err, DryRun: cfg.DryRun}
	if err := ensureStateDir(cfg.StateDir, cfg.DryRun); err != nil {
		return err
	}
	if err := writeMarker(cfg.StateDir, cfg.DryRun); err != nil {
		return err
	}
	if !cfg.DryRun && exists(filepath.Join(cfg.StateDir, "state.json")) {
		return fmt.Errorf("%s already has playground state; use status, reset, or destroy", cfg.StateDir)
	}
	if err := installContainerAssets(cfg.StateDir, cfg.DryRun); err != nil {
		return err
	}
	if err := writeGuestConfig(cfg, cfg.DryRun); err != nil {
		return err
	}
	if err := ensureOSDImage(cfg.OSDImage, cfg.OSDSizeBytes, cfg.DryRun); err != nil {
		return err
	}

	loop, err := attachLoop(context.Background(), r, cfg.OSDImage)
	if err != nil {
		return err
	}

	st := State{
		Version:         stateVersion,
		Name:            cfg.Name,
		StateDir:        cfg.StateDir,
		ContainerName:   cfg.ContainerName,
		Runtime:         cfg.Runtime,
		Image:           cfg.Image,
		LoopDevice:      loop,
		OSDImage:        cfg.OSDImage,
		OSDSizeBytes:    cfg.OSDSizeBytes,
		OSDMemoryTarget: cfg.OSDMemoryTarget,
		RGWPort:         cfg.RGWPort,
		Endpoint:        fmt.Sprintf("http://127.0.0.1:%d", cfg.RGWPort),
		Region:          cfg.Region,
		AccessKey:       cfg.AccessKey,
		SecretKey:       cfg.SecretKey,
		CreatedAt:       time.Now().UTC(),
	}
	if err := saveState(cfg.StateDir, st, cfg.DryRun); err != nil {
		_ = r.Run(context.Background(), "losetup", "-d", loop)
		return err
	}
	if err := runContainer(context.Background(), r, cfg, loop); err != nil {
		_ = r.Run(context.Background(), "losetup", "-d", loop)
		if !cfg.DryRun {
			_ = os.Remove(filepath.Join(cfg.StateDir, "state.json"))
		}
		return err
	}
	if cfg.DryRun {
		fmt.Fprintf(c.out, "dry run complete for %s\nendpoint would be: %s\n", cfg.ContainerName, st.Endpoint)
		return nil
	}

	fmt.Fprintf(c.out, "waiting for RGW at %s\n", st.Endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := waitForReady(ctx, filepath.Join(cfg.StateDir, "ready"), st.Endpoint); err != nil {
		return fmt.Errorf("%w; inspect with `%s logs --name %s` or clean up with `%s destroy --name %s`", err, appName, cfg.Name, appName, cfg.Name)
	}

	fmt.Fprintf(c.out, "ready %s\nendpoint: %s\n", cfg.ContainerName, st.Endpoint)
	fmt.Fprintln(c.out)
	printEnv(c.out, st)
	fmt.Fprintf(c.out, "\nrun `%s env --name %s` to print these variables again\n", appName, cfg.Name)
	return nil
}

func (c *CLI) destroy(args []string) error {
	cfg := defaultConfig(defaultName)
	dryRun := false
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	fs.SetOutput(c.err)
	addCommonFlags(fs, &cfg)
	fs.BoolVar(&dryRun, "dry-run", false, "print commands without executing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizeConfig(&cfg)
	if !dryRun && os.Geteuid() != 0 {
		return errors.New("destroy must run as root because it detaches the playground loop device")
	}
	if err := requireMarker(cfg.StateDir); err != nil {
		return err
	}

	st, _ := loadState(cfg.StateDir)
	if st.ContainerName == "" {
		st.ContainerName = cfg.ContainerName
	}
	if st.Runtime == "" {
		if runtimeName, err := resolveRuntime(cfg.Runtime); err == nil {
			st.Runtime = runtimeName
		}
	}
	if st.OSDImage == "" {
		st.OSDImage = cfg.OSDImage
	}

	r := &Runner{Out: c.out, Err: c.err, DryRun: dryRun}
	if st.Runtime != "" {
		_ = r.Run(context.Background(), st.Runtime, "stop", "--timeout", "30", st.ContainerName)
		_ = r.Run(context.Background(), st.Runtime, "rm", "-f", st.ContainerName)
	}
	if st.LoopDevice != "" {
		_ = r.Run(context.Background(), "losetup", "-d", st.LoopDevice)
	}
	if loops, err := loopDevicesFor(context.Background(), r, st.OSDImage); err == nil {
		for _, loop := range loops {
			_ = r.Run(context.Background(), "losetup", "-d", loop)
		}
	}
	if dryRun {
		fmt.Fprintf(c.out, "would remove %s\n", cfg.StateDir)
		return nil
	}
	if err := os.RemoveAll(cfg.StateDir); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "removed %s\n", cfg.StateDir)
	return nil
}

func (c *CLI) reset(args []string) error {
	destroyArgs := resetDestroyArgs(args)
	if err := c.destroy(destroyArgs); err != nil && !isMissingStateErr(err) {
		fmt.Fprintf(c.err, "reset destroy phase: %v\n", err)
	}
	return c.launch(args)
}

func (c *CLI) status(args []string) error {
	cfg := defaultConfig(defaultName)
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(c.err)
	addCommonFlags(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizeConfig(&cfg)

	st, err := loadState(cfg.StateDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "name: %s\nstate: %s\ncontainer: %s\nruntime: %s\nimage: %s\nendpoint: %s\nosd image: %s (%s)\n",
		st.Name, st.StateDir, st.ContainerName, st.Runtime, st.Image, st.Endpoint, st.OSDImage, formatSize(st.OSDSizeBytes))
	if st.LoopDevice != "" {
		fmt.Fprintf(c.out, "loop: %s\n", st.LoopDevice)
	}

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(st.Endpoint)
	if err != nil {
		fmt.Fprintf(c.out, "rgw probe: failed: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	fmt.Fprintf(c.out, "rgw probe: HTTP %s\n", resp.Status)
	return nil
}

func (c *CLI) env(args []string) error {
	cfg := defaultConfig(defaultName)
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	fs.SetOutput(c.err)
	addCommonFlags(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizeConfig(&cfg)

	if b, err := os.ReadFile(filepath.Join(cfg.StateDir, "env")); err == nil {
		_, _ = c.out.Write(b)
		return nil
	}
	st, err := loadState(cfg.StateDir)
	if err != nil {
		return err
	}
	printEnv(c.out, st)
	return nil
}

func (c *CLI) shell(args []string) error {
	cfg := defaultConfig(defaultName)
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(c.err)
	addCommonFlags(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizeConfig(&cfg)
	st, err := loadState(cfg.StateDir)
	if err != nil {
		return err
	}
	cmd := exec.Command(st.Runtime, "exec", "-it", st.ContainerName, "bash")
	cmd.Stdin = os.Stdin
	cmd.Stdout = c.out
	cmd.Stderr = c.err
	return cmd.Run()
}

func (c *CLI) logs(args []string) error {
	cfg := defaultConfig(defaultName)
	follow := true
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(c.err)
	addCommonFlags(fs, &cfg)
	fs.BoolVar(&follow, "f", follow, "follow logs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	normalizeConfig(&cfg)
	st, err := loadState(cfg.StateDir)
	if err != nil {
		return err
	}
	largs := []string{"logs"}
	if follow {
		largs = append(largs, "-f")
	}
	largs = append(largs, st.ContainerName)
	cmd := exec.Command(st.Runtime, largs...)
	cmd.Stdout = c.out
	cmd.Stderr = c.err
	return cmd.Run()
}

func (c *CLI) doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(c.err)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "os: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(c.out, "uid: %d\n", os.Geteuid())

	for _, name := range []string{"losetup"} {
		if path, err := exec.LookPath(name); err == nil {
			fmt.Fprintf(c.out, "ok: %s -> %s\n", name, path)
		} else {
			fmt.Fprintf(c.out, "missing: %s\n", name)
		}
	}
	if path, err := exec.LookPath("docker"); err == nil {
		fmt.Fprintf(c.out, "ok: docker -> %s\n", path)
	} else {
		fmt.Fprintf(c.out, "optional missing: docker (needed unless podman is available)\n")
	}
	if path, err := exec.LookPath("podman"); err == nil {
		fmt.Fprintf(c.out, "ok: podman -> %s\n", path)
	} else {
		fmt.Fprintf(c.out, "optional missing: podman (needed unless docker is available)\n")
	}

	tmp := os.TempDir()
	if isTmpfs(tmp) {
		fmt.Fprintf(c.out, "ok: %s appears to be tmpfs\n", tmp)
	} else {
		fmt.Fprintf(c.out, "warn: %s is not detected as tmpfs; use --state-dir on tmpfs if SSD avoidance matters\n", tmp)
	}
	return nil
}

func normalizeConfig(cfg *Config) {
	if cfg.Name == "" {
		cfg.Name = defaultName
	}
	defaultBase := filepath.Join(os.TempDir(), appName, defaultName)
	if cfg.StateDir == "" || (cfg.StateDir == defaultBase && cfg.Name != defaultName) {
		cfg.StateDir = filepath.Join(os.TempDir(), appName, cfg.Name)
	}
	cfg.StateDir = filepath.Clean(cfg.StateDir)
	cfg.ContainerName = "cephplay-" + sanitizeName(cfg.Name)
	cfg.OSDImage = filepath.Join(cfg.StateDir, "osd0.img")
}

func ensureStateDir(path string, dryRun bool) error {
	if path == "" || path == "/" {
		return fmt.Errorf("refusing unsafe state dir %q", path)
	}
	if dryRun {
		return nil
	}
	return os.MkdirAll(path, 0o755)
}

func writeMarker(path string, dryRun bool) error {
	if dryRun {
		return nil
	}
	return os.WriteFile(filepath.Join(path, markerName), []byte(appName+"\n"), 0o644)
}

func requireMarker(path string) error {
	b, err := os.ReadFile(filepath.Join(path, markerName))
	if err != nil {
		return fmt.Errorf("refusing to touch %s: marker %s missing", path, markerName)
	}
	if strings.TrimSpace(string(b)) != appName {
		return fmt.Errorf("refusing to touch %s: marker content is not %q", path, appName)
	}
	return nil
}

func installContainerAssets(stateDir string, dryRun bool) error {
	data, err := assets.ReadFile("assets/container-entrypoint.sh")
	if err != nil {
		return err
	}
	target := filepath.Join(stateDir, "container-entrypoint.sh")
	if dryRun {
		return nil
	}
	return os.WriteFile(target, data, 0o755)
}

func writeGuestConfig(cfg Config, dryRun bool) error {
	if dryRun {
		return nil
	}
	fsid := randomUUIDLike()
	env := fmt.Sprintf(`FSID=%s
OSD_ID=0
OSD_DEVICE=/dev/cephplay-osd0
OSD_MEMORY_TARGET=%d
RGW_PORT=%d
S3_REGION=%s
S3_UID=%s
AWS_ACCESS_KEY_ID=%s
AWS_SECRET_ACCESS_KEY=%s
`, shellPlain(fsid), cfg.OSDMemoryTarget, cfg.RGWPort, shellQuote(cfg.Region), shellQuote(cfg.S3UID), shellQuote(cfg.AccessKey), shellQuote(cfg.SecretKey))
	return os.WriteFile(filepath.Join(cfg.StateDir, "config.env"), []byte(env), 0o600)
}

func ensureOSDImage(path string, size int64, dryRun bool) error {
	if dryRun {
		return nil
	}
	if exists(path) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

func attachLoop(ctx context.Context, r *Runner, image string) (string, error) {
	if r.DryRun {
		fmt.Fprintf(r.Out, "would run: losetup --find --show %s\n", image)
		return "/dev/loopDRYRUN", nil
	}
	out, err := r.Output(ctx, "losetup", "--find", "--show", image)
	if err != nil {
		return "", err
	}
	loop := strings.TrimSpace(out)
	if loop == "" {
		return "", errors.New("losetup returned empty loop device")
	}
	return loop, nil
}

func runContainer(ctx context.Context, r *Runner, cfg Config, loop string) error {
	args := []string{
		"run",
		"-d",
		"--name", cfg.ContainerName,
		"--hostname", cfg.ContainerName,
		"--stop-timeout", "30",
		"-p", fmt.Sprintf("127.0.0.1:%d:7480", cfg.RGWPort),
		"--device", loop + ":/dev/cephplay-osd0",
		"--cap-add", "SYS_ADMIN",
		"--tmpfs", "/etc/ceph:rw,size=64m,mode=755",
		"--tmpfs", "/run/ceph:rw,size=64m,mode=755",
		"--tmpfs", "/var/lib/ceph:rw,size=2g,mode=755",
		"--tmpfs", "/var/log/ceph:rw,size=256m,mode=755",
		"--tmpfs", "/tmp:rw,size=1g,mode=1777",
		"-v", cfg.StateDir + ":/cephplay",
	}
	if cfg.Privileged {
		args = append(args, "--privileged")
	}
	args = append(args, cfg.Image, "/cephplay/container-entrypoint.sh")
	return r.Run(ctx, cfg.Runtime, args...)
}

func loopDevicesFor(ctx context.Context, r *Runner, image string) ([]string, error) {
	if r.DryRun {
		return nil, nil
	}
	out, err := r.Output(ctx, "losetup", "-j", image)
	if err != nil {
		return nil, err
	}
	var loops []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if idx := strings.Index(line, ":"); idx > 0 {
			loops = append(loops, line[:idx])
		}
	}
	return loops, nil
}

func saveState(dir string, st State, dryRun bool) error {
	if dryRun {
		return nil
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644)
}

func printEnv(out io.Writer, st State) {
	fmt.Fprintf(out, "export AWS_ACCESS_KEY_ID=%s\n", shellQuote(st.AccessKey))
	fmt.Fprintf(out, "export AWS_SECRET_ACCESS_KEY=%s\n", shellQuote(st.SecretKey))
	fmt.Fprintf(out, "export AWS_REGION=%s\n", shellQuote(st.Region))
	fmt.Fprintf(out, "export AWS_DEFAULT_REGION=%s\n", shellQuote(st.Region))
	fmt.Fprintf(out, "export AWS_ENDPOINT_URL=%s\n", shellQuote(st.Endpoint))
	fmt.Fprintf(out, "export CEPHPLAY_ENDPOINT=%s\n", shellQuote(st.Endpoint))
	fmt.Fprintf(out, "export CEPHPLAY_REGION=%s\n", shellQuote(st.Region))
}

func loadState(dir string) (State, error) {
	var st State
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

func waitForReady(ctx context.Context, readyPath, endpoint string) error {
	client := http.Client{Timeout: 2 * time.Second}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		if exists(readyPath) {
			resp, err := client.Get(endpoint)
			if err == nil {
				_ = resp.Body.Close()
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for RGW")
		case <-tick.C:
		}
	}
}

func (r *Runner) Run(ctx context.Context, name string, args ...string) error {
	if r.DryRun {
		fmt.Fprintf(r.Out, "would run: %s %s\n", name, strings.Join(args, " "))
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Err
	return cmd.Run()
}

func (r *Runner) Output(ctx context.Context, name string, args ...string) (string, error) {
	if r.DryRun {
		fmt.Fprintf(r.Out, "would run: %s %s\n", name, strings.Join(args, " "))
		return "", nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = r.Err
	b, err := cmd.Output()
	return string(b), err
}

func resolveRuntime(runtimeName string) (string, error) {
	runtimeName = strings.ToLower(strings.TrimSpace(runtimeName))
	if runtimeName == "" || runtimeName == "auto" {
		if _, err := exec.LookPath("docker"); err == nil {
			return "docker", nil
		}
		if _, err := exec.LookPath("podman"); err == nil {
			return "podman", nil
		}
		return "", errors.New("neither docker nor podman was found")
	}
	if runtimeName != "docker" && runtimeName != "podman" {
		return "", fmt.Errorf("unsupported runtime %q", runtimeName)
	}
	if _, err := exec.LookPath(runtimeName); err != nil {
		return "", fmt.Errorf("%s not found", runtimeName)
	}
	return runtimeName, nil
}

func parseSize(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty size")
	}
	mult := int64(1)
	units := []struct {
		suffix string
		mult   int64
	}{
		{"gib", 1024 * 1024 * 1024},
		{"gb", 1000 * 1000 * 1000},
		{"gi", 1024 * 1024 * 1024},
		{"g", 1024 * 1024 * 1024},
		{"mib", 1024 * 1024},
		{"mb", 1000 * 1000},
		{"mi", 1024 * 1024},
		{"m", 1024 * 1024},
		{"kib", 1024},
		{"kb", 1000},
		{"ki", 1024},
		{"k", 1024},
		{"b", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.mult
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", orig, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%q must be positive", orig)
	}
	if n > (1<<63-1)/mult {
		return 0, fmt.Errorf("%q is too large", orig)
	}
	return n * mult, nil
}

func formatSize(n int64) string {
	const gib = 1024 * 1024 * 1024
	if n%gib == 0 {
		return fmt.Sprintf("%dGiB", n/gib)
	}
	return fmt.Sprintf("%d bytes", n)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return defaultName
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func shellPlain(s string) string {
	return strings.NewReplacer("\n", "", "\r", "", "'", "", "\"", "").Replace(s)
}

func resetDestroyArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			out = append(out, arg)
		case arg == "--name" || arg == "--state-dir":
			out = append(out, arg)
			if i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		case strings.HasPrefix(arg, "--name=") || strings.HasPrefix(arg, "--state-dir="):
			out = append(out, arg)
		}
	}
	return out
}

func isMissingStateErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "marker "+markerName+" missing")
}

func randomUUIDLike() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := time.Now().UnixNano()
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", now&0xffffffffffff)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

func isTmpfs(path string) bool {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	path = filepath.Clean(path)
	bestLen := -1
	bestType := ""
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mount := strings.ReplaceAll(fields[1], `\040`, " ")
		if path == mount || strings.HasPrefix(path, mount+"/") {
			if len(mount) > bestLen {
				bestLen = len(mount)
				bestType = fields[2]
			}
		}
	}
	return bestType == "tmpfs"
}
