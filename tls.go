package kingbase

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// negotiateTLS performs the SSLRequest exchange and TLS handshake according to
// the configured trust policy. A requested TLS policy never falls back to
// plaintext.
func (c *conn) negotiateTLS(ctx context.Context, host string) error {
	if c.cfg.SSLMode == SSLDisable {
		return nil
	}
	// Validate configured material before contacting the server, so missing or
	// unreadable files fail the same way regardless of the server reply.
	config, err := tlsConfigForHost(c.cfg, host)
	if err != nil {
		return err
	}
	if _, err := c.netConn.Write(wire.EncodeSSLRequest()); err != nil {
		return fmt.Errorf("kingbase: send TLS request: %w", err)
	}
	var reply [1]byte
	if _, err := io.ReadFull(c.netConn, reply[:]); err != nil {
		return fmt.Errorf("kingbase: TLS negotiation: %w", err)
	}
	switch reply[0] {
	case 'S':
	case 'N':
		return fmt.Errorf("kingbase: server does not support TLS (sslmode=%s)", c.cfg.SSLMode)
	case 'E':
		return fmt.Errorf("kingbase: server rejected the TLS request")
	default:
		return fmt.Errorf("kingbase: unexpected TLS negotiation reply 0x%02x", reply[0])
	}

	tlsConn := tls.Client(c.netConn, config)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("kingbase: TLS handshake: %w", err)
	}
	c.netConn = tlsConn
	return nil
}

// tlsConfig builds the TLS client configuration for the configured sslmode
// against the first configured host.
func tlsConfig(cfg *Config) (*tls.Config, error) {
	return tlsConfigForHost(cfg, cfg.Host)
}

// tlsConfigForHost builds the TLS client configuration for one target host.
// require verifies the chain only when an explicit sslrootcert is configured;
// verify-ca verifies the chain; verify-full also verifies the host name.
func tlsConfigForHost(cfg *Config, host string) (*tls.Config, error) {
	// ServerName supplies SNI and, for verify-full, the host-name check.
	tlsConfig := &tls.Config{ServerName: host}
	if cfg.SSLCert != "" || cfg.SSLKey != "" {
		if cfg.SSLCert == "" || cfg.SSLKey == "" {
			return nil, fmt.Errorf("kingbase: sslcert and sslkey must be configured together")
		}
		if err := checkPrivateKeyPermissions(cfg.SSLKey); err != nil {
			return nil, err
		}
		certificate, err := tls.LoadX509KeyPair(cfg.SSLCert, cfg.SSLKey)
		if err != nil {
			return nil, fmt.Errorf("kingbase: load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	roots, err := loadRootCAs(cfg.SSLRootCert)
	if err != nil {
		return nil, err
	}
	switch cfg.SSLMode {
	case SSLRequire:
		tlsConfig.InsecureSkipVerify = true
		if roots != nil {
			tlsConfig.VerifyConnection = verifyChainOnly(roots)
		}
	case SSLVerifyCA:
		tlsConfig.InsecureSkipVerify = true
		tlsConfig.VerifyConnection = verifyChainOnly(roots)
	case SSLVerifyFull:
		tlsConfig.RootCAs = roots
	}
	return tlsConfig, nil
}

func loadRootCAs(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("kingbase: read sslrootcert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("kingbase: sslrootcert %s contains no certificates", path)
	}
	return pool, nil
}

// verifyChainOnly validates the server certificate chain without host-name
// verification, which is what require-with-root and verify-ca mean.
func verifyChainOnly(roots *x509.CertPool) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return fmt.Errorf("kingbase: server presented no certificate")
		}
		options := x509.VerifyOptions{
			Roots:         roots,
			Intermediates: x509.NewCertPool(),
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		for _, intermediate := range state.PeerCertificates[1:] {
			options.Intermediates.AddCert(intermediate)
		}
		if _, err := state.PeerCertificates[0].Verify(options); err != nil {
			return fmt.Errorf("kingbase: verify server certificate: %w", err)
		}
		return nil
	}
}

// checkPrivateKeyPermissions rejects group- or other-accessible private keys
// on POSIX platforms, where the file mode is meaningful. Windows ACLs are not
// inferred from the mode and are handled separately.
func checkPrivateKeyPermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("kingbase: read sslkey: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("kingbase: sslkey %s has permissions %04o; group/other access is not allowed", path, perm)
	}
	return nil
}
