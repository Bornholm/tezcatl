package plugin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/errors"
)

// Prefix is the naming convention of source plugin binaries.
const Prefix = "tezcatl-source-"

// DefaultDir is where packages install plugins; the tezcatl plugin
// command manages it.
const DefaultDir = "/usr/lib/tezcatl/plugins"

// Dir resolves the plugins directory: explicit value, then the
// TEZCATL_PLUGINS_DIR environment variable, then the default.
func Dir(explicit string) string {
	if explicit != "" {
		return explicit
	}

	if env := os.Getenv("TEZCATL_PLUGINS_DIR"); env != "" {
		return env
	}

	return DefaultDir
}

// Discover lists the source plugins installed in dir, by name. A missing
// directory is not an error: there are simply no plugins.
func Discover(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}

		return nil, errors.WithStack(err)
	}

	plugins := map[string]string{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), Prefix) {
			continue
		}

		info, err := entry.Info()
		if err != nil || info.Mode()&0o111 == 0 {
			continue
		}

		name := strings.TrimPrefix(entry.Name(), Prefix)
		plugins[name] = filepath.Join(dir, entry.Name())
	}

	return plugins, nil
}

// Lookup resolves one source plugin binary by name.
func Lookup(dir string, name string) (string, error) {
	plugins, err := Discover(dir)
	if err != nil {
		return "", errors.WithStack(err)
	}

	path, exists := plugins[name]
	if !exists {
		available := make([]string, 0, len(plugins))
		for name := range plugins {
			available = append(available, name)
		}
		sort.Strings(available)

		return "", errors.Errorf("no source plugin %q in %s (installed: %s)", name, dir, strings.Join(available, ", "))
	}

	return path, nil
}
