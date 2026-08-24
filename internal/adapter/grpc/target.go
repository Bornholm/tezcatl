package grpc

import (
	"net"
	"net/url"

	"github.com/pkg/errors"
)

// parseTarget maps a tezcatl target URL (unix:///path or tcp://host:port)
// to a network/address pair usable with net.Listen, and to a dial target
// usable with grpc.NewClient.
func parseTarget(target string) (network string, address string, err error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", "", errors.Wrapf(err, "malformed target %q", target)
	}

	switch u.Scheme {
	case "unix":
		path := u.Path
		if u.Host != "" {
			// Tolerate unix://relative/path.
			path = u.Host + u.Path
		}

		if path == "" {
			return "", "", errors.Errorf("missing socket path in %q", target)
		}

		return "unix", path, nil

	case "tcp":
		if u.Host == "" {
			return "", "", errors.Errorf("missing host in %q", target)
		}

		return "tcp", u.Host, nil

	default:
		return "", "", errors.Errorf("unsupported scheme %q in %q (expected unix:// or tcp://)", u.Scheme, target)
	}
}

// Listen creates a listener for a tezcatl target URL.
func Listen(target string) (net.Listener, error) {
	network, address, err := parseTarget(target)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return listener, nil
}

// DialTarget maps a tezcatl target URL to a gRPC client target.
func DialTarget(target string) (string, error) {
	network, address, err := parseTarget(target)
	if err != nil {
		return "", errors.WithStack(err)
	}

	switch network {
	case "unix":
		return "unix://" + address, nil
	default:
		return address, nil
	}
}
