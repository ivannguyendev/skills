package tlsconfig_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"example.com/skeleton/internal/tlsconfig"
)

func TestMutualTLS(t *testing.T) {
	dir := t.TempDir()
	ca, caKey := newCert(t, dir, "ca", nil, nil, true)
	newCert(t, dir, "server", ca, caKey, false)
	newCert(t, dir, "client", ca, caKey, false)
	files := func(name string) tlsconfig.Files {
		return tlsconfig.Files{
			CertFile: filepath.Join(dir, name+".crt"),
			KeyFile:  filepath.Join(dir, name+".key"),
			CAFile:   filepath.Join(dir, "ca.crt"),
		}
	}

	serverCreds, err := tlsconfig.ServerCredentials(files("server"))
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(serverCreds))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	check := func(creds credentials.TransportCredentials) error {
		conn, err := grpc.NewClient("passthrough:///"+lis.Addr().String(), grpc.WithTransportCredentials(creds))
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = healthpb.NewHealthClient(conn).Check(t.Context(), &healthpb.HealthCheckRequest{})
		return err
	}

	clientCreds, err := tlsconfig.ClientCredentials(files("client"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if err := check(clientCreds); err != nil {
		t.Errorf("mTLS call failed: %v", err)
	}

	// Server-auth-only TLS (no client cert) must be rejected by the server.
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	noClientCert := credentials.NewClientTLSFromCert(pool, "localhost")
	if err := check(noClientCert); err == nil {
		t.Error("call without client certificate succeeded, want rejection")
	}
}

// newCert writes name.crt and name.key to dir. A nil parent makes a
// self-signed CA. Leaf certs are valid for localhost and 127.0.0.1.
func newCert(t *testing.T, dir, name string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	if isCA {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
		tmpl.KeyUsage |= x509.KeyUsageCertSign
		tmpl.ExtKeyUsage, tmpl.DNSNames, tmpl.IPAddresses = nil, nil, nil
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, filepath.Join(dir, name+".crt"), "CERTIFICATE", der)
	writePEM(t, filepath.Join(dir, name+".key"), "EC PRIVATE KEY", keyDER)

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
