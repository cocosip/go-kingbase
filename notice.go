package gokb

import (
	"errors"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// Notice is one asynchronous server notice or warning. It is informational and
// never fails the command that triggered it.
type Notice struct {
	Severity string
	Code     string
	Message  string
}

// NoticeHandler receives server notices while the driver reads the connection.
// It runs on the protocol reader: it must not issue a command on the same
// connection, and such a command is rejected instead of deadlocking.
type NoticeHandler func(Notice)

// errNoticeReentry rejects a command issued from a notice handler on the same
// connection: only the reader already inside the handler could consume its
// response.
var errNoticeReentry = errors.New("kingbase: a command cannot be issued from a notice handler")

// noticeFromFields maps notice/error fields onto the public notice.
func noticeFromFields(fields []wire.ErrorField) Notice {
	notice := Notice{}
	for _, field := range fields {
		switch field.Code {
		case wire.ErrorSeverity:
			notice.Severity = field.Value
		case wire.ErrorCode:
			notice.Code = field.Value
		case wire.ErrorMessage:
			notice.Message = field.Value
		}
	}
	return notice
}

// dispatchNotice decodes and reports one NoticeResponse. A nil handler discards
// the notice, and notices are never copied to stderr or allowed to fail the
// command in flight.
func (c *conn) dispatchNotice(payload []byte) error {
	handler := c.cfg.NoticeHandler
	if handler == nil {
		return nil
	}
	fields, err := wire.DecodeErrorFields(payload)
	if err != nil {
		// The frame itself was valid; a malformed notice is informational and
		// must not turn an unrelated command into a failure.
		return nil
	}
	c.inNotice.Add(1)
	defer c.inNotice.Add(-1)
	defer func() {
		if recovered := recover(); recovered != nil {
			// A panicking handler leaves the reader mid-response, so the
			// connection cannot be reused; the panic still reaches the caller.
			c.retire()
			panic(recovered)
		}
	}()
	handler(noticeFromFields(fields))
	return nil
}
