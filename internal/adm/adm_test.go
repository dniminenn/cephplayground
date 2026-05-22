package adm

import (
	"reflect"
	"testing"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1", 1},
		{"1k", 1024},
		{"2MiB", 2 * 1024 * 1024},
		{"16GiB", 16 * 1024 * 1024 * 1024},
		{"3gb", 3 * 1000 * 1000 * 1000},
	}
	for _, tt := range tests {
		got, err := parseSize(tt.in)
		if err != nil {
			t.Fatalf("parseSize(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("parseSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	got := sanitizeName("rgw/dev one")
	want := "rgw-dev-one"
	if got != want {
		t.Fatalf("sanitizeName() = %q, want %q", got, want)
	}
}

func TestNormalizeConfigNameUpdatesDefaultStateDir(t *testing.T) {
	cfg := defaultConfig(defaultName)
	cfg.Name = "other"
	normalizeConfig(&cfg)

	wantSuffix := "/cephplayground/other"
	if len(cfg.StateDir) < len(wantSuffix) || cfg.StateDir[len(cfg.StateDir)-len(wantSuffix):] != wantSuffix {
		t.Fatalf("StateDir = %q, want suffix %q", cfg.StateDir, wantSuffix)
	}
	if cfg.ContainerName != "cephplay-other" {
		t.Fatalf("ContainerName = %q, want cephplay-other", cfg.ContainerName)
	}
}

func TestResetDestroyArgsKeepsOnlyDestroyFlags(t *testing.T) {
	got := resetDestroyArgs([]string{
		"--name", "dev",
		"--osd-size", "4GiB",
		"--state-dir=/tmp/custom",
		"--rgw-port", "9000",
		"--dry-run",
	})
	want := []string{"--name", "dev", "--state-dir=/tmp/custom", "--dry-run"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resetDestroyArgs() = %#v, want %#v", got, want)
	}
}
