// Package blocks has what the core and the service SDK both do to a
// building block's instance: fill its config, connect its slots, read its
// config schema.
package blocks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// DecodeConfig fills the instance's config fields from its block. Unknown
// keys are an error, so a typo does not silently do nothing.
func DecodeConfig(inst companion.Instance, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(inst); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

type slotField struct {
	index []int
	slot  string
	id    bool // the field takes the connected instance's ID (slotid=)
}

// slotFields finds the fields tagged `companion:"slot=<name>"` or
// `companion:"slotid=<name>"`.
func slotFields(t reflect.Type) []slotField {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []slotField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		for _, part := range strings.Split(f.Tag.Get("companion"), ",") {
			if name, ok := strings.CutPrefix(part, "slot="); ok {
				out = append(out, slotField{index: f.Index, slot: name})
			}
			if name, ok := strings.CutPrefix(part, "slotid="); ok {
				out = append(out, slotField{index: f.Index, slot: name, id: true})
			}
		}
	}
	return out
}

// InjectSlots sets the slot fields: an interface field gets the provider, a
// slice field all of them, a map field them by instance ID, a string field
// (slotid=) the ID, a []string field (slotid=) the IDs.
func InjectSlots(inst companion.Instance, conns map[string][]string, provider func(id string) (any, error)) error {
	v := reflect.ValueOf(inst)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		if len(conns) == 0 {
			return nil
		}
		return fmt.Errorf("instance %T has slots but is not a pointer to a struct", inst)
	}
	v = v.Elem()
	for _, sf := range slotFields(v.Type()) {
		f := v.FieldByIndex(sf.index)
		ids := conns[sf.slot]
		if sf.id {
			switch f.Kind() {
			case reflect.String:
				if len(ids) > 0 {
					f.SetString(ids[0])
				}
			case reflect.Slice:
				f.Set(reflect.ValueOf(append([]string(nil), ids...)))
			default:
				return fmt.Errorf("slotid field %s must be a string or []string", sf.slot)
			}
			continue
		}
		switch f.Kind() {
		case reflect.Interface:
			if len(ids) == 0 {
				continue
			}
			p, err := provider(ids[0])
			if err != nil {
				return err
			}
			if err := assign(f, p, sf.slot, ids[0]); err != nil {
				return err
			}
		case reflect.Slice:
			s := reflect.MakeSlice(f.Type(), len(ids), len(ids))
			for i, id := range ids {
				p, err := provider(id)
				if err != nil {
					return err
				}
				if err := assign(s.Index(i), p, sf.slot, id); err != nil {
					return err
				}
			}
			f.Set(s)
		case reflect.Map:
			m := reflect.MakeMapWithSize(f.Type(), len(ids))
			for _, id := range ids {
				p, err := provider(id)
				if err != nil {
					return err
				}
				e := reflect.New(f.Type().Elem()).Elem()
				if err := assign(e, p, sf.slot, id); err != nil {
					return err
				}
				m.SetMapIndex(reflect.ValueOf(id), e)
			}
			f.Set(m)
		default:
			return fmt.Errorf("slot field %s has unsupported type %s", sf.slot, f.Type())
		}
	}
	return nil
}

func assign(dst reflect.Value, p any, slot, id string) error {
	pv := reflect.ValueOf(p)
	if !pv.IsValid() || !pv.Type().AssignableTo(dst.Type()) {
		return fmt.Errorf("slot %s: %s (%T) does not implement %s", slot, id, p, dst.Type())
	}
	dst.Set(pv)
	return nil
}

// SecretFields lists the json names of fields tagged `companion:"secret"`.
func SecretFields(t reflect.Type) map[string]bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]bool{}
	if t == nil || t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if hasTagPart(f.Tag.Get("companion"), "secret") {
			out[jsonName(f)] = true
		}
	}
	return out
}

func hasTagPart(tag, part string) bool {
	for _, p := range strings.Split(tag, ",") {
		if p == part {
			return true
		}
	}
	return false
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		name = f.Name
	}
	return name
}

// Field describes one config field, for the settings form the shell
// generates for building blocks without their own settings element.
type Field struct {
	Name   string  `json:"name"`
	Type   string  `json:"type"` // string | number | boolean | duration | list | object
	Secret bool    `json:"secret,omitempty"`
	Slot   *string `json:"slot,omitempty"` // the capability a slot needs
	Help   string  `json:"help,omitempty"`
}

// ConfigFields are the json-tagged fields of a config struct; embedded
// structs contribute theirs, as encoding/json flattens them.
func ConfigFields(t reflect.Type) []Field {
	var out []Field
	durationType := reflect.TypeFor[companion.Duration]()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("companion")
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			out = append(out, ConfigFields(f.Type)...)
			continue
		}
		if !f.IsExported() || f.Tag.Get("json") == "" || f.Tag.Get("json") == "-" || strings.Contains(tag, "slot") {
			continue
		}
		fd := Field{Name: jsonName(f), Secret: hasTagPart(tag, "secret"), Help: f.Tag.Get("help")}
		switch {
		case f.Type == durationType:
			fd.Type = "duration"
		case f.Type.Kind() == reflect.String:
			fd.Type = "string"
		case f.Type.Kind() == reflect.Bool:
			fd.Type = "boolean"
		case f.Type.Kind() >= reflect.Int && f.Type.Kind() <= reflect.Float64:
			fd.Type = "number"
		case f.Type.Kind() == reflect.Slice:
			fd.Type = "list"
		default:
			fd.Type = "object"
		}
		out = append(out, fd)
	}
	return out
}
