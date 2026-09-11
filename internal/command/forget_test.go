package command

import (
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

// Flag parsing stops at the first positional argument, so a --dry-run
// written after the pattern reaches the action as an argument and the
// preview silently becomes the real thing.
func TestForgetRefusesAFlagAfterThePattern(t *testing.T) {
	command := NewForgetCommand()

	err := runCommand(command, "forget", "production/CRON", "--dry-run")
	if err == nil {
		t.Fatal("expected a flag after the pattern to be refused")
	}

	if !strings.Contains(err.Error(), "must come before the pattern") {
		t.Fatalf("unexpected error: %+v", err)
	}
}

func TestForgetRequiresAPattern(t *testing.T) {
	if err := runCommand(NewForgetCommand(), "forget"); err == nil {
		t.Fatal("expected a missing pattern to be refused")
	}
}

// runCommand runs one command in isolation, the way the root app would.
func runCommand(command *cli.Command, arguments ...string) error {
	app := &cli.App{Commands: []*cli.Command{command}}

	return app.Run(append([]string{"tezcatl"}, arguments...))
}
