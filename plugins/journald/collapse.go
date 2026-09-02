package main

import "regexp"

// Transient systemd units carry an identifier that is unique to one
// login, one container or one mount: session-2174.scope, user@1001,
// docker-<hex>.scope. Taken literally, each becomes a service of its
// own, with its own baselines that never get the time to mature, and
// every line it emits is by construction a template nobody has ever
// seen. On the dogfooding instance those units accounted for two
// templates out of three, learned once and never matched again.
//
// Collapsing them names the *kind* of thing instead: every SSH login
// is "session", every user manager is "user". The exact unit is not
// lost, it stays on the observation as journald.unit.
var transientUnits = []struct {
	pattern *regexp.Regexp
	name    string
}{
	// A login session scope: session-2174, and session-c1 for the
	// ones systemd numbers itself. Only numbered names: a unit called
	// session-manager is a real service someone named.
	{regexp.MustCompile(`^session-c?[0-9]+$`), "session"},
	// The per-user manager and its slice: user@1001, user-1001.
	{regexp.MustCompile(`^user@[0-9]+$`), "user"},
	{regexp.MustCompile(`^user-[0-9]+$`), "user"},
	// The per-user runtime mount: run-user-1000.
	{regexp.MustCompile(`^run-user-[0-9]+$`), "run-user"},
	// One scope per container, named by its id.
	{regexp.MustCompile(`^docker-[0-9a-f]{12,}$`), "docker"},
	{regexp.MustCompile(`^run-docker-netns-[0-9a-f]+$`), "run-docker-netns"},
	// One unit per checked device.
	{regexp.MustCompile(`^systemd-fsck@.+$`), "systemd-fsck"},
}

// collapseTransient returns the stable identity behind a unit name, or
// the name itself when it is already stable. A templated unit an
// operator chose, like tezcatl-ingest@blog, is stable: its instance is
// the application, and it comes back every day under the same name.
func collapseTransient(service string) string {
	for _, transient := range transientUnits {
		if transient.pattern.MatchString(service) {
			return transient.name
		}
	}

	return service
}
