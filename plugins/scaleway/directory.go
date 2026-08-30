package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/plugins/scaleway/internal/scw"
	"github.com/pkg/errors"
)

// directory maps the identifiers carried by Cockpit data back to names
// a person recognizes. Cockpit knows a container as a UUID and as a
// generated domain name ("psevetdevb18e955c-pse-vet-server"); the scw
// CLI knows it as "pse-vet-server" in namespace "psevetdev". Services
// and environments are named after the latter.
type directory struct {
	cli         *scw.CLI
	projectID   string
	environment string

	mu         sync.RWMutex
	byID       map[string]entry
	namespaces map[string]string
}

type entry struct {
	name        string
	environment string
}

func newDirectory(cli *scw.CLI, projectID string, environment string) *directory {
	return &directory{
		cli:         cli,
		projectID:   projectID,
		environment: environment,
		byID:        map[string]entry{},
		namespaces:  map[string]string{},
	}
}

// refresh reloads the container listing. Containers are created and
// deleted while the plugin runs, so this repeats.
func (d *directory) refresh(ctx context.Context) error {
	namespaces, err := d.cli.Namespaces(ctx, d.projectID)
	if err != nil {
		// A namespace listing is a convenience, not a requirement: the
		// containers alone are enough to name services.
		slog.Warn("could not list scaleway namespaces", slog.Any("error", err))

		namespaces = nil
	}

	containers, err := d.cli.Containers(ctx, d.projectID)
	if err != nil {
		return errors.WithStack(err)
	}

	names := map[string]string{}
	for _, namespace := range namespaces {
		names[namespace.ID] = namespace.Name
	}

	byID := make(map[string]entry, len(containers))
	for _, container := range containers {
		byID[container.ID] = entry{
			name:        container.Name,
			environment: d.environmentOf(names[container.NamespaceID]),
		}
	}

	d.mu.Lock()
	d.byID = byID
	d.namespaces = names
	d.mu.Unlock()

	return nil
}

func (d *directory) environmentOf(namespace string) string {
	if d.environment != "" {
		return d.environment
	}

	if namespace != "" {
		return namespace
	}

	return "scaleway"
}

func (d *directory) run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		case <-ticker.C:
			if err := d.refresh(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("could not refresh the scaleway container listing", slog.Any("error", err))
			}
		}
	}
}

// identify names the service and environment of a container. A
// container the listing does not know (created since the last refresh,
// or outside the configured project) falls back to the name Cockpit
// carries, so its logs are still attributed to something readable.
func (d *directory) identify(resourceID string, resourceName string) (string, string) {
	d.mu.RLock()
	known, exists := d.byID[resourceID]
	d.mu.RUnlock()

	if exists && known.name != "" {
		return known.name, known.environment
	}

	return fallbackName(resourceName, resourceID), d.environmentOf("")
}

// fallbackName strips the namespace prefix Scaleway prepends to the
// generated domain name ("psevetdevb18e955c-pse-vet-server"), leaving
// something closer to the container's own name.
func fallbackName(resourceName string, resourceID string) string {
	if resourceName == "" {
		if resourceID == "" {
			return "unknown"
		}

		return resourceID
	}

	if _, rest, found := strings.Cut(resourceName, "-"); found && rest != "" {
		return rest
	}

	return resourceName
}
