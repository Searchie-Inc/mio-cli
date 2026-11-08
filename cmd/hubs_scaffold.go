package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
)

type scaffoldContext struct {
	ctx    context.Context
	cl     *client.Client
	teamID string

	hubID, hubSlug string
	hubName        string
	isPrivate      bool
	isPrivateKnown bool

	nameOverride, slugOverride string

	faviconOverride, logoOverride *string
	registrationOverride          *bool

	branding scaffoldBranding

	spaceIDsBySlug, defIDsBySlug, playlistIDsByKey map[string]string

	homePageID       string
	homeDraftVersion int

	pageIDsBySlug map[string]string

	policyGate *bool

	welcomePostID, welcomePostStatus string

	contentNodes      []scaffoldContentNode
	contentNodesKnown bool

	playlistsNotInHub map[string]bool

	publish bool

	cat             *catalog.Catalog
	hubTmpl         catalog.HubTemplate
	pagePlan        *scaffoldPlan
	catalogOverride string

	dryRun bool
	plan   *[]planEntry

	noteW io.Writer

	resume, reapplyTemplate bool
	resumeHub               map[string]any
}

func (sc *scaffoldContext) fillGaps() bool {
	return sc.resume && !sc.reapplyTemplate
}

func (sc *scaffoldContext) notef(format string, args ...any) {
	if sc.noteW == nil {
		return
	}
	fmt.Fprintf(sc.noteW, format+"\n", args...)
}

func (sc *scaffoldContext) recordPageID(slug, pageID string) {
	if slug == "" || pageID == "" {
		return
	}
	if sc.pageIDsBySlug == nil {
		sc.pageIDsBySlug = map[string]string{}
	}
	sc.pageIDsBySlug[slug] = pageID
}

type planEntry struct {
	step   string
	detail string
}

func (sc *scaffoldContext) recordPlan(step, detail string) {
	if sc.plan == nil {
		return
	}
	*sc.plan = append(*sc.plan, planEntry{step: step, detail: detail})
}

func (sc *scaffoldContext) step(name, detail string, fn func() error) error {
	if sc.dryRun {
		sc.recordPlan(name, detail)
		return nil
	}
	return fn()
}

type scaffoldStep struct {
	name string
	run  func(sc *scaffoldContext, t *catalog.HubTemplate) error
}

var scaffoldPipeline = []scaffoldStep{
	{"hub", stepHub},
	{"blobs", stepBlobs},
	{"spaces", stepSpaces},
	{"onboarding", stepOnboarding},
	{"policies", stepPolicies},
	{"playlists", stepPlaylists},
	{"content-nodes", stepContentNodes},
	{"pages", stepPages},
	{"publish", stepPublish},
	{"welcome-post", stepWelcomePost},
}

func stepHub(sc *scaffoldContext, _ *catalog.HubTemplate) error {
	if sc.hubID != "" {
		return sc.step("hub", fmt.Sprintf("using existing hub %s (resume — no create)", sc.hubID),
			func() error { return nil })
	}

	detail := fmt.Sprintf("POST %s — create hub (identity only: name=%q slug=%q)",
		hubsPath(sc.teamID, ""), sc.nameOverride, sc.slugOverride)
	return sc.step("hub", detail, func() error {
		base := map[string]any{}
		if sc.nameOverride != "" {
			base["title"] = sc.nameOverride
		}
		if sc.slugOverride != "" {
			base["slug"] = sc.slugOverride
		}
		attrs, err := buildHubCreateAttrs(hubCreateParams{Base: base}, io.Discard)
		if err != nil {
			return err
		}
		res, err := sc.cl.Create(sc.ctx, hubsPath(sc.teamID, ""), attrs)
		if err != nil {
			return err
		}
		sc.hubID = res.ID
		sc.hubSlug, _ = res.Attributes["slug"].(string)
		sc.hubName, _ = res.Attributes["title"].(string)
		if sc.hubName == "" {
			sc.hubName = sc.nameOverride
		}
		if p, ok := res.Attributes["is_private"].(bool); ok {
			sc.isPrivate, sc.isPrivateKnown = p, true
		}
		noteSlugAssigned(sc)
		return nil
	})
}

func noteSlugAssigned(sc *scaffoldContext) {
	if sc.slugOverride == "" || sc.hubSlug == "" || sc.hubSlug == sc.slugOverride {
		return
	}
	sc.notef("backend assigned slug %q (requested %q — it was taken). This run created a NEW hub (%s); if you meant to resume the hub you already have, re-run with --hub <its id> instead", sc.hubSlug, sc.slugOverride, sc.hubID)
}

func scopeNavHrefs(nav map[string]any, slug string) {
	if slug == "" || nav == nil {
		return
	}
	prefix := "/" + slug
	for _, bucket := range []string{"header", "footer"} {
		items, ok := nav[bucket].([]any)
		if !ok {
			continue
		}
		for _, it := range items {
			obj, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := obj["type"].(string); t != "url" {
				continue
			}
			href, _ := obj["href"].(string)
			href = strings.TrimSpace(href)
			norm := strings.ReplaceAll(href, `\`, "/")
			if !strings.HasPrefix(norm, "/") || strings.HasPrefix(norm, "//") {
				continue
			}
			if hrefScopedToHub(href, slug) {
				continue
			}
			if href == "/" {
				obj["href"] = prefix
			} else {
				obj["href"] = prefix + href
			}
		}
	}
}

func stepBlobs(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if sc.fillGaps() {
		return stepBlobsFillGaps(sc, t)
	}
	detail := fmt.Sprintf("PATCH %s — branding+settings+navigation (strict keys)%s",
		hubsPath(sc.teamID, sc.hubIDOrPlaceholder()), sc.branding.planDetail())
	if sc.reapplyTemplate {
		detail += " — --reapply-template: the template's values overwrite the hub's, and its navigation replaces the hub's whole menu"
	}
	return sc.step("blobs", detail, func() error {
		nav, err := prepareTemplateNav(sc, t)
		if err != nil {
			return err
		}
		_, err = applyHubBlobs(sc.ctx, sc.cl, sc.teamID, sc.hubID, sc.hubSlug, blobPatches{
			Branding:     sc.branding.applyTo(t.Branding),
			Settings:     t.Settings,
			Navigation:   nav,
			SlugKnown:    true,
			Favicon:      sc.faviconOverride,
			Logo:         sc.logoOverride,
			Registration: sc.registrationOverride,
			Strict:       true,
		}, io.Discard)
		return scaffoldStrictKeyErr(err, scaffoldTemplateStrictKeyHint)
	})
}

func prepareTemplateNav(sc *scaffoldContext, t *catalog.HubTemplate) (map[string]any, error) {
	nav := catalog.CloneNode(t.Navigation)
	if nav == nil {
		return nil, nil
	}
	if err := catalog.InterpolateNavigation(nav, sc.hubName, sc.hubSlug); err != nil {
		return nil, errs.Wrap(errs.ExitUsage, err)
	}
	if err := validateNavigationBlob(nav); err != nil {
		return nil, err
	}
	scopeNavHrefs(nav, sc.hubSlug)
	return nav, nil
}

func stepSpaces(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Spaces) == 0 {
		return sc.step("spaces", "no spaces in template", func() error { return nil })
	}
	slugs := make([]string, len(t.Spaces))
	for i, s := range t.Spaces {
		slugs[i] = s.Slug
	}
	detail := fmt.Sprintf("GET+POST %s — create missing space(s) [%s] (skip-if-slug-exists, exhaustive)",
		spacesPath(sc.teamID, sc.hubIDOrPlaceholder(), ""), strings.Join(slugs, ", "))
	return sc.step("spaces", detail, func() error {
		existing, err := sc.existingSpacesBySlug()
		if err != nil {
			return err
		}
		for _, s := range t.Spaces {
			if _, exists := existing[s.Slug]; exists {
				continue
			}
			attrs, berr := buildSpaceAttrs(templateSpaceInput(s))
			if berr != nil {
				return berr
			}
			res, cerr := sc.cl.Create(sc.ctx, spacesPath(sc.teamID, sc.hubID, ""), attrs)
			if cerr != nil {
				return cerr
			}
			sc.spaceIDsBySlug[s.Slug] = res.ID
		}
		return nil
	})
}

func (sc *scaffoldContext) hubIDOrPlaceholder() string {
	if sc.hubID != "" {
		return sc.hubID
	}
	return "<hub_id>"
}

func (sc *scaffoldContext) existingSpacesBySlug() (map[string]string, error) {
	slugs := map[string]string{}
	seen := map[string]bool{}
	query := url.Values{}
	const maxPages = 1000
	for page := 0; page < maxPages; page++ {
		col, err := sc.cl.List(sc.ctx, spacesPath(sc.teamID, sc.hubID, ""), query)
		if err != nil {
			return nil, err
		}
		for _, r := range col.Data {
			if s, ok := r.Attributes["slug"].(string); ok && s != "" {
				slugs[s] = r.ID
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
	return slugs, nil
}

func nextPageCursor(col *client.Collection) string {
	page, ok := col.Meta["page"].(map[string]any)
	if !ok {
		return ""
	}
	if hasMore, present := page["has_more"].(bool); present && !hasMore {
		return ""
	}
	cur, _ := page["next_cursor"].(string)
	return cur
}

func templateSpaceInput(s catalog.TemplateSpace) SpaceInput {
	in := SpaceInput{Slug: &s.Slug}
	if s.Name != "" {
		in.Name = &s.Name
	}
	if s.Description != "" {
		in.Description = &s.Description
	}
	if s.AccessLevel != "" {
		in.AccessLevel = &s.AccessLevel
	}
	if s.PostingPermission != "" {
		in.PostingPermission = &s.PostingPermission
	}
	if s.Icon != "" {
		in.Icon = &s.Icon
	}
	return in
}

func stepOnboarding(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Onboarding) == 0 {
		return sc.step("onboarding", "no onboarding attributes in template", func() error { return nil })
	}
	slugs := make([]string, len(t.Onboarding))
	for i, d := range t.Onboarding {
		slugs[i] = d.Slug
	}
	detail := fmt.Sprintf("GET+POST %s then POST %s — create missing def(s) [%s] (skip-if-slug-exists, exhaustive) + enable on hub (definition_id in body, is_in_onboarding)",
		contactAttributesDefsPath(sc.teamID, ""),
		contactAttributesHubConfigPath(sc.teamID, sc.hubIDOrPlaceholder(), ""),
		strings.Join(slugs, ", "))
	if sc.fillGaps() {
		suffix, serr := onboardingFillPlanDetail(sc, t)
		if serr != nil {
			return serr
		}
		detail += suffix
	}
	return sc.step("onboarding", detail, func() error {
		existing, err := sc.existingContactAttrDefs()
		if err != nil {
			return err
		}
		var configs map[string]map[string]any
		if sc.fillGaps() {
			if configs, err = sc.existingHubConfigs(); err != nil {
				return err
			}
		}
		for _, d := range t.Onboarding {
			defID := existing[d.Slug]
			if defID == "" {
				attrs, berr := buildAttrDefCreateAttrs(templateAttrDefInput(d))
				if berr != nil {
					return berr
				}
				res, cerr := sc.cl.Create(sc.ctx, contactAttributesDefsPath(sc.teamID, ""), attrs)
				if cerr != nil {
					return cerr
				}
				defID = res.ID
			}
			sc.defIDsBySlug[d.Slug] = defID

			if cfg, has := configs[defID]; has {
				if kept := keptHubConfig(d, cfg); kept != "" {
					sc.notef("onboarding: kept the hub's existing %s — a --hub run fills gaps only; --reapply-template overwrites it.", kept)
				}
				continue
			}

			inOnboarding, required := d.InOnboarding, d.Required
			cfg := buildHubConfigAttrs(defID, HubConfigInput{
				IsInOnboarding: &inOnboarding,
				IsRequired:     &required,
			})
			if _, cerr := sc.cl.Create(sc.ctx, contactAttributesHubConfigPath(sc.teamID, sc.hubID, ""), cfg); cerr != nil {
				return cerr
			}
		}
		return nil
	})
}

func (sc *scaffoldContext) existingContactAttrDefs() (map[string]string, error) {
	bySlug := map[string]string{}
	seen := map[string]bool{}
	query := url.Values{}
	const maxPages = 1000
	for page := 0; page < maxPages; page++ {
		col, err := sc.cl.List(sc.ctx, contactAttributesDefsPath(sc.teamID, ""), query)
		if err != nil {
			return nil, err
		}
		for _, r := range col.Data {
			if s, ok := r.Attributes["slug"].(string); ok && s != "" {
				bySlug[s] = r.ID
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
	return bySlug, nil
}

func templateAttrDefInput(d catalog.TemplateAttrDef) AttrDefInput {
	in := AttrDefInput{Slug: &d.Slug, FieldType: &d.FieldType}
	if d.Name != "" {
		in.Name = &d.Name
	}
	return in
}

var policyTypeAliases = map[string]string{
	"tos":              "tos",
	"terms":            "tos",
	"terms_of_service": "tos",
	"privacy":          "privacy_policy",
	"privacy_policy":   "privacy_policy",
}

type templatePolicies struct {
	keys     []string
	policies []hubPolicy
	gate     *bool
}

func resolveTemplatePolicies(t *catalog.HubTemplate) (templatePolicies, error) {
	out := templatePolicies{keys: make([]string, 0, len(t.Policies))}
	if len(t.Policies) == 0 {
		return out, nil
	}
	for k := range t.Policies {
		out.keys = append(out.keys, k)
	}
	sort.Strings(out.keys)

	out.policies = make([]hubPolicy, 0, len(out.keys))
	for _, key := range out.keys {
		pol, err := templateHubPolicy(key, t.Policies[key])
		if err != nil {
			return templatePolicies{}, err
		}
		out.policies = append(out.policies, pol)
	}
	gate, err := scaffoldPolicyGate(t.Policies, out.keys)
	if err != nil {
		return templatePolicies{}, err
	}
	out.gate = gate
	return out, nil
}

func stepPolicies(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Policies) == 0 {
		return sc.step("policies", "no policies in template", func() error { return nil })
	}
	res, err := resolveTemplatePolicies(t)
	if err != nil {
		return err
	}

	var fill *policyFill
	if sc.fillGaps() {
		f := planPolicyFill(res, storedPolicies(sc.resumeHub))
		fill = &f
		detail := fmt.Sprintf("PATCH %s — fill gaps only (--hub: never resets a policy to the platform default, never replaces text the hub has): %s",
			hubsPoliciesPath(sc.teamID, sc.hubIDOrPlaceholder()), fill.describe())
		if serr := sc.step("policies", detail, func() error {
			cur, rerr := sc.cl.Retrieve(sc.ctx, hubsPath(sc.teamID, sc.hubID))
			if rerr != nil {
				return rerr
			}
			*fill = planPolicyFill(res, storedPolicies(cur.Attributes))
			for _, d := range fill.docs {
				if d.write() {
					if _, aerr := applyHubPolicies(sc.ctx, sc.cl, sc.teamID, sc.hubID, d.pol); aerr != nil {
						return aerr
					}
				}
				sc.notef("%s", d.note())
			}
			return nil
		}); serr != nil {
			return serr
		}
	} else {
		detail := fmt.Sprintf("PATCH %s — set policy(ies) [%s]",
			hubsPoliciesPath(sc.teamID, sc.hubIDOrPlaceholder()), strings.Join(res.keys, ", "))
		if serr := sc.step("policies", detail, func() error {
			for _, pol := range res.policies {
				if _, aerr := applyHubPolicies(sc.ctx, sc.cl, sc.teamID, sc.hubID, pol); aerr != nil {
					return aerr
				}
			}
			return nil
		}); serr != nil {
			return serr
		}
	}

	if res.gate == nil || !*res.gate {
		reason := policyGateSkipReason(res.gate)
		return sc.step("policies",
			"skipped — "+reason+"; enforcement gate not written (the hub's current setting stands)",
			func() error {
				sc.notef("policies: %s — enforcement gate NOT written; the hub's current setting stands (check or set it with `mio hubs policies gate %s --enabled`).",
					reason, sc.hubID)
				return nil
			})
	}

	if fill != nil && fill.gateSet {
		return sc.step("policies",
			fmt.Sprintf("skipped — the hub's enforcement gate is already set (enabled=%t) and a --hub run keeps it; gate not written", fill.gateValue),
			func() error {
				if fill.gateValue {
					sc.notef("policies: enforcement gate already enabled on the hub — not rewritten.")
					return nil
				}
				sc.notef("policies: kept the hub's enforcement gate (enabled=false) — the template declares enabled, but a --hub run does not flip a gate the hub already has; turn it on with `mio hubs policies gate %s --enabled`, or pass --reapply-template.",
					sc.hubID)
				return nil
			})
	}

	gateDetail := fmt.Sprintf("PATCH %s — enable policy enforcement (settings.policies.enabled=true, collapsed from the template's per-policy enabled)",
		hubsPoliciesGatePath(sc.teamID, sc.hubIDOrPlaceholder()))
	return sc.step("policies", gateDetail, func() error {
		if _, gerr := applyHubPolicyGate(sc.ctx, sc.cl, sc.teamID, sc.hubID, true); gerr != nil {
			return gerr
		}
		sc.policyGate = res.gate
		sc.notef("policies: enforcement gate set to enabled=true (settings.policies.enabled).")
		return nil
	})
}

func policyGateSkipReason(gate *bool) string {
	if gate == nil {
		return "no policy declares \"enabled\""
	}
	return "the template declares \"enabled\": false, which the applier contract does not act on"
}

func scaffoldPolicyGate(policies map[string]any, keys []string) (*bool, error) {
	var gate *bool
	var declaredBy string
	for _, key := range keys {
		val, _ := policies[key].(map[string]any)
		raw, present := val["enabled"]
		if !present {
			continue
		}
		enabled, ok := raw.(bool)
		if !ok {
			return nil, errs.New(errs.ExitUsage,
				"policy %q: \"enabled\" must be a JSON boolean, got %T", key, raw)
		}
		if gate != nil && *gate != enabled {
			return nil, errs.New(errs.ExitUsage,
				"policies[%q].enabled=%t conflicts with policies[%q].enabled=%t: policy enforcement is a single hub-level gate (settings.policies.enabled), not per-policy — make every policy that declares \"enabled\" agree",
				declaredBy, *gate, key, enabled)
		}
		gate, declaredBy = &enabled, key
	}
	return gate, nil
}

func templateHubPolicy(key string, raw any) (hubPolicy, error) {
	policyType, ok := policyTypeAliases[key]
	if !ok {
		return hubPolicy{}, errs.New(errs.ExitUsage,
			"invalid policy %q: must be one of tos, terms, privacy, privacy_policy", key)
	}
	val, ok := raw.(map[string]any)
	if !ok {
		return hubPolicy{}, errs.New(errs.ExitUsage,
			"policy %q must be an object, got %T", key, raw)
	}
	for f := range val {
		if !catalog.HubPolicyFieldKey(f) {
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: unknown field %q (allowed: %s)", key, f,
				strings.Join(catalog.HubPolicyFieldKeys(), ", "))
		}
	}

	p := hubPolicy{PolicyType: policyType}
	if c, present := val["content"]; present {
		content, ok := c.(string)
		if !ok {
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: \"content\" must be a string, got %T", key, c)
		}
		p.Content = &content
	}
	for _, f := range []string{"require_acceptance", "required"} {
		v, present := val[f]
		if !present {
			continue
		}
		ra, ok := v.(bool)
		if !ok {
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: %q must be a JSON boolean, got %T", key, f, v)
		}
		if p.RequireAcceptance != nil && *p.RequireAcceptance != ra {
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: \"require_acceptance\"=%t contradicts its alias \"required\"=%t — set one, or make them agree",
				key, *p.RequireAcceptance, ra)
		}
		p.RequireAcceptance = &ra
	}
	return p, nil
}

const scaffoldHubPlaylistVisibility = "public"

func stepPlaylists(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Playlists) == 0 {
		return sc.step("playlists", "no playlists in template", func() error { return nil })
	}
	keys := make([]string, len(t.Playlists))
	docs := 0
	for i, p := range t.Playlists {
		keys[i] = p.Key
		docs += len(p.Documents)
	}
	docDetail := ""
	if docs > 0 {
		docDetail = fmt.Sprintf(", register %d synthetic document file(s) at %s, publish each to the hub at %s and add each as an item",
			docs, syntheticFilesPath(sc.teamID), hubMediaPath(sc.teamID, sc.hubIDOrPlaceholder(), ""))
	}
	detail := fmt.Sprintf("GET %s (gate — skip all if the hub already has published playlists) else create playlist(s) [%s] scoped to the hub (hub_id), add items per file%s, and publish each to the hub (playlist_id in body, visibility=%s, published_at=now)",
		hubPlaylistsPath(sc.teamID, sc.hubIDOrPlaceholder(), ""), strings.Join(keys, ", "), docDetail, scaffoldHubPlaylistVisibility)
	return sc.step("playlists", detail, func() error {
		existing, err := sc.cl.List(sc.ctx, hubPlaylistsPath(sc.teamID, sc.hubID, ""), url.Values{})
		if err != nil {
			return err
		}
		if len(existing.Data) > 0 {
			return recoverPlaylistIDsForBindings(sc, t)
		}
		for _, p := range t.Playlists {
			hubID := sc.hubID
			in := PlaylistInput{Title: &p.Title, HubID: &hubID}
			if p.Visibility != "" {
				vis := p.Visibility
				in.Visibility = &vis
			}
			res, cerr := sc.cl.Create(sc.ctx, playlistsPath(sc.teamID, ""), buildPlaylistCreateAttrs(in))
			if cerr != nil {
				return cerr
			}
			playlistID := res.ID
			sc.playlistIDsByKey[p.Key] = playlistID

			fileIDs := append([]string(nil), p.FileIDs...)
			for _, d := range p.Documents {
				fileID, derr := createTemplateDocument(sc, p, d)
				if derr != nil {
					return derr
				}
				if perr := publishTemplateDocument(sc, fileID); perr != nil {
					return perr
				}
				fileIDs = append(fileIDs, fileID)
			}
			for i, fileID := range fileIDs {
				if _, ierr := sc.cl.Create(sc.ctx, playlistItemsPath(sc.teamID, playlistID, ""),
					map[string]any{"file_id": fileID, "position": i}); ierr != nil {
					return ierr
				}
			}

			pubAttrs, berr := buildHubMediaPublishAttrs(scaffoldHubPlaylistVisibility, time.Now().UTC(), nil)
			if berr != nil {
				return berr
			}
			pubAttrs["playlist_id"] = playlistID
			if _, perr := sc.cl.Create(sc.ctx, hubPlaylistsPath(sc.teamID, sc.hubID, ""), pubAttrs); perr != nil {
				return perr
			}
		}
		return nil
	})
}

func recoverPlaylistIDsForBindings(sc *scaffoldContext, t *catalog.HubTemplate) error {
	needed := map[string]bool{}
	for _, k := range playlistBindingKeys(sc) {
		needed[k] = true
	}
	if len(needed) == 0 {
		return nil
	}

	keyForTitle := map[string]string{}
	for _, p := range t.Playlists {
		if _, dup := keyForTitle[p.Title]; dup {
			keyForTitle[p.Title] = ""
			continue
		}
		keyForTitle[p.Title] = p.Key
	}

	idsForTitle := map[string][]string{}
	scopeOf := map[string]string{}
	query := url.Values{}
	seen := map[string]bool{}
	const maxPages = 1000
	for page := 0; page < maxPages; page++ {
		col, lerr := sc.cl.List(sc.ctx, hubPlaylistsPath(sc.teamID, sc.hubID, ""), query)
		if lerr != nil {
			return lerr
		}
		for _, row := range col.Data {
			playlistID, _ := row.Attributes["playlist_id"].(string)
			if playlistID == "" {
				continue
			}
			res, rerr := sc.cl.Retrieve(sc.ctx, playlistsPath(sc.teamID, playlistID))
			if rerr != nil {
				return rerr
			}
			title, _ := res.Attributes["title"].(string)
			idsForTitle[title] = append(idsForTitle[title], playlistID)
			scopeOf[playlistID], _ = res.Attributes["hub_id"].(string)
		}
		next := nextPageCursor(col)
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		query = url.Values{}
		query.Set("page[after]", next)
	}

	for title, key := range keyForTitle {
		if key == "" || !needed[key] {
			continue
		}
		ids := idsForTitle[title]
		if len(ids) != 1 {
			continue
		}
		sc.playlistIDsByKey[key] = ids[0]
		if scopeOf[ids[0]] != sc.hubID {
			if sc.playlistsNotInHub == nil {
				sc.playlistsNotInHub = map[string]bool{}
			}
			sc.playlistsNotInHub[ids[0]] = true
		}
		sc.notef("playlist %q recovered from the hub as %s (the playlists step skipped — this hub already has published playlists)", key, ids[0])
	}
	return nil
}

func unresolvedBindingKeys(sc *scaffoldContext) []string {
	var missing []string
	for _, k := range playlistBindingKeys(sc) {
		if sc.playlistIDsByKey[k] == "" {
			missing = append(missing, k)
		}
	}
	return missing
}

const templateDocumentMimeType = "text/markdown"

func createTemplateDocument(sc *scaffoldContext, p catalog.TemplatePlaylist, d catalog.TemplateDocument) (string, error) {
	kind := "document"
	mimeType := templateDocumentMimeType
	in := SyntheticFileInput{Title: d.Title, AssetKind: &kind, MimeType: &mimeType}
	if d.Description != "" {
		desc := d.Description
		in.Description = &desc
	}
	if p.Visibility != "" {
		vis := p.Visibility
		in.Visibility = &vis
	}
	attrs, berr := buildSyntheticFileAttrs(in)
	if berr != nil {
		return "", berr
	}
	res, cerr := sc.cl.Create(sc.ctx, syntheticFilesPath(sc.teamID), attrs)
	if cerr != nil {
		return "", cerr
	}
	return res.ID, nil
}

func publishTemplateDocument(sc *scaffoldContext, fileID string) error {
	attrs, berr := buildHubMediaPublishAttrs(scaffoldHubPlaylistVisibility, time.Now().UTC(), nil)
	if berr != nil {
		return berr
	}
	attrs["file_id"] = fileID
	_, err := sc.cl.Create(sc.ctx, hubMediaPath(sc.teamID, sc.hubID, ""), attrs)
	return err
}

func attrInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func stepPublish(sc *scaffoldContext, _ *catalog.HubTemplate) error {
	if !sc.publish {
		return sc.step("publish",
			"skipped — hub stays private (pass --publish to go live)",
			func() error { return nil })
	}
	detail := fmt.Sprintf("PATCH %s — set is_private=false (publish the hub public)",
		hubsPath(sc.teamID, sc.hubIDOrPlaceholder()))
	return sc.step("publish", detail, func() error {
		res, err := sc.cl.Update(sc.ctx, hubsPath(sc.teamID, sc.hubID), publishedStateAttrs(true))
		if err != nil {
			return err
		}
		sc.isPrivate, sc.isPrivateKnown = false, true
		if res != nil {
			if p, ok := res.Attributes["is_private"].(bool); ok {
				sc.isPrivate = p
			}
		}
		return nil
	})
}

func stepWelcomePost(sc *scaffoldContext, t *catalog.HubTemplate) error {
	wp := t.WelcomePost
	if wp == nil {
		return sc.step("welcome-post", "no welcome post in template", func() error { return nil })
	}
	title := strings.TrimSpace(wp.Title)
	detail := fmt.Sprintf("GET+POST %s — create welcome discussion %q in space %q (skip-if-title-exists)",
		discussionsAdminPath(sc.teamID, sc.hubIDOrPlaceholder(), ""), title, wp.Space)
	return sc.step("welcome-post", detail, func() error {
		spaceID := sc.spaceIDsBySlug[wp.Space]
		if spaceID == "" {
			spaces, lerr := sc.existingSpacesBySlug()
			if lerr != nil {
				return lerr
			}
			spaceID = spaces[wp.Space]
		}
		if spaceID == "" {
			return errs.New(errs.ExitGeneric,
				"welcome post: space %q was not found on hub %s (it should have been created by the spaces step)",
				wp.Space, sc.hubID)
		}

		existing, ferr := sc.findDiscussionByTitle(spaceID, title)
		if ferr != nil {
			return ferr
		}
		if existing != nil {
			sc.welcomePostID = existing.ID
			sc.welcomePostStatus = welcomePostAdopted
			if discussionSoftDeleted(*existing) {
				sc.welcomePostStatus = welcomePostAdoptedDeleted
				sc.notef("welcome post: %q already exists in space %q but is soft-deleted — left deleted (id %s)",
					title, wp.Space, existing.ID)
				return nil
			}
			sc.notef("welcome post: %q already exists in space %q — skipped (id %s)", title, wp.Space, existing.ID)
			return nil
		}

		attrs := map[string]any{
			"space_id":     spaceID,
			"title":        title,
			"is_published": wp.Published,
		}
		if wp.Body != "" {
			attrs["body"] = wp.Body
		}
		res, cerr := sc.cl.Create(sc.ctx, discussionsAdminPath(sc.teamID, sc.hubID, ""), attrs)
		if cerr != nil {
			return cerr
		}
		sc.welcomePostID = res.ID
		sc.welcomePostStatus = welcomePostCreated
		return nil
	})
}

const (
	welcomePostCreated        = "created"
	welcomePostAdopted        = "adopted"
	welcomePostAdoptedDeleted = "adopted_deleted"
)

func discussionSoftDeleted(r client.Resource) bool {
	v, ok := r.Attributes["deleted_at"]
	return ok && v != nil
}

func (sc *scaffoldContext) findDiscussionByTitle(spaceID, title string) (*client.Resource, error) {
	seen := map[string]bool{}
	cursor := ""
	const maxPages = 1000
	for page := 0; page < maxPages; page++ {
		query := url.Values{}
		query.Set("page[size]", "100")
		if cursor != "" {
			query.Set("page[after]", cursor)
		}
		col, err := sc.cl.List(sc.ctx, discussionsAdminPath(sc.teamID, sc.hubID, ""), query)
		if err != nil {
			return nil, err
		}
		for i, r := range col.Data {
			if s, _ := r.Attributes["space_id"].(string); s != spaceID {
				continue
			}
			if s, ok := r.Attributes["title"].(string); ok && strings.TrimSpace(s) == title {
				return &col.Data[i], nil
			}
		}
		next, nerr := discussionsNextCursor(col)
		if nerr != nil {
			return nil, nerr
		}
		if next == "" {
			return nil, nil
		}
		if seen[next] {
			return nil, errs.New(errs.ExitServer,
				"discussions list: the server repeated pagination cursor %q instead of advancing, so the scan "+
					"cannot be completed; refusing to continue rather than risk creating a duplicate welcome post. %s",
				next, welcomePostScanRecovery)
		}
		seen[next] = true
		cursor = next
	}
	return nil, errs.New(errs.ExitGeneric,
		"discussions list: gave up after %d pages without reaching the end of the hub's discussions, so the scan "+
			"cannot be completed; refusing to continue rather than risk creating a duplicate welcome post. %s",
		maxPages, welcomePostScanRecovery)
}

const welcomePostScanRecovery = "Create the welcome post by hand with " +
	"`mio community discussions create --hub <hub> --space-id <space> --title <title>` and re-run the scaffold: " +
	"it is the newest discussion, so the next scan finds it on the first page and adopts it instead of posting again."

func discussionsNextCursor(col *client.Collection) (string, error) {
	hasMore, present := col.Meta["has_more"].(bool)
	cur, _ := col.Meta["next_cursor"].(string)
	if present && hasMore && cur == "" {
		return "", errs.New(errs.ExitServer,
			"discussions list: the server reports more pages (meta.has_more=true) but returned no meta.next_cursor, "+
				"so the scan cannot be completed; refusing to continue rather than risk creating a duplicate welcome post "+
				"(the last row of a page has a null last_activity_at). %s", welcomePostScanRecovery)
	}
	if present && !hasMore {
		return "", nil
	}
	return cur, nil
}

var scaffoldAfterResolve func(*scaffoldContext)

var hubsScaffoldCmd = &cobra.Command{
	Use:   "scaffold",
	Short: "Scaffold a full-experience hub from a template.",
	Long: `Create a hub and apply a full-experience template — branding/logo/favicon,
navigation, registration, discussion spaces, onboarding schema, policies,
playlists and a homepage — by orchestrating the CLI's own request-builders and
client layer (strictly CLI-only; never raw REST).

Create mode (default) uses --name/--slug to create a new hub. Resume/target mode
uses --hub <id> to apply onto an existing hub (also how you resume after a
mid-pipeline failure). --dry-run prints the ordered plan and makes no changes.

A --hub run FILLS GAPS: it adds what the hub is missing and keeps what it
already has — branding keys, navigation buckets, settings (registration
included), policy text, the policy gate and onboarding config — reporting each
kept value on stderr and in the --dry-run plan. Flags on the command still win.
It never resets a policy to the platform default. --reapply-template lets the
template's values win instead (the policy gate is only ever turned on, never
off); it is destructive (prompts, or needs --yes off a TTY). Pages are never overwritten: on every --hub run,
--reapply-template and --dry-run included, a page conflict stops the run before
anything is written.

Brand the hub in the SAME command: --primary-color/--secondary-color/
--text-color/--background-color/--header-color/--header-accent, plus
--logo-url/--favicon-url/--social-image-url, each merge over the template's
branding block (they do not replace it). --branding-json merges a whole object
the same way, and the scalar flags win over the keys in it. --primary-color also
fills header_color unless you give a header color yourself.

Output follows --output: json (the default off a TTY) and plain emit the result
— the new hub's id/slug plus the page, space, onboarding and playlist ids the
run created — while table prints the human summary. Progress notes always go to
stderr, so a json stdout stays parseable.`,
	Example: `  mio hubs scaffold --template starter --name "My Community" --slug my-community
  mio hubs scaffold --template starter --name "My Community" --slug my-community --dry-run
  mio hubs scaffold --template starter --name Acme --slug acme --primary-color '#B91C1C' --secondary-color '#F59E0B'
  mio hubs scaffold --template starter --name Acme --slug acme --branding-json '{"primary":"#B91C1C","font_body":"Inter"}'
  mio hubs scaffold --template starter --hub hub_abc123
  mio hubs scaffold --template starter --hub hub_abc123 --reapply-template --yes
  HUB_ID=$(mio hubs scaffold --template starter --name "My Community" --slug my-community -o plain --jq .hub_id)`,
	Args: cobra.NoArgs,
	RunE: runHubsScaffold,
}

func runHubsScaffold(cmd *cobra.Command, _ []string) error {
	templateID, ferr := cmd.Flags().GetString("template")
	if ferr != nil {
		return errs.New(errs.ExitUsage, "--template: %s", ferr.Error())
	}

	branding, berr := resolveScaffoldBranding(cmd)
	if berr != nil {
		return berr
	}
	if uerr := validateScaffoldBrandingURLs(branding, changedString(cmd, "logo-url"), changedString(cmd, "favicon-url")); uerr != nil {
		return uerr
	}
	reapply, rerr := checkReapplyTemplate(cmd, templateID)
	if rerr != nil {
		return rerr
	}

	c, teamID, err := hubsContext(cmd)
	if err != nil {
		return err
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")

	nameOverride, _ := cmd.Flags().GetString("name")
	slugOverride, _ := cmd.Flags().GetString("slug")

	publish, _ := cmd.Flags().GetBool("publish")

	catalogOverride, _ := cmd.Flags().GetString("catalog")

	var plan []planEntry
	sc := &scaffoldContext{
		ctx:                  c.ctx,
		cl:                   c.client,
		teamID:               teamID,
		nameOverride:         nameOverride,
		slugOverride:         slugOverride,
		faviconOverride:      changedString(cmd, "favicon-url"),
		logoOverride:         changedString(cmd, "logo-url"),
		registrationOverride: changedBool(cmd, "registration-enabled"),
		branding:             branding,
		spaceIDsBySlug:       map[string]string{},
		defIDsBySlug:         map[string]string{},
		playlistIDsByKey:     map[string]string{},
		publish:              publish,
		catalogOverride:      strings.TrimSpace(catalogOverride),
		dryRun:               dryRun,
		plan:                 &plan,
		noteW:                cmd.ErrOrStderr(),
		resume:               flags.hub != "",
		reapplyTemplate:      reapply,
	}

	if flags.hub != "" {
		hubID, herr := c.client.ResolveHub(sc.ctx, teamID, flags.hub)
		if herr != nil {
			return errs.Wrap(errs.ExitUsage, herr)
		}
		res, rerr := c.client.Retrieve(sc.ctx, hubsPath(teamID, hubID))
		if rerr != nil {
			return rerr
		}
		sc.hubID = hubID
		sc.resumeHub = res.Attributes
		sc.hubSlug, _ = res.Attributes["slug"].(string)
		sc.hubName, _ = res.Attributes["title"].(string)
		if p, ok := res.Attributes["is_private"].(bool); ok {
			sc.isPrivate, sc.isPrivateKnown = p, true
		}
	}

	if scaffoldAfterResolve != nil {
		scaffoldAfterResolve(sc)
	}

	if perr := scaffoldPreflight(cmd, sc, templateID); perr != nil {
		return perr
	}

	opHandled, operr := maybeApplyViaHubOp(cmd, sc)
	if operr != nil {
		return errs.Wrap(errs.CodeOf(operr), scaffoldStepError(sc, "hub-op", operr))
	}

	for _, step := range scaffoldPipeline {
		if opHandled {
			break
		}
		if serr := step.run(sc, &sc.hubTmpl); serr != nil {
			printScaffoldRecovery(cmd.ErrOrStderr(), sc, templateID)
			return errs.Wrap(errs.CodeOf(serr), scaffoldStepError(sc, step.name, serr))
		}
	}

	if dryRun {
		if scaffoldHumanOutput(c) {
			printScaffoldPlan(cmd.OutOrStdout(), templateID, plan)
			return nil
		}
		return c.render(cmd, scaffoldPlanResult(templateID, plan))
	}
	if scaffoldHumanOutput(c) {
		printScaffoldSummary(cmd.OutOrStdout(), sc, &sc.hubTmpl, templateID)
		return nil
	}
	return c.render(cmd, scaffoldResult(sc, templateID))
}

func scaffoldHumanOutput(c *cmdContext) bool {
	return c.out.Format == output.FormatTable && c.out.JQ == ""
}

func scaffoldStepError(sc *scaffoldContext, step string, err error) error {
	if sc.hubID == "" {
		return fmt.Errorf("scaffold: step %q failed: %w", step, err)
	}
	if sc.resume {
		return fmt.Errorf("scaffold: step %q failed (target hub %s): %w", step, sc.hubID, err)
	}
	return fmt.Errorf("scaffold: step %q failed (hub %s was created and is NOT rolled back): %w",
		step, sc.hubID, err)
}

func printScaffoldPlan(w io.Writer, templateID string, plan []planEntry) {
	fmt.Fprintf(w, "Scaffold plan for template %q (dry-run — no changes made):\n", templateID)
	for i, e := range plan {
		if e.detail != "" {
			fmt.Fprintf(w, "  %d. %s — %s\n", i+1, e.step, e.detail)
		} else {
			fmt.Fprintf(w, "  %d. %s\n", i+1, e.step)
		}
	}
}

func printScaffoldRecovery(w io.Writer, sc *scaffoldContext, templateID string) {
	if sc.hubID == "" {
		return
	}
	if sc.resume {
		fmt.Fprintf(w, "Scaffold did not finish for target hub %s.\n", sc.hubID)
	} else {
		fmt.Fprintf(w, "Hub %s was created but the scaffold did not finish (no rollback).\n", sc.hubID)
	}
	parts := []string{"mio hubs scaffold", "--hub " + sc.hubID, "--template " + templateID}
	if sc.teamID != "" {
		parts = append(parts, "--team "+sc.teamID)
	}
	if sc.publish {
		parts = append(parts, "--publish")
	}
	if sc.faviconOverride != nil {
		parts = append(parts, fmt.Sprintf("--favicon-url %q", *sc.faviconOverride))
	}
	if sc.logoOverride != nil {
		parts = append(parts, fmt.Sprintf("--logo-url %q", *sc.logoOverride))
	}
	if sc.registrationOverride != nil {
		parts = append(parts, fmt.Sprintf("--registration-enabled=%t", *sc.registrationOverride))
	}
	if sc.reapplyTemplate {
		parts = append(parts, "--reapply-template")
	}
	parts = append(parts, sc.branding.flagArgs()...)
	fmt.Fprintf(w, "Resume with: %s\n", strings.Join(parts, " "))
}

func printScaffoldSummary(w io.Writer, sc *scaffoldContext, t *catalog.HubTemplate, templateID string) {
	fmt.Fprintf(w, "Scaffolded hub %q (id %s) from template %q.\n", sc.hubSlug, sc.hubID, templateID)

	parts := []string{
		fmt.Sprintf("%d space(s)", len(t.Spaces)),
		fmt.Sprintf("%d playlist(s)", len(t.Playlists)),
	}
	if len(t.Onboarding) > 0 {
		parts = append(parts, fmt.Sprintf("%d onboarding attribute(s)", len(t.Onboarding)))
	}
	if len(t.Policies) > 0 {
		parts = append(parts, fmt.Sprintf("%d policy(ies)", len(t.Policies)))
	}
	if len(t.Pages) > 0 {
		names := make([]string, len(t.Pages))
		for i, p := range t.Pages {
			names[i] = p.Slug
			if p.IsHomepage {
				names[i] += " (homepage)"
			}
		}
		parts = append(parts, "page(s): "+strings.Join(names, ", "))
	}
	fmt.Fprintf(w, "  Includes: %s.\n", strings.Join(parts, ", "))

	if t.WelcomePost != nil {
		switch sc.welcomePostStatus {
		case welcomePostCreated:
			fmt.Fprintf(w, "  Welcome post: created %q (id %s).\n", strings.TrimSpace(t.WelcomePost.Title), sc.welcomePostID)
		case welcomePostAdopted:
			fmt.Fprintf(w, "  Welcome post: already present (id %s) — not re-posted.\n", sc.welcomePostID)
		case welcomePostAdoptedDeleted:
			fmt.Fprintf(w, "  Welcome post: already present but SOFT-DELETED (id %s) — left deleted, not re-posted.\n", sc.welcomePostID)
		}
	}

	if !sc.branding.empty() {
		fmt.Fprintf(w, "  Branding overrides: %s.\n", sc.branding.describe())
	}

	published := sc.isPrivateKnown && !sc.isPrivate
	if published {
		fmt.Fprintln(w, "  State: LIVE — the hub is published (public).")
	} else {
		fmt.Fprintf(w, "  State: PRIVATE — the hub is not published yet. Publish with: mio hubs update %s --published (or re-run scaffold with --publish).\n", sc.hubID)
	}

	fmt.Fprintf(w, "  Public URL: <your-hub-frontend-host>/%s (the API does not return the hub's public URL; substitute your hub frontend host).\n", sc.hubSlug)
}

func scaffoldResult(sc *scaffoldContext, templateID string) map[string]any {
	t := &sc.hubTmpl

	pages := make([]any, 0, len(t.Pages))
	for _, p := range t.Pages {
		pages = append(pages, map[string]any{
			"slug":          p.Slug,
			"role":          p.Role,
			"page_template": p.PageTemplate,
			"is_homepage":   p.IsHomepage,
			"page_id":       nilIfEmpty(sc.pageIDsBySlug[p.Slug]),
		})
	}
	spaces := make([]any, 0, len(t.Spaces))
	for _, s := range t.Spaces {
		spaces = append(spaces, map[string]any{
			"slug":     s.Slug,
			"name":     s.Name,
			"space_id": nilIfEmpty(sc.spaceIDsBySlug[s.Slug]),
		})
	}
	onboarding := make([]any, 0, len(t.Onboarding))
	for _, d := range t.Onboarding {
		onboarding = append(onboarding, map[string]any{
			"slug":          d.Slug,
			"definition_id": nilIfEmpty(sc.defIDsBySlug[d.Slug]),
		})
	}
	playlists := make([]any, 0, len(t.Playlists))
	for _, p := range t.Playlists {
		playlists = append(playlists, map[string]any{
			"key":         p.Key,
			"title":       p.Title,
			"playlist_id": nilIfEmpty(sc.playlistIDsByKey[p.Key]),
		})
	}
	policies := make([]string, 0, len(t.Policies))
	for k := range t.Policies {
		policies = append(policies, k)
	}
	sort.Strings(policies)

	out := map[string]any{
		"dry_run":               false,
		"hub_id":                sc.hubID,
		"hub_slug":              sc.hubSlug,
		"hub_name":              sc.hubName,
		"hub_path":              nilIfEmpty(hubPublicPath(sc.hubSlug)),
		"published":             sc.isPrivateKnown && !sc.isPrivate,
		"template_id":           templateID,
		"catalog_revision":      nil,
		"branding_overrides":    sc.branding.resolved(),
		"homepage_page_id":      nilIfEmpty(scaffoldHomepageID(sc)),
		"welcome_post_id":       nilIfEmpty(sc.welcomePostID),
		"welcome_post_status":   nilIfEmpty(sc.welcomePostStatus),
		"pages":                 pages,
		"spaces":                spaces,
		"onboarding_attributes": onboarding,
		"playlists":             playlists,
		"policies":              policies,
		"policy_gate":           policyGateResult(sc.policyGate),
		"content_nodes":         contentNodesResult(sc),
	}
	if sc.cat != nil {
		out["catalog_revision"] = sc.cat.Meta.Revision
	}
	return out
}

func scaffoldHomepageID(sc *scaffoldContext) string {
	for _, p := range sc.hubTmpl.Pages {
		if p.IsHomepage {
			if id := sc.pageIDsBySlug[p.Slug]; id != "" {
				return id
			}
		}
	}
	return sc.homePageID
}

func hubPublicPath(slug string) string {
	if slug == "" {
		return ""
	}
	return "/" + slug
}

func policyGateResult(gate *bool) any {
	if gate == nil {
		return nil
	}
	return *gate
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scaffoldPlanResult(templateID string, plan []planEntry) map[string]any {
	steps := make([]any, 0, len(plan))
	for _, e := range plan {
		steps = append(steps, map[string]any{"step": e.step, "detail": e.detail})
	}
	return map[string]any{
		"dry_run":     true,
		"template_id": templateID,
		"steps":       steps,
	}
}

var hubsTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List the hub templates from the target backend's catalog.",
	Long: `List the hub templates that 'mio hubs scaffold --template <id>' can apply.

Templates live in the page-builder catalog served by the backend the CLI is
pointed at, so this command needs credentials and lists exactly what a
scaffold against that backend would see. --catalog <file> lists a local
artifact instead — the same escape hatch 'hubs scaffold' takes, so a catalog
branch's templates can be inspected before they are scaffolded.`,
	Example: `  mio hubs templates
  mio hubs templates --output json
  mio hubs templates --catalog ./catalog.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		override := strings.TrimSpace(flagValue(cmd, "catalog"))
		opts := catalog.ResolveOptions{
			OverrideFile: override,
			Mutating:     true,
			CacheDir:     catalogCacheDirFor(c.client.BaseURL()),
			Warnf:        catalogWarnf(cmd),
		}
		if override == "" {
			opts.Fetcher = catalogFetcher{c: c.client}
		}
		cat, src, rerr := catalog.Resolve(c.ctx, opts)
		if rerr != nil {
			return wrapCatalogResolveErr(rerr, override != "")
		}
		printCatalogProvenance(cmd, src, cat)
		if len(cat.HubTemplates) == 0 {
			return errNoHubTemplates(cat, src, override == "")
		}
		rows := make([]any, 0, len(cat.HubTemplates))
		for _, ht := range cat.HubTemplates {
			rows = append(rows, map[string]any{"id": ht.ID, "label": ht.Label, "pages": len(ht.Pages)})
		}
		return c.render(cmd, rows)
	},
}

func init() {
	hubsScaffoldCmd.Flags().String("template", "", "Template id to apply (e.g. starter; list them with `mio hubs templates list`). Required.")
	hubsScaffoldCmd.Flags().String("name", "", "Display name for the new hub (create mode).")
	hubsScaffoldCmd.Flags().String("slug", "", "URL slug for the new hub (create mode).")
	hubsScaffoldCmd.Flags().Bool("dry-run", false, "Print the ordered plan and make no changes.")
	_ = hubsScaffoldCmd.MarkFlagRequired("template")

	hubsScaffoldCmd.Flags().String("catalog", "",
		"Path to a catalog.json file to scaffold from instead of the backend's live catalog (digest-verified; fails closed on mismatch).")

	hubsScaffoldCmd.Flags().Bool("publish", false,
		"Publish the hub as the final step (go live). Default off — the hub stays private so you can review first.")
	hubsScaffoldCmd.Flags().String("favicon-url", "", "Override the template's branding.favicon_url.")
	hubsScaffoldCmd.Flags().String("logo-url", "", "Override the template's branding.logo_url.")
	hubsScaffoldCmd.Flags().Bool("registration-enabled", false, "Override the template's settings.registration.enabled.")

	hubsScaffoldCmd.Flags().Bool("reapply-template", false,
		"With --hub: overwrite the hub's branding, navigation, settings (registration included), policy text and onboarding config with the template's values, and turn the policy gate on when the template enables it (never off), instead of only filling what is missing. Destructive: prompts, or needs --yes off a TTY. A usage error without --hub.")

	registerScaffoldBrandingFlags(hubsScaffoldCmd)

	hubsTemplatesCmd.Flags().String("catalog", "",
		"Path to a catalog.json file to list templates from instead of the backend's live catalog (digest-verified; fails closed on mismatch).")

	hubsCmd.AddCommand(hubsScaffoldCmd)
	hubsCmd.AddCommand(hubsTemplatesCmd)
}
