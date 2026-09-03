package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dotenvLineProblem reports why `just`'s dotenv loader would reject line, or "" if it accepts it.
// It mirrors the one rule that actually bites us: in an UNQUOTED value, whitespace ends the value,
// so anything after it other than a `#` comment is a parse error ("Error parsing line: '<value>'").
func dotenvLineProblem(line string) string {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return ""
	}
	s = strings.TrimPrefix(s, "export ")

	key, val, ok := strings.Cut(s, "=")
	if !ok {
		return "is neither blank, a comment, nor KEY=VALUE"
	}
	if key == "" || strings.TrimSpace(key) != key {
		return "has whitespace around the key"
	}
	if strings.HasPrefix(val, `"`) || strings.HasPrefix(val, "'") {
		return "" // quoted: spaces are fine
	}
	space := strings.IndexAny(val, " \t")
	if space < 0 {
		return ""
	}
	if rest := strings.TrimLeft(val[space:], " \t"); rest == "" || strings.HasPrefix(rest, "#") {
		return "" // trailing whitespace, or a trailing comment
	}
	return "has an unquoted value containing a space — wrap it in double quotes"
}

// The justfile sets `dotenv-load`, and `just setup` copies .env.example to .env. A value that just's
// parser rejects therefore breaks EVERY recipe on a fresh clone, before any recipe body runs — and it
// is invisible to anyone whose .env predates the offending line.
func TestEnvExample_LoadsUnderJustDotenv(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	require.NoError(t, err)

	for i, line := range strings.Split(string(data), "\n") {
		if problem := dotenvLineProblem(line); problem != "" {
			assert.Fail(t, "unparsable .env.example line",
				".env.example:%d %s\n\t%s", i+1, problem, line)
		}
	}
}

func TestDotenvLineProblem(t *testing.T) {
	tests := []struct {
		name string
		line string
		ok   bool
	}{
		{"blank", "", true},
		{"comment holding a space", "# ZWO ASI1600MM", true},
		{"plain value", "ASTRO_FOCAL_MM=740", true},
		{"value then comment", "ASTRO_PIXEL_UM=3.8            # ASI1600MM Pro pixel size", true},
		{"empty value", "ASTRO_NIGHTSCAPE_OSC_SENSOR=", true},
		{"exported", "export SIRIL_BIN=/usr/bin/siril-cli", true},
		{"quoted spaces", `ASTRO_SPCC_SENSOR="ZWO ASI1600MM"`, true},
		{"url with =", "DATABASE_URL=postgres://astro:astro@localhost:5432/db?sslmode=disable", true},
		{"unquoted spaces", "ASTRO_SPCC_SENSOR=ZWO ASI1600MM", false},
		{"space before =", "ASTRO_FOCAL_MM =740", false},
		{"not an assignment", "ZWO ASI1600MM", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problem := dotenvLineProblem(tt.line)
			assert.Equal(t, tt.ok, problem == "", "problem: %q", problem)
		})
	}
}
