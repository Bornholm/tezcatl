package grpc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestListenUnixSocket covers the local ingestion socket contract:
// world-writable (dokku hooks and per-app units run as other users),
// stale sockets from a crashed process are replaced, regular files are
// not clobbered.
func TestListenUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tezcatl.sock")
	target := "unix://" + path

	listener, err := Listen(target, nil)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if info.Mode().Perm() != 0o666 {
		t.Errorf("expected a world-writable socket, got %v", info.Mode().Perm())
	}

	// Leave a stale socket behind, as after a crash.
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	listener.Close()

	relisten, err := Listen(target, nil)
	if err != nil {
		t.Fatalf("expected the stale socket to be replaced, got %+v", err)
	}
	relisten.Close()

	// A regular file at the socket path is somebody else's data.
	occupied := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(occupied, []byte("data"), 0o600); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if _, err := Listen("unix://"+occupied, nil); err == nil {
		t.Error("expected listening over a regular file to fail")
	}

	if content, err := os.ReadFile(occupied); err != nil || string(content) != "data" {
		t.Errorf("expected the regular file to be preserved, got %q, %v", content, err)
	}
}
