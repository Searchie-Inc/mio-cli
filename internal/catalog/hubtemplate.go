package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	hubSpaceAccessLevels        = map[string]bool{"public": true, "restricted": true}
	hubSpacePostingPermissions  = map[string]bool{"any_member": true, "admins_only": true, "admins_and_moderators": true, "segment": true}
	hubPagePrivacyValues        = map[string]bool{"public": true, "members": true, "private": true}
	hubPlaylistVisibilityValues = map[string]bool{"public": true, "unlisted": true, "private": true}
	hubAttrFieldTypes           = map[string]bool{"text": true, "number": true, "boolean": true, "date": true, "multiple": true}
	hubPolicyFieldKeys          = map[string]bool{"content": true, "require_acceptance": true, "required": true, "enabled": true}
)

func HubPolicyFieldKey(f string) bool { return hubPolicyFieldKeys[f] }

func HubPolicyFieldKeys() []string {
	keys := make([]string, 0, len(hubPolicyFieldKeys))
	for k := range hubPolicyFieldKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type PageRef struct {
	Role, PageTemplate, Slug, Title, Privacy string
	IsHomepage                               bool
}

type TemplateSpace struct{ Name, Slug, Description, AccessLevel, PostingPermission, Icon string }

type TemplateDocument struct{ Title, Description string }

type TemplateAttrDef struct {
	Name, Slug, FieldType  string
	InOnboarding, Required bool
}

type TemplatePlaylist struct {
	Title, Key, Visibility string
	FileIDs                []string
	Documents              []TemplateDocument
}

const DiscussionTitleMaxCP = 280

type TemplateWelcomePost struct {
	Space, Title, Body string
	Published          bool
}

type HubTemplate struct {
	ID, Label, Lifecycle                     string
	Branding, Navigation, Settings, Policies map[string]any
	Spaces                                   []TemplateSpace
	Onboarding                               []TemplateAttrDef
	Playlists                                []TemplatePlaylist
	Pages                                    []PageRef
	WelcomePost                              *TemplateWelcomePost
}

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
			Icon:              str(m["icon"]),
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
		p := TemplatePlaylist{
			Title:      str(m["title"]),
			Key:        str(m["key"]),
			Visibility: str(m["visibility"]),
			FileIDs:    strSlice(m["file_ids"]),
		}
		for _, d := range slice(m["documents"]) {
			dm, ok := d.(map[string]any)
			if !ok {
				continue
			}
			p.Documents = append(p.Documents, TemplateDocument{
				Title:       str(dm["title"]),
				Description: str(dm["description"]),
			})
		}
		h.Playlists = append(h.Playlists, p)
	}
	if wp := asNode(n["welcomePost"]); wp != nil {
		post := &TemplateWelcomePost{
			Space:     str(wp["space"]),
			Title:     str(wp["title"]),
			Body:      str(wp["body"]),
			Published: true,
		}
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

func (h HubTemplate) Validate(c *Catalog) error {
	if len(h.Pages) == 0 {
		return fmt.Errorf("hub template %q: pages must not be empty", h.ID)
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
			return fmt.Errorf("hub template %q: playlists[%d] invalid visibility %q (must be public, unlisted, or private)", h.ID, i, p.Visibility)
		}
		for j, d := range p.Documents {
			if strings.TrimSpace(d.Title) == "" {
				return fmt.Errorf("hub template %q: playlists[%d].documents[%d] missing title", h.ID, i, j)
			}
		}
	}
	homepages := 0
	seenPageSlugs := map[string]bool{}
	for i, p := range h.Pages {
		if p.Slug == "" {
			return fmt.Errorf("hub template %q: pages[%d] missing slug", h.ID, i)
		}
		if IsReservedPageSlug(p.Slug) {
			return fmt.Errorf("hub template %q: pages[%d] slug %q is backend-reserved", h.ID, i, p.Slug)
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
		for _, key := range PlaylistDataSourceKeys(tmpl.Starter) {
			if !seenPlaylistKeys[key] {
				return fmt.Errorf("hub template %q: pages[%d] pageTemplate %q binds a playlist dataSource key %q, which is not one of this template's playlists[].key", h.ID, i, p.PageTemplate, key)
			}
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
		case !seenSpaceSlugs[wp.Space]:
			return fmt.Errorf("hub template %q: welcomePost space %q is not one of this template's spaces", h.ID, wp.Space)
		}
	}
	return nil
}

func (c *Catalog) HubTemplateByID(id string) (HubTemplate, bool) {
	for _, h := range c.HubTemplates {
		if h.ID == id {
			return h, true
		}
	}
	return HubTemplate{}, false
}

func (c *Catalog) HubTemplateIDs() []string {
	ids := make([]string, 0, len(c.HubTemplates))
	for _, h := range c.HubTemplates {
		ids = append(ids, h.ID)
	}
	sort.Strings(ids)
	return ids
}

func (h HubTemplate) HomepagePage() *PageRef {
	for i := range h.Pages {
		if h.Pages[i].IsHomepage {
			return &h.Pages[i]
		}
	}
	return nil
}

func ApplicationID(hubID, hubTemplateID string) string {
	sum := sha256.Sum256([]byte(hubID + "\x1f" + hubTemplateID))
	return hex.EncodeToString(sum[:])
}

func CreateApplicationID(teamID, hubTemplateID, name, slug string) string {
	sum := sha256.Sum256([]byte(teamID + "\x1f" + hubTemplateID + "\x1f" + name + "\x1f" + slug))
	return hex.EncodeToString(sum[:])
}

func TreeDigest(tree map[string]any) (string, error) {
	b, err := CanonicalJSON(tree)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func CloneNode(n Node) Node {
	if n == nil {
		return nil
	}
	c, _ := deepClone(n).(Node)
	return c
}
