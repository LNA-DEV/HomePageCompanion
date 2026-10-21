package blocks_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/internal/blocks"
)

type greeter interface{ Greet() string }

type hi string

func (h hi) Greet() string { return string(h) }

type block struct {
	Name    string              `json:"name" help:"who"`
	Wait    companion.Duration  `json:"wait"`
	Key     string              `json:"key" companion:"secret"`
	One     greeter             `companion:"slot=one"`
	OneID   string              `companion:"slotid=one"`
	Many    []greeter           `companion:"slot=many"`
	ByID    map[string]greeter  `companion:"slot=many"`
	ManyIDs []string            `companion:"slotid=many"`
	Extra   map[string][]string `json:"extra"`
}

func TestDecodeConfig(t *testing.T) {
	var b block
	if err := blocks.DecodeConfig(&b, map[string]any{"name": "x", "wait": "7d"}); err != nil || b.Name != "x" || b.Wait.D().Hours() != 168 {
		t.Fatalf("%+v %v", b, err)
	}
	if err := blocks.DecodeConfig(&b, map[string]any{"nmae": "typo"}); err == nil || !strings.Contains(err.Error(), "nmae") {
		t.Fatalf("unknown key accepted: %v", err)
	}
}

func TestInjectSlots(t *testing.T) {
	var b block
	conns := map[string][]string{"one": {"g:a"}, "many": {"g:a", "g:b"}}
	err := blocks.InjectSlots(&b, conns, func(id string) (any, error) { return hi("hi " + id), nil })
	if err != nil {
		t.Fatal(err)
	}
	if b.One.Greet() != "hi g:a" || b.OneID != "g:a" || len(b.Many) != 2 || b.ByID["g:b"].Greet() != "hi g:b" || len(b.ManyIDs) != 2 {
		t.Fatalf("%+v", b)
	}
	if err := blocks.InjectSlots(&b, conns, func(string) (any, error) { return 42, nil }); err == nil {
		t.Fatal("a provider of the wrong type was accepted")
	}
}

func TestSchema(t *testing.T) {
	secrets := blocks.SecretFields(reflect.TypeFor[*block]())
	if !secrets["key"] || len(secrets) != 1 {
		t.Fatalf("secrets %v", secrets)
	}
	types := map[string]string{}
	for _, f := range blocks.ConfigFields(reflect.TypeFor[block]()) {
		types[f.Name] = f.Type
	}
	if types["name"] != "string" || types["wait"] != "duration" || types["extra"] != "object" || types["One"] != "" {
		t.Fatalf("fields %v", types)
	}
}
