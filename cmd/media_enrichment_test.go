package cmd

// media_enrichment_test.go — contract tests for the MIO-2266 media-enrichment
// commands:
//
//	media files cards    get|set   (PUT full-replace, JSON:API type file_cards)
//	media files chapters get|set   (PUT full-replace, JSON:API type file_chapters)
//	media folders move              (POST, JSON:API type "folders" — NOT "move")
//	media search                    (GET /search/media?q=…)
//	media hub-media publish|list|unpublish (standalone files, type hub_media)
//
// Each write pins method + path suffix + JSON:API type + key attributes + exit
// code; each required-flag/enum has a case asserting ExitUsage fires NO request.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// emptyCollectionBody is a minimal JSON:API collection the client can decode for
// the cards/chapters/search/list read paths (which expect a `data: [...]`).
const emptyCollectionBody = `{"data":[],"meta":{"count":0},"links":{"next":null}}`

// captureMediaRequest starts a server recording the first request's method,
// path, raw query, and body, replying with respBody at the given status.
func captureMediaRequest(t *testing.T, status int, respBody string) (
	srv *httptest.Server, method, path, rawQuery *string, body *[]byte,
) {
	t.Helper()
	var gotMethod, gotPath, gotQuery string
	var gotBody []byte
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotMethod, &gotPath, &gotQuery, &gotBody
}

// firedServer returns a server that flips *fired true on ANY request, used to
// prove a client-side usage error fired NO HTTP request.
func firedServer(t *testing.T, fired *bool, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ─── media files cards ─────────────────────────────────────────────────────────

func TestMediaFilesCardsGet_Path(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureMediaRequest(t, http.StatusOK, emptyCollectionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "cards", "get", "file_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/files/file_1/cards") {
		t.Errorf("path %q does not end with /files/file_1/cards", *gotPath)
	}
}

func TestMediaFilesCardsSet_Body(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureMediaRequest(t, http.StatusOK, emptyCollectionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "cards", "set", "file_1",
			"--cards", `[{"label":"Buy","start":15000,"url":"https://x.co"}]`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/files/file_1/cards") {
		t.Errorf("path %q does not end with /files/file_1/cards", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "file_cards" {
		t.Errorf("data.type = %q, want file_cards (typeOverride files/cards)", typ)
	}
	cards, ok := attrs["cards"].([]any)
	if !ok || len(cards) != 1 {
		t.Fatalf("attributes.cards = %#v, want a 1-element array", attrs["cards"])
	}
	card0, _ := cards[0].(map[string]any)
	if card0["label"] != "Buy" {
		t.Errorf("cards[0].label = %v, want Buy", card0["label"])
	}
	if card0["start"] != float64(15000) {
		t.Errorf("cards[0].start = %#v, want 15000", card0["start"])
	}
}

func TestMediaFilesCardsSet_ClearWithEmptyArray(t *testing.T) {
	srv, _, _, _, gotBody := captureMediaRequest(t, http.StatusOK, emptyCollectionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "cards", "set", "file_1", "--cards", `[]`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *gotBody)
	cards, ok := attrs["cards"].([]any)
	if !ok || len(cards) != 0 {
		t.Errorf("attributes.cards = %#v, want empty array (clear)", attrs["cards"])
	}
}

func TestMediaFilesCardsSet_RequiresCards(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, emptyCollectionBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "cards", "set", "file_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("missing --cards must exit before any HTTP request")
	}
}

func TestMediaFilesCardsSet_RejectsNonArray(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, emptyCollectionBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "cards", "set", "file_1", "--cards", `{"label":"x"}`)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("a non-array --cards must exit before any HTTP request")
	}
}

func TestMediaFilesCardsSet_RejectsInvalidJSON(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, emptyCollectionBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "cards", "set", "file_1", "--cards", `not-json`)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("malformed --cards must exit before any HTTP request")
	}
}

// ─── media files chapters ────────────────────────────────────────────────────

func TestMediaFilesChaptersGet_Path(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureMediaRequest(t, http.StatusOK, emptyCollectionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "chapters", "get", "file_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/files/file_1/chapters") {
		t.Errorf("path %q does not end with /files/file_1/chapters", *gotPath)
	}
}

func TestMediaFilesChaptersSet_Body(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureMediaRequest(t, http.StatusOK, emptyCollectionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "chapters", "set", "file_1",
			"--chapters", `[{"title":"Intro","start":0},{"title":"Demo","start":60000}]`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/files/file_1/chapters") {
		t.Errorf("path %q does not end with /files/file_1/chapters", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "file_chapters" {
		t.Errorf("data.type = %q, want file_chapters (typeOverride files/chapters)", typ)
	}
	chapters, ok := attrs["chapters"].([]any)
	if !ok || len(chapters) != 2 {
		t.Fatalf("attributes.chapters = %#v, want a 2-element array", attrs["chapters"])
	}
	ch0, _ := chapters[0].(map[string]any)
	if ch0["title"] != "Intro" {
		t.Errorf("chapters[0].title = %v, want Intro", ch0["title"])
	}
}

func TestMediaFilesChaptersSet_RequiresChapters(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, emptyCollectionBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "chapters", "set", "file_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("missing --chapters must exit before any HTTP request")
	}
}

// ─── media folders move ──────────────────────────────────────────────────────

func TestMediaFoldersMove_ParentBody(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureMediaRequest(t, http.StatusOK, minimalHubBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "move", "folder_1", "--parent-id", "folder_2")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/folders/folder_1/move") {
		t.Errorf("path %q does not end with /folders/folder_1/move", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "folders" {
		t.Errorf("data.type = %q, want folders (typeOverride folders/move — NOT move)", typ)
	}
	if attrs["new_parent_id"] != "folder_2" {
		t.Errorf("new_parent_id = %v, want folder_2", attrs["new_parent_id"])
	}
}

func TestMediaFoldersMove_ToRootBody(t *testing.T) {
	srv, _, _, _, gotBody := captureMediaRequest(t, http.StatusOK, minimalHubBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "move", "folder_1", "--to-root")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *gotBody)
	// new_parent_id must be present AND explicitly null (move to root).
	v, ok := attrs["new_parent_id"]
	if !ok {
		t.Errorf("new_parent_id key must be present (null); attrs=%v", attrs)
	}
	if v != nil {
		t.Errorf("new_parent_id = %#v, want null (to-root)", v)
	}
}

func TestMediaFoldersMove_RequiresTarget(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "move", "folder_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("missing move target must exit before any HTTP request")
	}
}

func TestMediaFoldersMove_RejectsBothTargets(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "folders", "move", "folder_1",
			"--parent-id", "folder_2", "--to-root")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("both --parent-id and --to-root must exit before any HTTP request")
	}
}

// ─── media search ────────────────────────────────────────────────────────────

func TestMediaSearch_Query(t *testing.T) {
	srv, gotMethod, gotPath, gotQuery, _ := captureMediaRequest(t, http.StatusOK, emptyCollectionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "search",
			"--query", "onboarding", "--hub-id", "hub_9", "--limit", "50")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/search/media") {
		t.Errorf("path %q does not end with /search/media", *gotPath)
	}
	vals, err := url.ParseQuery(*gotQuery)
	if err != nil {
		t.Fatalf("bad query %q: %v", *gotQuery, err)
	}
	if vals.Get("q") != "onboarding" {
		t.Errorf("q = %q, want onboarding", vals.Get("q"))
	}
	if vals.Get("hub_id") != "hub_9" {
		t.Errorf("hub_id = %q, want hub_9", vals.Get("hub_id"))
	}
	if vals.Get("page[size]") != "50" {
		t.Errorf("page[size] = %q, want 50", vals.Get("page[size]"))
	}
}

func TestMediaSearch_RequiresQuery(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, emptyCollectionBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "search")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("missing --query must exit before any HTTP request")
	}
}

func TestMediaSearch_RejectsOutOfRangeLimit(t *testing.T) {
	for _, limit := range []string{"0", "-1", "101"} {
		t.Run("limit="+limit, func(t *testing.T) {
			fired := false
			srv := firedServer(t, &fired, emptyCollectionBody)
			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1", "media", "search", "--query", "x", "--limit", limit)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
			}
			if fired {
				t.Errorf("out-of-range --limit %s must exit before any HTTP request", limit)
			}
		})
	}
}

// ─── media hub-media (standalone files) ──────────────────────────────────────

func TestMediaHubMediaPublish_Body(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureMediaRequest(t, http.StatusCreated, minimalHubBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"media", "hub-media", "publish",
			"--file-id", "file_abc", "--visibility", "public", "--position", "2")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/media") {
		t.Errorf("path %q does not end with /hubs/hub_123/media", *gotPath)
	}
	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_media" {
		t.Errorf("data.type = %q, want hub_media (typeOverride hubs/media)", typ)
	}
	if attrs["file_id"] != "file_abc" {
		t.Errorf("file_id = %v, want file_abc", attrs["file_id"])
	}
	if attrs["visibility"] != "public" {
		t.Errorf("visibility = %v, want public", attrs["visibility"])
	}
	if attrs["position"] != float64(2) {
		t.Errorf("position = %#v, want 2", attrs["position"])
	}
	if _, leaked := attrs["file-id"]; leaked {
		t.Errorf("attributes.file-id must not be present; got %v", attrs)
	}
}

func TestMediaHubMediaPublish_RequiresFileID(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "media", "hub-media", "publish")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("missing --file-id must exit before any HTTP request")
	}
}

func TestMediaHubMediaPublish_RejectsInvalidVisibility(t *testing.T) {
	fired := false
	srv := firedServer(t, &fired, minimalHubBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "media", "hub-media", "publish",
			"--file-id", "file_abc", "--visibility", "everyone")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit = %d, want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("invalid --visibility must exit before any HTTP request")
	}
}

func TestMediaHubMediaList_Path(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureMediaRequest(t, http.StatusOK, emptyCollectionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "media", "hub-media", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/media") {
		t.Errorf("path %q does not end with /hubs/hub_123/media", *gotPath)
	}
}

func TestMediaHubMediaUnpublish_DeletesPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "media", "hub-media", "unpublish", "file_abc", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/media/file_abc") {
		t.Errorf("path %q does not end with /hubs/hub_123/media/file_abc", gotPath)
	}
}

func TestMediaHubMediaUnpublish_RequiresYes(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "media", "hub-media", "unpublish", "file_abc")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit = %d, want ExitNeedsConfir (5); stderr=%q", res.Code, res.Stderr)
	}
	if fired {
		t.Error("unpublish without --yes must not fire a DELETE")
	}
}
