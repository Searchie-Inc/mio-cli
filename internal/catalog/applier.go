package catalog

// applier.go — the Go port of the page-builder reference applier
// (mio-page-catalog@f75ddf4 src/applier.ts). It is the ~5% imperative step of
// the catalog-sync contract (charter §7): deep-clone a template's starter (or a
// selected variant) subtree and mint a fresh UUIDv7 id for every node,
// REPLACING any placeholder id the recipe carries. It deliberately does NOT run
// the layer-2 defaults cascade, resolve dataSources, resolve gates, or prune —
// those stay runtime-only. Structural keys (settings.slot / role / name) and
// every other field round-trip untouched.
//
// Byte-parity with the TS reference is proven by parity_test.go (golden
// fixtures + whole-catalog digest).

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// IDGen mints node ids. The error return exists because the production
// generator draws from crypto/rand, which can fail; deterministic test
// generators never error.
type IDGen func() (string, error)

// InstantiateTemplate deep-clones a template into a fresh authored subtree,
// selecting variants[variant] when a matching key exists, else falling back to
// the base starter — a graceful fallback that never errors for a missing or
// omitted variant (matches mio-hub's applyTemplate(name, {variant}), MIO-2248).
// Every node gets a fresh id from gen.
func InstantiateTemplate(t Template, variant string, gen IDGen) (Node, error) {
	var base Node
	if variant != "" {
		if v, ok := t.Variants[variant]; ok {
			base = v
		}
	}
	if base == nil {
		base = t.Starter
	}
	if base == nil {
		return nil, fmt.Errorf("template %q has no starter subtree", t.ID)
	}
	return CloneWithFreshIDs(base, gen)
}

// CloneWithFreshIDs deep-clones node (so mutating an instance never mutates the
// catalog recipe), then walks it once re-IDing every node in the children-tree.
// Ports mio-hub's cloneWithFreshIds.
func CloneWithFreshIDs(node Node, gen IDGen) (Node, error) {
	clone, ok := deepClone(node).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("applier: recipe root is not an object node")
	}
	seen := make(map[string]bool)
	if err := reIDInPlace(clone, gen, seen); err != nil {
		return nil, err
	}
	return clone, nil
}

// reIDInPlace assigns a fresh id to node and recurses into its children in
// order (pre-order DFS). Applier contract step 4: ids must be unique within the
// tree — a well-behaved UUIDv7 gen never collides; a broken one is surfaced
// loudly rather than emitting a corrupt tree.
func reIDInPlace(node map[string]any, gen IDGen, seen map[string]bool) error {
	id, err := gen()
	if err != nil {
		return err
	}
	if seen[id] {
		return fmt.Errorf("applier: idGen produced a duplicate node id %q — ids must be unique within a tree", id)
	}
	seen[id] = true
	node["id"] = id
	if children, ok := node["children"].([]any); ok {
		for _, c := range children {
			if cm, ok := c.(map[string]any); ok {
				if err := reIDInPlace(cm, gen, seen); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// NormalizeIDs returns a deep copy of node with every string node id replaced by
// a deterministic pre-order (DFS) placeholder (#0, #1, …) so two structurally
// identical trees compare equal regardless of the concrete UUIDv7 ids each
// applier minted. Ports mio-page-catalog src/normalize-ids.ts; used by the
// golden parity test. Only nodes that already carry a string id are renumbered
// (matches the reference), and the input is left untouched.
func NormalizeIDs(node any) any {
	clone := deepClone(node)
	i := 0
	var walk func(n any)
	walk = func(n any) {
		m, ok := n.(map[string]any)
		if !ok {
			return
		}
		if _, isStr := m["id"].(string); isStr {
			m["id"] = fmt.Sprintf("#%d", i)
			i++
		}
		if children, ok := m["children"].([]any); ok {
			for _, c := range children {
				walk(c)
			}
		}
	}
	walk(clone)
	return clone
}

// deepClone recursively copies maps and slices; scalars (string, bool,
// json.Number, nil) are immutable and returned as-is. structuredClone's Go
// analogue for the applier's JSON value space.
func deepClone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepClone(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = deepClone(val)
		}
		return out
	default:
		return v
	}
}

// NewUUIDv7Gen returns an IDGen minting RFC 9562 (§5.7) UUIDv7 strings — a
// 48-bit Unix-millis timestamp, version nibble 7, variant bits 10, and 74 bits
// of cryptographic randomness. Uniqueness within a tree is guaranteed by the
// random bits (and enforced by reIDInPlace). mio-cli carries no uuid
// dependency, so this is implemented directly.
func NewUUIDv7Gen() IDGen {
	return func() (string, error) {
		return newUUIDv7(time.Now().UnixMilli())
	}
}

// newUUIDv7 builds a UUIDv7 for the given Unix-millisecond timestamp. Split out
// from NewUUIDv7Gen so the timestamp source is injectable if ever needed.
func newUUIDv7(ms int64) (string, error) {
	var b [16]byte
	u := uint64(ms)
	b[0] = byte(u >> 40)
	b[1] = byte(u >> 32)
	b[2] = byte(u >> 24)
	b[3] = byte(u >> 16)
	b[4] = byte(u >> 8)
	b[5] = byte(u)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", fmt.Errorf("applier: uuidv7 randomness: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return formatUUID(b), nil
}

// formatUUID renders 16 bytes as the canonical 8-4-4-4-12 lowercase hex form.
func formatUUID(b [16]byte) string {
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}
