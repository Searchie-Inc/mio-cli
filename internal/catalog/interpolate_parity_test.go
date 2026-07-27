package catalog

// interpolate_parity_test.go — runs the Go interpolation implementation
// (interpolate.go) against the vendored cross-language corpus in
// testdata/interpolation. The corpus is the normative byte-parity arbiter
// shared with the TS and Python implementations: if a corpus case and the Go
// code disagree, the Go code is wrong — fix interpolate.go, never a fixture.
//
// Envelope: <case>.raw.json = {"vars": {hub_name, hub_slug}, "input": <plan>};
// <case>.expected.json = {"output": <plan>} on success or {"error": "<CODE>"}
// on rejection.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// interpolatePlanForTest mirrors the TS reference interpolatePlan
// (mio-page-catalog src/interpolate.ts): deep-clone the plan, then
// interpolate page titles, navigation labels, and tree leaf values — in that
// order — aborting on the first error. The input plan is never mutated.
func interpolatePlanForTest(t *testing.T, plan map[string]any, name, slug string) (map[string]any, error) {
	t.Helper()
	out, ok := deepClone(plan).(map[string]any)
	if !ok {
		t.Fatalf("deepClone returned %T, want map[string]any", deepClone(plan))
	}
	if pages, ok := out["pages"].([]any); ok {
		for _, p := range pages {
			page, ok := p.(map[string]any)
			if !ok {
				continue
			}
			title, ok := page["title"].(string)
			if !ok {
				continue
			}
			got, err := InterpolateTitle(title, name, slug)
			if err != nil {
				return nil, err
			}
			page["title"] = got
		}
	}
	if nav, ok := out["navigation"].(map[string]any); ok {
		if err := InterpolateNavigation(nav, name, slug); err != nil {
			return nil, err
		}
	}
	if trees, ok := out["trees"].([]any); ok {
		for _, tr := range trees {
			tree, ok := tr.(map[string]any)
			if !ok {
				continue
			}
			if err := InterpolateTreeValues(tree, name, slug); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// TestInterpolationCorpusParity replays every vendored corpus case against
// the Go implementation and compares canonical JSON byte-for-byte.
func TestInterpolationCorpusParity(t *testing.T) {
	const dir = "testdata/interpolation"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}

	cases := 0
	sawAstral := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".raw.json") {
			continue
		}
		cases++
		base := strings.TrimSuffix(e.Name(), ".raw.json")
		rawPath := filepath.Join(dir, e.Name())

		// Astral guard (§4.3): the corpus must exercise at least one
		// non-BMP code point somewhere in a raw input.
		rawBytes, err := os.ReadFile(rawPath)
		if err != nil {
			t.Fatalf("read %s: %v", rawPath, err)
		}
		for _, r := range string(rawBytes) {
			if r > 0xFFFF {
				sawAstral = true
				break
			}
		}

		t.Run(base, func(t *testing.T) {
			raw, ok := readJSONNumber(t, rawPath).(map[string]any)
			if !ok {
				t.Fatalf("%s: top level is not an object", rawPath)
			}
			expPath := filepath.Join(dir, base+".expected.json")
			expected, ok := readJSONNumber(t, expPath).(map[string]any)
			if !ok {
				t.Fatalf("%s: top level is not an object", expPath)
			}

			plan, ok := raw["input"].(map[string]any)
			if !ok {
				t.Fatalf("%s: missing or non-object \"input\"", rawPath)
			}
			var hubName, hubSlug string
			if vars, ok := raw["vars"].(map[string]any); ok {
				hubName, _ = vars["hub_name"].(string)
				hubSlug, _ = vars["hub_slug"].(string)
			}

			got, err := interpolatePlanForTest(t, plan, hubName, hubSlug)

			if wantCode, isErr := expected["error"]; isErr {
				if err == nil {
					t.Fatalf("expected error %v, got success", wantCode)
				}
				var ierr *InterpolationError
				if !errors.As(err, &ierr) {
					t.Fatalf("error is %T (%v), want *InterpolationError", err, err)
				}
				if ierr.Code != wantCode {
					t.Fatalf("error code = %q, want %v", ierr.Code, wantCode)
				}
				return
			}

			if err != nil {
				t.Fatalf("interpolatePlanForTest: %v", err)
			}
			wantOut, ok := expected["output"]
			if !ok {
				t.Fatalf("%s: expected file has neither \"output\" nor \"error\"", expPath)
			}
			if gotJSON, wantJSON := mustCanonical(t, got), mustCanonical(t, wantOut); gotJSON != wantJSON {
				t.Fatalf("canonical output mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}

	if cases != 9 {
		t.Errorf("found %d corpus cases in %s, want exactly 9", cases, dir)
	}
	if !sawAstral {
		t.Error("corpus must include a non-BMP (astral) case (§4.3)")
	}
}
