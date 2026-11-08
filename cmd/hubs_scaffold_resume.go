package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func checkReapplyTemplate(cmd *cobra.Command, templateID string) (bool, error) {
	reapply, _ := cmd.Flags().GetBool("reapply-template")
	if !reapply {
		return false, nil
	}
	if flags.hub == "" {
		return false, errs.New(errs.ExitUsage,
			"--reapply-template needs --hub <id>: it overwrites an EXISTING hub's branding, navigation, settings, policy text and onboarding config with the template's values (and turns its policy gate on when the template enables it), and a create has nothing to overwrite")
	}
	if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
		return true, nil
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Overwrite hub %s's branding, navigation, settings (registration included), policy text and onboarding config with template %q, and turn its policy gate on if the template enables it?",
		flags.hub, templateID)); err != nil {
		return false, err
	}
	return true, nil
}

type keptValue struct {
	path     string
	hub, tpl any
}

func (k keptValue) String() string {
	if k.tpl == nil {
		return fmt.Sprintf("%s=%s (template: none)", k.path, describeKeptValue(k.hub))
	}
	return fmt.Sprintf("%s=%s (template: %s)", k.path, describeKeptValue(k.hub), describeKeptValue(k.tpl))
}

func describeKeptValue(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case []any:
		return fmt.Sprintf("%d item(s)", len(x))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:57]) + "..."
	}
	return s
}

func joinKept(kept []keptValue) string {
	parts := make([]string, len(kept))
	for i, k := range kept {
		parts[i] = k.String()
	}
	return strings.Join(parts, "; ")
}

func sameJSON(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ab, bb)
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneJSONValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return catalog.CloneNode(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneJSONValue(e)
		}
		return out
	}
	return v
}

func fillMissing(prefix string, tmpl, cur map[string]any, kept *[]keptValue) map[string]any {
	var out map[string]any
	for _, k := range sortedMapKeys(tmpl) {
		tv, path := tmpl[k], prefix+"."+k
		cv, present := cur[k]
		if !present || cv == nil {
			if tv == nil {
				continue
			}
			if out == nil {
				out = map[string]any{}
			}
			out[k] = cloneJSONValue(tv)
			continue
		}
		tm, tIsMap := tv.(map[string]any)
		cm, cIsMap := cv.(map[string]any)
		if tIsMap && cIsMap {
			if sub := fillMissing(path, tm, cm, kept); len(sub) > 0 {
				if out == nil {
					out = map[string]any{}
				}
				out[k] = sub
			}
			continue
		}
		if !sameJSON(tv, cv) {
			*kept = append(*kept, keptValue{path: path, hub: cv, tpl: tv})
		}
	}
	return out
}

func navBucketEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []any:
		return len(x) == 0
	}
	return false
}

func fillNavigation(tmplNav, curNav map[string]any, kept *[]keptValue) map[string]any {
	if tmplNav == nil {
		return nil
	}
	filled := false
	out := make(map[string]any, len(curNav)+len(tmplNav))
	for k, v := range curNav {
		out[k] = v
	}
	for _, b := range sortedMapKeys(tmplNav) {
		tv, cv := tmplNav[b], curNav[b]
		switch {
		case navBucketEmpty(cv) && !navBucketEmpty(tv):
			out[b] = tv
			filled = true
		case !navBucketEmpty(cv) && !sameJSON(tv, cv):
			*kept = append(*kept, keptValue{path: "navigation." + b, hub: cv, tpl: tv})
		}
	}
	for _, b := range sortedMapKeys(curNav) {
		if _, declared := tmplNav[b]; !declared && !navBucketEmpty(curNav[b]) {
			*kept = append(*kept, keptValue{path: "navigation." + b, hub: curNav[b]})
		}
	}
	if !filled {
		return nil
	}
	return out
}

type blobFill struct {
	branding, settings, navigation map[string]any
	kept                           []keptValue
}

func (sc *scaffoldContext) planBlobFill(t *catalog.HubTemplate, nav map[string]any, cur map[string]any) blobFill {
	var kept []keptValue
	f := blobFill{
		branding:   sc.branding.applyTo(fillMissing("branding", t.Branding, attrMap(cur["branding"]), &kept)),
		settings:   fillMissing("settings", fillableSettings(t.Settings), attrMap(cur["settings"]), &kept),
		navigation: fillNavigation(nav, attrMap(cur["navigation"]), &kept),
	}
	for _, k := range kept {
		if !sc.overwrittenByInvocation(k.path) {
			f.kept = append(f.kept, k)
		}
	}
	return f
}

func (f blobFill) empty(sc *scaffoldContext) bool {
	return f.branding == nil && f.settings == nil && f.navigation == nil &&
		sc.logoOverride == nil && sc.faviconOverride == nil && sc.registrationOverride == nil
}

func fillableSettings(s map[string]any) map[string]any {
	if _, ok := s["policies"]; !ok {
		return s
	}
	out := make(map[string]any, len(s))
	for k, v := range s {
		if k != "policies" {
			out[k] = v
		}
	}
	return out
}

func (sc *scaffoldContext) overwrittenByInvocation(path string) bool {
	segs := strings.Split(path, ".")
	switch {
	case path == "branding.logo_url" && sc.logoOverride != nil,
		path == "branding.favicon_url" && sc.faviconOverride != nil,
		path == "settings.registration.enabled" && sc.registrationOverride != nil:
		return true
	case segs[0] == "branding":
		return pathSetIn(sc.branding.resolved(), segs[1:])
	}
	return false
}

func pathSetIn(m map[string]any, segs []string) bool {
	for i, s := range segs {
		v, ok := m[s]
		if !ok {
			return false
		}
		if i == len(segs)-1 {
			return true
		}
		sub, isMap := v.(map[string]any)
		if !isMap {
			return true
		}
		m = sub
	}
	return false
}

func validateTemplateBlobKeysStrict(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if err := validateBlobKeys(io.Discard, brandingKeyCheck, sc.branding.applyTo(t.Branding), true); err != nil {
		return scaffoldStrictKeyErr(err, scaffoldTemplateStrictKeyHint)
	}
	if err := validateBlobKeys(io.Discard, settingsKeyCheck, t.Settings, true); err != nil {
		return scaffoldStrictKeyErr(err, scaffoldTemplateStrictKeyHint)
	}
	return nil
}

func stepBlobsFillGaps(sc *scaffoldContext, t *catalog.HubTemplate) error {
	nav, err := prepareTemplateNav(sc, t)
	if err != nil {
		return err
	}
	if err := validateTemplateBlobKeysStrict(sc, t); err != nil {
		return err
	}
	if err := validateNavigationHrefs(nav, sc.hubSlug); err != nil {
		return err
	}

	planned := sc.planBlobFill(t, nav, sc.resumeHub)
	detail := fmt.Sprintf("PATCH %s — fill gaps only: add the branding/settings keys and navigation buckets the hub lacks, keep what it has (flags on this command still win) (strict keys)%s",
		hubsPath(sc.teamID, sc.hubIDOrPlaceholder()), sc.branding.planDetail())
	if planned.empty(sc) {
		detail = fmt.Sprintf("nothing to fill — the hub already has every branding/settings key and navigation bucket the template declares; no PATCH%s", sc.branding.planDetail())
	}
	if len(planned.kept) > 0 {
		detail += "; keeps the hub's own " + joinKept(planned.kept)
	}

	return sc.step("blobs", detail, func() error {
		cur, rerr := sc.cl.Retrieve(sc.ctx, hubsPath(sc.teamID, sc.hubID))
		if rerr != nil {
			return rerr
		}
		fill := sc.planBlobFill(t, nav, cur.Attributes)
		if len(fill.kept) > 0 {
			sc.notef("blobs: kept %d value(s) this hub already has (a --hub run fills gaps only; --reapply-template overwrites them with the template's): %s",
				len(fill.kept), joinKept(fill.kept))
		}
		if fill.empty(sc) {
			sc.notef("blobs: nothing to fill — the hub already has every branding/settings key and navigation bucket the template declares; no PATCH sent.")
			return nil
		}
		_, perr := applyHubBlobs(sc.ctx, sc.cl, sc.teamID, sc.hubID, sc.hubSlug, blobPatches{
			Branding:        fill.branding,
			Settings:        fill.settings,
			Navigation:      fill.navigation,
			SlugKnown:       true,
			Favicon:         sc.faviconOverride,
			Logo:            sc.logoOverride,
			Registration:    sc.registrationOverride,
			Strict:          true,
			Current:         cur,
			NavHrefsChecked: true,
		}, io.Discard)
		if fill.navigation != nil {
			perr = explainKeptNavRejection(perr, sc.hubID, attrMap(cur.Attributes["navigation"]))
		}
		return scaffoldStrictKeyErr(perr, scaffoldTemplateStrictKeyHint)
	})
}

func explainKeptNavRejection(err error, hubID string, curNav map[string]any) error {
	if err == nil || !client.HasAPIErrorCode(err, "navigation_page_invalid") {
		return err
	}
	var kept []string
	for _, b := range sortedMapKeys(curNav) {
		if !navBucketEmpty(curNav[b]) {
			kept = append(kept, b)
		}
	}
	if len(kept) == 0 {
		return err
	}
	return errs.Wrap(errs.CodeOf(err), fmt.Errorf(
		"%w — this run sent the hub's own navigation bucket(s) [%s] back unchanged alongside the one(s) it filled (the API stores navigation whole), and the API refuses every navigation write while a page item points at a page that is not one of this hub's active pages (a deleted page, typically). If the item it names is in one of those buckets it is already on the hub's menu: remove it (`mio hubs navigation list %s`, then `mio hubs navigation remove %s <bucket> --index <n>`) and re-run",
		err, strings.Join(kept, ", "), hubID, hubID))
}

func storedPolicies(attrs map[string]any) map[string]any {
	settings, _ := attrs["settings"].(map[string]any)
	policies, _ := settings["policies"].(map[string]any)
	return policies
}

type policyFillDoc struct {
	pol        hubPolicy
	configured bool
	hubChars   int
	same       bool
}

func (d policyFillDoc) write() bool { return d.pol.Content != nil && !d.configured }

func (d policyFillDoc) note() string {
	pt := d.pol.PolicyType
	switch {
	case d.write():
		return fmt.Sprintf("policies: wrote the template's %s text — the hub had none of its own (it was serving the platform default).", pt)
	case d.same:
		return fmt.Sprintf("policies: %s already carries the template's text — nothing to write.", pt)
	case d.configured && d.pol.Content == nil:
		return fmt.Sprintf("policies: kept the hub's own %s text (%d characters) — the template declares none, and a --hub run never resets a policy to the platform default (--reapply-template does).", pt, d.hubChars)
	case d.configured:
		return fmt.Sprintf("policies: kept the hub's own %s text (%d characters) — the template's text was not applied (a --hub run fills gaps only; --reapply-template replaces it).", pt, d.hubChars)
	default:
		return fmt.Sprintf("policies: %s — the template declares no text and the hub has none of its own, so the platform default already applies; nothing to write.", pt)
	}
}

type policyFill struct {
	docs               []policyFillDoc
	gateSet, gateValue bool
}

func planPolicyFill(res templatePolicies, stored map[string]any) policyFill {
	var out policyFill
	for _, pol := range res.policies {
		doc, _ := stored[pol.PolicyType].(map[string]any)
		text, configured := doc["content"].(string)
		d := policyFillDoc{pol: pol, configured: configured, hubChars: utf8.RuneCountInString(text)}
		d.same = configured && pol.Content != nil && *pol.Content == text
		out.docs = append(out.docs, d)
	}
	if v, ok := stored["enabled"].(bool); ok {
		out.gateSet, out.gateValue = true, v
	}
	return out
}

func (f policyFill) describe() string {
	var write, keep, none []string
	for _, d := range f.docs {
		switch {
		case d.write():
			write = append(write, d.pol.PolicyType)
		case d.configured && !d.same:
			keep = append(keep, fmt.Sprintf("%s (%d characters)", d.pol.PolicyType, d.hubChars))
		default:
			none = append(none, d.pol.PolicyType)
		}
	}
	var parts []string
	if len(write) > 0 {
		parts = append(parts, "write the template's text for ["+strings.Join(write, ", ")+"] (the hub has none of its own)")
	}
	if len(keep) > 0 {
		parts = append(parts, "keep the hub's own text for ["+strings.Join(keep, ", ")+"]")
	}
	if len(none) > 0 {
		parts = append(parts, "nothing to write for ["+strings.Join(none, ", ")+"]")
	}
	return strings.Join(parts, "; ")
}

func (sc *scaffoldContext) existingHubConfigs() (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	seen := map[string]bool{}
	query := url.Values{}
	const maxPages = 1000
	for page := 0; page < maxPages; page++ {
		col, err := sc.cl.List(sc.ctx, contactAttributesHubConfigPath(sc.teamID, sc.hubID, ""), query)
		if err != nil {
			return nil, err
		}
		for _, r := range col.Data {
			if id, _ := r.Attributes["definition_id"].(string); id != "" {
				out[id] = r.Attributes
			}
		}
		next := nextPageCursor(col)
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		query = url.Values{}
		query.Set("page[after]", next)
	}
	return out, nil
}

func keptHubConfig(d catalog.TemplateAttrDef, cfg map[string]any) string {
	inOn, _ := cfg["is_in_onboarding"].(bool)
	req, _ := cfg["is_required"].(bool)
	if inOn == d.InOnboarding && req == d.Required {
		return ""
	}
	return fmt.Sprintf("config for %q (is_in_onboarding=%t, is_required=%t; template: is_in_onboarding=%t, is_required=%t)",
		d.Slug, inOn, req, d.InOnboarding, d.Required)
}

func onboardingFillPlanDetail(sc *scaffoldContext, t *catalog.HubTemplate) (string, error) {
	const base = "; --hub fills gaps only: an attribute the hub already has a config for keeps it"
	if !sc.dryRun {
		return base, nil
	}
	defs, err := sc.existingContactAttrDefs()
	if err != nil {
		return "", err
	}
	configs, err := sc.existingHubConfigs()
	if err != nil {
		return "", err
	}
	var kept []string
	for _, d := range t.Onboarding {
		if cfg, ok := configs[defs[d.Slug]]; ok && defs[d.Slug] != "" {
			if k := keptHubConfig(d, cfg); k != "" {
				kept = append(kept, k)
			}
		}
	}
	if len(kept) == 0 {
		return base, nil
	}
	return base + "; keeps the hub's " + strings.Join(kept, "; "), nil
}

func preflightResumePages(sc *scaffoldContext) error {
	if sc.pagePlan == nil || len(sc.pagePlan.pages) == 0 {
		return nil
	}
	ourApp := catalog.ApplicationID(sc.hubID, sc.hubTmpl.ID)
	var conflicts []string
	for _, pp := range sc.pagePlan.pages {
		rp, err := sc.recoverPageAtSlug(pp.ref.Slug, ourApp)
		if err != nil {
			return err
		}
		switch decideRecovery(ourApp, rp) {
		case actionConflict:
			conflicts = append(conflicts, fmt.Sprintf("page %q conflicts with existing page %s (%s)",
				pp.ref.Slug, rp.id, recoveryConflictReason(ourApp, rp)))
		case actionNoop, actionResumeFull:
			if !pp.ref.IsHomepage || rp.isHome {
				continue
			}
			hres, found, herr := sc.foreignHomepage(rp.id)
			if herr != nil {
				return herr
			}
			if found {
				conflicts = append(conflicts, fmt.Sprintf("page %q would replace the hub's existing homepage %s (making it the homepage clears the current one server-side)",
					pp.ref.Slug, hres.ID))
			}
		case actionCreate:
			if !pp.ref.IsHomepage {
				continue
			}
			hres, found, herr := sc.findHubPage(func(r client.Resource) bool {
				isHome, _ := r.Attributes["is_homepage"].(bool)
				return isHome
			})
			if herr != nil {
				return herr
			}
			if found {
				conflicts = append(conflicts, fmt.Sprintf("page %q would replace the hub's existing homepage %s (creating a homepage clears the current one server-side)",
					pp.ref.Slug, hres.ID))
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return errs.New(errs.ExitUsage,
		"%s; refusing to overwrite, and nothing was written (pages are checked before any write on --hub) — inspect the page(s), or re-run with --hub %s after resolving",
		strings.Join(conflicts, "; "), sc.hubID)
}
