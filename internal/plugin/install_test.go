package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginArchive(t *testing.T, binaryName string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	files := map[string]string{
		"README.md": "# fixture",
		binaryName:  "#!/bin/sh\necho fake plugin\n",
	}

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
	}

	tw.Close()
	gz.Close()

	return buf.Bytes()
}

func TestInstallFromGitHubRelease(t *testing.T) {
	archive := pluginArchive(t, "tezcatl-source-kubernetes")

	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  tezcatl-source-kubernetes_1.0.0_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:]))

	mux := http.NewServeMux()

	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/repos/bornholm/tezcatl-source-kubernetes/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"tag_name": "v1.0.0",
			"assets": [
				{"name": "tezcatl-source-kubernetes_1.0.0_darwin_arm64.tar.gz", "browser_download_url": "%[1]s/dl/darwin.tar.gz"},
				{"name": "tezcatl-source-kubernetes_1.0.0_linux_amd64.tar.gz", "browser_download_url": "%[1]s/dl/linux.tar.gz"},
				{"name": "checksums.txt", "browser_download_url": "%[1]s/dl/checksums.txt"}
			]
		}`, server.URL)
	})

	mux.HandleFunc("/dl/linux.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})

	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, checksums)
	})

	dir := filepath.Join(t.TempDir(), "plugins")

	name, err := Install(context.Background(), InstallOptions{
		Repo:       "https://github.com/bornholm/tezcatl-source-kubernetes",
		Dir:        dir,
		OS:         "linux",
		Arch:       "amd64",
		APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if name != "kubernetes" {
		t.Errorf("unexpected plugin name: %q", name)
	}

	installed := filepath.Join(dir, "tezcatl-source-kubernetes")

	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if info.Mode()&0o111 == 0 {
		t.Error("expected the installed plugin to be executable")
	}

	plugins, err := Discover(dir)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if plugins["kubernetes"] != installed {
		t.Errorf("expected the plugin to be discoverable, got %+v", plugins)
	}
}

// TestInstallFromMultiPluginRelease covers the main tezcatl repository
// shape: one release shipping tezcatl itself plus several plugins. The
// plugin name selects the archive; without it, the ambiguity is an
// error listing the choices.
func TestInstallFromMultiPluginRelease(t *testing.T) {
	archive := pluginArchive(t, "tezcatl-source-prometheus")

	mux := http.NewServeMux()

	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/repos/bornholm/tezcatl/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"tag_name": "v1.0.0",
			"assets": [
				{"name": "tezcatl_1.0.0_linux_amd64.tar.gz", "browser_download_url": "%[1]s/dl/tezcatl.tar.gz"},
				{"name": "tezcatl-source-host_1.0.0_linux_amd64.tar.gz", "browser_download_url": "%[1]s/dl/host.tar.gz"},
				{"name": "tezcatl-source-prometheus_1.0.0_linux_amd64.tar.gz", "browser_download_url": "%[1]s/dl/prometheus.tar.gz"}
			]
		}`, server.URL)
	})

	mux.HandleFunc("/dl/prometheus.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})

	name, err := Install(context.Background(), InstallOptions{
		Repo:       "github.com/bornholm/tezcatl",
		Name:       "prometheus",
		Dir:        filepath.Join(t.TempDir(), "plugins"),
		OS:         "linux",
		Arch:       "amd64",
		APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if name != "prometheus" {
		t.Errorf("unexpected plugin name: %q", name)
	}

	if _, err := Install(context.Background(), InstallOptions{
		Repo:       "github.com/bornholm/tezcatl",
		Dir:        t.TempDir(),
		OS:         "linux",
		Arch:       "amd64",
		APIBaseURL: server.URL,
	}); err == nil || !strings.Contains(err.Error(), "several plugins") {
		t.Errorf("expected an ambiguity error, got %v", err)
	}

	if _, err := Install(context.Background(), InstallOptions{
		Repo:       "github.com/bornholm/tezcatl",
		Name:       "unknown",
		Dir:        t.TempDir(),
		OS:         "linux",
		Arch:       "amd64",
		APIBaseURL: server.URL,
	}); err == nil {
		t.Error("expected an error for an unknown plugin name")
	}
}

func TestInstallRejectsChecksumMismatch(t *testing.T) {
	archive := pluginArchive(t, "tezcatl-source-bad")

	mux := http.NewServeMux()

	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/repos/o/tezcatl-source-bad/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"tag_name": "v1.0.0",
			"assets": [
				{"name": "tezcatl-source-bad_1.0.0_linux_amd64.tar.gz", "browser_download_url": "%[1]s/dl/a.tar.gz"},
				{"name": "checksums.txt", "browser_download_url": "%[1]s/dl/checksums.txt"}
			]
		}`, server.URL)
	})

	mux.HandleFunc("/dl/a.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})

	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%064d  tezcatl-source-bad_1.0.0_linux_amd64.tar.gz\n", 0)
	})

	if _, err := Install(context.Background(), InstallOptions{
		Repo:       "o/tezcatl-source-bad",
		Dir:        t.TempDir(),
		OS:         "linux",
		Arch:       "amd64",
		APIBaseURL: server.URL,
	}); err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
}

func TestParseRepo(t *testing.T) {
	cases := map[string][2]string{
		"github.com/owner/repo":             {"owner", "repo"},
		"https://github.com/owner/repo":     {"owner", "repo"},
		"https://github.com/owner/repo.git": {"owner", "repo"},
		"owner/repo":                        {"owner", "repo"},
	}

	for raw, want := range cases {
		owner, repo, err := parseRepo(raw)
		if err != nil || owner != want[0] || repo != want[1] {
			t.Errorf("parseRepo(%q) = %q/%q, %v", raw, owner, repo, err)
		}
	}

	if _, _, err := parseRepo("not-a-repo"); err == nil {
		t.Error("expected malformed repository to be rejected")
	}
}
