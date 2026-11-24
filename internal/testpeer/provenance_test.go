package testpeer

import (
	"strings"
	"testing"
	"time"
)

func capturedProvenance() Provenance {
	return Provenance{
		Origin:       OriginCaptured,
		Product:      "KingbaseES V009R001C002B001",
		Edition:      "safe",
		Mode:         "pg",
		WireProtocol: "3.0",
		Query:        "SELECT t.oid, n.nspname, t.typname FROM pg_catalog.pg_type AS t",
		CapturedAt:   time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC),
		Redaction:    "credentials removed",
	}
}

func TestProvenanceOrigins(t *testing.T) {
	cases := []struct {
		name    string
		origin  Provenance
		wantErr string
	}{
		{"synthetic", Provenance{Origin: OriginSynthetic}, ""},
		{"synthetic-claims-capture", Provenance{Origin: OriginSynthetic, Product: "KingbaseES"}, "claims capture or SDK"},
		{"replay", Provenance{Origin: OriginReplay}, ""},
		{"replay-claims-capture", Provenance{Origin: OriginReplay, Query: "SELECT 1"}, "claims capture or SDK"},
		{"sdk", Provenance{Origin: OriginSDK, SDKVersion: "42.7.0", SDKHash: "sha256:abc"}, ""},
		{"sdk-missing-hash", Provenance{Origin: OriginSDK, SDKVersion: "42.7.0"}, "package version and artifact hash"},
		{"sdk-whitespace-hash", Provenance{Origin: OriginSDK, SDKVersion: "42.7.0", SDKHash: "  "}, "package version and artifact hash"},
		{"sdk-claims-query", Provenance{Origin: OriginSDK, SDKVersion: "42.7.0", SDKHash: "sha256:abc", Query: "SELECT 1"}, "SDK fixture claims capture"},
		{"captured", capturedProvenance(), ""},
		{"captured-missing-edition", func() Provenance {
			p := capturedProvenance()
			p.Edition = ""
			return p
		}(), "edition"},
		{"captured-whitespace-product", func() Provenance {
			p := capturedProvenance()
			p.Product = " \t"
			return p
		}(), "product"},
		{"captured-missing-redaction", func() Provenance {
			p := capturedProvenance()
			p.Redaction = ""
			return p
		}(), "redaction note"},
		{"captured-sdk-label", func() Provenance {
			p := capturedProvenance()
			p.SDKHash = "sha256:abc"
			return p
		}(), "captured fixture carries SDK"},
		{"unknown-origin", Provenance{Origin: 9}, "no origin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.origin.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestOriginString(t *testing.T) {
	cases := map[Origin]string{
		OriginUnknown:   "unknown",
		OriginSynthetic: "synthetic",
		OriginSDK:       "sdk",
		OriginReplay:    "replay",
		OriginCaptured:  "captured",
		Origin(9):       "origin(9)",
	}
	for origin, want := range cases {
		if got := origin.String(); got != want {
			t.Errorf("Origin(%d).String() = %q, want %q", uint8(origin), got, want)
		}
	}
}

func TestCatalogCaptureValidation(t *testing.T) {
	valid := CatalogCapture{Provenance: capturedProvenance(), Rows: CatalogRows()}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid capture: %v", err)
	}
	replay := CatalogCapture{Provenance: Provenance{Origin: OriginReplay}, Rows: CatalogRows()}
	if err := replay.Validate(); err != nil {
		t.Fatalf("replay capture: %v", err)
	}
	synthetic := CatalogCapture{Provenance: Provenance{Origin: OriginSynthetic}, Rows: CatalogRows()}
	if err := synthetic.Validate(); err == nil || !strings.Contains(err.Error(), "not a replayable") {
		t.Fatalf("synthetic capture Validate = %v, want a replay refusal", err)
	}
	unlabelled := CatalogCapture{Rows: CatalogRows()}
	if err := unlabelled.Validate(); err == nil || !strings.Contains(err.Error(), "not a replayable") {
		t.Fatalf("unlabelled capture Validate = %v, want a replay refusal", err)
	}
	empty := CatalogCapture{Provenance: capturedProvenance()}
	if err := empty.Validate(); err == nil || !strings.Contains(err.Error(), "no rows") {
		t.Fatalf("empty capture Validate = %v, want a no-rows error", err)
	}
	narrow := CatalogCapture{
		Provenance: capturedProvenance(),
		Rows:       [][][]byte{{[]byte("23")}},
	}
	if err := narrow.Validate(); err == nil || !strings.Contains(err.Error(), "columns") {
		t.Fatalf("narrow capture Validate = %v, want a column-count error", err)
	}
	duplicate := CatalogCapture{Provenance: capturedProvenance(), Rows: append(CatalogRows(), CatalogRows()[0])}
	if err := duplicate.Validate(); err == nil || !strings.Contains(err.Error(), "repeats OID") {
		t.Fatalf("duplicate capture Validate = %v, want a duplicate-OID error", err)
	}
	badOID := CatalogCapture{
		Provenance: capturedProvenance(),
		Rows:       [][][]byte{append([][]byte{[]byte("x")}, CatalogRows()[0][1:]...)},
	}
	if err := badOID.Validate(); err == nil || !strings.Contains(err.Error(), "non-numeric OID") {
		t.Fatalf("bad-OID capture Validate = %v, want a non-numeric error", err)
	}
}
