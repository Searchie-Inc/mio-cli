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

// TestContactAttributesValuesSet_MultiSelectResolvesOptionValue (MIO-4124)
// supersedes the old "not yet supported" expectation (MIO-2553) for a
// multi-select attribute: `--attr tier=enterprise` now resolves the value
// against the definition's options and writes option_ids, instead of exiting
// ExitUsage.
func TestContactAttributesValuesSet_MultiSelectResolvesOptionValue(t *testing.T) {
	srv, patchBody, patchFired, _ := caSelectValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "tier=enterprise",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}
	ops := decodeCAOps(t, *patchBody)
	if len(ops) != 1 {
		t.Fatalf("data should be a 1-item list; got %#v", ops)
	}
	ids, _ := ops[0].Attributes["option_ids"].([]any)
	if len(ids) != 1 || ids[0] != "opt_enterprise" {
		t.Errorf("option_ids = %#v, want [\"opt_enterprise\"]", ops[0].Attributes["option_ids"])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-4124 — values set: option values for single/multiple-select attributes
// ═══════════════════════════════════════════════════════════════════════════════
//
// caDefsWithSelectsBody extends the MIO-2553 definitions fixture with two
// select-type attributes: "plan" (single) and "tier" (multiple, already
// present pre-MIO-4124). "status" is a second single-type def whose two
// options share a label case-insensitively, for the ambiguous-label test.
const caDefsWithSelectsBody = `{"data":[` +
	`{"id":"def_age","type":"contact_attribute_definitions","attributes":{"slug":"age","type":"number"}},` +
	`{"id":"def_company","type":"contact_attribute_definitions","attributes":{"slug":"company","type":"text"}},` +
	`{"id":"def_plan","type":"contact_attribute_definitions","attributes":{"slug":"plan","type":"single"}},` +
	`{"id":"def_tier","type":"contact_attribute_definitions","attributes":{"slug":"tier","type":"multiple"}},` +
	`{"id":"def_status","type":"contact_attribute_definitions","attributes":{"slug":"status","type":"single"}}` +
	`]}`

const caPlanOptionsBody = `{"data":[` +
	`{"id":"opt_gold","type":"contact_attribute_options","attributes":{"definition_id":"def_plan","slug":"gold","label":"Gold","position":1}},` +
	`{"id":"opt_silver","type":"contact_attribute_options","attributes":{"definition_id":"def_plan","slug":"silver","label":"Silver","position":2}}` +
	`]}`

const caTierOptionsBody = `{"data":[` +
	`{"id":"opt_basic","type":"contact_attribute_options","attributes":{"definition_id":"def_tier","slug":"basic","label":"Basic","position":1}},` +
	`{"id":"opt_pro","type":"contact_attribute_options","attributes":{"definition_id":"def_tier","slug":"pro","label":"Pro","position":2}},` +
	`{"id":"opt_enterprise","type":"contact_attribute_options","attributes":{"definition_id":"def_tier","slug":"enterprise","label":"Enterprise","position":3}}` +
	`]}`

// caStatusOptionsBody's opt_active1/opt_active2 share a label case-insensitively
// ("Active"/"active") — the ambiguous-label test. opt_dup_id's SLUG
// ("opt_active1") deliberately collides with opt_active1's ID, for the
// id-over-slug precedence test: a raw value of "opt_active1" must resolve via
// the id match (to opt_active1 itself), not via a slug match against
// opt_dup_id.
const caStatusOptionsBody = `{"data":[` +
	`{"id":"opt_active1","type":"contact_attribute_options","attributes":{"definition_id":"def_status","slug":"active","label":"Active","position":1}},` +
	`{"id":"opt_active2","type":"contact_attribute_options","attributes":{"definition_id":"def_status","slug":"live","label":"active","position":2}},` +
	`{"id":"opt_dup_id","type":"contact_attribute_options","attributes":{"definition_id":"def_status","slug":"opt_active1","label":"Duplicate ID Slug","position":3}}` +
	`]}`

// caOp is a decoded {type, attributes} entry of a values-set write body's
// data[] list.
type caOp struct {
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

// decodeCAOps decodes a values-set PATCH body's {data:[...]} list.
func decodeCAOps(t *testing.T, body []byte) []caOp {
	t.Helper()
	var doc struct {
		Data []caOp `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body /data is not a valid JSON list: %v; body=%q", err, body)
	}
	return doc.Data
}

// caSelectValuesSetServer starts a spy server serving caDefsWithSelectsBody
// (the def-type lookup) plus each select definition's options list, and
// captures the PATCH write. optionsGETCount counts GET .../options calls PER
// definition id, so a test can assert a definition's options are fetched
// exactly once no matter how many --attr occurrences (or comma-separated
// tokens) reference it (MIO-4124).
func caSelectValuesSetServer(t *testing.T) (srv *httptest.Server, patchBody *[]byte, patchFired *bool, optionsGETCount *map[string]int) {
	t.Helper()
	var body []byte
	var fired bool
	counts := map[string]int{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contact-attributes"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(caDefsWithSelectsBody))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/def_plan/options"):
			counts["def_plan"]++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(caPlanOptionsBody))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/def_tier/options"):
			counts["def_tier"]++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(caTierOptionsBody))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/def_status/options"):
			counts["def_status"]++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(caStatusOptionsBody))
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
	return srv, &body, &fired, &counts
}

// TestContactAttributesValuesSet_SingleSelectByLabel (MIO-4124) verifies a
// single-select attribute value can be set by LABEL: `--attr plan=Gold`
// resolves through exactly one options GET, and the write body carries
// option_ids (never option_slugs or a value_* key).
func TestContactAttributesValuesSet_SingleSelectByLabel(t *testing.T) {
	srv, patchBody, patchFired, counts := caSelectValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "plan=Gold",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}
	if got := (*counts)["def_plan"]; got != 1 {
		t.Errorf("options GET count for def_plan = %d, want 1", got)
	}

	ops := decodeCAOps(t, *patchBody)
	if len(ops) != 1 {
		t.Fatalf("data should be a 1-item list; got %#v", ops)
	}
	op := ops[0]
	if op.Type != "set" {
		t.Errorf("data[0].type = %q, want \"set\"", op.Type)
	}
	if slug, _ := op.Attributes["definition_slug"].(string); slug != "plan" {
		t.Errorf("definition_slug = %q, want \"plan\"", slug)
	}
	ids, ok := op.Attributes["option_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "opt_gold" {
		t.Errorf("option_ids = %#v, want [\"opt_gold\"]", op.Attributes["option_ids"])
	}
	if _, ok := op.Attributes["option_slugs"]; ok {
		t.Errorf("must NOT carry option_slugs: %#v", op.Attributes)
	}
	for _, k := range []string{"value_text", "value_number", "value_boolean", "value_date"} {
		if _, ok := op.Attributes[k]; ok {
			t.Errorf("must NOT carry %s: %#v", k, op.Attributes)
		}
	}
}

// TestContactAttributesValuesSet_SingleSelectBySlugAndByID (MIO-4124) verifies
// a single-select value resolves to the SAME option id whether the raw --attr
// value names the option's slug or its id directly.
func TestContactAttributesValuesSet_SingleSelectBySlugAndByID(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"by slug", "gold"},
		{"by id", "opt_gold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, patchBody, patchFired, _ := caSelectValuesSetServer(t)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1",
					"contact-attributes", "values", "set", "tcid_abc123",
					"--attr", "plan="+tc.raw,
				)...)

			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
			}
			if !*patchFired {
				t.Fatalf("no PATCH request fired")
			}
			ops := decodeCAOps(t, *patchBody)
			if len(ops) != 1 {
				t.Fatalf("data should be a 1-item list; got %#v", ops)
			}
			ids, _ := ops[0].Attributes["option_ids"].([]any)
			if len(ids) != 1 || ids[0] != "opt_gold" {
				t.Errorf("option_ids = %#v, want [\"opt_gold\"]", ops[0].Attributes["option_ids"])
			}
		})
	}
}

// TestContactAttributesValuesSet_MultipleAccumulatesAndDedupes (MIO-4124,
// extended round 2 for resolved-id dedup) verifies a multiple-select
// attribute accumulates values across BOTH repeated --attr flags and a
// comma-separated value into ONE op, preserves order, drops exact (raw)
// duplicate tokens, ALSO drops duplicates that only tie once RESOLVED — "pro"
// (slug), "Pro" (label) and "opt_pro" (id) are three different raw tokens
// that all name the same option and must collapse to one entry — and fetches
// the definition's options exactly once no matter how many --attr
// occurrences reference it.
func TestContactAttributesValuesSet_MultipleAccumulatesAndDedupes(t *testing.T) {
	srv, patchBody, patchFired, counts := caSelectValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "tier=basic,pro",
			"--attr", "tier=pro", // duplicate RAW token of "pro" above — dropped before resolution
			"--attr", "tier=Pro", // different raw token, but resolves (by label) to the SAME option — dropped after resolution
			"--attr", "tier=opt_pro", // different raw token again, resolves (by id) to the SAME option — dropped after resolution
			"--attr", "tier=enterprise",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}
	if got := (*counts)["def_tier"]; got != 1 {
		t.Errorf("options GET count for def_tier = %d, want 1", got)
	}

	ops := decodeCAOps(t, *patchBody)
	if len(ops) != 1 {
		t.Fatalf("data should be a 1-item list (one op for the whole 'tier' attribute); got %#v", ops)
	}
	ids, ok := ops[0].Attributes["option_ids"].([]any)
	if !ok {
		t.Fatalf("option_ids missing or wrong type: %#v", ops[0].Attributes)
	}
	want := []any{"opt_basic", "opt_pro", "opt_enterprise"}
	if len(ids) != len(want) {
		t.Fatalf("option_ids = %#v, want %#v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("option_ids[%d] = %#v, want %#v", i, ids[i], want[i])
		}
	}
}

// TestContactAttributesValuesSet_MixedScalarAndSelect (MIO-4124) verifies a
// call mixing one scalar attribute and one select attribute produces two ops
// — the scalar op is unchanged from pre-MIO-4124 behaviour (value_text,
// carries no option_ids) — and the options list is fetched ONLY for the
// select definition actually referenced.
func TestContactAttributesValuesSet_MixedScalarAndSelect(t *testing.T) {
	srv, patchBody, patchFired, counts := caSelectValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "company=Acme",
			"--attr", "plan=Gold",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}
	if got := (*counts)["def_plan"]; got != 1 {
		t.Errorf("options GET count for def_plan = %d, want 1", got)
	}
	if got := (*counts)["def_tier"]; got != 0 {
		t.Errorf("options GET count for def_tier = %d, want 0 (not referenced)", got)
	}

	ops := decodeCAOps(t, *patchBody)
	if len(ops) != 2 {
		t.Fatalf("data should be a 2-item list; got %#v", ops)
	}
	bySlug := make(map[string]map[string]any, len(ops))
	for _, op := range ops {
		slug, _ := op.Attributes["definition_slug"].(string)
		bySlug[slug] = op.Attributes
	}
	if got := bySlug["company"]["value_text"]; got != "Acme" {
		t.Errorf("company value_text = %#v, want \"Acme\"", got)
	}
	if _, ok := bySlug["company"]["option_ids"]; ok {
		t.Errorf("company (scalar) must NOT carry option_ids: %#v", bySlug["company"])
	}
	ids, _ := bySlug["plan"]["option_ids"].([]any)
	if len(ids) != 1 || ids[0] != "opt_gold" {
		t.Errorf("plan option_ids = %#v, want [\"opt_gold\"]", bySlug["plan"]["option_ids"])
	}
}

// TestContactAttributesValuesSet_UnknownOptionValueExitsUsageNoWrite (MIO-4124)
// verifies a value that matches none of a select definition's options exits
// ExitUsage, names the definition, lists its options as "slug (label)", and
// fires NO write request. Uses executeCLI (not runContract) to assert on the
// error MESSAGE: the SilenceErrors root (cmd/root.go) never writes RunE's
// returned error into the captured stderr buffer in-process — only main.go's
// post-Execute rendering does that, so res.Stderr is always empty here (see
// pages_catalog_test.go's executeCLI doc comment).
func TestContactAttributesValuesSet_UnknownOptionValueExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired, _ := caSelectValuesSetServer(t)

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "plan=Bronze",
		)...)

	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire for an unmatched option value")
	}
	if err == nil || !strings.Contains(err.Error(), "plan") {
		t.Errorf("error must name the definition %q: %v", "plan", err)
	}
	if err == nil || !strings.Contains(err.Error(), "gold (Gold)") || !strings.Contains(err.Error(), "silver (Silver)") {
		t.Errorf("error must list the definition's options as \"slug (label)\": %v", err)
	}
}

// TestContactAttributesValuesSet_TwoValuesForSingleExitsUsageNoWrite (MIO-4124,
// extended round 2) verifies passing two DISTINCT option values for a
// single-select attribute exits ExitUsage (mirroring the backend's "accepts
// exactly one option, not multiple" rule), fires NO write request, and — the
// point that actually proves the check runs BEFORE resolution, not just
// before the write — fires NO options GET for the definition either. Uses
// executeCLI to see the error message (see the comment on
// TestContactAttributesValuesSet_UnknownOptionValueExitsUsageNoWrite).
func TestContactAttributesValuesSet_TwoValuesForSingleExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired, counts := caSelectValuesSetServer(t)

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "plan=Gold", "--attr", "plan=Silver",
		)...)

	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire for two values on a single-select attribute")
	}
	if got := (*counts)["def_plan"]; got != 0 {
		t.Errorf("options GET count for def_plan = %d, want 0 (the single-select cardinality check must run before fetching options)", got)
	}
	if err == nil || !strings.Contains(err.Error(), "plan") {
		t.Errorf("error must name the definition %q: %v", "plan", err)
	}
}

// TestContactAttributesValuesSet_AmbiguousLabelExitsUsageNoWrite (MIO-4124)
// verifies a value that case-insensitively matches TWO options' labels (and
// matches neither by id nor by slug) exits ExitUsage as ambiguous and fires
// NO write request. Uses executeCLI to see the error message (see the comment
// on TestContactAttributesValuesSet_UnknownOptionValueExitsUsageNoWrite).
func TestContactAttributesValuesSet_AmbiguousLabelExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired, _ := caSelectValuesSetServer(t)

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "status=Active",
		)...)

	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire for an ambiguous option value")
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Errorf("error must say the value is ambiguous: %v", err)
	}
}

// TestContactAttributesValuesSet_SlugMatchWinsOverAmbiguousLabel (MIO-4124
// round 2) verifies the resolution order actually discriminates slug from
// label, not just in theory: "status=active" (lowercase) exact-matches
// opt_active1's SLUG ("active") and must resolve there directly — even though
// opt_active2's label ("active", case-insensitive) would otherwise tie with
// opt_active1's label ("Active") and make the value ambiguous, exactly like
// TestContactAttributesValuesSet_AmbiguousLabelExitsUsageNoWrite above. That
// test alone cannot tell a resolver that checks slug-before-label apart from
// one that checks label-before-slug, because "status=Active" never
// exact-matches any slug at all; this one gives the resolver a value that
// DOES, so a label-first implementation would (wrongly) hit the ambiguous
// branch instead of resolving.
func TestContactAttributesValuesSet_SlugMatchWinsOverAmbiguousLabel(t *testing.T) {
	srv, patchBody, patchFired, _ := caSelectValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "status=active",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}
	ops := decodeCAOps(t, *patchBody)
	if len(ops) != 1 {
		t.Fatalf("data should be a 1-item list; got %#v", ops)
	}
	ids, _ := ops[0].Attributes["option_ids"].([]any)
	if len(ids) != 1 || ids[0] != "opt_active1" {
		t.Errorf("option_ids = %#v, want [\"opt_active1\"] (an exact slug match must win over an otherwise-ambiguous label match)", ops[0].Attributes["option_ids"])
	}
}

// TestContactAttributesValuesSet_IDMatchWinsOverSlugMatch (MIO-4124 round 2)
// verifies an id match takes precedence over a slug match: caStatusOptionsBody
// carries opt_dup_id, whose SLUG ("opt_active1") deliberately collides with
// opt_active1's ID. A raw value of "opt_active1" must resolve to opt_active1
// itself (the id match, checked first) — not to opt_dup_id (which would only
// match on slug).
func TestContactAttributesValuesSet_IDMatchWinsOverSlugMatch(t *testing.T) {
	srv, patchBody, patchFired, _ := caSelectValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "status=opt_active1",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}
	ops := decodeCAOps(t, *patchBody)
	if len(ops) != 1 {
		t.Fatalf("data should be a 1-item list; got %#v", ops)
	}
	ids, _ := ops[0].Attributes["option_ids"].([]any)
	if len(ids) != 1 || ids[0] != "opt_active1" {
		t.Errorf("option_ids = %#v, want [\"opt_active1\"] (an exact id match must win over a different option's colliding slug)", ops[0].Attributes["option_ids"])
	}
}

// TestContactAttributesValuesSet_EmptyOptionValueExitsUsageNoWrite (MIO-4124)
// verifies an empty option value (a trailing comma in a comma-separated list)
// exits ExitUsage BEFORE any write — and before any options GET, since the
// check runs while splitting --attr values, ahead of option resolution — with
// a message that specifically calls out the empty value. Asserting only the
// exit code here would not discriminate this from the (also ExitUsage)
// "unknown option" fallback an empty string would otherwise hit during
// resolution (verifying-guards.md: a reject-side case must be able to tell
// the two implementations apart) — the message and options-GET-count checks
// are what make this guard mutation-provable.
func TestContactAttributesValuesSet_EmptyOptionValueExitsUsageNoWrite(t *testing.T) {
	srv, _, patchFired, counts := caSelectValuesSetServer(t)

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "tier=basic,",
		)...)

	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if *patchFired {
		t.Errorf("PATCH must NOT fire for an empty option value")
	}
	if got := (*counts)["def_tier"]; got != 0 {
		t.Errorf("options GET count for def_tier = %d, want 0 (the empty-value check must run before option resolution)", got)
	}
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("error must specifically call out the empty value: %v", err)
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

// TestContactAttributesCreate_RejectsInvalidFieldType (MIO-2543, extended
// MIO-4124) verifies the client-side --field-type enum validation added with
// the pure-builder extraction: an out-of-enum field type exits ExitUsage and
// fires NO request, instead of round-tripping to a backend 422 — and that the
// error message names "single" in the accepted-type list (MIO-4124 added
// "single" to attrDefFieldTypes alongside the pre-existing "multiple"). (The
// valid-field-type body guards live in jake_qa_drift_test.go and are
// unchanged.) Uses executeCLI (not runContract) to see the error message: the
// SilenceErrors root never writes RunE's returned error into the captured
// stderr buffer in-process (see pages_catalog_test.go's executeCLI doc
// comment).
func TestContactAttributesCreate_RejectsInvalidFieldType(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalHubConfigBody)

	err := executeCLI(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Company", "--slug", "company", "--field-type", "select",
		)...)

	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage for invalid --field-type); err=%v",
			codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if *fired {
		t.Error("an invalid --field-type must exit before any HTTP request")
	}
	if err == nil || !strings.Contains(err.Error(), "single") {
		t.Errorf("error must name \"single\" in the accepted --field-type list: %v", err)
	}
}

// TestContactAttributesCreate_FieldTypeSingle (MIO-4124) verifies
// `contact-attributes create --field-type=single` is accepted client-side
// (attrDefFieldTypes gained "single" alongside the pre-existing scalar types
// and "multiple") and the wire body carries attributes.type = "single",
// mirroring the pattern TestWritePath_ContactAttributesCreate_ExactBody
// (jake_qa_drift_test.go) uses for the other field types.
func TestContactAttributesCreate_FieldTypeSingle(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContactAttrBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contact-attributes", "create",
			"--name", "Plan",
			"--slug", "plan",
			"--field-type", "single",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, *gotBody)
	}
	if got := doc.Data.Attributes["type"]; got != "single" {
		t.Errorf(`attributes["type"] = %v, want "single"`, got)
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
