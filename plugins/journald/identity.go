package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bornholm/tezcatl/plugins/journald/internal/journal"
)

// maxLearnedIdentifiers bounds what the resolver remembers. A host has
// a few dozen identifiers; a much larger number means something is
// generating them, and remembering those is not worth unbounded memory.
const maxLearnedIdentifiers = 1024

// unitResolver names the source of an entry, and remembers which unit
// an identifier belongs to so that an entry which lost its unit still
// lands in the same place.
//
// journald reads the unit from the cgroup of the process that logged.
// A process that exits before it looks, or that wrote through the
// syslog socket, leaves an entry with no unit at all: no _SYSTEMD_UNIT,
// no _SYSTEMD_CGROUP, no _COMM, nothing but the identifier it chose for
// itself. Measured on the dogfooding instance over two days, 161 of
// 10 374 sshd entries arrived that way.
//
// Those 161 lines cannot be attributed from their own content, so the
// resolver attributes them from the other 10 213, which carry both the
// identifier "sshd" and the unit ssh.service. Lowercasing the
// identifier, which is what this did before, only works when the two
// names happen to coincide as they do for cron and CRON; ssh and sshd
// do not, and the stream split into two partitions. The smaller one
// relearned from scratch every template the larger already knew, and
// Drain generalized them differently against its smaller corpus, so
// the markings posted on the real partition matched none of them.
//
// An identifier seen under two different units is left alone: systemd
// itself logs under init, session and user, and folding those together
// would merge sources that are genuinely distinct.
type unitResolver struct {
	path string

	mu        sync.Mutex
	units     map[string]string
	ambiguous map[string]bool
}

// newUnitResolver loads what a previous run learned. An unreadable or
// absent file is not an error: the resolver relearns from the stream.
func newUnitResolver(path string) *unitResolver {
	resolver := &unitResolver{
		path:      path,
		units:     map[string]string{},
		ambiguous: map[string]bool{},
	}

	if path == "" {
		return resolver
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("could not read the learned unit identities", slog.Any("error", err))
		}

		return resolver
	}

	var learned struct {
		Units     map[string]string `json:"units"`
		Ambiguous []string          `json:"ambiguous"`
	}

	if err := json.Unmarshal(data, &learned); err != nil {
		slog.Warn("could not decode the learned unit identities", slog.Any("error", err))

		return resolver
	}

	for identifier, service := range learned.Units {
		resolver.units[identifier] = service
	}

	for _, identifier := range learned.Ambiguous {
		resolver.ambiguous[identifier] = true
	}

	return resolver
}

// resolve names the source of an entry. An entry carrying its unit says
// what the identifier means; an entry without one asks.
func (r *unitResolver) resolve(entry journal.Entry, collapse func(string) string) string {
	service := entry.Service()
	if service != "" {
		service = collapse(service)
	}

	identifier := strings.ToLower(entry.Identifier)
	if identifier == "" {
		return service
	}

	if entry.Unit != "" {
		r.learn(identifier, service)

		return service
	}

	if known := r.lookup(identifier); known != "" {
		return known
	}

	return service
}

func (r *unitResolver) lookup(identifier string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ambiguous[identifier] {
		return ""
	}

	return r.units[identifier]
}

// learn records that an identifier belongs to a unit, and gives up on
// an identifier that turns out to belong to several.
func (r *unitResolver) learn(identifier string, service string) {
	if identifier == "" || service == "" || identifier == service {
		return
	}

	r.mu.Lock()

	switch {
	case r.ambiguous[identifier]:
		r.mu.Unlock()

		return
	case r.units[identifier] == service:
		r.mu.Unlock()

		return
	case r.units[identifier] != "":
		slog.Info("an identifier logs under several units, leaving its entries where they fall",
			slog.String("identifier", identifier),
			slog.String("units", r.units[identifier]+", "+service))

		delete(r.units, identifier)

		r.ambiguous[identifier] = true
	case len(r.units) >= maxLearnedIdentifiers:
		r.mu.Unlock()

		return
	default:
		r.units[identifier] = service
	}

	data, err := r.encode()

	r.mu.Unlock()

	if err != nil {
		slog.Warn("could not encode the learned unit identities", slog.Any("error", err))

		return
	}

	r.save(data)
}

// encode renders the learned identities. The caller holds the lock.
func (r *unitResolver) encode() ([]byte, error) {
	ambiguous := make([]string, 0, len(r.ambiguous))
	for identifier := range r.ambiguous {
		ambiguous = append(ambiguous, identifier)
	}

	return json.Marshal(struct {
		Units     map[string]string `json:"units"`
		Ambiguous []string          `json:"ambiguous"`
	}{Units: r.units, Ambiguous: ambiguous})
}

// save persists the learned identities. Learning happens a handful of
// times per host and then stops, so writing on the spot costs nothing
// and avoids losing a whole run's worth of it to a crash.
func (r *unitResolver) save(data []byte) {
	if r.path == "" {
		return
	}

	if dir := filepath.Dir(r.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			slog.Warn("could not create the unit identity directory", slog.Any("error", err))

			return
		}
	}

	temporary := r.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		slog.Warn("could not write the learned unit identities", slog.Any("error", err))

		return
	}

	if err := os.Rename(temporary, r.path); err != nil {
		slog.Warn("could not replace the learned unit identities", slog.Any("error", err))
	}
}

// identityFile sits next to the cursor: both record what the reader
// learned about a journal it is following, and an operator who gave the
// cursor a home has already said where that belongs.
func identityFile(cursorFile string) string {
	if cursorFile == "" {
		return ""
	}

	return filepath.Join(filepath.Dir(cursorFile), "units.json")
}
