package main

import (
	"path/filepath"
	"testing"

	"github.com/bornholm/tezcatl/plugins/journald/internal/journal"
)

func TestUnitResolverFoldsAnOrphanEntryIntoItsUnit(t *testing.T) {
	resolver := newUnitResolver("")

	withUnit := journal.Entry{Unit: "ssh.service", Identifier: "sshd"}
	if service := resolver.resolve(withUnit, collapseTransient); service != "ssh" {
		t.Fatalf("expected ssh, got %q", service)
	}

	// The same daemon, logging after its cgroup was reaped: only the
	// identifier survives, and it must not open a partition of its own.
	orphan := journal.Entry{Identifier: "sshd"}
	if service := resolver.resolve(orphan, collapseTransient); service != "ssh" {
		t.Fatalf("expected the orphan entry to fold into ssh, got %q", service)
	}
}

func TestUnitResolverKeepsAnUnknownIdentifier(t *testing.T) {
	resolver := newUnitResolver("")

	if service := resolver.resolve(journal.Entry{Identifier: "CRON"}, collapseTransient); service != "cron" {
		t.Fatalf("expected cron, got %q", service)
	}
}

func TestUnitResolverLeavesAnAmbiguousIdentifierAlone(t *testing.T) {
	resolver := newUnitResolver("")

	// systemd logs under init, and under every login session scope.
	resolver.resolve(journal.Entry{Unit: "init.scope", Identifier: "systemd"}, collapseTransient)
	resolver.resolve(journal.Entry{Unit: "session-4477.scope", Identifier: "systemd"}, collapseTransient)

	if service := resolver.resolve(journal.Entry{Identifier: "systemd"}, collapseTransient); service != "systemd" {
		t.Fatalf("expected an ambiguous identifier to stay put, got %q", service)
	}
}

func TestUnitResolverPersistsWhatItLearned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "units.json")

	resolver := newUnitResolver(path)
	resolver.resolve(journal.Entry{Unit: "ssh.service", Identifier: "sshd"}, collapseTransient)
	resolver.resolve(journal.Entry{Unit: "init.scope", Identifier: "systemd"}, collapseTransient)
	resolver.resolve(journal.Entry{Unit: "session-4477.scope", Identifier: "systemd"}, collapseTransient)

	restarted := newUnitResolver(path)

	if service := restarted.resolve(journal.Entry{Identifier: "sshd"}, collapseTransient); service != "ssh" {
		t.Fatalf("expected the learned identity to survive a restart, got %q", service)
	}

	if service := restarted.resolve(journal.Entry{Identifier: "systemd"}, collapseTransient); service != "systemd" {
		t.Fatalf("expected the ambiguity to survive a restart, got %q", service)
	}
}

func TestIdentityFileSitsNextToTheCursor(t *testing.T) {
	if got := identityFile("/var/lib/tezcatl-journald/cursor"); got != "/var/lib/tezcatl-journald/units.json" {
		t.Fatalf("unexpected identity file: %q", got)
	}

	if got := identityFile(""); got != "" {
		t.Fatalf("expected no identity file without a cursor, got %q", got)
	}
}
