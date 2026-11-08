package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestUseNativeUpdaterOnlyOnWindows(t *testing.T) {
	if !useNativeUpdater("windows") {
		t.Error("windows must use the native updater — it has no sh")
	}
	for _, goos := range []string{"darwin", "linux"} {
		if useNativeUpdater(goos) {
			t.Errorf("%s must keep the install-script path (MIO-2603 lives there)", goos)
		}
	}
}

func TestReleaseAssetName(t *testing.T) {
	cases := []struct {
		version, goos, goarch, want string
	}{
		{"0.12.1", "windows", "amd64", "mio_0.12.1_windows_amd64.zip"},
		{"v0.12.1", "windows", "amd64", "mio_0.12.1_windows_amd64.zip"},
		{"0.12.1", "darwin", "arm64", "mio_0.12.1_darwin_arm64.tar.gz"},
		{"0.12.1", "linux", "amd64", "mio_0.12.1_linux_amd64.tar.gz"},
	}
	for _, tc := range cases {
		if got := releaseAssetName(tc.version, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("releaseAssetName(%q, %q, %q) = %q, want %q", tc.version, tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestReleaseAssetURL(t *testing.T) {
	want := "https://github.com/Searchie-Inc/mio-cli/releases/download/v0.12.1/mio_0.12.1_windows_amd64.zip"
	if got := releaseAssetURL("v0.12.1", "mio_0.12.1_windows_amd64.zip"); got != want {
		t.Errorf("releaseAssetURL = %q, want %q", got, want)
	}
}

func TestInstalledBinaryName(t *testing.T) {
	if got := installedBinaryName("windows"); got != "mio.exe" {
		t.Errorf("windows binary name = %q, want mio.exe", got)
	}
	if got := installedBinaryName("linux"); got != "mio" {
		t.Errorf("linux binary name = %q, want mio", got)
	}
}

func TestChecksumForAsset(t *testing.T) {
	body := []byte(
		"aaa111  mio_0.12.1_darwin_arm64.tar.gz\n" +
			"bbb222  mio_0.12.1_windows_amd64.zip\n" +
			"CCC333 *mio_0.12.1_linux_amd64.tar.gz\n" +
			"\n")

	got, err := checksumForAsset(body, "mio_0.12.1_windows_amd64.zip")
	if err != nil {
		t.Fatalf("checksumForAsset: %v", err)
	}
	if got != "bbb222" {
		t.Errorf("checksum = %q, want bbb222", got)
	}

	got, err = checksumForAsset(body, "mio_0.12.1_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("checksumForAsset (binary mode): %v", err)
	}
	if got != "ccc333" {
		t.Errorf("checksum = %q, want ccc333", got)
	}

	if _, err := checksumForAsset(body, "mio_9.9.9_windows_amd64.zip"); err == nil {
		t.Error("a missing checksum entry must be an error — we never install an unverified binary")
	}
}

func TestExtractBinaryFromZip(t *testing.T) {
	dir := t.TempDir()

	t.Run("picks the platform binary out of the archive", func(t *testing.T) {
		archive := filepath.Join(dir, "ok.zip")
		writeTestZip(t, archive, map[string]string{
			"LICENSE":   "MIT",
			"README.md": "docs",
			"mio.exe":   "NEW-BINARY",
		})
		dest := filepath.Join(dir, "out.exe")
		if err := extractBinaryFromZip(archive, "windows", dest); err != nil {
			t.Fatalf("extractBinaryFromZip: %v", err)
		}
		if got := readFile(t, dest); got != "NEW-BINARY" {
			t.Errorf("extracted %q, want NEW-BINARY", got)
		}
	})

	t.Run("falls back to the extension-less member", func(t *testing.T) {
		archive := filepath.Join(dir, "bare.zip")
		writeTestZip(t, archive, map[string]string{"mio": "BARE"})
		dest := filepath.Join(dir, "bare-out.exe")
		if err := extractBinaryFromZip(archive, "windows", dest); err != nil {
			t.Fatalf("extractBinaryFromZip: %v", err)
		}
		if got := readFile(t, dest); got != "BARE" {
			t.Errorf("extracted %q, want BARE", got)
		}
	})

	t.Run("rejects an archive with no mio binary", func(t *testing.T) {
		archive := filepath.Join(dir, "empty.zip")
		writeTestZip(t, archive, map[string]string{"LICENSE": "MIT"})
		if err := extractBinaryFromZip(archive, "windows", filepath.Join(dir, "nope.exe")); err == nil {
			t.Error("expected an error for an archive with no mio binary")
		}
	})

	t.Run("ignores entries with a directory component (zip slip shape)", func(t *testing.T) {
		archive := filepath.Join(dir, "slip.zip")
		writeTestZip(t, archive, map[string]string{
			"../../mio.exe":   "EVIL",
			"nested/mio.exe":  "ALSO-EVIL",
			"mio.exe.backup":  "NOT-IT",
			"decoy/README.md": "x",
		})
		if err := extractBinaryFromZip(archive, "windows", filepath.Join(dir, "slip-out.exe")); err == nil {
			t.Error("only archive-root mio/mio.exe members may match")
		}
		if _, err := os.Stat(filepath.Join(dir, "..", "..", "mio.exe")); err == nil {
			t.Error("a ../.. entry must never be written outside the destination")
		}
	})
}

func TestInstallStagedBinaryRenamesTheOldOneAside(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mio.exe")
	writeFile(t, dest, "OLD")
	writeFile(t, dest+".old", "ANCIENT")
	staged := filepath.Join(dir, "mio.exe.new-123")
	writeFile(t, staged, "NEW")

	var out bytes.Buffer
	if err := installStagedBinary(staged, dest, &out); err != nil {
		t.Fatalf("installStagedBinary: %v", err)
	}
	if got := readFile(t, dest); got != "NEW" {
		t.Errorf("dest = %q, want NEW", got)
	}
	if _, err := os.Stat(staged); err == nil {
		t.Error("the staged file should have been renamed away")
	}
	if _, err := os.Stat(dest + ".old"); err == nil {
		if got := readFile(t, dest+".old"); got == "ANCIENT" {
			t.Error("the stale .old from a previous update was not replaced")
		}
	}
}

func TestInstallStagedBinaryFreshInstall(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mio.exe")
	staged := filepath.Join(dir, "mio.exe.new-1")
	writeFile(t, staged, "NEW")

	if err := installStagedBinary(staged, dest, &bytes.Buffer{}); err != nil {
		t.Fatalf("installStagedBinary with no existing binary: %v", err)
	}
	if got := readFile(t, dest); got != "NEW" {
		t.Errorf("dest = %q, want NEW", got)
	}
}

func TestInstallStagedBinaryRestoresTheOldBinaryWhenTheSwapFails(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mio.exe")
	writeFile(t, dest, "OLD")
	staged := filepath.Join(dir, "does-not-exist")

	err := installStagedBinary(staged, dest, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error when the staged binary cannot be moved into place")
	}
	if !strings.Contains(err.Error(), "restored") {
		t.Errorf("error should say the previous binary was restored, got: %v", err)
	}
	if got := readFile(t, dest); got != "OLD" {
		t.Errorf("dest = %q, want the original OLD binary restored", got)
	}
	if _, err := os.Stat(dest + ".old"); err == nil {
		t.Error(".old should not survive a successful rollback")
	}
}

func TestSweepSupersededBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mio.exe")
	writeFile(t, exe, "current")
	old := exe + ".old"
	writeFile(t, old, "previous")

	sweepSupersededBinary("linux", exe)
	if _, err := os.Stat(old); err != nil {
		t.Fatalf(".old must be left alone off Windows: %v", err)
	}

	sweepSupersededBinary("windows", exe)
	if _, err := os.Stat(old); err == nil {
		t.Error("the superseded binary should have been swept on the next windows run")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Errorf("the running binary must survive the sweep: %v", err)
	}

	sweepSupersededBinary("windows", exe)
	sweepSupersededBinary("windows", "")
}

func TestLatestReleaseVersion(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Searchie-Inc/mio-cli/releases/latest" {
			http.NotFound(w, r)
			return
		}
		gotUA = r.Header.Get("User-Agent")
		fmt.Fprint(w, `{"tag_name":"v0.12.1","name":"v0.12.1"}`)
	}))
	defer srv.Close()
	withReleaseEndpoints(t, srv.URL, srv.URL)

	got, err := latestReleaseVersion(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("latestReleaseVersion: %v", err)
	}
	if got != "0.12.1" {
		t.Errorf("version = %q, want 0.12.1 (leading v stripped)", got)
	}
	if !strings.HasPrefix(gotUA, "mio-cli/") {
		t.Errorf("User-Agent = %q, want a mio-cli/... UA", gotUA)
	}
}

func TestRunNativeUpdateEndToEnd(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mio.exe")
	writeFile(t, dest, "OLD-BINARY")

	srv := newFakeReleaseServer(t, "0.12.1", "FRESH-BINARY", false)
	defer srv.Close()
	withReleaseEndpoints(t, srv.URL, srv.URL)

	var out bytes.Buffer
	err := runNativeUpdate(context.Background(), nativeUpdateConfig{
		Prefix:     dir,
		GOOS:       "windows",
		GOARCH:     "amd64",
		HTTPClient: srv.Client(),
		Out:        &out,
	})
	if err != nil {
		t.Fatalf("runNativeUpdate: %v\noutput:\n%s", err, out.String())
	}
	if got := readFile(t, dest); got != "FRESH-BINARY" {
		t.Errorf("installed binary = %q, want FRESH-BINARY", got)
	}
	for _, want := range []string{"0.12.1", "Downloading", "Checksum verified", "Installed:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("progress output missing %q; got:\n%s", want, out.String())
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("stat installed binary: %v", err)
		}
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("installed binary mode = %v, want the exec bits set", fi.Mode().Perm())
		}
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".new-") {
			t.Errorf("staging file left behind: %s", e.Name())
		}
	}
}

func TestRunNativeUpdateRefusesOnChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mio.exe")
	writeFile(t, dest, "OLD-BINARY")

	srv := newFakeReleaseServer(t, "0.12.1", "TAMPERED", true)
	defer srv.Close()
	withReleaseEndpoints(t, srv.URL, srv.URL)

	err := runNativeUpdate(context.Background(), nativeUpdateConfig{
		Prefix:     dir,
		Version:    "0.12.1",
		GOOS:       "windows",
		GOARCH:     "amd64",
		HTTPClient: srv.Client(),
		Out:        &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want a checksum mismatch", err)
	}
	if got := readFile(t, dest); got != "OLD-BINARY" {
		t.Errorf("the existing binary must be untouched on a failed verification, got %q", got)
	}
}

func TestRunNativeUpdateMissingReleaseIsAClearError(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withReleaseEndpoints(t, srv.URL, srv.URL)

	err := runNativeUpdate(context.Background(), nativeUpdateConfig{
		Prefix:     dir,
		Version:    "9.9.9",
		GOOS:       "windows",
		GOARCH:     "amd64",
		HTTPClient: srv.Client(),
		Out:        &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want a 'release asset not found' message", err)
	}
}

func TestRunNativeUpdateRejectsUnbuiltWindowsArch(t *testing.T) {
	err := runNativeUpdate(context.Background(), nativeUpdateConfig{
		Prefix: t.TempDir(),
		GOOS:   "windows",
		GOARCH: "arm64",
	})
	if err == nil || !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("error = %v, want a no-published-release message for windows/arm64", err)
	}
}

func TestRunNativeUpdateValidatesLocalInputBeforeAnyRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withReleaseEndpoints(t, srv.URL, srv.URL)

	cases := []struct {
		name    string
		cfg     nativeUpdateConfig
		wantErr string
	}{
		{
			name:    "missing install dir",
			cfg:     nativeUpdateConfig{Prefix: filepath.Join(t.TempDir(), "nope"), GOOS: "windows", GOARCH: "amd64"},
			wantErr: "install directory",
		},
		{
			name:    "install dir is a file",
			cfg:     nativeUpdateConfig{Prefix: writeTempFile(t, "not-a-dir"), GOOS: "windows", GOARCH: "amd64"},
			wantErr: "not a directory",
		},
		{
			name:    "unbuilt arch",
			cfg:     nativeUpdateConfig{Prefix: t.TempDir(), GOOS: "windows", GOARCH: "arm64"},
			wantErr: "windows/arm64",
		},
		{
			name:    "path traversal in --version",
			cfg:     nativeUpdateConfig{Prefix: t.TempDir(), Version: "../../../../etc", GOOS: "windows", GOARCH: "amd64"},
			wantErr: "not a valid release version",
		},
		{
			name:    "separator in --version",
			cfg:     nativeUpdateConfig{Prefix: t.TempDir(), Version: "0.12.1/../x", GOOS: "windows", GOARCH: "amd64"},
			wantErr: "not a valid release version",
		},
		{
			name:    "dest exists but is not a regular file",
			cfg:     nativeUpdateConfig{Prefix: dirContainingDir(t, "mio.exe"), GOOS: "windows", GOARCH: "amd64"},
			wantErr: "not a regular file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.HTTPClient = srv.Client()
			tc.cfg.Out = &bytes.Buffer{}
			err := runNativeUpdate(context.Background(), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if got := errs.CodeOf(err); got != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
			}
		})
	}
	if requests != 0 {
		t.Errorf("%d HTTP request(s) fired for locally-invalid input; want 0", requests)
	}
}

func TestValidateReleaseVersion(t *testing.T) {
	for _, ok := range []string{"0.12.1", "v0.12.1", "1.0.0-rc.1", "0.12.1+build_7"} {
		if err := validateReleaseVersion(normalizeReleaseVersion(ok)); err != nil {
			t.Errorf("validateReleaseVersion(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"../0.12.1", "0.12.1/x", `0.12.1\x`, "0.12 .1", "0.12.1?a=b", ".."} {
		if err := validateReleaseVersion(normalizeReleaseVersion(bad)); err == nil {
			t.Errorf("validateReleaseVersion(%q) = nil, want an error", bad)
		}
	}
}

func TestInstallStagedBinaryRefusesANonRegularDest(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mio.exe")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep.txt"), []byte("precious"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	staged := filepath.Join(dir, "mio.exe.new-1")
	writeFile(t, staged, "NEW")
	writeFile(t, dest+".old", "PREVIOUS")

	err := installStagedBinary(staged, dest, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want a not-a-regular-file refusal", err)
	}
	if got := readFile(t, dest+".old"); got != "PREVIOUS" {
		t.Errorf("the existing .old was disturbed by a refused install: %q", got)
	}
	if fi, statErr := os.Stat(dest); statErr != nil || !fi.IsDir() {
		t.Errorf("the existing directory must be left exactly as it was (stat err %v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "keep.txt")); statErr != nil {
		t.Errorf("contents of the existing directory were disturbed: %v", statErr)
	}
	if got := readFile(t, dest+".old"); got != "PREVIOUS" {
		t.Errorf("nothing should have been renamed aside; .old = %q", got)
	}
}

func TestRunNativeUpdateMalformedUpstreamTagIsNotAUsageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.12.1/../../x"}`)
	}))
	defer srv.Close()
	withReleaseEndpoints(t, srv.URL, srv.URL)

	err := runNativeUpdate(context.Background(), nativeUpdateConfig{
		Prefix:     t.TempDir(),
		GOOS:       "windows",
		GOARCH:     "amd64",
		HTTPClient: srv.Client(),
		Out:        &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "unusable") {
		t.Fatalf("error = %v, want an unusable-upstream-tag error", err)
	}
	if got := errs.CodeOf(err); got != errs.ExitGeneric {
		t.Errorf("exit code = %d, want %d (ExitGeneric — the user typed nothing wrong)", got, errs.ExitGeneric)
	}
}

func withReleaseEndpoints(t *testing.T, api, download string) {
	t.Helper()
	oldAPI, oldDownload := githubAPIBaseURL, githubDownloadBaseURL
	githubAPIBaseURL, githubDownloadBaseURL = api, download
	t.Cleanup(func() { githubAPIBaseURL, githubDownloadBaseURL = oldAPI, oldDownload })
}

func newFakeReleaseServer(t *testing.T, version, binaryContent string, corruptChecksum bool) *httptest.Server {
	t.Helper()

	archive := filepath.Join(t.TempDir(), "release.zip")
	writeTestZip(t, archive, map[string]string{
		"LICENSE": "MIT",
		"mio.exe": binaryContent,
	})
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read fixture archive: %v", err)
	}
	sum := sha256.Sum256(archiveBytes)
	hexSum := hex.EncodeToString(sum[:])
	if corruptChecksum {
		hexSum = strings.Repeat("0", len(hexSum))
	}
	asset := releaseAssetName(version, "windows", "amd64")

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Searchie-Inc/mio-cli/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v%s"}`, version)
		case "/Searchie-Inc/mio-cli/releases/download/v" + version + "/" + asset:
			w.Write(archiveBytes)
		case "/Searchie-Inc/mio-cli/releases/download/v" + version + "/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", hexSum, asset)
		default:
			http.NotFound(w, r)
		}
	}))
}

func writeTestZip(t *testing.T, path string, members map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func dirContainingDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	writeFile(t, path, content)
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
