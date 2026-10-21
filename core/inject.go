package core

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/internal/blocks"
)

// Field describes one config field, for the settings form the shell
// generates for building blocks without their own settings element.
type Field = blocks.Field

// splitSlots takes the slot keys out of an instance's config block. A slot
// value is an instance reference ("mastodon:main") or, for a multiple slot, a
// list of them; a single reference is accepted for a multiple slot too.
func splitSlots(info companion.Info, values map[string]any) (map[string]any, map[string][]string, error) {
	rest := make(map[string]any, len(values))
	for k, v := range values {
		rest[k] = v
	}
	conns := map[string][]string{}
	for _, s := range info.Slots {
		v, ok := rest[s.Name]
		if !ok {
			continue
		}
		delete(rest, s.Name)
		var refs []string
		switch x := v.(type) {
		case nil:
		case string:
			if strings.TrimSpace(x) != "" {
				refs = []string{x}
			}
		case []any:
			for _, e := range x {
				str, ok := e.(string)
				if !ok {
					return nil, nil, fmt.Errorf("slot %s: %v is not an instance reference", s.Name, e)
				}
				refs = append(refs, str)
			}
		default:
			return nil, nil, fmt.Errorf("slot %s: %v is not an instance reference", s.Name, v)
		}
		if len(refs) > 1 && !s.Multiple {
			return nil, nil, fmt.Errorf("slot %s takes one instance, got %d", s.Name, len(refs))
		}
		for i, r := range refs {
			b, n := companion.SplitInstanceID(strings.TrimSpace(r))
			refs[i] = companion.InstanceID(b, n)
		}
		conns[s.Name] = refs
	}
	return rest, conns, nil
}

// masked returns a copy of the values with secret fields replaced.
func masked(values map[string]any, secrets map[string]bool) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		if secrets[k] {
			if s, ok := v.(string); ok && s == "" {
				out[k] = ""
			} else {
				out[k] = "••••••"
			}
			continue
		}
		out[k] = v
	}
	return out
}

// schemaOf lists the config fields of an instance type, slots included.
func schemaOf(info companion.Info) []Field {
	var out []Field
	for _, s := range info.Slots {
		c := s.Needs.String()
		typ := "instance"
		if s.Multiple {
			typ = "instances"
		}
		out = append(out, Field{Name: s.Name, Type: typ, Slot: &c})
	}
	if info.New == nil {
		return out
	}
	t := reflect.TypeOf(info.New())
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return out
	}
	out = append(out, blocks.ConfigFields(t)...)
	sort.SliceStable(out, func(a, b int) bool { return out[a].Slot != nil && out[b].Slot == nil })
	return out
}

// SchemaOf is the config schema of a building block; for tests and tools.
func SchemaOf(info companion.Info) []Field { return schemaOf(info) }
