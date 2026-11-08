package cmd

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/version"
)

const (
	releaseRepo         = "Searchie-Inc/mio-cli"
	releaseBinary       = "mio"
	nativeUpdateTimeout = 10 * time.Minute
	maxChecksumsSize    = 1 << 20
)

var (
	githubAPIBaseURL      = "https://api.github.com"
	githubDownloadBaseURL = "https://github.com"
)

func useNativeUpdater(goos string) bool { return goos == "windows" }

type nativeUpdateConfig struct {
	Prefix     string
	Version    string
	GOOS       string
	GOARCH     string
	HTTPClient *http.Client
	Out        io.Writer
}

func runNativeUpdate(ctx context.Context, cfg nativeUpdateConfig) error {
	out := cfg.Out
	if out == nil {
		out = io.Discard
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: nativeUpdateTimeout}
	}

	if cfg.GOOS == "windows" && cfg.GOARCH != "amd64" {
		return errs.New(errs.ExitUsage,
			"no published mio release for %s/%s — install the amd64 build or build from source", cfg.GOOS, cfg.GOARCH)
	}

	destDir := cfg.Prefix
	if fi, err := os.Stat(destDir); err != nil {
		return errs.New(errs.ExitUsage, "install directory %s is not usable: %v", destDir, err)
	} else if !fi.IsDir() {
		return errs.New(errs.ExitUsage, "install directory %s is not a directory", destDir)
	}
	dest := filepath.Join(destDir, installedBinaryName(cfg.GOOS))
	if err := checkInstallTarget(dest); err != nil {
		return err
	}

	rel := normalizeReleaseVersion(cfg.Version)
	if rel != "" {
		if err := validateReleaseVersion(rel); err != nil {
			return err
		}
	}

	if rel == "" {
		nativeInfof(out, "Fetching latest release version...")
		resolved, err := latestReleaseVersion(ctx, httpClient)
		if err != nil {
			return err
		}
		if err := validateReleaseVersion(resolved); err != nil {
			return errs.New(errs.ExitGeneric, "the latest release tag from GitHub is unusable: %v", err)
		}
		rel = resolved
	}

	asset := releaseAssetName(rel, cfg.GOOS, cfg.GOARCH)
	nativeInfof(out, "Installing mio v%s (%s/%s) → %s", rel, cfg.GOOS, cfg.GOARCH, dest)

	tmpDir, err := os.MkdirTemp("", "mio-update-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, asset)
	nativeInfof(out, "Downloading %s...", asset)
	sum, err := downloadReleaseFile(ctx, httpClient, releaseAssetURL(rel, asset), archivePath)
	if err != nil {
		var se *httpStatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return fmt.Errorf("release asset %s not found — is v%s a published mio release?", asset, rel)
		}
		return err
	}

	nativeInfof(out, "Verifying checksum...")
	checksums, err := downloadReleaseBytes(ctx, httpClient, releaseAssetURL(rel, "checksums.txt"))
	if err != nil {
		return fmt.Errorf("fetch checksums.txt for v%s: %w", rel, err)
	}
	expected, err := checksumForAsset(checksums, asset)
	if err != nil {
		return err
	}
	if !strings.EqualFold(expected, sum) {
		return fmt.Errorf("checksum mismatch for %s — expected %s, got %s; refusing to install", asset, expected, sum)
	}
	nativeInfof(out, "Checksum verified.")

	// vendor: on Windows os.Rename (MoveFileEx without MOVEFILE_COPY_ALLOWED) fails across volumes, so stage in the install dir https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw
	stagedFile, err := os.CreateTemp(destDir, installedBinaryName(cfg.GOOS)+".new-*")
	if err != nil {
		return fmt.Errorf("install directory %s is not writable: %w", destDir, err)
	}
	stagedPath := stagedFile.Name()
	stagedFile.Close()
	defer func() { _ = os.Remove(stagedPath) }()

	nativeInfof(out, "Extracting...")
	if err := extractBinaryFromZip(archivePath, cfg.GOOS, stagedPath); err != nil {
		return err
	}
	if err := os.Chmod(stagedPath, 0o755); err != nil {
		return fmt.Errorf("make %s executable: %w", stagedPath, err)
	}

	if err := installStagedBinary(stagedPath, dest, out); err != nil {
		return err
	}
	nativeInfof(out, "Installed: %s", dest)
	return nil
}

// vendor: Windows cannot overwrite or delete a running .exe but allows renaming it https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw
func installStagedBinary(staged, dest string, out io.Writer) error {
	if err := checkInstallTarget(dest); err != nil {
		return err
	}

	backup := dest + ".old"
	_ = os.Remove(backup)

	existed := true
	if _, err := os.Lstat(dest); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", dest, err)
		}
		existed = false
	}

	if existed {
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("move the current binary aside (%s → %s): %w", dest, backup, err)
		}
	}

	if err := os.Rename(staged, dest); err != nil {
		if !existed {
			return fmt.Errorf("install the new binary at %s: %w", dest, err)
		}
		if rbErr := os.Rename(backup, dest); rbErr != nil {
			return fmt.Errorf("install the new binary at %s: %w — and restoring the previous binary ALSO failed: %v; "+
				"mio is currently not installed at that path: move %s back to %s by hand", dest, err, rbErr, backup, dest)
		}
		return fmt.Errorf("install the new binary at %s: %w (the previous binary was restored; mio is unchanged)", dest, err)
	}

	if existed {
		if err := os.Remove(backup); err != nil {
			nativeInfof(out, "Previous binary kept at %s (it is still open — Windows cannot delete a running executable); it is removed automatically on the next mio run.", backup)
		}
	}
	return nil
}

func checkInstallTarget(dest string) error {
	fi, err := os.Lstat(dest)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", dest, err)
	}
	if !fi.Mode().IsRegular() {
		return errs.New(errs.ExitUsage,
			"%s exists but is not a regular file (%s) — refusing to replace it; "+
				"remove it or choose another directory with --prefix", dest, fi.Mode().Type())
	}
	return nil
}

func sweepSupersededBinary(goos, exePath string) {
	if goos != "windows" || exePath == "" {
		return
	}
	old := exePath + ".old"
	fi, err := os.Lstat(old)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	_ = os.Remove(old)
}

func currentExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func normalizeReleaseVersion(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		v = v[1:]
	}
	return strings.TrimSpace(v)
}

func validateReleaseVersion(v string) error {
	bad := func() error {
		return errs.New(errs.ExitUsage, "%q is not a valid release version (expected a tag like 0.12.1)", v)
	}
	if v == "" {
		return errs.New(errs.ExitUsage, "release version is empty")
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r == '.' || r == '-' || r == '_' || r == '+':
		default:
			return bad()
		}
	}
	if strings.Contains(v, "..") {
		return bad()
	}
	return nil
}

func releaseAssetName(v, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s.%s", releaseBinary, normalizeReleaseVersion(v), goos, goarch, ext)
}

func releaseAssetURL(v, asset string) string {
	return fmt.Sprintf("%s/%s/releases/download/v%s/%s",
		strings.TrimRight(githubDownloadBaseURL, "/"), releaseRepo, normalizeReleaseVersion(v), asset)
}

func installedBinaryName(goos string) string {
	if goos == "windows" {
		return releaseBinary + ".exe"
	}
	return releaseBinary
}

func latestReleaseVersion(ctx context.Context, c *http.Client) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(githubAPIBaseURL, "/"), releaseRepo)
	resp, err := releaseGet(ctx, c, url, "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("resolve the latest mio release: %w", err)
	}
	defer resp.Body.Close()

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxChecksumsSize)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode the GitHub release response: %w", err)
	}
	v := normalizeReleaseVersion(payload.TagName)
	if v == "" {
		return "", errors.New("could not determine the latest mio release version (no tag_name in the GitHub response)")
	}
	return v, nil
}

type httpStatusError struct {
	URL    string
	Status string
	Code   int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("GET %s: unexpected response %s", e.URL, e.Status)
}

func releaseGet(ctx context.Context, c *http.Client, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mio-cli/"+version.Version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &httpStatusError{URL: url, Status: resp.Status, Code: resp.StatusCode}
	}
	return resp, nil
}

func downloadReleaseFile(ctx context.Context, c *http.Client, url, dest string) (string, error) {
	resp, err := releaseGet(ctx, c, url, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	f, err := os.Create(dest)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", dest, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write %s: %w", dest, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func downloadReleaseBytes(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	resp, err := releaseGet(ctx, c, url, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, maxChecksumsSize))
}

func checksumForAsset(body []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		if strings.TrimPrefix(fields[len(fields)-1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read checksums.txt: %w", err)
	}
	return "", fmt.Errorf("no checksum entry for %s in checksums.txt; refusing to install an unverified binary", asset)
}

func extractBinaryFromZip(archivePath, goos, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(archivePath), err)
	}
	defer func() { _ = zr.Close() }()

	want := []string{installedBinaryName(goos), releaseBinary}
	for _, name := range want {
		for _, f := range zr.File {
			if f.Name != name || f.FileInfo().IsDir() {
				continue
			}
			if err := writeZipEntry(f, dest); err != nil {
				return fmt.Errorf("extract %s from %s: %w", name, filepath.Base(archivePath), err)
			}
			return nil
		}
	}
	return fmt.Errorf("release archive %s does not contain a %s binary", filepath.Base(archivePath), releaseBinary)
}

func writeZipEntry(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func nativeInfof(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "  => "+format+"\n", a...)
}
