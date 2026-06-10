// Package config owns the mio CLI's persistent state: the TOML config file at
// $XDG_CONFIG_HOME/mio/config.toml (default ~/.config/mio/config.toml) and the
// OS keychain entry that stores the API key. It also implements the canonical
// auth-resolution order used by every command.
//
// The config file holds non-secret context (current team/hub, api base, named
// profiles). The API key is a secret and lives in the OS keychain, with a
// plaintext-file fallback when no keychain backend is available (headless CI,
// containers). Secrets are NEVER written to the TOML file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/99designs/keyring"
	"github.com/BurntSushi/toml"
)

const (
	// keyringService is the OS keychain service name under which the API key
	// is stored. Stable across versions so `mio login` once persists.
	keyringService = "mio-cli"
	// keyringKeyName is the item label within the service.
	keyringKeyName = "api-key"
	// EnvAPIKey is the environment variable holding the bearer key (primary
	// path for agents and CI).
	EnvAPIKey = "MIO_API_KEY"
	// EnvAPIBase overrides the API base URL.
	EnvAPIBase = "MIO_API_BASE_URL"
	// DefaultAPIBase is the fallback API base (production). Overridable via the
	// MIO_API_BASE_URL env var, the --api-base flag, or `mio config set api-base`.
	DefaultAPIBase = "https://api.member.dev"

	// fileKeyName is the filename (inside the config dir) that holds the
	// per-install random passphrase for the file-backend keyring fallback.
	fileKeyName = "file-keyring.key"
)

// Profile is a named set of context overrides, mirroring Stripe's profiles.
// The active profile is selected by the --profile flag or the default profile.
type Profile struct {
	CurrentTeam string `toml:"current_team,omitempty"`
	CurrentHub  string `toml:"current_hub,omitempty"`
	APIBase     string `toml:"api_base,omitempty"`
}

// Config is the on-disk shape of config.toml. Top-level fields form the default
// profile; named profiles live under [profiles.<name>].
type Config struct {
	CurrentTeam string             `toml:"current_team,omitempty"`
	CurrentHub  string             `toml:"current_hub,omitempty"`
	APIBase     string             `toml:"api_base,omitempty"`
	Profiles    map[string]Profile `toml:"profiles,omitempty"`

	// path is where this config was loaded from / will be saved to. Not
	// serialized.
	path string `toml:"-"`
}

// Resolved is the fully-resolved runtime context a command needs: which key to
// present, where to send it, and the active team/hub scope.
type Resolved struct {
	APIKey  string
	APIBase string
	TeamID  string
	HubID   string
}

// Path returns the absolute path of the config file, honouring XDG_CONFIG_HOME
// and falling back to ~/.config/mio/config.toml.
func Path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "mio", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "mio", "config.toml"), nil
}

// Load reads the config file. A missing file is not an error — it returns an
// empty Config bound to the default path so a later Save creates it.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Profiles: map[string]Profile{}, path: path}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	cfg.path = path
	return cfg, nil
}

// Save writes the config back to disk, creating the parent directory (0700) and
// writing the file 0600 since it may contain context an operator considers
// sensitive. Secrets are never written here.
func (c *Config) Save() error {
	if c.path == "" {
		p, err := Path()
		if err != nil {
			return err
		}
		c.path = p
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open config for write: %w", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return nil
}

// ---- Keychain helpers -------------------------------------------------------

// ErrLegacyCredentials is returned by GetAPIKey when the stored credential blob
// was encrypted with the old hardcoded passphrase.  Callers (including the
// login flow) should treat it as "no stored key" and proceed to the login
// prompt rather than aborting.  Root command wiring maps this to ExitAuth.
var ErrLegacyCredentials = errors.New("stored credentials use legacy encryption; please run `mio login` to re-login")

// loadOrCreateFileKey returns the per-install random passphrase used for the
// file-backend keyring fallback.  On the first call it generates 32 random
// bytes, hex-encodes them, and writes the result to
// <config-dir>/file-keyring.key at mode 0600.  Subsequent calls reload that
// file.  Two different config dirs therefore always produce different keys,
// which means a copied keyring blob cannot be decrypted with source knowledge
// alone.
//
// Key generation uses write-to-temp + O_CREATE|O_EXCL link to final path, so
// concurrent first-run processes are safe: the loser reads the winner's key.
func loadOrCreateFileKey() (string, error) {
	cfgPath, err := Path()
	if err != nil {
		return "", err
	}
	keyPath := filepath.Join(filepath.Dir(cfgPath), fileKeyName)

	// Try to read an existing key first.  Any validation failure means we must
	// regenerate (the old key is removed inside readAndValidateFileKey).
	if key, err := readAndValidateFileKey(keyPath); err == nil {
		return key, nil
	}
	// Fall through to create/regenerate (the key either doesn't exist, is
	// corrupt, has bad permissions, or was a symlink/non-regular file).

	// Ensure the parent directory exists (config dir, mode 0700).
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", fmt.Errorf("create config dir for key file: %w", err)
	}

	// Generate a fresh 32-byte random key.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate file keyring key: %w", err)
	}
	key := hex.EncodeToString(raw)

	// Write to a temp file first so the reader never sees a partial write.
	tmp, err := os.CreateTemp(filepath.Dir(keyPath), ".file-keyring-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create tmp file keyring key: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName) // no-op if link succeeded
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("chmod tmp file keyring key: %w", err)
	}
	if _, err := fmt.Fprint(tmp, key); err != nil {
		return "", fmt.Errorf("write tmp file keyring key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close tmp file keyring key: %w", err)
	}

	// Hard-link tmp → final path with O_EXCL semantics via os.Link (POSIX
	// atomic, create-if-absent).  If the link fails because the target already
	// exists (concurrent winner), discard our key and read the winner's.
	linkErr := os.Link(tmpName, keyPath)
	switch {
	case linkErr == nil:
		// We are the winner: our key is now at keyPath.  Re-read it so we
		// go through the same validation every other call does.
		return readAndValidateFileKeyRetry(keyPath)
	case errors.Is(linkErr, os.ErrExist):
		// Another process published first; read their key.
		return readAndValidateFileKeyRetry(keyPath)
	default:
		return "", fmt.Errorf("publish file keyring key: %w", linkErr)
	}
}

// readAndValidateFileKey reads keyPath and verifies:
//   - it exists and is a regular file (not a symlink, FIFO, device, or dir)
//   - its mode is exactly 0600 (permission drift means potential exposure;
//     we invalidate rather than silently repair to avoid using a key that
//     may have been read by another user)
//   - it contains exactly 64 lowercase hex characters
//
// On any validation failure the offending path is removed (best-effort) and
// an error is returned so loadOrCreateFileKey regenerates.
func readAndValidateFileKey(keyPath string) (string, error) {
	// Lstat so we see the symlink itself, not its target.
	info, err := os.Lstat(keyPath)
	if err != nil {
		return "", err
	}
	// Reject anything that is not a plain regular file.
	if !info.Mode().IsRegular() {
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("file keyring key is not a regular file (mode %v); regenerating", info.Mode())
	}
	// Permission drift means the key may have been readable by others.
	// Treat it as compromised: remove it and the encrypted credential blob so
	// the user must re-login with a fresh key.
	if perm := info.Mode().Perm(); perm != 0o600 {
		_ = os.Remove(keyPath)
		// Ignore blob-deletion errors here; loadOrCreateFileKey will regenerate
		// the key, and the next GetAPIKey / Resolve call will surface any
		// remaining stale-blob issue via the normal decryption-error path.
		if derr := deleteKeyringFile(); derr != nil {
			return "", fmt.Errorf(
				"file keyring key had permissions %04o (expected 0600); key invalidated (blob cleanup failed: %v) — please run `mio login` to re-login",
				perm, derr)
		}
		return "", fmt.Errorf(
			"file keyring key had permissions %04o (expected 0600); key and credentials invalidated for security — please run `mio login` to re-login",
			perm)
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if !isValidFileKey(key) {
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("file keyring key content is invalid; regenerating")
	}
	return key, nil
}

// readAndValidateFileKeyRetry retries readAndValidateFileKey with brief waits
// for the case where the file was just created by a concurrent runner and may
// still be visible after the hard-link.
func readAndValidateFileKeyRetry(keyPath string) (string, error) {
	const (
		retries = 5
		wait    = 10 * time.Millisecond
	)
	var lastErr error
	for i := range retries {
		if key, err := readAndValidateFileKey(keyPath); err == nil {
			return key, nil
		} else {
			lastErr = err
		}
		if i < retries-1 {
			time.Sleep(wait)
		}
	}
	return "", fmt.Errorf("file keyring key not readable after retries: %w", lastErr)
}

// isValidFileKey returns true if key is a 64-character lowercase hex string
// (the expected encoding of 32 random bytes).
func isValidFileKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, c := range key {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// keyringAllowedBackends is the ordered list of backends tried by openKeyring.
// It is a package-level variable so tests can force file-only mode to avoid
// OS keychain interaction.
var keyringAllowedBackends = []keyring.BackendType{
	keyring.KeychainBackend,
	keyring.SecretServiceBackend,
	keyring.WinCredBackend,
	keyring.KWalletBackend,
	keyring.FileBackend,
}

// openKeyring opens the OS keychain for the mio-cli service, falling back to an
// encrypted-at-rest file backend under the config dir when no native keychain
// is available (Linux headless, containers, CI).
//
// The file-backend passphrase is loaded lazily inside FilePasswordFunc so that
// OS keychain users (macOS Keychain, Windows Credential Manager, Linux Secret
// Service) never pay the cost of file-key creation or validation.
// loadOrCreateFileKey is only called if the keyring library actually selects
// the file backend.
func openKeyring() (keyring.Keyring, error) {
	cfgPath, err := Path()
	if err != nil {
		return nil, err
	}
	fileDir := filepath.Join(filepath.Dir(cfgPath), "keyring")
	return keyring.Open(keyring.Config{
		ServiceName:     keyringService,
		AllowedBackends: keyringAllowedBackends,
		FileDir:         fileDir,
		// FilePasswordFunc is invoked lazily, only when the file backend is
		// actually selected by the keyring library.
		FilePasswordFunc: func(string) (string, error) { return loadOrCreateFileKey() },
	})
}

// openKeyringWithPassword opens a keyring using the given password and the
// file backend only.  cfgDirOverride sets FileDir to
// <cfgDirOverride>/mio/keyring; pass "" to derive from Path().
// This is used in tests and for legacy-credential seeding.
func openKeyringWithPassword(password, cfgDirOverride string) (keyring.Keyring, error) {
	var fileDir string
	if cfgDirOverride != "" {
		fileDir = filepath.Join(cfgDirOverride, "mio", "keyring")
	} else {
		cfgPath, err := Path()
		if err != nil {
			return nil, err
		}
		fileDir = filepath.Join(filepath.Dir(cfgPath), "keyring")
	}
	return keyring.Open(keyring.Config{
		ServiceName:      keyringService,
		AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
		FileDir:          fileDir,
		FilePasswordFunc: func(string) (string, error) { return password, nil },
	})
}

// legacyKeyringItem returns a keyring.Item suitable for seeding a legacy
// (hardcoded-passphrase) keyring file in tests.
func legacyKeyringItem(apiKey string) keyring.Item {
	return keyring.Item{
		Key:         keyringKeyName,
		Data:        []byte(apiKey),
		Label:       "mio CLI API key",
		Description: "API key used by the mio CLI",
	}
}

// GetAPIKey returns the stored API key, or "" (no error) if none is stored.
//
// If the keyring file was encrypted with the old hardcoded passphrase (a
// legacy install), decryption will fail.  In that case GetAPIKey deletes the
// stale file so the next `mio login` can write a fresh entry, and returns
// ErrLegacyCredentials.  Callers in the login flow detect this sentinel and
// continue to the interactive prompt rather than aborting; other callers
// (root command wiring) map it to ExitAuth.
func GetAPIKey() (string, error) {
	ring, err := openKeyring()
	if err != nil {
		return "", err
	}
	item, err := ring.Get(keyringKeyName)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return "", nil
	}
	if err != nil {
		// A decryption failure most likely means the file was encrypted with the
		// old hardcoded passphrase.  Remove it so the next login can start fresh,
		// then return the typed sentinel so callers can handle it appropriately.
		if isDecryptionError(err) {
			if derr := deleteKeyringFile(); derr != nil {
				// Deletion failed: the stale blob remains.  Return a combined
				// error so the operator can see why, still typed as legacy so
				// callers map to ExitAuth.
				return "", fmt.Errorf("%w (cleanup failed: %v)", ErrLegacyCredentials, derr)
			}
			return "", ErrLegacyCredentials
		}
		return "", fmt.Errorf("read key from keychain: %w", err)
	}
	return string(item.Data), nil
}

// isDecryptionError returns true when err looks like a JOSE / crypto decryption
// failure that the file backend returns when the passphrase is wrong.
func isDecryptionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// The jose2go library used by the file backend returns descriptive strings
	// rather than sentinel errors.  We match the common substrings here.
	return strings.Contains(msg, "decrypt") ||
		strings.Contains(msg, "cipher") ||
		strings.Contains(msg, "HMAC") ||
		strings.Contains(msg, "integrity") ||
		strings.Contains(msg, "authentication tag") ||
		strings.Contains(msg, "invalid compact") ||
		strings.Contains(msg, "unwrap") ||
		strings.Contains(msg, "pbes")
}

// deleteKeyringFile removes the encrypted keyring file so it can be
// re-created on the next `mio login`.
func deleteKeyringFile() error {
	cfgPath, err := Path()
	if err != nil {
		return err
	}
	fileDir := filepath.Join(filepath.Dir(cfgPath), "keyring")
	target := filepath.Join(fileDir, keyringKeyName)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SetAPIKey persists the API key to the keychain.
func SetAPIKey(key string) error {
	ring, err := openKeyring()
	if err != nil {
		return err
	}
	if err := ring.Set(keyring.Item{
		Key:         keyringKeyName,
		Data:        []byte(key),
		Label:       "mio CLI API key",
		Description: "API key used by the mio CLI",
	}); err != nil {
		return fmt.Errorf("write key to keychain: %w", err)
	}
	return nil
}

// DeleteAPIKey removes the stored API key. A missing key is not an error.
func DeleteAPIKey() error {
	ring, err := openKeyring()
	if err != nil {
		return err
	}
	if err := ring.Remove(keyringKeyName); err != nil && !errors.Is(err, keyring.ErrKeyNotFound) {
		return fmt.Errorf("delete key from keychain: %w", err)
	}
	return nil
}

// ---- Resolution -------------------------------------------------------------

// Overrides carries the per-invocation flag values that take precedence over
// env and config. Empty strings mean "not set on the command line".
type Overrides struct {
	APIKey  string
	APIBase string
	TeamID  string
	HubID   string
	Profile string
}

// Resolve computes the effective {apiKey, apiBase, teamID, hubID} from the
// precedence chain:
//
//	api key : --api-key flag  >  MIO_API_KEY env  >  keychain
//	api base: --api-base flag >  MIO_API_BASE_URL >  profile/config  >  default
//	team/hub: --team/--hub    >  profile/config
//
// A missing API key is NOT an error here — commands that require auth check for
// an empty key and emit the auth exit code themselves, so read-only/login flows
// can proceed.
func (c *Config) Resolve(o Overrides) (Resolved, error) {
	prof := c.profile(o.Profile)

	// API key: flag > env > keychain.
	apiKey := o.APIKey
	if apiKey == "" {
		apiKey = os.Getenv(EnvAPIKey)
	}
	if apiKey == "" {
		stored, err := GetAPIKey()
		switch {
		case err == nil:
			apiKey = stored
		case errors.Is(err, ErrLegacyCredentials):
			// The stale blob has been deleted; treat as no key stored and surface
			// the sentinel so callers (root wiring, login) can react appropriately.
			return Resolved{
				APIBase: firstNonEmpty(o.APIBase, os.Getenv(EnvAPIBase), prof.APIBase, c.APIBase, DefaultAPIBase),
				TeamID:  firstNonEmpty(o.TeamID, prof.CurrentTeam, c.CurrentTeam),
				HubID:   firstNonEmpty(o.HubID, prof.CurrentHub, c.CurrentHub),
			}, ErrLegacyCredentials
		default:
			return Resolved{}, err
		}
	}

	// API base: flag > env > profile/config > default.
	apiBase := o.APIBase
	if apiBase == "" {
		apiBase = os.Getenv(EnvAPIBase)
	}
	if apiBase == "" {
		apiBase = firstNonEmpty(prof.APIBase, c.APIBase)
	}
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}

	team := firstNonEmpty(o.TeamID, prof.CurrentTeam, c.CurrentTeam)
	hub := firstNonEmpty(o.HubID, prof.CurrentHub, c.CurrentHub)

	return Resolved{APIKey: apiKey, APIBase: apiBase, TeamID: team, HubID: hub}, nil
}

// profile returns the named profile merged conceptually with defaults. An
// unknown or empty name yields a zero Profile (callers fall back to top-level
// config fields).
func (c *Config) profile(name string) Profile {
	if name == "" {
		return Profile{}
	}
	return c.Profiles[name]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
