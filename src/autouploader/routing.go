package autouploader

import (
	"strings"
)

// RoutingDecision is the result of evaluating meta_skip:/meta_only: tags
// against a single target platform.
type RoutingDecision struct {
	Allow  bool
	Reason string
}

// EvaluateRouting applies the meta_skip:<platform> / meta_only:<platform>
// convention to decide whether a feed item should publish to a given platform.
//
// Rules:
//   - Tags are case-insensitive on both the prefix and the platform name.
//   - meta_skip:<platform> blocks that platform unconditionally.
//   - If any meta_only:<platform> tag is present (for ANY platform), the post
//     is restricted: it only goes to platforms listed in the meta_only set
//     AND not blocked by meta_skip.
//   - Tags that are not meta_skip:/meta_only: are ignored.
//   - When no meta_* tags are present (or no meta_only: tags exist), the post
//     is allowed unless meta_skip explicitly blocks it.
func EvaluateRouting(tags []string, platform string) RoutingDecision {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return RoutingDecision{Allow: true}
	}

	skipSet := map[string]bool{}
	onlySet := map[string]bool{}

	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case strings.HasPrefix(tag, "meta_skip:"):
			p := strings.TrimSpace(tag[len("meta_skip:"):])
			if p != "" {
				skipSet[p] = true
			}
		case strings.HasPrefix(tag, "meta_only:"):
			p := strings.TrimSpace(tag[len("meta_only:"):])
			if p != "" {
				onlySet[p] = true
			}
		}
	}

	if skipSet[platform] {
		return RoutingDecision{Allow: false, Reason: "meta_skip:" + platform + " present"}
	}
	if len(onlySet) > 0 && !onlySet[platform] {
		return RoutingDecision{Allow: false, Reason: "meta_only:* present and excludes " + platform}
	}
	return RoutingDecision{Allow: true}
}
