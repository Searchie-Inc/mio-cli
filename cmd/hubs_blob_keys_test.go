package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func firedAnyServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubWithBlobsBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &fired
}

func TestHubsUpdate_UnknownKeyWarnsByDefault(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--meta-json", `{"memberDirectry":{}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) — an unknown key should warn, not block; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Warning") || !strings.Contains(res.Stderr, "meta.memberDirectry") {
		t.Errorf("stderr should carry a warning naming meta.memberDirectry; got %q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must still fire in warn mode (the API stores meta keys as sent)")
	}
	if strings.Contains(res.Stdout, "Warning") {
		t.Error("the warning must go to stderr only, never stdout (it would corrupt --output json/yaml)")
	}
}

func TestHubsUpdate_UnknownKeyStrictRejectsNoRequest(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--branding-json", `{"primry":"#ffffff"}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage) under --strict-keys; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a strict unknown-key rejection must fire no HTTP request (validation runs before retrieve)")
	}
}

func TestHubsUpdate_KnownKeysNoWarning(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--branding-json", `{"favicon_url":"https://x/f.ico"}`,
			"--settings-json", `{"registration":{"enabled":true}}`,
			"--meta-json", `{"discussions":{"enabled":false}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("known keys must not warn; stderr=%q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must fire for known keys")
	}
}

func TestHubsUpdate_MetaDirectMessagesAccepted(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--meta-json", `{"directMessages":{"enabled":true}}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) for a known meta key under --strict-keys; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("directMessages must not warn as an unknown key; stderr=%q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must fire for a known meta key")
	}
}

func TestHubsUpdate_UnknownNestedKeyStrictRejects(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"achievements":{"enabld":false}}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage) for a misspelled nested key under --strict-keys; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a strict nested unknown-key rejection must fire no HTTP request")
	}
}

func TestHubsCreate_UnknownKeyWarnsByDefault(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--meta-json", `{"bogus":1}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Warning") || !strings.Contains(res.Stderr, "meta.bogus") {
		t.Errorf("stderr should warn naming meta.bogus; got %q", res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("the POST must still fire in warn mode; got method %q", *gotMethod)
	}
}

func TestHubsCreate_UnknownKeyStrictRejectsNoRequest(t *testing.T) {
	srv, fired := firedFlagHubServer(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--meta-json", `{"bogus":1}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage) under --strict-keys; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a strict unknown-key rejection on create must fire no HTTP request")
	}
}

func TestHubsCreate_LogoURLDoesNotTripKeyValidation(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--logo-url", "https://x/l.png",
			"--strict-keys",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) — logo_url is a known branding key; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("--logo-url (logo_url) is a known key and must not warn; stderr=%q", res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("the POST must fire; got method %q", *gotMethod)
	}
}

func TestHubsCreate_AuthLogoURLIsKnownBrandingKey(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--branding-json", `{"auth_logo_url":"https://x/a.png"}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) — auth_logo_url is a known branding key; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("auth_logo_url is a known key and must not warn; stderr=%q", res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("the POST must fire; got method %q", *gotMethod)
	}
}

func TestHubsUpdate_AchievementsSettingsKeyAccepted(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"achievements":{"enabled":true}}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) for the achievements settings key under --strict-keys; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("achievements must not warn as an unknown settings key; stderr=%q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Fatal("the PATCH must fire for the achievements settings key")
	}

	var doc struct {
		Data struct {
			Attributes struct {
				Settings map[string]any `json:"settings"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*patchBody, &doc); err != nil {
		t.Fatalf("PATCH body is not valid JSON: %v; body=%q", err, *patchBody)
	}
	ach, ok := doc.Data.Attributes.Settings["achievements"].(map[string]any)
	if !ok {
		t.Fatalf("settings.achievements missing from PATCH body; settings=%v", doc.Data.Attributes.Settings)
	}
	if ach["enabled"] != true {
		t.Errorf("settings.achievements.enabled = %v, want true", ach["enabled"])
	}
}

func TestHubsUpdate_AchievementsUnknownNestedKeyWarns(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"achievements":{"enabld":true}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "settings.achievements.enabld") {
		t.Errorf("stderr should name the misspelled nested key settings.achievements.enabld; got %q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must still fire in warn mode")
	}
}

var settingsDeferCases = []struct {
	name, settings, key string
}{
	{"top-level key the API accepts", `{"language":"de"}`, "language"},
	{"email sub-key the API accepts", `{"email":{"from_localpart":"news"}}`, "email"},
	{"top-level key the API rejects", `{"qa_probe":{"x":1}}`, "qa_probe"},
	{"policies sub-key the API rejects", `{"policies":{"qa_probe":true}}`, "policies"},
	{"registration sub-key the API rejects", `{"registration":{"enabld":true}}`, "registration"},
	{"email sub-key the API rejects", `{"email":{"qa_probe":"x"}}`, "email"},
	{"auth sub-key the API rejects", `{"auth":{"qa_probe":[]}}`, "auth"},
}

func TestHubsUpdate_SettingsKeysAreTheAPIsToCheck(t *testing.T) {
	for _, tc := range settingsDeferCases {
		for _, strict := range []bool{false, true} {
			if strict && tc.key == "policies" {
				continue
			}
			name := tc.name
			if strict {
				name += " --strict-keys"
			}
			t.Run(name, func(t *testing.T) {
				srv, patchBody := rmwServer(t)
				args := []string{"hubs", "update", "hub_abc123", "--settings-json", tc.settings}
				if strict {
					args = append(args, "--strict-keys")
				}
				res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)

				if res.Code != errs.ExitOK {
					t.Fatalf("exit code = %d, want %d (ExitOK) — the CLI must leave settings keys to the API, not block them; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
				}
				if strings.Contains(res.Stderr, "Warning") || strings.Contains(res.Stderr, "unknown key") {
					t.Errorf("the CLI must not warn about a settings key the API checks itself; stderr=%q", res.Stderr)
				}
				if len(*patchBody) == 0 {
					t.Fatal("the PATCH must fire: the API, not the CLI, decides on settings keys")
				}
				s, ok := decodeHubAttrs(t, *patchBody)["settings"].(map[string]any)
				if !ok {
					t.Fatalf("PATCH carries no settings object; body=%s", *patchBody)
				}
				if _, ok := s[tc.key]; !ok {
					t.Errorf("settings.%s must reach the API; settings=%v", tc.key, s)
				}
			})
		}
	}
}

func TestHubsCreate_SettingsKeysAreTheAPIsToCheck(t *testing.T) {
	for _, tc := range settingsDeferCases {
		t.Run(tc.name, func(t *testing.T) {
			srv, gotMethod, _, gotBody := captureHubRequest(t, http.StatusCreated)
			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "hubs", "create", "--name", "X",
					"--settings-json", tc.settings, "--strict-keys")...)

			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want %d (ExitOK) under --strict-keys; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
			}
			if strings.Contains(res.Stderr, "Warning") || strings.Contains(res.Stderr, "unknown key") {
				t.Errorf("the CLI must not warn about a settings key the API checks itself; stderr=%q", res.Stderr)
			}
			if *gotMethod != http.MethodPost {
				t.Fatalf("the POST must fire; got method %q", *gotMethod)
			}
			s, ok := decodeHubAttrs(t, *gotBody)["settings"].(map[string]any)
			if !ok {
				t.Fatalf("POST carries no settings object; body=%s", *gotBody)
			}
			if _, ok := s[tc.key]; !ok {
				t.Errorf("settings.%s must reach the API; settings=%v", tc.key, s)
			}
		})
	}
}

func TestHubsUpdate_UnknownSettingsKeySurfacesTheAPIs422(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"errors":[{"status":"422","detail":"Value error, hub settings has unsupported key(s): ['qa_probe']","source":{"pointer":"/data/attributes/settings/qa_probe"}}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubWithBlobsBody))
	}))
	t.Cleanup(srv.Close)

	stderr, err := runWithErr(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_abc123",
			"--settings-json", `{"qa_probe":{"x":1}}`)...)

	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage, from the API's 422); err=%v stderr=%q", got, errs.ExitUsage, err, stderr)
	}
	if !strings.Contains(err.Error(), "unsupported key(s)") {
		t.Errorf("the error must be the API's own rejection; got %v", err)
	}
	for _, stale := range []string{"stored verbatim", "Warning"} {
		if strings.Contains(stderr, stale) {
			t.Errorf("stderr must not also carry the CLI's contradicting claim %q; got %q", stale, stderr)
		}
	}
}

func runWithErr(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	restore := overlayEnv(t, env)
	defer restore()
	resetGlobalFlags()
	root := RootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	defer root.SetArgs(nil)
	err := root.Execute()
	return stderr.String(), err
}

func TestBlobKeyMessages_EachBlobSaysWhatTheAPIDoes(t *testing.T) {
	type blobCase struct {
		blob, flag, value, key string
		says                   []string
	}
	cases := []blobCase{
		{"branding", "--branding-json", `{"primry":"#ffffff"}`, "branding.primry",
			[]string{"The API stores branding keys as sent", "ending in _url"}},
		{"meta", "--meta-json", `{"memberDirectry":{}}`, "meta.memberDirectry",
			[]string{"The API stores meta keys as sent"}},
		{"settings", "--settings-json", `{"achievements":{"enabld":false}}`, "settings.achievements.enabld",
			[]string{"stores the sub-keys of settings.achievements as sent", "reads only settings.achievements.enabled"}},
	}
	for _, tc := range cases {
		t.Run(tc.blob, func(t *testing.T) {
			warnSrv, _ := rmwServer(t)
			warned := runContract(t, baseEnv(warnSrv.URL),
				withTeam("t_team1", "hubs", "update", "hub_abc123", tc.flag, tc.value)...)
			strictSrv, _ := firedAnyServer(t)
			_, strictErr := runWithErr(t, baseEnv(strictSrv.URL),
				withTeam("t_team1", "hubs", "update", "hub_abc123", tc.flag, tc.value, "--strict-keys")...)
			if errs.CodeOf(strictErr) != errs.ExitUsage {
				t.Fatalf("%s under --strict-keys: exit code = %d, want %d (ExitUsage); err=%v", tc.blob, errs.CodeOf(strictErr), errs.ExitUsage, strictErr)
			}
			strictMsg := strictErr.Error()

			for mode, msg := range map[string]string{"warning": warned.Stderr, "--strict-keys error": strictMsg} {
				if !strings.Contains(msg, tc.key) {
					t.Errorf("%s %s must name %s; got %q", tc.blob, mode, tc.key, msg)
				}
				for _, want := range tc.says {
					if !strings.Contains(msg, want) {
						t.Errorf("%s %s must say %q; got %q", tc.blob, mode, want, msg)
					}
				}
				for _, other := range cases {
					if other.blob == tc.blob {
						continue
					}
					for _, borrowed := range other.says {
						if strings.Contains(msg, borrowed) {
							t.Errorf("%s %s borrows the %s sentence %q; got %q", tc.blob, mode, other.blob, borrowed, msg)
						}
					}
				}
				for _, stale := range []string{"stored verbatim", "no server-side validation"} {
					if strings.Contains(msg, stale) {
						t.Errorf("%s %s still carries the old one-size claim %q; got %q", tc.blob, mode, stale, msg)
					}
				}
			}
			if !strings.Contains(strictMsg, "drop --strict-keys") {
				t.Errorf("%s --strict-keys error must say how to send the key anyway; got %q", tc.blob, strictMsg)
			}
		})
	}
}

var settingsSectionProbes = []string{
	"registration", "policies", "header", "menu", "email", "auth", "customCss",
	"background", "appearance", "language", "footer", "achievements",
	"speaker_diarization", "moderation", "word_boost", "playlistHero",
	"discussionsBanner", "buttonColors",
	"qa_probe",
}

func TestHubsSettingsHelp_NamesExactlyTheChecksEachCommandMakes(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"create", hubsCreateCmd, []string{"hubs", "create", "--name", "X"}},
		{"update", hubsUpdateCmd, []string{"hubs", "update", "hub_abc123"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stopped := map[string]bool{}
			for _, section := range settingsSectionProbes {
				srv, fired := firedAnyServer(t)
				args := append(append([]string{}, c.args...),
					"--settings-json", `{"`+section+`":{"qa_probe":1}}`, "--strict-keys")
				res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
				if res.Code == errs.ExitUsage && !*fired {
					stopped[section] = true
				}
			}
			if len(stopped) == 0 {
				t.Fatalf("hubs %s stopped no settings section client-side; the probe is not reaching the CLI's checks", c.name)
			}

			for _, flag := range []string{"strict-keys", "settings-json"} {
				f := c.cmd.Flags().Lookup(flag)
				if f == nil {
					t.Fatalf("hubs %s has no --%s flag", c.name, flag)
				}
				wantVar := map[string]string{"strict-keys": "", "settings-json": "string"}[flag]
				if varName, _ := pflag.UnquoteUsage(f); varName != wantVar {
					t.Errorf("hubs %s --%s renders its value name as %q, want %q: a backquoted span in the help becomes the value name; help=%q",
						c.name, flag, varName, wantVar, f.Usage)
				}
				for _, section := range settingsSectionProbes {
					named := strings.Contains(f.Usage, "settings."+section)
					switch {
					case stopped[section] && !named:
						t.Errorf("hubs %s stops settings.%s client-side under --strict-keys (exit 2, no request), but its --%s help never names settings.%s — an agent reading that help would take the exit 2 for the API's 422; help=%q",
							c.name, section, flag, section, f.Usage)
					case !stopped[section] && named:
						t.Errorf("hubs %s --%s help names settings.%s as a key the CLI checks, but the command sends it to the API under --strict-keys; help=%q",
							c.name, flag, section, f.Usage)
					}
				}
			}
		})
	}
}
