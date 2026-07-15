package xdgpaths

import (
	"os"
	"strings"
	"testing"
)

func TestResolveCreatesOwnerOnlyDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	dirs, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{dirs.Config, dirs.Data} {
		if !strings.HasPrefix(dir, home) {
			t.Fatalf("got dir %q outside HOME %q", dir, home)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("got mode %v, want 0700", info.Mode().Perm())
		}
	}
}

func TestResolveHonorsXDGOverrides(t *testing.T) {
	home := t.TempDir()
	cfgOverride := t.TempDir()
	dataOverride := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfgOverride)
	t.Setenv("XDG_DATA_HOME", dataOverride)

	dirs, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dirs.Config, cfgOverride) {
		t.Fatalf("got %q, want under %q", dirs.Config, cfgOverride)
	}
	if !strings.HasPrefix(dirs.Data, dataOverride) {
		t.Fatalf("got %q, want under %q", dirs.Data, dataOverride)
	}
}
