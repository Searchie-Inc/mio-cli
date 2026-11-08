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

const minimalContactValueListBody = `{"data":[{"id":"tcid_abc123:def_1","type":"contact_attribute_values","attributes":{"definition_id":"def_1","definition_slug":"company","value_text":"Acme"}}]}`

const minimalHubConfigBody = `{"data":{"id":"cfg_1","type":"contact_attribute_hub_configs","attributes":{"definition_id":"attr_abc123","position":1}}}`

func TestContactAttributesValuesSet_SendsDataList(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contact-attributes"):
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

const minimalDefsListBody = `{"data":[` +
	`{"id":"def_age","type":"contact_attribute_definitions","attributes":{"slug":"age","type":"number"}},` +
	`{"id":"def_active","type":"contact_attribute_definitions","attributes":{"slug":"active","type":"boolean"}},` +
	`{"id":"def_born","type":"contact_attribute_definitions","attributes":{"slug":"born","type":"date"}},` +
	`{"id":"def_company","type":"contact_attribute_definitions","attributes":{"slug":"company","type":"text"}},` +
	`{"id":"def_notes","type":"contact_attribute_definitions","attributes":{"slug":"notes","type":"text"}},` +
	`{"id":"def_tier","type":"contact_attribute_definitions","attributes":{"slug":"tier","type":"multiple"}}` +
	`]}`

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

	if got := bySlug["age"]["value_number"]; got != float64(42) {
		t.Errorf("age value_number = %#v, want 42", got)
	}
	if _, ok := bySlug["age"]["value_text"]; ok {
		t.Errorf("age must not carry value_text: %#v", bySlug["age"])
	}
	if got := bySlug["active"]["value_boolean"]; got != true {
		t.Errorf("active value_boolean = %#v, want true", got)
	}
	if _, ok := bySlug["active"]["value_text"]; ok {
		t.Errorf("active must not carry value_text: %#v", bySlug["active"])
	}
	if got := bySlug["born"]["value_date"]; got != "1990-05-01" {
		t.Errorf("born value_date = %#v, want \"1990-05-01\"", got)
	}
	if _, ok := bySlug["born"]["value_text"]; ok {
		t.Errorf("born must not carry value_text: %#v", bySlug["born"])
	}
	if got := bySlug["company"]["value_text"]; got != "Acme" {
		t.Errorf("company value_text = %#v, want \"Acme\"", got)
	}
}

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

const caStatusOptionsBody = `{"data":[` +
	`{"id":"opt_active1","type":"contact_attribute_options","attributes":{"definition_id":"def_status","slug":"active","label":"Active","position":1}},` +
	`{"id":"opt_active2","type":"contact_attribute_options","attributes":{"definition_id":"def_status","slug":"live","label":"active","position":2}},` +
	`{"id":"opt_dup_id","type":"contact_attribute_options","attributes":{"definition_id":"def_status","slug":"opt_active1","label":"Duplicate ID Slug","position":3}}` +
	`]}`

type caOp struct {
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

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

func TestContactAttributesValuesSet_MultipleAccumulatesAndDedupes(t *testing.T) {
	srv, patchBody, patchFired, counts := caSelectValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "tier=basic,pro",
			"--attr", "tier=pro",
			"--attr", "tier=Pro",
			"--attr", "tier=opt_pro",
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

const caDefsPage1Body = `{"data":[` +
	`{"id":"def_scalar1","type":"contact_attribute_definitions","attributes":{"slug":"scalar1","type":"text"}}` +
	`],"meta":{"has_more":true},"links":{"next":"/api/teams/t_team1/contact-attributes?page%5Bafter%5D=def_last1&page%5Bsize%5D=100"}}`

const caDefsPage2Body = `{"data":[` +
	`{"id":"def_tier2","type":"contact_attribute_definitions","attributes":{"slug":"tier2","type":"multiple"}},` +
	`{"id":"def_scalar2","type":"contact_attribute_definitions","attributes":{"slug":"scalar2","type":"text"}}` +
	`],"meta":{"has_more":false}}`

const caTier2OptionsBody = `{"data":[` +
	`{"id":"opt_gold2","type":"contact_attribute_options","attributes":{"definition_id":"def_tier2","slug":"gold","label":"Gold","position":1}}` +
	`]}`

func caPagedDefsValuesSetServer(t *testing.T) (srv *httptest.Server, patchBody *[]byte, patchFired *bool, defsGETCount *int) {
	t.Helper()
	var body []byte
	var fired bool
	defsCount := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contact-attributes"):
			defsCount++
			w.WriteHeader(http.StatusOK)
			if r.URL.Query().Get("page[after]") == "def_last1" {
				_, _ = w.Write([]byte(caDefsPage2Body))
			} else {
				_, _ = w.Write([]byte(caDefsPage1Body))
			}
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/def_tier2/options"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(caTier2OptionsBody))
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
	return srv, &body, &fired, &defsCount
}

func TestContactAttributesValuesSet_DefinitionsListFollowsRealPagination(t *testing.T) {
	srv, patchBody, patchFired, defsGETCount := caPagedDefsValuesSetServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"contact-attributes", "values", "set", "tcid_abc123",
			"--attr", "tier2=gold",
			"--attr", "scalar2=hello",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !*patchFired {
		t.Fatalf("no PATCH request fired")
	}
	if got := *defsGETCount; got != 2 {
		t.Errorf("definitions-list GET count = %d, want 2 (must follow links.next to page two)", got)
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
	ids, _ := bySlug["tier2"]["option_ids"].([]any)
	if len(ids) != 1 || ids[0] != "opt_gold2" {
		t.Errorf("tier2 option_ids = %#v, want [\"opt_gold2\"]", bySlug["tier2"]["option_ids"])
	}
	if got := bySlug["scalar2"]["value_text"]; got != "hello" {
		t.Errorf("scalar2 value_text = %#v, want \"hello\"", got)
	}
}

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
	if !strings.Contains(res.Stdout, "def_1") {
		t.Errorf("stdout should render the value array; got %q", res.Stdout)
	}
}

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
