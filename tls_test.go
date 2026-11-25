package kingbase

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// testCA is a generated certificate authority for offline TLS tests.
type testCA struct {
	cert *x509.Certificate
	key  ed25519.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "go-kingbase test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return &testCA{
		cert: certificate,
		key:  privateKey,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// issue returns a server certificate and key signed by the CA.
func (ca *testCA) issue(t *testing.T, dnsNames []string, ips []net.IP) (certPEM, keyPEM []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, publicKey, ca.key)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func writeTempFile(t *testing.T, name string, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// serveTLSHandshake answers an SSLRequest, completes the TLS handshake and
// serves the startup exchange inside TLS. A handshake failure is returned to
// the caller, since negative tests make the client reject the certificate.
func serveTLSHandshake(peer *testpeer.Conn, serverConfig *tls.Config) {
	code, _, err := peer.ReadStartupPacket()
	if err != nil {
		// A client that rejects configured material fails before contacting the
		// server, which is an expected outcome in these tests.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return
		}
		peer.Errorf("ReadStartupPacket: %v", err)
	}
	if code != wire.SSLRequestCode {
		peer.Errorf("startup code = %d, want SSLRequest", code)
	}
	if err := peer.WriteRaw([]byte{'S'}); err != nil {
		peer.Errorf("write TLS acceptance: %v", err)
	}
	tlsConn := tls.Server(peer.NetConn(), serverConfig)
	if err := tlsConn.Handshake(); err != nil {
		// The client rejected the certificate; that is expected in negative
		// tests and the test asserts the client-side error.
		return
	}
	peer.UseConn(tlsConn)
	params, err := peer.ReadStartup()
	if err != nil {
		peer.Errorf("ReadStartup: %v", err)
	}
	if user := startupParam(params, "user"); user != testUser {
		peer.Errorf("startup user = %q, want %q", user, testUser)
	}
	if err := peer.StartupOK(nil); err != nil {
		peer.Errorf("StartupOK: %v", err)
	}
	waitTerminate(peer)

}

// tlsConfigFor builds a server TLS configuration from PEM material.
func tlsConfigFor(t *testing.T, certPEM, keyPEM []byte) *tls.Config {
	t.Helper()
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load key pair: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
}

// dsnValue quotes a value for the libpq form, escaping backslashes so Windows
// paths survive parsing.
func dsnValue(value string) string {
	return "'" + strings.ReplaceAll(value, `\`, `\\`) + "'"
}

// dsnForPeer builds a client DSN for a TLS test.
func dsnForPeer(t *testing.T, srv *testpeer.Server, extra string) string {
	t.Helper()
	host, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split peer address: %v", err)
	}
	return "host=" + host + " port=" + port + " user=" + testUser + " dbname=" + testDatabase + " " + extra
}

func TestTLSModes(t *testing.T) {
	ca := newTestCA(t)
	otherCA := newTestCA(t)
	certPEM, keyPEM := ca.issue(t, []string{testHostName}, []net.IP{net.ParseIP("127.0.0.1")})
	serverConfig := tlsConfigFor(t, certPEM, keyPEM)
	rootPath := writeTempFile(t, "ca.pem", ca.pem, 0o644)
	otherRootPath := writeTempFile(t, "other-ca.pem", otherCA.pem, 0o644)

	cases := []struct {
		name    string
		extra   string
		wantErr string
	}{
		{name: "require without root", extra: "sslmode=require"},
		{name: "require with root", extra: "sslmode=require sslrootcert=" + dsnValue(rootPath)},
		{name: "require wrong root", extra: "sslmode=require sslrootcert=" + dsnValue(otherRootPath), wantErr: "verify server certificate"},
		{name: "verify-ca with root", extra: "sslmode=verify-ca sslrootcert=" + dsnValue(rootPath)},
		{name: "verify-ca wrong root", extra: "sslmode=verify-ca sslrootcert=" + dsnValue(otherRootPath), wantErr: "verify server certificate"},
		{name: "verify-full with root", extra: "sslmode=verify-full sslrootcert=" + dsnValue(rootPath)},
		{name: "verify-full untrusted", extra: "sslmode=verify-full", wantErr: "certificate"},
		{name: "verify-full missing root", extra: "sslmode=verify-full sslrootcert=/missing/ca.pem", wantErr: "sslrootcert"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				serveTLSHandshake(peer, serverConfig)
			})
			dsn := dsnForPeer(t, srv, tc.extra)
			cfg, err := ParseConfig(dsn)
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			connector := &Connector{cfg: cfg}
			physical, err := connector.Connect(context.Background())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Connect: %v", err)
				}
				_ = physical.Close()
				return
			}
			if err == nil {
				_ = physical.Close()
				t.Fatalf("Connect succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestTLSVerifyFullRejectsWrongHostname(t *testing.T) {
	ca := newTestCA(t)
	certPEM, keyPEM := ca.issue(t, []string{"other.example"}, nil)
	serverConfig := tlsConfigFor(t, certPEM, keyPEM)
	rootPath := writeTempFile(t, "ca.pem", ca.pem, 0o644)
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		serveTLSHandshake(peer, serverConfig)
	})
	cfg, err := ParseConfig(dsnForPeer(t, srv, "sslmode=verify-full sslrootcert="+dsnValue(rootPath)))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	connector := &Connector{cfg: cfg}
	if _, err := connector.Connect(context.Background()); err == nil {
		t.Fatal("Connect succeeded, want host-name verification error")
	} else if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "host") {
		t.Fatalf("error = %v, want certificate/host-name failure", err)
	}
}

func TestTLSClientCertificatePairing(t *testing.T) {
	ca := newTestCA(t)
	certPEM, keyPEM := ca.issue(t, []string{testHostName}, nil)
	certPath := writeTempFile(t, "client.crt", certPEM, 0o644)
	keyPath := writeTempFile(t, "client.key", keyPEM, 0o600)

	if _, err := tlsConfig(&Config{SSLMode: SSLRequire, SSLCert: certPath}); err == nil ||
		!strings.Contains(err.Error(), "sslkey") {
		t.Errorf("cert without key error = %v, want sslkey pairing error", err)
	}
	if _, err := tlsConfig(&Config{SSLMode: SSLRequire, SSLKey: keyPath}); err == nil ||
		!strings.Contains(err.Error(), "sslcert") {
		t.Errorf("key without cert error = %v, want sslcert pairing error", err)
	}
	if _, err := tlsConfig(&Config{SSLMode: SSLRequire, SSLCert: certPath, SSLKey: keyPath}); err != nil {
		t.Errorf("readable public cert plus 0600 key rejected: %v", err)
	}
}

func TestPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == testWindows {
		t.Skip("POSIX file modes are not enforced on windows")
	}
	ca := newTestCA(t)
	certPEM, keyPEM := ca.issue(t, []string{testHostName}, nil)
	certPath := writeTempFile(t, "client.crt", certPEM, 0o644)
	unsafeKey := writeTempFile(t, "unsafe.key", keyPEM, 0o644)
	safeKey := writeTempFile(t, "safe.key", keyPEM, 0o600)

	if _, err := tlsConfig(&Config{SSLMode: SSLRequire, SSLCert: certPath, SSLKey: unsafeKey}); err == nil ||
		!strings.Contains(err.Error(), "permissions") {
		t.Errorf("group-readable key error = %v, want permissions error", err)
	}
	if _, err := tlsConfig(&Config{SSLMode: SSLRequire, SSLCert: certPath, SSLKey: safeKey}); err != nil {
		t.Errorf("0600 key rejected: %v", err)
	}
}

func TestTLSMaterialRequiresTLS(t *testing.T) {
	if _, err := ParseConfig("sslmode=disable sslrootcert=/tmp/ca.pem"); err == nil ||
		!strings.Contains(err.Error(), "TLS material") {
		t.Errorf("error = %v, want TLS material rejection", err)
	}
}
