package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const (
	scaffoldPrimaryFlag     = "primary-color"
	scaffoldHeaderColorFlag = "header-color"

	brandingPrimaryKey     = "primary"
	brandingHeaderColorKey = "header_color"
)

type scaffoldBrandingFlag struct {
	flag string
	key  string
	help string
}

var scaffoldBrandingFlags = []scaffoldBrandingFlag{
	{scaffoldPrimaryFlag, brandingPrimaryKey,
		"Override the template's branding.primary (the hub's primary brand color). Unless a header color is given (--header-color, or a header_color key in --branding-json) this ALSO fills branding.header_color, so the header matches the brand."},
	{"secondary-color", "secondary",
		"Override the template's branding.secondary (the accent color)."},
	{"text-color", "text",
		"Override the template's branding.text (body text color)."},
	{"background-color", "background",
		"Override the template's branding.background (page background color)."},
	{scaffoldHeaderColorFlag, brandingHeaderColorKey,
		"Override the template's branding.header_color (header chrome). Setting it also switches OFF the --primary-color cascade."},
	{"header-accent", "header_accent",
		"Override the template's branding.header_accent (the header's accent color)."},
	{"social-image-url", "social_image_url",
		"Override the template's branding.social_image_url (the Open Graph / social share image)."},
}

type scaffoldBranding struct {
	jsonBlob map[string]any
	scalars  map[string]string
	cascaded map[string]bool
}

func resolveScaffoldBranding(cmd *cobra.Command) (scaffoldBranding, error) {
	b := scaffoldBranding{scalars: map[string]string{}, cascaded: map[string]bool{}}

	obj, err := parseJSONObjectFlag(cmd, "branding-json")
	if err != nil {
		return scaffoldBranding{}, err
	}
	if obj != nil {
		if verr := validateBlobKeys(io.Discard, brandingKeyCheck, obj, true); verr != nil {
			return scaffoldBranding{}, scaffoldStrictKeyErr(verr, scaffoldFlagStrictKeyHint)
		}
		b.jsonBlob = obj
	}

	for _, f := range scaffoldBrandingFlags {
		if v := changedString(cmd, f.flag); v != nil {
			b.scalars[f.key] = *v
		}
	}

	if primary, ok := b.scalars[brandingPrimaryKey]; ok && !b.headerColorGiven() {
		b.scalars[brandingHeaderColorKey] = primary
		b.cascaded[brandingHeaderColorKey] = true
	}

	return b, nil
}

const (
	scaffoldFlagStrictKeyHint = "`hubs scaffold` always checks these keys strictly (it has no --strict-keys to drop). Fix the key; to send one this best-effort allowlist does not know, scaffold first and then apply it with `mio hubs update <hub-id> --branding-json`."

	scaffoldTemplateStrictKeyHint = "`hubs scaffold` checks these keys strictly and has no --strict-keys to drop. This key came from the HUB TEMPLATE, not from a flag you passed (the flag named above is the equivalent `hubs create`/`hubs update` one): fix the template in the page-builder catalog, or scaffold from a corrected copy with --catalog <file>."
)

func scaffoldStrictKeyErr(err error, hint string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, strictKeyDropHint) {
		return err
	}
	return errs.New(errs.CodeOf(err), "%s", strings.Replace(msg, strictKeyDropHint, hint, 1))
}

func (b scaffoldBranding) headerColorGiven() bool {
	if _, ok := b.scalars[brandingHeaderColorKey]; ok {
		return true
	}
	_, ok := b.jsonBlob[brandingHeaderColorKey]
	return ok
}

func (b scaffoldBranding) empty() bool {
	return len(b.jsonBlob) == 0 && len(b.scalars) == 0
}

func (b scaffoldBranding) applyTo(tmplBranding map[string]any) map[string]any {
	if b.empty() {
		return tmplBranding
	}
	out := deepMergeMap(tmplBranding, b.jsonBlob)
	for k, v := range b.scalars {
		out[k] = v
	}
	return out
}

func (b scaffoldBranding) resolved() map[string]any {
	out := make(map[string]any, len(b.jsonBlob)+len(b.scalars))
	for k, v := range b.jsonBlob {
		out[k] = v
	}
	for k, v := range b.scalars {
		out[k] = v
	}
	return out
}

func (b scaffoldBranding) describe() string {
	res := b.resolved()
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		part := k + "=" + formatBrandingValue(res[k])
		if b.cascaded[k] {
			part += " (cascaded from --" + scaffoldPrimaryFlag + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func (b scaffoldBranding) planDetail() string {
	if b.empty() {
		return ""
	}
	return " [branding overrides: " + b.describe() + "]"
}

func (b scaffoldBranding) flagArgs() []string {
	var parts []string
	if len(b.jsonBlob) > 0 {
		if enc, err := json.Marshal(b.jsonBlob); err == nil {
			parts = append(parts, fmt.Sprintf("--branding-json %q", string(enc)))
		}
	}
	for _, f := range scaffoldBrandingFlags {
		v, ok := b.scalars[f.key]
		if !ok || b.cascaded[f.key] {
			continue
		}
		parts = append(parts, fmt.Sprintf("--%s %q", f.flag, v))
	}
	return parts
}

func formatBrandingValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if enc, err := json.Marshal(v); err == nil {
		return string(enc)
	}
	return fmt.Sprintf("%v", v)
}

func registerScaffoldBrandingFlags(cmd *cobra.Command) {
	for _, f := range scaffoldBrandingFlags {
		cmd.Flags().String(f.flag, "", f.help)
	}
	cmd.Flags().String("branding-json", "",
		"Branding keys to merge OVER the template's branding block, as a JSON object (inline or @file). The scalar branding flags WIN over the same key here. Accepted keys: "+
			brandingKeysHelp+". Unknown keys are an ERROR (scaffold runs in strict key mode).")
}

func validateScaffoldBrandingURLs(b scaffoldBranding, logo, favicon *string) error {
	bad := func(key string, v any) bool {
		if !strings.HasSuffix(strings.ToLower(key), "_url") {
			return false
		}
		str, ok := v.(string)
		return ok && strings.TrimSpace(str) == ""
	}

	offenders := make([]string, 0, 2)
	for k, v := range b.resolved() {
		if bad(k, v) {
			offenders = append(offenders, k)
		}
	}
	for _, e := range []struct {
		key string
		val *string
	}{{"logo_url", logo}, {"favicon_url", favicon}} {
		if e.val != nil && bad(e.key, *e.val) {
			offenders = append(offenders, e.key)
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)

	return errs.New(errs.ExitUsage,
		"branding %s was given an empty or whitespace-only value, which the API rejects on both hub create "+
			"and hub update (branding *_url keys must be absolute https:// URLs). To CLEAR a branding key, "+
			"scaffold without it and then run: mio hubs update <hub_id> --unset branding.%s",
		strings.Join(offenders, ", "), offenders[0])
}
