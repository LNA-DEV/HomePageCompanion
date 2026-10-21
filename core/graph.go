package core

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/storage"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/conf"
)

// instance is one running (or failed) instance.
type instance struct {
	id, block, name string
	info            companion.Info
	values          map[string]any      // config without the slot keys
	slots           map[string][]string // slot → connected instance IDs
	value           companion.Instance
	ctx             *instCtx
	running         bool
	err             string
	startedAt       time.Time
	service         *serviceInstance // set for an instance of a service
}

// provides reports whether the instance offers the capability, in a
// compatible version (same name and major).
func provides(info companion.Info, svc *serviceInstance, cap companion.Capability) bool {
	if svc != nil {
		return svc.offers(cap)
	}
	for _, p := range info.Provides {
		if p.Name == cap.Name && p.Major == cap.Major {
			return true
		}
	}
	return false
}

// provide returns the implementation of cap.
func (in *instance) provide(cap companion.Capability) (any, error) {
	if !provides(in.info, in.service, cap) {
		return nil, fmt.Errorf("%s does not offer %s", in.id, cap)
	}
	if in.service != nil {
		return in.service.adapter(cap)
	}
	if cap.Type != nil && !reflect.TypeOf(in.value).Implements(cap.Type) {
		return nil, fmt.Errorf("%s (%T) does not implement %s", in.id, in.value, cap)
	}
	return in.value, nil
}

// planned is an instance as the config describes it, before it exists.
type planned struct {
	id, block, name string
	info            companion.Info
	values          map[string]any
	slots           map[string][]string
	service         *conf.ServiceBlock
	auto            bool // added by the core (the storage default)
}

// deps are the instances it is connected to.
func (p *planned) deps() []string {
	var out []string
	for _, ids := range p.slots {
		out = append(out, ids...)
	}
	sort.Strings(out)
	return out
}

// plan turns the config into instances in start order: storage first, then
// every instance after the ones it is connected to. It checks everything it
// can without starting anything.
func (c *Core) plan(f *conf.File) ([]*planned, error) {
	var ps []*planned
	byID := map[string]*planned{}
	var errs []string
	for _, ci := range f.Instances() {
		p := &planned{id: ci.ID(), block: ci.Block, name: ci.Name}
		if !companion.ValidInstanceName(ci.Name) {
			errs = append(errs, fmt.Sprintf("%s: instance names are lower case a-z, 0-9, '.', '_' and '-' (%q would do)", p.id, companion.LegacyName(ci.Name)))
			continue
		}
		if ci.Service {
			sb := f.Services[ci.Block]
			p.service = &sb
			p.info = serviceInfo(ci.Block)
			p.values = ci.Values
			_, p.slots, _ = splitSlots(p.info, ci.Values)
		} else {
			info, ok := companion.Lookup(ci.Block)
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: module %q is not compiled in (add it to build.yaml)", p.id, ci.Block))
				continue
			}
			p.info = info
			rest, slots, err := splitSlots(info, ci.Values)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", p.id, err))
				continue
			}
			p.values, p.slots = rest, slots
		}
		ps = append(ps, p)
		byID[p.id] = p
	}

	// Exactly one database module and at most one blob module. Without a
	// configured one, the only compiled-in one is used with defaults.
	for _, st := range []struct {
		cap      companion.Capability
		ref      string
		required bool
	}{
		{storage.SQLV1, f.Core.Database, true},
		{storage.BlobV1, f.Core.Blobs, false},
	} {
		var have []*planned
		for _, p := range ps {
			if provides(p.info, nil, st.cap) {
				have = append(have, p)
			}
		}
		if st.ref != "" {
			b, n := conf.Ref(st.ref)
			id := companion.InstanceID(b, n)
			if p, ok := byID[id]; !ok || !provides(p.info, nil, st.cap) {
				errs = append(errs, fmt.Sprintf("core: %s is not a configured %s instance", id, st.cap))
			}
			continue
		}
		if len(have) > 1 {
			var ids []string
			for _, p := range have {
				ids = append(ids, p.id)
			}
			errs = append(errs, fmt.Sprintf("core: several %s instances (%s), name one in core.%s", st.cap, strings.Join(ids, ", "), map[bool]string{true: "database", false: "blobs"}[st.required]))
			continue
		}
		if len(have) == 1 {
			continue
		}
		var compiled []companion.Info
		for _, info := range companion.Registered() {
			if provides(info, nil, st.cap) {
				compiled = append(compiled, info)
			}
		}
		if len(compiled) == 1 {
			p := &planned{id: companion.InstanceID(compiled[0].ID, companion.DefaultInstance), block: compiled[0].ID,
				name: companion.DefaultInstance, info: compiled[0], values: map[string]any{}, slots: map[string][]string{}, auto: true}
			ps = append(ps, p)
			byID[p.id] = p
			continue
		}
		if st.required {
			if len(compiled) == 0 {
				errs = append(errs, "core: no database module compiled in (add db-sqlite to build.yaml)")
			} else {
				errs = append(errs, "core: several database modules compiled in, configure one under modules:")
			}
		}
	}

	// Connections: every slot points at an existing instance offering the
	// capability; required slots are connected.
	for _, p := range ps {
		for _, s := range p.info.Slots {
			ids := p.slots[s.Name]
			if len(ids) == 0 && !s.Optional {
				errs = append(errs, fmt.Sprintf("%s: slot %s (%s) is not connected", p.id, s.Name, s.Needs))
			}
			for _, id := range ids {
				q, ok := byID[id]
				if !ok {
					errs = append(errs, fmt.Sprintf("%s: slot %s: no instance %s", p.id, s.Name, id))
					continue
				}
				if !provides(q.info, serviceOf(q), s.Needs) {
					errs = append(errs, fmt.Sprintf("%s: slot %s: %s does not offer %s", p.id, s.Name, id, s.Needs))
				}
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return order(ps)
}

func serviceOf(p *planned) *serviceInstance {
	if p.service == nil {
		return nil
	}
	return &serviceInstance{block: p.block, manifest: manifestFor(p.block)}
}

// order sorts the instances so each comes after the ones it is connected
// to; ties go by Info.Order, then by ID.
func order(ps []*planned) ([]*planned, error) {
	byID := map[string]*planned{}
	indeg := map[string]int{}
	users := map[string][]string{}
	for _, p := range ps {
		byID[p.id] = p
		indeg[p.id] += 0
		for _, d := range p.deps() {
			indeg[p.id]++
			users[d] = append(users[d], p.id)
		}
	}
	less := func(a, b string) bool {
		pa, pb := byID[a], byID[b]
		if pa.info.Order != pb.info.Order {
			return pa.info.Order < pb.info.Order
		}
		return a < b
	}
	var ready []string
	for id, n := range indeg {
		if n == 0 {
			ready = append(ready, id)
		}
	}
	var out []*planned
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
		id := ready[0]
		ready = ready[1:]
		out = append(out, byID[id])
		for _, u := range users[id] {
			indeg[u]--
			if indeg[u] == 0 {
				ready = append(ready, u)
			}
		}
	}
	if len(out) != len(ps) {
		var cyc []string
		for id, n := range indeg {
			if n > 0 {
				cyc = append(cyc, id)
			}
		}
		sort.Strings(cyc)
		return nil, fmt.Errorf("connections form a cycle: %s", strings.Join(cyc, ", "))
	}
	return out, nil
}

// sameAs reports whether a running instance matches its planned successor,
// so a reload can keep it.
func (in *instance) sameAs(p *planned) bool {
	return in.block == p.block && in.name == p.name &&
		reflect.DeepEqual(normalize(in.values), normalize(p.values)) &&
		reflect.DeepEqual(normalizeSlots(in.slots), normalizeSlots(p.slots))
}

func normalize(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func normalizeSlots(m map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		if len(v) > 0 {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
