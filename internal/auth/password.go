package auth

import (
	"crypto/hmac"
	"encoding/hex"
	"hash"

	"github.com/cocosip/go-kingbase/internal/sm3"
)

// SM3PasswordResponse computes the evidenced KingbaseES SM3 challenge
// response:
//
//	"sm3" + hex(HMAC-SM3(key=salt, message=hex(HMAC-SM3(key=user, message=password))))
//
// The composition is source-evidenced (V009R004C019B0004 conn.go auth case 13)
// and replayed offline; live acceptance remains V5.
func SM3PasswordResponse(user, password string, salt []byte) string {
	inner := hmacHex(sm3.New, []byte(user), []byte(password))
	outer := hmacHex(sm3.New, salt, []byte(inner))
	return "sm3" + outer
}

func hmacHex(newHash func() hash.Hash, key, message []byte) string {
	mac := hmac.New(newHash, key)
	_, _ = mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}
