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
// one element per --attr, mapping key→definition_slug and (for text attributes)
// value→value_text. Both slugs are text definitions, so the value stays value_text.
func TestContactAttributesValuesSet_SendsDataList(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contact-attributes"):
			// The def-type lookup (MIO-2553): company and notes are text.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(minimalDefsListBody))
		default:
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(minimalContactValueListBody))
		}
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "company=Acme", "--attr", "notes=hello",
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
	want := map[string]string{"company": "Acme", "notes": "hello"}
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

// minimalDefsListBody is a canned definitions collection (DefinitionResource
// list) the values-set command lists once to learn each attribute's field type
// (MIO-2553). Each resource carries the JSON:API attributes `slug` and `type`
// (the AttributeType enum: text/number/boolean/date/multiple/single).
const minimalDefsListBody = `{"data":[` +
	`{"id":"def_age","type":"contact_attribute_definitions","attributes":{"slug":"age","type":"number"}},` +
	`{"id":"def_active","type":"contact_attribute_definitions","attributes":{"slug":"active","type":"boolean"}},` +
	`{"id":"def_born","type":"contact_attribute_definitions","attributes":{"slug":"born","type":"date"}},` +
	`{"id":"def_company","type":"contact_attribute_definitions","attributes":{"slug":"company","type":"text"}},` +
	`{"id":"def_notes","type":"contact_attribute_definitions","attributes":{"slug":"notes","type":"text"}},` +
	`{"id":"def_tier","type":"contact_attribute_definitions","attributes":{"slug":"tier","type":"multiple"}}` +
	`]}`

// caValuesSetServer starts a server that answers the two requests a typed
// `values set` makes: GET .../contact-attributes (the def-type lookup) and
// PATCH .../attributes (the write). It records the PATCH body and whether a
// PATCH fired so tests can assert both the typed wire body and the
// no-write-on-usage-error contract.
func caValuesSetServer(t *testing.T) (srv *httptest.Server, patchBody *[]byte, patchFired *bool) {
	t.Helper()
	var body []byte
	var fired bool
	patchBody, patchFired = &body, &fired
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contact-attributes"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(minimalDefsListBody))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/attributes"):
			body, _ = io.ReadAll(r.Body)
			fired = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(minimalContactValueListBody))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, patchBody, patchFired
}

// TestContactAttributesValuesSet_MapsTypedFields (MIO-2553) verifies `values set`
// looks up each attribute's field type (one def-list GET) and maps the value to
// the correct typed field — value_number/value_boolean/value_date/value_text —
// instead of always sending value_text (which 422s TypeCompatibilityError for
// non-text attributes). number/boolean are parsed to their Go types; date stays
// an ISO string; text is preserved.
func TestContactAttributesValuesSet_MapsTypedFields(t *testing.T) {
	srv, patchBody, patchFired := caValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "age=42",
			"--attr", "active=true",
			"--attr", "born=1990-05-01",
			"--attr", "company=Acme",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}

	var doc struct {
		Data []struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*patchBody, &doc); err != nil {
		t.Fatalf("PATCH /data is not a valid JSON list: %v; body=%q", err, *patchBody)
	}
	if len(doc.Data) != 4 {
		t.Fatalf("data should be a 4-item list (one per --attr); got %#v", doc.Data)
	}

	bySlug := make(map[string]map[string]any, len(doc.Data))
	for i, op := range doc.Data {
		if op.Type != "set" {
			t.Errorf("data[%d].type = %q, want \"set\"", i, op.Type)
		}
		slug, _ := op.Attributes["definition_slug"].(string)
		bySlug[slug] = op.Attributes
	}

	// number → value_number (JSON number), NOT value_text.
	if got := bySlug["age"]["value_number"]; got != float64(42) {
		t.Errorf("age value_number = %#v, want 42", got)
	}
	if _, ok := bySlug["age"]["value_text"]; ok {
		t.Errorf("age must not carry value_text: %#v", bySlug["age"])
	}
	// boolean → value_boolean.
	if got := bySlug["active"]["value_boolean"]; got != true {
		t.Errorf("active value_boolean = %#v, want true", got)
	}
	if _, ok := bySlug["active"]["value_text"]; ok {
		t.Errorf("active must not carry value_text: %#v", bySlug["active"])
	}
	// date → value_date (kept as ISO string).
	if got := bySlug["born"]["value_date"]; got != "1990-05-01" {
		t.Errorf("born value_date = %#v, want \"1990-05-01\"", got)
	}
	if _, ok := bySlug["born"]["value_text"]; ok {
		t.Errorf("born must not carry value_text: %#v", bySlug["born"])
	}
	// text → value_text (preserved behavior).
	if got := bySlug["company"]["value_text"]; got != "Acme" {
		t.Errorf("company value_text = %#v, want \"Acme\"", got)
	}
}

// TestContactAttributesValuesSet_UnknownSlugExitsUsageNoWrite (MIO-2553) verifies a
// slug the team has no definition for exits ExitUsage and fires NO PATCH: the CLI
// cannot know the value's typed field, so a value_text guess (which the backend
// 422s for a non-text attribute, and which would never match a non-existent
// definition) is not sent — the user gets a clear "unknown attribute slug" error
// with no round-trip.
func TestContactAttributesValuesSet_UnknownSlugExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired := caValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "mystery=hello",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire for a slug with no definition")
	}
}

// TestContactAttributesValuesSet_InvalidNumberExitsUsageNoWrite (MIO-2553) verifies
// a non-numeric value for a number attribute exits ExitUsage and fires NO PATCH
// (the value is never sent). The def-list GET is expected; only the write must be
// suppressed on the type-parse usage error.
func TestContactAttributesValuesSet_InvalidNumberExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired := caValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "age=not-a-number",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire on a type-parse usage error")
	}
}

// TestContactAttributesValuesSet_InvalidDateExitsUsageNoWrite (MIO-2553) verifies a
// value that is not a valid ISO-8601 date for a date attribute exits ExitUsage and
// fires NO PATCH, rather than shipping the garbage string to the backend as
// value_date.
func TestContactAttributesValuesSet_InvalidDateExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired := caValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "born=not-a-date",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire for an invalid date value")
	}
}

// TestContactAttributesValuesSet_NonFiniteNumberExitsUsageNoWrite (MIO-2553)
// verifies NaN/±Inf — which strconv.ParseFloat accepts but JSON cannot encode —
// exit ExitUsage before the write instead of failing later as a generic marshal
// error after the request is built.
func TestContactAttributesValuesSet_NonFiniteNumberExitsUsageNoWrite(t *testing.T) {
	for _, val := range []string{"NaN", "Inf", "+Inf", "-Inf"} {
		t.Run(val, func(t *testing.T) {
			srv, _, patchFired := caValuesSetServer(t)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1",
					"contact-attributes", "values", "set", "tcid_abc123",
					"--attr", "age="+val,
				)...)

			if res.Code != errs.ExitUsage {
				t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *patchFired {
				t.Errorf("PATCH must NOT fire for a non-finite number")
			}
		})
	}
}

// TestContactAttributesValuesSet_MultiSelectExitsUsageNoWrite (MIO-2553) verifies
// a multi-select (multiple/single) attribute exits ExitUsage with a clear message
// and fires NO PATCH — option-value setting via bare key=value is a documented
// follow-up, and sending value_text would 422 server-side anyway.
func TestContactAttributesValuesSet_MultiSelectExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired := caValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "tier=enterprise",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire for an unsupported multi-select attribute")
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
