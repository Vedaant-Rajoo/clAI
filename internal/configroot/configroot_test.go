package configroot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMatrix(t *testing.T) {
	base := t.TempDir()
	xdg := filepath.Join(base, "xdg")
	home := filepath.Join(base, "home")
	platform := filepath.Join(base, "platform")
	separator := string(os.PathSeparator)

	cases := []struct {
		name           string
		goos           string
		xdg            string
		home           string
		platformConfig string
		want           Roots
		wantErr        bool
	}{
		{
			name: "darwin absolute xdg wins",
			goos: "darwin", xdg: xdg, home: home,
			want: Roots{Preferred: xdg, Legacy: filepath.Join(home, "Library", "Application Support")},
		},
		{
			name: "darwin empty xdg falls back to home",
			goos: "darwin", home: home,
			want: Roots{Preferred: filepath.Join(home, ".config"), Legacy: filepath.Join(home, "Library", "Application Support")},
		},
		{
			name: "linux absolute xdg wins without home",
			goos: "linux", xdg: xdg,
			want: Roots{Preferred: xdg},
		},
		{
			name: "linux empty xdg falls back to home",
			goos: "linux", home: home,
			want: Roots{Preferred: filepath.Join(home, ".config")},
		},
		{
			name: "windows preserves platform config and ignores xdg home",
			goos: "windows", xdg: "relative-xdg", home: "relative-home", platformConfig: platform,
			want: Roots{Preferred: platform},
		},
		{
			name: "other platforms preserve platform config",
			goos: "freebsd", platformConfig: platform,
			want: Roots{Preferred: platform},
		},
		{name: "relative xdg is rejected without fallback", goos: "linux", xdg: "relative", home: home, wantErr: true},
		{name: "dot xdg is rejected", goos: "linux", xdg: xdg + separator + ".." + separator + "escape", home: home, wantErr: true},
		{name: "relative fallback home is rejected", goos: "linux", home: "relative", wantErr: true},
		{name: "dot fallback home is rejected", goos: "linux", home: home + separator + "." + separator + "child", wantErr: true},
		{name: "missing fallback home is rejected", goos: "linux", wantErr: true},
		{name: "darwin requires home for legacy root", goos: "darwin", xdg: xdg, wantErr: true},
		{name: "missing platform config is rejected", goos: "windows", wantErr: true},
		{name: "relative platform config is rejected", goos: "windows", platformConfig: "relative", wantErr: true},
		{name: "dot platform config is rejected", goos: "windows", platformConfig: platform + separator + ".." + separator + "escape", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(tc.goos, tc.xdg, tc.home, tc.platformConfig)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolve(%q, %q, %q, %q) = %+v, nil; want error", tc.goos, tc.xdg, tc.home, tc.platformConfig, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%q, %q, %q, %q): %v", tc.goos, tc.xdg, tc.home, tc.platformConfig, err)
			}
			if got != tc.want {
				t.Fatalf("resolve(%q, %q, %q, %q) = %+v, want %+v", tc.goos, tc.xdg, tc.home, tc.platformConfig, got, tc.want)
			}
		})
	}

	roots := Roots{Preferred: xdg, Legacy: filepath.Join(home, "legacy")}
	if got, want := roots.PreferredPath("config.json"), filepath.Join(xdg, "clai", "config.json"); got != want {
		t.Errorf("PreferredPath = %q, want %q", got, want)
	}
	if got, want := roots.LegacyPath("credentials.json"), filepath.Join(home, "legacy", "clai", "credentials.json"); got != want {
		t.Errorf("LegacyPath = %q, want %q", got, want)
	}
	if got := (Roots{Preferred: xdg}).LegacyPath("config.json"); got != "" {
		t.Errorf("empty LegacyPath = %q, want empty", got)
	}
}

func TestResolveInputErrors(t *testing.T) {
	oldGOOS := currentGOOS
	oldLookupEnv := lookupEnv
	oldUserHomeDir := userHomeDir
	oldUserConfigDir := userConfigDir
	t.Cleanup(func() {
		currentGOOS = oldGOOS
		lookupEnv = oldLookupEnv
		userHomeDir = oldUserHomeDir
		userConfigDir = oldUserConfigDir
	})

	sentinel := errors.New("resolver failed")
	currentGOOS = "windows"
	lookupEnv = func(string) (string, bool) { return "ignored", true }
	userConfigDir = func() (string, error) { return "", sentinel }
	if _, err := Resolve(); !errors.Is(err, sentinel) {
		t.Fatalf("Resolve platform error = %v, want wrapped sentinel", err)
	}

	currentGOOS = "linux"
	lookupEnv = func(name string) (string, bool) {
		if name != "XDG_CONFIG_HOME" {
			t.Fatalf("lookup name = %q, want XDG_CONFIG_HOME", name)
		}
		return "", true
	}
	userHomeDir = func() (string, error) { return "", sentinel }
	if _, err := Resolve(); !errors.Is(err, sentinel) {
		t.Fatalf("Resolve home error = %v, want wrapped sentinel", err)
	}
}

func TestResolveLinuxXDGDoesNotRequireHome(t *testing.T) {
	oldGOOS := currentGOOS
	oldLookupEnv := lookupEnv
	oldUserHomeDir := userHomeDir
	t.Cleanup(func() {
		currentGOOS = oldGOOS
		lookupEnv = oldLookupEnv
		userHomeDir = oldUserHomeDir
	})

	xdg := filepath.Join(t.TempDir(), "xdg")
	currentGOOS = "linux"
	lookupEnv = func(string) (string, bool) { return xdg, true }
	userHomeDir = func() (string, error) { return "", errors.New("home must not be consulted") }

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve with absolute Linux XDG: %v", err)
	}
	if got != (Roots{Preferred: xdg}) {
		t.Fatalf("Resolve = %+v, want preferred %q", got, xdg)
	}
}
