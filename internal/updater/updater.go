package updater

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	repoOwner = "fabioconcina"
	repoName  = "claumon"
)

type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// CheckLatest fetches the latest release from GitHub and returns it.
func CheckLatest() (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("checking latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("parsing release: %w", err)
	}
	return &rel, nil
}

// ReleasesURL returns the GitHub "latest release" page for claumon.
func ReleasesURL() string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/latest", repoOwner, repoName)
}

// AssetName returns the expected binary name for the current platform.
func AssetName() string {
	name := fmt.Sprintf("%s-%s-%s", repoName, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// NeedsUpdate reports whether the latest release differs from what is running.
//
// A local build carries semver build metadata naming the fork and commit, e.g.
// "0.20.0+nirvana.3286ad6". Build metadata is explicitly outside version
// precedence in semver, and it is the whole point here: a fork sitting on
// upstream's current release is up to date, and saying otherwise puts a
// permanent "update available" badge on the dashboard that can never be
// cleared. Stripping it keeps a real upstream release detectable — once
// upstream ships 0.21.0, the 0.20.0 base no longer matches and the badge
// returns for a genuine reason.
func NeedsUpdate(current, latest string) bool {
	current = BaseVersion(current)
	latest = strings.TrimPrefix(latest, "v")
	return current != latest && current != "dev"
}

// BaseVersion strips a leading "v" and any semver build metadata, leaving the
// upstream release a build is based on.
func BaseVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	return v
}

// IsFork reports whether this build carries fork build metadata, meaning it was
// built from modified source rather than downloaded from upstream.
func IsFork(v string) bool {
	return strings.Contains(v, "+")
}

// Update downloads the given release and replaces the current binary.
// Returns the new version string.
func Update(rel *Release) (string, error) {
	asset := AssetName()
	var downloadURL string
	for _, a := range rel.Assets {
		if a.Name == asset {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return "", fmt.Errorf("no release asset found for %s", asset)
	}

	log.Printf("[update] Downloading %s...", asset)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("downloading release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("resolving symlinks: %w", err)
	}

	// Download to a temp file
	tmpFile, err := os.CreateTemp("", "claumon-update-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("writing binary: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("closing temp file: %w", err)
	}

	if err := verifyChecksum(rel, asset, tmpPath); err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("setting permissions: %w", err)
	}

	// Try direct rename (works if same filesystem and we have write permission)
	oldPath := execPath + ".old"
	os.Remove(oldPath)

	if err := os.Rename(execPath, oldPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("cannot replace binary at %s (permission denied)", execPath)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		// Rename across filesystems — fall back to copy
		if copyErr := copyFile(tmpPath, execPath); copyErr != nil {
			// Restore the old binary
			os.Rename(oldPath, execPath)
			os.Remove(tmpPath)
			return "", fmt.Errorf("installing new binary: %w", copyErr)
		}
		os.Remove(tmpPath)
	}

	// On macOS, remove quarantine/provenance attributes so Gatekeeper
	// doesn't kill the new binary.
	clearQuarantine(execPath)

	// Clean up old binary (best effort, may fail on Windows while running)
	os.Remove(oldPath)

	return rel.TagName, nil
}

// verifyChecksum downloads the checksums file from the release and verifies
// the SHA256 of the downloaded binary. Skips verification if no checksums
// asset is present in the release.
func verifyChecksum(rel *Release, asset, filePath string) error {
	var checksumURL string
	for _, a := range rel.Assets {
		if strings.Contains(a.Name, "checksums") {
			checksumURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumURL == "" {
		log.Printf("[update] No checksum file in release, skipping verification")
		return nil
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(checksumURL)
	if err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums download returned %d", resp.StatusCode)
	}

	var expected string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 2 && parts[1] == asset {
			expected = parts[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("no checksum found for %s in checksums file", asset)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("opening file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("computing checksum: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}

	log.Printf("[update] Checksum verified (%s)", expected[:12])
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
