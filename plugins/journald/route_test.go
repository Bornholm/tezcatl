package main

import "testing"

func TestRouteMessage(t *testing.T) {
	// The rule the dogfooding instance needs: BuildKit progress lines
	// leave the session scope they happen to run in.
	routes, err := compileRoutes([]routeSpec{
		{Match: `^#[0-9]+ `, Service: "build"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for message, want := range map[string]string{
		"#12 2.345 go: downloading github.com/pkg/errors v0.9.1": "build",
		"#9 DONE 0.1s":                         "build",
		"#16 sha256:abcdef 1.2MB / 3.4MB done": "build",
		// Everything else keeps the unit it came from.
		"Disconnected from user root 10.0.0.1 port 22":    "session",
		"Received disconnect from 10.0.0.1 port 22:11":    "session",
		"pam_unix(sshd:session): session opened for root": "session",
		// Close to the shape, but not it: no digits after the hash.
		"#hashtag not a build step": "session",
	} {
		if got := routeMessage(routes, "session", message); got != want {
			t.Errorf("routeMessage(%q) = %q, want %q", message, got, want)
		}
	}
}

func TestRoutesAreOptionalAndValidated(t *testing.T) {
	routes, err := compileRoutes(nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := routeMessage(routes, "session", "anything at all"); got != "session" {
		t.Errorf("with no route the service must not change, got %q", got)
	}

	if _, err := compileRoutes([]routeSpec{{Match: "[", Service: "build"}}); err == nil {
		t.Error("a malformed pattern must be refused at startup, not per line")
	}

	if _, err := compileRoutes([]routeSpec{{Match: "ok", Service: ""}}); err == nil {
		t.Error("a route with no destination must be refused")
	}
}
