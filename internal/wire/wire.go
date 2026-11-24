// Package wire implements the bounded PostgreSQL v3 protocol framing used by
// the KingbaseES PG-wire profiles. It contains no server-specific policy: it
// reads and writes frames, and callers enforce authentication, mode and
// catalog rules.
package wire

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// ProtocolVersion is PostgreSQL protocol 3.0.
const ProtocolVersion = 3 << 16

// Special startup request codes.
const (
	SSLRequestCode    = 80877103
	CancelRequestCode = 80877102
)

// MaxMessageSize bounds a single protocol message payload. A peer that
// announces a larger message is a protocol error, never an allocation
// request. PostgreSQL permits up to 1 GiB, so this is deliberately stricter.
const MaxMessageSize = 64 << 20

// MsgType is a backend message type byte.
type MsgType byte

// Backend message types used by this driver.
const (
	MsgAuthentication         MsgType = 'R'
	MsgBackendKeyData         MsgType = 'K'
	MsgBindComplete           MsgType = '2'
	MsgCloseComplete          MsgType = '3'
	MsgCommandComplete        MsgType = 'C'
	MsgCopyData               MsgType = 'd'
	MsgCopyDone               MsgType = 'c'
	MsgCopyInResponse         MsgType = 'G'
	MsgDataRow                MsgType = 'D'
	MsgEmptyQueryResponse     MsgType = 'I'
	MsgErrorResponse          MsgType = 'E'
	MsgNoData                 MsgType = 'n'
	MsgNoticeResponse         MsgType = 'N'
	MsgNotificationResponse   MsgType = 'A'
	MsgParameterDescription   MsgType = 't'
	MsgParameterStatus        MsgType = 'S'
	MsgParseComplete          MsgType = '1'
	MsgPortalSuspended        MsgType = 's'
	MsgReadyForQuery          MsgType = 'Z'
	MsgRowDescription         MsgType = 'T'
	MsgNegotiateProtocolLevel MsgType = 'v'
)

// Frontend message types used by this driver. CopyData and CopyDone share
// their byte values with the backend messages of the same name.
const (
	MsgBind      MsgType = 'B'
	MsgClose     MsgType = 'C'
	MsgDescribe  MsgType = 'D'
	MsgExecute   MsgType = 'E'
	MsgFlush     MsgType = 'H'
	MsgParse     MsgType = 'P'
	MsgPassword  MsgType = 'p'
	MsgQuery     MsgType = 'Q'
	MsgSync      MsgType = 'S'
	MsgTerminate MsgType = 'X'
	MsgCopyFail  MsgType = 'f'
)

// Authentication request codes sent by the server.
const (
	AuthOK           int32 = 0
	AuthCleartext    int32 = 3
	AuthMD5          int32 = 5
	AuthSASL         int32 = 10
	AuthSASLContinue int32 = 11
	AuthSASLFinal    int32 = 12
	// AuthSM3 is the KingbaseES SM3 password challenge, evidenced from the
	// vendor driver's PG-wire implementation.
	AuthSM3 int32 = 13
)

// ReadyForQuery transaction status bytes.
const (
	StatusIdle              = 'I'
	StatusInTransaction     = 'T'
	StatusFailedTransaction = 'E'
)

// Reader decodes typed backend messages from a server connection.
type Reader struct {
	r   *bufio.Reader
	max int32
}

// NewReader wraps r. A non-positive maxMessageSize selects MaxMessageSize.
func NewReader(r io.Reader, maxMessageSize int32) *Reader {
	if maxMessageSize <= 0 {
		maxMessageSize = MaxMessageSize
	}
	return &Reader{r: bufio.NewReader(r), max: maxMessageSize}
}

// ReadMessage reads one typed message and returns its type and payload. The
// payload is a fresh slice owned by the caller.
func (r *Reader) ReadMessage() (MsgType, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r.r, header[:1]); err != nil {
		return 0, nil, err
	}
	if _, err := io.ReadFull(r.r, header[1:]); err != nil {
		return 0, nil, err
	}
	rawLength := binary.BigEndian.Uint32(header[1:])
	if rawLength < 4 || rawLength > 1<<31-1 {
		return 0, nil, fmt.Errorf("wire: invalid message length %d", rawLength)
	}
	payloadLength := int32(rawLength) - 4
	if payloadLength > r.max {
		return 0, nil, fmt.Errorf("wire: message %q payload %d exceeds limit %d", MsgType(header[0]), payloadLength, r.max)
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(r.r, payload); err != nil {
		return 0, nil, err
	}
	return MsgType(header[0]), payload, nil
}

// Writer encodes typed frontend messages to a server connection.
type Writer struct {
	w *bufio.Writer
}

// NewWriter wraps w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: bufio.NewWriter(w)}
}

// WriteMessage writes a typed message and flushes it immediately.
func (w *Writer) WriteMessage(t MsgType, payload []byte) error {
	if _, err := w.w.Write(encodeMessage(t, payload)); err != nil {
		return err
	}
	return w.w.Flush()
}

// WriteBuffered writes a typed message without flushing.
func (w *Writer) WriteBuffered(t MsgType, payload []byte) error {
	_, err := w.w.Write(encodeMessage(t, payload))
	return err
}

// WriteStartup writes an untyped startup packet and flushes it.
func (w *Writer) WriteStartup(payload []byte) error {
	if _, err := w.w.Write(encodeStartup(payload)); err != nil {
		return err
	}
	return w.w.Flush()
}

// WriteRaw writes raw bytes and flushes them. It is used for the SSL request
// reply byte, which is not a framed message.
func (w *Writer) WriteRaw(data []byte) error {
	if _, err := w.w.Write(data); err != nil {
		return err
	}
	return w.w.Flush()
}

// Flush flushes buffered frames.
func (w *Writer) Flush() error {
	return w.w.Flush()
}

func encodeMessage(t MsgType, payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = byte(t)
	binary.BigEndian.PutUint32(buf[1:], uint32(len(payload)+4))
	copy(buf[5:], payload)
	return buf
}

func encodeStartup(payload []byte) []byte {
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)+4))
	copy(buf[4:], payload)
	return buf
}

// AppendInt16 appends a big-endian int16.
func AppendInt16(b []byte, v int16) []byte {
	return append(b, byte(v>>8), byte(v))
}

// AppendInt32 appends a big-endian int32.
func AppendInt32(b []byte, v int32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// AppendString appends a NUL-terminated string.
func AppendString(b []byte, s string) []byte {
	b = append(b, s...)
	return append(b, 0)
}
