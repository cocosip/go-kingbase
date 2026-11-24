package testpeer

import (
	"strings"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// SetModeReport sets the database_mode the auto responder reports for the
// driver's discovery candidates; an empty value disables the responder so a
// script can observe or fail those queries itself.
func (c *Conn) SetModeReport(report string) {
	c.modeReport = report
}

// answerModeQuery answers one mode-discovery candidate when auto mode is
// enabled. It reports whether the message was consumed.
func (c *Conn) answerModeQuery(typ wire.MsgType, payload []byte) (bool, error) {
	if c.modeReport == "" || typ != wire.MsgQuery {
		return false, nil
	}
	sql, err := wire.DecodeQuery(payload)
	if err != nil {
		return false, nil
	}
	if strings.Contains(sql, "pg_catalog.pg_settings") {
		if err := c.RowDescription([]wire.ColumnDescription{
			{Name: "name", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
			{Name: "setting", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		}); err != nil {
			return true, err
		}
		if err := c.DataRow([][]byte{[]byte("database_mode"), []byte(c.modeReport)}); err != nil {
			return true, err
		}
		if err := c.CommandComplete("SELECT 1"); err != nil {
			return true, err
		}
		return true, c.ReadyForQuery(wire.StatusIdle)
	}
	if strings.EqualFold(strings.TrimSpace(sql), "show database_mode") {
		if err := c.RowDescription([]wire.ColumnDescription{
			{Name: "database_mode", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		}); err != nil {
			return true, err
		}
		if err := c.DataRow([][]byte{[]byte(c.modeReport)}); err != nil {
			return true, err
		}
		if err := c.CommandComplete("SHOW"); err != nil {
			return true, err
		}
		return true, c.ReadyForQuery(wire.StatusIdle)
	}
	return false, nil
}
