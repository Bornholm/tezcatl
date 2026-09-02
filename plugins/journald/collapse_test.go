package main

import "testing"

func TestCollapseTransient(t *testing.T) {
	for name, want := range map[string]string{
		// The units that made two thirds of the learned templates on
		// the dogfooding instance.
		"session-2174":                      "session",
		"session-1946":                      "session",
		"session-c1":                        "session",
		"user@1001":                         "user",
		"user@0":                            "user",
		"user-1001":                         "user",
		"run-user-1000":                     "run-user",
		"docker-3f2b1c9d8e7a6b5c4d3e2f10":   "docker",
		"run-docker-netns-4a5b6c7d8e9f":     "run-docker-netns",
		"systemd-fsck@dev-disk-by\\x2duuid": "systemd-fsck",

		// Stable identities an operator chose or recognizes: these
		// must survive untouched, instance included.
		"tezcatl-ingest@blog":  "tezcatl-ingest@blog",
		"tezcatl-server":       "tezcatl-server",
		"nginx":                "nginx",
		"sshd":                 "sshd",
		"cron":                 "cron",
		"dokku-retire":         "dokku-retire",
		"getty@tty1":           "getty@tty1",
		"blog":                 "blog",
		"leash-toolbox":        "leash-toolbox",
		"user-service-manager": "user-service-manager",
		// Close to a transient pattern, but not one: a session named
		// after something rather than numbered.
		"session-manager": "session-manager",
	} {
		if got := collapseTransient(name); got != want {
			t.Errorf("collapseTransient(%q) = %q, want %q", name, got, want)
		}
	}
}
