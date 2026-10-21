package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/types"
	"sort"
	"strings"
)

// genGo writes a package's wire_gen.go: per capability a client that calls
// a provider over companion/wire, a server that answers for an
// implementation, and their registration.
func genGo(p *genPkg) ([]byte, error) {
	imports := map[string]string{"context": "context", corePath + "/companion/wire": "wire"}
	q := func(pkg *types.Package) string {
		if pkg.Path() == p.Path {
			return ""
		}
		imports[pkg.Path()] = pkg.Name()
		return pkg.Name()
	}
	ts := func(t types.Type) string { return types.TypeString(t, q) }

	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("var wireErrors = wire.Errors{\n")
	for _, e := range p.Errors {
		w("\t%q: %s,\n", e.Code, e.Var)
	}
	w("}\n\nfunc init() {\n")
	for _, c := range p.Caps {
		var req, opt []string
		for _, o := range c.Required {
			req = append(req, fmt.Sprintf("%q", o.Name))
		}
		for _, oi := range c.Optional {
			for _, o := range oi.Ops {
				opt = append(opt, fmt.Sprintf("%q", o.Name))
			}
		}
		base := lowerFirst(c.Var)
		w("\twire.Register(wire.Adapter{\n\t\tCapability: %s,\n\t\tRequired: []string{%s},\n", c.Var, strings.Join(req, ", "))
		if len(opt) > 0 {
			w("\t\tOptional: []string{%s},\n", strings.Join(opt, ", "))
		}
		w("\t\tClient: new%sClient,\n\t\tServer: new%sServer,\n\t})\n", upperFirst(base), upperFirst(base))
	}
	w("}\n")

	for _, c := range p.Caps {
		base := lowerFirst(c.Var)
		client := base + "Client"
		w("\n// %s calls %s on a provider over the wire.\ntype %s struct{ c wire.Caller }\n", client, c.Wire(), client)
		for _, o := range c.Required {
			clientMethod(w, "*"+client, o, ts)
		}
		for _, oi := range c.Optional {
			t := base + oi.Iface.Name()
			w("\n// %s has the optional operations of %s.\ntype %s struct{ c wire.Caller }\n", t, oi.Iface.Name(), t)
			for _, o := range oi.Ops {
				clientMethod(w, t, o, ts)
			}
		}
		// One type per combination of optional interfaces, so a type
		// assertion (p.(social.Lister)) tells what the provider offers.
		n := len(c.Optional)
		for mask := 1; mask < 1<<n; mask++ {
			name, fields := client, []string{"*" + client}
			for i, oi := range c.Optional {
				if mask&(1<<i) != 0 {
					name += oi.Iface.Name()
					fields = append(fields, base+oi.Iface.Name())
				}
			}
			w("\ntype %s struct {\n", name)
			for _, f := range fields {
				w("\t%s\n", f)
			}
			w("}\n")
		}
		w("\nfunc new%sClient(c wire.Caller, ops []string) any {\n\tbase := &%s{c: c}\n", upperFirst(base), client)
		for i, oi := range c.Optional {
			w("\thas%d := wire.Has(ops, %q)\n", i, oi.Ops[0].Name)
		}
		for mask := 1<<n - 1; mask >= 1; mask-- {
			var conds []string
			name, vals := client, []string{"base"}
			for i, oi := range c.Optional {
				if mask&(1<<i) != 0 {
					conds = append(conds, fmt.Sprintf("has%d", i))
					name += oi.Iface.Name()
					vals = append(vals, fmt.Sprintf("%s%s{c}", base, oi.Iface.Name()))
				} else {
					conds = append(conds, fmt.Sprintf("!has%d", i))
				}
			}
			w("\tif %s {\n\t\treturn &%s{%s}\n\t}\n", strings.Join(conds, " && "), name, strings.Join(vals, ", "))
		}
		w("\treturn base\n}\n")

		w("\nfunc new%sServer(impl any) *wire.Server {\n\tv, ok := impl.(%s)\n\tif !ok {\n\t\treturn nil\n\t}\n", upperFirst(base), c.Iface.Name())
		w("\ts := &wire.Server{Capability: %q, Errors: wireErrors, Ops: map[string]wire.Op{\n", c.Wire())
		for _, o := range c.Required {
			serverOp(w, "v", o, ts)
		}
		w("\t}}\n")
		for i, oi := range c.Optional {
			w("\tif o%d, ok := impl.(%s); ok {\n", i, oi.Iface.Name())
			for _, o := range oi.Ops {
				w("\t\ts.Ops[%q] = ", o.Name)
				serverFunc(w, fmt.Sprintf("o%d", i), o, ts)
				w("\n")
			}
			w("\t}\n")
		}
		w("\treturn s\n}\n")
	}

	var head bytes.Buffer
	fmt.Fprintf(&head, "// %s\n\npackage %s\n\nimport (\n", genHeader, p.Name)
	var paths []string
	for path := range imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	// The standard library first, as goimports groups them.
	sort.SliceStable(paths, func(i, j int) bool { return !strings.Contains(paths[i], ".") && strings.Contains(paths[j], ".") })
	for i, path := range paths {
		if i > 0 && strings.Contains(path, ".") && !strings.Contains(paths[i-1], ".") {
			head.WriteString("\n")
		}
		fmt.Fprintf(&head, "\t%q\n", path)
	}
	head.WriteString(")\n\n")
	head.Write(b.Bytes())
	src, err := format.Source(head.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s: generated code does not format: %w\n%s", p.Path, err, head.String())
	}
	return src, nil
}

// argsStruct is the anonymous struct carrying the arguments by name.
func argsStruct(o *genOp, ts func(types.Type) string, skipRaw bool) string {
	var fs []string
	for i, a := range o.Params {
		if skipRaw && i == o.Raw {
			continue
		}
		fs = append(fs, fmt.Sprintf("%s %s `json:\"%s\"`", a.Field, ts(a.Type), a.JSON))
	}
	return "struct{ " + strings.Join(fs, "; ") + " }"
}

func clientMethod(w func(string, ...any), recv string, o *genOp, ts func(types.Type) string) {
	var params, names []string
	for _, a := range o.Params {
		params = append(params, fmt.Sprintf("%s %s", a.Name, ts(a.Type)))
		names = append(names, a.Name)
	}
	sig := "ctx context.Context"
	if len(params) > 0 {
		sig += ", " + strings.Join(params, ", ")
	}
	req := "wire.Request{}"
	switch {
	case o.Raw >= 0:
		var rest []string
		for i, n := range names {
			if i != o.Raw {
				rest = append(rest, n)
			}
		}
		if len(rest) > 0 {
			req = fmt.Sprintf("wire.Request{Raw: %s, Args: %s{%s}}", names[o.Raw], argsStruct(o, ts, true), strings.Join(rest, ", "))
		} else {
			req = fmt.Sprintf("wire.Request{Raw: %s}", names[o.Raw])
		}
	case o.Itself:
		req = fmt.Sprintf("wire.Request{Args: %s}", names[0])
	case len(names) > 0:
		req = fmt.Sprintf("wire.Request{Args: %s{%s}}", argsStruct(o, ts, false), strings.Join(names, ", "))
	}
	if o.Result == nil {
		w("\nfunc (x %s) %s(%s) error {\n\treturn wireErrors.Map(x.c.Call(ctx, %q, %s, nil))\n}\n", recv, o.Method, sig, o.Name, req)
		return
	}
	rt := ts(o.Result)
	w("\nfunc (x %s) %s(%s) (%s, error) {\n\tvar out %s\n\terr := x.c.Call(ctx, %q, %s, &out)\n\treturn out, wireErrors.Map(err)\n}\n", recv, o.Method, sig, rt, rt, o.Name, req)
}

func serverOp(w func(string, ...any), v string, o *genOp, ts func(types.Type) string) {
	w("\t\t%q: ", o.Name)
	serverFunc(w, v, o, ts)
	w(",\n")
}

func serverFunc(w func(string, ...any), v string, o *genOp, ts func(types.Type) string) {
	w("func(ctx context.Context, in *wire.In) (any, error) {\n")
	var call []string
	switch {
	case o.Itself:
		w("\t\t\tvar a %s\n\t\t\tif err := in.Decode(&a); err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n", ts(o.Params[0].Type))
		call = append(call, "a")
	case len(o.Params) > 0:
		if len(o.Params) > 1 || o.Raw < 0 {
			w("\t\t\tvar a %s\n\t\t\tif err := in.Decode(&a); err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n", argsStruct(o, ts, true))
		}
		for i, a := range o.Params {
			if i == o.Raw {
				call = append(call, "in.Body()")
			} else {
				call = append(call, "a."+a.Field)
			}
		}
	}
	args := "ctx"
	if len(call) > 0 {
		args += ", " + strings.Join(call, ", ")
	}
	switch {
	case o.Result == nil:
		w("\t\t\treturn nil, %s.%s(%s)\n\t\t}", v, o.Method, args)
	case o.RawResult:
		w("\t\t\tr, err := %s.%s(%s)\n\t\t\treturn wire.Raw(r), err\n\t\t}", v, o.Method, args)
	default:
		w("\t\t\treturn %s.%s(%s)\n\t\t}", v, o.Method, args)
	}
}
