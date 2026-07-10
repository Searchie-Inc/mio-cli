package cmd

// community_moderation_test.go — contract tests for the community moderation
// console (MIO-2265): report-reasons CRUD, comments admin list/delete, the
// moderation reads (queue/counts/audit-log/banned/removed), content
// view/remove/restore, reports get/resolve, and members soft-ban.
//
// Each command pins method + path suffix + (for writes) JSON:API type + key
// attrs + exit code, and every required-flag / enum has a case asserting
// ExitUsage with NO HTTP request fired.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// moderationFiredServer returns a server that flips *fired and replies 2xx with the given
// body. Used to prove client-side validation fires before any HTTP request.
func moderationFiredServer(t *testing.T, fired *bool, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// decodeDataTypeIDAttrs returns data.type, data.id and data.attributes.
func decodeDataTypeIDAttrs(t *testing.T, body []byte) (string, string, map[string]any) {
	t.Helper()
	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			ID         string         `json:"id"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, body)
	}
	return doc.Data.Type, doc.Data.ID, doc.Data.Attributes
}

// ── report-reasons ──────────────────────────────────────────────────────────

func TestReportReasonsList_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "community", "report-reasons", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/report-reasons") {
		t.Errorf("path %q does not end with /hubs/hub_123/report-reasons", *gotPath)
	}
}

func TestReportReasonsCreate_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "report-reasons", "create", "--label", "Spam", "--position", "3")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/report-reasons") {
		t.Errorf("path %q does not end with /report-reasons", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "report_reasons" {
		t.Errorf("data.type = %q, want report_reasons", typ)
	}
	if attrs["label"] != "Spam" {
		t.Errorf("label = %v, want Spam", attrs["label"])
	}
	if attrs["position"] != float64(3) {
		t.Errorf("position = %#v, want 3", attrs["position"])
	}
}

func TestReportReasonsCreate_RequiresLabel(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusCreated, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "report-reasons", "create", "--position", "1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("a create missing --label must exit before any HTTP request")
	}
}

func TestReportReasonsCreate_RejectsNegativePosition(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusCreated, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "report-reasons", "create", "--label", "X", "--position", "-1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("a negative --position must exit before any HTTP request")
	}
}

func TestReportReasonsUpdate_BodyCarriesTypeAndID(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "report-reasons", "update", "rr_9",
			"--label", "Renamed", "--is-active=false")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/report-reasons/rr_9") {
		t.Errorf("path %q does not end with /report-reasons/rr_9", *gotPath)
	}
	typ, id, attrs := decodeDataTypeIDAttrs(t, *gotBody)
	if typ != "report_reasons" {
		t.Errorf("data.type = %q, want report_reasons", typ)
	}
	if id != "rr_9" {
		t.Errorf("data.id = %q, want rr_9 (backend schema requires data.id)", id)
	}
	if attrs["label"] != "Renamed" {
		t.Errorf("label = %v, want Renamed", attrs["label"])
	}
	if attrs["is_active"] != false {
		t.Errorf("is_active = %v, want false", attrs["is_active"])
	}
}

func TestReportReasonsUpdate_RequiresAField(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "report-reasons", "update", "rr_9")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("an update with no field flags must exit before any HTTP request")
	}
}

func TestReportReasonsDelete_PathAndConfirm(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "report-reasons", "delete", "rr_9", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/report-reasons/rr_9") {
		t.Errorf("path %q does not end with /report-reasons/rr_9", *gotPath)
	}
}

func TestReportReasonsDelete_NonTTYWithoutYesBlocks(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "report-reasons", "delete", "rr_9")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit = %d, want ExitNeedsConfir (5); stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("a delete without --yes must not fire an HTTP request in a non-TTY")
	}
}

// ── comments admin ──────────────────────────────────────────────────────────

func TestCommentsList_PathAndFilters(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "comments", "list",
			"--target-type", "discussion", "--target-id", "disc_1", "--limit", "10")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/comments") {
		t.Errorf("path %q does not end with /hubs/hub_123/comments", gotPath)
	}
	for _, want := range []string{"filter%5Btarget_type%5D=discussion", "filter%5Btarget_id%5D=disc_1", "page%5Bsize%5D=10"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
}

func TestCommentsList_RejectsBadTargetType(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, `{"data":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "comments", "list", "--target-type", "message", "--target-id", "x")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("an invalid --target-type must exit before any HTTP request")
	}
}

func TestCommentsDelete_PathAndConfirm(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "comments", "delete", "cmt_5", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/comments/cmt_5") {
		t.Errorf("path %q does not end with /comments/cmt_5", *gotPath)
	}
}

func TestCommentsDelete_NonTTYWithoutYesBlocks(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "comments", "delete", "cmt_5")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit = %d, want ExitNeedsConfir (5); stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("a delete without --yes must not fire an HTTP request in a non-TTY")
	}
}

// ── moderation reads ────────────────────────────────────────────────────────

func TestModerationQueue_PathAndQuery(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "queue",
			"--status", "pending", "--reportable-type", "comment", "--sort", "-report_count", "--limit", "5")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(gotPath, "/moderation/queue") {
		t.Errorf("path %q does not end with /moderation/queue", gotPath)
	}
	for _, want := range []string{"filter%5Bstatus%5D=pending", "filter%5Breportable_type%5D=comment", "sort=-report_count", "page%5Bsize%5D=5"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
}

func TestModerationQueue_RejectsBadSort(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, `{"data":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "queue", "--sort", "created_at")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("an invalid --sort must exit before any HTTP request")
	}
}

func TestModerationQueue_RejectsBadReportableType(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, `{"data":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "queue", "--reportable-type", "reply")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("an invalid --reportable-type must exit before any HTTP request")
	}
}

func TestModerationCounts_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "community", "moderation", "counts")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/moderation/counts") {
		t.Errorf("path %q does not end with /moderation/counts", *gotPath)
	}
}

func TestModerationAuditLog_PathAndFilters(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "audit-log",
			"--action-type", "ban_member", "--admin-user-id", "u_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(gotPath, "/moderation/audit-log") {
		t.Errorf("path %q does not end with /moderation/audit-log", gotPath)
	}
	for _, want := range []string{"filter%5Baction_type%5D=ban_member", "filter%5Badmin_user_id%5D=u_1"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
}

func TestModerationBanned_RejectsBadSort(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, `{"data":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "banned", "--sort", "-report_count")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("an invalid banned --sort must exit before any HTTP request")
	}
}

func TestModerationRemoved_PathAndRejectsBadContentType(t *testing.T) {
	// valid path
	srv, _, gotPath, _ := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "removed", "--content-type", "comment", "--sort", "removed_at")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.HasSuffix(*gotPath, "/moderation/removed") {
		t.Errorf("path %q does not end with /moderation/removed", *gotPath)
	}
	// bad content-type → no request
	fired := false
	srv2 := moderationFiredServer(t, &fired, http.StatusOK, `{"data":[]}`)
	res2 := runContract(t, baseEnv(srv2.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "removed", "--content-type", "message")...)
	if res2.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res2.Code, res2.Stderr)
	}
	if fired {
		t.Error("an invalid --content-type must exit before any HTTP request")
	}
}

// ── moderation content view / remove / restore ──────────────────────────────

func TestModerationContentView_PathAndEnum(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "content", "view", "comment", "cmt_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/moderation/content/comment/cmt_1") {
		t.Errorf("path %q does not end with /moderation/content/comment/cmt_1", *gotPath)
	}

	fired := false
	srv2 := moderationFiredServer(t, &fired, http.StatusOK, minimalHubBody)
	res2 := runContract(t, baseEnv(srv2.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "content", "view", "reply", "x")...)
	if res2.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res2.Code, res2.Stderr)
	}
	if fired {
		t.Error("an invalid content_type must exit before any HTTP request")
	}
}

func TestModerationContentRemove_PathAndConfirm(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusCreated)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "content", "remove", "discussion", "disc_1", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/moderation/content/discussion/disc_1/remove") {
		t.Errorf("path %q does not end with .../content/discussion/disc_1/remove", *gotPath)
	}
}

func TestModerationContentRemove_NonTTYWithoutYesBlocks(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusCreated, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "content", "remove", "discussion", "disc_1")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit = %d, want ExitNeedsConfir (5); stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("content remove without --yes must not fire an HTTP request in a non-TTY")
	}
}

func TestModerationContentRestore_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusCreated)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "content", "restore", "comment", "cmt_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/moderation/content/comment/cmt_1/restore") {
		t.Errorf("path %q does not end with .../content/comment/cmt_1/restore", *gotPath)
	}
}

// ── moderation reports get / resolve ────────────────────────────────────────

func TestModerationReportsGet_Path(t *testing.T) {
	srv, gotMethod, gotPath, _ := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "reports", "get", "rep_1")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/moderation/reports/rep_1") {
		t.Errorf("path %q does not end with /moderation/reports/rep_1", *gotPath)
	}
}

func TestModerationReportsResolve_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusOK)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "reports", "resolve", "rep_1",
			"--resolution", "soft_banned", "--soft-ban-until", "2026-08-01T00:00:00Z", "--notes", "n")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/moderation/reports/rep_1") {
		t.Errorf("path %q does not end with /moderation/reports/rep_1", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "moderation_reports" {
		t.Errorf("data.type = %q, want moderation_reports", typ)
	}
	if attrs["resolution"] != "soft_banned" {
		t.Errorf("resolution = %v, want soft_banned", attrs["resolution"])
	}
	if attrs["soft_ban_until"] != "2026-08-01T00:00:00Z" {
		t.Errorf("soft_ban_until = %v, want 2026-08-01T00:00:00Z", attrs["soft_ban_until"])
	}
	if attrs["notes"] != "n" {
		t.Errorf("notes = %v, want n", attrs["notes"])
	}
}

func TestModerationReportsResolve_RequiresResolution(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "reports", "resolve", "rep_1", "--notes", "n")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("resolve missing --resolution must exit before any HTTP request")
	}
}

func TestModerationReportsResolve_RejectsBadResolution(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusOK, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "moderation", "reports", "resolve", "rep_1", "--resolution", "ignored")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("an invalid --resolution must exit before any HTTP request")
	}
}

// ── members soft-ban ────────────────────────────────────────────────────────

func TestMembersSoftBan_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "members", "soft-ban", "contact_9",
			"--reason", "spamming", "--until", "2026-08-01T00:00:00Z", "--notes", "n")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/members/contact_9/soft_ban") {
		t.Errorf("path %q does not end with /members/contact_9/soft_ban", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "moderation_actions" {
		t.Errorf("data.type = %q, want moderation_actions (NOT hub_memberships)", typ)
	}
	if attrs["reason"] != "spamming" {
		t.Errorf("reason = %v, want spamming", attrs["reason"])
	}
	if attrs["soft_ban_until"] != "2026-08-01T00:00:00Z" {
		t.Errorf("soft_ban_until = %v, want 2026-08-01T00:00:00Z", attrs["soft_ban_until"])
	}
	if attrs["notes"] != "n" {
		t.Errorf("notes = %v, want n", attrs["notes"])
	}
}

func TestMembersSoftBan_RejectsBadReason(t *testing.T) {
	fired := false
	srv := moderationFiredServer(t, &fired, http.StatusCreated, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"community", "members", "soft-ban", "contact_9", "--reason", "being_rude")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("an invalid --reason must exit before any HTTP request")
	}
}
