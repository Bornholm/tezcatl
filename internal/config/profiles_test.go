package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedProfilesParse loads every example profile the way a user
// would. Validation is strict, so an unknown key is a startup error:
// a typo in a shipped example turns into a support question, and the
// comments in these files are documentation that nothing else checks.
func TestShippedProfilesParse(t *testing.T) {
	// The profiles reference the environment variables a real
	// deployment would set, and validation rejects an enabled sink
	// with an empty DSN. Provide them so the test checks the schema
	// rather than the shell it runs in.
	for name, value := range map[string]string{
		"TEZCATL_POSTGRES_DSN":  "postgres://tezcatl@localhost:5432/tezcatl",
		"TEZCATL_WEBHOOK_TOKEN": "token",
		"SCW_COCKPIT_TOKEN":     "token",
	} {
		t.Setenv(name, value)
	}

	dir := filepath.Join("..", "..", "misc", "config")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	profiles := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}

		// itztli has its own configuration schema and its own loader.
		if strings.HasPrefix(name, "itztli") {
			continue
		}

		t.Run(name, func(t *testing.T) {
			if _, err := Load(filepath.Join(dir, name)); err != nil {
				t.Errorf("%+v", err)
			}
		})

		profiles++
	}

	if profiles == 0 {
		t.Fatalf("no profile found in %s", dir)
	}
}
