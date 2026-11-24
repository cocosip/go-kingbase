package auth

import (
	"strings"
	"testing"
)

// RFC 7677 section 3 test vector for SCRAM-SHA-256.
const (
	scramTestClientFirst = "n,,n=user,r=rOprNGfwEbeRWgbNEkqO"
	scramTestServerFirst = "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0," +
		"s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
	scramTestClientFinal = "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0," +
		"p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	scramTestServerFinal = "v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="
)

func newTestSCRAMClient() *SCRAMClient {
	return &SCRAMClient{
		username: "user",
		password: "pencil",
		nonce:    "rOprNGfwEbeRWgbNEkqO",
	}
}

func TestSCRAMRFCVector(t *testing.T) {
	client := newTestSCRAMClient()
	if got := string(client.ClientFirst()); got != scramTestClientFirst {
		t.Fatalf("client-first = %q, want %q", got, scramTestClientFirst)
	}
	final, err := client.HandleServerFirst([]byte(scramTestServerFirst))
	if err != nil {
		t.Fatalf("HandleServerFirst: %v", err)
	}
	if string(final) != scramTestClientFinal {
		t.Fatalf("client-final = %q, want %q", final, scramTestClientFinal)
	}
	if err := client.VerifyServerFinal([]byte(scramTestServerFinal)); err != nil {
		t.Fatalf("VerifyServerFinal: %v", err)
	}
}

func TestSCRAMServerFinalError(t *testing.T) {
	client := newTestSCRAMClient()
	client.ClientFirst()
	if _, err := client.HandleServerFirst([]byte(scramTestServerFirst)); err != nil {
		t.Fatalf("HandleServerFirst: %v", err)
	}
	if err := client.VerifyServerFinal([]byte("e=invalid-proof")); err == nil ||
		!strings.Contains(err.Error(), "invalid-proof") {
		t.Fatalf("error = %v, want server-side rejection message", err)
	}
}

func TestSCRAMServerSignatureMismatch(t *testing.T) {
	client := newTestSCRAMClient()
	client.ClientFirst()
	if _, err := client.HandleServerFirst([]byte(scramTestServerFirst)); err != nil {
		t.Fatalf("HandleServerFirst: %v", err)
	}
	wrong := "v=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if err := client.VerifyServerFinal([]byte(wrong)); err == nil ||
		!strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("error = %v, want signature mismatch", err)
	}
}

func TestSCRAMRejectsMalformedServerFirst(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{"nonce mismatch", "r=otherNonce,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", "nonce"},
		{"nonce extension missing", "r=rOprNGfwEbeRWgbNEkqO,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", "nonce"},
		{"low iterations", "r=rOprNGfwEbeRWgbNEkqOAAAA,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=1000", "iteration"},
		{"high iterations", "r=rOprNGfwEbeRWgbNEkqOAAAA,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=1000001", "iteration"},
		{"bad salt", "r=rOprNGfwEbeRWgbNEkqOAAAA,s=%%%,i=4096", "base64"},
		{"missing attribute", "r=rOprNGfwEbeRWgbNEkqOAAAA,i=4096", "lacks"},
		{"duplicate attribute", "r=a,r=b,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", "duplicate"},
		{"mandatory extension", "m=ext,r=rOprNGfwEbeRWgbNEkqOAAAA,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", "extension"},
		{"malformed attribute", "r", "malformed"},
		{"oversized", "r=" + strings.Repeat("x", maxSCRAMMessageLen), "too large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestSCRAMClient()
			client.ClientFirst()
			_, err := client.HandleServerFirst([]byte(tc.message))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestSCRAMRequiresClientFirst(t *testing.T) {
	client := newTestSCRAMClient()
	if _, err := client.HandleServerFirst([]byte(scramTestServerFirst)); err == nil ||
		!strings.Contains(err.Error(), "client-first") {
		t.Fatalf("error = %v, want client-first ordering error", err)
	}
}

func TestSCRAMUsernameEscaping(t *testing.T) {
	client, err := NewSCRAMClient("a=b,c", "pw")
	if err != nil {
		t.Fatalf("NewSCRAMClient: %v", err)
	}
	if first := string(client.ClientFirst()); !strings.HasPrefix(first, "n,,n=a=3Db=2Cc,r=") {
		t.Errorf("client-first = %q, want escaped user name", first)
	}
}

func TestNewSCRAMClientRandomNonce(t *testing.T) {
	first, err := NewSCRAMClient("", "pw")
	if err != nil {
		t.Fatalf("NewSCRAMClient: %v", err)
	}
	second, err := NewSCRAMClient("", "pw")
	if err != nil {
		t.Fatalf("NewSCRAMClient: %v", err)
	}
	firstMessage := string(first.ClientFirst())
	if firstMessage == string(second.ClientFirst()) {
		t.Fatal("two clients generated the same nonce")
	}
	if !strings.HasPrefix(firstMessage, "n,,n=,r=") {
		t.Errorf("client-first = %q, want empty user name", firstMessage)
	}
}
