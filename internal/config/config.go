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
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	DefaultAPIBase = "https://api.membership.io"
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

// openKeyring opens the OS keychain for the mio-cli service, falling back to an
// encrypted-at-rest file backend under the config dir when no native keychain
// is available (Linux headless, containers, CI).
func openKeyring() (keyring.Keyring, error) {
	cfgPath, err := Path()
	if err != nil {
		return nil, err
	}
	fileDir := filepath.Join(filepath.Dir(cfgPath), "keyring")
	return keyring.Open(keyring.Config{
		ServiceName: keyringService,
		// Allow the OS-native backends first; FileBackend is the portable
		// fallback so headless environments still work without prompting.
		AllowedBackends: []keyring.BackendType{
			keyring.KeychainBackend,
			keyring.SecretServiceBackend,
			keyring.WinCredBackend,
			keyring.KWalletBackend,
			keyring.FileBackend,
		},
		FileDir: fileDir,
		// For the file fallback we use a fixed, non-interactive passphrase
		// prompt: the file is already protected by 0600 + the user's home dir,
		// and prompting would break non-TTY agents. The keychain backends do
		// not use this.
		FilePasswordFunc: func(string) (string, error) { return "mio-cli", nil },
	})
}

// GetAPIKey returns the stored API key, or "" (no error) if none is stored.
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
		return "", fmt.Errorf("read key from keychain: %w", err)
	}
	return string(item.Data), nil
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
		if err != nil {
			return Resolved{}, err
		}
		apiKey = stored
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
