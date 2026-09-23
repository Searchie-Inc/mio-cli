package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestRequiredFlags_DeclaredToCobra pins every requirement MIO-4154 moved out
// of a RunE body and into markFlagsRequired.
//
// The oracle is behaviour, read three ways: omitting the flag must exit 2
// (ExitUsage), fire NO request, and fail with COBRA's own message naming the
// flag. The message is what makes this test discriminating: several of these
// commands still reject an EMPTY value inside RunE (`--contact-id must not be
// empty`), so without the message check a deleted declaration would still exit
// 2 with no request — and would silently vanish from the doc-example guard
// (doc_examples_test.go), which can only see what cobra can see.
//
// Each case's base invocation supplies every required flag and positional, so
// dropping exactly one flag makes that flag the only one cobra reports.
func TestRequiredFlags_DeclaredToCobra(t *testing.T) {
	cases := []struct {
		base     []string // full invocation, every required flag present
		required []string // flags declared with markFlagsRequired
	}{
		{[]string{"products", "create", "--name", "N", "--type", "course"}, []string{"name", "type"}},
		{[]string{"products", "prices", "create", "prod_1", "--amount", "100", "--currency", "usd", "--type", "one_time"}, []string{"amount", "currency", "type"}},
		{[]string{"products", "deliverables", "create", "prod_1", "--type", "hub_access"}, []string{"type"}},
		{[]string{"automations", "create", "--hub", "hub_1", "--name", "N", "--definition", "{}"}, []string{"name", "definition"}},
		{[]string{"content", "create", "--hub", "hub_1", "--title", "T", "--node-type", "lesson"}, []string{"title", "node-type"}},
		{[]string{"coupons", "create", "--code", "C", "--discount-type", "percent", "--discount-value", "10"}, []string{"code", "discount-type", "discount-value"}},
		{[]string{"contact-attributes", "create", "--name", "N", "--slug", "s", "--field-type", "text"}, []string{"name", "slug", "field-type"}},
		{[]string{"roles", "create", "--name", "N", "--slug", "s"}, []string{"name", "slug"}},
		{[]string{"roles", "permissions", "assign", "role_1", "--slug", "content.publish"}, []string{"slug"}},
		{[]string{"tags", "create", "--name", "N", "--slug", "s"}, []string{"name", "slug"}},
		{[]string{"segments", "create", "--name", "N", "--conditions", "{}"}, []string{"name", "conditions"}},
		{[]string{"events", "create", "--hub", "hub_1", "--title", "T", "--starts-at", "2026-01-01T00:00:00Z",
			"--ends-at", "2026-01-01T01:00:00Z", "--timezone", "UTC", "--location-type", "url"},
			[]string{"title", "starts-at", "ends-at", "timezone", "location-type"}},
		{[]string{"events", "rsvp", "set", "evt_1", "--hub", "hub_1", "--status", "going"}, []string{"status"}},
		{[]string{"achievements", "create", "--title", "T"}, []string{"title"}},
		{[]string{"achievements", "grant", "ach_1", "--hub", "hub_1", "--contact-id", "c_1"}, []string{"contact-id"}},
		{[]string{"achievements", "revoke", "ach_1", "--hub", "hub_1", "--contact-id", "c_1", "--yes"}, []string{"contact-id"}},
		{[]string{"achievements", "restore", "ach_1", "--hub", "hub_1", "--contact-id", "c_1"}, []string{"contact-id"}},
		{[]string{"achievements", "override", "ach_1", "--hub", "hub_1", "--contact-id", "c_1", "--reason", "r"}, []string{"contact-id", "reason"}},
		{[]string{"media", "search", "--query", "q"}, []string{"query"}},
		{[]string{"media", "hub-media", "publish", "--hub", "hub_1", "--file-id", "f_1"}, []string{"file-id"}},
		{[]string{"media", "hub-playlists", "publish", "--hub", "hub_1", "--playlist-id", "pl_1"}, []string{"playlist-id"}},
		{[]string{"media", "files", "cards", "set", "f_1", "--cards", "[]"}, []string{"cards"}},
		{[]string{"media", "files", "chapters", "set", "f_1", "--chapters", "[]"}, []string{"chapters"}},
		{[]string{"media", "transcripts", "edit", "m_1", "--words", "[]"}, []string{"words"}},
		{[]string{"media", "playlists", "items", "add", "--playlist-id", "pl_1", "--file-id", "f_1"}, []string{"playlist-id", "file-id"}},
		{[]string{"media", "playlists", "items", "list", "--playlist-id", "pl_1"}, []string{"playlist-id"}},
		{[]string{"media", "playlists", "items", "remove", "it_1", "--playlist-id", "pl_1", "--yes"}, []string{"playlist-id"}},
		{[]string{"media", "playlists", "items", "reorder", "it_1", "--playlist-id", "pl_1", "--position", "1"}, []string{"playlist-id", "position"}},
		{[]string{"email", "suppressions", "create", "--hub", "hub_1", "--email", "a@example.com"}, []string{"email"}},
		{[]string{"pages", "sections", "create", "page_1", "--hub", "hub_1", "--type", "text"}, []string{"type"}},
	}

	for _, tc := range cases {
		path := tc.base
		for i, w := range tc.base {
			if strings.HasPrefix(w, "--") {
				path = tc.base[:i]
				break
			}
		}
		for _, flag := range tc.required {
			args := withoutFlag(t, tc.base, flag)
			t.Run(strings.Join(path, "_")+"/without_--"+flag, func(t *testing.T) {
				fired := false
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fired = true
					w.WriteHeader(http.StatusTeapot)
				}))
				defer srv.Close()

				restore := overlayEnv(t, baseEnv(srv.URL))
				defer restore()
				resetGlobalFlags()
				err := runRoot(t, append(args, "--team", "t_team1")...)

				if code := codeForExecuteErr(err); code != errs.ExitUsage {
					t.Errorf("mio %s: exit code = %d, want %d (ExitUsage); err=%v", strings.Join(args, " "), code, errs.ExitUsage, err)
				}
				want := `required flag(s) "` + flag + `" not set`
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("mio %s: err = %v, want cobra's %q — the flag is not declared with markFlagsRequired, so the doc-example guard cannot see it",
						strings.Join(args, " "), err, want)
				}
				if fired {
					t.Errorf("mio %s: a request was sent although --%s is missing", strings.Join(args, " "), flag)
				}
			})
		}
	}
}

// withoutFlag returns base with `--flag value` removed. It fails the test when
// the flag is not in base, so a case can never pass by dropping nothing.
func withoutFlag(t *testing.T, base []string, flag string) []string {
	t.Helper()
	out := make([]string, 0, len(base))
	found := false
	for i := 0; i < len(base); i++ {
		if base[i] == "--"+flag {
			found = true
			i++ // skip the value
			continue
		}
		out = append(out, base[i])
	}
	if !found {
		t.Fatalf("case %v does not pass --%s", base, flag)
	}
	return out
}
