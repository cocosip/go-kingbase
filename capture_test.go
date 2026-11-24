package gokb

import (
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// testCatalogCapture replays the scripted rows under the replay origin so the
// capture path is exercised without claiming a server capture.
func testCatalogCapture() testpeer.CatalogCapture {
	return testpeer.CatalogCapture{
		Provenance: testpeer.Provenance{Origin: testpeer.OriginReplay},
		Rows:       testpeer.CatalogRows(),
	}
}

func serveCapturedCatalog(peer *testpeer.Conn) {
	peer.DisableAutoCatalog()
	if _, err := peer.ReadStartup(); err != nil {
		return
	}
	if err := peer.StartupOK(nil); err != nil {
		return
	}
	for {
		message, payload, err := peer.ReadMessage()
		if err != nil {
			return
		}
		if message == wire.MsgQuery {
			sql, err := wire.DecodeQuery(payload)
			if err != nil || sql != catalogQuery {
				peer.Errorf("catalog query = %q err %v, want the recorded capture query", sql, err)
				return
			}
			break
		}
	}
	if err := peer.ServeCatalogCapture(testCatalogCapture()); err != nil {
		peer.Errorf("ServeCatalogCapture: %v", err)
		return
	}
	waitTerminate(peer)
}

func TestCatalogCaptureReplay(t *testing.T) {
	srv := testpeer.Start(t, serveCapturedCatalog)
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if labels := physical.typeLabels(); labels[23] != "pg_catalog.int4" {
		t.Errorf("catalog[23] = %q, want the capture to load verified identities", labels[23])
	}
	if physical.catalogStatus != CatalogFull {
		t.Errorf("catalogStatus = %d, want full", physical.catalogStatus)
	}
}
