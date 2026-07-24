package cmd

// config_validation_test.go — MIO-2646: `mio config set` must validate the value
// shape of its known keys (api_base = http(s) URL; current_team/current_hub =
// UUID) so a bogus value is rejected at the setter (exit 2) instead of persisting
// verbatim and failing with a cryptic error on a later, unrelated command.
//
// This validates the CLI's OWN local state (the config file) — distinct from the
// --team/--hub flags, which stay faithful conduits the API validates.

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestConfigSet_ValidatesValues(t *testing.T) {
	const validUUID = "019e204f-9ea0-7601-ac0f-ab522eece374"
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"api_base rejects non-url", []string{"config", "set", "api_base", "not-a-url"}, true},
		{"api_base rejects non-http scheme", []string{"config", "set", "api_base", "ftp://x.example"}, true},
		{"api_base accepts https", []string{"config", "set", "api_base", "https://api.membership.io"}, false},
		{"api_base accepts http localhost", []string{"config", "set", "api_base", "http://localhost:8000"}, false},
		{"current_team rejects non-uuid", []string{"config", "set", "current_team", "not-a-uuid"}, true},
		{"current_team rejects prefixed id", []string{"config", "set", "current_team", "t_team1"}, true},
		{"current_team accepts uuid", []string{"config", "set", "current_team", validUUID}, false},
		{"current_hub rejects non-uuid", []string{"config", "set", "current_hub", "hub_abc"}, true},
		{"current_hub accepts uuid", []string{"config", "set", "current_hub", validUUID}, false},
		{"unknown key still rejected", []string{"config", "set", "totally_bogus", "x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			res := runContract(t, []string{"XDG_CONFIG_HOME=" + dir}, tc.args...)
			if tc.wantErr && res.Code != errs.ExitUsage {
				t.Errorf("exit=%d want ExitUsage (2); stderr=%q", res.Code, res.Stderr)
			}
			if !tc.wantErr && res.Code != errs.ExitOK {
				t.Errorf("exit=%d want ExitOK (0); stderr=%q", res.Code, res.Stderr)
			}
		})
	}
}

// A rejected value must NOT be written to the config file — the setter validates
// before Save, so the next command reads the prior (default) value, not garbage.
func TestConfigSet_RejectionDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	env := []string{"XDG_CONFIG_HOME=" + dir}

	res := runContract(t, env, "config", "set", "api_base", "not-a-url")
	if res.Code != errs.ExitUsage {
		t.Fatalf("set exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	res = runContract(t, env, "config", "get", "api_base")
	if res.Code != errs.ExitOK {
		t.Fatalf("get exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stdout, "not-a-url") {
		t.Errorf("rejected value was persisted: get returned %q", res.Stdout)
	}
}
