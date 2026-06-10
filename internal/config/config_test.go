package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99designs/keyring"
)

// withXDG points config storage at a temp dir for the duration of the test and
// clears the auth env vars so resolution is deterministic.
func withXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPIBase, "")
	return dir
}

// withFileBackendOnly restricts keyring operations to the file backend for the
// duration of the test, preventing OS keychain interaction and making keyring
// tests deterministic across platforms.
func withFileBackendOnly(t *testing.T) {
	t.Helper()
	orig := keyringAllowedBackends
	keyringAllowedBackends = []keyring.BackendType{keyring.FileBackend}
	t.Cleanup(func() { keyringAllowedBackends = orig })
}

func TestConfig_SaveLoadRoundTrip(t *testing.T) {
	withXDG(t)

	cfg := &Config{CurrentTeam: "team_1", CurrentHub: "hub_2", APIBase: "https://staging.example"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.CurrentTeam != "team_1" || loaded.CurrentHub != "hub_2" || loaded.APIBase != "https://staging.example" {
		t.Errorf("round-trip mismatch: %#v", loaded)
	}
}

func TestConfig_SaveUses0600(t *testing.T) {
	dir := withXDG(t)
	cfg := &Config{CurrentTeam: "t"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "mio", "config.toml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perms = %o, want 600", perm)
	}
}

func TestResolve_APIKeyPrecedence_FlagWins(t *testing.T) {
	withXDG(t)
	t.Setenv(EnvAPIKey, "env_key")

	cfg, _ := Load()
	r, err := cfg.Resolve(Overrides{APIKey: "flag_key"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.APIKey != "flag_key" {
		t.Errorf("APIKey = %q, want flag_key (flag beats env)", r.APIKey)
	}
}

func TestResolve_APIKeyPrecedence_EnvBeatsConfig(t *testing.T) {
	withXDG(t)
	t.Setenv(EnvAPIKey, "env_key")

	cfg, _ := Load()
	r, err := cfg.Resolve(Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.APIKey != "env_key" {
		t.Errorf("APIKey = %q, want env_key", r.APIKey)
	}
}

func TestResolve_APIBaseDefault(t *testing.T) {
	withXDG(t)
	cfg, _ := Load()
	r, err := cfg.Resolve(Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	const wantDefaultAPIBase = "https://api.member.dev"
	if r.APIBase != wantDefaultAPIBase {
		t.Errorf("APIBase = %q, want default %q", r.APIBase, wantDefaultAPIBase)
	}
}

func TestResolve_TeamFlagBeatsConfig(t *testing.T) {
	withXDG(t)
	cfg := &Config{CurrentTeam: "config_team"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := Load()
	r, err := loaded.Resolve(Overrides{TeamID: "flag_team"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.TeamID != "flag_team" {
		t.Errorf("TeamID = %q, want flag_team", r.TeamID)
	}
}

func TestResolve_ProfileOverridesTopLevel(t *testing.T) {
	withXDG(t)
	cfg := &Config{
		CurrentTeam: "default_team",
		Profiles: map[string]Profile{
			"prod": {CurrentTeam: "prod_team", APIBase: "https://prod.example"},
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := Load()
	r, err := loaded.Resolve(Overrides{Profile: "prod"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.TeamID != "prod_team" {
		t.Errorf("TeamID = %q, want prod_team (profile)", r.TeamID)
	}
	if r.APIBase != "https://prod.example" {
		t.Errorf("APIBase = %q, want profile base", r.APIBase)
	}
}

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	withXDG(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if cfg.CurrentTeam != "" {
		t.Errorf("expected empty config, got %#v", cfg)
	}
}

// ---- File-backend key management tests ----------------------------------------

// TestFileKey_GeneratedOnFirstUse verifies that loadOrCreateFileKey creates a
// key file when none exists and returns a non-empty key.
func TestFileKey_GeneratedOnFirstUse(t *testing.T) {
	dir := withXDG(t)
	key, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("loadOrCreateFileKey: %v", err)
	}
	if len(key) == 0 {
		t.Fatal("expected non-empty key")
	}
	// Key file must exist on disk.
	keyPath := filepath.Join(dir, "mio", fileKeyName)
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("key file not created at %s: %v", keyPath, err)
	}
}

// TestFileKey_StableAcrossCalls verifies the key is reloaded unchanged on the
// second call (idempotent).
func TestFileKey_StableAcrossCalls(t *testing.T) {
	withXDG(t)
	k1, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	k2, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if k1 != k2 {
		t.Errorf("key changed between calls: %q vs %q", k1, k2)
	}
}

// TestFileKey_UniqueAcrossInstalls verifies that two fresh config dirs get
// different keys (i.e., no hardcoded passphrase).
func TestFileKey_UniqueAcrossInstalls(t *testing.T) {
	// Install A.
	withXDG(t)
	kA, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("install A: %v", err)
	}

	// Install B — new temp dir, new key.
	dirB := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dirB)
	kB, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("install B: %v", err)
	}

	if kA == kB {
		t.Errorf("two fresh installs got the same key %q — passphrase is hardcoded", kA)
	}
}

// TestFileKey_Permissions verifies the key file is stored 0600.
func TestFileKey_Permissions(t *testing.T) {
	dir := withXDG(t)
	if _, err := loadOrCreateFileKey(); err != nil {
		t.Fatalf("loadOrCreateFileKey: %v", err)
	}
	keyPath := filepath.Join(dir, "mio", fileKeyName)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file perms = %04o, want 0600", perm)
	}
}

// TestFileKey_PermissionDriftInvalidates verifies that a key file whose
// permissions have drifted beyond 0600 is treated as potentially compromised:
// the key file is removed and loadOrCreateFileKey regenerates with a NEW key
// on the next call (rather than silently repairing and reusing the old key).
func TestFileKey_PermissionDriftInvalidates(t *testing.T) {
	dir := withXDG(t)
	// Generate the key initially.
	origKey, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("initial loadOrCreateFileKey: %v", err)
	}
	// Widen permissions to simulate drift.
	keyPath := filepath.Join(dir, "mio", fileKeyName)
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// The validation must reject the drifted key and regenerate.
	newKey, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("loadOrCreateFileKey after drift: %v", err)
	}
	// A fresh key must have been generated (different from the old one).
	if newKey == origKey {
		t.Error("expected a new key after permission drift, but got the same key — drift was silently repaired instead of invalidated")
	}
	// New key file must be at 0600.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("new key file perms = %04o, want 0600", perm)
	}
}

// TestFileKey_SymlinkRejected verifies that a symlink at the key path is
// detected and regenerated as a regular file.
func TestFileKey_SymlinkRejected(t *testing.T) {
	dir := withXDG(t)
	keyPath := filepath.Join(dir, "mio", fileKeyName)

	// Create the mio config dir.
	if err := os.MkdirAll(filepath.Join(dir, "mio"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Put a symlink at the key path.
	target := filepath.Join(dir, "other-key.txt")
	if err := os.WriteFile(target, []byte("deadbeef"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, keyPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// loadOrCreateFileKey must detect the symlink, remove it, and generate a
	// real regular-file key.
	key, err := loadOrCreateFileKey()
	if err != nil {
		t.Fatalf("loadOrCreateFileKey with symlink: %v", err)
	}
	if !isValidFileKey(key) {
		t.Errorf("regenerated key %q is invalid", key)
	}
	// The path should now be a regular file, not a symlink.
	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatalf("lstat after symlink rejection: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("key path is still a symlink after rejection")
	}
}

// TestKeyring_SetGetRoundTrip verifies that SetAPIKey+GetAPIKey round-trip via
// the file backend (no OS keychain in the test environment).
func TestKeyring_SetGetRoundTrip(t *testing.T) {
	withXDG(t)
	withFileBackendOnly(t)
	const want = "mio_sk_test_ROUNDTRIP_abc123"

	if err := SetAPIKey(want); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	got, err := GetAPIKey()
	if err != nil {
		t.Fatalf("GetAPIKey: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestKeyring_DeleteRemovesKey verifies DeleteAPIKey clears the stored key and a
// subsequent GetAPIKey returns "".
func TestKeyring_DeleteRemovesKey(t *testing.T) {
	withXDG(t)
	withFileBackendOnly(t)

	if err := SetAPIKey("some_key"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := DeleteAPIKey(); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	got, err := GetAPIKey()
	if err != nil {
		t.Fatalf("GetAPIKey after delete: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty key after delete, got %q", got)
	}
}

// TestKeyring_LegacyHardcodedPassphraseInvalidated verifies that a keyring file
// encrypted with the old hardcoded passphrase produces an error that tells the
// user to re-login, and that the stale file is removed so the next login works.
func TestKeyring_LegacyHardcodedPassphraseInvalidated(t *testing.T) {
	withXDG(t)
	// Force file backend only so this test is deterministic (no OS keychain).
	withFileBackendOnly(t)

	// Simulate a legacy installation: write a keyring file encrypted with the
	// old hardcoded passphrase "mio-cli" directly via the keyring library.
	// openKeyringWithPassword uses file-backend-only with cfgDirOverride="".
	legacyRing, err := openKeyringWithPassword("mio-cli", "")
	if err != nil {
		t.Fatalf("open legacy ring: %v", err)
	}
	if err := legacyRing.Set(legacyKeyringItem("mio_sk_old_legacy_key")); err != nil {
		t.Fatalf("set legacy key: %v", err)
	}

	// Now call GetAPIKey — it must not silently succeed (that would mean we
	// decrypted with the hardcoded passphrase again), and must return an error
	// whose message tells the user to re-login.
	//
	// After this call the stale file should be deleted so a subsequent SetAPIKey
	// works without error.
	_, err = GetAPIKey()
	if err == nil {
		t.Fatal("expected an error for legacy-encrypted keyring, got nil — hardcoded passphrase still in use!")
	}
	if !strings.Contains(err.Error(), "re-login") && !strings.Contains(err.Error(), "login") {
		t.Errorf("error message should mention re-login, got: %v", err)
	}

	// After invalidation, writing and reading a fresh key must succeed.
	if err := SetAPIKey("mio_sk_fresh_key"); err != nil {
		t.Fatalf("SetAPIKey after invalidation: %v", err)
	}
	got, err := GetAPIKey()
	if err != nil {
		t.Fatalf("GetAPIKey after fresh set: %v", err)
	}
	if got != "mio_sk_fresh_key" {
		t.Errorf("got %q, want mio_sk_fresh_key", got)
	}
}
