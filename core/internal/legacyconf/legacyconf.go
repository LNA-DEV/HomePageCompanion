// Package legacyconf turns the pre-module companion's data/config.yaml into
// companion.yaml and build.yaml (docs/concepts/modular-architecture.md
// §5.11). It reads the file as written: ${NAME} placeholders stay
// placeholders, so the secrets stay in .env.
package legacyconf

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gopkg.in/yaml.v3"
)

// Config is the old config.yaml. Every scalar is kept as text: a value may
// be a placeholder where a number or a flag belongs.
type Config struct {
	Security struct {
		ApiKey       string   `yaml:"apiKey"`
		Domain       string   `yaml:"domain"`
		IPHashSalt   string   `yaml:"ipHashSalt"`
		ExtraOrigins []string `yaml:"extraOrigins"`
	} `yaml:"security"`
	Datasources struct {
		Rss []Datasource `yaml:"rss"`
	} `yaml:"datasources"`
	Targets     []Target     `yaml:"targets"`
	Connections []Connection `yaml:"connections"`
	Webpush     struct {
		Subscriber string `yaml:"subscriberMail"`
	} `yaml:"webpush"`
	Microblog struct {
		PublishTo []string `yaml:"publishTo"`
	} `yaml:"microblog"`
	Storage struct {
		BucketURL string `yaml:"bucketUrl"`
		Prefix    string `yaml:"prefix"`
	} `yaml:"storage"`
	Basemap struct {
		Enabled     string `yaml:"enabled"`
		Source      string `yaml:"source"`
		SchemaMajor string `yaml:"schemaMajor"`
		Schedule    string `yaml:"schedule"`
		RetainDays  string `yaml:"retainDays"`
		PublicURL   string `yaml:"publicUrl"`
		PartSizeMiB string `yaml:"partSizeMiB"`
		Concurrency string `yaml:"concurrency"`
		CacheSizeMB string `yaml:"cacheSizeMB"`
	} `yaml:"basemap"`
	Routing struct {
		OSRMURL       string `yaml:"osrmUrl"`
		TransitousURL string `yaml:"transitousUrl"`
	} `yaml:"routing"`
	Gbif struct {
		BaseURL string `yaml:"baseUrl"`
		CacheMB string `yaml:"cacheMB"`
	} `yaml:"gbif"`
}

type Datasource struct {
	Name     string `yaml:"name"`
	FeedURL  string `yaml:"feedUrl"`
	ItemType string `yaml:"itemType"`
}

type Target struct {
	Name             string `yaml:"name"`
	Platform         string `yaml:"platform"`
	PAT              string `yaml:"pat"`
	InstanceURL      string `yaml:"instance"`
	Username         string `yaml:"username"`
	AccessToken      string `yaml:"accessToken"`
	AccountID        string `yaml:"accountId"`
	MaxImageBytes    string `yaml:"maxImageBytes"`
	MaxImageLongEdge string `yaml:"maxImageLongEdge"`
	MaxCaptionLength string `yaml:"maxCaptionLength"`
}

type Connection struct {
	Name              string  `yaml:"name"`
	SourceName        string  `yaml:"sourceName"`
	TargetName        string  `yaml:"targetName"`
	Caption           string  `yaml:"caption"`
	Cron              *string `yaml:"cron"`
	IncludeAltText    string  `yaml:"includeAltText"`
	TagModels         string  `yaml:"tagModels"`
	ModelsPrefix      string  `yaml:"modelsPrefix"`
	ModelsAnd         string  `yaml:"modelsAnd"`
	RoutingTagsSource string  `yaml:"routingTagsSource"`
	AddExifToCaption  string  `yaml:"addExifToCaption"`
	CopyrightSource   string  `yaml:"copyrightSource"`
}

// Parse reads an old config.yaml without expanding it.
func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("config.yaml: %w", err)
	}
	return &c, nil
}

// Result is the converted installation.
type Result struct {
	Companion []byte // companion.yaml
	Build     []byte // build.yaml
	// Modules are the building blocks in use, sorted.
	Modules []string
	// Notes are what the operator should know about the conversion.
	Notes []string
	// The old names, for the modules' Import.
	Targets     []companion.LegacyTarget
	Connections []companion.LegacyConnection
	Feeds       []companion.LegacyFeed
}

// ModulePath is where this repository's module of a building block lives.
func ModulePath(id string) string {
	return "github.com/LNA-DEV/HomePageCompanion/modules/" + strings.ReplaceAll(id, ".", "-")
}

var platforms = map[string]bool{"mastodon": true, "pixelfed": true, "bluesky": true, "instagram": true, "threads": true}

// Convert builds companion.yaml and build.yaml. coreVersion goes into
// build.yaml.
func Convert(c *Config, coreVersion string) (*Result, error) {
	r := &Result{}
	note := func(format string, args ...any) { r.Notes = append(r.Notes, fmt.Sprintf(format, args...)) }
	used := map[string]bool{}
	var modules []kv

	// Names must stay unique once they are instance names.
	seen := map[string]string{}
	name := func(kind, old string) (string, error) {
		n := companion.LegacyName(old)
		if prev, ok := seen[kind+"|"+n]; ok && prev != old {
			return "", fmt.Errorf("%s %q and %q both become the instance name %q: rename one in config.yaml", kind, prev, old, n)
		}
		seen[kind+"|"+n] = old
		if n != old {
			note("%s %q is now the instance %q", kind, old, n)
		}
		return n, nil
	}

	// Storage.
	used["db.sqlite"] = true
	modules = append(modules, kv{k: "db.sqlite", v: empty()})
	blobs := "blob.fs"
	bucket := strings.TrimSpace(c.Storage.BucketURL)
	switch {
	case bucket == "":
		modules = append(modules, kv{k: "blob.fs", v: empty(), comment: "files under data/blobs/"})
	case strings.HasPrefix(bucket, "file://"):
		modules = append(modules, kv{k: "blob.fs", v: config(opt("dir", str(strings.TrimPrefix(bucket, "file://"))), opt("prefix", str(c.Storage.Prefix)))})
	default:
		blobs = "blob.s3"
		modules = append(modules, kv{k: "blob.s3", v: config(opt("bucketUrl", str(bucket)), opt("prefix", str(c.Storage.Prefix))),
			comment: "credentials from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, as before"})
	}
	used[blobs] = true
	used["media"] = true
	modules = append(modules, kv{k: "media", v: empty()})

	// Feeds.
	feeds := map[string]string{} // old name → instance ID
	var rss []kv
	for _, d := range c.Datasources.Rss {
		if strings.TrimSpace(d.ItemType) != "image" {
			note("feed %q (itemType %q) is left out: the old companion only read image feeds", d.Name, d.ItemType)
			continue
		}
		n, err := name("feed", d.Name)
		if err != nil {
			return nil, err
		}
		feeds[d.Name] = "rss:" + n
		rss = append(rss, kv{k: n, v: flow(opt("url", str(d.FeedURL)))})
		r.Feeds = append(r.Feeds, companion.LegacyFeed{Name: d.Name, FeedURL: d.FeedURL, ItemType: d.ItemType})
	}
	if len(rss) > 0 {
		used["rss"] = true
		modules = append(modules, kv{k: "rss", v: instances(rss)})
	}

	// Targets, grouped by platform.
	targets := map[string]string{} // old name → instance ID
	byPlatform := map[string][]kv{}
	var allTargets []string
	for _, t := range c.Targets {
		p := strings.TrimSpace(t.Platform)
		if !platforms[p] {
			note("target %q (platform %q) is left out: no such module", t.Name, t.Platform)
			continue
		}
		n, err := name("target", t.Name)
		if err != nil {
			return nil, err
		}
		id := companion.InstanceID(p, n)
		targets[t.Name] = id
		allTargets = append(allTargets, id)
		r.Targets = append(r.Targets, companion.LegacyTarget{Name: t.Name, Platform: p})
		var fields []kv
		switch p {
		case "mastodon", "pixelfed":
			fields = []kv{opt("instance", str(t.InstanceURL)), opt("token", str(t.PAT)), optNum("maxLength", t.MaxCaptionLength)}
		case "bluesky":
			fields = []kv{opt("username", str(t.Username)), opt("password", str(t.PAT))}
		case "instagram":
			fields = []kv{opt("accountId", str(t.AccountID)), opt("accessToken", str(t.AccessToken)), optNum("maxLength", t.MaxCaptionLength)}
		case "threads":
			fields = []kv{opt("userId", str(t.AccountID)), opt("accessToken", str(t.AccessToken))}
		}
		if p != "instagram" && p != "threads" {
			fields = append(fields, optNum("maxImageBytes", t.MaxImageBytes), optNum("maxImageLongEdge", t.MaxImageLongEdge))
		}
		byPlatform[p] = append(byPlatform[p], kv{k: n, v: flow(fields...)})
	}
	var ps []string
	for p := range byPlatform {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	for _, p := range ps {
		used[p] = true
		modules = append(modules, kv{k: p, v: instances(byPlatform[p])})
	}

	// Connections.
	var conns []kv
	for _, cn := range c.Connections {
		src, ok := feeds[cn.SourceName]
		if !ok {
			note("connection %q is left out: no image feed %q", cn.Name, cn.SourceName)
			continue
		}
		tgt, ok := targets[cn.TargetName]
		if !ok {
			note("connection %q is left out: no target %q", cn.Name, cn.TargetName)
			continue
		}
		n, err := name("connection", cn.Name)
		if err != nil {
			return nil, err
		}
		r.Connections = append(r.Connections, companion.LegacyConnection{Name: cn.Name, SourceName: cn.SourceName, TargetName: cn.TargetName})
		cronSpec := ""
		if cn.Cron != nil {
			cronSpec = *cn.Cron
		} else {
			note("connection %q has no cron: it posts only when started by hand, as before", cn.Name)
		}
		conns = append(conns, kv{k: n, v: mapping(
			kv{k: "source", v: str(src)},
			kv{k: "target", v: str(tgt)},
			kv{k: "media", v: str("media")},
			kv{k: "strategy", v: str("rotate")},
			opt("cron", str(cronSpec)),
			opt("caption", str(cn.Caption)),
			optFlag("includeAltText", cn.IncludeAltText),
			optFlag("tagModels", cn.TagModels),
			opt("modelsPrefix", str(cn.ModelsPrefix)),
			opt("modelsAnd", str(cn.ModelsAnd)),
			opt("routingTagsSource", str(cn.RoutingTagsSource)),
			optFlag("addExifToCaption", cn.AddExifToCaption),
			opt("copyrightSource", str(cn.CopyrightSource)),
		)})
	}
	if len(conns) > 0 {
		used["autoupload"] = true
		modules = append(modules, kv{k: "autoupload", v: instances(conns)})
	}

	// The website's building blocks.
	used["interactions"] = true
	modules = append(modules, kv{k: "interactions", v: config(opt("ipHashSalt", str(c.Security.IPHashSalt)), optList("platforms", allTargets)),
		comment: "likes from every target, and the website's own"})

	var publishTo []string
	for _, t := range c.Microblog.PublishTo {
		id, ok := targets[t]
		if !ok {
			note("microblog: target %q is left out of publishTo: no such target", t)
			continue
		}
		publishTo = append(publishTo, id)
		if !strings.HasPrefix(id, "mastodon:") {
			note("microblog: the old companion published only to Mastodon; posts now go to %s as well", id)
		}
	}
	used["microblog"] = true
	modules = append(modules, kv{k: "microblog", v: config(optList("publishTo", publishTo))})

	used["webpush"] = true
	modules = append(modules, kv{k: "webpush", v: config(opt("subscriber", str(c.Webpush.Subscriber)))})
	used["webmention"] = true
	modules = append(modules, kv{k: "webmention", v: empty()})

	used["routing"], used["trips"] = true, true
	modules = append(modules,
		kv{k: "routing", v: config(opt("osrmUrl", str(c.Routing.OSRMURL)), opt("transitousUrl", str(c.Routing.TransitousURL)))},
		kv{k: "trips", v: config(kv{k: "routing", v: str("routing")}, kv{k: "media", v: str("media")})})

	publicURL := ""
	if b := c.Basemap; isTrue(b.Enabled) {
		if bucket == "" {
			note("basemap: enabled, but there was no storage bucket, so it never ran; it is left out")
		} else {
			used["basemap"] = true
			publicURL = strings.TrimSuffix(strings.TrimRight(b.PublicURL, "/"), "/api/tiles/basemap")
			modules = append(modules, kv{k: "basemap", v: config(
				opt("source", str(b.Source)), optNum("schemaMajor", b.SchemaMajor), opt("schedule", str(b.Schedule)),
				optNum("retainDays", b.RetainDays), opt("publicUrl", str(b.PublicURL)), optNum("partSizeMiB", b.PartSizeMiB),
				optNum("concurrency", b.Concurrency), optNum("cacheSizeMB", b.CacheSizeMB))})
		}
	}
	used["gbif"] = true
	modules = append(modules, kv{k: "gbif", v: config(opt("baseUrl", str(c.Gbif.BaseURL)), optNum("cacheMB", c.Gbif.CacheMB))})

	coreBlock := mapping(
		opt("domain", str(c.Security.Domain)),
		optList("extraOrigins", c.Security.ExtraOrigins),
		opt("adminKey", str(c.Security.ApiKey)),
		opt("publicUrl", str(publicURL)),
		kv{k: "database", v: str("db.sqlite")},
		kv{k: "blobs", v: str(blobs)},
	)
	if publicURL == "" {
		coreBlock.FootComment = "publicUrl: https://companion.example.com  # the companion's public URL, for links to it"
	}
	doc := mapping(kv{k: "core", v: coreBlock}, kv{k: "modules", v: mapping(modules...)})
	doc.HeadComment = "companion.yaml, generated by \"companion migrate\" from the old config.yaml.\n" +
		"What runs and how it is connected; build.yaml says what is compiled in.\n" +
		"Secrets stay in .env; the placeholders read them from there, as before."
	var err error
	if r.Companion, err = encode(doc); err != nil {
		return nil, err
	}

	for id := range used {
		r.Modules = append(r.Modules, id)
	}
	sort.Strings(r.Modules)
	var paths []*yaml.Node
	for _, id := range r.Modules {
		paths = append(paths, str(ModulePath(id)))
	}
	list := &yaml.Node{Kind: yaml.SequenceNode, Content: paths}
	build := mapping(kv{k: "core", v: str(coreVersion)}, kv{k: "modules", v: list})
	build.HeadComment = "build.yaml, generated by \"companion migrate\": what hpc build compiles in.\n" +
		"A module without @version has the core's version; \"path => ./dir\" builds a local one."
	if r.Build, err = encode(build); err != nil {
		return nil, err
	}
	return r, nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// ---- YAML nodes ----

type kv struct {
	k       string
	v       *yaml.Node
	comment string
}

// str is a text value; plain when it can be, quoted when it would read as
// something else.
func str(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// plain is a number, a flag or a placeholder for one.
func plain(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: s} }

func opt(k string, v *yaml.Node) kv {
	if v == nil || v.Kind == yaml.ScalarNode && strings.TrimSpace(v.Value) == "" {
		return kv{}
	}
	return kv{k: k, v: v}
}

func optNum(k, v string) kv {
	if v = strings.TrimSpace(v); v == "" || v == "0" {
		return kv{}
	}
	return kv{k: k, v: plain(v)}
}

func optFlag(k, v string) kv {
	if v = strings.TrimSpace(v); v == "" || strings.EqualFold(v, "false") {
		return kv{}
	}
	return kv{k: k, v: plain(v)}
}

func optList(k string, items []string) kv {
	if len(items) == 0 {
		return kv{}
	}
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, it := range items {
		n.Content = append(n.Content, str(it))
	}
	return kv{k: k, v: n}
}

func mapping(pairs ...kv) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range pairs {
		if p.k == "" {
			continue
		}
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: p.k, LineComment: p.comment}
		n.Content = append(n.Content, key, p.v)
	}
	return n
}

// flow is a mapping in block style; flow style would quote "${N:-1}" and
// turn a number into text once expanded. Only an empty one is written {}.
func flow(pairs ...kv) *yaml.Node {
	n := mapping(pairs...)
	if len(n.Content) == 0 {
		n.Style = yaml.FlowStyle
	}
	return n
}

func empty() *yaml.Node { return flow() }

// config is a building block's default instance.
func config(pairs ...kv) *yaml.Node {
	inner := mapping(pairs...)
	if len(inner.Content) == 0 {
		return empty()
	}
	return mapping(kv{k: "config", v: inner})
}

func instances(named []kv) *yaml.Node {
	return mapping(kv{k: "instances", v: mapping(named...)})
}

func encode(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	enc.Close()
	return buf.Bytes(), nil
}
