package cmd

// hubs_blob_keys_test.go — contract tests for the best-effort key validation of
// the hub presentation blobs (--branding-json / --settings-json / --meta-json)
// on `mio hubs create` and `mio hubs update` (MIO-2515, MIO-4171).
//
// Contract pinned here:
//   branding / meta — the API stores their keys as sent (branding checks only
//   the VALUES of keys ending in _url), so a typo is saved and does nothing:
//     default (no --strict-keys): an unknown key WARNS on stderr and the
//       request still fires — warn, don't block
//     --strict-keys: an unknown key is a usage error (ExitUsage) with NO HTTP
//       request fired (validation runs before auth/retrieve)
//   settings (MIO-4171) — the API enforces its OWN allowlist (MIO-3334): an
//     unknown top-level key, or an unknown sub-key of policies/registration/
//     email/auth, is a 422 naming it. So the CLI checks none of that, with or
//     without --strict-keys: the key reaches the API and the API's verdict is
//     the one the caller sees. The one settings check left is the sub-keys of
//     settings.achievements, which the API stores as sent.
//   each blob's message says what the API does with THAT blob's keys
//   the warning goes to STDERR only, never stdout, so --output json/yaml is safe

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// firedAnyServer starts a server that flips *fired on ANY request and answers
// with hubWithBlobsBody, used to assert that a strict-mode rejection fires no
// HTTP request at all (no retrieve, no PATCH/POST).
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

// TestHubsUpdate_UnknownKeyWarnsByDefault verifies an unknown top-level meta key
// warns on stderr (naming the blob.key) yet still exits ExitOK and fires the
// PATCH — the API stores meta keys as sent, so the CLI warns rather than
// blocking. (A settings key used to be the example here; since MIO-3334 the API
// rejects an unknown settings key itself — see
// TestHubsUpdate_SettingsKeysAreTheAPIsToCheck.)
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

// TestHubsUpdate_UnknownKeyStrictRejectsNoRequest verifies --strict-keys turns an
// unknown branding key into a usage error that fires NO HTTP request (no
// retrieve, no PATCH).
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

// TestHubsUpdate_KnownKeysNoWarning verifies known keys — including nested keys
// under the stable settings sections (registration.enabled) and known branding/
// meta keys — produce no warning and exit ExitOK.
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

// TestHubsUpdate_MetaDirectMessagesAccepted (MIO-2612) verifies `directMessages`
// is an accepted --meta-json feature-guard key: it no longer warns "unknown key",
// and it passes under --strict-keys (which rejects an unknown key with no request
// before this fix). directMessages is a real portal flag (MIO-2579).
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

// TestHubsUpdate_UnknownNestedKeyStrictRejects verifies the one nested settings
// check the CLI still makes: a misspelled settings.achievements sub-key — which
// the API stores as sent, because its own nested allowlist deliberately stops at
// policies/registration/email/auth — is a usage error with no request under
// --strict-keys.
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

// TestHubsCreate_UnknownKeyWarnsByDefault verifies the create path mirror: an
// unknown meta key warns on stderr yet the POST still fires.
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

// TestHubsCreate_UnknownKeyStrictRejectsNoRequest verifies --strict-keys on
// create rejects an unknown meta key with ExitUsage and no POST.
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

// TestHubsCreate_LogoURLDoesNotTripKeyValidation verifies the --logo-url merge
// into branding (logo_url) is a known key, so it does not produce a spurious
// warning even under --strict-keys.
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

// TestHubsCreate_AuthLogoURLIsKnownBrandingKey (MIO-3464) verifies the
// canonical login/register-panel logo key auth_logo_url (MIO-3354) is on the
// branding allowlist, so authoring it does not warn (and does not error under
// --strict-keys) the way it did when only the deprecated alias
// custom_login_logo_url was listed.
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

// TestHubsUpdate_AchievementsSettingsKeyAccepted (MIO-3412) verifies
// `achievements` is an accepted --settings-json key — it is the per-hub gate
// for the achievements module (backend reads
// hub.settings.achievements.enabled IS TRUE, app/achievements/feature_flag.py).
// It must pass under --strict-keys, produce no unknown-key warning, and the
// PATCH must carry settings.achievements.enabled=true after the
// read-modify-write merge.
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

// TestHubsUpdate_AchievementsUnknownNestedKeyWarns verifies the achievements
// section is deep-validated: a misspelled sub-key (settings.achievements.enabld)
// warns by name in default mode. The API stores it as sent, and its per-hub gate
// reads exactly `enabled` (the hub is opted out only by the literal false), so
// `{"enabld":false}` is saved and leaves achievements on.
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

// settingsDeferCases are --settings-json inputs whose verdict belongs to the API
// since MIO-3334, each chosen because the CLI's old hand-kept copy of the API's
// allowlist got it WRONG in one direction or the other (MIO-4171):
//   - keys the API ACCEPTS that the old list flagged, and that --strict-keys
//     blocked with no request (the list had fallen 7 top-level and 2 email
//     sub-keys behind the server);
//   - keys the API REJECTS, where the CLI warned "stored verbatim" and then the
//     API answered 422 — two contradicting messages on one call.
//
// Either way the CLI's correct behaviour is the same: send the key, say nothing
// of its own. The expected outcomes are not a second list — they are what the
// live API answered (mio-backend app/hubs/validation.py validate_settings; see
// the MIO-4171 PR for the recorded run).
var settingsDeferCases = []struct {
	name, settings, key string
}{
	{"top-level key the API accepts", `{"language":"de"}`, "language"},
	{"email sub-key the API accepts", `{"email":{"from_localpart":"news"}}`, "email"},
	{"top-level key the API rejects", `{"qa_probe":{"x":1}}`, "qa_probe"},
	{"registration sub-key the API rejects", `{"registration":{"enabld":true}}`, "registration"},
}

// TestHubsUpdate_SettingsKeysAreTheAPIsToCheck (MIO-4171): the CLI no longer
// keeps a client-side copy of the API's settings allowlist. With or without
// --strict-keys, a settings key the old copy got wrong reaches the PATCH, and
// the CLI prints no key warning of its own about it.
func TestHubsUpdate_SettingsKeysAreTheAPIsToCheck(t *testing.T) {
	for _, tc := range settingsDeferCases {
		for _, strict := range []bool{false, true} {
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

// TestHubsCreate_SettingsKeysAreTheAPIsToCheck is the create-path mirror.
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

// TestHubsUpdate_UnknownSettingsKeySurfacesTheAPIs422 is the ticket's own
// repro: `--settings-json '{"qa_probe":{"x":1}}'` used to print "It is stored
// verbatim" and then exit 2 on the API's 422 — two messages contradicting each
// other on one call. The API's rejection must now be the only verdict.
func TestHubsUpdate_UnknownSettingsKeySurfacesTheAPIs422(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.Method == http.MethodPatch {
			// The live API's answer, recorded against :8000 (MIO-4171).
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

// runWithErr runs the command tree in-process and returns BOTH its stderr and
// the error it returned: the SilenceErrors root never writes the error to the
// stderr buffer, and these tests need what the CLI warned as well as what it
// failed with.
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

// TestBlobKeyMessages_EachBlobSaysWhatTheAPIDoes (MIO-4171): the unknown-key
// warning and the --strict-keys error used to share ONE sentence — "stored
// verbatim ... no server-side validation" — for all three blobs, which stopped
// being true for settings when the API grew its own allowlist. Each blob now
// says what the API does with ITS keys, in both modes, and never borrows
// another blob's sentence.
func TestBlobKeyMessages_EachBlobSaysWhatTheAPIDoes(t *testing.T) {
	type blobCase struct {
		blob, flag, value, key string
		says                   []string // what THIS blob's message must say
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
