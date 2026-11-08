package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

func TestAchievementsCreate_MissingTitle(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "create", "--points", "10")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

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

func TestAchievementsUpdate_NothingToUpdate(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestAchievementsUpdate_Clear_SendsExplicitNull(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1", "--clear", "rule-type")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", cap.Method)
	}

	if !strings.Contains(string(cap.Body), `"rule_type":null`) {
		t.Fatalf("request body must contain the literal wire token \"rule_type\":null; body=%q", cap.Body)
	}

	_, attrs := decodeEnvelope(t, cap.Body)
	v, present := attrs["rule_type"]
	if !present {
		t.Fatalf("attributes.rule_type is absent, want an explicit null present; body=%q", cap.Body)
	}
	if v != nil {
		t.Errorf("attributes.rule_type = %v, want nil (JSON null)", v)
	}
}

func TestAchievementsUpdate_Clear_Multiple(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1",
			"--clear", "rule-type",
			"--clear", "rule-criteria",
			"--clear", "rule-threshold",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	for _, key := range []string{"rule_type", "rule_criteria", "rule_threshold"} {
		if !strings.Contains(string(cap.Body), `"`+key+`":null`) {
			t.Errorf("request body must contain the literal wire token %q; body=%q", `"`+key+`":null`, cap.Body)
		}
	}
	_, attrs := decodeEnvelope(t, cap.Body)
	if len(attrs) != 3 {
		t.Errorf("attributes = %v, want exactly 3 keys (rule_type, rule_criteria, rule_threshold)", attrs)
	}
}

func TestAchievementsUpdate_Clear_CommaSeparated(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1",
			"--clear", "rule-type,rule-criteria",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	_, attrs := decodeEnvelope(t, cap.Body)
	if len(attrs) != 2 {
		t.Errorf("attributes = %v, want exactly 2 keys (rule_type, rule_criteria)", attrs)
	}
}

func TestAchievementsUpdate_Clear_WithNormalFlag(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1",
			"--award-mode", "manual",
			"--clear", "rule-type",
			"--clear", "rule-criteria",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if cap.Method != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", cap.Method)
	}
	_, attrs := decodeEnvelope(t, cap.Body)
	if len(attrs) != 3 {
		t.Errorf("attributes = %v, want exactly 3 keys (award_mode, rule_type, rule_criteria)", attrs)
	}
	if attrs["award_mode"] != "manual" {
		t.Errorf("attributes.award_mode = %v, want \"manual\"", attrs["award_mode"])
	}
	for _, key := range []string{"rule_type", "rule_criteria"} {
		v, present := attrs[key]
		if !present || v != nil {
			t.Errorf("attributes.%s = %v (present=%v), want an explicit null", key, v, present)
		}
	}
}

func TestAchievementsUpdate_Clear_UnknownField(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1", "--clear", "not-a-real-field")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire when --clear names an unknown field")
	}
}

func TestAchievementsUpdate_Clear_ConflictsWithSet(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1",
			"--rule-type", "milestone", "--clear", "rule-type")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request may fire when a field is both set and cleared")
	}
}

func TestAchievementsUpdate_NoClear_NoStrayNulls(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, achievementBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "achievements", "update", "ach_1", "--title", "New Title")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	_, attrs := decodeEnvelope(t, cap.Body)
	if len(attrs) != 1 {
		t.Errorf("attributes = %v, want exactly 1 key (title) — no stray nulls without --clear", attrs)
	}
	for k, v := range attrs {
		if v == nil {
			t.Errorf("attributes.%s is an unexpected null; attrs=%v", k, attrs)
		}
	}
}

func TestAchievementsUpdate_RulePiecesNotAllowed_Hint(t *testing.T) {
	detail, exitCode := earnHintDetail(t, 422,
		`{"errors":[{"status":"422","code":"rule_pieces_not_allowed","detail":"A manual badge cannot carry rule pieces.","source":{"pointer":"/data/attributes/rule_type"}}]}`,
		"--team", "t_team1", "achievements", "update", "ach_1", "--award-mode", "manual")

	if exitCode != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage, from 422)", exitCode, errs.ExitUsage)
	}
	for _, want := range []string{"--clear rule-type", "--clear rule-criteria", "--clear rule-threshold"} {
		if !strings.Contains(detail, want) {
			t.Errorf("422 detail must mention %q; got %q", want, detail)
		}
	}
	if !strings.Contains(detail, "not reliable") {
		t.Errorf("422 detail must warn that the source pointer is not reliable; got %q", detail)
	}
}

func TestAchievementsUpdate_OtherValidation422_NoHint(t *testing.T) {
	detail, exitCode := earnHintDetail(t, 422,
		`{"errors":[{"status":"422","code":"rule_type_not_available","detail":"streak is not yet available."}]}`,
		"--team", "t_team1", "achievements", "update", "ach_1", "--rule-type", "streak")

	if exitCode != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage, from 422)", exitCode, errs.ExitUsage)
	}
	if strings.Contains(detail, "--clear") {
		t.Errorf("422 for an unrelated code must not carry the --clear hint; got %q", detail)
	}
}

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

const ruleSetPath = "/api/v1/teams/t_team1/hubs/hub_123/achievements/ach_1/rule"

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

func TestAchievementsGrant_BodyShape(t *testing.T) {
	srv, cap := captureServer(t, http.StatusCreated, achievementEarnBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "grant", "ach_1",
			"--contact-id", "ct_456",
			"--reason", "manual",
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
	if attrs["award_reason"] != "manual" {
		t.Errorf("attributes.award_reason = %v, want \"manual\"", attrs["award_reason"])
	}
}

func TestAchievementsGrant_NonManualReason_RejectedClientSide(t *testing.T) {
	srv, fired := firedAnyServer(t)

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"achievements", "grant", "ach_1",
			"--contact-id", "ct_456",
			"--reason", "community week winner",
		)...)

	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); err=%q", got, errs.ExitUsage, err)
	}
	if *fired {
		t.Error("no HTTP request may fire for a non-\"manual\" --reason")
	}
	if !strings.Contains(err.Error(), "manual") {
		t.Errorf("error = %q, want it to name the \"manual\" constraint", err)
	}
}

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
	if !strings.Contains(res.Stdout, "Revoke accepted") || !strings.Contains(res.Stdout, "does not confirm") {
		t.Errorf("stdout must disclose the idempotent 204 (\"Revoke accepted\" + \"does not confirm\"); got %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "Revoked achievement") {
		t.Errorf("stdout must not flatly claim an earn was revoked — 204 does not confirm one existed; got %q", res.Stdout)
	}
}

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
			if strings.Contains(detail, "--jq .contact_id") {
				t.Errorf("404 detail must not carry the contact-id capture instruction; got %q", detail)
			}
		})
	}
}

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
