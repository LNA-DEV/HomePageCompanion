package main

import (
	"bytes"
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// genTS writes the TypeScript types of a capability package: its structs
// as interfaces and every capability as an interface of its operations,
// for UIs (@companion/ui/capabilities/<package>).
func genTS(p *genPkg) []byte {
	g := &tsGen{p: p, done: map[string]bool{}, imports: map[string]bool{}}
	var caps bytes.Buffer
	for _, c := range p.Caps {
		writeDoc(&caps, "", p.docs[c.Iface.Name()]+"\n\n"+c.Wire())
		fmt.Fprintf(&caps, "export interface %s {\n", c.Var)
		for _, o := range c.Required {
			g.method(&caps, o, false)
		}
		for _, oi := range c.Optional {
			for _, o := range oi.Ops {
				if o.Doc == "" {
					o.Doc = p.docs[oi.Iface.Name()]
				}
				g.method(&caps, o, true)
			}
		}
		caps.WriteString("}\n\n")
		fmt.Fprintf(&caps, "export const %sName = %q;\n\n", c.Var, c.Wire())
	}
	if len(p.Errors) > 0 {
		var codes []string
		for _, e := range p.Errors {
			codes = append(codes, fmt.Sprintf("%q", e.Code))
		}
		fmt.Fprintf(&caps, "/** Error codes of the package's capabilities. */\nexport type ErrorCode = %s;\n", strings.Join(codes, " | "))
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "// %s\n// From %s.\n\n", genHeader, p.Path)
	var imps []string
	for name := range g.imports {
		imps = append(imps, name)
	}
	sort.Strings(imps)
	for _, name := range imps {
		fmt.Fprintf(&out, "import type * as %s from './%s';\n", name, name)
	}
	if len(imps) > 0 {
		out.WriteString("\n")
	}
	for _, name := range g.order {
		out.WriteString(g.types[name])
	}
	out.Write(caps.Bytes())
	return append(bytes.TrimRight(out.Bytes(), "\n"), '\n')
}

type tsGen struct {
	p       *genPkg
	done    map[string]bool
	order   []string
	types   map[string]string
	imports map[string]bool
}

func writeDoc(b *bytes.Buffer, indent, doc string) {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return
	}
	lines := strings.Split(doc, "\n")
	if len(lines) == 1 {
		fmt.Fprintf(b, "%s/** %s */\n", indent, lines[0])
		return
	}
	fmt.Fprintf(b, "%s/**\n", indent)
	for _, l := range lines {
		fmt.Fprintf(b, "%s\n", strings.TrimRight(indent+" * "+l, " "))
	}
	fmt.Fprintf(b, "%s */\n", indent)
}

func (g *tsGen) method(b *bytes.Buffer, o *genOp, optional bool) {
	doc := o.Doc
	if optional {
		doc = strings.TrimSpace(doc + "\n\nOptional: not every provider offers it.")
	}
	writeDoc(b, "\t", doc)
	var params []string
	for _, a := range o.Params {
		t := g.of(a.Type)
		if isBytes(a.Type) {
			t = "Uint8Array"
		}
		params = append(params, fmt.Sprintf("%s: %s", a.Name, t))
	}
	res := "void"
	switch {
	case o.RawResult:
		res = "Uint8Array"
	case o.Result != nil:
		res = g.of(o.Result)
	}
	q := ""
	if optional {
		q = "?"
	}
	fmt.Fprintf(b, "\t%s%s(%s): Promise<%s>;\n", o.Name, q, strings.Join(params, ", "), res)
}

func (g *tsGen) of(t types.Type) string {
	switch {
	case isTime(t):
		return "string"
	case isRawJSON(t):
		return "unknown"
	}
	if n, ok := t.(*types.Named); ok && isStruct(t) {
		if pkg := n.Obj().Pkg(); pkg != nil && pkg.Path() != g.p.Path {
			g.imports[pkg.Name()] = true
			return pkg.Name() + "." + n.Obj().Name()
		}
		g.declare(n)
		return n.Obj().Name()
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsBoolean != 0:
			return "boolean"
		case u.Info()&types.IsNumeric != 0:
			return "number"
		case u.Info()&types.IsString != 0:
			return "string"
		}
	case *types.Pointer:
		return g.of(u.Elem())
	case *types.Slice:
		return g.elem(u.Elem()) + "[]"
	case *types.Array:
		parts := make([]string, u.Len())
		for i := range parts {
			parts[i] = g.of(u.Elem())
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *types.Map:
		return "Record<string, " + g.of(u.Elem()) + ">"
	case *types.Struct:
		return g.inline(t)
	}
	return "unknown"
}

// elem is an array's element type, parenthesised where needed.
func (g *tsGen) elem(t types.Type) string {
	s := g.of(t)
	if strings.ContainsAny(s, " |") {
		return "(" + s + ")"
	}
	return s
}

func (g *tsGen) inline(t types.Type) string {
	var parts []string
	for _, f := range jsonFields(t) {
		parts = append(parts, fmt.Sprintf("%s%s: %s", f.Name, optMark(f), g.of(f.Type)))
	}
	return "{ " + strings.Join(parts, "; ") + " }"
}

func optMark(f field) string {
	if _, ptr := f.Type.(*types.Pointer); ptr || f.OmitEmpty {
		return "?"
	}
	return ""
}

func (g *tsGen) declare(n *types.Named) {
	name := n.Obj().Name()
	if g.done[name] {
		return
	}
	g.done[name] = true
	if g.types == nil {
		g.types = map[string]string{}
	}
	var b bytes.Buffer
	writeDoc(&b, "", g.p.docs[name])
	fmt.Fprintf(&b, "export interface %s {\n", name)
	for _, f := range jsonFields(n) {
		writeDoc(&b, "\t", g.p.docs[f.Owner+"."+f.GoName])
		fmt.Fprintf(&b, "\t%s%s: %s;\n", f.Name, optMark(f), g.of(f.Type))
	}
	b.WriteString("}\n\n")
	g.types[name] = b.String()
	g.order = append(g.order, name)
}
