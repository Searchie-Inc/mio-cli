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

// completeBlob reports whether data is a whole JWE compact token as the file
// backend writes it: five dot-separated parts, the last being the 16-byte
// A256GCM auth tag (22 base64url characters). Any prefix of a real blob fails
// this: a cut inside the ciphertext leaves fewer parts, a cut inside the tag a
// shorter one.
func completeBlob(data []byte) bool {
	parts := strings.Split(string(data), ".")
	return len(parts) == 5 && len(parts[4]) == 22
}

// TestSetAPIKey_ReaderNeverSeesTheBlobMissingOrPartial pins what the inode
// test above cannot: that the new blob is PUBLISHED atomically, not merely
// that it lands on a new inode. An implementation that removed the live blob
// and then renamed its replacement in would pass the inode test while a
// concurrent reader saw no blob at all — "no API key found", MIO-2995's exact
// symptom. A reader spins on the live path for the whole of a run of writes
// and must see a complete blob every single time.
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

// TestGetAPIKey_LegacyCleanupNeverDeletesANewerKey pins the delete step of the
// legacy path. Between recognising a legacy blob and removing it, another
// process can publish a fresh key over the same path (`mio login` does exactly
// that). A plain remove at that point deletes the NEW key. The removal must
// only ever take the blob it verified.
func TestGetAPIKey_LegacyCleanupNeverDeletesANewerKey(t *testing.T) {
	withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)

	legacyRing, err := openKeyringWithPassword(legacyFilePassphrase, "")
	if err != nil {
		t.Fatalf("open legacy ring: %v", err)
	}
	if err := legacyRing.Set(legacyKeyringItem("mio_sk_old_legacy_key")); err != nil {
		t.Fatalf("seed legacy blob: %v", err)
	}

	const fresh = "mio_sk_live_published_by_a_concurrent_login"
	published := false
	orig := beforeLegacyRemoval
	beforeLegacyRemoval = func() {
		if published {
			return
		}
		published = true
		if err := SetAPIKey(fresh); err != nil {
			t.Errorf("concurrent SetAPIKey: %v", err)
		}
	}
	t.Cleanup(func() { beforeLegacyRemoval = orig })

	if _, err := GetAPIKey(); !errors.Is(err, ErrLegacyCredentials) {
		t.Fatalf("first read = %v, want ErrLegacyCredentials (it saw the legacy blob)", err)
	}
	if !published {
		t.Fatal("the removal hook never ran, so this test did not exercise the race")
	}
	got, err := GetAPIKey()
	if err != nil || got != fresh {
		t.Fatalf("after the legacy cleanup the store holds (%q, %v); the key published between recognition and removal was deleted", got, err)
	}
}

// TestGetAPIKey_LegacyCleanupNeverHidesANewerKey: not deleting the key a
// concurrent `mio login` published over a recognised legacy blob is not
// enough; the cleanup must not move it off the live path at all. While it is
// detached, every reader, and every `mio` command resolving the stored key,
// reports "no API key stored", and a crash in that window strands it in a
// hidden directory. A login publishes by rename, so the live path then names a
// different file from the one recognised as legacy, and the cleanup must see
// that before it detaches anything.
func TestGetAPIKey_LegacyCleanupNeverHidesANewerKey(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)

	legacyRing, err := openKeyringWithPassword(legacyFilePassphrase, "")
	if err != nil {
		t.Fatalf("open legacy ring: %v", err)
	}
	if err := legacyRing.Set(legacyKeyringItem("mio_sk_old_legacy_key")); err != nil {
		t.Fatalf("seed legacy blob: %v", err)
	}

	const fresh = "mio_sk_live_published_by_a_concurrent_login"
	published := false
	origBefore := beforeLegacyRemoval
	beforeLegacyRemoval = func() {
		if published {
			return
		}
		published = true
		if err := SetAPIKey(fresh); err != nil {
			t.Errorf("concurrent SetAPIKey: %v", err)
		}
	}
	var hidden string
	origDetach := afterLegacyDetach
	afterLegacyDetach = func() {
		if _, err := os.Lstat(blobPathFor(dir)); err != nil {
			hidden = err.Error()
		}
	}
	t.Cleanup(func() { beforeLegacyRemoval, afterLegacyDetach = origBefore, origDetach })

	if _, err := GetAPIKey(); !errors.Is(err, ErrLegacyCredentials) {
		t.Fatalf("first read = %v, want ErrLegacyCredentials (it saw the legacy blob)", err)
	}
	if !published {
		t.Fatal("the removal hook never ran, so this test did not exercise the race")
	}
	if hidden != "" {
		t.Fatalf("the legacy cleanup moved the key a concurrent login had just published off the live path (%s); "+
			"a reader in that window is told no key is stored", hidden)
	}
	if got, err := GetAPIKey(); err != nil || got != fresh {
		t.Fatalf("after the legacy cleanup the store holds (%q, %v), want the published key", got, err)
	}
}

// TestGetAPIKey_KeyMovedAsideByLegacyCleanupIsNeverReportedMissing: the
// identity check cannot close the race completely. A login whose rename lands
// between that check and the detach still has its key moved aside for a
// moment, and a crash there strands it in the .legacy-* directory. POSIX has
// no compare-and-rename, so the reader has to cope: while a detached blob
// exists and the live one does not, a read must never answer "no key stored".
// It waits (the bounded retry) for the blob to be put back, and failing that
// names where the key is. Here the reader runs inside the window, on the
// cleanup's own goroutine, so the blob cannot come back while it waits: the
// answer must be the unusable-credential error naming the detached copy.
func TestGetAPIKey_KeyMovedAsideByLegacyCleanupIsNeverReportedMissing(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)

	legacyRing, err := openKeyringWithPassword(legacyFilePassphrase, "")
	if err != nil {
		t.Fatalf("open legacy ring: %v", err)
	}
	if err := legacyRing.Set(legacyKeyringItem("mio_sk_old_legacy_key")); err != nil {
		t.Fatalf("seed legacy blob: %v", err)
	}

	const fresh = "mio_sk_live_published_after_the_identity_check"
	published := false
	origBefore := beforeLegacyDetach
	beforeLegacyDetach = func() {
		if published {
			return
		}
		published = true
		if err := SetAPIKey(fresh); err != nil {
			t.Errorf("concurrent SetAPIKey: %v", err)
		}
	}
	var midKey string
	var midErr error
	midRead := false
	origDetach := afterLegacyDetach
	afterLegacyDetach = func() {
		midRead = true
		midKey, _, midErr = LoadAPIKey()
	}
	t.Cleanup(func() { beforeLegacyDetach, afterLegacyDetach = origBefore, origDetach })

	if _, err := GetAPIKey(); !errors.Is(err, ErrLegacyCredentials) {
		t.Fatalf("first read = %v, want ErrLegacyCredentials (it saw the legacy blob)", err)
	}
	if !published || !midRead {
		t.Fatalf("the detach hooks did not both run (published=%v, mid-window read=%v), so this test did not exercise the race", published, midRead)
	}
	if midErr == nil {
		t.Fatalf("a read while the published key was moved aside answered (%q, nil): no key stored, for a key that exists", midKey)
	}
	if !errors.Is(midErr, ErrUnreadableCredentials) || !strings.Contains(midErr.Error(), filepath.Join(filepath.Dir(blobPathFor(dir)), ".legacy-")) {
		t.Errorf("a read while the key was moved aside = %v; want ErrUnreadableCredentials naming the .legacy-* copy", midErr)
	}
	if got, err := GetAPIKey(); err != nil || got != fresh {
		t.Fatalf("after the cleanup the store holds (%q, %v), want the published key put back", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(blobPathFor(dir)), ".legacy-*")); len(left) != 0 {
		t.Errorf("the cleanup left %v behind", left)
	}
}

// TestLoadAPIKey_WaitsOutALegacyCleanupInFlight: the other half. A reader
// that finds the live blob missing while a .legacy-* copy exists backs off
// and re-reads, so a cleanup that puts the key back within the bounded retry
// is invisible to it.
func TestLoadAPIKey_WaitsOutALegacyCleanupInFlight(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)

	const want = "mio_sk_live_moved_aside_for_a_moment"
	if err := SetAPIKey(want); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	live := blobPathFor(dir)
	hold, err := os.MkdirTemp(filepath.Dir(live), ".legacy-")
	if err != nil {
		t.Fatalf("hold dir: %v", err)
	}
	held := filepath.Join(hold, filepath.Base(live))
	if err := os.Rename(live, held); err != nil {
		t.Fatalf("move the blob aside: %v", err)
	}

	waits := 0
	orig := unreadableRetryWait
	unreadableRetryWait = func() {
		waits++
		if waits == 1 {
			if err := os.Rename(held, live); err != nil {
				t.Errorf("put the blob back: %v", err)
			}
		}
	}
	t.Cleanup(func() { unreadableRetryWait = orig })

	got, _, err := LoadAPIKey()
	if err != nil || got != want {
		t.Fatalf("LoadAPIKey while a cleanup had the blob moved aside = (%q, %v), want %q: the read must back off and re-read", got, err, want)
	}
	if waits == 0 {
		t.Fatal("LoadAPIKey never backed off, so this test did not exercise the window")
	}
}

// TestGetAPIKey_LegacyCleanupPutsBackAKeyRewrittenInPlace: a mio from v0.22.0
// or earlier writes the blob IN PLACE, so a key it logs in with between the
// cleanup recognising the legacy blob and removing it keeps the recognised
// file's identity. The cleanup must then detach it, find it no longer decrypts
// as legacy, and put it back. If putting it back fails, the detached copy is
// the only copy of that key: it must be kept, and the error must say where.
func TestGetAPIKey_LegacyCleanupPutsBackAKeyRewrittenInPlace(t *testing.T) {
	for _, linkFails := range []bool{false, true} {
		name := "put back"
		if linkFails {
			name = "put back fails"
		}
		t.Run(name, func(t *testing.T) {
			dir := withXDG(t)
			withFileBackendOnly(t)
			noRetryWait(t)

			legacyRing, err := openKeyringWithPassword(legacyFilePassphrase, "")
			if err != nil {
				t.Fatalf("open legacy ring: %v", err)
			}
			if err := legacyRing.Set(legacyKeyringItem("mio_sk_old_legacy_key")); err != nil {
				t.Fatalf("seed legacy blob: %v", err)
			}

			const fresh = "mio_sk_live_written_in_place_by_an_older_mio"
			var installKey string
			rewritten := false
			origBefore := beforeLegacyRemoval
			beforeLegacyRemoval = func() {
				if rewritten {
					return
				}
				rewritten = true
				key, err := loadOrCreateFileKey()
				if err != nil {
					t.Errorf("file key: %v", err)
					return
				}
				installKey = key
				ring, err := openKeyringWithPassword(key, "")
				if err != nil {
					t.Errorf("open ring: %v", err)
					return
				}
				// The library's own Set: os.WriteFile over the live path, same inode.
				if err := ring.Set(apiKeyItem(fresh)); err != nil {
					t.Errorf("in-place write: %v", err)
				}
			}
			origLink := linkBlob
			if linkFails {
				linkBlob = func(oldname, newname string) error {
					return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EIO}
				}
			}
			t.Cleanup(func() { beforeLegacyRemoval, linkBlob = origBefore, origLink })

			_, readErr := GetAPIKey()
			if !rewritten {
				t.Fatal("the removal hook never ran, so this test did not exercise the race")
			}
			held, _ := filepath.Glob(filepath.Join(filepath.Dir(blobPathFor(dir)), ".legacy-*", keyringKeyName))

			if !linkFails {
				if got, err := GetAPIKey(); err != nil || got != fresh {
					t.Fatalf("after the legacy cleanup the store holds (%q, %v), want the key written in place; first read: %v", got, err, readErr)
				}
				if len(held) != 0 {
					t.Errorf("the cleanup left detached copies behind: %v", held)
				}
				return
			}

			if len(held) != 1 {
				t.Fatalf("the legacy cleanup could not put back a key that is not legacy, and left %d copies of it (%v); read error: %v. "+
					"The detached copy is the only one, so deleting it loses the key", len(held), held, readErr)
			}
			if readErr == nil || !strings.Contains(readErr.Error(), held[0]) {
				t.Errorf("the read error must say where the key was kept (%s); got %v", held[0], readErr)
			}
			ring, err := keyring.Open(keyringConfig(keyring.FileBackend, filepath.Dir(held[0]),
				func(string) (string, error) { return installKey, nil }))
			if err != nil {
				t.Fatalf("open kept copy: %v", err)
			}
			if item, err := ring.Get(keyringKeyName); err != nil || string(item.Data) != fresh {
				t.Fatalf("the kept copy holds (%q, %v), want the key written in place", item.Data, err)
			}
		})
	}
}

// TestGetAPIKey_LegacyBlobIsRemoved: the other half of the legacy contract. A
// v0.1 blob is encrypted under a passphrase published in this repo's history,
// so it is effectively plaintext on disk; recognising one must take it off the
// disk, and leave no copy behind in the private directory it is detached into.
func TestGetAPIKey_LegacyBlobIsRemoved(t *testing.T) {
	dir := withXDG(t)
	withFileBackendOnly(t)
	noRetryWait(t)

	legacyRing, err := openKeyringWithPassword(legacyFilePassphrase, "")
	if err != nil {
		t.Fatalf("open legacy ring: %v", err)
	}
	if err := legacyRing.Set(legacyKeyringItem("mio_sk_old_legacy_key")); err != nil {
		t.Fatalf("seed legacy blob: %v", err)
	}

	if _, err := GetAPIKey(); !errors.Is(err, ErrLegacyCredentials) {
		t.Fatalf("GetAPIKey = %v, want ErrLegacyCredentials", err)
	}
	keyringDir := filepath.Dir(blobPathFor(dir))
	entries, err := os.ReadDir(keyringDir)
	if err != nil {
		t.Fatalf("read keyring dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("after recognising a legacy blob the keyring dir still holds %v; the legacy blob must be removed and no detached copy left behind", names)
	}
}

// TestSetAPIKey_StagesBesideTheBlob: the replacement blob must be staged in
// the SAME directory as the live one, the only place a rename is guaranteed
// not to cross a filesystem (the keyring dir can be a mount point or a
// symlink to another volume). A cross-device mount cannot be built in a unit
// test, so this uses a proxy that tells the two layouts apart just as well:
// the keyring dir is writable but its parent is not.
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

// TestGetAPIKey_KeyFileFilesystemErrorIsNotACredentialVerdict: a key file that
// exists but cannot be READ (EACCES, EIO) is an environment failure, the same
// class as an unreadable blob, and takes the same generic path (exit 1). Only
// a key file that is missing or invalid says something about the credential
// itself (exit 3, re-authenticate). The injected error stands in for EACCES,
// which cannot be produced on the key file alone without root.
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

// TestDeleteAPIKey_NothingStoredIsNotAnError: DeleteAPIKey (`mio logout`) is
// documented as "a missing key is not an error", but the file backend answers
// a missing blob with a raw os.ErrNotExist rather than keyring.ErrKeyNotFound,
// so `mio logout` with nothing stored used to exit 1.
func TestDeleteAPIKey_NothingStoredIsNotAnError(t *testing.T) {
	withXDG(t)
	withFileBackendOnly(t)
	if err := DeleteAPIKey(); err != nil {
		t.Fatalf("DeleteAPIKey with nothing stored = %v, want nil", err)
	}
}
