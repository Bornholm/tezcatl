package main

import (
	"context"
	"testing"

	"github.com/bornholm/tezcatl/plugins/scaleway/internal/scw"
)

// fakeCLI answers the discovery commands without running scw.
func fakeCLI(t *testing.T, responses map[string]string) *scw.CLI {
	t.Helper()

	cli := scw.NewCLI("", "", "fr-par")
	scw.SetRunner(cli, func(ctx context.Context, args ...string) ([]byte, error) {
		for prefix, body := range responses {
			if len(args) >= 2 && args[0]+" "+args[1] == prefix {
				return []byte(body), nil
			}
		}

		return []byte("[]"), nil
	})

	return cli
}

func TestDirectoryNamesServicesAndEnvironments(t *testing.T) {
	cli := fakeCLI(t, map[string]string{
		"container container": `[{"id":"051f3161","name":"pse-vet-server","namespace_id":"b18e955c","region":"fr-par","status":"ready"}]`,
		"container namespace": `[{"id":"b18e955c","name":"psevetdev"}]`,
	})

	directory := newDirectory(cli, "", "")
	if err := directory.refresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	// The container's own name is the service; its namespace is the
	// environment, as in Kubernetes.
	service, environment := directory.identify("051f3161", "psevetdevb18e955c-pse-vet-server")
	if service != "pse-vet-server" || environment != "psevetdev" {
		t.Errorf("expected pse-vet-server/psevetdev, got %s/%s", service, environment)
	}
}

// TestDirectoryFallsBackOnUnknownContainer covers a container created
// since the last refresh: its logs must still be attributed to
// something readable rather than dropped.
func TestDirectoryFallsBackOnUnknownContainer(t *testing.T) {
	directory := newDirectory(fakeCLI(t, nil), "", "")

	service, environment := directory.identify("unknown-id", "psevetdevb18e955c-pse-vet-server")
	if service != "pse-vet-server" {
		t.Errorf("expected the generated name to be trimmed, got %q", service)
	}

	if environment != "scaleway" {
		t.Errorf("expected a default environment, got %q", environment)
	}

	// With nothing at all to go on, the identifier is better than
	// silence.
	if service, _ := directory.identify("only-id", ""); service != "only-id" {
		t.Errorf("expected the id as a last resort, got %q", service)
	}
}

func TestDirectoryEnvironmentOverride(t *testing.T) {
	cli := fakeCLI(t, map[string]string{
		"container container": `[{"id":"a","name":"api","namespace_id":"n"}]`,
		"container namespace": `[{"id":"n","name":"psevetdev"}]`,
	})

	directory := newDirectory(cli, "", "production")
	if err := directory.refresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if _, environment := directory.identify("a", ""); environment != "production" {
		t.Errorf("expected the configured environment to win, got %q", environment)
	}
}

// TestDirectorySurvivesNamespaceFailure keeps discovery working when
// only part of it is permitted: naming environments is a convenience.
func TestDirectorySurvivesNamespaceFailure(t *testing.T) {
	cli := scw.NewCLI("", "", "")
	scw.SetRunner(cli, func(ctx context.Context, args ...string) ([]byte, error) {
		if args[1] == "namespace" {
			return nil, context.DeadlineExceeded
		}

		return []byte(`[{"id":"a","name":"api","namespace_id":"n"}]`), nil
	})

	directory := newDirectory(cli, "", "")
	if err := directory.refresh(context.Background()); err != nil {
		t.Fatalf("expected containers alone to be enough, got %+v", err)
	}

	if service, _ := directory.identify("a", ""); service != "api" {
		t.Errorf("expected the container name, got %q", service)
	}
}
