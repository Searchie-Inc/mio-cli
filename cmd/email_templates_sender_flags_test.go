package cmd

// email_templates_sender_flags_test.go — MIO-4784: `email templates
// create|update` no longer send `from_name`/`from_email`/`reply_to` (fields the
// API's template schema never had: dropped silently before mio-backend
// MIO-966, a 422 after), and gain `--description`. Every wire assertion reads
// the body the httptest server RECEIVED.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// templateRequest is what the mock backend saw on the last request: method,
// path, the JSON:API resource type and the attributes.
type templateRequest struct {
	Method, Path, Type string
	Attrs              map[string]any
}

// templateBodyServer answers every request 200 with an empty template and
// records the request count and the last request it received.
func templateBodyServer(t *testing.T) (*httptest.Server, *int, *templateRequest) {
	t.Helper()
	count := 0
	last := &templateRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		raw, _ := io.ReadAll(r.Body)
		var doc struct {
			Data struct {
				Type       string         `json:"type"`
				Attributes map[string]any `json:"attributes"`
			} `json:"data"`
		}
		_ = json.Unmarshal(raw, &doc)
		*last = templateRequest{Method: r.Method, Path: r.URL.Path, Type: doc.Data.Type, Attrs: doc.Data.Attributes}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{"type":"email_templates","id":"tmpl_1","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &count, last
}

func templatesArgs(verb string, rest ...string) []string {
	args := []string{"email", "templates", verb}
	if verb == "update" {
		args = append(args, "tmpl_1")
	}
	return append([]string{"--hub", "hub_123"}, withTeam("t_team1", append(args, rest...)...)...)
}

// TestEmailTemplates_SenderFlagsExitBeforeAnyRequest pins the retirement: each
// of --from-name/--from-email/--reply-to, on create and on update, exits 2 and
// fires NO request, and the error names `mio email config set` as the home of
// sender identity — not cobra's bare `unknown flag`, which is what a plain
// removal would print to a script still passing them.
func TestEmailTemplates_SenderFlagsExitBeforeAnyRequest(t *testing.T) {
	for _, verb := range []string{"create", "update"} {
		for _, flag := range []string{"--from-name", "--from-email", "--reply-to"} {
			t.Run(verb+" "+flag, func(t *testing.T) {
				srv, count, _ := templateBodyServer(t)
				res, err := runCaptured(t, baseEnv(srv.URL),
					templatesArgs(verb, "--name", "Welcome", "--subject", "S", flag, "x@example.com")...)
				if res.Code != errs.ExitUsage {
					t.Errorf("exit code = %d, want 2; err=%v", res.Code, err)
				}
				if *count != 0 {
					t.Errorf("%d request(s) fired; a retired sender flag must fire none", *count)
				}
				if err == nil || !strings.Contains(err.Error(), flag) ||
					!strings.Contains(err.Error(), "mio hubs email-settings update --from-name <name> --reply-to <addr>") ||
					!strings.Contains(err.Error(), "mio email config set (a full PUT: --mail-host --mail-username --mail-password --from-email --from-name") {
					t.Errorf("error %v must name the flag %s, the per-hub `hubs email-settings update` command for name/reply-to, and `email config set` as a FULL PUT for the From address", err, flag)
				}
			})
		}
	}
}

// TestEmailTemplates_SenderFlagsRejectBeforeHubResolution pins the ORDER: the
// rejection runs before context resolution. Without --hub (and no current_hub
// in the isolated config) the hub auto-default would GET the team's hubs; a
// guard placed after that resolution would let that request out before
// refusing. The server here answers a one-hub list, so a misplaced guard shows
// up as request count 1.
func TestEmailTemplates_SenderFlagsRejectBeforeHubResolution(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if strings.HasSuffix(r.URL.Path, "/hubs") {
			_, _ = w.Write([]byte(`{"data":[{"id":"hub_only","type":"hubs","attributes":{"name":"The Hub","slug":"the-hub"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"type":"email_templates","id":"tmpl_1","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	res, err := runCaptured(t, baseEnv(srv.URL),
		withTeam("t_team1", "email", "templates", "create", "--name", "Welcome", "--subject", "S", "--from-name", "Acme")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want 2; err=%v", res.Code, err)
	}
	if count != 0 {
		t.Errorf("%d request(s) fired (the hub auto-default ran before the sender-flag rejection); want none", count)
	}
}

// TestEmailTemplates_SenderFlagsAreHidden pins that the retired flags no longer
// advertise themselves — hidden on both verbs — while staying registered, so a
// script still passing one meets the rejection above rather than `unknown
// flag`. It also pins that --description IS advertised.
func TestEmailTemplates_SenderFlagsAreHidden(t *testing.T) {
	for _, c := range []struct {
		verb string
		cmd  *cobra.Command
	}{{"create", emailTemplatesCreateCmd}, {"update", emailTemplatesUpdateCmd}} {
		for _, name := range []string{"from-name", "from-email", "reply-to"} {
			f := c.cmd.Flags().Lookup(name)
			if f == nil {
				t.Errorf("%s: --%s must stay registered (hidden) so its use is explained, not `unknown flag`", c.verb, name)
				continue
			}
			if !f.Hidden {
				t.Errorf("%s: --%s is still advertised in --help; it must be hidden", c.verb, name)
			}
		}
		if f := c.cmd.Flags().Lookup("description"); f == nil || f.Hidden {
			t.Errorf("%s: --description must be a visible flag (got %v)", c.verb, f)
		}
	}
}

// TestEmailTemplates_BodyIsExactlyTheAllowedFields pins the wire for the fields
// that remain: the attributes sent are EXACTLY the flags passed, mapped to the
// API's names — nothing else leaks in (no from_*/reply_to, no bare body), on
// create and on update, --description included.
func TestEmailTemplates_BodyIsExactlyTheAllowedFields(t *testing.T) {
	cases := []struct {
		name       string
		verb       string
		args       []string
		wantMethod string
		wantPath   string
		want       map[string]any
	}{
		{"create all fields", "create",
			[]string{"--name", "Welcome", "--subject", "S", "--description", "Sent on signup", "--body", "<mjml></mjml>", "--plain-text", "hello"},
			http.MethodPost, "/v1/hubs/hub_123/email-templates",
			map[string]any{"name": "Welcome", "subject": "S", "description": "Sent on signup", "mjml_source": "<mjml></mjml>", "plain_text": "hello"}},
		{"update description only", "update",
			[]string{"--description", "Sent on signup"},
			http.MethodPatch, "/v1/hubs/hub_123/email-templates/tmpl_1",
			map[string]any{"description": "Sent on signup"}},
		{"update subject only", "update",
			[]string{"--subject", "S2"},
			http.MethodPatch, "/v1/hubs/hub_123/email-templates/tmpl_1",
			map[string]any{"subject": "S2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, count, req := templateBodyServer(t)
			res := runContract(t, baseEnv(srv.URL), templatesArgs(tc.verb, tc.args...)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if *count != 1 {
				t.Fatalf("requests = %d, want 1", *count)
			}
			if req.Method != tc.wantMethod || req.Path != tc.wantPath {
				t.Errorf("request = %s %s, want %s %s", req.Method, req.Path, tc.wantMethod, tc.wantPath)
			}
			if req.Type != "email_templates" {
				t.Errorf("JSON:API type = %q, want email_templates", req.Type)
			}
			got, _ := json.Marshal(req.Attrs)
			want, _ := json.Marshal(tc.want)
			if string(got) != string(want) {
				t.Errorf("attributes sent = %s, want exactly %s", got, want)
			}
		})
	}
}

// TestEmailTemplates_UpdateWithNoFieldsFiresNothing keeps the existing
// "nothing to update" usage error: no field flag → exit 2, no request.
func TestEmailTemplates_UpdateWithNoFieldsFiresNothing(t *testing.T) {
	srv, count, _ := templateBodyServer(t)
	res := runContract(t, baseEnv(srv.URL), templatesArgs("update")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want 2; stderr=%q", res.Code, res.Stderr)
	}
	if *count != 0 {
		t.Errorf("%d request(s) fired; want none", *count)
	}
}
