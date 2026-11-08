package catalog

import (
	"errors"
	"strings"
	"testing"
)

const rocket = "\U0001F680"

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want *InterpolationError with code %q, got nil", code)
	}
	var ie *InterpolationError
	if !errors.As(err, &ie) {
		t.Fatalf("want *InterpolationError, got %T: %v", err, err)
	}
	if ie.Code != code {
		t.Fatalf("want code %q, got %q (%v)", code, ie.Code, err)
	}
}

func TestInterpolateTitle_ReplacesBothTokensAllOccurrences(t *testing.T) {
	got, err := InterpolateTitle("{{hub_name}} ({{hub_slug}}) {{hub_name}}", "Acme", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Acme (acme) Acme"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInterpolate_UnknownWhitespacedDanglingAreErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{name: "unknown token name", in: "hi {{hub_id}}"},
		{name: "inner whitespace is not tolerated", in: "{{ hub_name }}"},
		{name: "dangling open brace never closed", in: "Join {{hub_name today"},
		{name: "dangling close brace never opened", in: "stray }} close"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InterpolateTitle(tc.in, "Acme", "acme")
			wantCode(t, err, CodeUnknownToken)
		})
	}
}

func TestInterpolate_ValueInsertedLiterally(t *testing.T) {
	name := "{{hub_slug}} & $1 $& \\1 ${x} \\g<0>"
	got, err := InterpolateTitle("{{hub_name}}", name, "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != name {
		t.Fatalf("replacement not literal: got %q, want %q", got, name)
	}
}

func TestInterpolateTitle_CapIsCodePointsStrict(t *testing.T) {
	atCap := strings.Repeat(rocket, CapPageTitle)
	got, err := InterpolateTitle("{{hub_name}}", atCap, "s")
	if err != nil {
		t.Fatalf("unexpected error at exact cap: %v", err)
	}
	if got != atCap {
		t.Fatalf("got %q, want %q", got, atCap)
	}
	_, err = InterpolateTitle("{{hub_name}}", atCap+rocket, "s")
	wantCode(t, err, CodeTitleTooLong)
}

func TestInterpolateTreeValues_ScansOnlyLeafKinds(t *testing.T) {
	tree := Node{
		"kind":  "stack",
		"value": "{{hub_name}}",
		"children": []any{
			map[string]any{"kind": "headline", "value": "Welcome to {{hub_name}}"},
			map[string]any{"kind": "text", "value": "slug: {{hub_slug}}"},
			map[string]any{
				"kind":  "button",
				"value": "Join {{hub_name}}",
				"settings": map[string]any{
					"action": map[string]any{"value": "{{hub_slug}}"},
				},
			},
		},
	}
	if err := InterpolateTreeValues(tree, "Acme", "acme"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := tree["value"]; got != "{{hub_name}}" {
		t.Fatalf("stack value must stay untouched, got %q", got)
	}
	children := tree["children"].([]any)
	if got := children[0].(map[string]any)["value"]; got != "Welcome to Acme" {
		t.Fatalf("headline value: got %q", got)
	}
	if got := children[1].(map[string]any)["value"]; got != "slug: acme" {
		t.Fatalf("text value: got %q", got)
	}
	button := children[2].(map[string]any)
	if got := button["value"]; got != "Join Acme" {
		t.Fatalf("button value: got %q", got)
	}
	action := button["settings"].(map[string]any)["action"].(map[string]any)
	if got := action["value"]; got != "{{hub_slug}}" {
		t.Fatalf("settings.action.value must stay literal, got %q", got)
	}

	long := Node{"kind": "text", "value": strings.Repeat(rocket, CapLeafValue+1)}
	wantCode(t, InterpolateTreeValues(long, "Acme", "acme"), CodeValueTooLong)
}

func TestInterpolateNavigation_LabelsOnly(t *testing.T) {
	nav := map[string]any{
		"header": []any{
			map[string]any{
				"label": "{{hub_name}} Home",
				"href":  "/hubs/{{hub_slug}}",
			},
		},
		"footer": []any{
			map[string]any{"label": "Contact"},
		},
	}
	if err := InterpolateNavigation(nav, "Acme", "acme"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	header := nav["header"].([]any)[0].(map[string]any)
	if got := header["label"]; got != "Acme Home" {
		t.Fatalf("header label: got %q", got)
	}
	if got := header["href"]; got != "/hubs/{{hub_slug}}" {
		t.Fatalf("href must stay literal, got %q", got)
	}
	footer := nav["footer"].([]any)[0].(map[string]any)
	if got := footer["label"]; got != "Contact" {
		t.Fatalf("footer label: got %q", got)
	}

	over := map[string]any{
		"footer": []any{
			map[string]any{"label": strings.Repeat(rocket, CapNavLabel+1)},
		},
	}
	wantCode(t, InterpolateNavigation(over, "Acme", "acme"), CodeLabelTooLong)
}
