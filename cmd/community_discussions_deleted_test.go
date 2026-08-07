package cmd

// community_discussions_deleted_test.go — MIO-3022.
//
// The admin discussions views return soft-deleted rows on purpose, and the API's
// computed `status` has no "deleted" value — so a tombstoned discussion comes
// back as `status: "published"` with a non-null `deleted_at`. Operators read the
// status, conclude the delete failed, retry, and get a legitimate 404. The delete
// verb was never broken; the reads could not show its effect.
//
// The oracle here is the RENDERED OUTPUT, not the helper: these drive the real
// commands so a verb that forgets to call the helper fails.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// discussionBody renders one discussion resource. deletedAt "" means the JSON
// null a live row carries; "-" means the key is absent entirely.
func discussionBody(id, deletedAt string) string {
	da := "null"
	switch deletedAt {
	case "-":
		return fmt.Sprintf(`{"id":%q,"type":"discussions","attributes":{"title":"t","status":"published"}}`, id)
	case "":
	default:
		da = fmt.Sprintf("%q", deletedAt)
	}
	return fmt.Sprintf(
		`{"id":%q,"type":"discussions","attributes":{"title":"t","status":"published","deleted_at":%s}}`, id, da)
}

func discussionServer(t *testing.T, single, list string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/discussions") {
			_, _ = fmt.Fprintf(w, `{"data":[%s]}`, list)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":%s}`, single)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func decodeOne(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not JSON (%v): %q", err, out)
	}
	return m
}

// A soft-deleted discussion must say so. This is the whole bug.
func TestDiscussions_SoftDeletedRowIsMarkedDeleted(t *testing.T) {
	srv := discussionServer(t, discussionBody("d_1", "2026-08-07T13:17:43Z"), "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_1", "community", "discussions", "retrieve", "d_1", "--hub", "h_1", "-o", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	got := decodeOne(t, res.Stdout)
	if got["deleted"] != true {
		t.Errorf("deleted = %v, want true — the row carries a non-null deleted_at; without this an operator "+
			"reads status %q and concludes the delete failed", got["deleted"], got["status"])
	}
	// status must be left ALONE: it records what the row was before deletion.
	if got["status"] != "published" {
		t.Errorf("status = %v, want the API's own value untouched", got["status"])
	}
}

// ...and a live row must not be mislabelled.
func TestDiscussions_LiveRowIsNotMarkedDeleted(t *testing.T) {
	srv := discussionServer(t, discussionBody("d_1", ""), "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_1", "community", "discussions", "retrieve", "d_1", "--hub", "h_1", "-o", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if got := decodeOne(t, res.Stdout); got["deleted"] != false {
		t.Errorf("deleted = %v, want false for a null deleted_at", got["deleted"])
	}
}

// An ABSENT deleted_at must not become a synthesized false. That is the
// fail-closed mistake MIO-2991 made: an invented value is indistinguishable from
// a real one, so the caller cannot tell "not deleted" from "server didn't say".
func TestDiscussions_AbsentDeletedAtInjectsNothing(t *testing.T) {
	srv := discussionServer(t, discussionBody("d_1", "-"), "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_1", "community", "discussions", "retrieve", "d_1", "--hub", "h_1", "-o", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if _, present := decodeOne(t, res.Stdout)["deleted"]; present {
		t.Errorf("deleted must be ABSENT when the server sent no deleted_at — a synthesized false reads as "+
			"a real answer; stdout=%q", res.Stdout)
	}
}

// EVERY verb that renders a discussion must derive it. MIO-2991 was exactly this
// shape one resource over: retrieve/create/update had the derived fields and
// `list` did not, and `list` is what automation enumerates with.
func TestDiscussions_EveryVerbDerivesDeleted(t *testing.T) {
	del := discussionBody("d_1", "2026-08-07T13:17:43Z")

	for _, tc := range []struct {
		name string
		args []string
		list bool
	}{
		{"retrieve", []string{"community", "discussions", "retrieve", "d_1", "--hub", "h_1", "-o", "json"}, false},
		{"list", []string{"community", "discussions", "list", "--hub", "h_1", "-o", "json"}, true},
		{"create", []string{"community", "discussions", "create", "--hub", "h_1", "--space-id", "s_1",
			"--title", "x", "--body", "y", "-o", "json"}, false},
		{"update", []string{"community", "discussions", "update", "d_1", "--hub", "h_1", "--is-pinned=true", "-o", "json"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := discussionServer(t, del, del)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_1", tc.args...)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
			}
			if tc.list {
				var rows []map[string]any
				if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
					t.Fatalf("list stdout is not a JSON array: %v; %q", err, res.Stdout)
				}
				if len(rows) != 1 || rows[0]["deleted"] != true {
					t.Errorf("%s did not derive `deleted` — automation enumerates with list, so this is the "+
						"half that matters most; rows=%v", tc.name, rows)
				}
				return
			}
			if decodeOne(t, res.Stdout)["deleted"] != true {
				t.Errorf("%s did not derive `deleted`; stdout=%q", tc.name, res.Stdout)
			}
		})
	}
}

// --raw is the escape hatch to the untouched envelope; a derived field must not
// leak into it.
func TestDiscussions_RawBypassesTheDerivedField(t *testing.T) {
	srv := discussionServer(t, discussionBody("d_1", "2026-08-07T13:17:43Z"), "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_1", "community", "discussions", "retrieve", "d_1", "--hub", "h_1", "--raw")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stdout, `"deleted"`) {
		t.Errorf("--raw must render the API's own envelope, without derived fields; stdout=%q", res.Stdout)
	}
}
