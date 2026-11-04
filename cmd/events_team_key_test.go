package cmd

// events_team_key_test.go — MIO-3610: events authoring (create/update/cancel)
// with a team API key, no MIO_CONTACT_TOKEN. The oracle is the wire: method,
// path, Authorization bearer, envelope type and body of every request, plus the
// number of requests when something must be refused locally.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type seenReq struct {
	method, path, bearer string
	body                 []byte
}

type recordingServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	seen   []seenReq
	status int // status to answer (0 = 200)
}

func newRecordingServer(t *testing.T, status int) *recordingServer {
	t.Helper()
	rs := &recordingServer{status: status}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStrayProbe(r) { // the dev box's port prober, not a request the test sent
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		rs.seen = append(rs.seen, seenReq{r.Method, r.URL.Path, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), b})
		rs.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if rs.status >= 400 {
			w.WriteHeader(rs.status)
			_, _ = w.Write([]byte(`{"errors":[{"status":"` + http.StatusText(rs.status) + `","detail":"Insufficient permissions."}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"evt_1","type":"hub_events","attributes":{"title":"t"}}}`))
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *recordingServer) only(t *testing.T) seenReq {
	t.Helper()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.seen) != 1 {
		t.Fatalf("requests = %+v, want exactly one", rs.seen)
	}
	return rs.seen[0]
}

func (rs *recordingServer) count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.seen)
}

func bodyTypeAttrs(t *testing.T, b []byte) (typ string, attrs map[string]any) {
	t.Helper()
	var env struct {
		Data struct {
			Type       string         `json:"type"`
			ID         string         `json:"id"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("body %q is not a JSON:API envelope: %v", b, err)
	}
	return env.Data.Type, env.Data.Attributes
}

const adminBase = "/api/v1/admin/teams/t_team1/hubs/hub_123/events"
const memberBase = "/api/v1/hubs/hub_123/events"

var createArgs = []string{"--hub", "hub_123", "events", "create", "--title", "Meetup",
	"--starts-at", "2026-09-01T18:00:00Z", "--ends-at", "2026-09-01T20:00:00Z",
	"--timezone", "America/New_York", "--location-type", "url", "--location-url", "https://example.com/j"}

func TestEventsAuthoring_TeamKey_UsesTheAdminRoute(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "")
	for _, tc := range []struct {
		name         string
		args         []string
		method, path string
		wantType     string // "" = no body expected
	}{
		{"create", createArgs, "POST", adminBase, "hub_events"},
		{"update", []string{"--hub", "hub_123", "events", "update", "evt_1", "--title", "Renamed"}, "PATCH", adminBase + "/evt_1", "hub_events"},
		{"cancel", []string{"--hub", "hub_123", "events", "cancel", "evt_1", "--yes"}, "POST", adminBase + "/evt_1/cancel", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := newRecordingServer(t, 0)
			res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", tc.args...)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			r := rs.only(t)
			if r.method != tc.method || r.path != tc.path {
				t.Errorf("request = %s %s, want %s %s", r.method, r.path, tc.method, tc.path)
			}
			if r.bearer != "test-key-contract" {
				t.Errorf("bearer = %q, want the team API key (no contact token was set)", r.bearer)
			}
			if tc.wantType == "" {
				if len(r.body) != 0 {
					t.Errorf("cancel on the team-key route must send no body, got %s", r.body)
				}
				return
			}
			typ, _ := bodyTypeAttrs(t, r.body)
			if typ != tc.wantType {
				t.Errorf("envelope type = %q, want %q", typ, tc.wantType)
			}
		})
	}
}

func TestEventsAuthoring_ContactToken_KeepsTheMemberRoute(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "member-token")
	for _, tc := range []struct {
		name         string
		args         []string
		method, path string
	}{
		{"create", createArgs, "POST", memberBase},
		{"update", []string{"--hub", "hub_123", "events", "update", "evt_1", "--title", "Renamed"}, "PATCH", memberBase + "/evt_1"},
		{"cancel", []string{"--hub", "hub_123", "events", "cancel", "evt_1", "--yes"}, "POST", memberBase + "/evt_1/cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := newRecordingServer(t, 0)
			res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", tc.args...)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
			}
			r := rs.only(t)
			if r.method != tc.method || r.path != tc.path || r.bearer != "member-token" {
				t.Errorf("request = %s %s bearer %q, want %s %s with the contact token (an explicit token keeps the member route)", r.method, r.path, r.bearer, tc.method, tc.path)
			}
		})
	}
}

func TestEventsAuthoring_Anonymous_KeepsTheMemberRouteWithNoAuth(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "")
	rs := newRecordingServer(t, 0)
	res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", append([]string{"--anonymous"}, createArgs...)...)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	r := rs.only(t)
	if r.path != memberBase || r.bearer != "" {
		t.Errorf("request = %s bearer %q, want the member route with no Authorization under --anonymous", r.path, r.bearer)
	}
}

func TestEventsAuthoring_CancelMessage_RefusedOnTheTeamKeyRoute(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "")
	rs := newRecordingServer(t, 0)
	res, err := runResume(t, baseEnv(rs.srv.URL), withTeam("t_team1", "--hub", "hub_123", "events", "cancel", "evt_1", "--yes", "--message", "Venue flooded")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d (err=%v), want 2: the admin cancel takes no body, so the message would be silently dropped", res.Code, err)
	}
	if rs.count() != 0 {
		t.Errorf("a request was sent although --message cannot be honoured: %+v", rs.seen)
	}
	if err == nil || !strings.Contains(err.Error(), eventsContactTokenEnv) {
		t.Errorf("the refusal must name the way out (%s); got %v", eventsContactTokenEnv, err)
	}
}

func TestEventsAuthoring_Forbidden_ExplainsTheFlag(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "")
	rs := newRecordingServer(t, http.StatusForbidden)
	res, err := runResume(t, baseEnv(rs.srv.URL), withTeam("t_team1", createArgs...)...)
	if res.Code != errs.ExitAuth {
		t.Fatalf("exit = %d, want 3 (a 403 keeps its exit code); err=%v", res.Code, err)
	}
	// The recovery command (api-keys update/create) is refused for an API key by
	// the backend, so the hint must say what credential it needs (codex review).
	for _, want := range []string{"can_author_events", "mio api-keys update", "--can-author-events", eventsContactTokenEnv, "USER access token", "/api/v1/auth/login", "--api-key"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the 403 hint must mention %q; got %v", want, err)
		}
	}
	// On the contact-token route the same 403 is NOT about the key flag.
	t.Setenv(eventsContactTokenEnv, "member-token")
	rs2 := newRecordingServer(t, http.StatusForbidden)
	_, err = runResume(t, baseEnv(rs2.srv.URL), withTeam("t_team1", createArgs...)...)
	if err == nil || strings.Contains(err.Error(), "can_author_events") {
		t.Errorf("the member-route 403 must not blame the API key flag; got %v", err)
	}
}

func TestEventsAuthoring_ReadsAndRSVPStillNeedTheContactToken(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "")
	for _, args := range [][]string{
		{"--hub", "hub_123", "events", "list"},
		{"--hub", "hub_123", "events", "retrieve", "evt_1"},
		{"--hub", "hub_123", "events", "rsvp", "set", "evt_1", "--status", "going"},
		{"--hub", "hub_123", "events", "rsvps", "list", "evt_1"},
	} {
		rs := newRecordingServer(t, 0)
		res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", args...)...)
		if res.Code != errs.ExitAuth || rs.count() != 0 {
			t.Errorf("%v: exit=%d requests=%d, want exit 3 and none (RSVP/reads stay on the member route)", args, res.Code, rs.count())
		}
	}
}

// ── api-keys: the verbs that make the 403 hint actionable ────────────────────

func TestAPIKeys_PermissionFlags(t *testing.T) {
	t.Run("create sends a flag only when it was passed", func(t *testing.T) {
		rs := newRecordingServer(t, 0)
		res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", "api-keys", "create", "--name", "bot", "--can-author-events")...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
		}
		var body map[string]any
		_ = json.Unmarshal(rs.only(t).body, &body)
		if body["can_author_events"] != true {
			t.Errorf("body = %v, want can_author_events true", body)
		}
		if _, present := body["can_moderate"]; present {
			t.Errorf("can_moderate was not passed and must not be sent: %v", body)
		}
	})
	t.Run("update PATCHes only the passed flags, flat; =false is sent", func(t *testing.T) {
		rs := newRecordingServer(t, 0)
		res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", "api-keys", "update", "apk_1", "--can-author-events=false")...)
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
		}
		r := rs.only(t)
		if r.method != "PATCH" || r.path != "/api/v1/teams/t_team1/api-keys/apk_1" {
			t.Errorf("request = %s %s", r.method, r.path)
		}
		var body map[string]any
		_ = json.Unmarshal(r.body, &body)
		if v, ok := body["can_author_events"]; !ok || v != false {
			t.Errorf("body = %v, want an explicit can_author_events:false (a revoke must be sent)", body)
		}
		if _, present := body["can_moderate"]; present || len(body) != 1 {
			t.Errorf("only the passed flag may be sent: %v", body)
		}
	})
	t.Run("nothing to update and a blank id fire no request", func(t *testing.T) {
		rs := newRecordingServer(t, 0)
		for _, args := range [][]string{{"api-keys", "update", "apk_1"}, {"api-keys", "update", " ", "--can-moderate"}} {
			res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", args...)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("%v: exit = %d, want 2", args, res.Code)
			}
		}
		if rs.count() != 0 {
			t.Errorf("requests fired for a usage error: %+v", rs.seen)
		}
	})
}

// With neither a contact token nor any API key there is nothing to author with:
// exit 3 before a request (the admin route must not be tried unauthenticated).
func TestEventsAuthoring_NoCredentialsAtAll_FailsBeforeAnyRequest(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rs := newRecordingServer(t, 0)
	res := runContract(t, []string{"MIO_API_BASE_URL=" + rs.srv.URL, "MIO_API_KEY="}, withTeam("t_team1", createArgs...)...)
	if res.Code != errs.ExitAuth || rs.count() != 0 {
		t.Errorf("exit=%d requests=%d, want exit 3 and none", res.Code, rs.count())
	}
}

// The admin route must carry exactly the attributes the member route would for
// the same flags: the oracle is the member-route body for identical args, not a
// second hand-written list, so a flag dropped on the admin path (or added to
// only one path) fails here.
func TestEventsAuthoring_AdminRouteCarriesTheSameAttributesAsTheMemberRoute(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"create", createArgs},
		{"update", []string{"--hub", "hub_123", "events", "update", "evt_1", "--title", "Renamed", "--capacity", "40", "--visibility", "members", "--description", "d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(eventsContactTokenEnv, "")
			adminSrv := newRecordingServer(t, 0)
			if res := runContract(t, baseEnv(adminSrv.srv.URL), withTeam("t_team1", tc.args...)...); res.Code != errs.ExitOK {
				t.Fatalf("admin exit = %d; %q", res.Code, res.Stderr)
			}
			t.Setenv(eventsContactTokenEnv, "member-token")
			memberSrv := newRecordingServer(t, 0)
			if res := runContract(t, baseEnv(memberSrv.srv.URL), withTeam("t_team1", tc.args...)...); res.Code != errs.ExitOK {
				t.Fatalf("member exit = %d; %q", res.Code, res.Stderr)
			}
			_, adminAttrs := bodyTypeAttrs(t, adminSrv.only(t).body)
			_, memberAttrs := bodyTypeAttrs(t, memberSrv.only(t).body)
			if len(memberAttrs) < 2 {
				t.Fatalf("member-route attrs %v: the oracle is too thin to prove anything", memberAttrs)
			}
			a, _ := json.Marshal(adminAttrs)
			m, _ := json.Marshal(memberAttrs)
			if string(a) != string(m) {
				t.Errorf("admin attrs %s != member attrs %s", a, m)
			}
		})
	}
}

// A blank or whitespace-only MIO_CONTACT_TOKEN is "no token": it must select the
// admin route; a non-blank one (even padded) must select the member route.
func TestEventsAuthoring_ContactTokenBlankVersusSet(t *testing.T) {
	for _, tc := range []struct {
		token, wantPath, wantBearer string
	}{
		{"   ", adminBase, "test-key-contract"},
		{"", adminBase, "test-key-contract"},
		{"  member-token  ", memberBase, "member-token"},
	} {
		t.Run("token="+strings.ReplaceAll(tc.token, " ", "_"), func(t *testing.T) {
			t.Setenv(eventsContactTokenEnv, tc.token)
			rs := newRecordingServer(t, 0)
			if res := runContract(t, baseEnv(rs.srv.URL), withTeam("t_team1", createArgs...)...); res.Code != errs.ExitOK {
				t.Fatalf("exit = %d; %q", res.Code, res.Stderr)
			}
			r := rs.only(t)
			if r.path != tc.wantPath || r.bearer != tc.wantBearer {
				t.Errorf("request = %s bearer %q, want %s bearer %q", r.path, r.bearer, tc.wantPath, tc.wantBearer)
			}
		})
	}
}
