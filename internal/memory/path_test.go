package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProjectPathHyphenatedNames(t *testing.T) {
	root := t.TempDir()
	// Mirror the real shape: a hyphenated user dir, a dotted dir, a hyphenated repo.
	deep := filepath.Join(root, "home", "jay-den", ".epic", "Fabbed-EpicAssetManager")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// Encoding maps both "/" and "-" (and "." here) to "-", exactly as Claude Code does.
	encoded := "-" + filepath.ToSlash(deep[1:])
	encoded = replaceAll(encoded, "/", "-")
	encoded = replaceAll(encoded, ".", "-")

	got := ResolveProjectPath(encoded)
	if got != deep {
		t.Errorf("ResolveProjectPath(%q)\n got  %q\n want %q", encoded, got, deep)
	}
}

func TestResolveProjectPathFallsBack(t *testing.T) {
	enc := "-nonexistent-path-that-cannot-resolve"
	if got, want := ResolveProjectPath(enc), DecodePath(enc); got != want {
		t.Errorf("unresolvable input should fall back to DecodePath: got %q, want %q", got, want)
	}
}

func replaceAll(s, old, new string) string {
	out := ""
	for _, r := range s {
		if string(r) == old {
			out += new
			continue
		}
		out += string(r)
	}
	return out
}
