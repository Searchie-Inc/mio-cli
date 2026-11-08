package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type IDGen func() (string, error)

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

func NewUUIDv7Gen() IDGen {
	return func() (string, error) {
		return newUUIDv7(time.Now().UnixMilli())
	}
}

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
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b), nil
}

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
