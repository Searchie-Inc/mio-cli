package cmd

// contactattributes_test.go — contract tests for the request/response wire
// shapes of `mio contact-attributes values set/get` and
// `hub-config create` (MIO-2497 / MIO-2501 / MIO-2502).
//
// MIO-2497 (values set): the bulk PATCH body must carry `data` as a LIST of
//   set-operation objects ({type:"set", attributes:{definition_slug, value_text}}),
//   not a JSON:API object at /data. The backend BulkValuePatchEnvelope binds
//   `data: list[ValueOperation]` (extra="forbid"), so an object 400s with
//   "Input should be a valid list (/data)".
//
// MIO-2501 (values get): GET .../attributes returns a LIST
//   (ContactValueListResponse), so the command must decode a collection, not a
//   single resource (Retrieve would fail: "cannot unmarshal array into ...").
//
// MIO-2502 (hub-config create): create must POST to the COLLECTION path
//   .../hubs/{hub}/contact-attributes (NOT the /{definition_id}-suffixed path,
//   which only supports PATCH/DELETE and 405s on POST) and carry the
//   definition id in data.attributes.definition_id.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// minimalContactValueListBody is a canned ContactValueListResponse (a LIST at
// /data) returned by the values set (PATCH) and get (GET) endpoints.
const minimalContactValueListBody = `{"data":[{"id":"tcid_abc123:def_1","type":"contact_attribute_values","attributes":{"definition_id":"def_1","definition_slug":"company","value_text":"Acme"}}]}`

// minimalHubConfigBody is a canned single HubConfigResponse resource.
const minimalHubConfigBody = `{"data":{"id":"cfg_1","type":"contact_attribute_hub_configs","attributes":{"definition_id":"attr_abc123","position":1}}}`

// TestContactAttributesValuesSet_SendsDataList (MIO-2497) verifies `values set`
// PATCHes .../attributes with a bare {data:[{type:"set",attributes:{...}}]} LIST,
// one element per --attr, mapping key→definition_slug and value→value_text.
func TestContactAttributesValuesSet_SendsDataList(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalContactValueListBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "company=Acme", "--attr", "tier=enterprise",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/contacts/tcid_abc123/attributes") {
		t.Errorf("path %q does not end with /contacts/tcid_abc123/attributes", gotPath)
	}

	var doc struct {
		Data []struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("body /data is not a valid JSON list: %v; body=%q", err, gotBody)
	}
	if len(doc.Data) != 2 {
		t.Fatalf("data should be a 2-item list (one per --attr); got %#v", doc.Data)
	}
	want := map[string]string{"company": "Acme", "tier": "enterprise"}
	for i, op := range doc.Data {
		if op.Type != "set" {
			t.Errorf("data[%d].type = %q, want \"set\"", i, op.Type)
		}
		slug, _ := op.Attributes["definition_slug"].(string)
		val, _ := op.Attributes["value_text"].(string)
		if wantVal, ok := want[slug]; !ok || wantVal != val {
			t.Errorf("data[%d] = {definition_slug:%q, value_text:%q}, not a matching --attr pair", i, slug, val)
		}
	}
}

// TestContactAttributesValuesGet_DecodesCollection (MIO-2501) verifies `values
// get` decodes a LIST response (collection) and exits 0, rather than trying to
// decode the array as a single resource.
func TestContactAttributesValuesGet_DecodesCollection(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalContactValueListBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "get", "tcid_abc123",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/contacts/tcid_abc123/attributes") {
		t.Errorf("path %q does not end with /contacts/tcid_abc123/attributes", gotPath)
	}
	// The set value must be rendered from the collection.
	if !strings.Contains(res.Stdout, "def_1") {
		t.Errorf("stdout should render the value array; got %q", res.Stdout)
	}
}

// TestContactAttributesCreate_RejectsInvalidFieldType (MIO-2543) verifies the NEW
// client-side --field-type enum validation added with the pure-builder extraction:
// an out-of-enum field type now exits ExitUsage and fires NO request, instead of
// round-tripping to a backend 422. (The valid-field-type body guards live in
// jake_qa_drift_test.go and are unchanged.)
func TestContactAttributesCreate_RejectsInvalidFieldType(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalHubConfigBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Company", "--slug", "company", "--field-type", "select",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for invalid --field-type); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("an invalid --field-type must exit before any HTTP request")
	}
}

// TestContactAttributesHubConfigCreate_CollectionPathAndBodyId (MIO-2502)
// verifies create POSTs to the COLLECTION path (path ends with
// /contact-attributes, no trailing definition id) and carries the definition id
// in data.attributes.definition_id.
func TestContactAttributesHubConfigCreate_CollectionPathAndBodyId(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubConfigBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_xyz",
			"contact-attributes", "hub-config", "create", "attr_abc123",
			"--position", "1",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_xyz/contact-attributes") {
		t.Errorf("path %q must be the collection path (end with /contact-attributes, no trailing def id)", gotPath)
	}
	attrs := decodeHubAttrs(t, gotBody)
	if attrs["definition_id"] != "attr_abc123" {
		t.Errorf("data.attributes.definition_id = %v, want attr_abc123", attrs["definition_id"])
	}
}
