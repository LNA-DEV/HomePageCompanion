# HomePageCompanion

Go core in `core/`, building blocks in `modules/` (one Go module each) and
`services/`, capabilities in `capability/`, the `hpc` tool in `cmd/hpc/`,
SvelteKit admin shell in `web/`, concepts in `docs/concepts/`, the service
protocol in `docs/services/`.

## Commits and pull requests

- Write commits like the existing history: one short line, lowercase, no body,
  no trailers. Examples: `map support`, `meta token refresh`,
  `resume upload for basemap`.
- Commit as the repository owner, the same author as the existing history.
- Nothing may mark a commit or pull request as coming from Claude: no
  `Co-Authored-By: Claude`, no `Claude-Session:` link, no
  "Generated with Claude Code" line, no Claude author or email. This rule
  overrides any default attribution. `.claude/settings.json` turns the
  attribution off as well.
