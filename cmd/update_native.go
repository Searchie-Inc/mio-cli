package cmd

// update_native.go — Go-native self-update path (MIO-2688).
//
// `mio update` re-runs the POSIX installer:
//
//	sh -c "curl -fsSL .../scripts/install.sh | sh"
//
// which can never work on stock Windows. A Windows box has no `sh` (and
// usually no `curl`) on PATH, so the updater died before doing anything:
//
//	exec: "sh": executable file not found in %PATH%
//
// meaning the Windows build could not update itself at all — QA had to
// download the release zip and swap mio.exe by hand to move 0.10.0 → 0.11.0.
//
// This file re-implements what scripts/install.sh does, using only the Go
// stdlib (net/http, archive/zip, crypto/sha256, os), so the Windows path needs
// no Unix shell and no external download tool:
//
//	resolve version → download the release asset → verify its SHA-256 against
//	checksums.txt → extract mio.exe → install it (rename-then-replace)
//
// The Unix path is deliberately untouched: defaultSelfUpdateRunner still shells
// out to the install script everywhere except Windows, so the darwin Gatekeeper
// mitigation added in MIO-2603 keeps running exactly as before.
//
// NOTE ON TESTING: this file carries no `_windows.go` build tag and no
// runtime.GOOS reads below the dispatch helpers — the platform is passed in as
// data (nativeUpdateConfig.GOOS/GOARCH). That is what lets the Linux CI box
// exercise the whole Windows pipeline (asset naming, checksum verification,
// zip extraction, rename-then-replace) against an httptest server.

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

	"github.com/Searchie-Inc/mio-cli/internal/version"
)

const (
	// releaseRepo is the GitHub owner/repo the release assets are published to.
	releaseRepo = "Searchie-Inc/mio-cli"
	// releaseBinary is the base name goreleaser gives the built binary; the
	// archive members and the installed file derive from it.
	releaseBinary = "mio"
	// nativeUpdateTimeout caps one self-update end to end (release metadata +
	// archive + checksums). Generous: the archive is a few MB over links we do
	// not control.
	nativeUpdateTimeout = 10 * time.Minute
	// maxChecksumsSize bounds the checksums.txt read so a wrong URL cannot make
	// the CLI buffer something unbounded.
	maxChecksumsSize = 1 << 20 // 1 MiB
)

// Release endpoints. Package vars rather than consts so tests can point them at
// an httptest server: the real Windows download+swap cannot run on CI, so
// everything underneath it is covered against a local server instead.
var (
	githubAPIBaseURL      = "https://api.github.com"
	githubDownloadBaseURL = "https://github.com"
)

// useNativeUpdater reports whether goos must take the Go-native path instead of
// the POSIX install script. Windows only — it is the one supported platform
// with no `sh` to pipe the installer into (MIO-2688).
func useNativeUpdater(goos string) bool { return goos == "windows" }

// nativeUpdateConfig is everything the Go-native updater needs. GOOS/GOARCH are
// passed in (not read from runtime) so the Windows pipeline is testable on
// Linux; Version is empty for "latest".
type nativeUpdateConfig struct {
	Prefix     string // install directory (already resolved by the caller)
	Version    string // release to install; empty resolves the latest
	GOOS       string // target OS, e.g. "windows"
	GOARCH     string // target arch, e.g. "amd64"
	HTTPClient *http.Client
	Out        io.Writer
}

// runNativeUpdate downloads the release archive for cfg's platform, verifies it
// against the published checksums, and installs the binary into cfg.Prefix.
func runNativeUpdate(ctx context.Context, cfg nativeUpdateConfig) error {
	out := cfg.Out
	if out == nil {
		out = io.Discard
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: nativeUpdateTimeout}
	}

	// ── validate every local input BEFORE touching the network ───────────────
	//
	// Same rule the resource commands follow: a usage error must fire no
	// request. Resolving "latest" first meant a typo'd --prefix surfaced as a
	// DNS/connection error instead of "that directory does not exist" (Codex
	// review round 1).

	// goreleaser explicitly skips windows/arm64 (see .goreleaser.yaml `ignore`),
	// so there is no asset to fetch. Say that instead of surfacing a 404 from a
	// URL the user never typed.
	if cfg.GOOS == "windows" && cfg.GOARCH != "amd64" {
		return fmt.Errorf("no published mio release for %s/%s — install the amd64 build or build from source", cfg.GOOS, cfg.GOARCH)
	}

	destDir := cfg.Prefix
	if fi, err := os.Stat(destDir); err != nil {
		return fmt.Errorf("install directory %s is not usable: %w", destDir, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("install directory %s is not a directory", destDir)
	}
	dest := filepath.Join(destDir, installedBinaryName(cfg.GOOS))

	rel := normalizeReleaseVersion(cfg.Version)
	if rel != "" {
		if err := validateReleaseVersion(rel); err != nil {
			return err
		}
	}

	// ── resolve the release ──────────────────────────────────────────────────
	if rel == "" {
		nativeInfof(out, "Fetching latest release version...")
		resolved, err := latestReleaseVersion(ctx, httpClient)
		if err != nil {
			return err
		}
		// The tag came off the wire, so it gets the same treatment as --version
		// before it reaches a URL path and a local filename.
		if err := validateReleaseVersion(resolved); err != nil {
			return fmt.Errorf("the latest release tag from GitHub is unusable: %w", err)
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

	// ── download + verify ────────────────────────────────────────────────────
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
	// Fail closed. Unlike install.sh (which skips verification when no sha tool
	// exists) crypto/sha256 is always available here, so a mismatch is always a
	// real signal — and we are about to replace the binary the user runs.
	if !strings.EqualFold(expected, sum) {
		return fmt.Errorf("checksum mismatch for %s — expected %s, got %s; refusing to install", asset, expected, sum)
	}
	nativeInfof(out, "Checksum verified.")

	// ── extract ──────────────────────────────────────────────────────────────
	//
	// Stage the new binary INSIDE the install directory, not in the OS temp dir:
	// on Windows os.Rename is MoveFileEx without MOVEFILE_COPY_ALLOWED, which
	// fails across volumes — and the reported install dir (D:\MIO\bin) is
	// routinely on a different drive from %TEMP% (C:\...). Staging next to the
	// destination keeps the final install a same-volume rename, i.e. atomic and
	// cheap. It also fails fast, before anything is touched, when the install
	// directory is not writable.
	stagedFile, err := os.CreateTemp(destDir, installedBinaryName(cfg.GOOS)+".new-*")
	if err != nil {
		return fmt.Errorf("install directory %s is not writable: %w", destDir, err)
	}
	stagedPath := stagedFile.Name()
	stagedFile.Close()
	// No-op once the file has been renamed into place; cleans up on any failure.
	defer func() { _ = os.Remove(stagedPath) }()

	nativeInfof(out, "Extracting...")
	if err := extractBinaryFromZip(archivePath, cfg.GOOS, stagedPath); err != nil {
		return err
	}
	// os.CreateTemp made the staging file 0600 and writing through an existing
	// file does not change its mode, so set the exec bits explicitly. Inert on
	// Windows (executability comes from the .exe extension) but correct if this
	// path is ever reused elsewhere.
	if err := os.Chmod(stagedPath, 0o755); err != nil {
		return fmt.Errorf("make %s executable: %w", stagedPath, err)
	}

	// ── install ──────────────────────────────────────────────────────────────
	if err := installStagedBinary(stagedPath, dest, out); err != nil {
		return err
	}
	nativeInfof(out, "Installed: %s", dest)
	return nil
}

// installStagedBinary moves the freshly extracted binary at staged into place
// at dest.
//
// Windows will not let you overwrite (or delete) a running .exe — but it DOES
// allow renaming one, and the running process keeps executing happily from the
// renamed file. So the sequence is:
//
//  1. drop any <dest>.old left by a previous update (see sweepSupersededBinary),
//  2. rename the current binary aside to <dest>.old,
//  3. rename the staged binary into <dest>.
//
// If step 3 fails we roll step 2 back so the user is never left without a
// working mio. If even the rollback fails we say exactly which file to move
// where by hand — a half-installed CLI must not be reported as success.
func installStagedBinary(staged, dest string, out io.Writer) error {
	backup := dest + ".old"
	// Best-effort: a .old from an earlier update (held open by the then-running
	// mio) is dead weight now, and step 2 must not trip over it.
	_ = os.Remove(backup)

	existed := true
	fi, err := os.Lstat(dest)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", dest, err)
		}
		existed = false
	} else if !fi.Mode().IsRegular() {
		// Whatever is sitting there is not a mio binary — a directory, a
		// symlink, a device. Renaming it aside and dropping an executable in
		// its place would be a silent, destructive surprise (it would move a
		// whole directory to mio.exe.old and report success). Refuse instead
		// (Codex review round 1).
		return fmt.Errorf("%s exists but is not a regular file (%s) — refusing to replace it; "+
			"remove it or choose another directory with --prefix", dest, fi.Mode().Type())
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
		// Usually impossible on Windows: the mio.exe running this very update
		// still has the renamed file open, so the delete is denied. That is
		// expected — sweepSupersededBinary clears it on the next mio run.
		if err := os.Remove(backup); err != nil {
			nativeInfof(out, "Previous binary kept at %s (it is still open — Windows cannot delete a running executable); it is removed automatically on the next mio run.", backup)
		}
	}
	return nil
}

// sweepSupersededBinary removes the <exe>.old that a Windows self-update leaves
// behind (MIO-2688): Windows cannot delete the .exe that is currently running,
// so `mio update` renames it aside and the NEXT mio process is the first one
// able to clean it up. Best-effort and Windows-only — any error is ignored, the
// leftover file is inert either way. goos/exePath are parameters so this is
// testable off Windows.
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

// currentExecutablePath returns os.Executable() or "" when it cannot be
// resolved (sweepSupersededBinary then no-ops).
func currentExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// ── release metadata / naming ───────────────────────────────────────────────

// normalizeReleaseVersion trims whitespace and the tag's leading "v", so both
// "v0.12.1" (GitHub tag) and "0.12.1" (--version) resolve to the same string.
func normalizeReleaseVersion(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		v = v[1:]
	}
	return strings.TrimSpace(v)
}

// validateReleaseVersion rejects anything that is not a plain release tag.
//
// The version reaches both a URL path segment and a local filename
// (filepath.Join(tmpDir, asset)), so a value carrying a separator or a ".."
// could walk out of the temp dir — or bend the download URL to another path on
// github.com. Nothing in a goreleaser tag needs more than this alphabet, so
// this is a cheap fail-closed guard rather than a semver parser (Codex review
// round 1).
func validateReleaseVersion(v string) error {
	if v == "" {
		return errors.New("release version is empty")
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r == '.' || r == '-' || r == '_' || r == '+':
		default:
			return fmt.Errorf("%q is not a valid release version (expected a tag like 0.12.1)", v)
		}
	}
	if strings.Contains(v, "..") {
		return fmt.Errorf("%q is not a valid release version (expected a tag like 0.12.1)", v)
	}
	return nil
}

// releaseAssetName builds the goreleaser archive name for a version/platform:
// mio_0.12.1_windows_amd64.zip, mio_0.12.1_darwin_arm64.tar.gz. Must stay in
// sync with .goreleaser.yaml's archives.name_template and format_overrides (and
// with scripts/install.sh, which composes the same name).
func releaseAssetName(v, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s.%s", releaseBinary, normalizeReleaseVersion(v), goos, goarch, ext)
}

// releaseAssetURL is the browser-download URL for an asset of tag v<version>.
func releaseAssetURL(v, asset string) string {
	return fmt.Sprintf("%s/%s/releases/download/v%s/%s",
		strings.TrimRight(githubDownloadBaseURL, "/"), releaseRepo, normalizeReleaseVersion(v), asset)
}

// installedBinaryName is the on-disk name of the installed binary for goos.
func installedBinaryName(goos string) string {
	if goos == "windows" {
		return releaseBinary + ".exe"
	}
	return releaseBinary
}

// latestReleaseVersion resolves the newest published release via the GitHub API
// (the Go equivalent of install.sh's latest_version()).
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

// ── HTTP ────────────────────────────────────────────────────────────────────

// httpStatusError is a non-200 response, kept typed so callers can special-case
// a 404 (asset/tag does not exist) with a better message.
type httpStatusError struct {
	URL    string
	Status string
	Code   int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("GET %s: unexpected response %s", e.URL, e.Status)
}

// releaseGet performs the GET. GitHub rejects requests without a User-Agent, so
// one is always sent. Redirects (release download → object storage) are followed
// by the default http.Client policy.
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

// downloadReleaseFile streams url into dest and returns the hex SHA-256 of what
// was written, so the caller can verify without re-reading the file.
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

// downloadReleaseBytes fetches a small release asset (checksums.txt) into memory.
func downloadReleaseBytes(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	resp, err := releaseGet(ctx, c, url, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, maxChecksumsSize))
}

// checksumForAsset pulls the expected SHA-256 for asset out of a goreleaser
// checksums.txt, which is sha256sum format: "<hex>  <filename>" (a leading '*'
// on the name marks binary mode).
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

// ── archive ─────────────────────────────────────────────────────────────────

// extractBinaryFromZip writes the mio binary member of the release zip to dest
// (which the caller has already created inside the install directory).
//
// Only archive-root members named exactly "mio.exe" or "mio" are accepted — the
// entry name is never used to build a path, and anything carrying a directory
// component (the zip-slip shape, e.g. "../../mio.exe") simply does not match.
func extractBinaryFromZip(archivePath, goos, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(archivePath), err)
	}
	defer func() { _ = zr.Close() }()

	// Prefer the platform's name, but accept the bare one too — install.sh does
	// the same (unzip mio.exe || unzip mio).
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

// writeZipEntry copies one zip member onto dest, truncating it.
func writeZipEntry(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// 0o755: a no-op on Windows (which infers executability from .exe), correct
	// everywhere else.
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

// nativeInfof prints one progress line. Deliberately ANSI-free: install.sh's
// green "=>" is fine in a POSIX shell, but legacy Windows consoles with VT
// processing off render escape codes as literal garbage.
func nativeInfof(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "  => "+format+"\n", a...)
}
