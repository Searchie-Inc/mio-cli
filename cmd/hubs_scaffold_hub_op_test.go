package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type hubOpCapture struct {
	opPosts     [][]byte
	opKeys      []string
	clientPosts []string
	hubCreates  int
}

func hubOpScaffoldServer(t *testing.T, opStatus int, opBody string) (*httptest.Server, *hubOpCapture) {
	t.Helper()
	return hubOpScaffoldServerWithCatalog(t, opStatus, opBody, catalog21Body(t))
}

func hubOpScaffoldServerWithCatalog(t *testing.T, opStatus int, opBody string, catBody []byte) (*httptest.Server, *hubOpCapture) {
	t.Helper()
	rec := &hubOpCapture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		path := r.URL.Path

		if r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs/from-template") {
			body, _ := io.ReadAll(r.Body)
			rec.opPosts = append(rec.opPosts, body)
			rec.opKeys = append(rec.opKeys, r.Header.Get("Idempotency-Key"))
			if opStatus == http.StatusMethodNotAllowed {
				w.Header().Set("Allow", "GET")
			}
			w.WriteHeader(opStatus)
			_, _ = w.Write([]byte(opBody))
			return
		}
		if r.Method == http.MethodPost {
			rec.clientPosts = append(rec.clientPosts, path)
		}

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/scaffold-from-template"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/hubs"):
			rec.hubCreates++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hubs","attributes":{"slug":"my-community","is_private":true}}}`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"res_new","type":"resources","attributes":{"slug":"home","is_homepage":true}}}`))
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hubs","attributes":{"slug":"my-community","is_private":false}}}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"pdt_1","type":"page_draft_trees","attributes":{"draft_version":1}}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/hubs/hub_new"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hubs","attributes":{"slug":"my-community","title":"Founders","is_private":false,"branding":{"primary":"#000"}}}}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

const hubOpLiveBody = `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
  "hub_id":"hub_new",
  "summary":[
    {"resource":"hub","action":"created"},
    {"resource":"branding","action":"created"},
    {"resource":"settings","action":"created"},
    {"resource":"navigation","action":"created"},
    {"resource":"space:general","action":"created"},
    {"resource":"space:announcements","action":"created"},
    {"resource":"onboarding:company","action":"created"},
    {"resource":"onboarding:role","action":"skipped","reason":"a team attribute definition with this slug already exists — reused"},
    {"resource":"policy:terms","action":"created"},
    {"resource":"policy_gate","action":"created"},
    {"resource":"page:homepage","action":"created"},
    {"resource":"page:about","action":"created"},
    {"resource":"page:faq","action":"created"},
    {"resource":"publish","action":"created"}
  ],
  "created_resource_ids":{
    "hubs":["hub_new"],
    "spaces":["sp_general","sp_announcements"],
    "contact_attribute_definitions":["def_company"],
    "pages":["pg_home","pg_about","pg_faq"]
  },
  "replayed":false}}}`

func scaffoldArgs(extra ...string) []string {
	base := []string{"hubs", "scaffold", "--template", "community",
		"--name", "Founders", "--slug", "founders", "--output", "json"}
	return withTeam("t_team1", append(base, extra...)...)
}

func TestScaffoldHubOp_AbsentOpFallsBackToClientPipeline(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusMethodNotAllowed, `{"detail":"Method Not Allowed"}`)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.opPosts) != 1 {
		t.Fatalf("the op must be PROBED exactly once (the probe IS the POST); got %d", len(rec.opPosts))
	}
	if rec.hubCreates != 1 {
		t.Errorf("client-side hub create fired %d times, want 1 — an absent op MUST fall back", rec.hubCreates)
	}
}

func TestScaffoldHubOp_LiveOpSkipsTheClientPipeline(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.opPosts) != 1 {
		t.Fatalf("op POSTs = %d, want 1", len(rec.opPosts))
	}
	if rec.hubCreates != 0 {
		t.Errorf("the op built the hub — the client-side create must NOT fire; got %d", rec.hubCreates)
	}
	if len(rec.clientPosts) != 0 {
		t.Errorf("no client-side write may follow a successful op; got %v", rec.clientPosts)
	}
}

func TestScaffoldHubOp_NonAbsenceNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"server error", http.StatusInternalServerError, `{"errors":[{"detail":"boom"}]}`, errs.ExitServer},
		{"template not found", http.StatusNotFound, `{"errors":[{"code":"template_not_found","detail":"nope"}]}`, errs.ExitNotFound},
		{"digest mismatch", http.StatusConflict, `{"errors":[{"code":"catalog_digest_mismatch","detail":"stale"}]}`, errs.ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := hubOpScaffoldServer(t, tc.status, tc.body)

			res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
			if res.Code != tc.want {
				t.Errorf("exit = %d, want %d; stderr=%q", res.Code, tc.want, res.Stderr)
			}
			if rec.hubCreates != 0 {
				t.Errorf("status %d must NOT fall back — a client-side apply against a backend that HAS the op smears partial state; hub creates=%d",
					tc.status, rec.hubCreates)
			}
		})
	}
}

func TestScaffoldHubOp_FingerprintMismatchExplainsItself(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusConflict,
		`{"errors":[{"code":"idempotency_fingerprint_mismatch","detail":"key reused with a different request"}]}`)

	err := executeCLI(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if err == nil {
		t.Fatal("a fingerprint mismatch must fail the run")
	}
	for _, want := range []string{"Nothing was applied", "--name/--slug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("guidance must contain %q; err=%v", want, err)
		}
	}
	if rec.hubCreates != 0 {
		t.Errorf("a fingerprint mismatch must not fall back — the backend refused a DIFFERENT request under a used key; hub creates=%d", rec.hubCreates)
	}

	if res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...); res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want %d (ExitUsage)", res.Code, errs.ExitUsage)
	}
}

func TestScaffoldHubOp_UnsupportedFlagsForceClientPath(t *testing.T) {
	for _, tc := range []struct{ name, flag, value string }{
		{"branding blob", "--branding-json", `{"primary":"#B91C1C"}`},
		{"palette scalar", "--primary-color", "#B91C1C"},
		{"social image", "--social-image-url", "https://cdn/og.png"},
		{"local catalog", "--catalog", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

			value := tc.value
			if tc.flag == "--catalog" {
				value = writeTempCatalog(t, catalog21Body(t))
			}

			res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs(tc.flag, value)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if len(rec.opPosts) != 0 {
				t.Errorf("%s cannot be expressed in overrides{} — the op must not be probed; got %d POST(s)", tc.flag, len(rec.opPosts))
			}
			if rec.hubCreates != 1 {
				t.Errorf("%s must force the CLIENT-side pipeline; hub creates=%d", tc.flag, rec.hubCreates)
			}
			if !strings.Contains(res.Stderr, tc.flag) {
				t.Errorf("the run must say WHICH flag pushed it off the op path; stderr=%q", res.Stderr)
			}
		})
	}
}

func TestScaffoldHubOp_SupportedOverridesReachTheWire(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs(
		"--logo-url", "https://cdn/logo.png",
		"--favicon-url", "https://cdn/fav.ico",
		"--registration-enabled",
		"--publish")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.opPosts) != 1 {
		t.Fatalf("op POSTs = %d, want 1 — these four flags ARE expressible", len(rec.opPosts))
	}
	ov := hubOpOverrides(t, rec.opPosts[0])
	for k, want := range map[string]any{
		"logo_url":             "https://cdn/logo.png",
		"favicon_url":          "https://cdn/fav.ico",
		"registration_enabled": true,
		"publish":              true,
	} {
		if ov[k] != want {
			t.Errorf("overrides[%q] = %v, want %v", k, ov[k], want)
		}
	}
}

func TestScaffoldHubOp_UnsetOverridesAreOmitted(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	if res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...); res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	ov := hubOpOverrides(t, rec.opPosts[0])
	for _, k := range []string{"logo_url", "favicon_url", "registration_enabled"} {
		if _, present := ov[k]; present {
			t.Errorf("overrides[%q] must be omitted when the flag was not passed; overrides=%v", k, ov)
		}
	}
}

func TestScaffoldHubOp_ResumeModeNeverProbesTheOp(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	res := runContract(t, scaffoldEnv(t, srv.URL),
		withTeam("t_team1", "hubs", "scaffold", "--template", "community",
			"--hub", "hub_new", "--name", "Founders", "--slug", "founders",
			"--output", "json")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.opPosts) != 0 {
		t.Errorf("--hub is create-mode-incompatible — probing the op would CREATE A SECOND HUB; got %d POST(s)", len(rec.opPosts))
	}
}

func TestScaffoldHubOp_DryRunNeverProbesTheOp(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs("--dry-run")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if len(rec.opPosts) != 0 {
		t.Fatalf("--dry-run POSTed to the op — that CREATES the hub a dry run promised not to create; got %d", len(rec.opPosts))
	}
	if rec.hubCreates != 0 {
		t.Errorf("a dry run must fire no writes at all; hub creates=%d", rec.hubCreates)
	}
}

func TestScaffoldHubOp_IdempotencyKeyIsDeterministicAcrossRuns(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	for i := 0; i < 2; i++ {
		if res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...); res.Code != errs.ExitOK {
			t.Fatalf("run %d: exit = %d; stderr=%q", i, res.Code, res.Stderr)
		}
	}
	if len(rec.opKeys) != 2 {
		t.Fatalf("op keys = %d, want 2", len(rec.opKeys))
	}
	if rec.opKeys[0] == "" {
		t.Fatal("the op 400s without an Idempotency-Key")
	}
	if rec.opKeys[0] != rec.opKeys[1] {
		t.Errorf("the same command must reuse its key (%q vs %q) — a fresh key per run is exactly the duplicate-hub bug",
			rec.opKeys[0], rec.opKeys[1])
	}
}

func TestScaffoldHubOp_IdempotencyKeyVariesWithIdentity(t *testing.T) {
	base := catalog.CreateApplicationID("t_1", "community", "Founders", "founders")
	for _, tc := range []struct{ name, team, tmpl, hubName, slug string }{
		{"different slug", "t_1", "community", "Founders", "other"},
		{"different name", "t_1", "community", "Other", "founders"},
		{"different team", "t_2", "community", "Founders", "founders"},
		{"different template", "t_1", "starter", "Founders", "founders"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalog.CreateApplicationID(tc.team, tc.tmpl, tc.hubName, tc.slug); got == base {
				t.Errorf("%s must yield a DIFFERENT key — colliding here replays someone else's hub", tc.name)
			}
		})
	}
}

func TestScaffoldHubOp_ResultCarriesPerSlugIDs(t *testing.T) {
	srv, _ := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	out := decodeScaffoldJSON(t, res.Stdout)

	if out["hub_id"] != "hub_new" {
		t.Errorf("hub_id = %v, want hub_new", out["hub_id"])
	}
	if out["hub_slug"] != "my-community" {
		t.Errorf("hub_slug = %v, want my-community (the slug the server actually assigned)", out["hub_slug"])
	}
	if out["hub_path"] != "/my-community" {
		t.Errorf("hub_path = %v, want /my-community", out["hub_path"])
	}
	if out["published"] != true {
		t.Errorf("published = %v, want true", out["published"])
	}
	if out["homepage_page_id"] != "pg_home" {
		t.Errorf("homepage_page_id = %v, want pg_home", out["homepage_page_id"])
	}

	wantPages := map[string]any{"homepage": "pg_home", "about": "pg_about", "faq": "pg_faq"}
	for _, raw := range out["pages"].([]any) {
		p := raw.(map[string]any)
		if got, want := p["page_id"], wantPages[p["slug"].(string)]; got != want {
			t.Errorf("pages[%v].page_id = %v, want %v", p["slug"], got, want)
		}
	}
	wantSpaces := map[string]any{"general": "sp_general", "announcements": "sp_announcements"}
	for _, raw := range out["spaces"].([]any) {
		s := raw.(map[string]any)
		if got, want := s["space_id"], wantSpaces[s["slug"].(string)]; got != want {
			t.Errorf("spaces[%v].space_id = %v, want %v", s["slug"], got, want)
		}
	}
	wantDefs := map[string]any{"company": "def_company", "role": nil}
	for _, raw := range out["onboarding_attributes"].([]any) {
		d := raw.(map[string]any)
		if got, want := d["definition_id"], wantDefs[d["slug"].(string)]; got != want {
			t.Errorf("onboarding[%v].definition_id = %v, want %v — a skipped row appends NO id and must not shift the pairing",
				d["slug"], got, want)
		}
	}
}

func TestScaffoldHubOp_JSONShapeMatchesTheClientPath(t *testing.T) {
	opSrv, _ := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)
	cliSrv, _ := hubOpScaffoldServer(t, http.StatusMethodNotAllowed, `{"detail":"Method Not Allowed"}`)

	opRes := runContract(t, scaffoldEnv(t, opSrv.URL), scaffoldArgs()...)
	cliRes := runContract(t, scaffoldEnv(t, cliSrv.URL), scaffoldArgs()...)
	if opRes.Code != errs.ExitOK || cliRes.Code != errs.ExitOK {
		t.Fatalf("both paths must succeed; op=%d client=%d", opRes.Code, cliRes.Code)
	}

	opOut, cliOut := decodeScaffoldJSON(t, opRes.Stdout), decodeScaffoldJSON(t, cliRes.Stdout)
	if len(opOut) != len(cliOut) {
		t.Errorf("key COUNT differs: op=%d client=%d", len(opOut), len(cliOut))
	}
	for k := range cliOut {
		if _, ok := opOut[k]; !ok {
			t.Errorf("op path is missing result key %q — an agent parsing one path breaks on the other", k)
		}
	}
	for _, k := range []string{"hub_id", "hub_slug", "hub_path", "hub_name", "template_id"} {
		if opOut[k] != cliOut[k] {
			t.Errorf("%s: op=%v client=%v — the two paths built the same hub and must describe it identically",
				k, opOut[k], cliOut[k])
		}
	}
}

func TestScaffoldHubOp_IDCountMismatchDropsIDsRatherThanMisattributing(t *testing.T) {
	const skewed = `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
	  "hub_id":"hub_new",
	  "summary":[
	    {"resource":"hub","action":"created"},
	    {"resource":"page:homepage","action":"created"},
	    {"resource":"page:about","action":"created"},
	    {"resource":"page:faq","action":"created"}
	  ],
	  "created_resource_ids":{"hubs":["hub_new"],"pages":["pg_home","pg_about"]},
	  "replayed":false}}}`

	srv, _ := hubOpScaffoldServer(t, http.StatusCreated, skewed)
	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 — a skewed listing is not a run failure; stderr=%q", res.Code, res.Stderr)
	}
	out := decodeScaffoldJSON(t, res.Stdout)
	for _, raw := range out["pages"].([]any) {
		p := raw.(map[string]any)
		if p["page_id"] != nil {
			t.Errorf("pages[%v].page_id = %v, want null — with the counts disagreeing, ANY attribution is a guess",
				p["slug"], p["page_id"])
		}
	}
	if !strings.Contains(res.Stderr, "not recorded") {
		t.Errorf("dropping ids must be disclosed; stderr=%q", res.Stderr)
	}
}

func TestScaffoldHubOp_ReplayIsReportedNotReapplied(t *testing.T) {
	const replay = `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
	  "hub_id":"hub_new",
	  "summary":[{"resource":"hub","action":"created"},{"resource":"page:homepage","action":"created"}],
	  "created_resource_ids":{},
	  "replayed":true}}}`

	srv, rec := hubOpScaffoldServer(t, http.StatusCreated, replay)
	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if rec.hubCreates != 0 {
		t.Errorf("a replay must NOT trigger a client-side re-apply; hub creates=%d", rec.hubCreates)
	}
	if out := decodeScaffoldJSON(t, res.Stdout); out["hub_id"] != "hub_new" {
		t.Errorf("hub_id = %v, want hub_new — a replay still identifies the hub", out["hub_id"])
	}
	if !strings.Contains(res.Stderr, "already scaffolded") {
		t.Errorf("a replay must be disclosed — it is how the operator knows no duplicate was made; stderr=%q", res.Stderr)
	}
	if strings.Contains(res.Stderr, "not recorded") {
		t.Errorf("a replay's empty created_resource_ids is by design and must not be reported as a backend anomaly; stderr=%q", res.Stderr)
	}
}

func TestScaffoldHubOp_SkippedRowsBecomeOperatorNotes(t *testing.T) {
	srv, _ := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "onboarding:role") ||
		!strings.Contains(res.Stderr, "already exists") {
		t.Errorf("the skip and its reason must reach the operator; stderr=%q", res.Stderr)
	}
	if strings.Contains(res.Stdout, "onboarding:role skipped") {
		t.Errorf("notes belong on stderr — stdout carries the rendered result only; stdout=%q", res.Stdout)
	}
}

func hubOpOverrides(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("op body is not JSON: %v", err)
	}
	data, _ := env["data"].(map[string]any)
	attrs, _ := data["attributes"].(map[string]any)
	ov, ok := attrs["overrides"].(map[string]any)
	if !ok {
		t.Fatalf("op body carries no overrides block: %s", body)
	}
	return ov
}

func decodeScaffoldJSON(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not JSON (%v): %q", err, stdout)
	}
	return out
}

func writeTempCatalog(t *testing.T, body []byte) string {
	t.Helper()
	p := fmt.Sprintf("%s/catalog.json", t.TempDir())
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("write temp catalog: %v", err)
	}
	return p
}

func TestScaffoldHubOp_PublishStateFallsBackToTheSummaryRow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		row           string
		wantPublished bool
	}{
		{"published", `{"resource":"publish","action":"created"}`, true},
		{"stayed private", `{"resource":"publish","action":"skipped","reason":"the hub stays private (pass overrides.publish to go live)"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
			  "hub_id":"hub_new",
			  "summary":[{"resource":"hub","action":"created"},` + tc.row + `],
			  "created_resource_ids":{"hubs":["hub_new"]},
			  "replayed":false}}}`

			srv, rec := hubOpScaffoldServerNoReadBack(t, body)
			res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0 — a failed read-back is not a run failure, the hub exists; stderr=%q", res.Code, res.Stderr)
			}
			if rec.hubCreates != 0 {
				t.Errorf("a failed read-back must not trigger a client-side re-apply; hub creates=%d", rec.hubCreates)
			}
			if out := decodeScaffoldJSON(t, res.Stdout); out["published"] != tc.wantPublished {
				t.Errorf("published = %v, want %v — taken from the publish summary row when the hub cannot be read back",
					out["published"], tc.wantPublished)
			}
			if !strings.Contains(res.Stderr, "could not read the new hub back") {
				t.Errorf("a failed read-back must be disclosed — the reported slug is then only the REQUESTED one; stderr=%q", res.Stderr)
			}
		})
	}
}

func hubOpScaffoldServerNoReadBack(t *testing.T, opBody string) (*httptest.Server, *hubOpCapture) {
	t.Helper()
	rec := &hubOpCapture{}
	catBody := catalog21Body(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catBody) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs/from-template"):
			body, _ := io.ReadAll(r.Body)
			rec.opPosts = append(rec.opPosts, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(opBody))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/hubs/hub_new"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"read-back unavailable"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs"):
			rec.hubCreates++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"hub_new","type":"hubs","attributes":{}}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestScaffoldHubOp_PartialIdentityForcesClientPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no slug", []string{"hubs", "scaffold", "--template", "community", "--name", "Founders", "--output", "json"}},
		{"no name", []string{"hubs", "scaffold", "--template", "community", "--slug", "founders", "--output", "json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

			res := runContract(t, scaffoldEnv(t, srv.URL), withTeam("t_team1", tc.args...)...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if len(rec.opPosts) != 0 {
				t.Errorf("the op requires BOTH --name and --slug non-empty; probing it sends an empty one and 422s once the flag flips. Got %d POST(s)", len(rec.opPosts))
			}
			if rec.hubCreates != 1 {
				t.Errorf("must fall through to the client-side pipeline, which can mint the missing half; hub creates=%d", rec.hubCreates)
			}
			if !strings.Contains(res.Stderr, "--name and --slug") {
				t.Errorf("the run must say why it left the op path; stderr=%q", res.Stderr)
			}
		})
	}
}

func TestScaffoldHubOp_UnidentifiableSuccessIsAnError(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty body", ""},
		{"no hub id anywhere", `{"data":{"type":"hub_scaffolds","attributes":{"summary":[],"replayed":false}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := hubOpScaffoldServer(t, http.StatusCreated, tc.body)

			res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
			if res.Code == errs.ExitOK {
				t.Errorf("exit = 0 with an unusable response — an empty hub_id at exit 0 is exactly what breaks --jq .hub_id; stdout=%q", res.Stdout)
			}
			if strings.Contains(res.Stdout, `"hub_id": ""`) {
				t.Errorf("an empty hub_id must never be rendered as a result; stdout=%q", res.Stdout)
			}
			if rec.hubCreates != 0 {
				t.Errorf("the op answered 2xx and may have built the hub — falling back would build a SECOND one; hub creates=%d", rec.hubCreates)
			}
		})
	}
}

func TestScaffoldHubOp_FallsBackToTheEnvelopeIDForTheHub(t *testing.T) {
	const idOnly = `{"data":{"id":"hub_new","type":"hub_scaffolds","attributes":{
	  "summary":[{"resource":"hub","action":"created"}],
	  "created_resource_ids":{"hubs":["hub_new"]},
	  "replayed":false}}}`
	srv, _ := hubOpScaffoldServer(t, http.StatusCreated, idOnly)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 — the envelope id identifies the hub; stderr=%q", res.Code, res.Stderr)
	}
	if out := decodeScaffoldJSON(t, res.Stdout); out["hub_id"] != "hub_new" {
		t.Errorf("hub_id = %v, want hub_new (read from data.id when the attribute is absent)", out["hub_id"])
	}
}

func TestScaffoldHubOp_EmptyURLOverrideIsAUsageErrorBeforeAnyRequest(t *testing.T) {
	var cases [][]string
	hubsScaffoldCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if strings.HasSuffix(f.Name, "-url") {
			cases = append(cases, []string{"--" + f.Name})
		}
	})
	if len(cases) < 3 {
		t.Fatalf("expected at least 3 --*-url flags on `hubs scaffold`, found %v — has the flag surface changed?", cases)
	}
	cases = append(cases, []string{"--branding-json"})

	for _, tc := range cases {
		flag := tc[0]
		t.Run(flag, func(t *testing.T) {
			value := func(v string) string {
				if flag == "--branding-json" {
					return `{"logo_url":"` + v + `"}`
				}
				return v
			}
			srv, rec := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

			res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs(flag, value("   "))...)
			if res.Code != errs.ExitUsage {
				t.Errorf("a whitespace-only %s must be refused too — the API rejects it as well; exit = %d, stderr=%q",
					flag, res.Code, res.Stderr)
			}
			res = runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs(flag, value(""))...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit = %d, want %d (ExitUsage) — the API rejects an empty branding *_url on create AND "+
					"update, so this can never succeed; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if rec.hubCreates != 0 || len(rec.opPosts) != 0 || len(rec.clientPosts) != 0 {
				t.Errorf("no request may fire — the old behaviour created a hub and then 422'd at the blobs "+
					"PATCH; opPosts=%d hubCreates=%d otherPosts=%v",
					len(rec.opPosts), rec.hubCreates, rec.clientPosts)
			}
			err := executeCLI(t, scaffoldEnv(t, srv.URL), scaffoldArgs(flag, value(""))...)
			if err == nil {
				t.Fatal("an empty URL override must fail")
			}
			if !strings.Contains(err.Error(), "--unset branding.") {
				t.Errorf("the error must name the real way to clear a branding key; err=%v", err)
			}
		})
	}
}

func TestScaffoldHubOp_EveryScaffoldFlagIsClassified(t *testing.T) {
	unsupported := map[string]bool{"branding-json": true, "catalog": true}
	for _, f := range scaffoldBrandingFlags {
		unsupported[f.flag] = true
	}

	rootPersistent := RootCmd().PersistentFlags()
	scan := func(f *pflag.Flag) {
		if f.Name == "help" || rootPersistent.Lookup(f.Name) != nil {
			return
		}
		if hubOpExpressibleFlags[f.Name] || hubOpStructuralFlags[f.Name] || unsupported[f.Name] {
			return
		}
		t.Errorf("--%s is classified neither expressible nor unsupported. "+
			"The op path would silently ignore it (pflag Changed() is false for an unknown name, so the "+
			"unsupported check cannot see it). If overrides{} CANNOT carry it, add it to "+
			"hubOpUnsupportedFlags — that is the only list the runtime reads, and the only one that "+
			"actually forces the client path. Add it to hubOpExpressibleFlags ONLY if the request really "+
			"carries it; that map wires nothing and merely records why the flag is exempt.", f.Name)
	}
	hubsScaffoldCmd.LocalFlags().VisitAll(scan)
	hubsCmd.PersistentFlags().VisitAll(scan)
}

func TestScaffoldHubOp_FingerprintMismatchNamesTheMachineToken(t *testing.T) {
	srv, _ := hubOpScaffoldServer(t, http.StatusConflict,
		`{"errors":[{"code":"idempotency_fingerprint_mismatch","detail":"reused"}]}`)

	err := executeCLI(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if err == nil {
		t.Fatal("a fingerprint mismatch must fail the run")
	}
	if !strings.Contains(err.Error(), "["+hubOpFingerprintMismatch+"]") {
		t.Errorf("AGENTS.md, llms.txt, api-surface.md and mio-skill.md all tell agents to branch on %q "+
			"in the error text; the server's detail is rendered instead of its code, so the token only "+
			"appears if we put it there. err=%v", hubOpFingerprintMismatch, err)
	}
}

func TestScaffoldHubOp_SkipNotesMatchWhatTheDocsPromise(t *testing.T) {
	const marker = "not using the server-side hub scaffold op"

	for _, tc := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"branding flag is named", []string{"--primary-color", "#B91C1C"}, "--primary-color"},
		{"local catalog is named", []string{"--catalog", "@CATALOG@"}, "--catalog"},
		{"resume mode is explained", []string{"--hub", "hub_new"}, "--hub"},
		{"partial identity is explained", []string{"@DROP-SLUG@"}, "--name and --slug"},
		{"structural --dry-run stays silent", []string{"--dry-run"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := hubOpScaffoldServer(t, http.StatusCreated, hubOpLiveBody)

			args := scaffoldArgs(tc.extra...)
			switch {
			case len(tc.extra) == 1 && tc.extra[0] == "@DROP-SLUG@":
				args = withTeam("t_team1", "hubs", "scaffold", "--template", "community",
					"--name", "Founders", "--output", "json")
			case len(tc.extra) == 2 && tc.extra[1] == "@CATALOG@":
				args = scaffoldArgs("--catalog", writeTempCatalog(t, catalog21Body(t)))
			}
			res := runContract(t, scaffoldEnv(t, srv.URL), args...)
			if res.Code != errs.ExitOK {
				t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
			}
			if tc.want == "" {
				if strings.Contains(res.Stderr, marker) {
					t.Errorf("--dry-run is structural and the plan renderer emits no apply-time notes; "+
						"the docs say so. stderr=%q", res.Stderr)
				}
				return
			}
			if !strings.Contains(res.Stderr, marker) || !strings.Contains(res.Stderr, tc.want) {
				t.Errorf("a flag-forced skip must be announced AND name %s — silently dropping what the "+
					"operator asked for is the outcome this whole branch exists to prevent. stderr=%q",
					tc.want, res.Stderr)
			}
		})
	}
}

func TestScaffoldHubOp_ForeignEnvelopeTypeIsNotAHub(t *testing.T) {
	srv, rec := hubOpScaffoldServer(t, http.StatusCreated,
		`{"data":{"id":"hub_created","type":"hubs","attributes":{"slug":"x","is_private":true}}}`)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code == errs.ExitOK {
		t.Errorf("a `hubs` envelope is not a hub_scaffolds result — accepting its id reports a whole-hub "+
			"build that never happened; stdout=%q", res.Stdout)
	}
	if rec.hubCreates != 0 {
		t.Errorf("must not fall back either — the op answered 2xx and may have built the hub; creates=%d", rec.hubCreates)
	}
}

func TestScaffoldHubOp_UnusableSuccessReportsTheRealHTTPStatus(t *testing.T) {
	bin := buildBinary(t)
	srv, _ := hubOpScaffoldServer(t, http.StatusCreated,
		`{"data":{"type":"hub_scaffolds","attributes":{"summary":[],"replayed":false}}}`)

	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	}, "--team", "t_team1", "hubs", "scaffold",
		"--template", "community", "--name", "Founders", "--slug", "founders")

	var envelope struct {
		Errors []struct {
			Status string         `json:"status"`
			Meta   map[string]any `json:"meta"`
		} `json:"errors"`
	}
	raw := stderr
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("stderr tail is not a JSON:API envelope: %v; stderr=%q", err, stderr)
	}
	if len(envelope.Errors) == 0 {
		t.Fatalf("error envelope empty; stderr=%q", stderr)
	}
	if got := envelope.Errors[0].Status; got != "201" {
		t.Errorf("errors[0].status = %q, want \"201\" — the API answered 201; reporting 500 tells an agent "+
			"the server failed when it did not, the relabelling TestContract_ErrorEnvelope_RealHTTPStatus forbids", got)
	}
	if exitCode != errs.ExitServer {
		t.Errorf("exit code = %d, want %d — the status is precise, the exit code is coarse", exitCode, errs.ExitServer)
	}
}

func TestScaffoldHubOp_RecoveryGuidanceNamesRealCommands(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveCatalogGET(w, r, catalog21Body(t)) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hubs/from-template"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(hubOpLiveBody))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/hubs/hub_new"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"nope"}]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, scaffoldEnv(t, srv.URL), scaffoldArgs()...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}

	refs := regexp.MustCompile("`mio ([^`]+)`").FindAllStringSubmatch(res.Stderr, -1)
	if len(refs) == 0 {
		t.Fatalf("expected the read-back failure to suggest a command; stderr=%q", res.Stderr)
	}
	cmdToken := regexp.MustCompile(`^[a-z][a-z-]*$`)
	for _, m := range refs {
		var path []string
		for _, tok := range strings.Fields(m[1]) {
			if !cmdToken.MatchString(tok) {
				break
			}
			path = append(path, tok)
		}
		if len(path) == 0 {
			continue
		}
		cmd, _, err := RootCmd().Find(path)
		if err != nil || len(strings.Fields(cmd.CommandPath())) != len(path)+1 {
			t.Errorf("recovery guidance names `mio %s`, which is not a command (resolved to %q). "+
				"Guidance is executed literally by agents; stderr=%q",
				m[1], cmd.CommandPath(), res.Stderr)
		}
	}
}
