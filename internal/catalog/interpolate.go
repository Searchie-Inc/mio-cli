package catalog

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	CapLeafValue = 5000
	CapPageTitle = 200
	CapNavLabel  = 80
	MaxHubNameCP = 255
)

const (
	CodeUnknownToken = "UNKNOWN_TOKEN"
	CodeValueTooLong = "VALUE_TOO_LONG"
	CodeTitleTooLong = "TITLE_TOO_LONG"
	CodeLabelTooLong = "LABEL_TOO_LONG"
)

var (
	knownTokenRE = regexp.MustCompile(`\{\{(hub_name|hub_slug)\}\}`)
	anyTokenRE   = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

var scannedLeafKinds = map[string]bool{"headline": true, "text": true, "button": true}

type InterpolationError struct {
	Code string
	msg  string
}

func (e *InterpolationError) Error() string { return e.msg }

func interpolateString(s, hubName, hubSlug string, capCP int, code string) (string, error) {
	var bad []string
	for _, m := range anyTokenRE.FindAllString(s, -1) {
		if knownTokenRE.FindString(m) != m {
			bad = append(bad, m)
		}
	}
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

func truncateForError(s string) string {
	const max = 120
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

func InterpolateTitle(title, hubName, hubSlug string) (string, error) {
	return interpolateString(title, hubName, hubSlug, CapPageTitle, CodeTitleTooLong)
}

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
