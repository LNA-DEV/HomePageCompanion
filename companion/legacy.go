package companion

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Legacy is a pre-module installation that hpc migrate moves over: its
// companion.db, its data directory and the parts of its config.yaml the
// building blocks need to map old names to instances. Old target names
// become instance names unchanged ("mastodon" of platform mastodon becomes
// mastodon:mastodon), so modules can map rows directly.
type Legacy struct {
	DB      *gorm.DB // the old companion.db, read only
	DataDir string   // the old data/ directory

	Targets     []LegacyTarget
	Connections []LegacyConnection
	Feeds       []LegacyFeed

	// Check is set for "hpc migrate --check": report every step, change
	// nothing.
	Check bool

	report func(string)
}

// LegacyTarget is one entry of the old targets[].
type LegacyTarget struct {
	Name     string
	Platform string
}

// InstanceID is the instance the target became.
func (t LegacyTarget) InstanceID() string { return InstanceID(t.Platform, LegacyName(t.Name)) }

// LegacyName is the instance name an old config name became: lower case,
// every run of other characters than a-z, 0-9, '.', '_' and '-' one '-'
// ("Photo RSS" → "photo-rss"). Names that were valid stay as they were.
func LegacyName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "default"
	}
	return out
}

// ValidInstanceName reports whether a name may name an instance: it is
// used in references ("rss:photos"), URLs and job keys.
func ValidInstanceName(name string) bool {
	return name != "" && LegacyName(name) == name
}

// LegacyConnection is one entry of the old connections[].
type LegacyConnection struct {
	Name, SourceName, TargetName string
}

// LegacyFeed is one entry of the old datasources.rss[].
type LegacyFeed struct {
	Name, FeedURL, ItemType string
}

// NewLegacy is used by the core.
func NewLegacy(db *gorm.DB, dataDir string, check bool, report func(string)) *Legacy {
	return &Legacy{DB: db, DataDir: dataDir, Check: check, report: report}
}

// Reportf records a migration step for the operator.
func (l *Legacy) Reportf(format string, args ...any) {
	if l.report != nil {
		l.report(fmt.Sprintf(format, args...))
	}
}

// HasTable reports whether the old database has the table.
func (l *Legacy) HasTable(name string) bool {
	return l.DB != nil && l.DB.Migrator().HasTable(name)
}

// Target returns the old target with this name.
func (l *Legacy) Target(name string) (LegacyTarget, bool) {
	for _, t := range l.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return LegacyTarget{}, false
}

// TargetOfPlatform returns the first old target on a platform; rows that
// recorded only the platform (publications, interactions) belong to it.
func (l *Legacy) TargetOfPlatform(platform string) (LegacyTarget, bool) {
	for _, t := range l.Targets {
		if t.Platform == platform {
			return t, true
		}
	}
	return LegacyTarget{}, false
}
