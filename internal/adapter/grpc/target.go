package grpc

import (
	"crypto/tls"
	"net"
	"net/url"
	"os"

	"github.com/pkg/errors"
)

// parseTarget maps a tezcatl target URL (unix:///path, tcp://host:port
// or tls://host:port) to a network/address pair usable with net.Listen,
// plus whether the transport must be TLS-encrypted.
func parseTarget(target string) (network string, address string, secure bool, err error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", "", false, errors.Wrapf(err, "malformed target %q", target)
	}

	switch u.Scheme {
	case "unix":
		path := u.Path
		if u.Host != "" {
			// Tolerate unix://relative/path.
			path = u.Host + u.Path
		}

		if path == "" {
			return "", "", false, errors.Errorf("missing socket path in %q", target)
		}

		return "unix", path, false, nil

	case "tcp", "tls":
		if u.Host == "" {
			return "", "", false, errors.Errorf("missing host in %q", target)
		}

		return "tcp", u.Host, u.Scheme == "tls", nil

	default:
		return "", "", false, errors.Errorf("unsupported scheme %q in %q (expected unix://, tcp:// or tls://)", u.Scheme, target)
	}
}

// Listen creates a listener for a tezcatl target URL. tls:// targets are
// wrapped with the given certificate; certificate may be nil for
// plaintext targets.
func Listen(target string, certificate *tls.Certificate) (net.Listener, error) {
	network, address, secure, err := parseTarget(target)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// A socket left over by a crashed process prevents binding; only an
	// actual socket is removed, a regular file still fails the listen.
	if network == "unix" {
		if info, err := os.Stat(address); err == nil && info.Mode()&os.ModeSocket != 0 {
			os.Remove(address)
		}
	}

	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// The unix socket is a local ingestion endpoint shared with other
	// users (dokku deploy hooks, per-app ingest units): world-writable,
	// access is restricted by the parent directory mode when needed.
	if network == "unix" {
		if err := os.Chmod(address, 0o666); err != nil {
			listener.Close()
			return nil, errors.WithStack(err)
		}
	}

	if !secure {
		return listener, nil
	}

	if certificate == nil {
		listener.Close()
		return nil, errors.Errorf("target %q requires a tls certificate (server.tls)", target)
	}

	return tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{*certificate},
		NextProtos:   []string{"h2"},
	}), nil
}

// DialTarget maps a tezcatl target URL to a gRPC client target.
func DialTarget(target string) (string, error) {
	network, address, _, err := parseTarget(target)
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
