package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withEnv sets each key=val pair before t runs and restores the previous
// values (or unsets) afterwards.
func withEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		prev, had := os.LookupEnv(k)
		_ = os.Setenv(k, v)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

// withUnset ensures each key is unset for the duration of the test.
func withUnset(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		prev, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// expandEnv

func TestExpandEnv_DoubleQuotedPlaceholder_BasicValue(t *testing.T) {
	withEnv(t, map[string]string{"API_KEY": "topsecret"})

	got := string(expandEnv([]byte(`apiKey: "${API_KEY}"`)))
	want := `apiKey: "topsecret"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DoubleQuotedPlaceholder_ValueWithDoubleQuote(t *testing.T) {
	// The original bug: a value with " breaks the surrounding YAML string.
	// The fix escapes the embedded quote via JSON encoding.
	withEnv(t, map[string]string{"IP_HASH_SALT": `<jQBS[p@gEPf2U6W8$@Kya{j"qU$*msJ`})

	got := string(expandEnv([]byte(`ipHashSalt: "${IP_HASH_SALT}"`)))
	want := `ipHashSalt: "<jQBS[p@gEPf2U6W8$@Kya{j\"qU$*msJ"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DoubleQuotedPlaceholder_ValueWithBackslash(t *testing.T) {
	withEnv(t, map[string]string{"X": `path\\to\\thing`})

	got := string(expandEnv([]byte(`p: "${X}"`)))
	want := `p: "path\\\\to\\\\thing"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DoubleQuotedPlaceholder_ValueWithNewline(t *testing.T) {
	withEnv(t, map[string]string{"X": "line1\nline2"})

	got := string(expandEnv([]byte(`x: "${X}"`)))
	want := `x: "line1\nline2"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DoubleQuotedPlaceholder_UnsetVarBecomesEmptyQuoted(t *testing.T) {
	withUnset(t, "MISSING")

	got := string(expandEnv([]byte(`a: "${MISSING}"`)))
	want := `a: ""`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_SingleQuotedPlaceholder(t *testing.T) {
	withEnv(t, map[string]string{"X": "value 'with' apostrophes"})

	got := string(expandEnv([]byte(`x: '${X}'`)))
	want := `x: 'value ''with'' apostrophes'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_UnquotedPlaceholder_LeavesRawValue(t *testing.T) {
	withEnv(t, map[string]string{"PORT": "8080"})

	got := string(expandEnv([]byte(`port: ${PORT}`)))
	want := `port: 8080`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DefaultWhenUnset(t *testing.T) {
	withUnset(t, "MISSING")

	got := string(expandEnv([]byte(`instance: "${MISSING:-https://pixelfed.de}"`)))
	want := `instance: "https://pixelfed.de"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DefaultIgnoredWhenSet(t *testing.T) {
	withEnv(t, map[string]string{"X": "actual"})

	got := string(expandEnv([]byte(`v: "${X:-fallback}"`)))
	want := `v: "actual"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DefaultIsEmptyOnExplicitEmpty(t *testing.T) {
	withUnset(t, "MISSING")

	got := string(expandEnv([]byte(`v: "${MISSING:-}"`)))
	want := `v: ""`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_DollarDollarEscape(t *testing.T) {
	withUnset(t, "X")

	got := string(expandEnv([]byte(`raw: $${literal}`)))
	want := `raw: ${literal}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_LoneDollarLeftAlone(t *testing.T) {
	got := string(expandEnv([]byte(`amount: $5`)))
	want := `amount: $5`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_MultipleSubstitutionsOneLine(t *testing.T) {
	withEnv(t, map[string]string{"A": "alpha", "B": "beta"})

	got := string(expandEnv([]byte(`pair: "${A}" then "${B}"`)))
	want := `pair: "alpha" then "beta"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_MultilineConfigFragment(t *testing.T) {
	withEnv(t, map[string]string{
		"API_KEY":      "tok-abc",
		"IP_HASH_SALT": `s@lt"with"quote`,
	})

	in := []byte(`security:
  apiKey: "${API_KEY}"
  ipHashSalt: "${IP_HASH_SALT}"
`)
	got := string(expandEnv(in))
	want := `security:
  apiKey: "tok-abc"
  ipHashSalt: "s@lt\"with\"quote"
`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_MalformedPlaceholderEmittedVerbatim(t *testing.T) {
	got := string(expandEnv([]byte(`x: ${} y`)))
	want := `x: ${} y`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_UnclosedPlaceholderEmittedVerbatim(t *testing.T) {
	got := string(expandEnv([]byte(`x: ${UNCLOSED hello`)))
	want := `x: ${UNCLOSED hello`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnv_MismatchedQuotesNoSwallow(t *testing.T) {
	// `"${X}'` — preceding double-quote, trailing single-quote — should NOT
	// be treated as a wrapping pair. Raw substitution into the YAML stream.
	withEnv(t, map[string]string{"X": "v"})

	got := string(expandEnv([]byte(`x: "${X}'`)))
	want := `x: "v'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// -----------------------------------------------------------------------------
// parsePlaceholder

func TestParsePlaceholder_Variants(t *testing.T) {
	cases := []struct {
		spec        string
		wantName    string
		wantDefault string
		wantHas     bool
	}{
		{"VAR", "VAR", "", false},
		{"VAR:-default", "VAR", "default", true},
		{"VAR:-", "VAR", "", true},
		{"  VAR  ", "VAR", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			name, def, has := parsePlaceholder(tc.spec)
			if name != tc.wantName || def != tc.wantDefault || has != tc.wantHas {
				t.Fatalf("parsePlaceholder(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tc.spec, name, def, has, tc.wantName, tc.wantDefault, tc.wantHas)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// loadDotenv

func TestLoadDotenv_BasicAndQuotedValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	contents := strings.Join([]string{
		`# a comment`,
		``,
		`PLAIN=hello`,
		`DOUBLE="value with spaces"`,
		`SINGLE='no $expansion here'`,
		`export EXPORTED=true`,
		`EMPTY=`,
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	withUnset(t, "PLAIN", "DOUBLE", "SINGLE", "EXPORTED", "EMPTY")
	loadDotenv(path)

	checks := map[string]string{
		"PLAIN":    "hello",
		"DOUBLE":   "value with spaces",
		"SINGLE":   "no $expansion here",
		"EXPORTED": "true",
		"EMPTY":    "",
	}
	for k, want := range checks {
		got, ok := os.LookupEnv(k)
		if !ok || got != want {
			t.Errorf("after loadDotenv: %s = %q (set=%v), want %q (set=true)", k, got, ok, want)
		}
	}
}

func TestLoadDotenv_DoesNotOverwriteExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(`X=fromfile`), 0o644); err != nil {
		t.Fatal(err)
	}

	withEnv(t, map[string]string{"X": "fromos"})
	loadDotenv(path)

	if got := os.Getenv("X"); got != "fromos" {
		t.Fatalf("X = %q, want %q (OS env should win over .env)", got, "fromos")
	}
}

func TestLoadDotenv_MissingFileIsNoOp(t *testing.T) {
	loadDotenv(filepath.Join(t.TempDir(), "does-not-exist"))
	// Just confirming no panic; nothing else to assert.
}
