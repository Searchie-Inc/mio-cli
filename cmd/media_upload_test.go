package cmd

// media_upload_test.go — contract tests for the media ingest commands
// (MIO-2267): `files upload` (create → S3 PUT → finalize orchestration),
// `files finalize`, `files transcode`, and `files register-synthetic`.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestFilesUpload_Orchestration(t *testing.T) {
	var createHit, putHit, finalizeHit bool
	var createBody, putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/teams/t_team1/files":
			createHit = true
			createBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"data":{"id":"file_new","type":"files","attributes":{"title":"x","status_upload":"PENDING"},"meta":{"upload_url":%q}}}`, "http://"+r.Host+"/s3put")
		case r.Method == http.MethodPut && r.URL.Path == "/s3put":
			putHit = true
			putBody, _ = io.ReadAll(r.Body)
			w.Header().Set("ETag", `"etag1"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/teams/t_team1/files/file_new/finalize":
			finalizeHit = true
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"data":{"id":"file_new","type":"files","attributes":{"status_upload":"READY"}}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, []byte("the contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", path, "--title", "My Doc")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !createHit || !putHit || !finalizeHit {
		t.Fatalf("create=%v put=%v finalize=%v — all three must fire", createHit, putHit, finalizeHit)
	}
	if string(putBody) != "the contents" {
		t.Errorf("S3 PUT body=%q, want file contents", putBody)
	}
	typ, attrs := decodeDataTypeAttrs(t, createBody)
	if typ != "files" {
		t.Errorf("create type=%q want files", typ)
	}
	if attrs["title"] != "My Doc" {
		t.Errorf("title=%v want My Doc", attrs["title"])
	}
	if attrs["size_bytes"] != float64(len("the contents")) {
		t.Errorf("size_bytes=%v want %d", attrs["size_bytes"], len("the contents"))
	}
	if s, _ := attrs["mime_type"].(string); s == "" {
		t.Errorf("mime_type missing from create body")
	}
}

func TestFilesUpload_MissingFile(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "upload", "/no/such/file.txt")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("a missing file must exit before any HTTP request")
	}
}

func TestFilesFinalize_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK,
		`{"data":{"id":"file_x","type":"files","attributes":{"status_upload":"READY"}}}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "finalize", "file_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/teams/t_team1/files/file_x/finalize"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestFilesTranscode_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusAccepted,
		`{"data":{"id":"file_x","type":"files","attributes":{"status_transcode":"PENDING"}}}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "transcode", "file_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/teams/t_team1/files/file_x/transcode"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestFilesRegisterSynthetic_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusCreated,
		`{"data":{"id":"file_syn","type":"files","attributes":{"status_upload":"READY"}}}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "register-synthetic", "--title", "Stub Doc", "--asset-kind", "pdf")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/admin/teams/t_team1/files/synthetic"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "files" {
		t.Errorf("type=%q want files", typ)
	}
	if attrs["title"] != "Stub Doc" {
		t.Errorf("title=%v want Stub Doc", attrs["title"])
	}
	if attrs["asset_kind"] != "pdf" {
		t.Errorf("asset_kind=%v want pdf", attrs["asset_kind"])
	}
}

func TestFilesRegisterSynthetic_RequiresTitle(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "files", "register-synthetic", "--asset-kind", "document")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --title must exit before any HTTP request")
	}
}
