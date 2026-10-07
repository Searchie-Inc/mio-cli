package cmd

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestUpdateCommands_RejectEmailFlag (MIO-4930; also covers users update --avatar-url,
// which PATCH /api/v1/users/{id} never accepted): neither API update contract
// takes an email — PATCH team-contacts answers 422 and PATCH users answers 200
// and silently ignores it. The flag must therefore not exist on the update
// commands (create and register keep theirs): passing it is an unknown flag,
// exit 2, with no request sent.
func TestUpdateCommands_RejectEmailFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"contacts update", withTeam("t_team1", "contacts", "update", "ctt_1", "--first-name", "A", "--email", "a@example.com")},
		{"users update", []string{"users", "update", "usr_1", "--first-name", "A", "--email", "a@example.com"}},
		{"users update avatar", []string{"users", "update", "usr_1", "--first-name", "A", "--avatar-url", "https://example.com/a.png"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{"id":"x","type":"x","attributes":{}}}`))
			}))
			t.Cleanup(srv.Close)

			res := runContract(t, baseEnv(srv.URL), tc.args...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("sent %d request(s), want 0 — a rejected flag must not reach the API", n)
			}
		})
	}
}

// create keeps --email: dropping it from update must not take it from create.
func TestContactsCreate_StillAcceptsEmail(t *testing.T) {
	if contactsCreateCmd.Flags().Lookup("email") == nil {
		t.Fatal("contacts create must keep --email")
	}
	if contactsUpdateCmd.Flags().Lookup("email") != nil {
		t.Fatal("contacts update must not define --email")
	}
	if usersUpdateCmd.Flags().Lookup("email") != nil {
		t.Fatal("users update must not define --email")
	}
	// MIO-4930: UserUpdate takes first_name, last_name, is_active only.
	if usersUpdateCmd.Flags().Lookup("avatar-url") != nil {
		t.Fatal("users update must not define --avatar-url")
	}
}
