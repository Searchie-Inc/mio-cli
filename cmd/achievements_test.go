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
//   - rule set: PUT .../rule, hub-scoped path, envelope type
//     "achievement_rules", confirm defaults to false and is ALWAYS sent
//     explicitly, --confirm flips it to true, --notify-on-backfill threaded,
//     confirm=false renders the meta-only preview count in plain words
//     (never claiming an estimate is exact when it's a lower bound)
//   - rule delete: DELETE .../rule, hub-scoped path, --yes gate
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

// achievementRuleBody is a confirm=true PUT .../rule response (201 new bind /
// 200 recompile) — a full achievement_rules resource.
const achievementRuleBody = `{
	"data": {
		"id": "rule_1",
		"type": "achievement_rules",
		"attributes": {
			"achievement_id": "ach_1",
			"hub_id": "hub_123",
			"segment_id": "seg_1",
			"is_active": true,
			"backfill_status": "pending",
			"compiled_definition_version": 1,
			"broken_reason": null,
			"notify_on_backfill": false,
			"created_at": "2026-09-04T00:00:00Z",
			"updated_at": "2026-09-04T00:00:00Z"
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

// ── create/update: rule-piece flags (MIO-3372/MIO-3662) ────────────────────────

// TestAchievementsCreate_RuleFlags_ThresholdVariant verifies --rule-type/
// --rule-criteria/--rule-threshold thread into the attrs body, and that
// --rule-content-node-ids/--rule-window-days stay ABSENT (not zero-valued)
// when not given — the whole point of Changed()-gating these like every
// other create/update flag.
func TestAchievementsCreate_RuleFlags_ThresholdVariant(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"achievements", "create",
			"--title", "Fast Learner",
			"--award-mode", "rule",
			"--rule-type", "milestone",
			"--rule-criteria", "time-since-joining",
			"--rule-threshold", "5",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, cap.Body)
	if attrs["rule_type"] != "milestone" {
		t.Errorf("attributes.rule_type = %v, want \"milestone\"", attrs["rule_type"])
	}
	if attrs["rule_criteria"] != "time-since-joining" {
		t.Errorf("attributes.rule_criteria = %v, want \"time-since-joining\"", attrs["rule_criteria"])
	}
	if attrs["rule_threshold"] != float64(5) {
		t.Errorf("attributes.rule_threshold = %v, want 5", attrs["rule_threshold"])
	}
	if _, present := attrs["rule_content_node_ids"]; present {
		t.Errorf("attributes.rule_content_node_ids must be ABSENT when --rule-content-node-ids is not given; attrs=%v", attrs)
	}
	if _, present := attrs["rule_window_days"]; present {
		t.Errorf("attributes.rule_window_days must be ABSENT when --rule-window-days is not given; attrs=%v", attrs)
	}
}

// TestAchievementsCreate_RuleFlags_ContentNodeIDsAsJSONArray is the regression
// guard that matters most for this flag: --rule-content-node-ids must
// serialize as a JSON ARRAY of ids, never a single comma-joined string — the
// backend reads rule_content_node_ids as list[str] (rules.py), and a
// comma-string would be accepted on the wire as one bad id instead of a list.
// Also verifies --rule-threshold stays absent (the two are mutually
// exclusive; the CLI doesn't block it, but it must not invent one either).
func TestAchievementsCreate_RuleFlags_ContentNodeIDsAsJSONArray(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"achievements", "create",
			"--title", "Course Completer",
			"--award-mode", "rule",
			"--rule-type", "milestone",
			"--rule-criteria", "completed-content",
			"--rule-content-node-ids", "node_1,node_2",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, cap.Body)
	ids, ok := attrs["rule_content_node_ids"].([]any)
	if !ok {
		t.Fatalf("attributes.rule_content_node_ids = %v (%T), want a JSON array, not a string", attrs["rule_content_node_ids"], attrs["rule_content_node_ids"])
	}
	if len(ids) != 2 || ids[0] != "node_1" || ids[1] != "node_2" {
		t.Errorf("attributes.rule_content_node_ids = %v, want [\"node_1\", \"node_2\"]", ids)
	}
	if _, present := attrs["rule_threshold"]; present {
		t.Errorf("attributes.rule_threshold must be ABSENT when --rule-threshold is not given; attrs=%v", attrs)
	}
}

// TestAchievementsCreate_RuleFlags_UnsetFieldsAbsent verifies that a plain
// create (no --rule-* flags at all) sends NONE of the five rule-piece keys —
// pinning the Changed() guarantee explicitly, since a stray zero-valued
// rule_threshold or rule_window_days would trip the backend's
// rule_pieces_not_allowed check for a manual badge.
func TestAchievementsCreate_RuleFlags_UnsetFieldsAbsent(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "create", "--title", "Plain Manual Badge")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, cap.Body)
	for _, key := range []string{"rule_type", "rule_criteria", "rule_threshold", "rule_window_days", "rule_content_node_ids"} {
		if _, present := attrs[key]; present {
			t.Errorf("attributes.%s must be ABSENT when no --rule-* flag is given; attrs=%v", key, attrs)
		}
	}
}

// TestAchievementsCreate_RuleContentNodeIDs_RejectsBlankEntry verifies a blank
// entry in --rule-content-node-ids is a usage error before any request fires
// — dropping it silently would ship a SHORTER list than the caller named
// (same shape as content.go's --playlist-id, MIO-3074).
func TestAchievementsCreate_RuleContentNodeIDs_RejectsBlankEntry(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"achievements", "create",
			"--title", "Course Completer",
			"--rule-content-node-ids", "node_1,,node_2",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire when --rule-content-node-ids contains a blank entry")
	}
}

// TestAchievementsUpdate_RuleFlags_PartialBody verifies updating a single
// rule piece (--rule-threshold) sends ONLY that key — PATCH partial-update
// semantics apply to the rule-piece flags exactly like every other
// create/update flag, unlike the rule-set command's confirm/notify_on_backfill
// which are always sent explicitly.
func TestAchievementsUpdate_RuleFlags_PartialBody(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1", "--rule-threshold", "7")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, cap.Body)
	if len(attrs) != 1 {
		t.Errorf("attributes = %v, want exactly 1 key (rule_threshold)", attrs)
	}
	if attrs["rule_threshold"] != float64(7) {
		t.Errorf("attributes.rule_threshold = %v, want 7", attrs["rule_threshold"])
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

// ── rule ─────────────────────────────────────────────────────────────────────

// ruleSetPath is the hub-scoped rule path every "rule set"/"rule delete" test
// asserts against verbatim — the regression that matters most here (MIO-3662:
// the gap this command closes was found because a hand-rolled request hit the
// TEAM-scoped shape instead and 404d).
const ruleSetPath = "/api/v1/teams/t_team1/hubs/hub_123/achievements/ach_1/rule"

// TestAchievementsRuleSet_PreviewByDefault verifies "rule set" without
// --confirm PUTs the hub-scoped path with an explicit confirm:false (never
// omitted) and notify_on_backfill:false, and renders the preview count in
// plain words rather than dumping the raw meta object — saying, unmissably,
// that nothing was persisted.
func TestAchievementsRuleSet_PreviewByDefault(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, `{"meta":{"preview_count":42,"preview_count_is_lower_bound":false}}`)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "rule", "set", "ach_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", cap.Method)
	}
	if cap.Path != ruleSetPath {
		t.Errorf("path = %q, want %q (hub-scoped)", cap.Path, ruleSetPath)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievement_rules" {
		t.Errorf("envelope type = %q, want \"achievement_rules\" (backend AchievementRulePutData Literal)", typ)
	}
	v, ok := attrs["confirm"]
	if !ok {
		t.Fatalf("attributes.confirm is absent, want explicit false present; body=%q", cap.Body)
	}
	if v != false {
		t.Errorf("attributes.confirm = %v, want false", v)
	}
	nv, ok := attrs["notify_on_backfill"]
	if !ok {
		t.Fatalf("attributes.notify_on_backfill is absent, want explicit false present; body=%q", cap.Body)
	}
	if nv != false {
		t.Errorf("attributes.notify_on_backfill = %v, want false", nv)
	}

	for _, want := range []string{"Preview only", "NOTHING WAS SAVED", "42", "--confirm"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("stdout must mention %q; got %q", want, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "at least") {
		t.Errorf("stdout must not hedge with \"at least\" when preview_count_is_lower_bound is false; got %q", res.Stdout)
	}
}

// TestAchievementsRuleSet_PreviewLowerBound verifies preview_count_is_lower_bound
// makes the count print as a hedge ("at least N"), never as an exact figure —
// the estimate is capped above the preview page size and must not be
// misrepresented as precise.
func TestAchievementsRuleSet_PreviewLowerBound(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{"meta":{"preview_count":500,"preview_count_is_lower_bound":true}}`)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "rule", "set", "ach_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "at least 500") {
		t.Errorf("stdout must hedge a lower-bound estimate with \"at least 500\"; got %q", res.Stdout)
	}
}

// TestAchievementsRuleSet_Confirm verifies --confirm sends confirm:true and
// decodes the persisted achievement_rules resource (data present, NOT the
// meta-only preview shape).
func TestAchievementsRuleSet_Confirm(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "rule", "set", "ach_1", "--confirm")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", cap.Method)
	}
	if cap.Path != ruleSetPath {
		t.Errorf("path = %q, want %q (hub-scoped)", cap.Path, ruleSetPath)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievement_rules" {
		t.Errorf("envelope type = %q, want \"achievement_rules\"", typ)
	}
	if attrs["confirm"] != true {
		t.Errorf("attributes.confirm = %v, want true", attrs["confirm"])
	}

	if !strings.Contains(res.Stdout, "segment_id") || !strings.Contains(res.Stdout, "seg_1") {
		t.Errorf("stdout must render the persisted rule resource (segment_id=seg_1); got %q", res.Stdout)
	}
}

// TestAchievementsRuleSet_NotifyOnBackfillThreaded verifies --notify-on-backfill
// maps to notify_on_backfill:true on the wire.
func TestAchievementsRuleSet_NotifyOnBackfillThreaded(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementRuleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "rule", "set", "ach_1", "--confirm", "--notify-on-backfill")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	_, attrs := decodeEnvelope(t, cap.Body)
	if attrs["notify_on_backfill"] != true {
		t.Errorf("attributes.notify_on_backfill = %v, want true", attrs["notify_on_backfill"])
	}
}

// TestAchievementsRuleDelete_RequiresYes verifies "rule delete" without --yes
// exits 5 and never calls the API.
func TestAchievementsRuleDelete_RequiresYes(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "rule", "delete", "ach_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire without --yes")
	}
}

// TestAchievementsRuleDelete_WithYes verifies "rule delete" DELETEs the
// hub-scoped rule path.
func TestAchievementsRuleDelete_WithYes(t *testing.T) {
	srv, cap := captureServer(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "achievements", "rule", "delete", "ach_1", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", cap.Method)
	}
	if cap.Path != ruleSetPath {
		t.Errorf("path = %q, want %q (hub-scoped)", cap.Path, ruleSetPath)
	}
	if !strings.Contains(res.Stdout, "Unbound the rule for achievement ach_1") {
		t.Errorf("stdout missing confirmation; got %q", res.Stdout)
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
	// The 204 is idempotent server-side (a nonexistent earn — wrong contact
	// id included — answers the same), so the message must disclose that
	// rather than claim an earn was revoked (blind review, PR #109).
	if !strings.Contains(res.Stdout, "Revoke accepted") || !strings.Contains(res.Stdout, "does not confirm") {
		t.Errorf("stdout must disclose the idempotent 204 (\"Revoke accepted\" + \"does not confirm\"); got %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "Revoked achievement") {
		t.Errorf("stdout must not flatly claim an earn was revoked — 204 does not confirm one existed; got %q", res.Stdout)
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

// ── override ─────────────────────────────────────────────────────────────────

// TestAchievementsOverride_BodyShape verifies override POSTs to the member
// earn override path with envelope type "achievement_earns" (the SAME
// Literal grant/restore use — verified empirically in
// internal/client/client_test.go TestResourceTypeFromPath, not just assumed
// from the "restore" precedent) and a body carrying ONLY {"reason": ...}.
func TestAchievementsOverride_BodyShape(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementEarnBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "override", "ach_1",
			"--contact-id", "ct_456",
			"--reason", "segment missed them before the rule was bound",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", cap.Method)
	}
	if cap.Path != "/api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements/ach_1/override" {
		t.Errorf("path = %q, want /api/v1/teams/t_team1/hubs/hub_123/members/ct_456/achievements/ach_1/override", cap.Path)
	}

	typ, attrs := decodeEnvelope(t, cap.Body)
	if typ != "achievement_earns" {
		t.Errorf("envelope type = %q, want \"achievement_earns\" (backend AchievementOverrideData Literal)", typ)
	}
	if len(attrs) != 1 {
		t.Errorf("attributes = %v, want exactly 1 key (reason)", attrs)
	}
	if attrs["reason"] != "segment missed them before the rule was bound" {
		t.Errorf("attributes.reason = %v, want the --reason value", attrs["reason"])
	}
}

// TestAchievementsOverride_MissingContactID verifies omitting --contact-id
// exits 2 without hitting the API — mirrors
// TestAchievementsGrant_MissingContactID for the shared requireContactID path.
func TestAchievementsOverride_MissingContactID(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "override", "ach_1", "--reason", "backfill missed them")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire without --contact-id")
	}
}

// TestAchievementsOverride_MissingReason verifies omitting --reason entirely
// exits 2 without hitting the API — unlike grant/revoke/restore, override's
// --reason is required.
func TestAchievementsOverride_MissingReason(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "override", "ach_1", "--contact-id", "ct_456")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire without --reason")
	}
}

// TestAchievementsOverride_BlankReason_RejectedClientSide is the regression
// guard for the one piece of client-side validation this file adds: a
// whitespace-only --reason must be refused BEFORE any request fires. The
// backend's AuditReason type strips whitespace before its min_length=1
// check runs, so "   " would 422 there too — but there is no reading of a
// whitespace-only reason the server would ever accept, so catching it
// locally saves a guaranteed round trip (unlike --rule-content-node-ids'
// blank-entry check, this one has a real server-side twin to point at).
func TestAchievementsOverride_BlankReason_RejectedClientSide(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "override", "ach_1",
			"--contact-id", "ct_456",
			"--reason", "   ",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire when --reason is whitespace-only")
	}
}

// ── earn-verb error hints (status-keyed) ─────────────────────────────────────

// earnHintDetail runs an earn verb against a server answering the given
// status/body and returns the rendered error-envelope detail. Subprocess-
// driven because the JSON:API error envelope is written by main.go after
// os.Exit.
func earnHintDetail(t *testing.T, status int, body string, args ...string) (string, int) {
	t.Helper()
	srv := newMockServer(t, []mockHandler{{Status: status, Body: body}})
	bin := buildBinary(t)
	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + srv.URL,
	}, args...)
	raw := strings.TrimSpace(stderr)
	var envelope struct {
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("stderr not valid JSON:API envelope: %v; stderr=%q", err, raw)
	}
	if len(envelope.Errors) == 0 {
		t.Fatalf("error envelope empty; stderr=%q", raw)
	}
	return envelope.Errors[0].Detail, exitCode
}

// TestAchievementsEarn404_AmbiguousHint pins the earn verbs' 404 hint
// contract (Jay-r + blind review, PR #109): the 404 must name the causes a
// 404 CAN have (gates, achievement, hub containment) and must say a wrong
// contact id is NOT among them — a wrong contact id answers 422/204/409 on
// grant/revoke/restore respectively, never 404, so any contact-id capture
// instruction here would have zero recall and pure false-positive cost.
func TestAchievementsEarn404_AmbiguousHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"grant", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "grant", "ach_1", "--contact-id", "ct_1"}},
		{"revoke", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "revoke", "ach_1", "--contact-id", "ct_1", "--yes"}},
		{"restore", []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "restore", "ach_1", "--contact-id", "ct_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, exitCode := earnHintDetail(t, 404,
				`{"errors":[{"status":"404","detail":"Not found."}]}`, tc.args...)
			if exitCode != errs.ExitNotFound {
				t.Fatalf("exit code = %d, want %d (ExitNotFound)", exitCode, errs.ExitNotFound)
			}
			for _, want := range []string{"ACHIEVEMENTS_ENABLED", "settings.achievements.enabled", "wrong contact id never answers 404"} {
				if !strings.Contains(detail, want) {
					t.Errorf("404 detail must mention %q; got %q", want, detail)
				}
			}
			// No capture instruction on 404 — that diagnosis belongs to the
			// grant 422 / restore 409, where it has actual recall.
			if strings.Contains(detail, "--jq .contact_id") {
				t.Errorf("404 detail must not carry the contact-id capture instruction; got %q", detail)
			}
		})
	}
}

// TestAchievementsGrant_Membership422_NamespaceHint pins the wrong-namespace
// guidance where it actually surfaces on grant: the backend's 422
// achievement_membership_required. Keyed on the transport status, never the
// message string.
func TestAchievementsGrant_Membership422_NamespaceHint(t *testing.T) {
	detail, exitCode := earnHintDetail(t, 422,
		`{"errors":[{"status":"422","code":"achievement_membership_required","detail":"Contact 'ct_1' is not an active member of hub 'hub_1'."}]}`,
		"--team", "t_team1", "--hub", "hub_1", "achievements", "grant", "ach_1", "--contact-id", "ct_1")
	if exitCode != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage, from 422)", exitCode, errs.ExitUsage)
	}
	for _, want := range []string{"GLOBAL contact id", "--jq .contact_id", "team-contact"} {
		if !strings.Contains(detail, want) {
			t.Errorf("grant 422 detail must mention %q; got %q", want, detail)
		}
	}
}

// TestAchievementsRestore_NoEarn409_NamespaceHint pins the same guidance on
// restore's 409 ("No earn exists"), the other place a wrong contact id
// actually lands.
func TestAchievementsRestore_NoEarn409_NamespaceHint(t *testing.T) {
	detail, exitCode := earnHintDetail(t, 409,
		`{"errors":[{"status":"409","code":"achievement_not_revoked","detail":"No earn exists for achievement 'ach_1', contact 'ct_1', hub 'hub_1'."}]}`,
		"--team", "t_team1", "--hub", "hub_1", "achievements", "restore", "ach_1", "--contact-id", "ct_1")
	if exitCode != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage, from 409)", exitCode, errs.ExitUsage)
	}
	for _, want := range []string{"GLOBAL contact id", "--jq .contact_id", "nothing to restore"} {
		if !strings.Contains(detail, want) {
			t.Errorf("restore 409 detail must mention %q; got %q", want, detail)
		}
	}
}
