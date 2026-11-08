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
	keyringService = "mio-cli"
	keyringKeyName = "api-key"
	EnvAPIKey      = "MIO_API_KEY"
	EnvAPIBase     = "MIO_API_BASE_URL"
	DefaultAPIBase = "https://api.member.dev"

	fileKeyName = "file-keyring.key"
)

type Profile struct {
	CurrentTeam string `toml:"current_team,omitempty"`
	CurrentHub  string `toml:"current_hub,omitempty"`
	APIBase     string `toml:"api_base,omitempty"`
}

type Config struct {
	CurrentTeam string             `toml:"current_team,omitempty"`
	CurrentHub  string             `toml:"current_hub,omitempty"`
	APIBase     string             `toml:"api_base,omitempty"`
	Profiles    map[string]Profile `toml:"profiles,omitempty"`

	path string `toml:"-"`
}

type Resolved struct {
	APIKey    string
	APIBase   string
	TeamID    string
	HubID     string
	Anonymous bool
	KeyStore  *Store
}

func Path() (string, error) {
	dir, _, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

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

var ErrLegacyCredentials = errors.New("stored credentials use legacy encryption")

var ErrUnreadableCredentials = errors.New("stored API key could not be read")

func StoredKeyUnusable(err error) bool {
	return errors.Is(err, ErrLegacyCredentials) || errors.Is(err, ErrUnreadableCredentials)
}

const legacyFilePassphrase = "mio-cli"

func loadOrCreateFileKey() (string, error) {
	cfgPath, err := Path()
	if err != nil {
		return "", err
	}
	keyPath := filepath.Join(filepath.Dir(cfgPath), fileKeyName)

	if key, err := readAndValidateFileKey(keyPath); err == nil {
		return key, nil
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", fmt.Errorf("create config dir for key file: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate file keyring key: %w", err)
	}
	key := hex.EncodeToString(raw)

	tmp, err := os.CreateTemp(filepath.Dir(keyPath), ".file-keyring-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create tmp file keyring key: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
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

	linkErr := os.Link(tmpName, keyPath)
	switch {
	case linkErr == nil:
		return readAndValidateFileKeyRetry(keyPath)
	case errors.Is(linkErr, os.ErrExist):
		return readAndValidateFileKeyRetry(keyPath)
	default:
		return "", fmt.Errorf("publish file keyring key: %w", linkErr)
	}
}

func readAndValidateFileKey(keyPath string) (string, error) {
	info, err := os.Lstat(keyPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("file keyring key is not a regular file (mode %v)", info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		_ = os.Remove(keyPath)
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

var keyringAllowedBackends = []keyring.BackendType{
	keyring.KeychainBackend,
	keyring.SecretServiceBackend,
	keyring.WinCredBackend,
	keyring.KWalletBackend,
	keyring.FileBackend,
}

func UseFileBackendOnly() (restore func()) {
	orig := keyringAllowedBackends
	keyringAllowedBackends = []keyring.BackendType{keyring.FileBackend}
	return func() { keyringAllowedBackends = orig }
}

func KeyringBackends() []keyring.BackendType {
	return append([]keyring.BackendType(nil), keyringAllowedBackends...)
}

type Store struct {
	Backend   keyring.BackendType
	Path      string
	DirSource string
}

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

func (s Store) MissingKeyDetail() string {
	detail := "looked in " + s.Describe()
	switch s.Backend {
	case keyring.FileBackend:
		detail += "; a process whose XDG_CONFIG_HOME or HOME differs reads a different store"
	case keyring.KeychainBackend:
		detail += "; this backend also reports a locked or access-denied keychain as \"not found\""
	}
	return detail
}

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

func keyringConfig(backend keyring.BackendType, fileDir string, password keyring.PromptFunc) keyring.Config {
	return keyring.Config{
		ServiceName:      keyringService,
		AllowedBackends:  []keyring.BackendType{backend},
		FileDir:          fileDir,
		FilePasswordFunc: password,
	}
}

type filePasswordError struct{ err error }

func (e *filePasswordError) Error() string { return e.err.Error() }
func (e *filePasswordError) Unwrap() error { return e.err }

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

var readFileKey = readAndValidateFileKey

func writeFilePassword(string) (string, error) {
	key, err := loadOrCreateFileKey()
	if err != nil {
		return "", &filePasswordError{err: err}
	}
	return key, nil
}

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

func legacyKeyringItem(apiKey string) keyring.Item {
	return keyring.Item{
		Key:         keyringKeyName,
		Data:        []byte(apiKey),
		Label:       "mio CLI API key",
		Description: "API key used by the mio CLI",
	}
}

const unreadableRetries = 4

var unreadableRetryWait = func() { time.Sleep(25 * time.Millisecond) }

func GetAPIKey() (string, error) {
	key, _, err := LoadAPIKey()
	return key, err
}

func LoadAPIKey() (string, Store, error) {
	ring, store, err := openKeyring()
	if err != nil {
		return "", store, fmt.Errorf("open credential store: %w", err)
	}
	item, err := ring.Get(keyringKeyName)
	for i := 0; i < unreadableRetries && isUndecodableBlob(store, err); i++ {
		unreadableRetryWait()
		item, err = ring.Get(keyringKeyName)
	}
	var passErr *filePasswordError
	keyFileRejected := errors.As(err, &passErr) && !isFilesystemFailure(passErr)
	switch {
	case err == nil:
		return string(item.Data), store, nil
	case errors.Is(err, keyring.ErrKeyNotFound):
		return "", store, nil
	case isUndecodableBlob(store, err) || keyFileRejected:
		if isLegacyBlob(store) {
			return "", store, fmt.Errorf("%w: %s holds a key encrypted under the retired v0.1 passphrase, which this version does not read. "+
				"It was left in place: run `mio login` to replace it (or `mio logout` to delete it)",
				ErrLegacyCredentials, store.Describe())
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

func isUndecodableBlob(store Store, err error) bool {
	if err == nil || store.Backend != keyring.FileBackend || errors.Is(err, keyring.ErrKeyNotFound) {
		return false
	}
	var pathErr *fs.PathError
	var passErr *filePasswordError
	return !errors.As(err, &pathErr) && !errors.As(err, &passErr)
}

func isFilesystemFailure(err error) bool {
	var pathErr *fs.PathError
	return errors.As(err, &pathErr) && !errors.Is(err, fs.ErrNotExist)
}

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

func apiKeyItem(key string) keyring.Item {
	return keyring.Item{
		Key:         keyringKeyName,
		Data:        []byte(key),
		Label:       "mio CLI API key",
		Description: "API key used by the mio CLI",
	}
}

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

func DeleteAPIKey() error {
	ring, store, err := openKeyring()
	if err != nil {
		return err
	}
	if err := ring.Remove(keyringKeyName); err != nil && !errors.Is(err, keyring.ErrKeyNotFound) && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete API key from %s: %w", store.Describe(), err)
	}
	return nil
}

type Overrides struct {
	APIKey    string
	Anonymous bool
	APIBase   string
	TeamID    string
	HubID     string
	Profile   string
}

func (c *Config) Resolve(o Overrides) (Resolved, error) {
	prof := c.profile(o.Profile)

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

	return Resolved{APIKey: apiKey, APIBase: apiBase, TeamID: team, HubID: hub, Anonymous: o.Anonymous, KeyStore: keyStore}, nil
}

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
