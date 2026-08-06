// hubtemplate.go — the typed model over the catalog's hubTemplates[] (schema
// 2.1): the declarative full-experience hub definition the `hubs scaffold`
// pipeline applies. A HubTemplate carries the hub's four untyped JSONB blobs
// (branding/navigation/settings/policies) verbatim plus typed per-resource
// lists (spaces, onboarding defs, playlists, pages). Validate enforces the
// invariants BOTH scaffold apply paths depend on — enum membership mirrors the
// CLI's own command validators, slug/key uniqueness protects the pipeline's
// snapshot-once skip-if-exists steps, and every page must reference a real
// page template in the same catalog. ApplicationID and TreeDigest are the
// provenance primitives the scaffold's markers are built from.
//
// Parsing is TOLERANT (the schema is additionalProperties:true): unknown keys
// are ignored so an older CLI keeps working when the catalog adds fields.

package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Allowed enum sets, verified against the CLI's own command validators so a
// hub template can never carry a value the individual commands would reject:
//   - hubSpaceAccessLevels / hubSpacePostingPermissions: cmd/community_spaces.go setSpaceWriteAttrs
//   - hubPagePrivacyValues:                              cmd/pages_write.go setPageWriteAttrs
//   - hubPlaylistVisibilityValues:                       cmd/media.go applyHubMediaOptions
//   - hubAttrFieldTypes:                                 cmd/contactattributes.go (field-type help)
var (
	hubSpaceAccessLevels        = map[string]bool{"public": true, "restricted": true}
	hubSpacePostingPermissions  = map[string]bool{"any_member": true, "admins_only": true, "segment": true}
	hubPagePrivacyValues        = map[string]bool{"public": true, "members": true, "private": true}
	hubPlaylistVisibilityValues = map[string]bool{"members": true, "private": true, "public": true}
	hubAttrFieldTypes           = map[string]bool{"text": true, "number": true, "boolean": true, "date": true, "multiple": true}
	// hubPolicyFieldKeys is the accepted field set for each policies value: the
	// fields the scaffold's policy step reads (content + require_acceptance and
	// its friendly alias required — cmd templateHubPolicy) plus "enabled", which
	// the ratified 2.1 artifact carries (the pre-2.1 allow-list would have
	// rejected it). An unknown field — e.g. a typoed "require_acceptence" in a
	// hand-crafted --catalog artifact — must fail preflight, because the step
	// would otherwise ignore it and send content:null, silently RESETTING the
	// policy instead of failing loud.
	hubPolicyFieldKeys = map[string]bool{"content": true, "require_acceptance": true, "required": true, "enabled": true}
)

// HubPolicyFieldKey reports whether f is an accepted field inside a hubTemplate
// policies value. Exported so the CONSUMER (cmd templateHubPolicy) enforces
// THIS allow-list rather than keeping a second copy of it — two lists that must
// be edited together are two lists that will eventually disagree.
func HubPolicyFieldKey(f string) bool { return hubPolicyFieldKeys[f] }

// HubPolicyFieldKeys returns the accepted field set for a hubTemplate policies
// value, sorted.
//
// Exported for the CONSUMER-COVERAGE guard (MIO-2567): accepting a field at
// preflight only matters if something downstream ACTS on it, and "enabled" sat
// on this allow-list — shipped in the community template, waved through by
// Validate — while the scaffold's policy step read only content/
// require_acceptance/required. The result was a hub whose ToS was written and
// whose gate was never switched on. The cmd-side guard drives a template
// declaring each key here through the real step and asserts the REQUESTS
// change: membership in a second hand-maintained list proves nothing, because
// adding the key to that list is also the cheapest way to make such a test go
// green while the drop survives.
func HubPolicyFieldKeys() []string {
	keys := make([]string, 0, len(hubPolicyFieldKeys))
	for k := range hubPolicyFieldKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// PageRef is one hubTemplate pages[] entry: a page to instantiate from a page
// template. Slug/Title/Privacy feed the page create; IsHomepage marks the one
// entry the scaffold publishes as the hub's homepage.
type PageRef struct {
	Role, PageTemplate, Slug, Title, Privacy string
	IsHomepage                               bool
}

// TemplateSpace is a community discussion space to create.
type TemplateSpace struct{ Name, Slug, Description, AccessLevel, PostingPermission string }

// TemplateAttrDef is a contact-attribute definition, optionally surfaced in
// onboarding.
type TemplateAttrDef struct {
	Name, Slug, FieldType  string
	InOnboarding, Required bool
}

// TemplatePlaylist is a media playlist published onto the hub (mirrors the
// retired internal/hubtemplate Playlist shape).
type TemplatePlaylist struct {
	Title, Key, Visibility string
	FileIDs                []string
}

// DiscussionTitleMaxCP mirrors mio-backend's DISCUSSION_TITLE_MAX_LENGTH
// (app/community/discussion_text.py) — the cap the create endpoint enforces
// twice over, via Field(max_length=280) and normalize_discussion_title. Both
// count CODE POINTS (Python len() over a str), never bytes.
//
// The backend applies it to the RAW value because that is what it receives;
// HubTemplate.Validate applies it to the STRIPPED one because that is what the
// scaffold sends. See the reject conditions there for why the two differ.
const DiscussionTitleMaxCP = 280

// TemplateWelcomePost is the optional `welcomePost` block: the first discussion
// a scaffolded community lands in one of its own spaces (MIO-2558), authored via
// the admin welcome-post endpoint (MIO-2262). Space is a SLUG referencing this
// template's own spaces[] — the scaffold resolves it to the hub's real space id
// at apply time, so the manifest stays id-free and reusable.
//
// The four fields ARE the endpoint's whole attribute set (its request schema is
// extra="forbid"); notably there is no author field, because the author is
// derived server-side from the caller's credentials.
//
// NOT RATIFIED CATALOG VOCABULARY — MIO-2812 (read this before relying on the
// key name or the field spellings). `welcomePost` does not appear in
// mio-page-catalog's catalog.schema.json — $defs/hubTemplate declares no such
// property, nothing in that repo mentions it, and neither the TS reference
// applier nor the backend's W2b scaffold-from-template op implements it. It
// parses today only because $defs/hubTemplate is additionalProperties:true, i.e.
// the CLI defined this vocabulary unilaterally and the schema is not in a
// position to disagree. MIO-2812 asks the catalog owners to ratify it.
//
// Both failure modes are SILENT, which is the whole reason this is worth a
// ticket rather than a TODO:
//
//   - if the ratified spec lands with a different name or shape (`welcome_post`,
//     the block nested under spaces[], `body` renamed), this parser sees no
//     `welcomePost` key, the step takes its no-declaration branch, and the post
//     simply never appears — nothing errors, nothing warns;
//   - additionalProperties:true also means the schema cannot catch a TYPO, so a
//     template author who writes `welcomepost` gets neither a validation error
//     from the catalog nor a post from the scaffold.
//
// The one mitigation available here is that the no-declaration branch records a
// plan-VISIBLE entry ("no welcome post in template") rather than skipping
// silently, so `--dry-run` at least shows an operator that the step ran and
// found nothing to do. When MIO-2812 lands, BOTH this parser and stepWelcomePost
// move with it.
//
// Title/Body are LITERAL: {{hub_name}}/{{hub_slug}} interpolation is a closed
// contract whose scanned locations are exhaustively specified (MIO-2573 §4.3:
// leaf values on headline/text/button nodes, page titles, nav labels — "nothing
// else"), and it is implemented three times over in Go/TS/Python against a
// shared corpus. A welcome post is not one of those locations, so a token here
// would be stored verbatim — exactly like the equally literal spaces[].name,
// playlists[].title and policies[].content. Widening §4.3 is a catalog-spec
// change, not something the CLI may do unilaterally — which is why "should
// title/body join the §4.3 set?" is an open question ON MIO-2812 with this
// reasoning recorded as the CLI's position, not a decision taken here. If the
// catalog owners say yes, interpolating them is a follow-up on this repo.
type TemplateWelcomePost struct {
	Space, Title, Body string
	// Published mirrors the endpoint's is_published, whose server-side default is
	// TRUE. A template that omits the key therefore means "publish it" — hence the
	// explicit default at parse time rather than the bool zero value, which would
	// silently scaffold every welcome post as an invisible draft. A present but
	// NON-BOOL value (null, "true", 1) is treated as absent for the same reason:
	// coercing it through a comma-ok bool assertion yields false and lands exactly
	// the invisible draft the default exists to prevent. Type-policing a
	// schema-validated artifact belongs to the catalog's own validator, so this
	// stays a tolerant parse (the package-wide convention) that fails SAFE.
	Published bool
}

// HubTemplate is one hubTemplates[] entry: a declarative full-experience hub
// definition. The four map[string]any blobs mirror the hub's untyped JSONB
// blobs; the typed slices carry the per-resource inputs each pipeline step
// consumes.
type HubTemplate struct {
	ID, Label, Lifecycle                     string
	Branding, Navigation, Settings, Policies map[string]any
	Spaces                                   []TemplateSpace
	Onboarding                               []TemplateAttrDef
	Playlists                                []TemplatePlaylist
	Pages                                    []PageRef
	// WelcomePost is nil when the template declares no `welcomePost` — which is
	// the case for every catalog shipped so far, including `community` at catalog
	// 0.14.1 (its keys are branding/id/label/lifecycle/navigation/onboarding/
	// pages/playlists/policies/settings/spaces). The scaffold step is then a
	// clean no-op: the CLI holds no templates and must not invent post copy.
	WelcomePost *TemplateWelcomePost
}

// parseHubTemplate maps one raw hubTemplates[] entry onto the typed model.
// Tolerant by construction: unknown keys are ignored, missing keys zero-value.
// `requires` is deliberately NOT parsed: v7 removed the capability model; the
// backend serving the catalog is the authority.
func parseHubTemplate(n Node) HubTemplate {
	h := HubTemplate{
		ID:         str(n["id"]),
		Label:      str(n["label"]),
		Lifecycle:  str(n["lifecycle"]),
		Branding:   asNode(n["branding"]),
		Navigation: asNode(n["navigation"]),
		Settings:   asNode(n["settings"]),
		Policies:   asNode(n["policies"]),
	}
	for _, v := range slice(n["spaces"]) {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		h.Spaces = append(h.Spaces, TemplateSpace{
			Name:              str(m["name"]),
			Slug:              str(m["slug"]),
			Description:       str(m["description"]),
			AccessLevel:       str(m["access_level"]),
			PostingPermission: str(m["posting_permission"]),
		})
	}
	for _, v := range slice(n["onboarding"]) {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		h.Onboarding = append(h.Onboarding, TemplateAttrDef{
			Name:         str(m["name"]),
			Slug:         str(m["slug"]),
			FieldType:    str(m["field_type"]),
			InOnboarding: boolVal(m["in_onboarding"]),
			Required:     boolVal(m["required"]),
		})
	}
	for _, v := range slice(n["playlists"]) {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		h.Playlists = append(h.Playlists, TemplatePlaylist{
			Title:      str(m["title"]),
			Key:        str(m["key"]),
			Visibility: str(m["visibility"]),
			FileIDs:    strSlice(m["file_ids"]),
		})
	}
	// welcomePost (MIO-2558): OPTIONAL, so an absent key leaves WelcomePost nil
	// and the scaffold's welcome-post step a no-op. is_published defaults to the
	// endpoint's own server-side default (true) when the key is absent.
	if wp := asNode(n["welcomePost"]); wp != nil {
		post := &TemplateWelcomePost{
			Space:     str(wp["space"]),
			Title:     str(wp["title"]),
			Body:      str(wp["body"]),
			Published: true,
		}
		// Only a REAL bool moves this off the endpoint's own default. boolVal's
		// comma-ok assertion turns null/"true"/1 into false, which would scaffold an
		// invisible draft from a value that never said "draft" — fail safe instead.
		if b, ok := wp["is_published"].(bool); ok {
			post.Published = b
		}
		h.WelcomePost = post
	}
	for _, v := range slice(n["pages"]) {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		h.Pages = append(h.Pages, PageRef{
			Role:         str(m["role"]),
			PageTemplate: str(m["pageTemplate"]),
			Slug:         str(m["slug"]),
			Title:        str(m["title"]),
			Privacy:      str(m["privacy"]),
			IsHomepage:   boolVal(m["isHomepage"]),
		})
	}
	return h
}

// Validate enforces the hub-template invariants both scaffold apply paths
// depend on: pages non-empty with unique non-empty slugs (the backend reserves
// "home"), a valid privacy on every page, every pageTemplate resolving to a
// page template in c, and exactly one homepage; space/onboarding slugs unique
// and non-empty with enum-valid attributes; an optional welcomePost whose title
// clears the endpoint's own reject conditions (non-blank, NUL-free, and ≤280
// code points measured on the STRIPPED title — what the scaffold actually posts),
// whose body is NUL-free, and whose space slug names one of this
// template's own spaces; every policies value an object
// whose fields are within hubPolicyFieldKeys (a typo must fail preflight, not
// silently reset policy content); playlist titles non-empty and keys unique
// and non-empty with enum-valid visibility. Slug/key uniqueness matters
// because each pipeline step snapshots existing server slugs ONCE and
// skip-if-exists against that snapshot — a duplicate would issue a duplicate
// create mid-pipeline.
func (h HubTemplate) Validate(c *Catalog) error {
	if len(h.Pages) == 0 {
		return fmt.Errorf("hub template %q: pages must not be empty", h.ID)
	}
	homepages := 0
	seenPageSlugs := map[string]bool{}
	for i, p := range h.Pages {
		if p.Slug == "" {
			return fmt.Errorf("hub template %q: pages[%d] missing slug", h.ID, i)
		}
		if p.Slug == "home" {
			return fmt.Errorf("hub template %q: pages[%d] slug \"home\" is backend-reserved", h.ID, i)
		}
		if seenPageSlugs[p.Slug] {
			return fmt.Errorf("hub template %q: pages[%d] duplicate slug %q", h.ID, i, p.Slug)
		}
		seenPageSlugs[p.Slug] = true
		if !hubPagePrivacyValues[p.Privacy] {
			return fmt.Errorf("hub template %q: pages[%d] invalid privacy %q", h.ID, i, p.Privacy)
		}
		tmpl, ok := c.TemplateByID(p.PageTemplate)
		if !ok {
			return fmt.Errorf("hub template %q: pages[%d] unknown pageTemplate %q", h.ID, i, p.PageTemplate)
		}
		if !tmpl.IsPage {
			return fmt.Errorf("hub template %q: pages[%d] pageTemplate %q is not a page template", h.ID, i, p.PageTemplate)
		}
		if p.IsHomepage {
			homepages++
		}
	}
	if homepages != 1 {
		return fmt.Errorf("hub template %q: exactly one page must set isHomepage, got %d", h.ID, homepages)
	}
	seenSpaceSlugs := map[string]bool{}
	for i, s := range h.Spaces {
		if s.Slug == "" {
			return fmt.Errorf("hub template %q: spaces[%d] missing slug", h.ID, i)
		}
		if seenSpaceSlugs[s.Slug] {
			return fmt.Errorf("hub template %q: spaces[%d] duplicate slug %q", h.ID, i, s.Slug)
		}
		seenSpaceSlugs[s.Slug] = true
		if s.AccessLevel != "" && !hubSpaceAccessLevels[s.AccessLevel] {
			return fmt.Errorf("hub template %q: spaces[%d] invalid access_level %q", h.ID, i, s.AccessLevel)
		}
		if s.PostingPermission != "" && !hubSpacePostingPermissions[s.PostingPermission] {
			return fmt.Errorf("hub template %q: spaces[%d] invalid posting_permission %q", h.ID, i, s.PostingPermission)
		}
	}
	seenDefSlugs := map[string]bool{}
	for i, d := range h.Onboarding {
		if d.Slug == "" {
			return fmt.Errorf("hub template %q: onboarding[%d] missing slug", h.ID, i)
		}
		if seenDefSlugs[d.Slug] {
			return fmt.Errorf("hub template %q: onboarding[%d] duplicate slug %q", h.ID, i, d.Slug)
		}
		seenDefSlugs[d.Slug] = true
		if !hubAttrFieldTypes[d.FieldType] {
			return fmt.Errorf("hub template %q: onboarding[%d] invalid field_type %q", h.ID, i, d.FieldType)
		}
	}
	for k, v := range h.Policies {
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("hub template %q: policies[%q] must be an object", h.ID, k)
		}
		for f := range obj {
			if !hubPolicyFieldKeys[f] {
				return fmt.Errorf("hub template %q: policies[%q] unknown field %q (allowed: content, require_acceptance, required, enabled)", h.ID, k, f)
			}
		}
	}
	// welcomePost (MIO-2558). These checks exist because the welcome-post step runs
	// LAST, after the hub, its blobs, spaces, pages and the publish PATCH have all
	// been written: anything the endpoint would 422 on, discovered there, fails a
	// run that has already mutated everything else and cannot roll back. Preflight
	// is write-free, so catching it here costs nothing and rolls back nothing.
	//
	// This mirrors the ENDPOINT's reject conditions deliberately, and that is not a
	// breach of the conduit rule (`cmd/community.go`: don't second-guess the API on
	// the user's --title). Different input, different rule: a template constant is
	// an artifact this code already validates field-by-field before writing, and
	// the whole point of preflight is that a malformed template fails before, not
	// after, the hub exists. Mirrored from mio-backend
	// app/community/discussion_text.py (normalize_discussion_title +
	// validate_discussion_body, read 2026-07-29) — blank-after-strip, NUL, and an
	// over-cap length for the title; NUL for the optional body, which has no
	// length cap and is never stripped there.
	//
	// The length is measured on the STRIPPED title, deliberately diverging from the
	// backend helper, which measures the RAW one. The backend measures what it
	// RECEIVES; this validates what the scaffold will SEND, and stepWelcomePost
	// posts strings.TrimSpace(Title) precisely so the request matches what the
	// server stores. Measuring raw here would fail preflight on a 285-raw /
	// 275-stripped title whose actual request body — 275 characters — the API
	// accepts, i.e. it would be stricter than the endpoint for no reason.
	//
	// It is a CODE-POINT count, not len(): Python's len() and Pydantic's
	// Field(max_length=280) both count code points, so a byte-based check would
	// reject titles the API accepts (280 accented characters are 560 bytes).
	if wp := h.WelcomePost; wp != nil {
		title := strings.TrimSpace(wp.Title)
		switch {
		case wp.Title == "":
			return fmt.Errorf("hub template %q: welcomePost missing title", h.ID)
		case title == "":
			return fmt.Errorf("hub template %q: welcomePost title is whitespace-only (the API rejects it)", h.ID)
		case strings.ContainsRune(wp.Title, 0):
			return fmt.Errorf("hub template %q: welcomePost title contains a NUL byte", h.ID)
		case utf8.RuneCountInString(title) > DiscussionTitleMaxCP:
			return fmt.Errorf("hub template %q: welcomePost title is %d code points, over the %d-character limit",
				h.ID, utf8.RuneCountInString(title), DiscussionTitleMaxCP)
		case strings.ContainsRune(wp.Body, 0):
			return fmt.Errorf("hub template %q: welcomePost body contains a NUL byte", h.ID)
		case wp.Space == "":
			return fmt.Errorf("hub template %q: welcomePost missing space (the slug of one of this template's spaces)", h.ID)
		// The step converges on the hub's REAL spaces, so the ref must name a space
		// the template itself declares — the only one the scaffold guarantees exists.
		case !seenSpaceSlugs[wp.Space]:
			return fmt.Errorf("hub template %q: welcomePost space %q is not one of this template's spaces", h.ID, wp.Space)
		}
	}
	seenPlaylistKeys := map[string]bool{}
	for i, p := range h.Playlists {
		if p.Title == "" {
			return fmt.Errorf("hub template %q: playlists[%d] missing title", h.ID, i)
		}
		if p.Key == "" {
			return fmt.Errorf("hub template %q: playlists[%d] missing key", h.ID, i)
		}
		if seenPlaylistKeys[p.Key] {
			return fmt.Errorf("hub template %q: playlists[%d] duplicate key %q", h.ID, i, p.Key)
		}
		seenPlaylistKeys[p.Key] = true
		if p.Visibility != "" && !hubPlaylistVisibilityValues[p.Visibility] {
			return fmt.Errorf("hub template %q: playlists[%d] invalid visibility %q", h.ID, i, p.Visibility)
		}
	}
	return nil
}

// HubTemplateByID returns the hub template with the given id, if any.
func (c *Catalog) HubTemplateByID(id string) (HubTemplate, bool) {
	for _, h := range c.HubTemplates {
		if h.ID == id {
			return h, true
		}
	}
	return HubTemplate{}, false
}

// HubTemplateIDs returns every hub template id, sorted (stable help output).
func (c *Catalog) HubTemplateIDs() []string {
	ids := make([]string, 0, len(c.HubTemplates))
	for _, h := range c.HubTemplates {
		ids = append(ids, h.ID)
	}
	sort.Strings(ids)
	return ids
}

// HomepagePage returns a pointer to the pages[] entry marked isHomepage (into
// h.Pages), or nil if none is marked.
func (h HubTemplate) HomepagePage() *PageRef {
	for i := range h.Pages {
		if h.Pages[i].IsHomepage {
			return &h.Pages[i]
		}
	}
	return nil
}

// ApplicationID is the deterministic provenance id shared with the backend op:
// sha256hex(hub_id + "\x1f" + hub_template_id). A re-run recomputes it without
// any server-side record and can locate this application's pages.
func ApplicationID(hubID, hubTemplateID string) string {
	sum := sha256.Sum256([]byte(hubID + "\x1f" + hubTemplateID))
	return hex.EncodeToString(sum[:])
}

// CreateApplicationID is ApplicationID's CREATE-mode counterpart (MIO-2976):
// the deterministic Idempotency-Key for the whole-hub op, which runs before any
// hub id exists and so cannot key off one.
//
// It covers exactly the identity the operator typed — team, template, name,
// slug — so re-running the SAME command converges on the backend's stored
// application instead of creating a second hub (MIO-2565), while two different
// hubs from one template stay distinct keys. Both name and slug are folded in,
// not slug alone: the backend puts both in its request fingerprint, so a key
// that ignored name would turn `--name Other --slug same` into a fingerprint
// mismatch (409) rather than the distinct application it is.
//
// Note what is deliberately NOT in the key but IS in the backend's fingerprint:
// the catalog digest and the overrides. A re-run after the backend's catalog pin
// moves, or with a different --publish, therefore reuses this key with a changed
// body and gets 409 `idempotency_fingerprint_mismatch` — refusing to half-apply
// a different request under an old key. That is the safe direction, and the
// caller turns it into actionable guidance rather than retrying.
func CreateApplicationID(teamID, hubTemplateID, name, slug string) string {
	sum := sha256.Sum256([]byte(teamID + "\x1f" + hubTemplateID + "\x1f" + name + "\x1f" + slug))
	return hex.EncodeToString(sum[:])
}

// TreeDigest returns "sha256:<hex>" over the canonical tree — the provenance
// marker's appliedTreeDigest (§5.1). Written and read back only by the CLI's
// client-side path in v1 (§10.9 cross-language digest reconciliation pending).
func TreeDigest(tree map[string]any) (string, error) {
	b, err := CanonicalJSON(tree)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CloneNode returns a deep copy of a node tree (maps/slices/scalars). A nil
// input stays nil — Node is a map alias, so without the guard a typed nil map
// would match deepClone's map case and come back as an allocated EMPTY map,
// turning callers' "was there a blob at all?" nil checks always-true (the
// scaffold once PATCHed navigation:{} — a whole-blob wipe — because of this).
func CloneNode(n Node) Node {
	if n == nil {
		return nil
	}
	c, _ := deepClone(n).(Node)
	return c
}
