package config

// credential_store_test.go — MIO-2995: the stored API key must never be
// destroyed or mis-read because another mio process happened to be writing it
// at the same moment, and every stored-key lookup must be able to say WHICH
// store it consulted.
//
// Every test here pins the keyring to the encrypted FILE backend under a
// t.TempDir() config dir (withXDG + withFileBackendOnly), so none of them can
// read or write the developer's real ~/.config/mio, OS keychain or Secret
// Service.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99designs/keyring"
)

// blobPathFor is where the file backend keeps the encrypted API key under the
// config dir dir (as set in XDG_CONFIG_HOME by withXDG).
func blobPathFor(dir string) string {
	return filepath.Join(dir, "mio", "keyring", keyringKeyName)
}

// TestSetAPIKey_ReplacesBlobInsteadOfRewritingIt pins the WRITE half of the
// concurrent-writer race. The keyring library's file backend writes with
// os.WriteFile, which truncates the blob in place and then writes it: a reader
// that lands between the two sees an empty (or partial) blob. Measured before
// the fix: a reader looping GetAPIKey against a writer looping SetAPIKey got
// "jwt.DecodeBytes() expects token of 3 or 5 parts, but was given: 1 parts"
// (exit 1) twice in ~360 reads.
//
// A timing race is a poor guard, so this test checks the property that makes
// the race impossible instead: SetAPIKey must publish a NEW file over the old
// path (write elsewhere, then rename), never rewrite the existing inode. A hard
// link taken before the write still names the old inode afterwards; if
// SetAPIKey rewrote in place, that link would now hold the new blob.
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

// TestGetAPIKey_NeverDeletesATruncatedBlob pins the READ half. A truncated blob
// is exactly what a reader sees mid-write, and GetAPIKey used to classify some
// truncations as "legacy encryption" and DELETE the stored key: measured before
// the fix, every prefix of a 579-byte blob from length 557 to 578 (a cut inside
// the auth tag) was deleted with ErrLegacyCredentials. Deletion is only
// justified for a blob positively identified as legacy-encrypted; anything else
// must be left for the writer to finish (or for `mio login` to replace).
//
// Every prefix length is probed, not a sample, so a narrower deletion window
// cannot hide between probes.
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

// noRetryWait makes the bounded unreadable-blob retry instantaneous for the
// duration of a test, so probing hundreds of truncations stays fast.
func noRetryWait(t *testing.T) {
	t.Helper()
	orig := unreadableRetryWait
	unreadableRetryWait = func() {}
	t.Cleanup(func() { unreadableRetryWait = orig })
}

// TestGetAPIKey_WaitsOutAWriteInFlight pins the bounded retry. A mio binary
// that predates the rename-based write (every release up to v0.22.0) still
// truncates the blob in place, so a CURRENT reader can land in that window —
// the empty blob the concurrent reproduction observed. Here the "writer"
// finishes during the reader's first back-off, deterministically, and the read
// must return the finished key instead of an error.
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
	// The other process has truncated the blob and not yet written it...
	if err := os.WriteFile(blob, nil, 0o600); err != nil {
		t.Fatalf("truncate blob: %v", err)
	}

	waits := 0
	orig := unreadableRetryWait
	unreadableRetryWait = func() {
		waits++
		if waits == 1 {
			// ...and finishes its write while this reader backs off.
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

// TestGetAPIKey_KeyFileFailureIsNotRetriedAway pins the other edge of the
// retry: it may only re-read a blob that failed to DECODE, never a failure of
// the per-install key file. The case is real and deterministic: a key file
// whose permissions drifted from 0600 is treated as compromised, and
// readAndValidateFileKey deletes it AND the blob before failing. A re-read at
// that point answers "not found", and the caller would print "no API key
// found" for a key that was revoked one line earlier. The read must instead
// fail as an unusable stored credential (exit 3) that carries the real cause.
// (Before MIO-2995 this surfaced as "stored credentials use legacy
// encryption", because the read path silently regenerated the key file.)
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

// TestGetAPIKey_ReadNeverMintsAKeyFile: a read that finds a blob but no key
// file must not create one. A key minted after the blob can never decrypt it,
// so creating it only turns "the key file is missing" into an opaque decode
// failure — and leaves a fresh key file behind that the next write adopts.
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

// TestLoadAPIKey_NamesTheStoreItConsulted: every stored-key lookup reports the
// backend and, for the file backend, the blob path and the environment
// variable that chose its config dir — whether or not a key was found.
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
		t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows

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

// TestLoadAPIKey_AnotherConfigDirIsAnotherStore reproduces the mechanism behind
// MIO-2995's "eight calls in one shell all exit 3, the next shell is fine". The
// release builds keep the key in a FILE under the config dir, so a process
// whose XDG_CONFIG_HOME (or HOME) differs is reading a different, empty store —
// the key has not gone anywhere. The lookup must say which store it read, so
// the mismatch diagnoses itself instead of looking like a flaky keychain.
func TestLoadAPIKey_AnotherConfigDirIsAnotherStore(t *testing.T) {
	loggedIn := withXDG(t)
	withFileBackendOnly(t)
	if err := SetAPIKey("mio_sk_live_stored_under_the_login_shell"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}

	other := t.TempDir() // e.g. an agent shell that ran `export XDG_CONFIG_HOME=$(mktemp -d)`
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

// TestStore_Label pins what `whoami` reports as key_source for each backend.
// "keychain" keeps its exact spelling for the macOS Keychain, the only backend
// it was ever accurate for; every other backend is named for what it is.
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

// TestResolve_UnreadableBlobStillResolvesContext: an unreadable stored key must
// come back as ErrUnreadableCredentials WITH the rest of the context resolved —
// `mio login` relies on that to fall through and overwrite the blob. Before
// MIO-2995 an empty blob returned a bare error and an empty Resolved, which
// made `mio login --email … --password …` abort (exit 1) instead of repairing
// the store.
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
