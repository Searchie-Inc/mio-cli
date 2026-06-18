package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestEmailTemplatesUpdate_ContentFlagsMapToBackendFields verifies the MIO-1238
// fix: the backend email_templates write schema is mjml_source + plain_text, NOT
// a "body" attribute. --body must therefore map to mjml_source (so it actually
// sets the rendered content) and --plain-text to plain_text. Before the fix,
// --body produced an attrs["body"] the backend silently dropped, so templates
// never got content and drip sends failed with render_error.
func TestEmailTemplatesUpdate_ContentFlagsMapToBackendFields(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"type":"email_templates","id":"tmpl_1","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		append(
			[]string{"--hub", "hub_123"},
			withTeam("t_team1",
				"email", "templates", "update", "tmpl_1",
				"--subject", "S",
				"--body", "<mjml><mj-body></mj-body></mjml>",
				"--plain-text", "hello",
			)...,
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	attrs := doc.Data.Attributes

	if attrs["mjml_source"] != "<mjml><mj-body></mj-body></mjml>" {
		t.Errorf("--body must map to attributes.mjml_source, got %v", attrs["mjml_source"])
	}
	if attrs["plain_text"] != "hello" {
		t.Errorf("--plain-text must map to attributes.plain_text, got %v", attrs["plain_text"])
	}
	if _, ok := attrs["body"]; ok {
		t.Errorf("must NOT send a bare 'body' attribute (the backend silently drops it)")
	}
	if attrs["subject"] != "S" {
		t.Errorf("attributes.subject = %v, want \"S\"", attrs["subject"])
	}
}

// TestEmailTemplatesCreate_ContentFlagsMapToBackendFields covers the create path,
// which shares the same --body -> mjml_source / --plain-text -> plain_text mapping
// (MIO-1238).
func TestEmailTemplatesCreate_ContentFlagsMapToBackendFields(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"type":"email_templates","id":"tmpl_1","attributes":{}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		append(
			[]string{"--hub", "hub_123"},
			withTeam("t_team1",
				"email", "templates", "create",
				"--name", "Welcome",
				"--subject", "S",
				"--body", "<mjml></mjml>",
				"--plain-text", "hello",
			)...,
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	attrs := doc.Data.Attributes

	if attrs["mjml_source"] != "<mjml></mjml>" {
		t.Errorf("--body must map to attributes.mjml_source, got %v", attrs["mjml_source"])
	}
	if attrs["plain_text"] != "hello" {
		t.Errorf("--plain-text must map to attributes.plain_text, got %v", attrs["plain_text"])
	}
	if _, ok := attrs["body"]; ok {
		t.Errorf("must NOT send a bare 'body' attribute (the backend silently drops it)")
	}
}
