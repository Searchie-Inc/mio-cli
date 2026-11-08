package cmd

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
		{"api_base accepts port+path", []string{"config", "set", "api_base", "http://localhost:8000/proxy"}, false},
		{"api_base rejects query", []string{"config", "set", "api_base", "https://api.membership.io?x=y"}, true},
		{"api_base rejects fragment", []string{"config", "set", "api_base", "https://api.membership.io#frag"}, true},
		{"api_base rejects empty query delimiter", []string{"config", "set", "api_base", "https://api.membership.io?"}, true},
		{"api_base rejects empty fragment delimiter", []string{"config", "set", "api_base", "https://api.membership.io#"}, true},
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

func TestConfigSet_ValidValuePersists(t *testing.T) {
	const uuid = "019e204f-9ea0-7601-ac0f-ab522eece374"
	dir := t.TempDir()
	env := []string{"XDG_CONFIG_HOME=" + dir}

	if res := runContract(t, env, "config", "set", "current_team", uuid); res.Code != errs.ExitOK {
		t.Fatalf("set exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	res := runContract(t, env, "config", "get", "current_team")
	if res.Code != errs.ExitOK {
		t.Fatalf("get exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, uuid) {
		t.Errorf("current_team not persisted: get returned %q, want %s", res.Stdout, uuid)
	}
}
