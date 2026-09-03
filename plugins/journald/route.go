package main

import (
	"regexp"

	"github.com/pkg/errors"
)

// A collapsed unit names a kind of thing, and sometimes that kind hosts
// more than one activity. On the dogfooding instance every "dokku
// deploy" arrives over SSH, so the whole container build runs inside a
// login session scope: "session" ended up holding both the connection
// lines of an operator and the progress output of BuildKit, 34 of its
// 102 templates. Baselines learned from that mix predict neither, and
// every deployment showed up as a frequency spike by construction.
//
// A route splits such a partition by what the line says rather than by
// where it came from. There is no default: which activities share a
// unit is a property of the machine, not of journald.
type route struct {
	pattern *regexp.Regexp
	service string
}

type routeSpec struct {
	Match   string `json:"match"`
	Service string `json:"service"`
}

func compileRoutes(specs []routeSpec) ([]route, error) {
	routes := make([]route, 0, len(specs))

	for i, spec := range specs {
		if spec.Service == "" {
			return nil, errors.Errorf("route %d has no service", i)
		}

		pattern, err := regexp.Compile(spec.Match)
		if err != nil {
			return nil, errors.Wrapf(err, "route %d (%q)", i, spec.Match)
		}

		routes = append(routes, route{pattern: pattern, service: spec.Service})
	}

	return routes, nil
}

// routeMessage returns the service a line belongs to, first match
// winning, or the service it already had when no route claims it.
func routeMessage(routes []route, service string, message string) string {
	for _, route := range routes {
		if route.pattern.MatchString(message) {
			return route.service
		}
	}

	return service
}
