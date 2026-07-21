// Package hubtemplate loads and validates the declarative "hub template" that
// the (future) `hubs scaffold` command applies to build a full-experience hub —
// branding/favicon, navigation, registration, spaces, onboarding schema,
// policies, playlists, and a homepage. Templates ship embedded in the binary
// (//go:embed hubtemplates/*.json); there is no backend endpoint to fetch them
// from and no upstream they can drift from, so this package deliberately carries
// none of internal/catalog's live-fetch/digest/parity machinery — just the
// struct, the embedded loader, and schema validation.
//
// The package is pure: no cobra, no client, no HTTP. It only turns a template id
// into a validated *Template (or an error).
package hubtemplate

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed hubtemplates/*.json
var templatesFS embed.FS

// Allowed enum sets, verified against the CLI's own command validators so a
// template can never carry a value the individual commands would reject:
//   - spaceAccessLevels / spacePostingPermissions: cmd/community_spaces.go setSpaceWriteAttrs
//   - pagePrivacyValues:                           cmd/pages_write.go setPageWriteAttrs
//   - hubMediaVisibilityValues:                    cmd/media.go applyHubMediaOptions
//   - attrFieldTypes:                              cmd/contactattributes.go (field-type help)
var (
	spaceAccessLevels        = map[string]bool{"public": true, "restricted": true}
	spacePostingPermissions  = map[string]bool{"any_member": true, "admins_only": true, "segment": true}
	pagePrivacyValues        = map[string]bool{"public": true, "members": true, "private": true}
	hubMediaVisibilityValues = map[string]bool{"members": true, "private": true, "public": true}
	attrFieldTypes           = map[string]bool{"text": true, "number": true, "boolean": true, "date": true, "multiple": true}
)

// Template is a declarative full-experience hub definition. The four map[string]any
// blobs (Branding, Navigation, Settings, Policies) mirror the hub's untyped JSONB
// blobs; the typed slices carry the per-resource inputs each pipeline step consumes.
type Template struct {
	ID         string         `json:"id"`
	Branding   map[string]any `json:"branding,omitempty"`   // logo_url, favicon_url, colors → branding blob
	Navigation map[string]any `json:"navigation,omitempty"` // header/footer menu (typed items) → REPLACE
	Settings   map[string]any `json:"settings,omitempty"`   // registration.enabled, discussions.enabled, …
	Policies   map[string]any `json:"policies,omitempty"`
	Spaces     []Space        `json:"spaces,omitempty"`
	Onboarding []AttrDef      `json:"onboarding,omitempty"` // contact-attribute defs, optionally in onboarding
	Playlists  []Playlist     `json:"playlists,omitempty"`
	Homepage   *HomepageRef   `json:"homepage,omitempty"`
}

// Space is a community discussion space. AccessLevel and PostingPermission are
// optional; when set they are validated against their enums.
type Space struct {
	Name              string `json:"name,omitempty"`
	Slug              string `json:"slug"`
	Description       string `json:"description,omitempty"`
	AccessLevel       string `json:"access_level,omitempty"`
	PostingPermission string `json:"posting_permission,omitempty"`
}

// AttrDef is a contact-attribute definition, optionally surfaced in onboarding.
type AttrDef struct {
	Name         string `json:"name,omitempty"`
	Slug         string `json:"slug"`
	FieldType    string `json:"field_type"`
	InOnboarding bool   `json:"in_onboarding,omitempty"`
	Required     bool   `json:"required,omitempty"`
}

// Playlist is a media playlist published onto the hub. Visibility is optional;
// when set it is validated against the hub-media visibility enum.
type Playlist struct {
	Title      string   `json:"title"`
	Key        string   `json:"key"`
	Visibility string   `json:"visibility,omitempty"`
	FileIDs    []string `json:"file_ids,omitempty"`
}

// HomepageRef points at the homepage template to instantiate. Template is a
// template id string (static cards at this stage — never a data-source-bound
// grid). Privacy is optional; when set it is validated against the page privacy enum.
type HomepageRef struct {
	Template string `json:"template"`
	Variant  string `json:"variant,omitempty"`
	Privacy  string `json:"privacy,omitempty"`
}

// Load reads, unmarshals, and validates the embedded template with the given id.
// An unknown id returns a clear error; a malformed or schema-invalid template
// returns the underlying error wrapped with the id.
func Load(id string) (*Template, error) {
	b, err := templatesFS.ReadFile("hubtemplates/" + id + ".json")
	if err != nil {
		return nil, fmt.Errorf("unknown hub template %q", id)
	}
	var t Template
	// Strict decode: an unknown key in a hand-authored template (e.g. a typo'd
	// "vis1bility") fails LOUD at load rather than being silently dropped and
	// letting the scaffold build the wrong hub. The four map[string]any blobs
	// still absorb arbitrary keys — strictness only guards the typed structs,
	// which is exactly where a silent drop would mislead.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("hub template %q: %w", id, err)
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("hub template %q: %w", id, err)
	}
	return &t, nil
}

// List returns the ids of all embedded templates (filenames with .json stripped),
// sorted.
func List() []string {
	entries, err := templatesFS.ReadDir("hubtemplates")
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(ids)
	return ids
}

// Validate enforces the template schema: a non-empty id; every space carries a
// slug; every onboarding def carries a slug and field_type; every playlist
// carries a title and key; a homepage with a non-empty template; and enum
// membership for any set enum value (space access_level/posting_permission,
// def field_type, playlist visibility, homepage privacy).
func (t *Template) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("template: missing id")
	}
	// Slugs must be UNIQUE across spaces and (separately) onboarding defs: each
	// step snapshots existing server slugs ONCE and skip-if-exists against that
	// snapshot, so a duplicate template slug would issue a duplicate create
	// mid-pipeline (the snapshot can't yet include the sibling just created).
	// Reject at load rather than silently double-create.
	seenSpaceSlugs := map[string]bool{}
	for i, s := range t.Spaces {
		if s.Slug == "" {
			return fmt.Errorf("template: spaces[%d] missing slug", i)
		}
		if seenSpaceSlugs[s.Slug] {
			return fmt.Errorf("template: spaces[%d] duplicate slug %q", i, s.Slug)
		}
		seenSpaceSlugs[s.Slug] = true
		if s.AccessLevel != "" && !spaceAccessLevels[s.AccessLevel] {
			return fmt.Errorf("template: spaces[%d] invalid access_level %q", i, s.AccessLevel)
		}
		if s.PostingPermission != "" && !spacePostingPermissions[s.PostingPermission] {
			return fmt.Errorf("template: spaces[%d] invalid posting_permission %q", i, s.PostingPermission)
		}
	}
	seenDefSlugs := map[string]bool{}
	for i, d := range t.Onboarding {
		if d.Slug == "" {
			return fmt.Errorf("template: onboarding[%d] missing slug", i)
		}
		if seenDefSlugs[d.Slug] {
			return fmt.Errorf("template: onboarding[%d] duplicate slug %q", i, d.Slug)
		}
		seenDefSlugs[d.Slug] = true
		if d.FieldType == "" {
			return fmt.Errorf("template: onboarding[%d] missing field_type", i)
		}
		if !attrFieldTypes[d.FieldType] {
			return fmt.Errorf("template: onboarding[%d] invalid field_type %q", i, d.FieldType)
		}
	}
	// Each policy value must be an OBJECT. A non-object (string/number/typo) would
	// fall through the step's `raw.(map[string]any)` as an empty map and then send
	// content:null — silently RESETTING the policy content instead of failing loud.
	for k, v := range t.Policies {
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("template: policies[%q] must be an object", k)
		}
	}
	// Keys must be UNIQUE across playlists: the scaffold records playlist ids by
	// key (playlistIDsByKey[p.Key]), so a duplicate key would silently overwrite an
	// earlier playlist's id — and a future homepage step referencing playlists by
	// key would collapse them. Reject duplicates at load rather than silently drop.
	seenPlaylistKeys := map[string]bool{}
	for i, p := range t.Playlists {
		if p.Title == "" {
			return fmt.Errorf("template: playlists[%d] missing title", i)
		}
		if p.Key == "" {
			return fmt.Errorf("template: playlists[%d] missing key", i)
		}
		if seenPlaylistKeys[p.Key] {
			return fmt.Errorf("template: playlists[%d] duplicate playlist key %q", i, p.Key)
		}
		seenPlaylistKeys[p.Key] = true
		if p.Visibility != "" && !hubMediaVisibilityValues[p.Visibility] {
			return fmt.Errorf("template: playlists[%d] invalid visibility %q", i, p.Visibility)
		}
	}
	if t.Homepage == nil || t.Homepage.Template == "" {
		return fmt.Errorf("template: homepage template is required")
	}
	if t.Homepage.Privacy != "" && !pagePrivacyValues[t.Homepage.Privacy] {
		return fmt.Errorf("template: homepage invalid privacy %q", t.Homepage.Privacy)
	}
	return nil
}
