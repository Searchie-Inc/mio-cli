package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/99designs/keyring"
)

func blobPathFor(dir string) string {
	return filepath.Join(dir, "mio", "keyring", keyringKeyName)
}

func TestSetAPIKey_ReplacesBlobInsteadOfRewritingIt(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)

	if err := SetAPIKey("mio_sk_live_first"); err != nil {
		t.Fatalf("first SetAPIKey: %v", err)
	}
	blob := blobPathFor(dir)
	before, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("read first blob: %v", err)
	}
	oldInode := filepath.Join(dir, "old-inode")
	if err := os.Link(blob, oldInode); err != nil {
		t.Fatalf("hard-link the first blob: %v", err)
	}

	if err := SetAPIKey("mio_sk_live_second"); err != nil {
		t.Fatalf("second SetAPIKey: %v", err)
	}

	stillOld, err := os.ReadFile(oldInode)
	if err != nil {
		t.Fatalf("read old inode: %v", err)
	}
	if !bytes.Equal(stillOld, before) {
		t.Fatalf("SetAPIKey rewrote the stored blob IN PLACE (the inode that held the first key now holds %d different bytes): "+
			"a concurrent reader can observe it truncated or half-written — write a new file and rename it over the old path", len(stillOld))
	}
	got, err := GetAPIKey()
	if err != nil {
		t.Fatalf("GetAPIKey after replace: %v", err)
	}
	if got != "mio_sk_live_second" {
		t.Fatalf("GetAPIKey = %q, want the second key", got)
	}
	info, err := os.Stat(blob)
	if err != nil {
		t.Fatalf("stat replaced blob: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("replaced blob perms = %04o, want 0600", perm)
	}
}

func TestGetAPIKey_NeverDeletesATruncatedBlob(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)

	if err := SetAPIKey("mio_sk_live_truncation_probe_0123456789"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	blob := blobPathFor(dir)
	full, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	for n := 0; n < len(full); n++ {
		if err := os.WriteFile(blob, full[:n], 0o600); err != nil {
			t.Fatalf("write %d-byte prefix: %v", n, err)
		}
		key, gerr := GetAPIKey()
		if _, statErr := os.Stat(blob); errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("a %d-byte prefix of a valid %d-byte blob (what a reader sees mid-write) DELETED the stored key; GetAPIKey returned %v",
				n, len(full), gerr)
		}
		if errors.Is(gerr, ErrLegacyCredentials) {
			t.Fatalf("a %d-byte prefix of a valid blob was reported as legacy encryption: %v", n, gerr)
		}
		if !errors.Is(gerr, ErrUnreadableCredentials) {
			t.Fatalf("a %d-byte prefix: GetAPIKey = (%q, %v), want ErrUnreadableCredentials", n, key, gerr)
		}
	}
}

func noRetryWait(t *testing.T) {
	t.Helper()
	orig := unreadableRetryWait
	unreadableRetryWait = func() {}
	t.Cleanup(func() { unreadableRetryWait = orig })
}

func TestGetAPIKey_WaitsOutAWriteInFlight(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)

	const want = "mio_sk_live_written_by_the_other_process"
	if err := SetAPIKey(want); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	blob := blobPathFor(dir)
	full, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if err := os.WriteFile(blob, nil, 0o600); err != nil {
		t.Fatalf("truncate blob: %v", err)
	}

	waits := 0
	orig := unreadableRetryWait
	unreadableRetryWait = func() {
		waits++
		if waits == 1 {
			if werr := os.WriteFile(blob, full, 0o600); werr != nil {
				t.Errorf("finish the in-flight write: %v", werr)
			}
		}
	}
	t.Cleanup(func() { unreadableRetryWait = orig })

	got, err := GetAPIKey()
	if err != nil {
		t.Fatalf("GetAPIKey during an in-flight write = %v; the read must back off and re-read rather than fail on the half-written blob", err)
	}
	if got != want {
		t.Fatalf("GetAPIKey = %q, want %q", got, want)
	}
	if waits == 0 {
		t.Fatal("GetAPIKey succeeded without ever backing off, so this test did not exercise the in-flight window")
	}
}

func TestGetAPIKey_KeyFileFailureIsNotRetriedAway(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)

	if err := SetAPIKey("mio_sk_live_key_file_drift"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "mio", fileKeyName), 0o644); err != nil {
		t.Fatalf("chmod key file: %v", err)
	}

	got, err := GetAPIKey()
	if err == nil {
		t.Fatalf("GetAPIKey = (%q, nil) after the key file drift invalidated the stored key; "+
			"a revoked key must not be reported as an empty store", got)
	}
	if !StoredKeyUnusable(err) {
		t.Fatalf("GetAPIKey error = %v; want an unusable-stored-credential error (exit 3)", err)
	}
	if !strings.Contains(err.Error(), "permissions") {
		t.Errorf("GetAPIKey error = %v; want it to carry the key-file permission failure", err)
	}
}

func TestGetAPIKey_ReadNeverMintsAKeyFile(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)

	if err := SetAPIKey("mio_sk_live_orphaned_blob"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	keyFile := filepath.Join(dir, "mio", fileKeyName)
	if err := os.Remove(keyFile); err != nil {
		t.Fatalf("remove key file: %v", err)
	}

	_, err := GetAPIKey()
	if !errors.Is(err, ErrUnreadableCredentials) {
		t.Fatalf("GetAPIKey with the key file gone = %v; want ErrUnreadableCredentials", err)
	}
	if _, statErr := os.Stat(keyFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a READ created %s (stat err = %v); only writes may mint the per-install key", keyFile, statErr)
	}
	if _, statErr := os.Stat(blobPathFor(dir)); statErr != nil {
		t.Fatalf("the orphaned blob was removed by a read: %v", statErr)
	}
}

func TestLoadAPIKey_NamesTheStoreItConsulted(t *testing.T) {
	t.Run("XDG_CONFIG_HOME set", func(t *testing.T) {
		dir := withXDG(t)
		withFileBackendOnly(t)

		_, store, err := LoadAPIKey()
		if err != nil {
			t.Fatalf("LoadAPIKey on an empty store: %v", err)
		}
		want := Store{Backend: keyring.FileBackend, Path: blobPathFor(dir), DirSource: "XDG_CONFIG_HOME"}
		if store != want {
			t.Fatalf("store = %+v, want %+v", store, want)
		}
	})
	t.Run("XDG_CONFIG_HOME unset", func(t *testing.T) {
		withXDG(t)
		withFileBackendOnly(t)
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)

		_, store, err := LoadAPIKey()
		if err != nil {
			t.Fatalf("LoadAPIKey: %v", err)
		}
		wantPath := filepath.Join(home, ".config", "mio", "keyring", keyringKeyName)
		if store.Backend != keyring.FileBackend || store.Path != wantPath {
			t.Fatalf("store = %+v, want the file keyring at %s", store, wantPath)
		}
		if store.DirSource != homeEnvName() {
			t.Fatalf("DirSource = %q, want %q (XDG_CONFIG_HOME is unset)", store.DirSource, homeEnvName())
		}
	})
}

func TestLoadAPIKey_AnotherConfigDirIsAnotherStore(t *testing.T) {
	loggedIn := withXDG(t)
	withFileBackendOnly(t)
	if err := SetAPIKey("mio_sk_live_stored_under_the_login_shell"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}

	other := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", other)
	key, store, err := LoadAPIKey()
	if err != nil {
		t.Fatalf("LoadAPIKey under the other config dir: %v", err)
	}
	if key != "" {
		t.Fatalf("LoadAPIKey under a different XDG_CONFIG_HOME found %q; the premise of this reproduction is that it cannot", key)
	}
	if store.Path != blobPathFor(other) {
		t.Fatalf("the empty lookup reported store path %q, want the one it actually read (%s)", store.Path, blobPathFor(other))
	}

	t.Setenv("XDG_CONFIG_HOME", loggedIn)
	if key, _, err := LoadAPIKey(); err != nil || key != "mio_sk_live_stored_under_the_login_shell" {
		t.Fatalf("back under the login config dir: LoadAPIKey = (%q, %v), want the stored key", key, err)
	}
}

func TestStore_Label(t *testing.T) {
	cases := []struct {
		store Store
		want  string
	}{
		{Store{Backend: keyring.KeychainBackend}, "keychain"},
		{Store{Backend: keyring.FileBackend, Path: "/cfg/mio/keyring/api-key", DirSource: "XDG_CONFIG_HOME"}, "file keyring (/cfg/mio/keyring/api-key)"},
		{Store{Backend: keyring.SecretServiceBackend}, "secret service"},
		{Store{Backend: keyring.KWalletBackend}, "kwallet"},
		{Store{Backend: keyring.WinCredBackend}, "wincred"},
	}
	for _, tc := range cases {
		if got := tc.store.Label(); got != tc.want {
			t.Errorf("Store{%s}.Label() = %q, want %q", tc.store.Backend, got, tc.want)
		}
	}
}

func TestResolve_UnreadableBlobStillResolvesContext(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)
	if err := SetAPIKey("mio_sk_live_soon_unreadable"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := os.WriteFile(blobPathFor(dir), nil, 0o600); err != nil {
		t.Fatalf("empty the blob: %v", err)
	}
	cfg := &Config{CurrentTeam: "team_cfg"}

	r, err := cfg.Resolve(Overrides{APIBase: "https://api.example"})
	if !errors.Is(err, ErrUnreadableCredentials) {
		t.Fatalf("Resolve err = %v, want ErrUnreadableCredentials", err)
	}
	if r.APIBase != "https://api.example" || r.TeamID != "team_cfg" {
		t.Fatalf("Resolve returned %+v alongside the unreadable-key error; the base and team must still resolve", r)
	}
	if r.KeyStore == nil || r.KeyStore.Path != blobPathFor(dir) {
		t.Fatalf("Resolved.KeyStore = %+v, want the file store it read", r.KeyStore)
	}
	if _, statErr := os.Stat(blobPathFor(dir)); statErr != nil {
		t.Fatalf("the unreadable blob was removed by a read: %v", statErr)
	}
}

func completeBlob(data []byte) bool {
	parts := strings.Split(string(data), ".")
	return len(parts) == 5 && len(parts[4]) == 22
}

func TestSetAPIKey_ReaderNeverSeesTheBlobMissingOrPartial(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	if err := SetAPIKey("mio_sk_live_publication_0"); err != nil {
		t.Fatalf("seed SetAPIKey: %v", err)
	}
	blob := blobPathFor(dir)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		reads   int
		missing int
		partial int
		first   string
	)
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(blob)
			mu.Lock()
			reads++
			switch {
			case err != nil:
				missing++
				if first == "" {
					first = err.Error()
				}
			case !completeBlob(data):
				partial++
				if first == "" {
					first = fmt.Sprintf("a %d-byte partial blob", len(data))
				}
			}
			mu.Unlock()
		}
	}()

	const writes = 150
	for i := 1; i <= writes; i++ {
		if err := SetAPIKey("mio_sk_live_publication_" + strings.Repeat("x", i%7)); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("SetAPIKey #%d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()

	if missing > 0 || partial > 0 {
		t.Fatalf("across %d writes a concurrent reader saw the stored blob MISSING %d times and PARTIAL %d times (of %d reads; first: %s); "+
			"SetAPIKey must publish the new blob with a single rename over the old one", writes, missing, partial, reads, first)
	}
	if reads < writes {
		t.Fatalf("the reader only managed %d reads across %d writes, too few to have watched the publication windows", reads, writes)
	}
}

func TestSetAPIKey_StagesBesideTheBlob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not restrict writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	dir := withXDG(t)
	withFileBackendOnly(t)
	if err := SetAPIKey("mio_sk_live_first"); err != nil {
		t.Fatalf("seed SetAPIKey: %v", err)
	}
	mioDir := filepath.Join(dir, "mio")
	if err := os.Chmod(mioDir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", mioDir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(mioDir, 0o700) })

	if err := SetAPIKey("mio_sk_live_second"); err != nil {
		t.Fatalf("SetAPIKey with only the keyring dir writable: %v — the staging dir must sit beside the blob, not in its parent", err)
	}
	if got, err := GetAPIKey(); err != nil || got != "mio_sk_live_second" {
		t.Fatalf("GetAPIKey = (%q, %v), want the second key", got, err)
	}
}

func TestGetAPIKey_KeyFileFilesystemErrorIsNotACredentialVerdict(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)
	if err := SetAPIKey("mio_sk_live_key_file_unreadable"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	keyPath := filepath.Join(dir, "mio", fileKeyName)
	orig := readFileKey
	readFileKey = func(string) (string, error) {
		return "", &fs.PathError{Op: "open", Path: keyPath, Err: syscall.EACCES}
	}
	t.Cleanup(func() { readFileKey = orig })

	_, err := GetAPIKey()
	if err == nil {
		t.Fatal("GetAPIKey succeeded with an unreadable key file")
	}
	if StoredKeyUnusable(err) {
		t.Fatalf("GetAPIKey = %v; a key file that cannot be READ is a filesystem failure (exit 1), not a verdict on the stored credential (exit 3)", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("GetAPIKey error = %v; want the underlying permission error", err)
	}
	if _, statErr := os.Stat(blobPathFor(dir)); statErr != nil {
		t.Errorf("the blob was removed: %v", statErr)
	}
}

func TestDeleteAPIKey_NothingStoredIsNotAnError(t *testing.T) {
	withXDG(t)
	withFileBackendOnly(t)
	if err := DeleteAPIKey(); err != nil {
		t.Fatalf("DeleteAPIKey with nothing stored = %v, want nil", err)
	}
}

var legacyInstalls = []struct {
	name        string
	withKeyFile bool
}{
	{"v0.1 install, no key file", false},
	{"key file beside a legacy blob", true},
}

func seedLegacyBlob(t *testing.T, dir string, withKeyFile bool) ([]byte, fs.FileInfo) {
	t.Helper()
	if withKeyFile {
		if _, err := loadOrCreateFileKey(); err != nil {
			t.Fatalf("mint the key file: %v", err)
		}
	}
	ring, err := openKeyringWithPassword(legacyFilePassphrase, "")
	if err != nil {
		t.Fatalf("open legacy ring: %v", err)
	}
	if err := ring.Set(legacyKeyringItem("mio_sk_old_legacy_key")); err != nil {
		t.Fatalf("seed legacy blob: %v", err)
	}
	blob := blobPathFor(dir)
	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("read seeded blob: %v", err)
	}
	info, err := os.Lstat(blob)
	if err != nil {
		t.Fatalf("stat seeded blob: %v", err)
	}
	return data, info
}

func assertLegacyBlobUntouched(t *testing.T, dir string, want []byte, wantInfo fs.FileInfo, via string) {
	t.Helper()
	blob := blobPathFor(dir)
	info, err := os.Lstat(blob)
	if err != nil {
		t.Fatalf("after %s the legacy blob is gone from %s (%v): a read must report it and leave it in place", via, blob, err)
	}
	if !os.SameFile(info, wantInfo) {
		t.Fatalf("after %s %s is no longer the file that held the legacy blob: a read must not move or replace a stored credential", via, blob)
	}
	got, err := os.ReadFile(blob)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("after %s the legacy blob's bytes changed (read err %v): a read must not rewrite a stored credential", via, err)
	}
	entries, err := os.ReadDir(filepath.Dir(blob))
	if err != nil {
		t.Fatalf("read keyring dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("after %s the keyring dir holds %v, want only %s", via, names, keyringKeyName)
	}
}

func TestLegacyBlob_EveryReadReportsItAndLeavesItInPlace(t *testing.T) {
	reads := []struct {
		name string
		read func() error
	}{
		{"GetAPIKey", func() error { _, err := GetAPIKey(); return err }},
		{"LoadAPIKey", func() error { _, _, err := LoadAPIKey(); return err }},
		{"Resolve", func() error { _, err := (&Config{}).Resolve(Overrides{}); return err }},
	}
	for _, inst := range legacyInstalls {
		t.Run(inst.name, func(t *testing.T) {
			dir := withXDG(t)
			withFileBackendOnly(t)
			noRetryWait(t)
			want, wantInfo := seedLegacyBlob(t, dir, inst.withKeyFile)

			for _, r := range reads {
				err := r.read()
				if !errors.Is(err, ErrLegacyCredentials) {
					t.Fatalf("%s on a legacy blob = %v, want ErrLegacyCredentials", r.name, err)
				}
				if msg := err.Error(); !strings.Contains(msg, blobPathFor(dir)) || !strings.Contains(msg, "mio login") {
					t.Errorf("%s: the legacy error must name the store it read (%s) and say to run `mio login`: %q", r.name, blobPathFor(dir), msg)
				}
				assertLegacyBlobUntouched(t, dir, want, wantInfo, r.name)
			}
		})
	}
}

func TestSetAPIKey_ReplacesALegacyBlob(t *testing.T) {
	for _, inst := range legacyInstalls {
		t.Run(inst.name, func(t *testing.T) {
			dir := withXDG(t)
			withFileBackendOnly(t)
			noRetryWait(t)
			seedLegacyBlob(t, dir, inst.withKeyFile)
			if _, err := GetAPIKey(); !errors.Is(err, ErrLegacyCredentials) {
				t.Fatalf("precondition: reading the seeded blob = %v, want ErrLegacyCredentials", err)
			}

			const fresh = "mio_sk_live_stored_over_a_legacy_blob"
			if err := SetAPIKey(fresh); err != nil {
				t.Fatalf("SetAPIKey over a legacy blob = %v: storing a key must replace it, it is how `mio login` recovers", err)
			}
			if got, err := GetAPIKey(); err != nil || got != fresh {
				t.Fatalf("after SetAPIKey over a legacy blob the store reads (%q, %v), want %q", got, err, fresh)
			}
		})
	}
}

func TestDeleteAPIKey_RemovesTheLiveBlob(t *testing.T) {
	seeds := []struct {
		name string
		seed func(t *testing.T, dir string)
	}{
		{"current blob", func(t *testing.T, _ string) {
			if err := SetAPIKey("mio_sk_live_logged_out"); err != nil {
				t.Fatalf("SetAPIKey: %v", err)
			}
		}},
		{"legacy blob", func(t *testing.T, dir string) { seedLegacyBlob(t, dir, false) }},
	}
	for _, s := range seeds {
		t.Run(s.name, func(t *testing.T) {
			dir := withXDG(t)
			withFileBackendOnly(t)
			noRetryWait(t)
			s.seed(t, dir)

			if err := DeleteAPIKey(); err != nil {
				t.Fatalf("DeleteAPIKey = %v, want nil", err)
			}
			if _, err := os.Lstat(blobPathFor(dir)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("after DeleteAPIKey the live blob is still there (stat err = %v): logout must remove it", err)
			}
			if key, _, err := LoadAPIKey(); key != "" || err != nil {
				t.Fatalf("after DeleteAPIKey a read = (%q, %v), want no key stored", key, err)
			}
			if err := DeleteAPIKey(); err != nil {
				t.Fatalf("a second DeleteAPIKey, with nothing stored = %v, want nil", err)
			}
		})
	}
}
