package config

import (
	"bufio"
	"bytes"
	"log"
	"os"
	"strings"
)

// loadDotenv reads a .env-style file at path and sets each KEY=VALUE pair as
// an OS env var unless the key is already present in the environment. Missing
// files are silently ignored (so callers can probe multiple candidate paths).
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip an optional leading `export ` for shell-script compatibility.
		line = strings.TrimPrefix(line, "export ")

		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			log.Printf("config: %s:%d: ignoring line without '='", path, lineNum)
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])

		// Strip wrapping single- or double-quotes if present.
		if len(val) >= 2 {
			first, last := val[0], val[len(val)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		if _, present := os.LookupEnv(key); present {
			continue
		}
		_ = os.Setenv(key, val)
	}
}

// expandEnv substitutes ${VAR} and ${VAR:-default} placeholders in the input
// against the current OS environment. `$$` collapses to a literal `$`.
// Any other `$...` form is left untouched.
//
// Unset variables without a default substitute to the empty string and emit
// a single warning per occurrence so the operator notices the gap; defaults
// substitute silently.
func expandEnv(input []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(input))

	for i := 0; i < len(input); {
		c := input[i]
		if c != '$' {
			out.WriteByte(c)
			i++
			continue
		}
		// At a '$'. Decide what comes next.
		if i+1 < len(input) && input[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		if i+1 >= len(input) || input[i+1] != '{' {
			// Lone $, not followed by '{' — leave verbatim.
			out.WriteByte('$')
			i++
			continue
		}
		// Find the closing '}'.
		end := bytes.IndexByte(input[i+2:], '}')
		if end < 0 {
			// Unclosed placeholder — emit verbatim and stop scanning further.
			out.Write(input[i:])
			break
		}
		spec := string(input[i+2 : i+2+end])
		i += 2 + end + 1

		name, def, hasDefault := parsePlaceholder(spec)
		if name == "" {
			// Malformed (e.g. `${}`) — emit verbatim to make the issue visible.
			out.WriteString("${")
			out.WriteString(spec)
			out.WriteByte('}')
			continue
		}
		val, ok := os.LookupEnv(name)
		switch {
		case ok && val != "":
			out.WriteString(val)
		case hasDefault:
			out.WriteString(def)
		default:
			log.Printf("config: env var %q referenced by config.yaml but unset", name)
			// substitute empty string
		}
	}
	return out.Bytes()
}

// parsePlaceholder splits the inside of a `${...}` form into (name, default, hasDefault).
//   "VAR"               → ("VAR", "", false)
//   "VAR:-some default" → ("VAR", "some default", true)
//   ""                  → ("", "", false)
func parsePlaceholder(spec string) (name, def string, hasDefault bool) {
	if idx := strings.Index(spec, ":-"); idx >= 0 {
		return strings.TrimSpace(spec[:idx]), spec[idx+2:], true
	}
	return strings.TrimSpace(spec), "", false
}
