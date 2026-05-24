package adm

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	stateVersion = 3
	markerName   = ".cephplayground"
	defaultName  = "rgw"
	defaultImage = "quay.io/ceph/ceph:v19"

	serviceRGW    = "rgw"
	serviceCephFS = "cephfs"
	serviceRBD    = "rbd"

	cephFSName        = "playfs"
	cephFSClientName  = "cephplay-fs"
	rbdPoolName       = "rbd"
	rbdSampleImage    = "play"
	rbdClientName     = "cephplay-rbd"
	cephConfFile      = "ceph.conf"
	adminKeyringFile = "ceph.client.admin.keyring"
	cephFSKeyringFile = "ceph.client.cephplay-fs.keyring"
	rbdKeyringFile    = "ceph.client.cephplay-rbd.keyring"
)

// Exported service identifiers.
const (
	ServiceRGW    = serviceRGW
	ServiceCephFS = serviceCephFS
	ServiceRBD    = serviceRBD
)

// AppName is the binary / project name.
const AppName = appName

// DefaultName is the default playground name.
const DefaultName = defaultName

var defaultServices = []string{serviceRGW, serviceCephFS, serviceRBD}

// DefaultServices returns a fresh copy of the default service list.
func DefaultServices() []string {
	svcs := make([]string, len(defaultServices))
	copy(svcs, defaultServices)
	return svcs
}

// Version is set via -ldflags at build time. Defaults to "dev" for local builds.
var Version = "dev"

var cephCodenames = map[string]string{
	"pacific":  "v16",
	"quincy":   "v17",
	"reef":     "v18",
	"squid":    "v19",
	"tentacle": "v20",
}

func resolveCephImage(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if tag, ok := cephCodenames[v]; ok {
		v = tag
	}
	return "quay.io/ceph/ceph:" + v
}

// ResolveCephImage maps a tag or codename to a full quay.io image.
func ResolveCephImage(value string) string { return resolveCephImage(value) }

//go:embed assets/*
var assets embed.FS

// Config is the launch-time configuration. Callers populate it from CLI flags.
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
	Services        []string
	Privileged      bool
	DryRun          bool
}

// State is the persisted record of a running playground.
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
	Services        []string  `json:"services"`
	HostNetwork     bool      `json:"host_network"`
	FSID            string    `json:"fsid,omitempty"`
	MonHost         string    `json:"mon_host,omitempty"`
	CephConf        string    `json:"ceph_conf,omitempty"`
	AdminKeyring    string    `json:"admin_keyring,omitempty"`
	CephFSName      string    `json:"cephfs_name,omitempty"`
	CephFSClient    string    `json:"cephfs_client,omitempty"`
	CephFSKeyring   string    `json:"cephfs_keyring,omitempty"`
	RBDPool         string    `json:"rbd_pool,omitempty"`
	RBDImage        string    `json:"rbd_image,omitempty"`
	RBDClient       string    `json:"rbd_client,omitempty"`
	RBDKeyring      string    `json:"rbd_keyring,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// HasService reports whether name is in the state's service list.
func (s State) HasService(name string) bool {
	for _, svc := range s.Services {
		if svc == name {
			return true
		}
	}
	return false
}

// Runner wraps exec.Command for dry-run support.
type Runner struct {
	Out    io.Writer
	Err    io.Writer
	DryRun bool
}

// DefaultConfig returns a fully-populated Config with safe defaults.
func DefaultConfig(name string) Config { return defaultConfig(name) }

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
		Services:        DefaultServices(),
	}
}

// NormalizeConfig fixes up derived fields after flag parsing.
func NormalizeConfig(cfg *Config) { normalizeConfig(cfg) }

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

// Launch creates and starts the playground. It blocks until the cluster is
// ready (or returns an error / dry-run completes).
func Launch(out, errW io.Writer, cfg Config) (State, error) {
	normalizeConfig(&cfg)
	if cfg.RGWPort <= 0 || cfg.RGWPort > 65535 {
		return State{}, fmt.Errorf("invalid --rgw-port %d", cfg.RGWPort)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.S3UID == "" {
		return State{}, errors.New("access key, secret key, and uid must be non-empty")
	}
	if cfg.Region == "" {
		return State{}, errors.New("region must be non-empty")
	}
	rt, err := resolveRuntime(cfg.Runtime)
	if err != nil {
		return State{}, err
	}
	cfg.Runtime = rt
	if !cfg.DryRun && os.Geteuid() != 0 {
		return State{}, errors.New("launch must run as root because it creates a loop device")
	}
	r := &Runner{Out: out, Err: errW, DryRun: cfg.DryRun}
	if err := ensureStateDir(cfg.StateDir, cfg.DryRun); err != nil {
		return State{}, err
	}
	if err := writeMarker(cfg.StateDir, cfg.DryRun); err != nil {
		return State{}, err
	}
	if !cfg.DryRun && exists(filepath.Join(cfg.StateDir, "state.json")) {
		return State{}, fmt.Errorf("%s already has playground state; use status, reset, or destroy", cfg.StateDir)
	}
	if err := installContainerAssets(cfg.StateDir, cfg.DryRun); err != nil {
		return State{}, err
	}
	fsid := randomUUIDLike()
	hostNetwork := needsHostNetwork(cfg.Services)
	if err := writeGuestConfig(cfg, fsid, hostNetwork, cfg.DryRun); err != nil {
		return State{}, err
	}
	if err := ensureOSDImage(cfg.OSDImage, cfg.OSDSizeBytes, cfg.DryRun); err != nil {
		return State{}, err
	}

	loop, err := attachLoop(context.Background(), r, cfg.OSDImage)
	if err != nil {
		return State{}, err
	}

	st := newState(cfg, fsid, hostNetwork, loop)
	if err := saveState(cfg.StateDir, st, cfg.DryRun); err != nil {
		_ = r.Run(context.Background(), "losetup", "-d", loop)
		return State{}, err
	}
	if err := runContainer(context.Background(), r, cfg, loop); err != nil {
		_ = r.Run(context.Background(), "losetup", "-d", loop)
		if !cfg.DryRun {
			_ = os.Remove(filepath.Join(cfg.StateDir, "state.json"))
		}
		return State{}, err
	}
	if cfg.DryRun {
		return st, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	probe := ""
	if st.HasService(serviceRGW) {
		probe = st.Endpoint
	}
	if err := waitForReady(ctx, filepath.Join(cfg.StateDir, "ready"), probe); err != nil {
		return st, fmt.Errorf("%w; inspect with `%s logs --name %s` or clean up with `%s destroy --name %s`", err, appName, cfg.Name, appName, cfg.Name)
	}
	return st, nil
}

// Destroy stops the container, detaches the loop device, and removes the
// state directory (subject to the marker check).
func Destroy(out, errW io.Writer, cfg Config, dryRun bool) error {
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
		if rt, err := resolveRuntime(cfg.Runtime); err == nil {
			st.Runtime = rt
		}
	}
	if st.OSDImage == "" {
		st.OSDImage = cfg.OSDImage
	}
	r := &Runner{Out: out, Err: errW, DryRun: dryRun}
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
		return nil
	}
	return os.RemoveAll(cfg.StateDir)
}

// LoadState loads the persisted state for the playground at cfg's state dir.
func LoadState(cfg Config) (State, error) {
	normalizeConfig(&cfg)
	return loadState(cfg.StateDir)
}

// ProbeRGW makes a short HTTP GET against the endpoint and returns the status.
func ProbeRGW(endpoint string) (string, error) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return resp.Status, nil
}

// PrintEnv writes the shell export block for the given state.
func PrintEnv(out io.Writer, st State) { printEnv(out, st) }

// ContainerShell returns an *exec.Cmd that opens a bash inside the container.
func ContainerShell(st State) *exec.Cmd {
	return exec.Command(st.Runtime, "exec", "-it", st.ContainerName, "bash")
}

// ContainerLogs returns an *exec.Cmd that prints the container's logs.
func ContainerLogs(st State, follow bool) *exec.Cmd {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, st.ContainerName)
	return exec.Command(st.Runtime, args...)
}

// DoctorCheck is a single host-prerequisite check result.
type DoctorCheck struct {
	Name   string
	Status string // "ok" | "missing" | "warn"
	Detail string
}

// Doctor runs host prerequisite checks.
func Doctor() []DoctorCheck {
	out := []DoctorCheck{
		{Name: "os", Status: "ok", Detail: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)},
		{Name: "uid", Status: "ok", Detail: fmt.Sprintf("%d", os.Geteuid())},
	}
	if path, err := exec.LookPath("losetup"); err == nil {
		out = append(out, DoctorCheck{Name: "losetup", Status: "ok", Detail: path})
	} else {
		out = append(out, DoctorCheck{Name: "losetup", Status: "missing", Detail: "required"})
	}
	if path, err := exec.LookPath("docker"); err == nil {
		out = append(out, DoctorCheck{Name: "docker", Status: "ok", Detail: path})
	} else {
		out = append(out, DoctorCheck{Name: "docker", Status: "warn", Detail: "needed unless podman is available"})
	}
	if path, err := exec.LookPath("podman"); err == nil {
		out = append(out, DoctorCheck{Name: "podman", Status: "ok", Detail: path})
	} else {
		out = append(out, DoctorCheck{Name: "podman", Status: "warn", Detail: "needed unless docker is available"})
	}
	tmp := os.TempDir()
	if isTmpfs(tmp) {
		out = append(out, DoctorCheck{Name: "tmpfs", Status: "ok", Detail: tmp})
	} else {
		out = append(out, DoctorCheck{Name: "tmpfs", Status: "warn", Detail: fmt.Sprintf("%s is not tmpfs; use --state-dir on tmpfs if SSD avoidance matters", tmp)})
	}
	return out
}

// ParseServices validates and normalizes a comma-separated services list.
func ParseServices(raw string) ([]string, error) { return parseServices(raw) }

// ParseSize parses sizes like "16GiB", "1MB", "1024".
func ParseSize(s string) (int64, error) { return parseSize(s) }

// FormatSize is the inverse of ParseSize for round GiB sizes.
func FormatSize(n int64) string { return formatSize(n) }

// IsMissingStateErr reports whether the error is the "no marker" case from Destroy.
func IsMissingStateErr(err error) bool { return isMissingStateErr(err) }

func newState(cfg Config, fsid string, hostNetwork bool, loop string) State {
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
		Services:        cfg.Services,
		HostNetwork:     hostNetwork,
		FSID:            fsid,
		MonHost:         "127.0.0.1",
		CephConf:        filepath.Join(cfg.StateDir, cephConfFile),
		AdminKeyring:    filepath.Join(cfg.StateDir, adminKeyringFile),
		CreatedAt:       time.Now().UTC(),
	}
	if containsService(cfg.Services, serviceCephFS) {
		st.CephFSName = cephFSName
		st.CephFSClient = cephFSClientName
		st.CephFSKeyring = filepath.Join(cfg.StateDir, cephFSKeyringFile)
	}
	if containsService(cfg.Services, serviceRBD) {
		st.RBDPool = rbdPoolName
		st.RBDImage = rbdSampleImage
		st.RBDClient = rbdClientName
		st.RBDKeyring = filepath.Join(cfg.StateDir, rbdKeyringFile)
	}
	return st
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

func writeGuestConfig(cfg Config, fsid string, hostNetwork bool, dryRun bool) error {
	if dryRun {
		return nil
	}
	rgwBindAddr := "0.0.0.0"
	rgwBindPort := 7480
	if hostNetwork {
		rgwBindAddr = "127.0.0.1"
		rgwBindPort = cfg.RGWPort
	}
	hostNet := 0
	if hostNetwork {
		hostNet = 1
	}
	env := fmt.Sprintf(`FSID=%s
OSD_ID=0
OSD_DEVICE=/dev/cephplay-osd0
OSD_MEMORY_TARGET=%d
RGW_PORT=%d
RGW_BIND_ADDR=%s
RGW_BIND_PORT=%d
HOST_NETWORK=%d
SERVICES=%s
CEPHFS_NAME=%s
CEPHFS_CLIENT=%s
CEPHFS_KEYRING_FILE=%s
RBD_POOL=%s
RBD_IMAGE=%s
RBD_CLIENT=%s
RBD_KEYRING_FILE=%s
CEPH_CONF_FILE=%s
ADMIN_KEYRING_FILE=%s
S3_REGION=%s
S3_UID=%s
AWS_ACCESS_KEY_ID=%s
AWS_SECRET_ACCESS_KEY=%s
`,
		shellPlain(fsid),
		cfg.OSDMemoryTarget,
		cfg.RGWPort,
		rgwBindAddr,
		rgwBindPort,
		hostNet,
		shellQuote(strings.Join(cfg.Services, ",")),
		shellQuote(cephFSName),
		shellQuote(cephFSClientName),
		shellQuote(cephFSKeyringFile),
		shellQuote(rbdPoolName),
		shellQuote(rbdSampleImage),
		shellQuote(rbdClientName),
		shellQuote(rbdKeyringFile),
		shellQuote(cephConfFile),
		shellQuote(adminKeyringFile),
		shellQuote(cfg.Region),
		shellQuote(cfg.S3UID),
		shellQuote(cfg.AccessKey),
		shellQuote(cfg.SecretKey),
	)
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
	}
	if needsHostNetwork(cfg.Services) {
		args = append(args, "--network", "host")
	} else if containsService(cfg.Services, serviceRGW) {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:7480", cfg.RGWPort))
	}
	args = append(args,
		"--device", loop+":/dev/cephplay-osd0",
		"--cap-add", "SYS_ADMIN",
		"--tmpfs", "/etc/ceph:rw,size=64m,mode=755",
		"--tmpfs", "/run/ceph:rw,size=64m,mode=755",
		"--tmpfs", "/var/lib/ceph:rw,size=2g,mode=755",
		"--tmpfs", "/var/log/ceph:rw,size=256m,mode=755",
		"--tmpfs", "/tmp:rw,size=1g,mode=1777",
		"-v", cfg.StateDir+":/cephplay",
	)
	if cfg.Privileged {
		args = append(args, "--privileged")
	}
	args = append(args, cfg.Image, "/cephplay/container-entrypoint.sh")
	return r.Run(ctx, cfg.Runtime, args...)
}

func needsHostNetwork(services []string) bool {
	return containsService(services, serviceCephFS) || containsService(services, serviceRBD)
}

func containsService(services []string, name string) bool {
	for _, s := range services {
		if s == name {
			return true
		}
	}
	return false
}

func parseServices(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		s := strings.ToLower(strings.TrimSpace(p))
		if s == "" {
			continue
		}
		switch s {
		case serviceRGW, serviceCephFS, serviceRBD:
		default:
			return nil, fmt.Errorf("unknown service %q (allowed: rgw, cephfs, rbd)", s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("--services must list at least one of rgw, cephfs, rbd")
	}
	return out, nil
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
	if st.HasService(serviceRGW) {
		fmt.Fprintf(out, "# RGW (S3); drop-in for AWS SDKs\n")
		fmt.Fprintf(out, "export AWS_ACCESS_KEY_ID=%s\n", shellQuote(st.AccessKey))
		fmt.Fprintf(out, "export AWS_SECRET_ACCESS_KEY=%s\n", shellQuote(st.SecretKey))
		fmt.Fprintf(out, "export AWS_REGION=%s\n", shellQuote(st.Region))
		fmt.Fprintf(out, "export AWS_DEFAULT_REGION=%s\n", shellQuote(st.Region))
		fmt.Fprintf(out, "export AWS_ENDPOINT_URL=%s\n", shellQuote(st.Endpoint))
		fmt.Fprintf(out, "export CEPHPLAY_ENDPOINT=%s\n", shellQuote(st.Endpoint))
		fmt.Fprintf(out, "export CEPHPLAY_REGION=%s\n", shellQuote(st.Region))
	}
	if st.HasService(serviceCephFS) || st.HasService(serviceRBD) {
		fmt.Fprintf(out, "\n# Ceph cluster; point ceph-common at these\n")
		fmt.Fprintf(out, "export CEPH_CONF=%s\n", shellQuote(st.CephConf))
		fmt.Fprintf(out, "export CEPHPLAY_FSID=%s\n", shellQuote(st.FSID))
		fmt.Fprintf(out, "export CEPHPLAY_MON_HOST=%s\n", shellQuote(st.MonHost))
		fmt.Fprintf(out, "export CEPHPLAY_ADMIN_KEYRING=%s\n", shellQuote(st.AdminKeyring))
	}
	if st.HasService(serviceCephFS) {
		fmt.Fprintf(out, "\n# CephFS; mount with ceph-fuse or mount.ceph\n")
		fmt.Fprintf(out, "export CEPHPLAY_CEPHFS_NAME=%s\n", shellQuote(st.CephFSName))
		fmt.Fprintf(out, "export CEPHPLAY_CEPHFS_CLIENT=%s\n", shellQuote(st.CephFSClient))
		fmt.Fprintf(out, "export CEPHPLAY_CEPHFS_KEYRING=%s\n", shellQuote(st.CephFSKeyring))
		fmt.Fprintf(out, "# example:\n")
		fmt.Fprintf(out, "#   sudo ceph-fuse --id %s --conf %s -k %s -r / /mnt/playfs\n", st.CephFSClient, st.CephConf, st.CephFSKeyring)
	}
	if st.HasService(serviceRBD) {
		fmt.Fprintf(out, "\n# RBD; map with rbd-nbd or rbd map\n")
		fmt.Fprintf(out, "export CEPHPLAY_RBD_POOL=%s\n", shellQuote(st.RBDPool))
		fmt.Fprintf(out, "export CEPHPLAY_RBD_IMAGE=%s\n", shellQuote(st.RBDImage))
		fmt.Fprintf(out, "export CEPHPLAY_RBD_CLIENT=%s\n", shellQuote(st.RBDClient))
		fmt.Fprintf(out, "export CEPHPLAY_RBD_KEYRING=%s\n", shellQuote(st.RBDKeyring))
		fmt.Fprintf(out, "# example:\n")
		fmt.Fprintf(out, "#   sudo rbd-nbd map --id %s --conf %s -k %s %s/%s\n", st.RBDClient, st.CephConf, st.RBDKeyring, st.RBDPool, st.RBDImage)
	}
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
			if endpoint == "" {
				return nil
			}
			resp, err := client.Get(endpoint)
			if err == nil {
				_ = resp.Body.Close()
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for cluster")
		case <-tick.C:
		}
	}
}

// Run executes the command, honoring dry-run mode. Stdout is discarded so
// noisy command output (docker container IDs, losetup details) does not leak
// into pretty CLI output; stderr is kept for diagnostics.
func (r *Runner) Run(ctx context.Context, name string, args ...string) error {
	if r.DryRun {
		fmt.Fprintf(r.Out, "would run: %s %s\n", name, strings.Join(args, " "))
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = r.Err
	return cmd.Run()
}

// Output captures stdout from the command.
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
