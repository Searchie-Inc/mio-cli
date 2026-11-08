package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func decodeExact(t *testing.T, label, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: not valid JSON: %v\n%s", label, err, s)
	}
	return v
}

func apiErrorObjects(t *testing.T, label string, doc any) []map[string]any {
	t.Helper()
	top, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("%s: document is %T, want an object", label, doc)
	}
	arr, ok := top["errors"].([]any)
	if !ok {
		t.Fatalf("%s: errors is %T, want an array", label, top["errors"])
	}
	out := make([]map[string]any, 0, len(arr))
	for i, el := range arr {
		obj, ok := el.(map[string]any)
		if !ok {
			t.Fatalf("%s: errors[%d] is %T, want an object", label, i, el)
		}
		out = append(out, obj)
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func compactJSON(t *testing.T, label, s string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(strings.TrimSpace(s))); err != nil {
		t.Fatalf("%s: not valid JSON: %v\n%s", label, err, s)
	}
	return buf.String()
}

func stripCLIAdditions(t *testing.T, emitted, api any) any {
	t.Helper()
	b, _ := json.Marshal(emitted)
	cp := decodeExact(t, "copy", string(b))
	apiErrs := apiErrorObjects(t, "api", api)
	for i, obj := range apiErrorObjects(t, "emitted", cp) {
		if i >= len(apiErrs) {
			break
		}
		if meta, ok := obj["meta"].(map[string]any); ok {
			delete(meta, "exit_code")
			if _, apiHad := apiErrs[i]["meta"]; !apiHad && len(meta) == 0 {
				delete(obj, "meta")
			}
		}
		if _, apiHad := apiErrs[i]["status"]; !apiHad {
			delete(obj, "status")
		}
	}
	return cp
}

var envelopeFamilyCases = []struct {
	name       string
	httpStatus int
	body       string
	wantExit   int
	wantDetail string
}{
	{
		name:       "400",
		httpStatus: 400,
		body:       `{"errors":[{"id":"err-400","status":"400","code":"invalid_filter","title":"Bad Request","detail":"Unknown filter field.","source":{"parameter":"filter[colour]"},"meta":{"request_id":"rid-400"},"links":{"about":"https://example.test/errors/invalid_filter"},"x_future_member":{"k":[1,2]}}]}`,
		wantExit:   errs.ExitUsage,
		wantDetail: "Unknown filter field. (parameter: filter[colour])",
	},
	{
		name:       "403",
		httpStatus: 403,
		body:       `{"errors":[{"status":"403","code":"api_key_principal_forbidden","title":"Forbidden","detail":"API key principals cannot manage API keys.","meta":{"request_id":"rid-403"},"x_future_member":true}]}`,
		wantExit:   errs.ExitAuth,
		wantDetail: "API key principals cannot manage API keys.",
	},
	{
		name:       "404",
		httpStatus: 404,
		body:       `{"errors":[{"status":"404","code":"contact_not_found","title":"Not Found","detail":"Contact not found.","meta":{"request_id":"rid-404","resource":"contacts"}}]}`,
		wantExit:   errs.ExitNotFound,
		wantDetail: "Contact not found.",
	},
	{
		name:       "409",
		httpStatus: 409,
		body:       `{"errors":[{"status":"409","code":"email_taken","title":"Conflict","detail":"Email 'a@b.c' is already registered.","source":{"pointer":"/data/attributes/email"},"meta":{"request_id":"rid-409"}}]}`,
		wantExit:   errs.ExitUsage,
		wantDetail: "Email 'a@b.c' is already registered. (/data/attributes/email)",
	},
	{
		name:       "422 multi-error",
		httpStatus: 422,
		body:       `{"errors":[{"status":"422","code":"invalid_price_config","title":"Invalid price config","detail":"interval_count=4 exceeds the maximum for interval='year' (3).","source":{"pointer":"/data/attributes/interval_count"},"meta":{"request_id":"rid-422","max":3}},{"status":"422","code":"graph_validation_error","title":"Unprocessable Entity","detail":"Unknown node kind.","source":{"pointer":"/data/attributes/definition/nodes/0"},"meta":{"request_id":"rid-422","big":12345678901234567890}}]}`,
		wantExit:   errs.ExitUsage,
		wantDetail: "interval_count=4 exceeds the maximum for interval='year' (3). (/data/attributes/interval_count); Unknown node kind. (/data/attributes/definition/nodes/0)",
	},
	{
		name:       "multi-error, a later object's own status differs",
		httpStatus: 422,
		body:       `{"errors":[{"status":"422","code":"invalid_price_config","title":"Invalid price config","detail":"interval_count=4 exceeds the maximum.","source":{"pointer":"/data/attributes/interval_count"},"meta":{"request_id":"rid-422a"}},{"status":"409","code":"price_conflict","title":"Conflict","detail":"A price with this lookup key exists.","meta":{"request_id":"rid-422a"}}]}`,
		wantExit:   errs.ExitUsage,
		wantDetail: "interval_count=4 exceeds the maximum. (/data/attributes/interval_count); A price with this lookup key exists.",
	},
	{
		name:       "multi-error, a later object without status; top-level members",
		httpStatus: 422,
		body:       `{"jsonapi":{"version":"1.1"},"errors":[{"status":"422","code":"invalid_price_config","title":"Invalid price config","detail":"interval_count=4 exceeds the maximum.","meta":{"request_id":"rid-422b"}},{"code":"graph_validation_error","title":"Unprocessable Entity","detail":"Unknown node kind.","source":{"pointer":"/data/attributes/definition/nodes/0"},"meta":{"request_id":"rid-422b"}}],"meta":{"request_id":"top-level"}}`,
		wantExit:   errs.ExitUsage,
		wantDetail: "interval_count=4 exceeds the maximum.; Unknown node kind. (/data/attributes/definition/nodes/0)",
	},
	{
		name:       "transport status vs a stale body status",
		httpStatus: 403,
		body:       `{"errors":[{"status":"200","code":"stale_status","title":"Forbidden","detail":"forbidden","meta":{"request_id":"rid-stale"}}]}`,
		wantExit:   errs.ExitAuth,
		wantDetail: "forbidden",
	},
	{
		name:       "503",
		httpStatus: 503,
		body:       `{"errors":[{"status":"503","code":"maintenance","title":"Service Unavailable","detail":"Down for maintenance.","meta":{"request_id":"rid-503"}}]}`,
		wantExit:   errs.ExitServer,
		wantDetail: "Down for maintenance.",
	},
}

func TestContract_ErrorEnvelope_KeepsEveryAPIMember(t *testing.T) {
	bin := buildBinary(t)

	for _, tc := range envelopeFamilyCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMockServer(t, []mockHandler{{Status: tc.httpStatus, Body: tc.body}})
			_, stderr, exitCode := runBinary(t, bin, []string{
				"MIO_API_KEY=test-key",
				"MIO_API_BASE_URL=" + srv.URL,
			}, "--team", "t_team1", "contacts", "retrieve", "ctt_any")

			if exitCode != tc.wantExit {
				t.Errorf("CONTRACT: HTTP %d → process exit code = %d, want %d (MIO-3912 must not move exit codes); stderr=%q",
					tc.httpStatus, exitCode, tc.wantExit, stderr)
			}

			apiObjs := apiErrorObjects(t, "api body", decodeExact(t, "api body", tc.body))
			emitted := decodeExact(t, "stderr envelope", stderr)
			gotObjs := apiErrorObjects(t, "stderr envelope", emitted)
			if top, _ := emitted.(map[string]any); strings.Join(sortedKeys(top), ",") != "errors" {
				t.Errorf("CONTRACT: default envelope top-level members = %s, want errors only (the API's top-level members are --raw only); stderr=%q",
					strings.Join(sortedKeys(top), ","), stderr)
			}
			if len(gotObjs) != len(apiObjs) {
				t.Fatalf("CONTRACT: envelope has %d error object(s), the API sent %d — one per API error object; stderr=%q",
					len(gotObjs), len(apiObjs), stderr)
			}

			wantExitNum := json.Number(strconv.Itoa(tc.wantExit))
			for i, apiObj := range apiObjs {
				got := gotObjs[i]
				for _, k := range sortedKeys(apiObj) {
					switch {
					case k == "meta":
						apiMeta := apiObj["meta"].(map[string]any)
						gotMeta, ok := got["meta"].(map[string]any)
						if !ok {
							t.Errorf("CONTRACT: errors[%d].meta missing; the API sent %v; stderr=%q", i, apiMeta, stderr)
							continue
						}
						for _, mk := range sortedKeys(apiMeta) {
							if !reflect.DeepEqual(gotMeta[mk], apiMeta[mk]) {
								t.Errorf("CONTRACT: errors[%d].meta.%s = %#v, the API sent %#v (dropped or rewritten); stderr=%q",
									i, mk, gotMeta[mk], apiMeta[mk], stderr)
							}
						}
					case i == 0 && k == "status":
					case i == 0 && k == "detail":
					default:
						if !reflect.DeepEqual(got[k], apiObj[k]) {
							t.Errorf("CONTRACT: errors[%d].%s = %#v, the API sent %#v (dropped or rewritten); stderr=%q",
								i, k, got[k], apiObj[k], stderr)
						}
					}
				}
				if _, sent := apiObj["status"]; i > 0 && !sent && got["status"] != strconv.Itoa(tc.httpStatus) {
					t.Errorf("CONTRACT: errors[%d] arrived without status; it carries %#v, want the transport status %q; stderr=%q",
						i, got["status"], strconv.Itoa(tc.httpStatus), stderr)
				}
				gotMeta, _ := got["meta"].(map[string]any)
				if gotMeta["exit_code"] != wantExitNum {
					t.Errorf("CONTRACT: errors[%d].meta.exit_code = %#v, want %s; stderr=%q", i, gotMeta["exit_code"], wantExitNum, stderr)
				}
			}

			if s := gotObjs[0]["status"]; s != strconv.Itoa(tc.httpStatus) {
				t.Errorf("CONTRACT: errors[0].status = %#v, want the transport status %q (MIO-2656); stderr=%q", s, strconv.Itoa(tc.httpStatus), stderr)
			}
			if d := gotObjs[0]["detail"]; d != tc.wantDetail {
				t.Errorf("CONTRACT: errors[0].detail = %#v, want the pre-MIO-3912 CLI message %q; stderr=%q", d, tc.wantDetail, stderr)
			}
		})
	}
}

func TestContract_ErrorEnvelope_RawIsTheAPIDocument(t *testing.T) {
	bin := buildBinary(t)

	for _, tc := range envelopeFamilyCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMockServer(t, []mockHandler{{Status: tc.httpStatus, Body: tc.body}})
			_, stderr, exitCode := runBinary(t, bin, []string{
				"MIO_API_KEY=test-key",
				"MIO_API_BASE_URL=" + srv.URL,
			}, "--raw", "--team", "t_team1", "contacts", "retrieve", "ctt_any")

			if exitCode != tc.wantExit {
				t.Errorf("CONTRACT: --raw HTTP %d → process exit code = %d, want %d; stderr=%q",
					tc.httpStatus, exitCode, tc.wantExit, stderr)
			}

			api := decodeExact(t, "api body", tc.body)
			emitted := decodeExact(t, "--raw stderr", stderr)
			if got := stripCLIAdditions(t, emitted, api); !reflect.DeepEqual(got, api) {
				gb, _ := json.Marshal(got)
				t.Errorf("CONTRACT: --raw envelope minus the CLI's additions is not the API's document.\n got: %s\nwant: %s", gb, tc.body)
			}
			wantExitNum := json.Number(strconv.Itoa(tc.wantExit))
			for i, obj := range apiErrorObjects(t, "--raw stderr", emitted) {
				meta, _ := obj["meta"].(map[string]any)
				if meta["exit_code"] != wantExitNum {
					t.Errorf("CONTRACT: --raw errors[%d].meta.exit_code = %#v, want %s; stderr=%q", i, meta["exit_code"], wantExitNum, stderr)
				}
			}
		})
	}
}

func TestContract_ErrorEnvelope_RawIsByteFaithful(t *testing.T) {
	bin := buildBinary(t)

	cases := []struct {
		name       string
		httpStatus int
		body       string
		want       string
		wantExit   int
	}{
		{
			name:       "additions merge into the API's own meta; top-level members survive",
			httpStatus: 422,
			body:       `{"jsonapi":{"version":"1.1"},"errors":[{"status":"422","code":"invalid_price_config","title":"Invalid price config","detail":"interval_count <b>4</b> & up: caf\u00e9","source":{"pointer":"/data/attributes/interval_count"},"meta":{"request_id":"e7d177d88b394f4db249eb61883cac00","limit":12345678901234567890}}],"meta":{"request_id":"top-level"}}`,
			want:       `{"jsonapi":{"version":"1.1"},"errors":[{"status":"422","code":"invalid_price_config","title":"Invalid price config","detail":"interval_count <b>4</b> & up: caf\u00e9","source":{"pointer":"/data/attributes/interval_count"},"meta":{"request_id":"e7d177d88b394f4db249eb61883cac00","limit":12345678901234567890,"exit_code":2}}],"meta":{"request_id":"top-level"}}`,
			wantExit:   errs.ExitUsage,
		},
		{
			name:       "an error object without status or meta gets both, and nothing else",
			httpStatus: 409,
			body:       `{"errors":[{"code":"slug_taken","detail":"Slug in use."},{"title":"Conflict","status":"409","meta":null}]}`,
			want:       `{"errors":[{"code":"slug_taken","detail":"Slug in use.","status":"409","meta":{"exit_code":2}},{"title":"Conflict","status":"409","meta":{"exit_code":2}}]}`,
			wantExit:   errs.ExitUsage,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMockServer(t, []mockHandler{{Status: tc.httpStatus, Body: tc.body}})
			_, stderr, exitCode := runBinary(t, bin, []string{
				"MIO_API_KEY=test-key",
				"MIO_API_BASE_URL=" + srv.URL,
			}, "--raw", "--team", "t_team1", "contacts", "retrieve", "ctt_any")

			if exitCode != tc.wantExit {
				t.Errorf("CONTRACT: exit code = %d, want %d; stderr=%q", exitCode, tc.wantExit, stderr)
			}
			if got := compactJSON(t, "--raw stderr", stderr); got != tc.want {
				t.Errorf("CONTRACT: --raw envelope is not the API's bytes plus the documented additions.\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestContract_ErrorEnvelope_CLIShapeWhereNoAPIDocument(t *testing.T) {
	bin := buildBinary(t)
	htmlSrv := newMockServer(t, []mockHandler{
		{Status: 502, Body: `<html><body>502 Bad Gateway</body></html>`},
	})

	cases := []struct {
		name       string
		env        []string
		args       []string
		wantExit   int
		wantStatus string
	}{
		{"bad flag", nil, []string{"version", "--definitely-not-a-real-flag"}, errs.ExitUsage, "400"},
		{"bad flag --raw", nil, []string{"--raw", "version", "--definitely-not-a-real-flag"}, errs.ExitUsage, "400"},
		{"no API key --raw", nil, []string{"--raw", "--team", "t_team1", "contacts", "list"}, errs.ExitAuth, "401"},
		{
			"destructive without --yes --raw",
			[]string{"MIO_API_KEY=test-key", "MIO_API_BASE_URL=" + htmlSrv.URL},
			[]string{"--raw", "--team", "t_team1", "contacts", "delete", "ctt_any"},
			errs.ExitNeedsConfir, "412",
		},
		{
			"non-JSON:API body",
			[]string{"MIO_API_KEY=test-key", "MIO_API_BASE_URL=" + htmlSrv.URL},
			[]string{"--team", "t_team1", "contacts", "retrieve", "ctt_any"},
			errs.ExitServer, "502",
		},
		{
			"non-JSON:API body --raw",
			[]string{"MIO_API_KEY=test-key", "MIO_API_BASE_URL=" + htmlSrv.URL},
			[]string{"--raw", "--team", "t_team1", "contacts", "retrieve", "ctt_any"},
			errs.ExitServer, "502",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, exitCode := runBinary(t, bin, tc.env, tc.args...)
			if exitCode != tc.wantExit {
				t.Errorf("CONTRACT: exit code = %d, want %d; stderr=%q", exitCode, tc.wantExit, stderr)
			}
			objs := apiErrorObjects(t, "stderr envelope", decodeExact(t, "stderr envelope", stderr))
			if len(objs) != 1 {
				t.Fatalf("CONTRACT: want exactly one error object, got %d; stderr=%q", len(objs), stderr)
			}
			if got := strings.Join(sortedKeys(objs[0]), ","); got != "detail,meta,status" {
				t.Errorf("CONTRACT: errors[0] members = %s, want detail,meta,status; stderr=%q", got, stderr)
			}
			meta, _ := objs[0]["meta"].(map[string]any)
			if got := strings.Join(sortedKeys(meta), ","); got != "exit_code" {
				t.Errorf("CONTRACT: errors[0].meta members = %s, want exit_code; stderr=%q", got, stderr)
			}
			if objs[0]["status"] != tc.wantStatus {
				t.Errorf("CONTRACT: errors[0].status = %#v, want %q; stderr=%q", objs[0]["status"], tc.wantStatus, stderr)
			}
			if meta["exit_code"] != json.Number(strconv.Itoa(tc.wantExit)) {
				t.Errorf("CONTRACT: errors[0].meta.exit_code = %#v, want %d; stderr=%q", meta["exit_code"], tc.wantExit, stderr)
			}
		})
	}
}

func TestContract_ErrorEnvelope_HintedErrorsKeepAPIMembers(t *testing.T) {
	bin := buildBinary(t)

	cases := []struct {
		name       string
		httpStatus int
		body       string
		args       []string
		wantExit   int
		wantHint   string
		scaffold   bool
	}{
		{
			name:       "achievements grant 422 (hintAchievementsEarnErr)",
			httpStatus: 422,
			body:       `{"errors":[{"status":"422","code":"achievement_membership_required","title":"Unprocessable Entity","detail":"Contact 'ct_1' is not an active member of hub 'hub_1'.","meta":{"request_id":"rid-grant"}}]}`,
			args:       []string{"--team", "t_team1", "--hub", "hub_1", "achievements", "grant", "ach_1", "--contact-id", "ct_1"},
			wantExit:   errs.ExitUsage,
			wantHint:   "GLOBAL contact id",
		},
		{
			name:       "achievements update 422 rule_pieces_not_allowed (hintAchievementsUpdateErr)",
			httpStatus: 422,
			body:       `{"errors":[{"status":"422","code":"rule_pieces_not_allowed","title":"Unprocessable Entity","detail":"A manual badge cannot carry rule pieces.","source":{"pointer":"/data/attributes/rule_type"},"meta":{"request_id":"rid-update"}}]}`,
			args:       []string{"--team", "t_team1", "achievements", "update", "ach_1", "--award-mode", "manual"},
			wantExit:   errs.ExitUsage,
			wantHint:   "--clear rule-type",
		},
		{
			name:       "activity contact 404 (hintGlobalContactID)",
			httpStatus: 404,
			body:       `{"errors":[{"status":"404","code":"contact_not_found","title":"Not Found","detail":"Contact not found.","meta":{"request_id":"rid-activity"}}]}`,
			args:       []string{"--team", "t_team1", "--hub", "hub_1", "activity", "contact", "contact_x"},
			wantExit:   errs.ExitNotFound,
			wantHint:   "GLOBAL contact id",
		},
		{
			name:       "hubs scaffold 409 idempotency_fingerprint_mismatch (hubOpError)",
			httpStatus: 409,
			body:       `{"errors":[{"status":"409","code":"idempotency_fingerprint_mismatch","title":"Conflict","detail":"Idempotency-Key reused with a different request.","meta":{"request_id":"rid-scaffold"}}]}`,
			args:       scaffoldArgs(),
			wantExit:   errs.ExitUsage,
			wantHint:   "[idempotency_fingerprint_mismatch]",
			scaffold:   true,
		},
	}

	for _, tc := range cases {
		for _, raw := range []bool{false, true} {
			name := tc.name
			args := tc.args
			if raw {
				name += " --raw"
				args = append([]string{"--raw"}, tc.args...)
			}
			t.Run(name, func(t *testing.T) {
				var env []string
				if tc.scaffold {
					srv, _ := hubOpScaffoldServer(t, tc.httpStatus, tc.body)
					env = scaffoldEnv(t, srv.URL)
				} else {
					srv := newMockServer(t, []mockHandler{{Status: tc.httpStatus, Body: tc.body}})
					env = []string{"MIO_API_KEY=test-key", "MIO_API_BASE_URL=" + srv.URL}
				}
				_, stderr, exitCode := runBinary(t, bin, env, args...)
				if exitCode != tc.wantExit {
					t.Errorf("CONTRACT: exit code = %d, want %d; stderr=%q", exitCode, tc.wantExit, stderr)
				}
				api := decodeExact(t, "api body", tc.body)
				apiObj := apiErrorObjects(t, "api body", api)[0]
				envelope := stderr
				if tc.scaffold {
					if at := strings.LastIndex("\n"+stderr, "\n{\n"); at >= 0 {
						envelope = stderr[at:]
					}
				}
				emitted := decodeExact(t, "stderr envelope", envelope)
				got := apiErrorObjects(t, "stderr envelope", emitted)[0]

				for _, k := range []string{"code", "title"} {
					if got[k] != apiObj[k] {
						t.Errorf("CONTRACT: errors[0].%s = %#v, the API sent %#v; stderr=%q", k, got[k], apiObj[k], stderr)
					}
				}
				gotMeta, _ := got["meta"].(map[string]any)
				apiMeta := apiObj["meta"].(map[string]any)
				if gotMeta["request_id"] != apiMeta["request_id"] {
					t.Errorf("CONTRACT: errors[0].meta.request_id = %#v, the API sent %#v; stderr=%q", gotMeta["request_id"], apiMeta["request_id"], stderr)
				}

				if raw {
					if stripped := stripCLIAdditions(t, emitted, api); !reflect.DeepEqual(stripped, api) {
						t.Errorf("CONTRACT: --raw envelope minus the CLI's additions is not the API's document; stderr=%q", stderr)
					}
					return
				}
				if d, _ := got["detail"].(string); !strings.Contains(d, tc.wantHint) {
					t.Errorf("CONTRACT: errors[0].detail lost the CLI's hint %q; got %q", tc.wantHint, d)
				}
			})
		}
	}
}
