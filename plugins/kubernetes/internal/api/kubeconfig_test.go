package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeKubeconfig(t *testing.T, dir string, content string) string {
	t.Helper()

	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	return path
}

// TestKubeconfigToken exercises the full path: server URL, inline CA
// data and bearer token all coming from the kubeconfig, TLS verified.
func TestKubeconfigToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer from-kubeconfig" {
			t.Errorf("missing bearer token, got %q", r.Header.Get("Authorization"))
		}

		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	path := writeKubeconfig(t, t.TempDir(), fmt.Sprintf(`
apiVersion: v1
kind: Config
current-context: main
clusters:
  - name: main
    cluster:
      server: %s
      certificate-authority-data: %s
contexts:
  - name: main
    context: {cluster: main, user: main}
  - name: other
    context: {cluster: main, user: exec-user}
users:
  - name: main
    user:
      token: from-kubeconfig
  - name: exec-user
    user:
      exec: {command: aws}
`, server.URL, base64.StdEncoding.EncodeToString(caPEM)))

	client, err := NewClient(&Config{Kubeconfig: path})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	into := map[string]bool{}
	if err := client.Get(context.Background(), "/api/v1/things", &into); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if !into["ok"] {
		t.Errorf("unexpected response: %v", into)
	}

	// The other context only carries exec credentials: unsupported.
	if _, err := NewClient(&Config{Kubeconfig: path, Context: "other"}); err == nil {
		t.Error("expected an error for exec-based credentials")
	}

	if _, err := NewClient(&Config{Kubeconfig: path, Context: "missing"}); err == nil {
		t.Error("expected an error for an unknown context")
	}
}

// TestKubeconfigClientCertificate authenticates with an inline client
// certificate against a server that requires one.
func TestKubeconfigClientCertificate(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("expected a client certificate")
		}

		fmt.Fprint(w, `{}`)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	t.Cleanup(server.Close)

	certPEM, keyPEM := selfSignedPair(t)

	path := writeKubeconfig(t, t.TempDir(), fmt.Sprintf(`
current-context: main
clusters:
  - name: main
    cluster:
      server: %s
      insecure-skip-tls-verify: true
contexts:
  - name: main
    context: {cluster: main, user: main}
users:
  - name: main
    user:
      client-certificate-data: %s
      client-key-data: %s
`, server.URL, base64.StdEncoding.EncodeToString(certPEM), base64.StdEncoding.EncodeToString(keyPEM)))

	client, err := NewClient(&Config{Kubeconfig: path})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	into := map[string]any{}
	if err := client.Get(context.Background(), "/api/v1/things", &into); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
}

// TestKubeconfigRelativePaths resolves certificate-authority relative
// to the kubeconfig's directory, and lets explicit fields override the
// kubeconfig (like kubectl --server).
func TestKubeconfigRelativePaths(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), selfSignedCA(t), 0o600); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	path := writeKubeconfig(t, dir, `
current-context: main
clusters:
  - name: main
    cluster:
      server: https://cluster.example:6443
      certificate-authority: ca.crt
contexts:
  - name: main
    context: {cluster: main, user: main}
users:
  - name: main
    user:
      token: secret
`)

	if _, err := NewClient(&Config{Kubeconfig: path}); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	// An absent relative CA must fail loudly, not silently skip TLS
	// verification.
	broken := writeKubeconfig(t, t.TempDir(), `
current-context: main
clusters:
  - name: main
    cluster:
      server: https://cluster.example:6443
      certificate-authority: ca.crt
contexts:
  - name: main
    context: {cluster: main, user: main}
`)
	if _, err := NewClient(&Config{Kubeconfig: broken}); err == nil {
		t.Error("expected an error for a missing CA file")
	}

	// Explicit fields overlay the kubeconfig.
	client, err := NewClient(&Config{Kubeconfig: path, Server: "https://other.example:6443", Token: "override"})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if client.base != "https://other.example:6443" || client.token != "override" {
		t.Errorf("expected explicit overrides, got %q / %q", client.base, client.token)
	}
}

// selfSignedPair generates a throwaway client certificate.
func selfSignedPair(t *testing.T) (certPEM []byte, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func selfSignedCA(t *testing.T) []byte {
	t.Helper()

	certPEM, _ := selfSignedPair(t)
	return certPEM
}
