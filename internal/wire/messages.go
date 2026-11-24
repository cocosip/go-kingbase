package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const errTruncated = "wire: truncated %s"

// ColumnDescription is one RowDescription field.
type ColumnDescription struct {
	Name     string
	TableOID uint32
	AttrNum  int16
	TypeOID  uint32
	TypeLen  int16
	TypeMod  int32
	Format   int16
}

// DataColumn is one DataRow column. Null distinguishes SQL NULL from an empty
// value.
type DataColumn struct {
	Null bool
	Data []byte
}

// ErrorField is one ErrorResponse/NoticeResponse field code and value.
type ErrorField struct {
	Code  byte
	Value string
}

// Error-field codes preserved by this driver.
const (
	ErrorSeverity         byte = 'S'
	ErrorSeverityNonLocal byte = 'V'
	ErrorCode             byte = 'C'
	ErrorMessage          byte = 'M'
	ErrorDetail           byte = 'D'
	ErrorHint             byte = 'H'
	ErrorPosition         byte = 'P'
	ErrorInternalPosition byte = 'p'
	ErrorInternalQuery    byte = 'q'
	ErrorWhere            byte = 'W'
	ErrorSchemaName       byte = 's'
	ErrorTableName        byte = 't'
	ErrorColumnName       byte = 'c'
	ErrorDataTypeName     byte = 'd'
	ErrorConstraintName   byte = 'n'
	ErrorFile             byte = 'F'
	ErrorLine             byte = 'L'
	ErrorRoutine          byte = 'R'
)

// Authentication is a decoded AuthenticationRequest.
type Authentication struct {
	Code       int32
	Salt       []byte
	Mechanisms []string
	Data       []byte
}

// CopyInResponse is a decoded CopyInResponse.
type CopyInResponse struct {
	Format        int8
	ColumnFormats []int16
}

// Notification is a decoded NotificationResponse.
type Notification struct {
	PID     uint32
	Channel string
	Payload string
}

// EncodeStartupPayload encodes the startup packet body: protocol version and
// parameters. Use Writer.WriteStartup to add the length prefix.
func EncodeStartupPayload(params [][2]string) []byte {
	payload := make([]byte, 0, 64)
	payload = AppendInt32(payload, ProtocolVersion)
	for _, p := range params {
		payload = AppendString(payload, p[0])
		payload = AppendString(payload, p[1])
	}
	return append(payload, 0)
}

// EncodeSSLRequest returns the complete 8-byte SSLRequest packet.
func EncodeSSLRequest() []byte {
	return encodeStartup(AppendInt32(nil, SSLRequestCode))
}

// EncodeCancelRequest returns the complete 16-byte CancelRequest packet.
func EncodeCancelRequest(pid, secret uint32) []byte {
	payload := AppendInt32(nil, CancelRequestCode)
	payload = AppendInt32(payload, int32(pid))
	payload = AppendInt32(payload, int32(secret))
	return encodeStartup(payload)
}

// EncodePasswordMessage returns a PasswordMessage payload.
func EncodePasswordMessage(password string) []byte {
	return AppendString(nil, password)
}

// EncodeSASLInitialResponse returns a SASLInitialResponse payload.
func EncodeSASLInitialResponse(mechanism string, data []byte) []byte {
	payload := AppendString(nil, mechanism)
	payload = AppendInt32(payload, int32(len(data)))
	return append(payload, data...)
}

// EncodeSASLResponse returns a SASLResponse payload.
func EncodeSASLResponse(data []byte) []byte {
	return data
}

// EncodeQuery returns a Query payload.
func EncodeQuery(sql string) []byte {
	return AppendString(nil, sql)
}

// EncodeParse returns a Parse payload.
func EncodeParse(name, sql string, paramOIDs []uint32) []byte {
	payload := AppendString(nil, name)
	payload = AppendString(payload, sql)
	payload = AppendInt16(payload, int16(len(paramOIDs)))
	for _, oid := range paramOIDs {
		payload = AppendInt32(payload, int32(oid))
	}
	return payload
}

// EncodeBind returns a Bind payload. A nil parameter encodes SQL NULL.
func EncodeBind(portal, statement string, paramFormats []int16, params [][]byte, resultFormats []int16) []byte {
	payload := AppendString(nil, portal)
	payload = AppendString(payload, statement)
	payload = AppendInt16(payload, int16(len(paramFormats)))
	for _, format := range paramFormats {
		payload = AppendInt16(payload, format)
	}
	payload = AppendInt16(payload, int16(len(params)))
	for _, param := range params {
		if param == nil {
			payload = AppendInt32(payload, -1)
			continue
		}
		payload = AppendInt32(payload, int32(len(param)))
		payload = append(payload, param...)
	}
	payload = AppendInt16(payload, int16(len(resultFormats)))
	for _, format := range resultFormats {
		payload = AppendInt16(payload, format)
	}
	return payload
}

// EncodeDescribe returns a Describe payload for a statement ('S') or portal
// ('P').
func EncodeDescribe(kind byte, name string) []byte {
	payload := []byte{kind}
	return AppendString(payload, name)
}

// EncodeExecute returns an Execute payload.
func EncodeExecute(portal string, maxRows int32) []byte {
	payload := AppendString(nil, portal)
	return AppendInt32(payload, maxRows)
}

// EncodeCopyData returns a CopyData payload.
func EncodeCopyData(data []byte) []byte {
	return data
}

// EncodeCopyFail returns a CopyFail payload.
func EncodeCopyFail(reason string) []byte {
	return AppendString(nil, reason)
}

// DecodeStartupParams decodes a startup packet body into ordered parameters.
func DecodeStartupParams(payload []byte) ([][2]string, error) {
	d := decoder{data: payload}
	version, err := d.int32()
	if err != nil {
		return nil, err
	}
	if version != ProtocolVersion {
		return nil, fmt.Errorf("wire: unsupported startup protocol version %d", version)
	}
	var params [][2]string
	for {
		rest := d.remaining()
		if len(rest) == 0 {
			return nil, fmt.Errorf("wire: startup packet missing terminator")
		}
		if rest[0] == 0 {
			d.off++
			break
		}
		key, err := d.cstring()
		if err != nil {
			return nil, err
		}
		value, err := d.cstring()
		if err != nil {
			return nil, err
		}
		params = append(params, [2]string{key, value})
	}
	return params, d.end()
}

// DecodeErrorFields decodes ErrorResponse/NoticeResponse fields.
func DecodeErrorFields(payload []byte) ([]ErrorField, error) {
	d := decoder{data: payload}
	var fields []ErrorField
	for {
		if d.off >= len(d.data) {
			return nil, fmt.Errorf("wire: response missing terminator")
		}
		code := d.data[d.off]
		d.off++
		if code == 0 {
			return fields, d.end()
		}
		value, err := d.cstring()
		if err != nil {
			return nil, err
		}
		fields = append(fields, ErrorField{Code: code, Value: value})
	}
}

// DecodeAuthentication decodes an AuthenticationRequest.
func DecodeAuthentication(payload []byte) (Authentication, error) {
	d := decoder{data: payload}
	code, err := d.int32()
	if err != nil {
		return Authentication{}, err
	}
	auth := Authentication{Code: code}
	switch code {
	case AuthMD5, AuthSM3:
		salt, err := d.bytes(4)
		if err != nil {
			return Authentication{}, err
		}
		auth.Salt = salt
	case AuthSASL:
		for {
			mechanism, err := d.cstring()
			if err != nil {
				return Authentication{}, err
			}
			if mechanism == "" {
				break
			}
			auth.Mechanisms = append(auth.Mechanisms, mechanism)
		}
	case AuthSASLContinue, AuthSASLFinal:
		auth.Data = d.remaining()
		d.off = len(d.data)
	}
	return auth, d.end()
}

// DecodeParameterStatus decodes a ParameterStatus message.
func DecodeParameterStatus(payload []byte) (name, value string, err error) {
	d := decoder{data: payload}
	if name, err = d.cstring(); err != nil {
		return "", "", err
	}
	if value, err = d.cstring(); err != nil {
		return "", "", err
	}
	return name, value, d.end()
}

// DecodeBackendKeyData decodes a BackendKeyData message.
func DecodeBackendKeyData(payload []byte) (pid, secret uint32, err error) {
	d := decoder{data: payload}
	if pid, err = d.uint32(); err != nil {
		return 0, 0, err
	}
	if secret, err = d.uint32(); err != nil {
		return 0, 0, err
	}
	return pid, secret, d.end()
}

// DecodeRowDescription decodes a RowDescription message.
func DecodeRowDescription(payload []byte) ([]ColumnDescription, error) {
	d := decoder{data: payload}
	count, err := d.int16()
	if err != nil {
		return nil, err
	}
	if count < 0 {
		return nil, fmt.Errorf("wire: negative field count %d", count)
	}
	columns := make([]ColumnDescription, 0, count)
	for i := 0; i < int(count); i++ {
		var col ColumnDescription
		if col.Name, err = d.cstring(); err != nil {
			return nil, err
		}
		if col.TableOID, err = d.uint32(); err != nil {
			return nil, err
		}
		if col.AttrNum, err = d.int16(); err != nil {
			return nil, err
		}
		if col.TypeOID, err = d.uint32(); err != nil {
			return nil, err
		}
		if col.TypeLen, err = d.int16(); err != nil {
			return nil, err
		}
		if col.TypeMod, err = d.int32(); err != nil {
			return nil, err
		}
		if col.Format, err = d.int16(); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}
	return columns, d.end()
}

// DecodeDataRow decodes a DataRow message. Column payloads alias the input.
func DecodeDataRow(payload []byte) ([]DataColumn, error) {
	d := decoder{data: payload}
	count, err := d.int16()
	if err != nil {
		return nil, err
	}
	if count < 0 {
		return nil, fmt.Errorf("wire: negative column count %d", count)
	}
	columns := make([]DataColumn, 0, count)
	for i := 0; i < int(count); i++ {
		length, err := d.int32()
		if err != nil {
			return nil, err
		}
		switch {
		case length == -1:
			columns = append(columns, DataColumn{Null: true})
		case length < -1:
			return nil, fmt.Errorf("wire: invalid column length %d", length)
		default:
			data, err := d.bytes(int(length))
			if err != nil {
				return nil, err
			}
			columns = append(columns, DataColumn{Data: data})
		}
	}
	return columns, d.end()
}

// DecodeCommandComplete decodes a CommandComplete tag.
func DecodeCommandComplete(payload []byte) (string, error) {
	d := decoder{data: payload}
	tag, err := d.cstring()
	if err != nil {
		return "", err
	}
	return tag, d.end()
}

// DecodeCopyInResponse decodes a CopyInResponse message.
func DecodeCopyInResponse(payload []byte) (CopyInResponse, error) {
	d := decoder{data: payload}
	format, err := d.int8()
	if err != nil {
		return CopyInResponse{}, err
	}
	count, err := d.int16()
	if err != nil {
		return CopyInResponse{}, err
	}
	if count < 0 {
		return CopyInResponse{}, fmt.Errorf("wire: negative copy column count %d", count)
	}
	resp := CopyInResponse{Format: format, ColumnFormats: make([]int16, 0, count)}
	for i := 0; i < int(count); i++ {
		columnFormat, err := d.int16()
		if err != nil {
			return CopyInResponse{}, err
		}
		resp.ColumnFormats = append(resp.ColumnFormats, columnFormat)
	}
	return resp, d.end()
}

// DecodeNotification decodes a NotificationResponse message.
func DecodeNotification(payload []byte) (Notification, error) {
	d := decoder{data: payload}
	pid, err := d.uint32()
	if err != nil {
		return Notification{}, err
	}
	channel, err := d.cstring()
	if err != nil {
		return Notification{}, err
	}
	notificationPayload, err := d.cstring()
	if err != nil {
		return Notification{}, err
	}
	return Notification{PID: pid, Channel: channel, Payload: notificationPayload}, d.end()
}

// EncodeAuthenticationOk returns an AuthenticationOk payload.
func EncodeAuthenticationOk() []byte {
	return AppendInt32(nil, AuthOK)
}

// EncodeAuthenticationCleartext returns a cleartext password request payload.
func EncodeAuthenticationCleartext() []byte {
	return AppendInt32(nil, AuthCleartext)
}

// EncodeAuthenticationMD5 returns an MD5 password request payload.
func EncodeAuthenticationMD5(salt [4]byte) []byte {
	payload := AppendInt32(nil, AuthMD5)
	return append(payload, salt[:]...)
}

// EncodeAuthenticationSASL returns a SASL request payload.
func EncodeAuthenticationSASL(mechanisms ...string) []byte {
	payload := AppendInt32(nil, AuthSASL)
	for _, mechanism := range mechanisms {
		payload = AppendString(payload, mechanism)
	}
	return append(payload, 0)
}

// EncodeAuthenticationSASLContinue returns a SASLContinue payload.
func EncodeAuthenticationSASLContinue(data []byte) []byte {
	payload := AppendInt32(nil, AuthSASLContinue)
	return append(payload, data...)
}

// EncodeAuthenticationSASLFinal returns a SASLFinal payload.
func EncodeAuthenticationSASLFinal(data []byte) []byte {
	payload := AppendInt32(nil, AuthSASLFinal)
	return append(payload, data...)
}

// EncodeAuthenticationSM3 returns an SM3 challenge payload.
func EncodeAuthenticationSM3(salt [4]byte) []byte {
	payload := AppendInt32(nil, AuthSM3)
	return append(payload, salt[:]...)
}

// EncodeParameterStatus returns a ParameterStatus payload.
func EncodeParameterStatus(name, value string) []byte {
	payload := AppendString(nil, name)
	return AppendString(payload, value)
}

// EncodeBackendKeyData returns a BackendKeyData payload.
func EncodeBackendKeyData(pid, secret uint32) []byte {
	payload := AppendInt32(nil, int32(pid))
	return AppendInt32(payload, int32(secret))
}

// EncodeReadyForQuery returns a ReadyForQuery payload.
func EncodeReadyForQuery(status byte) []byte {
	return []byte{status}
}

// EncodeRowDescription returns a RowDescription payload.
func EncodeRowDescription(columns []ColumnDescription) []byte {
	payload := AppendInt16(nil, int16(len(columns)))
	for _, col := range columns {
		payload = AppendString(payload, col.Name)
		payload = AppendInt32(payload, int32(col.TableOID))
		payload = AppendInt16(payload, col.AttrNum)
		payload = AppendInt32(payload, int32(col.TypeOID))
		payload = AppendInt16(payload, col.TypeLen)
		payload = AppendInt32(payload, col.TypeMod)
		payload = AppendInt16(payload, col.Format)
	}
	return payload
}

// EncodeDataRow returns a DataRow payload. A nil column encodes SQL NULL.
func EncodeDataRow(columns [][]byte) []byte {
	payload := AppendInt16(nil, int16(len(columns)))
	for _, column := range columns {
		if column == nil {
			payload = AppendInt32(payload, -1)
			continue
		}
		payload = AppendInt32(payload, int32(len(column)))
		payload = append(payload, column...)
	}
	return payload
}

// EncodeCommandComplete returns a CommandComplete payload.
func EncodeCommandComplete(tag string) []byte {
	return AppendString(nil, tag)
}

// EncodeErrorResponse returns an ErrorResponse payload.
func EncodeErrorResponse(fields ...ErrorField) []byte {
	return encodeErrorFields(fields)
}

// EncodeNoticeResponse returns a NoticeResponse payload.
func EncodeNoticeResponse(fields ...ErrorField) []byte {
	return encodeErrorFields(fields)
}

func encodeErrorFields(fields []ErrorField) []byte {
	var payload []byte
	for _, field := range fields {
		payload = append(payload, field.Code)
		payload = AppendString(payload, field.Value)
	}
	return append(payload, 0)
}

// EncodeCopyInResponse returns a CopyInResponse payload.
func EncodeCopyInResponse(format int8, columnFormats []int16) []byte {
	payload := append([]byte{byte(format)}, AppendInt16(nil, int16(len(columnFormats)))...)
	for _, columnFormat := range columnFormats {
		payload = AppendInt16(payload, columnFormat)
	}
	return payload
}

// EncodeNotificationResponse returns a NotificationResponse payload.
func EncodeNotificationResponse(pid uint32, channel, payload string) []byte {
	buf := AppendInt32(nil, int32(pid))
	buf = AppendString(buf, channel)
	return AppendString(buf, payload)
}

// DecodeQuery decodes a Query payload.
func DecodeQuery(payload []byte) (string, error) {
	d := decoder{data: payload}
	sql, err := d.cstring()
	if err != nil {
		return "", err
	}
	return sql, d.end()
}

// DecodePasswordMessage decodes a PasswordMessage payload.
func DecodePasswordMessage(payload []byte) (string, error) {
	d := decoder{data: payload}
	password, err := d.cstring()
	if err != nil {
		return "", err
	}
	return password, d.end()
}

// DecodeSASLInitialResponse decodes a SASLInitialResponse payload: mechanism,
// initial client response length (-1 means omitted) and response bytes.
func DecodeSASLInitialResponse(payload []byte) (mechanism string, data []byte, err error) {
	d := decoder{data: payload}
	if mechanism, err = d.cstring(); err != nil {
		return "", nil, err
	}
	length, err := d.int32()
	if err != nil {
		return "", nil, err
	}
	if length == -1 {
		return mechanism, nil, d.end()
	}
	if length < 0 || int(length) != len(d.data)-d.off {
		return "", nil, fmt.Errorf("wire: malformed %s length %d", "SASLInitialResponse", length)
	}
	data = d.remaining()
	d.off = len(d.data)
	return mechanism, data, d.end()
}

// DecodeParse decodes a Parse payload.
func DecodeParse(payload []byte) (name, sql string, paramOIDs []uint32, err error) {
	d := decoder{data: payload}
	if name, err = d.cstring(); err != nil {
		return "", "", nil, err
	}
	if sql, err = d.cstring(); err != nil {
		return "", "", nil, err
	}
	count, err := d.int16()
	if err != nil {
		return "", "", nil, err
	}
	if count < 0 {
		return "", "", nil, fmt.Errorf("wire: negative parameter count %d", count)
	}
	paramOIDs = make([]uint32, 0, count)
	for i := 0; i < int(count); i++ {
		oid, err := d.uint32()
		if err != nil {
			return "", "", nil, err
		}
		paramOIDs = append(paramOIDs, oid)
	}
	return name, sql, paramOIDs, d.end()
}

// DecodeBind decodes a Bind payload.
func DecodeBind(payload []byte) (portal, statement string, paramFormats []int16, params [][]byte, resultFormats []int16, err error) {
	d := decoder{data: payload}
	if portal, err = d.cstring(); err != nil {
		return "", "", nil, nil, nil, err
	}
	if statement, err = d.cstring(); err != nil {
		return "", "", nil, nil, nil, err
	}
	if paramFormats, err = d.int16Slice(); err != nil {
		return "", "", nil, nil, nil, err
	}
	count, err := d.int16()
	if err != nil {
		return "", "", nil, nil, nil, err
	}
	if count < 0 {
		return "", "", nil, nil, nil, fmt.Errorf("wire: negative parameter count %d", count)
	}
	params = make([][]byte, 0, count)
	for i := 0; i < int(count); i++ {
		length, err := d.int32()
		if err != nil {
			return "", "", nil, nil, nil, err
		}
		switch {
		case length == -1:
			params = append(params, nil)
		case length < -1:
			return "", "", nil, nil, nil, fmt.Errorf("wire: invalid parameter length %d", length)
		default:
			data, err := d.bytes(int(length))
			if err != nil {
				return "", "", nil, nil, nil, err
			}
			params = append(params, data)
		}
	}
	if resultFormats, err = d.int16Slice(); err != nil {
		return "", "", nil, nil, nil, err
	}
	return portal, statement, paramFormats, params, resultFormats, d.end()
}

// DecodeDescribe decodes a Describe payload.
func DecodeDescribe(payload []byte) (kind byte, name string, err error) {
	d := decoder{data: payload}
	rawKind, err := d.int8()
	if err != nil {
		return 0, "", err
	}
	kind = byte(rawKind)
	name, err = d.cstring()
	if err != nil {
		return 0, "", err
	}
	return kind, name, d.end()
}

// DecodeExecute decodes an Execute payload.
func DecodeExecute(payload []byte) (portal string, maxRows int32, err error) {
	d := decoder{data: payload}
	if portal, err = d.cstring(); err != nil {
		return "", 0, err
	}
	if maxRows, err = d.int32(); err != nil {
		return "", 0, err
	}
	return portal, maxRows, d.end()
}

// DecodeCopyFail decodes a CopyFail payload.
func DecodeCopyFail(payload []byte) (string, error) {
	d := decoder{data: payload}
	reason, err := d.cstring()
	if err != nil {
		return "", err
	}
	return reason, d.end()
}

type decoder struct {
	data []byte
	off  int
}

func (d *decoder) int8() (int8, error) {
	if d.off+1 > len(d.data) {
		return 0, fmt.Errorf(errTruncated, "int8")
	}
	v := int8(d.data[d.off])
	d.off++
	return v, nil
}

func (d *decoder) int16() (int16, error) {
	if d.off+2 > len(d.data) {
		return 0, fmt.Errorf(errTruncated, "int16")
	}
	v := int16(binary.BigEndian.Uint16(d.data[d.off:]))
	d.off += 2
	return v, nil
}

func (d *decoder) int32() (int32, error) {
	if d.off+4 > len(d.data) {
		return 0, fmt.Errorf(errTruncated, "int32")
	}
	v := int32(binary.BigEndian.Uint32(d.data[d.off:]))
	d.off += 4
	return v, nil
}

func (d *decoder) uint32() (uint32, error) {
	if d.off+4 > len(d.data) {
		return 0, fmt.Errorf(errTruncated, "uint32")
	}
	v := binary.BigEndian.Uint32(d.data[d.off:])
	d.off += 4
	return v, nil
}

func (d *decoder) cstring() (string, error) {
	i := bytes.IndexByte(d.data[d.off:], 0)
	if i < 0 {
		return "", fmt.Errorf(errTruncated, "string")
	}
	s := string(d.data[d.off : d.off+i])
	d.off += i + 1
	return s, nil
}

func (d *decoder) bytes(n int) ([]byte, error) {
	if n < 0 || d.off+n > len(d.data) {
		return nil, fmt.Errorf(errTruncated, "bytes")
	}
	b := d.data[d.off : d.off+n]
	d.off += n
	return b, nil
}

func (d *decoder) int16Slice() ([]int16, error) {
	count, err := d.int16()
	if err != nil {
		return nil, err
	}
	if count < 0 {
		return nil, fmt.Errorf("wire: negative format count %d", count)
	}
	formats := make([]int16, 0, count)
	for i := 0; i < int(count); i++ {
		format, err := d.int16()
		if err != nil {
			return nil, err
		}
		formats = append(formats, format)
	}
	return formats, nil
}

func (d *decoder) remaining() []byte {
	return d.data[d.off:]
}

func (d *decoder) end() error {
	if d.off != len(d.data) {
		return fmt.Errorf("wire: %d unexpected trailing bytes", len(d.data)-d.off)
	}
	return nil
}
