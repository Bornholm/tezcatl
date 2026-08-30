// Package scw reads the Scaleway account through the scw CLI. The CLI
// is used for discovery only, never for data: it knows which containers
// exist and where the observability endpoints are, and it already holds
// the operator's credentials, so tezcatl never has to be told them.
package scw

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
)

// Container is the identity of a serverless container. Everything else
// the CLI returns is deliberately dropped: the container listing also
// carries environment variables, which hold database URLs and API keys
// that have no business entering an observability pipeline.
type Container struct {
	ID          string
	Name        string
	NamespaceID string
	Region      string
	Status      string
}

// DataSource is a Cockpit endpoint: "logs" (Loki) or "metrics"
// (Prometheus).
type DataSource struct {
	Type   string
	URL    string
	Region string
}

// Namespace groups containers; its name is the closest thing Scaleway
// has to an environment.
type Namespace struct {
	ID   string
	Name string
}

// CLI runs scw commands. Path defaults to "scw" on PATH, Profile and
// Region are passed through when set.
type CLI struct {
	Path    string
	Profile string
	Region  string
	// run is swapped in tests.
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func NewCLI(path string, profile string, region string) *CLI {
	if path == "" {
		path = "scw"
	}

	cli := &CLI{Path: path, Profile: profile, Region: region}
	cli.run = cli.exec

	return cli
}

// SetRunner replaces the command execution, for tests that must not
// depend on a configured scw installation.
func SetRunner(c *CLI, run func(ctx context.Context, args ...string) ([]byte, error)) {
	c.run = run
}

func (c *CLI) exec(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{}, args...)
	full = append(full, "-o", "json")

	if c.Profile != "" {
		full = append(full, "--profile", c.Profile)
	}

	cmd := exec.CommandContext(ctx, c.Path, full...)

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			// The CLI reports the real cause on stderr; without it the
			// error is an unhelpful "exit status 1".
			return nil, errors.Wrapf(err, "scw %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}

		return nil, errors.Wrapf(err, "scw %s", strings.Join(args, " "))
	}

	return output, nil
}

// Containers lists the serverless containers of the account, optionally
// restricted to one project.
func (c *CLI) Containers(ctx context.Context, projectID string) ([]Container, error) {
	args := []string{"container", "container", "list"}
	if c.Region != "" {
		args = append(args, "region="+c.Region)
	}

	if projectID != "" {
		args = append(args, "project-id="+projectID)
	}

	output, err := c.run(ctx, args...)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Only the identity fields are decoded; the rest of the payload,
	// secrets included, is never materialized.
	var raw []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		NamespaceID string `json:"namespace_id"`
		Region      string `json:"region"`
		Status      string `json:"status"`
	}

	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, errors.Wrap(err, "malformed container listing")
	}

	containers := make([]Container, 0, len(raw))
	for _, item := range raw {
		containers = append(containers, Container{
			ID:          item.ID,
			Name:        item.Name,
			NamespaceID: item.NamespaceID,
			Region:      item.Region,
			Status:      item.Status,
		})
	}

	return containers, nil
}

// Namespaces lists the container namespaces, used to name environments.
func (c *CLI) Namespaces(ctx context.Context, projectID string) ([]Namespace, error) {
	args := []string{"container", "namespace", "list"}
	if c.Region != "" {
		args = append(args, "region="+c.Region)
	}

	if projectID != "" {
		args = append(args, "project-id="+projectID)
	}

	output, err := c.run(ctx, args...)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var raw []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, errors.Wrap(err, "malformed namespace listing")
	}

	namespaces := make([]Namespace, 0, len(raw))
	for _, item := range raw {
		namespaces = append(namespaces, Namespace{ID: item.ID, Name: item.Name})
	}

	return namespaces, nil
}

// DataSources lists the Cockpit endpoints of the account.
func (c *CLI) DataSources(ctx context.Context, projectID string) ([]DataSource, error) {
	args := []string{"cockpit", "data-source", "list"}
	if c.Region != "" {
		args = append(args, "region="+c.Region)
	}

	if projectID != "" {
		args = append(args, "project-id="+projectID)
	}

	output, err := c.run(ctx, args...)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// The CLI returns a bare array here, but a wrapped object on some
	// versions; accept both rather than break on an upgrade.
	var raw []struct {
		Type   string `json:"type"`
		URL    string `json:"url"`
		Region string `json:"region"`
	}

	if err := json.Unmarshal(output, &raw); err != nil {
		var wrapper struct {
			DataSources []struct {
				Type   string `json:"type"`
				URL    string `json:"url"`
				Region string `json:"region"`
			} `json:"data_sources"`
		}

		if err := json.Unmarshal(output, &wrapper); err != nil {
			return nil, errors.Wrap(err, "malformed data source listing")
		}

		raw = wrapper.DataSources
	}

	sources := make([]DataSource, 0, len(raw))
	for _, item := range raw {
		sources = append(sources, DataSource{Type: item.Type, URL: item.URL, Region: item.Region})
	}

	return sources, nil
}

// Endpoint returns the URL of the first data source of the given type.
func Endpoint(sources []DataSource, kind string) string {
	for _, source := range sources {
		if source.Type == kind {
			return source.URL
		}
	}

	return ""
}
