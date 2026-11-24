// Package testpeer implements a scripted PostgreSQL-wire peer for offline
// driver tests. It is not a database: each test decides which messages the
// peer sends, which lets tests replay fragmentation, truncation, asynchronous
// delivery and failure cases without a live server.
package testpeer

import (
	"crypto/md5" //nolint:gosec // MD5 is the documented PostgreSQL auth method.
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// MaxStartupPacketSize bounds the startup packet the peer will accept.
const MaxStartupPacketSize = 64 << 10

// Server is a loopback PG-wire endpoint serving each accepted connection with
// the supplied handler.
type Server struct {
	ln      net.Listener
	handler func(*Conn)
	wg      sync.WaitGroup
	once    sync.Once
	mu      sync.Mutex
	conns   map[*Conn]struct{}
	closed  bool
}

// Start listens on a loopback ephemeral TCP port and serves connections with
// handler. The listener is closed when the test finishes.
func Start(t testing.TB, handler func(*Conn)) *Server {
	return StartAt(t, "tcp", "127.0.0.1:0", handler)
}

// StartAt listens on the supplied network and address.
func StartAt(t testing.TB, network, address string, handler func(*Conn)) *Server {
	t.Helper()
	ln, err := net.Listen(network, address)
	if err != nil {
		t.Fatalf("testpeer: listen: %v", err)
	}
	s := &Server{ln: ln, handler: handler, conns: make(map[*Conn]struct{})}
	s.wg.Add(1)
	go s.serve(t)
	t.Cleanup(s.Close)
	return s
}

func (s *Server) serve(t testing.TB) {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		peer := newConn(t, conn)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[peer] = struct{}{}
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, peer)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("testpeer: handler panic: %v", r)
				}
			}()
			s.handler(peer)
		}()
	}
}

// Addr returns the host:port to dial.
func (s *Server) Addr() string {
	return s.ln.Addr().String()
}

// Close stops the listener, closes accepted connections and waits for active
// handlers, so a handler blocked on a read cannot hang test cleanup.
func (s *Server) Close() {
	s.once.Do(func() {
		_ = s.ln.Close()
	})
	s.mu.Lock()
	s.closed = true
	for peer := range s.conns {
		_ = peer.conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Conn is one peer connection.
type Conn struct {
	t      testing.TB
	conn   net.Conn
	reader *wire.Reader
	writer *wire.Writer
	chunk  int
}

func newConn(t testing.TB, conn net.Conn) *Conn {
	c := &Conn{t: t, conn: conn}
	c.reader = wire.NewReader(conn, 0)
	c.writer = wire.NewWriter(connWriter{c: c})
	return c
}

type connWriter struct {
	c *Conn
}

func (w connWriter) Write(p []byte) (int, error) {
	return w.c.writeAll(p)
}

func (c *Conn) writeAll(data []byte) (int, error) {
	if c.chunk <= 0 {
		return c.conn.Write(data)
	}
	written := 0
	for written < len(data) {
		n := c.chunk
		if n > len(data)-written {
			n = len(data) - written
		}
		m, err := c.conn.Write(data[written : written+n])
		written += m
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// SetWriteChunk makes every write emit at most n bytes, simulating TCP
// fragmentation. Call it before the handler starts writing.
func (c *Conn) SetWriteChunk(n int) {
	c.chunk = n
}

// SetReadLimit bounds a single frontend message payload. Call it before the
// handler starts reading messages.
func (c *Conn) SetReadLimit(maxMessageSize int32) {
	c.reader = wire.NewReader(c.conn, maxMessageSize)
}

// NetConn returns the raw connection, for example to upgrade it to TLS.
func (c *Conn) NetConn() net.Conn {
	return c.conn
}

// UseConn replaces the underlying connection and rebuilds the message
// reader/writer. It is used after a TLS handshake.
func (c *Conn) UseConn(conn net.Conn) {
	c.conn = conn
	c.reader = wire.NewReader(conn, 0)
	c.writer = wire.NewWriter(connWriter{c: c})
}

// Close closes the peer side of the connection immediately.
func (c *Conn) Close() error {
	return c.conn.Close()
}

// Errorf reports a handler-side assertion failure in the owning test.
func (c *Conn) Errorf(format string, args ...any) {
	c.t.Helper()
	c.t.Errorf(format, args...)
}

// ReadStartupPacket reads an untyped startup packet and returns its request
// code and remaining payload. It reads the raw socket, so call it before any
// ReadMessage on the same connection.
func (c *Conn) ReadStartupPacket() (int32, []byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(c.conn, header[:]); err != nil {
		return 0, nil, err
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length < 8 || length > MaxStartupPacketSize {
		return 0, nil, fmt.Errorf("testpeer: invalid startup packet length %d", length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return 0, nil, err
	}
	code := int32(binary.BigEndian.Uint32(payload[:4]))
	return code, payload[4:], nil
}

// ReadStartup reads a startup packet and decodes its parameters.
func (c *Conn) ReadStartup() ([][2]string, error) {
	code, payload, err := c.ReadStartupPacket()
	if err != nil {
		return nil, err
	}
	params, err := wire.DecodeStartupParams(append(wire.AppendInt32(nil, code), payload...))
	if err != nil {
		return nil, err
	}
	return params, nil
}

// ReadMessage reads one frontend message.
func (c *Conn) ReadMessage() (wire.MsgType, []byte, error) {
	return c.reader.ReadMessage()
}

// WriteMessage writes one backend message.
func (c *Conn) WriteMessage(typ wire.MsgType, payload []byte) error {
	return c.writer.WriteMessage(typ, payload)
}

// WriteRaw writes arbitrary bytes, for truncated-frame injection.
func (c *Conn) WriteRaw(data []byte) error {
	return c.writer.WriteRaw(data)
}

// AuthenticateCleartext requests a cleartext password and verifies it.
func (c *Conn) AuthenticateCleartext(password string) error {
	if err := c.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationCleartext()); err != nil {
		return err
	}
	got, err := c.readPassword()
	if err != nil {
		return err
	}
	if got != password {
		return fmt.Errorf("testpeer: cleartext password mismatch")
	}
	return nil
}

// AuthenticateMD5 requests MD5 authentication for user and verifies it.
func (c *Conn) AuthenticateMD5(user, password string) error {
	var salt [4]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	if err := c.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationMD5(salt)); err != nil {
		return err
	}
	got, err := c.readPassword()
	if err != nil {
		return err
	}
	want := md5Password(user, password, salt)
	if got != want {
		return fmt.Errorf("testpeer: md5 password mismatch")
	}
	return nil
}

func md5Password(user, password string, salt [4]byte) string {
	inner := md5.Sum([]byte(password + user)) //nolint:gosec // Documented PG MD5 auth.
	innerHex := hex.EncodeToString(inner[:])
	outer := md5.Sum(append([]byte(innerHex), salt[:]...)) //nolint:gosec // Documented PG MD5 auth.
	return "md5" + hex.EncodeToString(outer[:])
}

func (c *Conn) readPassword() (string, error) {
	typ, payload, err := c.ReadMessage()
	if err != nil {
		return "", err
	}
	if typ != wire.MsgPassword {
		return "", fmt.Errorf("testpeer: expected password message, got %q", typ)
	}
	return wire.DecodePasswordMessage(payload)
}

// StartupOK sends AuthenticationOk, the supplied parameter statuses, backend
// key data and ReadyForQuery idle.
func (c *Conn) StartupOK(params [][2]string) error {
	if err := c.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationOk()); err != nil {
		return err
	}
	for _, param := range params {
		if err := c.WriteMessage(wire.MsgParameterStatus, wire.EncodeParameterStatus(param[0], param[1])); err != nil {
			return err
		}
	}
	if err := c.WriteMessage(wire.MsgBackendKeyData, wire.EncodeBackendKeyData(4242, 9876)); err != nil {
		return err
	}
	return c.ReadyForQuery(wire.StatusIdle)
}

// ReadyForQuery sends ReadyForQuery with the transaction status.
func (c *Conn) ReadyForQuery(status byte) error {
	return c.WriteMessage(wire.MsgReadyForQuery, wire.EncodeReadyForQuery(status))
}

// CommandComplete sends a CommandComplete tag.
func (c *Conn) CommandComplete(tag string) error {
	return c.WriteMessage(wire.MsgCommandComplete, wire.EncodeCommandComplete(tag))
}

// RowDescription sends a RowDescription.
func (c *Conn) RowDescription(columns []wire.ColumnDescription) error {
	return c.WriteMessage(wire.MsgRowDescription, wire.EncodeRowDescription(columns))
}

// DataRow sends a DataRow. A nil column encodes SQL NULL.
func (c *Conn) DataRow(columns [][]byte) error {
	return c.WriteMessage(wire.MsgDataRow, wire.EncodeDataRow(columns))
}

// ErrorResponse sends an ErrorResponse built from the given fields.
func (c *Conn) ErrorResponse(fields ...wire.ErrorField) error {
	return c.WriteMessage(wire.MsgErrorResponse, wire.EncodeErrorResponse(fields...))
}

// NoticeResponse sends a NoticeResponse built from the given fields.
func (c *Conn) NoticeResponse(fields ...wire.ErrorField) error {
	return c.WriteMessage(wire.MsgNoticeResponse, wire.EncodeNoticeResponse(fields...))
}

// NotificationResponse sends an asynchronous notification.
func (c *Conn) NotificationResponse(pid uint32, channel, payload string) error {
	return c.WriteMessage(wire.MsgNotificationResponse, wire.EncodeNotificationResponse(pid, channel, payload))
}

// SendResult sends RowDescription (when columns are supplied), the rows,
// CommandComplete and ReadyForQuery.
func (c *Conn) SendResult(columns []wire.ColumnDescription, rows [][][]byte, tag string, status byte) error {
	if len(columns) > 0 {
		if err := c.RowDescription(columns); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := c.DataRow(row); err != nil {
			return err
		}
	}
	if err := c.CommandComplete(tag); err != nil {
		return err
	}
	return c.ReadyForQuery(status)
}
