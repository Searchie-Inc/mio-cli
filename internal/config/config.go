// Package config owns the mio CLI's persistent state: the TOML config file at
// $XDG_CONFIG_HOME/mio/config.toml (default ~/.config/mio/config.toml) and the
// credential-store entry that holds the API key. It also implements the canonical
// auth-resolution order used by every command.
//
// The config file holds non-secret context (current team/hub, api base, named
// profiles). The API key is a secret and lives in a credential store: an OS
// store where this build has one it can open (macOS Keychain in cgo builds
// only, Secret Service/KWallet on a Linux desktop session, Windows Credential
// Manager), otherwise an encrypted file under the config dir. The release
// binaries are built with CGO_ENABLED=0, so on macOS they ALWAYS use the file
// (MIO-2995). Secrets are NEVER written to the TOML file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/99designs/keyring"
	"github.com/BurntSushi/toml"
)

const (
	// keyringService is the credential-store service name under which the API key
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
	// MIO_API_BASE_URL env var, the --api-base flag, or `mio config set api_base`.
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
	// Anonymous records that this resolution was DELIBERATELY unauthenticated
	// (--anonymous). It is an input mode carried through to the output because
	// the command layer must be able to tell "no key was found" apart from "no
	// key was wanted" — an empty APIKey looks identical in both cases.
	//
	// Without it the key-required precondition (cmdContext.requireAuth) fires
	// before anything consults --anonymous, so the flag could never actually send
	// an unauthenticated request: every `--anonymous` invocation died with
	// "no API key found" and never reached the HTTP client (MIO-2694).
	Anonymous bool
	// KeyStore is the credential store Resolve read the stored key from, or
	// nil when it never looked (the key came from --api-key or MIO_API_KEY, or
	// --anonymous skipped the lookup). It is what lets an empty APIKey say
	// WHERE no key was found, and `whoami` say where one was (MIO-2995).
	KeyStore *Store
}

// Path returns the absolute path of the config file, honouring XDG_CONFIG_HOME
// and falling back to ~/.config/mio/config.toml.
func Path() (string, error) {
	dir, _, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// configDir returns the mio config directory (<base>/mio) and the name of the
// environment variable that chose <base>: XDG_CONFIG_HOME when it is set,
// otherwise the home directory's variable (<home>/.config). The encrypted
// file keyring lives under this directory too, so two processes that disagree
// on either variable read two different stores.
func configDir() (dir, source string, err error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "mio"), "XDG_CONFIG_HOME", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "mio"), homeEnvName(), nil
}

// homeEnvName is the variable os.UserHomeDir reads on this platform.
func homeEnvName() string {
	switch runtime.GOOS {
	case "windows":
		return "USERPROFILE"
	case "plan9":
		return "home"
	default:
		return "HOME"
	}
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

// ErrUnreadableCredentials is returned when the file keyring's blob exists but
// does not decode with this install's passphrase and is not a legacy blob
// either: a write still in flight in another mio process, a blob truncated by
// a crash, or one encrypted under a key file that has since been regenerated.
//
// Unlike ErrLegacyCredentials the blob is LEFT IN PLACE. Nothing on the read
// path can tell a half-finished write from a dead blob, and deleting a
// credential another process is in the middle of writing destroys it for good
// (MIO-2995: a blob cut inside its auth tag used to be classified "legacy" and
// deleted). Root wiring maps it to ExitAuth; `mio login` and `mio register`
// treat it like "no stored key" and overwrite it.
var ErrUnreadableCredentials = errors.New("stored API key could not be read")

// StoredKeyUnusable reports whether err says the stored key exists but cannot
// be used, and re-authenticating (`mio login`, or MIO_API_KEY) is the fix.
func StoredKeyUnusable(err error) bool {
	return errors.Is(err, ErrLegacyCredentials) || errors.Is(err, ErrUnreadableCredentials)
}

// legacyFilePassphrase is the hardcoded passphrase v0.1 (2026-06-01 .. 06-09)
// encrypted the file keyring with, before MIO-794 moved to a per-install key.
// It is only ever used to RECOGNISE such a blob so it can be cleared.
const legacyFilePassphrase = "mio-cli"

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
// an error is returned. On the write path loadOrCreateFileKey then generates a
// fresh key; the read path (readFilePassword) never does, and reports the
// stored key unusable instead.
func readAndValidateFileKey(keyPath string) (string, error) {
	// Lstat so we see the symlink itself, not its target.
	info, err := os.Lstat(keyPath)
	if err != nil {
		return "", err
	}
	// Reject anything that is not a plain regular file.
	if !info.Mode().IsRegular() {
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("file keyring key is not a regular file (mode %v)", info.Mode())
	}
	// Permission drift means the key may have been readable by others.
	// Treat it as compromised: remove it and the encrypted credential blob so
	// the user must re-login with a fresh key.
	if perm := info.Mode().Perm(); perm != 0o600 {
		_ = os.Remove(keyPath)
		// If the blob cannot be deleted it stays behind, undecryptable without
		// the key removed above; a later read reports it unusable and the
		// next `mio login` replaces it.
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
		return "", fmt.Errorf("file keyring key content is invalid")
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
		key, err := readAndValidateFileKey(keyPath)
		if err == nil {
			return key, nil
		}
		lastErr = err
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
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// keyringAllowedBackends is the ordered list of backends tried by openKeyring.
// It is a package-level variable so tests can force file-only mode to avoid
// OS keychain interaction.
//
// Which of these a given binary can use is decided at BUILD time, not by this
// list: 99designs/keyring compiles its macOS Keychain backend only under
// `darwin && cgo`, and the release binaries are built with CGO_ENABLED=0
// (.goreleaser.yaml), so a released macOS mio has no Keychain backend at all
// and always lands on the file backend. On Linux the Secret Service and
// KWallet backends register only when the process can reach a D-Bus session
// bus, so the choice can differ between two shells of the same user.
var keyringAllowedBackends = []keyring.BackendType{
	keyring.KeychainBackend,
	keyring.SecretServiceBackend,
	keyring.WinCredBackend,
	keyring.KWalletBackend,
	keyring.FileBackend,
}

// UseFileBackendOnly restricts every credential-store operation in this
// process to the encrypted file backend under the config dir, and returns a
// func that restores the previous list. It exists for tests outside this
// package, which must never read or write the developer's real OS keychain or
// Secret Service; production code never calls it.
func UseFileBackendOnly() (restore func()) {
	orig := keyringAllowedBackends
	keyringAllowedBackends = []keyring.BackendType{keyring.FileBackend}
	return func() { keyringAllowedBackends = orig }
}

// KeyringBackends returns a copy of the backends a credential-store operation
// in this process may try, in order. It exists so a test package can prove
// UseFileBackendOnly is in force BEFORE any test stores or reads a key: on a
// CI runner with no OS store the file backend is picked either way, so the
// pin's absence is invisible to behaviour there and shows only here.
func KeyringBackends() []keyring.BackendType {
	return append([]keyring.BackendType(nil), keyringAllowedBackends...)
}

// Store identifies the credential store a stored-key operation used, so that
// an empty or failed lookup can say WHERE it looked (MIO-2995). Before it, the
// CLI reported every stored key as "keychain" whatever backend held it, which
// sent the MIO-2995 diagnosis after a macOS Keychain the release binary never
// touches.
type Store struct {
	// Backend is the keyring backend in use.
	Backend keyring.BackendType
	// Path is the encrypted blob's path. File backend only.
	Path string
	// DirSource names the environment variable that chose the config dir the
	// blob lives under: XDG_CONFIG_HOME, or the home variable (HOME) when
	// XDG_CONFIG_HOME is unset. File backend only.
	DirSource string
}

// Label is the short name `whoami` reports as key_source for a key read from
// this store. "keychain" keeps its historical spelling for the macOS Keychain.
func (s Store) Label() string {
	switch s.Backend {
	case keyring.FileBackend:
		return "file keyring (" + s.Path + ")"
	case keyring.KeychainBackend:
		return "keychain"
	case keyring.SecretServiceBackend:
		return "secret service"
	case keyring.KWalletBackend:
		return "kwallet"
	case keyring.WinCredBackend:
		return "wincred"
	default:
		return string(s.Backend)
	}
}

// Describe names the store in prose, for error messages.
func (s Store) Describe() string {
	switch s.Backend {
	case keyring.FileBackend:
		if s.DirSource == "XDG_CONFIG_HOME" {
			return fmt.Sprintf("the file keyring at %s (config dir from $XDG_CONFIG_HOME)", s.Path)
		}
		return fmt.Sprintf("the file keyring at %s (config dir from $%s; XDG_CONFIG_HOME is unset)", s.Path, s.DirSource)
	case keyring.KeychainBackend:
		return fmt.Sprintf("the macOS Keychain (service %q, account %q)", keyringService, keyringKeyName)
	case keyring.SecretServiceBackend:
		return fmt.Sprintf("the Secret Service (collection %q, item %q)", keyringService, keyringKeyName)
	case keyring.KWalletBackend:
		return fmt.Sprintf("KWallet (wallet %q, entry %q)", keyringService, keyringKeyName)
	case keyring.WinCredBackend:
		return fmt.Sprintf("the Windows Credential Manager (target %q)", "keyring:"+keyringService+":"+keyringKeyName)
	default:
		return fmt.Sprintf("the %q keyring backend", string(s.Backend))
	}
}

// MissingKeyDetail explains an empty lookup in this store: where it looked,
// plus the one way this backend is known to report "no key" when a key does
// exist.
func (s Store) MissingKeyDetail() string {
	detail := "looked in " + s.Describe()
	switch s.Backend {
	case keyring.FileBackend:
		detail += "; a process whose XDG_CONFIG_HOME or HOME differs reads a different store"
	case keyring.KeychainBackend:
		// 99designs/keyring v1.2.2 keychain.go Get returns ErrKeyNotFound for
		// ANY failed query (it tests len(results) == 0, which every error has).
		detail += "; this backend also reports a locked or access-denied keychain as \"not found\""
	}
	return detail
}

// openKeyring opens the first backend in keyringAllowedBackends that this
// binary supports and can open, and reports which one it chose. It walks the
// list itself rather than handing the whole list to keyring.Open (which does
// the same walk) because keyring.Open does not say which backend it picked.
//
// The file-backend passphrase is loaded lazily inside FilePasswordFunc so that
// OS store users never pay the cost of file-key validation. The ring returned
// here reads with readFilePassword; SetAPIKey writes the file backend through
// replaceFileBlob instead of this ring's Set.
func openKeyring() (keyring.Keyring, Store, error) {
	dir, source, err := configDir()
	if err != nil {
		return nil, Store{}, err
	}
	fileDir := filepath.Join(dir, "keyring")
	lastErr := keyring.ErrNoAvailImpl
	for _, backend := range keyringAllowedBackends {
		ring, err := keyring.Open(keyringConfig(backend, fileDir, readFilePassword))
		if err != nil {
			lastErr = err
			continue
		}
		store := Store{Backend: backend}
		if backend == keyring.FileBackend {
			store.Path = filepath.Join(fileDir, keyringKeyName)
			store.DirSource = source
		}
		return ring, store, nil
	}
	return nil, Store{}, lastErr
}

// keyringConfig is the keyring.Config for one backend, with the file backend
// rooted at fileDir and unlocked by password.
func keyringConfig(backend keyring.BackendType, fileDir string, password keyring.PromptFunc) keyring.Config {
	return keyring.Config{
		ServiceName:      keyringService,
		AllowedBackends:  []keyring.BackendType{backend},
		FileDir:          fileDir,
		FilePasswordFunc: password,
	}
}

// filePasswordError marks a failure of the file backend's passphrase lookup,
// so a read can tell "could not get the key to decrypt with" apart from "the
// blob itself did not decode". Only the latter can be a write in flight.
type filePasswordError struct{ err error }

func (e *filePasswordError) Error() string { return e.err.Error() }
func (e *filePasswordError) Unwrap() error { return e.err }

// readFilePassword is the passphrase lookup for READING the file keyring. It
// never creates a key file: the library only asks for the passphrase once the
// blob exists, and a blob cannot be decrypted by a key minted after it, so
// creating one here would only replace a clear "key file missing" error with a
// decode failure. (Writes create it — see writeFilePassword.)
func readFilePassword(string) (string, error) {
	dir, _, err := configDir()
	if err != nil {
		return "", &filePasswordError{err: err}
	}
	key, err := readFileKey(filepath.Join(dir, fileKeyName))
	if err != nil {
		return "", &filePasswordError{err: err}
	}
	return key, nil
}

// readFileKey is readFilePassword's key-file reader. A variable only so a test
// can inject a filesystem failure (EACCES) that cannot be produced on the key
// file alone without root.
var readFileKey = readAndValidateFileKey

// writeFilePassword is the passphrase lookup for WRITING the file keyring: it
// creates the per-install key file on first use.
func writeFilePassword(string) (string, error) {
	key, err := loadOrCreateFileKey()
	if err != nil {
		return "", &filePasswordError{err: err}
	}
	return key, nil
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

// unreadableRetries and unreadableRetryWait bound how long a read waits out a
// write in flight before calling a blob unreadable: at most four re-reads,
// ~100ms in all. Current binaries never expose a partial blob (SetAPIKey
// renames a finished file into place), but a mio from v0.22.0 or earlier
// sharing the same config dir still truncates and rewrites in place, and a
// reader that lands in that window sees an empty blob (reproduced in
// MIO-2995). unreadableRetryWait is a variable so tests can step through the
// window deterministically.
const unreadableRetries = 4

var unreadableRetryWait = func() { time.Sleep(25 * time.Millisecond) }

// beforeLegacyRemoval runs between recognising a legacy blob and removing it.
// It does nothing in production; a test uses it to publish a new key into
// exactly that window.
var beforeLegacyRemoval = func() {}

// legacyHoldPrefix names the private directories removeLegacyBlob moves a
// blob into, beside the live one, while it decides whether to delete it.
const legacyHoldPrefix = ".legacy-"

// beforeLegacyDetach runs once removeLegacyBlob has found the live path still
// names the recognised file and before it moves that file aside. It does
// nothing in production; a test uses it to publish a new key into exactly
// that window.
var beforeLegacyDetach = func() {}

// afterLegacyDetach runs once removeLegacyBlob has moved the live blob aside
// and before it decides whether to put it back. It does nothing in
// production; a test uses it to look at the live path inside that window.
var afterLegacyDetach = func() {}

// linkBlob puts a detached blob back at the live path. A variable only so a
// test can make it fail, which a real filesystem does only rarely (EIO,
// ENOSPC, EDQUOT).
var linkBlob = os.Link

// GetAPIKey returns the stored API key, or "" (no error) if none is stored.
// See LoadAPIKey, which also reports the store it read.
func GetAPIKey() (string, error) {
	key, _, err := LoadAPIKey()
	return key, err
}

// LoadAPIKey returns the stored API key ("" with no error when none is
// stored) together with the credential store it consulted — reported even
// when the key is missing or unusable, so the caller can say where it looked.
//
// A file-backend blob that exists but cannot be used is never reported as
// "no key stored", and is deleted only on positive evidence that it is a
// legacy (v0.1) blob. Two failures lead there:
//   - The blob does not decode. It is re-read a bounded number of times,
//     because a mio from v0.22.0 or earlier may be rewriting it in place.
//   - The per-install key file cannot be read (missing, not 0600, invalid).
//     Not retried: the key file is published atomically, so this is never a
//     write in flight — and on a permission drift readAndValidateFileKey has
//     just invalidated the blob, so a re-read would find it gone and answer
//     "no key stored" for a key that was deliberately revoked.
//
// Either way, a blob that decrypts under the legacy passphrase is deleted
// (ErrLegacyCredentials — v0.1 installs have no key file at all), and
// anything else is ErrUnreadableCredentials with the blob left in place.
//
// A MISSING blob is "no key stored" only when no legacy cleanup has one moved
// aside (see removeLegacyBlob). While one does, the read is retried like an
// undecodable blob, and if the blob is still not back it is
// ErrUnreadableCredentials naming where it is.
func LoadAPIKey() (string, Store, error) {
	ring, store, err := openKeyring()
	if err != nil {
		return "", store, fmt.Errorf("open credential store: %w", err)
	}
	item, err := ring.Get(keyringKeyName)
	for i := 0; i < unreadableRetries && (isUndecodableBlob(store, err) || isMovedAside(store, err)); i++ {
		unreadableRetryWait()
		item, err = ring.Get(keyringKeyName)
	}
	// A key file that is missing or invalid is a verdict on the stored
	// credential. One that exists but cannot be READ (EACCES, EIO) is an
	// environment failure, the same class as an unreadable blob, and takes the
	// generic path below.
	var passErr *filePasswordError
	keyFileRejected := errors.As(err, &passErr) && !isFilesystemFailure(passErr)
	switch {
	case err == nil:
		return string(item.Data), store, nil
	case errors.Is(err, keyring.ErrKeyNotFound):
		if held := movedAsideBlobs(store); len(held) > 0 {
			return "", store, fmt.Errorf("%w: %s holds no blob, but a legacy-credential cleanup moved one aside to %s and has not put it back. "+
				"It was left there: if no other mio process is running, move it back to %s; or run `mio login` (or export MIO_API_KEY) to store a new key",
				ErrUnreadableCredentials, store.Describe(), held[0], store.Path)
		}
		return "", store, nil
	case isUndecodableBlob(store, err) || keyFileRejected:
		// The blob's identity is taken BEFORE it is recognised: what the check
		// then reads is this file or one written over it later, and a later one
		// is never legacy, so a positive answer is about this file.
		recognised, statErr := os.Lstat(store.Path)
		if statErr == nil && isLegacyBlob(store) {
			beforeLegacyRemoval()
			if derr := removeLegacyBlob(store, recognised); derr != nil {
				// Deletion failed: the stale blob remains. Still typed as legacy
				// so callers map it to ExitAuth.
				return "", store, fmt.Errorf("%w (cleanup failed: %v)", ErrLegacyCredentials, derr)
			}
			return "", store, ErrLegacyCredentials
		}
		if passErr != nil {
			return "", store, fmt.Errorf("%w: the key file that unlocks %s is unusable: %v",
				ErrUnreadableCredentials, store.Describe(), passErr)
		}
		return "", store, fmt.Errorf("%w: %s holds a blob that does not decode (%v). It was left in place: "+
			"if another mio process was writing it, retry; otherwise run `mio login` (or export MIO_API_KEY) to replace it",
			ErrUnreadableCredentials, store.Describe(), err)
	default:
		return "", store, fmt.Errorf("read API key from %s: %w", store.Describe(), err)
	}
}

// isUndecodableBlob reports whether err is the file backend failing to DECODE
// the blob it read — the only failure a write in flight can cause. A missing
// blob, a filesystem error and a key-file (passphrase) failure are excluded.
func isUndecodableBlob(store Store, err error) bool {
	if err == nil || store.Backend != keyring.FileBackend || errors.Is(err, keyring.ErrKeyNotFound) {
		return false
	}
	var pathErr *fs.PathError
	var passErr *filePasswordError
	return !errors.As(err, &pathErr) && !errors.As(err, &passErr)
}

// isMovedAside reports whether err is the file backend finding no blob while
// a legacy cleanup has one moved aside: a window a re-read may see closed.
func isMovedAside(store Store, err error) bool {
	return errors.Is(err, keyring.ErrKeyNotFound) && len(movedAsideBlobs(store)) > 0
}

// movedAsideBlobs lists the blobs removeLegacyBlob has moved out of
// store.Path into a .legacy-* directory beside it and not yet deleted or put
// back: normally for a few syscalls, indefinitely if the process died there.
// File backend only.
func movedAsideBlobs(store Store) []string {
	if store.Backend != keyring.FileBackend || store.Path == "" {
		return nil
	}
	dir := filepath.Dir(store.Path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var held []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), legacyHoldPrefix) {
			continue
		}
		p := filepath.Join(dir, e.Name(), filepath.Base(store.Path))
		if _, err := os.Lstat(p); err == nil {
			held = append(held, p)
		}
	}
	return held
}

// isFilesystemFailure reports whether err is the filesystem refusing an
// operation on a path that exists (EACCES, EIO, ...) rather than reporting it
// missing.
func isFilesystemFailure(err error) bool {
	var pathErr *fs.PathError
	return errors.As(err, &pathErr) && !errors.Is(err, fs.ErrNotExist)
}

// removeLegacyBlob deletes the blob at store.Path only if it is STILL the
// legacy blob that was recognised; recognised is its Lstat, taken before it
// was recognised. Between the two another process can write a fresh key over
// the same path: `mio login` renames a new file over it, and a mio from
// v0.22.0 or earlier rewrites it in place. Neither may be deleted, nor moved
// off the live path, where for as long as it is gone every reader is told no
// key is stored.
//
//   - A key published by rename is a different file, so once the live path no
//     longer names the recognised file nothing is touched.
//   - Otherwise the blob is detached with a rename into a private directory
//     beside it (atomic, same filesystem) and re-checked there. It is deleted
//     only if it is still the recognised file AND still decrypts as legacy,
//     which a blob rewritten in place does not. Anything else is linked back,
//     unless an even newer blob already took the live path; if the link
//     fails, the detached copy is kept and the error says where.
//
// POSIX has no compare-and-rename, so a narrow window remains: a key renamed
// in between the identity check and the detach is moved aside too (and linked
// straight back, without decrypting anything), and an in-place rewrite of the
// recognised file stays aside while it is re-checked. Readers cover it:
// LoadAPIKey re-reads while a blob is moved aside, and names the .legacy-*
// copy rather than answer "no key stored" if it never comes back.
func removeLegacyBlob(store Store, recognised fs.FileInfo) error {
	if cur, err := os.Lstat(store.Path); err != nil || !os.SameFile(cur, recognised) {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil // gone, or replaced since it was recognised: nothing of ours to remove
	}
	beforeLegacyDetach()
	hold, err := os.MkdirTemp(filepath.Dir(store.Path), legacyHoldPrefix)
	if err != nil {
		return err
	}
	held := filepath.Join(hold, filepath.Base(store.Path))
	if err := os.Rename(store.Path, held); err != nil {
		_ = os.Remove(hold)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	afterLegacyDetach()
	if info, err := os.Lstat(held); err == nil && os.SameFile(info, recognised) &&
		isLegacyBlob(Store{Backend: keyring.FileBackend, Path: held}) {
		return os.RemoveAll(hold)
	}
	if err := linkBlob(held, store.Path); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("a blob that is no longer the legacy one was moved aside and could not be put back (%w); "+
			"it is kept at %s: move it back to %s", err, held, store.Path)
	}
	_ = os.RemoveAll(hold)
	return nil
}

// isLegacyBlob reports whether the blob at store.Path decrypts under the v0.1
// hardcoded passphrase — positive evidence that it is a legacy blob, as
// opposed to a partial write, which decrypts under no passphrase at all.
func isLegacyBlob(store Store) bool {
	ring, err := keyring.Open(keyring.Config{
		ServiceName:      keyringService,
		AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
		FileDir:          filepath.Dir(store.Path),
		FilePasswordFunc: func(string) (string, error) { return legacyFilePassphrase, nil },
	})
	if err != nil {
		return false
	}
	_, err = ring.Get(keyringKeyName)
	return err == nil
}

// deleteKeyringFile removes the encrypted keyring file so it can be
// re-created on the next `mio login`.
func deleteKeyringFile() error {
	dir, _, err := configDir()
	if err != nil {
		return err
	}
	target := filepath.Join(dir, "keyring", keyringKeyName)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// apiKeyItem is the keyring item the API key is stored as.
func apiKeyItem(key string) keyring.Item {
	return keyring.Item{
		Key:         keyringKeyName,
		Data:        []byte(key),
		Label:       "mio CLI API key",
		Description: "API key used by the mio CLI",
	}
}

// SetAPIKey persists the API key to the credential store.
//
// On the file backend it never rewrites the live blob in place: the keyring
// library's own Set truncates and then writes (os.WriteFile), and a concurrent
// reader in between sees an empty or partial blob (MIO-2995). The blob is
// written into a private staging directory beside the keyring instead and
// renamed over the live path, which replaces it atomically — every reader, of
// any mio version, sees either the old blob or the new one.
func SetAPIKey(key string) error {
	ring, store, err := openKeyring()
	if err != nil {
		return err
	}
	if store.Backend == keyring.FileBackend {
		if err := replaceFileBlob(store, apiKeyItem(key)); err != nil {
			return fmt.Errorf("write API key to %s: %w", store.Describe(), err)
		}
		return nil
	}
	if err := ring.Set(apiKeyItem(key)); err != nil {
		return fmt.Errorf("write API key to %s: %w", store.Describe(), err)
	}
	return nil
}

// replaceFileBlob encrypts item with the file backend into a fresh staging
// directory INSIDE the keyring directory and renames the result over
// store.Path. Staging beside the blob is the only way to guarantee the rename
// stays on one filesystem: the keyring directory can be a mount point or a
// symlink to another volume, and a rename across devices fails.
func replaceFileBlob(store Store, item keyring.Item) error {
	liveDir := filepath.Dir(store.Path)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(liveDir, ".staging-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()

	staged, err := keyring.Open(keyringConfig(keyring.FileBackend, stage, writeFilePassword))
	if err != nil {
		return err
	}
	if err := staged.Set(item); err != nil {
		return err
	}
	return os.Rename(filepath.Join(stage, filepath.Base(store.Path)), store.Path)
}

// DeleteAPIKey removes the stored API key. A missing key is not an error.
//
// On the file backend it also removes any copy a legacy cleanup moved aside
// and never put back (movedAsideBlobs): reads report such a copy as a stored
// key, so leaving it would keep a secret under the config dir after logout.
func DeleteAPIKey() error {
	ring, store, err := openKeyring()
	if err != nil {
		return err
	}
	// The file backend reports a missing blob as a raw os.ErrNotExist, not
	// keyring.ErrKeyNotFound; both mean there was nothing to delete.
	if err := ring.Remove(keyringKeyName); err != nil && !errors.Is(err, keyring.ErrKeyNotFound) && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete API key from %s: %w", store.Describe(), err)
	}
	for _, held := range movedAsideBlobs(store) {
		if err := os.RemoveAll(filepath.Dir(held)); err != nil {
			return fmt.Errorf("delete the API key a legacy-credential cleanup moved aside to %s: %w", held, err)
		}
	}
	return nil
}

// ---- Resolution -------------------------------------------------------------

// Overrides carries the per-invocation flag values that take precedence over
// env and config. Empty strings mean "not set on the command line".
type Overrides struct {
	APIKey string
	// Anonymous forces an unauthenticated resolution: the env var and stored-key
	// fallbacks are skipped so a request can deliberately run without credentials
	// (MIO-2648). An explicit APIKey still takes effect.
	Anonymous bool
	APIBase   string
	TeamID    string
	HubID     string
	Profile   string
}

// Resolve computes the effective {apiKey, apiBase, teamID, hubID} from the
// precedence chain:
//
//	api key : --api-key flag  >  MIO_API_KEY env  >  stored key
//	api base: --api-base flag >  MIO_API_BASE_URL >  profile/config  >  default
//	team/hub: --team/--hub    >  profile/config
//
// A missing API key is NOT an error here — commands that require auth check for
// an empty key and emit the auth exit code themselves, so read-only/login flows
// can proceed.
func (c *Config) Resolve(o Overrides) (Resolved, error) {
	prof := c.profile(o.Profile)

	// API key: flag > env > stored key. --anonymous (o.Anonymous) skips the env
	// and stored-key fallbacks so a request can run explicitly unauthenticated (MIO-2648).
	apiKey := o.APIKey
	if apiKey == "" && !o.Anonymous {
		apiKey = os.Getenv(EnvAPIKey)
	}
	var keyStore *Store
	if apiKey == "" && !o.Anonymous {
		stored, store, err := LoadAPIKey()
		keyStore = &store
		switch {
		case err == nil:
			apiKey = stored
		case StoredKeyUnusable(err):
			// A legacy blob (already deleted) or an unreadable one (left in
			// place): either way there is no usable stored key. Resolve the rest
			// of the context and surface the sentinel so callers (root wiring,
			// login, register) can react — login must be able to go on and
			// overwrite the store.
			return Resolved{
				APIBase:   firstNonEmpty(o.APIBase, os.Getenv(EnvAPIBase), prof.APIBase, c.APIBase, DefaultAPIBase),
				TeamID:    firstNonEmpty(o.TeamID, prof.CurrentTeam, c.CurrentTeam),
				HubID:     firstNonEmpty(o.HubID, prof.CurrentHub, c.CurrentHub),
				Anonymous: o.Anonymous,
				KeyStore:  keyStore,
			}, err
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

	// Anonymous is echoed back so the command layer can distinguish a deliberate
	// unauthenticated resolution from a failed one (MIO-2694) — see Resolved.
	return Resolved{APIKey: apiKey, APIBase: apiBase, TeamID: team, HubID: hub, Anonymous: o.Anonymous, KeyStore: keyStore}, nil
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
