package gokb

import (
	"crypto/md5" //nolint:gosec // MD5 is the documented PostgreSQL MD5 auth method.
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cocosip/go-kingbase/internal/auth"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// conn is one physical connection. Its configuration, mode and catalog are
// immutable after Connect; concurrent use is serialized by database/sql.
type conn struct {
	cfg     *Config
	netConn net.Conn
	reader  *wire.Reader
	writer  *wire.Writer

	params        map[string]string
	backendPID    uint32
	backendSecret uint32
	txStatus      byte
	catalog       *typeCatalog
	nextStmtID    int
	// commandSent records that a request was written, so a later failure is
	// execution-uncertain rather than safely retryable.
	commandSent bool
	// stopWatch cancels the per-command context watcher that interrupts a
	// stalled socket after cancellation.
	stopWatch func() bool
}

var _ driver.Conn = (*conn)(nil)

// handshake performs the PG startup exchange through ReadyForQuery. It is
// bounded by the connection deadline installed by Connect.
func (c *conn) handshake() error {
	if err := c.writer.WriteStartup(wire.EncodeStartupPayload(startupParameters(c.cfg))); err != nil {
		return fmt.Errorf("kingbase: send startup packet: %w", err)
	}
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			return fmt.Errorf("kingbase: startup: %w", err)
		}
		switch typ {
		case wire.MsgAuthentication:
			if err := c.handleAuthentication(payload); err != nil {
				return err
			}
		case wire.MsgParameterStatus:
			name, value, err := wire.DecodeParameterStatus(payload)
			if err != nil {
				return fmt.Errorf("kingbase: malformed ParameterStatus: %w", err)
			}
			c.params[name] = value
		case wire.MsgBackendKeyData:
			pid, secret, err := wire.DecodeBackendKeyData(payload)
			if err != nil {
				return fmt.Errorf("kingbase: malformed BackendKeyData: %w", err)
			}
			c.backendPID, c.backendSecret = pid, secret
		case wire.MsgNoticeResponse:
			// Notices are discarded by default; callbacks are F-704 work.
		case wire.MsgErrorResponse:
			return serverError(payload)
		case wire.MsgReadyForQuery:
			if len(payload) != 1 {
				return fmt.Errorf("kingbase: malformed ReadyForQuery")
			}
			c.txStatus = payload[0]
			return c.validateStartup()
		case wire.MsgNegotiateProtocolLevel:
			return fmt.Errorf("kingbase: server does not support protocol 3.0")
		default:
			return fmt.Errorf("kingbase: unexpected message %q during startup", typ)
		}
	}
}

func (c *conn) handleAuthentication(payload []byte) error {
	request, err := wire.DecodeAuthentication(payload)
	if err != nil {
		return fmt.Errorf("kingbase: malformed authentication request: %w", err)
	}
	switch request.Code {
	case wire.AuthOK:
		return nil
	case wire.AuthCleartext:
		if c.cfg.Password == "" {
			return fmt.Errorf("kingbase: server requires a password but none is configured")
		}
		return c.writePassword(c.cfg.Password)
	case wire.AuthMD5:
		if c.cfg.Password == "" {
			return fmt.Errorf("kingbase: server requires a password but none is configured")
		}
		return c.writePassword(md5Response(c.cfg.User, c.cfg.Password, request.Salt))
	case wire.AuthSASL:
		if c.cfg.Password == "" {
			return fmt.Errorf("kingbase: server requires a password but none is configured")
		}
		return c.authenticateSCRAM(request.Mechanisms)
	case wire.AuthSM3:
		if c.cfg.Password == "" {
			return fmt.Errorf("kingbase: server requires a password but none is configured")
		}
		if len(request.Salt) != 4 {
			return fmt.Errorf("kingbase: malformed SM3 challenge")
		}
		return c.writePassword(auth.SM3PasswordResponse(c.cfg.User, c.cfg.Password, request.Salt))
	case wire.AuthSASLContinue, wire.AuthSASLFinal:
		return fmt.Errorf("kingbase: unexpected authentication request %d", request.Code)
	default:
		return fmt.Errorf("kingbase: unsupported authentication request %d", request.Code)
	}
}

// authenticateSCRAM runs the SCRAM-SHA-256 exchange. Channel binding is not
// offered by KingbaseES profiles that support this mechanism, so only the plain
// SCRAM-SHA-256 mechanism is accepted.
func (c *conn) authenticateSCRAM(mechanisms []string) error {
	if !slices.Contains(mechanisms, auth.SCRAMMechanism) {
		return fmt.Errorf("kingbase: server offered SASL mechanisms %v, want %s", mechanisms, auth.SCRAMMechanism)
	}
	client, err := auth.NewSCRAMClient(c.cfg.User, c.cfg.Password)
	if err != nil {
		return err
	}
	initial := client.ClientFirst()
	message := wire.EncodeSASLInitialResponse(auth.SCRAMMechanism, initial)
	if err := c.writer.WriteMessage(wire.MsgPassword, message); err != nil {
		return fmt.Errorf("kingbase: send SASL initial response: %w", err)
	}
	serverFirst, err := c.readSASLData(wire.AuthSASLContinue)
	if err != nil {
		return err
	}
	final, err := client.HandleServerFirst(serverFirst)
	if err != nil {
		return err
	}
	if err := c.writer.WriteMessage(wire.MsgPassword, wire.EncodeSASLResponse(final)); err != nil {
		return fmt.Errorf("kingbase: send SASL response: %w", err)
	}
	serverFinal, err := c.readSASLData(wire.AuthSASLFinal)
	if err != nil {
		return err
	}
	return client.VerifyServerFinal(serverFinal)
}

// readSASLData reads the next SASL authentication message and requires the
// expected authentication code. A server error is preserved verbatim.
func (c *conn) readSASLData(want int32) ([]byte, error) {
	typ, payload, err := c.reader.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("kingbase: read SASL response: %w", err)
	}
	switch typ {
	case wire.MsgAuthentication:
	case wire.MsgErrorResponse:
		return nil, serverError(payload)
	default:
		return nil, fmt.Errorf("kingbase: unexpected message %q during SASL authentication", typ)
	}
	request, err := wire.DecodeAuthentication(payload)
	if err != nil {
		return nil, fmt.Errorf("kingbase: malformed authentication request: %w", err)
	}
	if request.Code != want {
		return nil, fmt.Errorf("kingbase: unexpected authentication request %d, want %d", request.Code, want)
	}
	return request.Data, nil
}

func (c *conn) writePassword(password string) error {
	if err := c.writer.WriteMessage(wire.MsgPassword, wire.EncodePasswordMessage(password)); err != nil {
		return fmt.Errorf("kingbase: send password: %w", err)
	}
	return nil
}

// md5Response computes the documented PostgreSQL MD5 password response.
func md5Response(user, password string, salt []byte) string {
	inner := md5.Sum([]byte(password + user))                               //nolint:gosec // Documented PG MD5 auth.
	outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), salt...)) //nolint:gosec // Documented PG MD5 auth.
	return "md5" + hex.EncodeToString(outer[:])
}

// startupParameters builds the startup packet parameters. Only implemented
// runtime settings are forwarded; arbitrary keys are never passed through.
func startupParameters(cfg *Config) [][2]string {
	params := make([][2]string, 0, 6)
	params = append(params,
		[2]string{startupUser, cfg.User},
		[2]string{startupDatabase, cfg.Database},
		[2]string{startupEncoding, encodingUTF8},
	)
	applicationName := cfg.ApplicationName
	if applicationName == "" {
		applicationName = cfg.FallbackApplicationName
	}
	if applicationName != "" {
		params = append(params, [2]string{"application_name", applicationName})
	}
	if cfg.Options != "" {
		params = append(params, [2]string{"options", cfg.Options})
	}
	if cfg.SearchPath != "" {
		params = append(params, [2]string{"search_path", cfg.SearchPath})
	}
	return params
}

// maxDiagnosticValue bounds how much untrusted server text a diagnostic may
// echo back.
const maxDiagnosticValue = 64

// boundedValue renders untrusted server text for a diagnostic: control
// characters are replaced, the value is capped, and the result is quoted.
func boundedValue(value string) string {
	clean := make([]rune, 0, len(value))
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			r = '?'
		}
		clean = append(clean, r)
		if len(clean) == maxDiagnosticValue {
			return strconv.Quote(string(clean)) + "..."
		}
	}
	return strconv.Quote(string(clean))
}

// validateStartup rejects startup states the driver cannot honour instead of
// silently corrupting data.
func (c *conn) validateStartup() error {
	if encoding, ok := c.params["client_encoding"]; ok && !isUTF8(encoding) {
		return fmt.Errorf("kingbase: server client_encoding %s is not supported (only UTF8)", boundedValue(encoding))
	}
	if c.txStatus != wire.StatusIdle {
		return fmt.Errorf("kingbase: connection started in transaction state %q", c.txStatus)
	}
	return nil
}

func isUTF8(encoding string) bool {
	switch strings.ToLower(encoding) {
	case "utf8", "utf-8":
		return true
	default:
		return false
	}
}

// Close terminates the physical connection. It never retries commands.
func (c *conn) Close() error {
	if c.netConn == nil {
		return nil
	}
	// A command abandoned without endCommand may still hold a watcher; stop it
	// so a later context cancellation cannot dial a cancel request for a dead
	// backend.
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
	}
	// Terminate is best effort and bounded, so Close cannot block on a wedged
	// peer; the socket is closed regardless.
	_ = c.netConn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = c.writer.WriteMessage(wire.MsgTerminate, nil)
	err := c.netConn.Close()
	c.netConn = nil
	if err != nil {
		return fmt.Errorf("kingbase: close: %w", err)
	}
	return nil
}

// startupParameters builds the startup packet parameters. Only implemented
