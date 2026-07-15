package cmd

// media_transcripts_test.go — contract tests for the `mio media transcripts`
// command group (MIO-2289). Reads (get / vtt / content / versions) hit the
// team-scoped transcript router; the write surface is edit (PATCH words) and
// revert (POST version), both deriving JSON:API type "transcripts".

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const transcriptResourceBody = `{"data":{"type":"transcripts","id":"m1","attributes":{"version":2}}}`
const transcriptCollectionBody = `{"data":[]}`

func TestTranscriptGet_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK, transcriptResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "get", "media_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/media/media_x/transcript"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestTranscriptVtt_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK,
		`{"data":{"type":"transcript_vtt","id":"m1","attributes":{"signed_url":"https://x","expires_at":"2026-01-01T00:00:00Z"}}}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "vtt", "media_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/media/media_x/transcript.vtt"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestTranscriptContent_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK, transcriptResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "content", "media_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/media/media_x/transcript/content"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestTranscriptVersionsList_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK, transcriptCollectionBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "versions", "media_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/media/media_x/transcript/versions"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestTranscriptVersionShow_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK, transcriptResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "versions", "media_x", "3")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/media/media_x/transcript/versions/3"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestTranscriptEdit_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, transcriptResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "edit", "media_x",
			"--words", `[{"word":"hello","start_ms":0,"end_ms":500}]`,
			"--language", "en")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch {
		t.Errorf("method=%q want PATCH", *method)
	}
	if want := "/api/v1/teams/t_team1/media/media_x/transcript"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "transcripts" {
		t.Errorf("type=%q want transcripts", typ)
	}
	words, ok := attrs["words"].([]any)
	if !ok || len(words) != 1 {
		t.Fatalf("words attr = %v, want a 1-element array", attrs["words"])
	}
	if attrs["language"] != "en" {
		t.Errorf("language=%v want en", attrs["language"])
	}
}

func TestTranscriptEdit_RequiresWords(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "edit", "media_x")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --words must exit before any HTTP request")
	}
}

func TestTranscriptRevert_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, transcriptResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "revert", "media_x", "--version", "3")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/teams/t_team1/media/media_x/transcript/revert"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "transcripts" {
		t.Errorf("type=%q want transcripts", typ)
	}
	if attrs["version"] != float64(3) {
		t.Errorf("version=%v want 3", attrs["version"])
	}
}

func TestTranscriptRevert_RequiresVersion(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "transcripts", "revert", "media_x")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --version must exit before any HTTP request")
	}
}
