package testpeer

import (
	"strings"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// defaultVersionReport is the raw product string the auto responder returns for
// the driver's bounded identity candidate.
const defaultVersionReport = "KingbaseES V009R001C002B001 build 20240101 (PostgreSQL 12.1)"

// SetVersionReport overrides the raw identity report. An empty value disables
// the auto responder so a script can answer or fail the version query itself.
func (c *Conn) SetVersionReport(report string) {
	c.versionReport = report
}

// answerIdentityQuery answers the driver's SELECT version() candidate when auto
// identity is enabled. It reports whether the message was consumed.
func (c *Conn) answerIdentityQuery(typ wire.MsgType, payload []byte) (bool, error) {
	if c.versionReport == "" || typ != wire.MsgQuery {
		return false, nil
	}
	sql, err := wire.DecodeQuery(payload)
	if err != nil {
		return false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(sql), "select version()") {
		return false, nil
	}
	if err := c.RowDescription([]wire.ColumnDescription{
		{Name: "version", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
	}); err != nil {
		return true, err
	}
	if err := c.DataRow([][]byte{[]byte(c.versionReport)}); err != nil {
		return true, err
	}
	if err := c.CommandComplete("SELECT 1"); err != nil {
		return true, err
	}
	return true, c.ReadyForQuery(wire.StatusIdle)
}
