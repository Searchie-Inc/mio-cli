package client

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

const resolveListCap = 100

var (
	teamIDPrefixes    = []string{"team_", "t_"}
	hubIDPrefixes     = []string{"hub_", "h_"}
	tagIDPrefixes     = []string{"tag_"}
	contactIDPrefixes = []string{"ctt_", "contact_", "tcid_", "tc_"}
)

type resolveCache struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *resolveCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		return "", false
	}
	v, ok := c.m[key]
	return v, ok
}

func (c *resolveCache) put(key, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]string)
	}
	c.m[key] = id
}

func cacheKey(kind, scope, value string) string {
	return kind + "\x00" + scope + "\x00" + value
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexDigit(r) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func hasIDPrefix(value string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(value, p) {
			return true
		}
	}
	return false
}

func isIDShaped(value string, prefixes ...string) bool {
	return isUUID(value) || hasIDPrefix(value, prefixes...)
}

func (c *Client) ResolveTeam(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("empty team identifier")
	}
	if isIDShaped(value, teamIDPrefixes...) {
		return value, nil
	}
	key := cacheKey("team", "", value)
	if id, ok := c.resolveCache.get(key); ok {
		return id, nil
	}

	col, err := c.List(ctx, "/api/teams", listCapQuery())
	if err != nil {
		return "", err
	}
	id, err := matchByNameOrSlug("team", "teams", value, col.Data)
	if err != nil {
		return "", err
	}
	c.resolveCache.put(key, id)
	return id, nil
}

func (c *Client) ResolveHub(ctx context.Context, teamID, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("empty hub identifier")
	}
	if isIDShaped(value, hubIDPrefixes...) {
		return value, nil
	}
	if teamID == "" {
		return "", fmt.Errorf("cannot resolve hub %q by name without a team in context", value)
	}
	key := cacheKey("hub", teamID, value)
	if id, ok := c.resolveCache.get(key); ok {
		return id, nil
	}

	col, err := c.List(ctx, fmt.Sprintf("/api/teams/%s/hubs", teamID), listCapQuery())
	if err != nil {
		return "", err
	}
	id, err := matchByNameOrSlug("hub", "hubs", value, col.Data)
	if err != nil {
		return "", err
	}
	c.resolveCache.put(key, id)
	return id, nil
}

func (c *Client) ResolveTag(ctx context.Context, teamID, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("empty tag identifier")
	}
	if isIDShaped(value, tagIDPrefixes...) {
		return value, nil
	}
	if teamID == "" {
		return "", fmt.Errorf("cannot resolve tag %q by name without a team in context", value)
	}
	key := cacheKey("tag", teamID, value)
	if id, ok := c.resolveCache.get(key); ok {
		return id, nil
	}

	col, err := c.List(ctx, fmt.Sprintf("/api/teams/%s/tags", teamID), listCapQuery())
	if err != nil {
		return "", err
	}
	id, err := matchByNameOrSlug("tag", "tags", value, col.Data)
	if err != nil {
		return "", err
	}
	c.resolveCache.put(key, id)
	return id, nil
}

func (c *Client) ResolveContactByEmail(ctx context.Context, teamID, email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", fmt.Errorf("empty contact email")
	}
	if !strings.Contains(email, "@") && isIDShaped(email, contactIDPrefixes...) {
		return email, nil
	}
	if teamID == "" {
		return "", fmt.Errorf("cannot resolve contact %q by email without a team in context", email)
	}
	key := cacheKey("contact-email", teamID, strings.ToLower(email))
	if id, ok := c.resolveCache.get(key); ok {
		return id, nil
	}

	q := url.Values{}
	q.Set("filter[email]", email)
	q.Set("page[size]", itoaCap(resolveListCap))
	col, err := c.List(ctx, fmt.Sprintf("/api/teams/%s/contacts", teamID), q)
	if err != nil {
		return "", err
	}
	switch len(col.Data) {
	case 0:
		return "", fmt.Errorf("no contact with email %q — run 'mio contacts list'", email)
	case 1:
		id := col.Data[0].ID
		c.resolveCache.put(key, id)
		return id, nil
	default:
		return "", ambiguousError("contact", email, col.Data)
	}
}

func matchByNameOrSlug(kind, listCmd, value string, data []Resource) (string, error) {
	want := strings.ToLower(value)

	var slugHits []Resource
	for _, r := range data {
		if slug, ok := r.Attributes["slug"].(string); ok && strings.ToLower(slug) == want {
			slugHits = append(slugHits, r)
		}
	}
	if len(slugHits) == 1 {
		return slugHits[0].ID, nil
	}
	if len(slugHits) > 1 {
		return "", ambiguousError(kind, value, slugHits)
	}

	var nameHits []Resource
	for _, r := range data {
		if name, ok := r.Attributes["name"].(string); ok && strings.ToLower(name) == want {
			nameHits = append(nameHits, r)
		}
	}
	switch len(nameHits) {
	case 0:
		return "", fmt.Errorf("no %s named %q — run 'mio %s list'", kind, value, listCmd)
	case 1:
		return nameHits[0].ID, nil
	default:
		return "", ambiguousError(kind, value, nameHits)
	}
}

func ambiguousError(kind, value string, candidates []Resource) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s name %q is ambiguous — %d matches; pass a raw id instead:",
		kind, value, len(candidates))
	for _, r := range candidates {
		name, _ := r.Attributes["name"].(string)
		if name == "" {
			name, _ = r.Attributes["slug"].(string)
		}
		fmt.Fprintf(&sb, "\n  %s  %s", r.ID, name)
	}
	return fmt.Errorf("%s", sb.String())
}

func listCapQuery() url.Values {
	q := url.Values{}
	q.Set("page[size]", itoaCap(resolveListCap))
	return q
}

func itoaCap(n int) string {
	if n <= 0 {
		return "1"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
