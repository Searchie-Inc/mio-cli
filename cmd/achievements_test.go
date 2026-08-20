package cmd

// achievements_test.go — contract tests for the `mio achievements` command
// group (achievements admin surface, MIO-3054 backend / MIO-3412 CLI).
//
// Covers:
//   - create: body shape (JSON:API envelope, type "achievements", snake_case
//     attrs incl. the wholesale appearance object), --title required,
//     --appearance-json validated as JSON pre-request
//   - list: filter[award_mode]/filter[category] + page[size]/page[after]
//   - retrieve / update: GET/PATCH path + partial-update semantics, including
//     that an explicit --is-active=false serializes (present, false)
//   - archive: DELETE, --yes gate (exit 5, no request without it)
//   - offerings list/attach/detach: hub-scoped paths, attach envelope type
//     "achievement_hubs", detach --yes gate
//   - grant: POST .../members/{contact_id}/achievements, envelope type
//     "achievement_earns", award_reason only when --reason given,
//     --contact-id required
//   - revoke: DELETE with ?reason= QUERY (no body), --yes gate
//   - restore: POST .../restore sends the envelope EVEN WITH NO FLAGS (the
//     backend requires the body; a nil body would 422), type
//     "achievement_earns", restore_reason only when --reason given
//
// Reuses the in-process harness from contract_test.go (runContract,
// newMockServer, baseEnv, withTeam).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

const achievementBody = `{
	"data": {
		"id": "ach_1",
		"type": "achievements",
		"attributes": {
			"title": "First Post",
			"award_mode": "manual",
			"points": 10,
			"is_secret": false,
			"is_active": true
		}
	}
}`

const achievementHubBody = `{
	"data": {
		"id": "achhub_1",
		"type": "achievement_hubs",
		"attributes": {
			"achievement_id": "ach_1",
			"hub_id": "hub_123"
		}
	}
}`

const achievementEarnBody = `{
	"data": {
		"id": "earn_1",
		"type": "achievement_earns",
		"attributes": {
			"achievement_id": "ach_1",
			"hub_id": "hub_123",
			"contact_id": "ct_456",
			"source": "manual",
			"award_reason": "manual",
			"points_awarded": 10
		}
	}
}`

// captureServer records the last request's method, path, raw query and body,
// and answers with the given status + body.
func captureServer(t *testing.T, status int, respBody string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.Method = r.Method
		cap.Path = r.URL.Path
		cap.RawQuery = r.URL.RawQuery
		cap.Body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

type capturedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Body     []byte
}

func decodeEnvelope(t *testing.T, body []byte) (string, map[string]any) {
	t.Helper()
	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, body)
	}
	return doc.Data.Type, doc.Data.Attributes
}

// ── create ───────────────────────────────────────────────────────────────────

// TestAchievementsCreate_BodyShape verifies create sends a JSON:API envelope
// with type "achievements" and snake_case attribute keys — including the
// appearance object forwarded wholesale — to the team-scoped path.
func TestAchievementsCreate_BodyShape(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"achievements", "create",
			"--title", "First Post",
			"--description", "Posted for the first time",
			"--award-mode", "manual",
			"--category", "community",
			"--points", "10",
			"--is-secret",
			"--email-notification-enabled=false",
			"--appearance-json", `{"shape":"hexagon","emoji":"🏆","color":"#5581f4"}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/achievements" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/achievements", cap.Path)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievements" {
		t.Errorf("envelope type = %q, want \"achievements\"", typ)
	}
	if attrs["title"] != "First Post" {
		t.Errorf("attributes.title = %v, want \"First Post\"", attrs["title"])
	}
	if attrs["description"] != "Posted for the first time" {
		t.Errorf("attributes.description = %v", attrs["description"])
	}
	if attrs["award_mode"] != "manual" {
		t.Errorf("attributes.award_mode = %v, want \"manual\"", attrs["award_mode"])
	}
	if attrs["category"] != "community" {
		t.Errorf("attributes.category = %v, want \"community\"", attrs["category"])
	}
	if attrs["points"] != float64(10) {
		t.Errorf("attributes.points = %v, want 10", attrs["points"])
	}
	if attrs["is_secret"] != true {
		t.Errorf("attributes.is_secret = %v, want true", attrs["is_secret"])
	}
	if attrs["email_notification_enabled"] != false {
		t.Errorf("attributes.email_notification_enabled = %v, want explicit false", attrs["email_notification_enabled"])
	}
	appearance, ok := attrs["appearance"].(map[string]any)
	if !ok {
		t.Fatalf("attributes.appearance = %v, want a JSON object", attrs["appearance"])
	}
	if appearance["shape"] != "hexagon" || appearance["emoji"] != "🏆" || appearance["color"] != "#5581f4" {
		t.Errorf("appearance forwarded wrong: %v", appearance)
	}
}

// TestAchievementsCreate_MissingTitle verifies omitting --title exits 2
// without hitting the API.
func TestAchievementsCreate_MissingTitle(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "create", "--points", "10")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestAchievementsCreate_InvalidAppearanceJSON verifies malformed
// --appearance-json exits 2 without hitting the API.
func TestAchievementsCreate_InvalidAppearanceJSON(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"achievements", "create",
			"--title", "First Post",
			"--appearance-json", `{"shape":`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire when --appearance-json is malformed")
	}
}

// ── list ─────────────────────────────────────────────────────────────────────

// TestAchievementsList_QueryParams verifies --award-mode/--category map to
// filter[award_mode]/filter[category] alongside pagination.
func TestAchievementsList_QueryParams(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, `{"data":[],"meta":{"page":{"has_more":false}}}`)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"achievements", "list",
			"--award-mode", "manual",
			"--category", "community",
			"--limit", "50",
			"--after", "cursor_abc",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/achievements" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/achievements", cap.Path)
	}
	for _, want := range []string{
		"filter%5Baward_mode%5D=manual",
		"filter%5Bcategory%5D=community",
		"page%5Bsize%5D=50",
		"page%5Bafter%5D=cursor_abc",
	} {
		if !strings.Contains(cap.RawQuery, want) {
			t.Errorf("query %q missing %s", cap.RawQuery, want)
		}
	}
}

// ── retrieve ─────────────────────────────────────────────────────────────────

// TestAchievementsRetrieve_Path verifies retrieve issues a GET to the
// single-resource path.
func TestAchievementsRetrieve_Path(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "retrieve", "ach_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/achievements/ach_1" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/achievements/ach_1", cap.Path)
	}
}

// ── update ───────────────────────────────────────────────────────────────────

// TestAchievementsUpdate_PartialBody verifies update PATCHes exactly the
// changed flags (type "achievements"), and that an explicit --is-active=false
// is present with value false rather than dropped as a zero value.
func TestAchievementsUpdate_PartialBody(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"achievements", "update", "ach_1",
			"--title", "New Title",
			"--is-active=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/achievements/ach_1" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/achievements/ach_1", cap.Path)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievements" {
		t.Errorf("envelope type = %q, want \"achievements\"", typ)
	}
	if len(attrs) != 2 {
		t.Errorf("attributes = %v, want exactly 2 keys (title, is_active)", attrs)
	}
	if attrs["title"] != "New Title" {
		t.Errorf("attributes.title = %v, want \"New Title\"", attrs["title"])
	}
	v, ok := attrs["is_active"]
	if !ok {
		t.Fatalf("attributes.is_active is absent, want explicit false present; body=%q", cap.Body)
	}
	if v != false {
		t.Errorf("attributes.is_active = %v, want false", v)
	}
}

// TestAchievementsUpdate_NothingToUpdate verifies update with no field flags
// exits 2 without hitting the API.
func TestAchievementsUpdate_NothingToUpdate(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── archive ──────────────────────────────────────────────────────────────────

// TestAchievementsArchive_RequiresYes verifies archive without --yes exits 5
// in a non-TTY shell and never calls the API.
func TestAchievementsArchive_RequiresYes(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "archive", "ach_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire without --yes")
	}
}

// TestAchievementsArchive_WithYes verifies archive DELETEs the definition path.
func TestAchievementsArchive_WithYes(t *testing.T) {
	srv, cap := captureServer(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "archive", "ach_1", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/achievements/ach_1" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/achievements/ach_1", cap.Path)
	}
	if !strings.Contains(res.Stdout, "Archived achievement ach_1") {
		t.Errorf("stdout missing confirmation; got %q", res.Stdout)
	}
}

// ── offerings ────────────────────────────────────────────────────────────────

// TestAchievementsOfferingsList_Path verifies offerings list GETs the
// hub-scoped path.
func TestAchievementsOfferingsList_Path(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, `{"data":[],"meta":{"page":{"has_more":false}}}`)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "offerings", "list", "--limit", "5")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/hubs/hub_123/achievements" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/hubs/hub_123/achievements", cap.Path)
	}
	if !strings.Contains(cap.RawQuery, "page%5Bsize%5D=5") {
		t.Errorf("query %q missing page[size]=5", cap.RawQuery)
	}
}

// TestAchievementsOfferingsAttach_BodyShape verifies attach POSTs an envelope
// of type "achievement_hubs" (NOT the path-derived "achievements") carrying
// achievement_id.
func TestAchievementsOfferingsAttach_BodyShape(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementHubBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "offerings", "attach", "ach_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/hubs/hub_123/achievements" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/hubs/hub_123/achievements", cap.Path)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievement_hubs" {
		t.Errorf("envelope type = %q, want \"achievement_hubs\" (backend AchievementAttachData Literal)", typ)
	}
	if attrs["achievement_id"] != "ach_1" {
		t.Errorf("attributes.achievement_id = %v, want \"ach_1\"", attrs["achievement_id"])
	}
}

// TestAchievementsOfferingsDetach_RequiresYes verifies detach without --yes
// exits 5 and never calls the API.
func TestAchievementsOfferingsDetach_RequiresYes(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "offerings", "detach", "ach_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire without --yes")
	}
}

// TestAchievementsOfferingsDetach_WithYes verifies detach DELETEs the
// hub-scoped offering path.
func TestAchievementsOfferingsDetach_WithYes(t *testing.T) {
	srv, cap := captureServer(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "offerings", "detach", "ach_1", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/hubs/hub_123/achievements/ach_1" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/hubs/hub_123/achievements/ach_1", cap.Path)
	}
}

// ── grant ────────────────────────────────────────────────────────────────────

// TestAchievementsGrant_BodyShape verifies grant POSTs to the member earn path
// with envelope type "achievement_earns", achievement_id, and award_reason
// (only because --reason was given).
func TestAchievementsGrant_BodyShape(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementEarnBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "grant", "ach_1",
			"--contact-id", "ct_456",
			"--reason", "community week winner",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements", cap.Path)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievement_earns" {
		t.Errorf("envelope type = %q, want \"achievement_earns\" (backend AchievementGrantData Literal)", typ)
	}
	if attrs["achievement_id"] != "ach_1" {
		t.Errorf("attributes.achievement_id = %v, want \"ach_1\"", attrs["achievement_id"])
	}
	if attrs["award_reason"] != "community week winner" {
		t.Errorf("attributes.award_reason = %v, want the --reason value", attrs["award_reason"])
	}
}

// TestAchievementsGrant_DefaultReasonOmitted verifies grant without --reason
// sends ONLY achievement_id — award_reason defaults server-side and must not
// be fabricated by the CLI.
func TestAchievementsGrant_DefaultReasonOmitted(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementEarnBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "grant", "ach_1", "--contact-id", "ct_456")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	_, attrs := decodeEnvelope(t, cap.Body)
	if len(attrs) != 1 {
		t.Errorf("attributes = %v, want exactly {achievement_id}", attrs)
	}
	if _, present := attrs["award_reason"]; present {
		t.Errorf("award_reason must be absent when --reason is not given; attrs=%v", attrs)
	}
}

// TestAchievementsGrant_MissingContactID verifies omitting --contact-id exits
// 2 without hitting the API.
func TestAchievementsGrant_MissingContactID(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "grant", "ach_1")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire without --contact-id")
	}
}

// ── revoke ───────────────────────────────────────────────────────────────────

// TestAchievementsRevoke_RequiresYes verifies revoke without --yes exits 5 and
// never calls the API.
func TestAchievementsRevoke_RequiresYes(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "revoke", "ach_1", "--contact-id", "ct_456")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire without --yes")
	}
}

// TestAchievementsRevoke_ReasonAsQuery verifies revoke DELETEs the earn path
// with the reason in the ?reason= QUERY parameter and an empty body — the
// backend reads Query(None, alias="reason"); a body would be ignored and the
// reason lost.
func TestAchievementsRevoke_ReasonAsQuery(t *testing.T) {
	srv, cap := captureServer(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "revoke", "ach_1",
			"--contact-id", "ct_456",
			"--reason", "granted in error",
			"--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements/ach_1" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements/ach_1", cap.Path)
	}
	if !strings.Contains(cap.RawQuery, "reason=granted+in+error") &&
		!strings.Contains(cap.RawQuery, "reason=granted%20in%20error") {
		t.Errorf("query %q missing reason=granted in error", cap.RawQuery)
	}
	if len(strings.TrimSpace(string(cap.Body))) != 0 {
		t.Errorf("revoke request body = %q, want empty (reason travels as a query param)", cap.Body)
	}
	if !strings.Contains(res.Stdout, "Revoked achievement ach_1") {
		t.Errorf("stdout missing confirmation; got %q", res.Stdout)
	}
}

// TestAchievementsRevoke_NoReasonNoQuery verifies revoke without --reason
// sends no query string at all.
func TestAchievementsRevoke_NoReasonNoQuery(t *testing.T) {
	srv, cap := captureServer(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "revoke", "ach_1", "--contact-id", "ct_456", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.RawQuery != "" {
		t.Errorf("query = %q, want empty when --reason is not given", cap.RawQuery)
	}
}

// ── restore ──────────────────────────────────────────────────────────────────

// TestAchievementsRestore_EnvelopeAlwaysSent is the regression guard for the
// restore body contract: the backend REQUIRES the JSON:API envelope
// (AchievementRestoreEnvelope is a non-optional body param) even when
// restore_reason is omitted. A nil body — the natural reading of "no flags
// set" — would send no envelope and 422. The envelope type must resolve to
// "achievement_earns" through the members/achievements override ("restore" is
// deliberately not a known collection token).
func TestAchievementsRestore_EnvelopeAlwaysSent(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementEarnBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "restore", "ach_1", "--contact-id", "ct_456")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements/ach_1/restore" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements/ach_1/restore", cap.Path)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievement_earns" {
		t.Errorf("envelope type = %q, want \"achievement_earns\"", typ)
	}
	if attrs == nil {
		t.Fatalf("attributes member absent — the envelope must be sent even with no flags; body=%q", cap.Body)
	}
	if len(attrs) != 0 {
		t.Errorf("attributes = %v, want empty (restore_reason only when --reason given)", attrs)
	}
}

// TestAchievementsRestore_WithReason verifies --reason maps to restore_reason.
func TestAchievementsRestore_WithReason(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementEarnBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "restore", "ach_1",
			"--contact-id", "ct_456",
			"--reason", "revoked in error",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	_, attrs := decodeEnvelope(t, cap.Body)
	if attrs["restore_reason"] != "revoked in error" {
		t.Errorf("attributes.restore_reason = %v, want the --reason value", attrs["restore_reason"])
	}
}

// ── earn-verb 404 hint ───────────────────────────────────────────────────────

// TestAchievementsEarn404_AmbiguousHint pins the earn verbs' 404 hint contract
// (Jay-r review, PR #109): the backend deliberately collapses "feature gates
// off", "achievement missing/not offered" and "wrong contact-id namespace"
// into one generic 404, so the hint must name ALL the possibilities — in
// particular the gates — and must not assert the contact id is wrong. The
// generic hintGlobalContactID would fail this test: it names only the contact
// id. Driven as a subprocess because the JSON:API error envelope is written by
// main.go after os.Exit.
func TestAchievementsEarn404_AmbiguousHint(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Status: 404, Body: `{"errors":[{"status":"404","detail":"Not found."}]}`},
	})
	bin := buildBinary(t)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"grant", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "grant", "ach_1", "--contact-id", "ct_1"}},
		{"revoke", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "revoke", "ach_1", "--contact-id", "ct_1", "--yes"}},
		{"restore", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "restore", "ach_1", "--contact-id", "ct_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, exitCode := runBinary(t, bin, []string{
				"MIO_API_KEY=test-key",
				"MIO_API_BASE_URL=" + srv.URL,
			}, tc.args...)

			if exitCode != errs.ExitNotFound {
				t.Fatalf("exit code = %d, want %d (ExitNotFound); stderr=%q", exitCode, errs.ExitNotFound, stderr)
			}
			var envelope struct {
				Errors []struct {
					Detail string `json:"detail"`
				} `json:"errors"`
			}
			raw := strings.TrimSpace(stderr)
			if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
				t.Fatalf("stderr not valid JSON:API envelope: %v; stderr=%q", err, raw)
			}
			if len(envelope.Errors) == 0 {
				t.Fatalf("error envelope empty; stderr=%q", raw)
			}
			detail := envelope.Errors[0].Detail
			// The hint must name the FEATURE GATES — the piece the generic
			// contact-id hint lacks — so a gate-off 404 is not misread as a
			// wrong contact id.
			for _, want := range []string{"ACHIEVEMENTS_ENABLED", "settings.achievements.enabled", "GLOBAL contact id"} {
				if !strings.Contains(detail, want) {
					t.Errorf("404 detail must mention %q; got %q", want, detail)
				}
			}
			// And it must not issue the old false instruction that the contact
			// id IS the problem.
			if strings.Contains(detail, "this verb needs the GLOBAL contact id") {
				t.Errorf("404 detail asserts the contact id is wrong — the earn 404 is ambiguous and the hint must not diagnose; got %q", detail)
			}
		})
	}
}
