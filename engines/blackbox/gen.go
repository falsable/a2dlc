package main

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// gen extracts random instances of a JSON Schema. It supports what is needed to describe inputs of a CLI:
// type, const, enum, oneOf/anyOf, local $ref (#/$defs/… or #/definitions/…), properties/required, items,
// minItems/maxItems, minimum/maximum, minLength/maxLength and examples. The rest (pattern, format, if/then,
// uniqueItems…) is ignored: the repo's program must tolerate or reject those inputs, and the rejection is compared too.
type gen struct {
	r    *rand.Rand
	root map[string]any
}

const alphabet = "abcxyz/._-*0"

func (g *gen) value(s any, depth int) (any, error) {
	if depth > 40 {
		return nil, fmt.Errorf("schema too deep or recursive")
	}
	m, ok := s.(map[string]any)
	if !ok {
		if b, ok := s.(bool); ok && b {
			return g.r.Intn(10), nil
		}
		return nil, fmt.Errorf("unsupported subschema: %v", s)
	}
	if ref, ok := m["$ref"].(string); ok {
		sub, err := g.resolve(ref)
		if err != nil {
			return nil, err
		}
		return g.value(sub, depth+1)
	}
	if c, ok := m["const"]; ok {
		return c, nil
	}
	if e, ok := m["enum"].([]any); ok && len(e) > 0 {
		return e[g.r.Intn(len(e))], nil
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if alts, ok := m[k].([]any); ok && len(alts) > 0 {
			return g.value(alts[g.r.Intn(len(alts))], depth+1)
		}
	}
	if ex, ok := m["examples"].([]any); ok && len(ex) > 0 && g.r.Intn(2) == 0 {
		return ex[g.r.Intn(len(ex))], nil
	}
	kind := ""
	switch t := m["type"].(type) {
	case string:
		kind = t
	case []any:
		if len(t) > 0 {
			kind, _ = t[g.r.Intn(len(t))].(string)
		}
	}
	if kind == "" {
		switch {
		case m["properties"] != nil:
			kind = "object"
		case m["items"] != nil:
			kind = "array"
		default:
			kind = "string"
		}
	}
	switch kind {
	case "object":
		out := map[string]any{}
		props, _ := m["properties"].(map[string]any)
		req := map[string]bool{}
		if rs, ok := m["required"].([]any); ok {
			for _, r := range rs {
				if s, ok := r.(string); ok {
					req[s] = true
				}
			}
		}
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys) // fixed order: same seed, same instance
		for _, k := range keys {
			if !req[k] && g.r.Intn(2) == 0 {
				continue
			}
			v, err := g.value(props[k], depth+1)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = v
		}
		return out, nil
	case "array":
		lo, hi := integer(m, "minItems", 0), integer(m, "maxItems", -1)
		if hi < lo {
			hi = lo + 4
		}
		n := lo + g.r.Intn(hi-lo+1)
		out := make([]any, 0, n)
		items := m["items"]
		if items == nil {
			items = true
		}
		for i := 0; i < n; i++ {
			v, err := g.value(items, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case "integer":
		lo, hi := integer(m, "minimum", -100), integer(m, "maximum", -1<<31)
		if hi == -1<<31 {
			hi = lo + 200
		}
		if hi < lo {
			return nil, fmt.Errorf("minimum > maximum")
		}
		// Edge values come up more often than the rest: that is where off-by-one errors live.
		switch g.r.Intn(4) {
		case 0:
			return lo, nil
		case 1:
			return hi, nil
		}
		return lo + g.r.Intn(hi-lo+1), nil
	case "number":
		lo, hi := float64(integer(m, "minimum", -100)), float64(integer(m, "maximum", 100))
		return lo + g.r.Float64()*(hi-lo), nil
	case "boolean":
		return g.r.Intn(2) == 0, nil
	case "null":
		return nil, nil
	case "string":
		lo, hi := integer(m, "minLength", 0), integer(m, "maxLength", -1)
		if hi < lo {
			hi = lo + 8
		}
		n := lo + g.r.Intn(hi-lo+1)
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteByte(alphabet[g.r.Intn(len(alphabet))])
		}
		return b.String(), nil
	}
	return nil, fmt.Errorf("type %q not supported", kind)
}

func (g *gen) resolve(ref string) (any, error) {
	for _, pref := range []string{"#/$defs/", "#/definitions/"} {
		if strings.HasPrefix(ref, pref) {
			defs, _ := g.root[strings.Trim(pref[2:], "/")].(map[string]any)
			if v, ok := defs[strings.TrimPrefix(ref, pref)]; ok {
				return v, nil
			}
		}
	}
	return nil, fmt.Errorf("unsupported $ref: %s (only local #/$defs or #/definitions)", ref)
}

func integer(m map[string]any, k string, fallback int) int {
	if f, ok := m[k].(float64); ok {
		return int(f)
	}
	return fallback
}
