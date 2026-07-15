package client

// upload_test.go — tests for the raw-S3-PUT upload plumbing (MIO-2267): a direct
// binary PUT to a presigned URL (no mio auth header) and resource-level `meta`
// decoding (where the create response carries the presigned upload_url).

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPutFileToURL_PutsRawBytesNoAuth(t *testing.T) {
	var gotMethod, gotAuth, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("ETag", `"etag-xyz"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	etag, err := PutFileToURL(context.Background(), srv.URL, path, "text/plain")
	if err != nil {
		t.Fatalf("PutFileToURL: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (must NOT auth to S3)", gotAuth)
	}
	if gotCT != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", gotCT)
	}
	if string(gotBody) != "hello world" {
		t.Errorf("body = %q, want %q", gotBody, "hello world")
	}
	if etag != "etag-xyz" {
		t.Errorf("etag = %q, want etag-xyz (quotes trimmed)", etag)
	}
}

func TestPutFileToURL_NonSuccessIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Error>AccessDenied</Error>"))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "x.bin")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PutFileToURL(context.Background(), srv.URL, path, ""); err == nil {
		t.Fatal("expected an error on a 403 S3 response, got nil")
	}
}

func TestDecodeResource_PopulatesMeta(t *testing.T) {
	res, err := DecodeResource([]byte(`{"data":{"id":"file_1","type":"files","attributes":{"title":"x"},"meta":{"upload_url":"https://s3.example/put?sig=1"}}}`))
	if err != nil {
		t.Fatalf("DecodeResource: %v", err)
	}
	if res.Meta["upload_url"] != "https://s3.example/put?sig=1" {
		t.Errorf("Meta[upload_url] = %v, want the presigned URL", res.Meta["upload_url"])
	}
	// Meta must not leak into the flattened (agent-facing) view.
	if _, ok := res.Flatten()["meta"]; ok {
		t.Errorf("Flatten() must not include meta")
	}
}
