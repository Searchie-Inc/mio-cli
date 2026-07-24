package catalog

// interpolate.go — the Go port of the normative {{hub_name}}/{{hub_slug}}
// token-interpolation contract (MIO-2573 spec §4.3, reference implementation
// mio-page-catalog src/interpolate.ts). The Go, TS, and Python implementations
// MUST agree byte-for-byte; the vendored cross-language corpus asserts parity.
// Interpolation is a SEPARATE pass — the applier (applier.go) stays literal;
// run this AFTER instantiation and BEFORE any write.
//
// Contract in brief: a closed two-token vocabulary with no inner whitespace
// and no escape syntax; any other {{…}} occurrence or dangling brace pair is
// UNKNOWN_TOKEN; replacement values are inserted literally and never
// re-scanned; post-substitution caps are counted in Unicode code points with
// a strict > comparison. Scanned locations are exhaustive: leaf `value` on
// headline/text/button nodes, page titles, and navigation header/footer item
// labels — nothing else.

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Post-substitution caps in Unicode code points (§4.3), plus the --name
// preflight bound for a hub name itself: the hub title DB column is
// VARCHAR(255), so the CLI preflights MaxHubNameCP and a bad --name fails
// before any write.
const (
	CapLeafValue = 5000
	CapPageTitle = 200
	CapNavLabel  = 80
	MaxHubNameCP = 255
)

// Machine-readable error codes — the exact strings shared with the TS and
// Python implementations (corpus vocabulary).
const (
	CodeUnknownToken = "UNKNOWN_TOKEN"
	CodeValueTooLong = "VALUE_TOO_LONG"
	CodeTitleTooLong = "TITLE_TOO_LONG"
	CodeLabelTooLong = "LABEL_TOO_LONG"
)

var (
	// knownTokenRE is the closed vocabulary: exactly {{hub_name}} and
	// {{hub_slug}}, no inner whitespace.
	knownTokenRE = regexp.MustCompile(`\{\{(hub_name|hub_slug)\}\}`)
	// anyTokenRE collects every {{…}} occurrence (dotall, non-greedy) so
	// unknown spellings can be reported.
	anyTokenRE = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

// scannedLeafKinds are the node kinds whose string `value` is scanned
// (§4.3 allowed location (a)). Nothing else in a node is scanned.
var scannedLeafKinds = map[string]bool{"headline": true, "text": true, "button": true}

// InterpolationError carries the machine code the cross-language corpus keys
// on alongside a human-readable message.
type InterpolationError struct {
	Code string
	msg  string
}

func (e *InterpolationError) Error() string { return e.msg }

// interpolateString substitutes the known tokens in one string, rejecting
// unknown/dangling tokens and enforcing the location's code-point cap.
// Ports interpolateString + unknownTokensIn from the TS reference.
func interpolateString(s, hubName, hubSlug string, capCP int, code string) (string, error) {
	var bad []string
	for _, m := range anyTokenRE.FindAllString(s, -1) {
		// Unknown unless the match IS exactly a known token.
		if knownTokenRE.FindString(m) != m {
			bad = append(bad, m)
		}
	}
	// Dangling/unbalanced braces that never formed a token: strip every
	// {{…}} occurrence and look for leftover brace pairs.
	stripped := anyTokenRE.ReplaceAllString(s, "")
	if strings.Contains(stripped, "{{") || strings.Contains(stripped, "}}") {
		bad = append(bad, "{{…}}")
	}
	if len(bad) > 0 {
		return "", &InterpolationError{
			Code: CodeUnknownToken,
			msg: fmt.Sprintf("interpolate: unknown or malformed token(s) %v in %q (only {{hub_name}} and {{hub_slug}} are allowed)",
				bad, truncateForError(s)),
		}
	}
	// ReplaceAllStringFunc inserts the returned string literally — no
	// $-expansion, and the replacement is never re-scanned for tokens.
	out := knownTokenRE.ReplaceAllStringFunc(s, func(m string) string {
		if m == "{{hub_name}}" {
			return hubName
		}
		return hubSlug
	})
	if n := utf8.RuneCountInString(out); n > capCP {
		return "", &InterpolationError{
			Code: code,
			msg:  fmt.Sprintf("interpolate: %s: %d code points after substitution (max %d)", code, n, capCP),
		}
	}
	return out, nil
}

// truncateForError bounds an offending input quoted in an error message to
// its first 120 code points so a pathological string stays readable.
func truncateForError(s string) string {
	const max = 120
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// InterpolateTitle interpolates a page title (§4.3 allowed location (b)),
// capped at CapPageTitle code points.
func InterpolateTitle(title, hubName, hubSlug string) (string, error) {
	return interpolateString(title, hubName, hubSlug, CapPageTitle, CodeTitleTooLong)
}

// InterpolateNavigation interpolates, in place, the `label` of every
// header[]/footer[] item in a hub navigation blob (§4.3 allowed location (c)),
// capped at CapNavLabel code points. Nothing else on a nav item is scanned.
func InterpolateNavigation(nav map[string]any, hubName, hubSlug string) error {
	for _, zone := range []string{"header", "footer"} {
		items, ok := nav[zone].([]any)
		if !ok {
			continue
		}
		for _, it := range items {
			item, ok := it.(map[string]any)
			if !ok {
				continue
			}
			label, ok := item["label"].(string)
			if !ok {
				continue
			}
			out, err := interpolateString(label, hubName, hubSlug, CapNavLabel, CodeLabelTooLong)
			if err != nil {
				return err
			}
			item["label"] = out
		}
	}
	return nil
}

// InterpolateTreeValues walks an instantiated node tree in place,
// interpolating the string `value` of every headline/text/button node
// (§4.3 allowed location (a)), capped at CapLeafValue code points. No other
// node field is scanned — not settings, href, icon, or slug.
func InterpolateTreeValues(node Node, hubName, hubSlug string) error {
	if kind, _ := node["kind"].(string); scannedLeafKinds[kind] {
		if value, ok := node["value"].(string); ok {
			out, err := interpolateString(value, hubName, hubSlug, CapLeafValue, CodeValueTooLong)
			if err != nil {
				return err
			}
			node["value"] = out
		}
	}
	if children, ok := node["children"].([]any); ok {
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				if err := InterpolateTreeValues(child, hubName, hubSlug); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
