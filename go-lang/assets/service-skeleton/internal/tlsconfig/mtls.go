// Package tlsconfig builds mutual-TLS credentials for service-to-service gRPC.
package tlsconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
)

// Files points at PEM files, typically mounted from a Kubernetes Secret or
// written by cert-manager / SPIFFE.
type Files struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

// ServerCredentials requires every client to present a certificate signed by
// the CA. The key pair is re-read when its files change, so short-lived leaf
// certs rotate without a restart.
func ServerCredentials(f Files) (credentials.TransportCredentials, error) {
	pool, err := loadCAPool(f.CAFile)
	if err != nil {
		return nil, err
	}
	kp, err := newKeyPairReloader(f.CertFile, f.KeyFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return kp.get(hello.Context())
		},
	}), nil
}

// ClientCredentials verifies the server against the CA and presents the
// client certificate. serverName overrides the name checked against the
// server certificate; leave it empty to use the dial target's host.
func ClientCredentials(f Files, serverName string) (credentials.TransportCredentials, error) {
	pool, err := loadCAPool(f.CAFile)
	if err != nil {
		return nil, err
	}
	kp, err := newKeyPairReloader(f.CertFile, f.KeyFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
		GetClientCertificate: func(req *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return kp.get(req.Context())
		},
	}), nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("no valid certificates in CA file " + caFile)
	}
	return pool, nil
}

// keyPairReloader caches a key pair and reloads it when either file's
// modification time changes (in any direction, so `cp -p` and restored
// backups count). It runs once per TLS handshake, which is rare with
// long-lived gRPC connections, so two stat calls are cheap enough.
// Only the leaf key pair rotates; a changed CA bundle needs a restart.
type keyPairReloader struct {
	certFile, keyFile string

	mu              sync.Mutex
	cert            *tls.Certificate
	certMod, keyMod time.Time
}

func newKeyPairReloader(certFile, keyFile string) (*keyPairReloader, error) {
	r := &keyPairReloader{certFile: certFile, keyFile: keyFile}
	if _, err := r.get(context.Background()); err != nil {
		return nil, err // fail fast at startup instead of at first handshake
	}
	return r, nil
}

func (r *keyPairReloader) get(ctx context.Context) (*tls.Certificate, error) {
	certInfo, certErr := os.Stat(r.certFile)
	keyInfo, keyErr := os.Stat(r.keyFile)

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := errors.Join(certErr, keyErr); err != nil {
		return r.keepOrFail(ctx, fmt.Errorf("stat key pair: %w", err))
	}
	if r.cert != nil && certInfo.ModTime().Equal(r.certMod) && keyInfo.ModTime().Equal(r.keyMod) {
		return r.cert, nil
	}
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return r.keepOrFail(ctx, fmt.Errorf("load key pair: %w", err))
	}
	r.cert, r.certMod, r.keyMod = &c, certInfo.ModTime(), keyInfo.ModTime()
	return r.cert, nil
}

// keepOrFail serves the previous pair through a failed rotation (files
// briefly missing during a symlink swap, or half-written), but logs it:
// a rotation that keeps failing is invisible until the old cert expires.
func (r *keyPairReloader) keepOrFail(ctx context.Context, err error) (*tls.Certificate, error) {
	if r.cert == nil {
		return nil, err
	}
	slog.WarnContext(ctx, "tls key pair reload failed; serving previous certificate", "err", err)
	return r.cert, nil
}
