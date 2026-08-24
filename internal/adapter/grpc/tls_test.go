package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/model"
	"golang.org/x/sync/errgroup"
)

// selfSignedCert writes a localhost certificate and key to dir and
// returns their paths.
func selfSignedCert(t *testing.T, dir string) (certFile string, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tezcatl-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	return certFile, keyFile
}

func TestClientServerTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := selfSignedCert(t, dir)

	// Reserve a port.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	target := fmt.Sprintf("tls://127.0.0.1:%d", port)

	withTLS, err := WithTLS(certFile, keyFile)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	server := NewServerIngester([]string{target}, withTLS)

	received := make(chan model.Observation, 8)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serverCtx, stopServer := context.WithCancel(ctx)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		err := server.Ingest(serverCtx, received)
		if serverCtx.Err() != nil {
			return nil
		}

		return err
	})

	time.Sleep(200 * time.Millisecond)

	observations := make(chan model.Observation, 1)
	observations <- model.Observation{
		ID:       "tls-obs",
		Service:  "api",
		Modality: model.ModalityLog,
		Log:      &model.LogRecord{Raw: "over tls"},
	}
	close(observations)

	client := NewClient(target, ClientWithCA(certFile))

	g.Go(func() error {
		defer stopServer()

		return client.Forward(gctx, observations)
	})

	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}

	select {
	case obs := <-received:
		if obs.ID != "tls-obs" || obs.Log == nil || obs.Log.Raw != "over tls" {
			t.Fatalf("mangled observation: %+v", obs)
		}
	default:
		t.Fatal("expected the observation to be received over tls")
	}
}

func TestListenRequiresCertificateForTLS(t *testing.T) {
	if _, err := Listen("tls://127.0.0.1:0", nil); err == nil {
		t.Fatal("expected tls target without certificate to be rejected")
	}
}
