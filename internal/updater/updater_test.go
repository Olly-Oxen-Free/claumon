package updater

import (
	"runtime"
	"testing"
)

func TestAssetName(t *testing.T) {
	name := AssetName()
	if name == "" {
		t.Fatal("AssetName returned empty string")
	}
	want := "claumon-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	if name != want {
		t.Errorf("AssetName() = %q, want %q", name, want)
	}
}

func TestNeedsUpdate(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"v0.7.1", "v0.7.1", false},
		{"v0.7.1", "v0.8.0", true},
		{"0.7.1", "v0.7.1", false},
		{"dev", "v0.8.0", false},
		{"v0.7.0", "v0.7.1", true},
	}
	for _, tt := range tests {
		got := NeedsUpdate(tt.current, tt.latest)
		if got != tt.want {
			t.Errorf("NeedsUpdate(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestForkBuildOnUpstreamsReleaseIsUpToDate(t *testing.T) {
	// The badge this prevents: a fork built from upstream's current release
	// otherwise reports an update forever, because its version string never
	// equals the tag.
	if NeedsUpdate("0.20.0+nirvana.3286ad6", "v0.20.0") {
		t.Fatal("a fork of the current release must not report an update")
	}
}

func TestForkStillSeesARealUpstreamRelease(t *testing.T) {
	if !NeedsUpdate("0.20.0+nirvana.3286ad6", "v0.21.0") {
		t.Fatal("a genuinely newer upstream release must still be reported")
	}
}

func TestBaseVersionStripsPrefixAndMetadata(t *testing.T) {
	for in, want := range map[string]string{
		"v0.20.0":                 "0.20.0",
		"0.20.0":                  "0.20.0",
		"0.20.0+nirvana.abc1234":  "0.20.0",
		"v0.20.0+nirvana.abc1234": "0.20.0",
		"dev":                     "dev",
	} {
		if got := BaseVersion(in); got != want {
			t.Errorf("BaseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsForkOnlyForBuildMetadata(t *testing.T) {
	if !IsFork("0.20.0+nirvana.abc1234") {
		t.Error("build metadata marks a fork")
	}
	if IsFork("0.20.0") || IsFork("dev") {
		t.Error("a plain version is not a fork")
	}
}

func TestDevBuildsNeverNag(t *testing.T) {
	if NeedsUpdate("dev", "v0.21.0") {
		t.Fatal("dev builds must not report updates")
	}
}
