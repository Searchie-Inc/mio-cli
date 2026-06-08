package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// countingServer wraps an httptest.Server with an atomic request counter so a
// test can assert that an id-passthrough resolution made ZERO API calls.
type countingServer struct {
	*httptest.Server
	calls atomic.Int64
}

func newCountingServer(t *testing.T, handler http.HandlerFunc) *countingServer {
	t.Helper()
	cs := &countingServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(cs.Close)
	return cs
}

// ─── id-shape helpers ──────────────────────────────────────────────────────────

func TestIsUUID(t *testing.T) {
	cases := map[string]bool{
		"019e864a-7b10-74e0-9922-c2c62f9925eb":  true,  // uuid7 (real production id)
		"550E8400-E29B-41D4-A716-446655440000":  true,  // uppercase
		"tag_abc123":                            false, // CLI prefix convention
		"My Community":                          false, // a name
		"019e864a7b1074e09922c2c62f9925eb":      false, // unhyphenated 32-hex (ambiguous)
		"019e864a-7b10-74e0-9922-c2c62f9925e":   false, // too short
		"019e864a-7b10-74e0-9922-c2c62f9925ebX": false, // too long
		"019e864a-7b10-74e0-9922-c2c62f9925eg":  false, // non-hex 'g'
		"":                                      false,
	}
	for in, want := range cases {
		if got := isUUID(in); got != want {
			t.Errorf("isUUID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsIDShaped(t *testing.T) {
	if !isIDShaped("team_abc", "team_") {
		t.Error("prefixed value should be id-shaped")
	}
	if !isIDShaped("019e864a-7b10-74e0-9922-c2c62f9925eb", "team_") {
		t.Error("UUID should be id-shaped regardless of prefix")
	}
	if isIDShaped("Acme Corp", "team_") {
		t.Error("a plain name should NOT be id-shaped")
	}
}

// ─── id passthrough: NO API call ────────────────────────────────────────────────

func TestResolveTeam_IDPassthrough_NoAPICall(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	// UUID id — passthrough.
	id, err := c.ResolveTeam(context.Background(), "019e864a-7b10-74e0-9922-c2c62f9925eb")
	if err != nil {
		t.Fatalf("ResolveTeam(uuid) err: %v", err)
	}
	if id != "019e864a-7b10-74e0-9922-c2c62f9925eb" {
		t.Errorf("uuid passthrough = %q", id)
	}

	// Prefixed id — passthrough.
	id, err = c.ResolveTeam(context.Background(), "team_abc123")
	if err != nil {
		t.Fatalf("ResolveTeam(prefix) err: %v", err)
	}
	if id != "team_abc123" {
		t.Errorf("prefix passthrough = %q", id)
	}

	if n := srv.calls.Load(); n != 0 {
		t.Errorf("id passthrough made %d API call(s), want 0", n)
	}
}

func TestResolveHub_IDPassthrough_NoAPICall(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveHub(context.Background(), "t1", "hub_xyz")
	if err != nil {
		t.Fatalf("ResolveHub(prefix) err: %v", err)
	}
	if id != "hub_xyz" {
		t.Errorf("hub passthrough = %q", id)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Errorf("hub id passthrough made %d API call(s), want 0", n)
	}
}

func TestResolveContactByEmail_IDPassthrough_NoAPICall(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveContactByEmail(context.Background(), "t1", "ctt_abc")
	if err != nil {
		t.Fatalf("ResolveContactByEmail(id) err: %v", err)
	}
	if id != "ctt_abc" {
		t.Errorf("contact id passthrough = %q", id)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Errorf("contact id passthrough made %d API call(s), want 0", n)
	}
}

// ─── resolve by name/slug (list + match) ────────────────────────────────────────

const teamsListBody = `{"data":[
  {"id":"019e0001-0000-7000-8000-000000000001","type":"teams","attributes":{"name":"Acme Corp","slug":"acme"}},
  {"id":"019e0002-0000-7000-8000-000000000002","type":"teams","attributes":{"name":"Beta Inc","slug":"beta"}}
]}`

func TestResolveTeam_ExactSlugMatch(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/teams" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(teamsListBody))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveTeam(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ResolveTeam(slug) err: %v", err)
	}
	if id != "019e0001-0000-7000-8000-000000000001" {
		t.Errorf("slug match = %q", id)
	}
}

func TestResolveTeam_NameMatch_CaseInsensitive(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(teamsListBody))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveTeam(context.Background(), "beta inc") // lowercased name
	if err != nil {
		t.Fatalf("ResolveTeam(name) err: %v", err)
	}
	if id != "019e0002-0000-7000-8000-000000000002" {
		t.Errorf("name match = %q", id)
	}
}

func TestResolveTeam_NotFound(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(teamsListBody))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	_, err := c.ResolveTeam(context.Background(), "Nonexistent")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
	if !strings.Contains(err.Error(), "no team named") || !strings.Contains(err.Error(), "mio teams list") {
		t.Errorf("not-found error not helpful: %v", err)
	}
}

func TestResolveHub_ByName(t *testing.T) {
	body := `{"data":[
	  {"id":"019eaaaa-0000-7000-8000-00000000000a","type":"hubs","attributes":{"title":"My Community","name":"My Community","slug":"my-community"}}
	]}`
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/teams/t1/hubs") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveHub(context.Background(), "t1", "My Community")
	if err != nil {
		t.Fatalf("ResolveHub(name) err: %v", err)
	}
	if id != "019eaaaa-0000-7000-8000-00000000000a" {
		t.Errorf("hub name match = %q", id)
	}
}

func TestResolveHub_BySlug(t *testing.T) {
	body := `{"data":[
	  {"id":"019eaaaa-0000-7000-8000-00000000000a","type":"hubs","attributes":{"name":"My Community","slug":"my-community"}}
	]}`
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveHub(context.Background(), "t1", "my-community")
	if err != nil {
		t.Fatalf("ResolveHub(slug) err: %v", err)
	}
	if id != "019eaaaa-0000-7000-8000-00000000000a" {
		t.Errorf("hub slug match = %q", id)
	}
}

func TestResolveTag_Ambiguous_ErrorsWithCandidates(t *testing.T) {
	// Two tags share the SAME name (slug differs) → name pass is ambiguous.
	body := `{"data":[
	  {"id":"019eb001-0000-7000-8000-00000000000b","type":"tags","attributes":{"name":"VIP","slug":"vip-1"}},
	  {"id":"019eb002-0000-7000-8000-00000000000c","type":"tags","attributes":{"name":"VIP","slug":"vip-2"}}
	]}`
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	_, err := c.ResolveTag(context.Background(), "t1", "VIP")
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	for _, must := range []string{"ambiguous", "019eb001-0000-7000-8000-00000000000b", "019eb002-0000-7000-8000-00000000000c", "raw id"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("ambiguity error missing %q: %v", must, err)
		}
	}
}

func TestResolveTag_SlugBeatsName(t *testing.T) {
	// A value that is one tag's slug AND another tag's name resolves to the
	// SLUG match (slug pass runs first and is unique).
	body := `{"data":[
	  {"id":"019eb010-0000-7000-8000-00000000001a","type":"tags","attributes":{"name":"alpha","slug":"vip"}},
	  {"id":"019eb011-0000-7000-8000-00000000001b","type":"tags","attributes":{"name":"vip","slug":"beta"}}
	]}`
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveTag(context.Background(), "t1", "vip")
	if err != nil {
		t.Fatalf("ResolveTag err: %v", err)
	}
	if id != "019eb010-0000-7000-8000-00000000001a" {
		t.Errorf("slug-first match = %q, want the slug owner", id)
	}
}

// ─── contact-by-email uses the SERVER filter ────────────────────────────────────

func TestResolveContactByEmail_UsesServerFilter(t *testing.T) {
	var gotFilter, gotPath string
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFilter = r.URL.Query().Get("filter[email]")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[
		  {"id":"019ec001-0000-7000-8000-00000000000d","type":"team-contacts","attributes":{"email":"alice@example.com"}}
		]}`))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	id, err := c.ResolveContactByEmail(context.Background(), "t1", "alice@example.com")
	if err != nil {
		t.Fatalf("ResolveContactByEmail err: %v", err)
	}
	if id != "019ec001-0000-7000-8000-00000000000d" {
		t.Errorf("contact email match = %q", id)
	}
	if gotPath != "/api/v1/teams/t1/contacts" {
		t.Errorf("path = %q, want /api/v1/teams/t1/contacts", gotPath)
	}
	if gotFilter != "alice@example.com" {
		t.Errorf("server filter[email] = %q, want alice@example.com", gotFilter)
	}
}

func TestResolveContactByEmail_NotFound(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	_, err := c.ResolveContactByEmail(context.Background(), "t1", "ghost@example.com")
	if err == nil {
		t.Fatal("expected not-found, got nil")
	}
	if !strings.Contains(err.Error(), "no contact with email") {
		t.Errorf("not-found error: %v", err)
	}
}

// ─── cache: a repeat name lookup lists at most once ──────────────────────────────

func TestResolve_CachesNameLookup(t *testing.T) {
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(teamsListBody))
	})
	c := New(srv.URL, "k", WithHTTPClient(srv.Client()))

	for i := 0; i < 3; i++ {
		if _, err := c.ResolveTeam(context.Background(), "acme"); err != nil {
			t.Fatalf("ResolveTeam #%d err: %v", i, err)
		}
	}
	if n := srv.calls.Load(); n != 1 {
		t.Errorf("repeated name resolution made %d API calls, want 1 (cached)", n)
	}
}

// ─── empty input guard ──────────────────────────────────────────────────────────

func TestResolve_EmptyValue(t *testing.T) {
	c := New("http://unused", "k")
	if _, err := c.ResolveTeam(context.Background(), "   "); err == nil {
		t.Error("ResolveTeam(empty) should error")
	}
	if _, err := c.ResolveHub(context.Background(), "t1", ""); err == nil {
		t.Error("ResolveHub(empty) should error")
	}
	if _, err := c.ResolveContactByEmail(context.Background(), "t1", ""); err == nil {
		t.Error("ResolveContactByEmail(empty) should error")
	}
}
