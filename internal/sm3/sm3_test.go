package sm3

import (
	"bytes"
	"crypto/hmac"
	"encoding/hex"
	"hash"
	"strings"
	"testing"
)

func sum(t *testing.T, data []byte) string {
	t.Helper()
	h := New()
	if _, err := h.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestStandardVectors(t *testing.T) {
	// GB/T 32905-2016 appendix A test vectors.
	cases := []struct {
		name string
		data string
		want string
	}{
		{
			name: "abc",
			data: "abc",
			want: "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0",
		},
		{
			name: "abcd repeated 16 times",
			data: strings.Repeat("abcd", 16),
			want: "debe9ff92275b8a138604889c18e5a4d6fdb70e5387e5765293dcba39c0c5732",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sum(t, []byte(tc.data)); got != tc.want {
				t.Errorf("SM3(%q) = %s, want %s", tc.data, got, tc.want)
			}
		})
	}
}

func TestStreamingEqualsOneShot(t *testing.T) {
	data := []byte(strings.Repeat("go-kingbase-", 37))
	oneShot := sum(t, data)
	for _, chunk := range []int{1, 7, 63, 64, 65, 100} {
		h := New()
		for offset := 0; offset < len(data); offset += chunk {
			end := offset + chunk
			if end > len(data) {
				end = len(data)
			}
			if _, err := h.Write(data[offset:end]); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != oneShot {
			t.Errorf("chunk %d digest = %s, want %s", chunk, got, oneShot)
		}
	}
}

func TestSumDoesNotMutateState(t *testing.T) {
	h := New()
	_, _ = h.Write([]byte("abc"))
	first := h.Sum(nil)
	second := h.Sum(nil)
	if !bytes.Equal(first, second) {
		t.Error("Sum mutated the hash state")
	}
}

func TestHMACCompatibility(t *testing.T) {
	var newHash = func() hash.Hash { return New() }
	mac := hmac.New(newHash, []byte("key"))
	_, _ = mac.Write([]byte("message"))
	if got := mac.Size(); got != Size {
		t.Errorf("HMAC size = %d, want %d", got, Size)
	}
	if len(mac.Sum(nil)) != Size {
		t.Errorf("HMAC digest length = %d, want %d", len(mac.Sum(nil)), Size)
	}
	if h := New(); h.Size() != Size || h.BlockSize() != BlockSize {
		t.Errorf("Size/BlockSize = %d/%d, want %d/%d", h.Size(), h.BlockSize(), Size, BlockSize)
	}
}
