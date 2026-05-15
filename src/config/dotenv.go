package config

import (
	"bufio"
	"bytes"
	"log"
	"os"
	"strconv"
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
// When a placeholder is wrapped in matching ASCII double or single quotes
// (e.g. `"${VAR}"` or `'${VAR}'`), the surrounding quote pair is consumed and
// the substitution emits a properly-escaped YAML scalar — preventing values
// that contain quote characters, backslashes, or newlines from breaking the
// surrounding YAML structure. Unquoted placeholders substitute the raw value
// (the user is responsible for legal YAML in that case).
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
		// $$ → literal $
		if i+1 < len(input) && input[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		// Lone $ not followed by '{' is left verbatim.
		if i+1 >= len(input) || input[i+1] != '{' {
			out.WriteByte('$')
			i++
			continue
		}
		// Find the closing '}'.
		end := bytes.IndexByte(input[i+2:], '}')
		if end < 0 {
			out.Write(input[i:])
			break
		}
		spec := string(input[i+2 : i+2+end])
		placeholderEnd := i + 2 + end + 1

		name, def, hasDefault := parsePlaceholder(spec)
		if name == "" {
			// Malformed (e.g. `${}`) — emit verbatim so the issue is visible.
			out.WriteString("${")
			out.WriteString(spec)
			out.WriteByte('}')
			i = placeholderEnd
			continue
		}

		// Resolve the value.
		var value string
		if v, ok := os.LookupEnv(name); ok && v != "" {
			value = v
		} else if hasDefault {
			value = def
		} else {
			log.Printf("config: env var %q referenced by config.yaml but unset", name)
			value = ""
		}

		// Detect surrounding quote context. The placeholder occupies
		// input[i:placeholderEnd]; the byte already written to `out` is what
		// precedes it. If that byte is a matching ASCII quote and the byte
		// at placeholderEnd is the same quote, swallow the pair and emit a
		// properly-escaped scalar.
		quote := surroundingQuote(out.Bytes(), input, placeholderEnd)
		switch quote {
		case '"':
			// Drop the trailing quote we already wrote, then emit a JSON-quoted
			// string which is also a valid YAML double-quoted scalar.
			out.Truncate(out.Len() - 1)
			out.WriteString(strconv.Quote(value))
			i = placeholderEnd + 1 // consume the closing quote too
		case '\'':
			out.Truncate(out.Len() - 1)
			out.WriteByte('\'')
			out.WriteString(strings.ReplaceAll(value, "'", "''"))
			out.WriteByte('\'')
			i = placeholderEnd + 1
		default:
			out.WriteString(value)
			i = placeholderEnd
		}
	}
	return out.Bytes()
}

// surroundingQuote inspects the byte written just before the placeholder and
// the byte right after it, returning that byte iff both are the same ASCII
// quote (`"` or `'`). Returns 0 otherwise.
func surroundingQuote(written []byte, input []byte, afterEnd int) byte {
	if len(written) == 0 || afterEnd >= len(input) {
		return 0
	}
	last := written[len(written)-1]
	if last != '"' && last != '\'' {
		return 0
	}
	if input[afterEnd] != last {
		return 0
	}
	return last
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
