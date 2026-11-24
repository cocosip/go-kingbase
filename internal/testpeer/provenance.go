package testpeer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// Origin classifies where a catalog fixture came from. Synthetic, SDK and
// replay data are never reported as a server capture.
type Origin uint8

const (
	// OriginUnknown is the zero value: an unlabelled fixture must not pass as
	// synthetic.
	OriginUnknown Origin = iota
	// OriginSynthetic is a hand-written hostile or valid test case.
	OriginSynthetic
	// OriginSDK is a static vendor/JDBC OID table, kept for research only.
	OriginSDK
	// OriginReplay is a replay fixture that exercises the capture path without
	// claiming a server capture.
	OriginReplay
	// OriginCaptured is a genuine pg_type capture from a KingbaseES server.
	OriginCaptured
)

// String names the fixture origin for evidence and failure messages.
func (o Origin) String() string {
	switch o {
	case OriginUnknown:
		return "unknown"
	case OriginSynthetic:
		return "synthetic"
	case OriginSDK:
		return "sdk"
	case OriginReplay:
		return "replay"
	case OriginCaptured:
		return "captured"
	default:
		return fmt.Sprintf("origin(%d)", uint8(o))
	}
}

// Provenance records where catalog rows came from and which evidence they
// carry. Synthetic, SDK and replay fixtures fail validation if they claim a
// capture.
type Provenance struct {
	Origin Origin
	// Product, Edition, Mode, WireProtocol, Query, CapturedAt and Redaction
	// identify and describe a genuine capture: the KingbaseES build, database
	// mode, wire version, exact query text, capture time and how credentials or
	// private identifiers were removed.
	Product      string
	Edition      string
	Mode         string
	WireProtocol string
	Query        string
	CapturedAt   time.Time
	Redaction    string
	// SDKVersion and SDKHash label a static vendor/JDBC table.
	SDKVersion string
	SDKHash    string
}

// hasCaptureFields reports whether any capture-only field is set.
func (p Provenance) hasCaptureFields() bool {
	return p.Product != "" || p.Edition != "" || p.Mode != "" || p.WireProtocol != "" ||
		p.Query != "" || !p.CapturedAt.IsZero() || p.Redaction != ""
}

// hasSDKFields reports whether any static-table field is set.
func (p Provenance) hasSDKFields() bool {
	return p.SDKVersion != "" || p.SDKHash != ""
}

// Validate enforces per-origin evidence and keeps static data from claiming a
// server capture. Required fields are checked after trimming whitespace.
func (p Provenance) Validate() error {
	switch p.Origin {
	case OriginSynthetic, OriginReplay:
		if p.hasCaptureFields() || p.hasSDKFields() {
			return fmt.Errorf("testpeer: %s fixture claims capture or SDK provenance", p.Origin)
		}
		return nil
	case OriginSDK:
		if strings.TrimSpace(p.SDKVersion) == "" || strings.TrimSpace(p.SDKHash) == "" {
			return errors.New("testpeer: SDK fixture needs package version and artifact hash")
		}
		if p.hasCaptureFields() {
			return errors.New("testpeer: SDK fixture claims capture provenance")
		}
		return nil
	case OriginCaptured:
		missing := strings.TrimSpace(p.Product) == "" || strings.TrimSpace(p.Edition) == "" ||
			strings.TrimSpace(p.Mode) == "" || strings.TrimSpace(p.WireProtocol) == "" ||
			strings.TrimSpace(p.Query) == "" || p.CapturedAt.IsZero() ||
			strings.TrimSpace(p.Redaction) == ""
		if missing {
			return errors.New("testpeer: captured fixture needs product, edition, mode, wire protocol, query, capture time and redaction note")
		}
		if p.hasSDKFields() {
			return errors.New("testpeer: captured fixture carries SDK provenance")
		}
		return nil
	default:
		return fmt.Errorf("testpeer: catalog fixture has no origin (%d)", p.Origin)
	}
}

// CatalogCapture is a catalog snapshot replayed through the connect path with
// the provenance V3 requires for genuine captures.
type CatalogCapture struct {
	Provenance Provenance
	Rows       [][][]byte
}

// Validate rejects a capture without valid provenance, without rows, with a
// wrong column count or with duplicate OIDs.
func (c CatalogCapture) Validate() error {
	if c.Provenance.Origin != OriginCaptured && c.Provenance.Origin != OriginReplay {
		return fmt.Errorf("testpeer: %s fixture is not a replayable catalog capture", c.Provenance.Origin)
	}
	if err := c.Provenance.Validate(); err != nil {
		return err
	}
	if len(c.Rows) == 0 {
		return errors.New("testpeer: catalog capture has no rows")
	}
	width := len(CatalogColumns())
	seen := make(map[uint32]struct{}, len(c.Rows))
	for i, row := range c.Rows {
		if len(row) != width {
			return fmt.Errorf("testpeer: capture row %d has %d columns, want %d", i, len(row), width)
		}
		oid, err := strconv.ParseUint(string(row[0]), 10, 32)
		if err != nil {
			return fmt.Errorf("testpeer: capture row %d has a non-numeric OID", i)
		}
		if _, ok := seen[uint32(oid)]; ok {
			return fmt.Errorf("testpeer: capture repeats OID %d", oid)
		}
		seen[uint32(oid)] = struct{}{}
	}
	return nil
}

// ServeCatalogCapture replays a validated catalog capture in place of the
// scripted fixture.
func (c *Conn) ServeCatalogCapture(capture CatalogCapture) error {
	if err := capture.Validate(); err != nil {
		return err
	}
	if err := c.RowDescription(CatalogColumns()); err != nil {
		return err
	}
	for _, row := range capture.Rows {
		if err := c.DataRow(row); err != nil {
			return err
		}
	}
	if err := c.CommandComplete(fmt.Sprintf("SELECT %d", len(capture.Rows))); err != nil {
		return err
	}
	return c.ReadyForQuery(wire.StatusIdle)
}
