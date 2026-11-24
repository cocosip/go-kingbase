package wire

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// chunkReader returns its data in chunks of at most n bytes to simulate TCP
// splits and coalescing.
type chunkReader struct {
	data []byte
	n    int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.n
	if n <= 0 || n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestReadMessageSplitBytes(t *testing.T) {
	frames := new(bytes.Buffer)
	writer := NewWriter(frames)
	if err := writer.WriteMessage(MsgReadyForQuery, EncodeReadyForQuery(StatusIdle)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if err := writer.WriteMessage(MsgCommandComplete, EncodeCommandComplete("SELECT 1")); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	for _, chunk := range []int{1, 2, 3} {
		reader := NewReader(&chunkReader{data: frames.Bytes(), n: chunk}, 0)
		typ, payload, err := reader.ReadMessage()
		if err != nil || typ != MsgReadyForQuery || !bytes.Equal(payload, []byte{StatusIdle}) {
			t.Fatalf("chunk %d: first message = %q/%x err %v", chunk, typ, payload, err)
		}
		typ, payload, err = reader.ReadMessage()
		if err != nil || typ != MsgCommandComplete || string(payload) != "SELECT 1\x00" {
			t.Fatalf("chunk %d: second message = %q/%q err %v", chunk, typ, payload, err)
		}
		if _, _, err := reader.ReadMessage(); !errors.Is(err, io.EOF) {
			t.Fatalf("chunk %d: want EOF, got %v", chunk, err)
		}
	}
}

func TestReadMessageRejectsBadLength(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		max   int32
		want  string
	}{
		{"length below header", []byte{'Z', 0, 0, 0, 3}, 0, "invalid message length"},
		{"length of zero", []byte{'Z', 0, 0, 0, 0}, 0, "invalid message length"},
		{"payload over limit", []byte{'Z', 0, 0, 0, 21}, 16, "exceeds limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NewReader(bytes.NewReader(tc.bytes), tc.max).ReadMessage()
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(tc.want)) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestReadMessageTruncatedPayload(t *testing.T) {
	// Length announces 10 payload bytes; only 2 are present.
	_, _, err := NewReader(bytes.NewReader([]byte{'Z', 0, 0, 0, 10, 'I', 'x'}), 0).ReadMessage()
	if err == nil {
		t.Fatal("ReadMessage accepted a truncated payload")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v, want EOF kind", err)
	}
}

func TestStartupRoundTrip(t *testing.T) {
	params := [][2]string{{"user", "alice"}, {"database", "tenant"}, {"application_name", "svc"}}
	decoded, err := DecodeStartupParams(EncodeStartupPayload(params))
	if err != nil {
		t.Fatalf("DecodeStartupParams: %v", err)
	}
	if len(decoded) != len(params) {
		t.Fatalf("decoded %d params, want %d", len(decoded), len(params))
	}
	for i := range params {
		if decoded[i] != params[i] {
			t.Errorf("param %d = %q, want %q", i, decoded[i], params[i])
		}
	}

	payload := EncodeStartupPayload(params)
	if _, err := DecodeStartupParams(payload[:len(payload)-1]); err == nil {
		t.Error("DecodeStartupParams accepted a packet without terminator")
	}
	if _, err := DecodeStartupParams(AppendInt32(nil, 2<<16)); err == nil {
		t.Error("DecodeStartupParams accepted protocol version 2.0")
	}
}

func TestSpecialPackets(t *testing.T) {
	ssl := EncodeSSLRequest()
	wantSSL := []byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}
	if !bytes.Equal(ssl, wantSSL) {
		t.Errorf("SSLRequest = %x, want %x", ssl, wantSSL)
	}
	cancel := EncodeCancelRequest(1234, 5678)
	if len(cancel) != 16 {
		t.Fatalf("CancelRequest length = %d, want 16", len(cancel))
	}
	if got := int32(cancel[4])<<24 | int32(cancel[5])<<16 | int32(cancel[6])<<8 | int32(cancel[7]); got != CancelRequestCode {
		t.Errorf("CancelRequest code = %d, want %d", got, CancelRequestCode)
	}
}

func TestRowDescriptionRoundTrip(t *testing.T) {
	columns := []ColumnDescription{
		{Name: "id", TableOID: 16384, AttrNum: 1, TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1},
		{Name: "name", TableOID: 0, AttrNum: 2, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
	}
	decoded, err := DecodeRowDescription(EncodeRowDescription(columns))
	if err != nil {
		t.Fatalf("DecodeRowDescription: %v", err)
	}
	if len(decoded) != len(columns) {
		t.Fatalf("decoded %d columns, want %d", len(decoded), len(columns))
	}
	for i := range columns {
		if decoded[i] != columns[i] {
			t.Errorf("column %d = %+v, want %+v", i, decoded[i], columns[i])
		}
	}

	if _, err := DecodeRowDescription(EncodeRowDescription(columns)[:5]); err == nil {
		t.Error("DecodeRowDescription accepted a truncated message")
	}
	if _, err := DecodeRowDescription([]byte{0xff, 0xff}); err == nil {
		t.Error("DecodeRowDescription accepted a negative field count")
	}
}

func TestDataRowRoundTrip(t *testing.T) {
	columns := [][]byte{[]byte("42"), nil, {}}
	decoded, err := DecodeDataRow(EncodeDataRow(columns))
	if err != nil {
		t.Fatalf("DecodeDataRow: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("decoded %d columns, want 3", len(decoded))
	}
	if decoded[0].Null || string(decoded[0].Data) != "42" {
		t.Errorf("column 0 = %+v, want text 42", decoded[0])
	}
	if !decoded[1].Null {
		t.Errorf("column 1 = %+v, want NULL", decoded[1])
	}
	if decoded[2].Null || len(decoded[2].Data) != 0 {
		t.Errorf("column 2 = %+v, want non-NULL empty value", decoded[2])
	}

	if _, err := DecodeDataRow([]byte{0, 1, 0xff, 0xff, 0xfe}); err == nil {
		t.Error("DecodeDataRow accepted length -2")
	}
}

func TestErrorFieldsRoundTrip(t *testing.T) {
	fields := []ErrorField{
		{Code: ErrorSeverity, Value: "ERROR"},
		{Code: ErrorCode, Value: "23505"},
		{Code: ErrorMessage, Value: "duplicate key"},
		{Code: ErrorFile, Value: "xact.c"},
		{Code: 'Z', Value: "extension"},
	}
	decoded, err := DecodeErrorFields(EncodeErrorResponse(fields...))
	if err != nil {
		t.Fatalf("DecodeErrorFields: %v", err)
	}
	if len(decoded) != len(fields) {
		t.Fatalf("decoded %d fields, want %d", len(decoded), len(fields))
	}
	for i := range fields {
		if decoded[i] != fields[i] {
			t.Errorf("field %d = %+v, want %+v", i, decoded[i], fields[i])
		}
	}

	if _, err := DecodeErrorFields([]byte{'S', 'E', 'R', 'R', 'O', 'R'}); err == nil {
		t.Error("DecodeErrorFields accepted a missing terminator")
	}
	if _, err := DecodeErrorFields([]byte{0, 'x'}); err == nil {
		t.Error("DecodeErrorFields accepted trailing bytes")
	}
}

func TestAuthenticationDecode(t *testing.T) {
	auth, err := DecodeAuthentication(EncodeAuthenticationCleartext())
	if err != nil || auth.Code != AuthCleartext {
		t.Fatalf("cleartext auth = %+v err %v", auth, err)
	}

	salt := [4]byte{1, 2, 3, 4}
	auth, err = DecodeAuthentication(EncodeAuthenticationMD5(salt))
	if err != nil || auth.Code != AuthMD5 || !bytes.Equal(auth.Salt, salt[:]) {
		t.Fatalf("md5 auth = %+v err %v", auth, err)
	}

	auth, err = DecodeAuthentication(EncodeAuthenticationSASL("SCRAM-SHA-256", "SCRAM-SHA-256-PLUS"))
	if err != nil || auth.Code != AuthSASL || len(auth.Mechanisms) != 2 || auth.Mechanisms[1] != "SCRAM-SHA-256-PLUS" {
		t.Fatalf("sasl auth = %+v err %v", auth, err)
	}

	auth, err = DecodeAuthentication(EncodeAuthenticationSASLContinue([]byte("r=nonce")))
	if err != nil || auth.Code != AuthSASLContinue || string(auth.Data) != "r=nonce" {
		t.Fatalf("sasl continue = %+v err %v", auth, err)
	}

	if _, err := DecodeAuthentication(AppendInt32(nil, AuthMD5)); err == nil {
		t.Error("DecodeAuthentication accepted MD5 without salt")
	}
}

func TestClientMessageDecodeRoundTrip(t *testing.T) {
	sql, err := DecodeQuery(EncodeQuery("SELECT 1"))
	if err != nil || sql != "SELECT 1" {
		t.Fatalf("query = %q err %v", sql, err)
	}

	password, err := DecodePasswordMessage(EncodePasswordMessage("secret"))
	if err != nil || password != "secret" {
		t.Fatalf("password = %q err %v", password, err)
	}

	name, statement, oids, err := DecodeParse(EncodeParse("s1", "SELECT $1", []uint32{23, 25}))
	if err != nil || name != "s1" || statement != "SELECT $1" || len(oids) != 2 || oids[1] != 25 {
		t.Fatalf("parse = %q/%q/%v err %v", name, statement, oids, err)
	}

	portal, stmt, formats, params, results, err := DecodeBind(
		EncodeBind("", "s1", []int16{1}, [][]byte{[]byte("7"), nil}, []int16{0}))
	if err != nil || portal != "" || stmt != "s1" || len(formats) != 1 || formats[0] != 1 ||
		len(params) != 2 || string(params[0]) != "7" || params[1] != nil || len(results) != 1 {
		t.Fatalf("bind = %q/%q/%v/%v/%v err %v", portal, stmt, formats, params, results, err)
	}

	kind, describeName, err := DecodeDescribe(EncodeDescribe('S', "s1"))
	if err != nil || kind != 'S' || describeName != "s1" {
		t.Fatalf("describe = %q/%q err %v", kind, describeName, err)
	}

	portal, maxRows, err := DecodeExecute(EncodeExecute("p1", 10))
	if err != nil || portal != "p1" || maxRows != 10 {
		t.Fatalf("execute = %q/%d err %v", portal, maxRows, err)
	}

	reason, err := DecodeCopyFail(EncodeCopyFail("aborted"))
	if err != nil || reason != "aborted" {
		t.Fatalf("copy fail = %q err %v", reason, err)
	}
}

func TestCopyInAndNotificationDecode(t *testing.T) {
	copyIn, err := DecodeCopyInResponse(EncodeCopyInResponse(0, []int16{0, 0}))
	if err != nil || copyIn.Format != 0 || len(copyIn.ColumnFormats) != 2 {
		t.Fatalf("copy in = %+v err %v", copyIn, err)
	}

	notification, err := DecodeNotification(EncodeNotificationResponse(99, "chan", "payload"))
	if err != nil || notification.PID != 99 || notification.Channel != "chan" || notification.Payload != "payload" {
		t.Fatalf("notification = %+v err %v", notification, err)
	}

	name, value, err := DecodeParameterStatus(EncodeParameterStatus("server_version", "12.1"))
	if err != nil || name != "server_version" || value != "12.1" {
		t.Fatalf("parameter status = %q/%q err %v", name, value, err)
	}

	pid, secret, err := DecodeBackendKeyData(EncodeBackendKeyData(11, 22))
	if err != nil || pid != 11 || secret != 22 {
		t.Fatalf("backend key data = %d/%d err %v", pid, secret, err)
	}

	tag, err := DecodeCommandComplete(EncodeCommandComplete("INSERT 0 1"))
	if err != nil || tag != "INSERT 0 1" {
		t.Fatalf("command complete = %q err %v", tag, err)
	}

	if _, err := DecodeCommandComplete(nil); err == nil {
		t.Error("DecodeCommandComplete accepted an empty payload")
	}
}
