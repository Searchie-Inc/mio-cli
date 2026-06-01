package config

import (
	"os"
	"path/filepath"
	"testing"
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
	if r.APIBase != DefaultAPIBase {
		t.Errorf("APIBase = %q, want default %q", r.APIBase, DefaultAPIBase)
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
