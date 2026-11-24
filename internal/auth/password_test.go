package auth

import (
	"crypto/hmac"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/sm3"
)

// expectedSM3 recomputes the response through a different code path (direct
// HMAC construction) to keep the production helper honest.
func expectedSM3(user, password string, salt []byte) string {
	innerMAC := hmac.New(sm3.New, []byte(user))
	_, _ = innerMAC.Write([]byte(password))
	inner := hex.EncodeToString(innerMAC.Sum(nil))
	outerMAC := hmac.New(sm3.New, salt)
	_, _ = outerMAC.Write([]byte(inner))
	return "sm3" + hex.EncodeToString(outerMAC.Sum(nil))
}

func TestSM3PasswordResponse(t *testing.T) {
	cases := []struct {
		user     string
		password string
		salt     []byte
	}{
		{user: "alice", password: "secret", salt: []byte{1, 2, 3, 4}},
		{user: "用户", password: "密码 with spaces", salt: []byte{0, 0, 0, 0}},
		{user: "", password: "", salt: []byte{255, 254, 253, 252}},
	}
	for _, tc := range cases {
		got := SM3PasswordResponse(tc.user, tc.password, tc.salt)
		want := expectedSM3(tc.user, tc.password, tc.salt)
		if got != want {
			t.Errorf("SM3PasswordResponse(%q,%q) = %s, want %s", tc.user, tc.password, got, want)
		}
		if !strings.HasPrefix(got, "sm3") || len(got) != 3+2*sm3.Size {
			t.Errorf("response = %q, want sm3 prefix plus %d hex characters", got, 2*sm3.Size)
		}
	}
}

func TestSM3PasswordResponseDependsOnSalt(t *testing.T) {
	first := SM3PasswordResponse("alice", "secret", []byte{1, 2, 3, 4})
	second := SM3PasswordResponse("alice", "secret", []byte{4, 3, 2, 1})
	if first == second {
		t.Error("response did not change with the salt")
	}
}
