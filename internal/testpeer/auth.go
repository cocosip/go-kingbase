package testpeer

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/cocosip/go-kingbase/internal/sm3"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// SM3SaltSize is the challenge salt length used by the SM3 exchange.
const SM3SaltSize = 4

const scramMechanism = "SCRAM-SHA-256"

// SCRAMOptions tunes the scripted SCRAM server for negative tests.
type SCRAMOptions struct {
	// Iterations overrides the advertised work factor.
	Iterations int
	// CorruptServerSignature sends a bad server signature.
	CorruptServerSignature bool
}

// AuthenticateSM3 sends the KingbaseES SM3 challenge and verifies the client
// response. It is an independent server-side implementation of the documented
// double-HMAC composition.
func (c *Conn) AuthenticateSM3(user, password string) error {
	salt := make([]byte, SM3SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("testpeer: generate SM3 salt: %w", err)
	}
	var saltArray [SM3SaltSize]byte
	copy(saltArray[:], salt)
	if err := c.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationSM3(saltArray)); err != nil {
		return err
	}
	typ, payload, err := c.ReadMessage()
	if err != nil {
		return err
	}
	if typ != wire.MsgPassword {
		return fmt.Errorf("testpeer: expected PasswordMessage, got %q", typ)
	}
	received, err := wire.DecodePasswordMessage(payload)
	if err != nil {
		return err
	}
	want := sm3PasswordResponse(user, password, salt)
	if subtle.ConstantTimeCompare([]byte(received), []byte(want)) != 1 {
		return fmt.Errorf("testpeer: SM3 response mismatch")
	}
	return nil
}

func sm3PasswordResponse(user, password string, salt []byte) string {
	innerMAC := hmac.New(sm3.New, []byte(user))
	_, _ = innerMAC.Write([]byte(password))
	inner := hex.EncodeToString(innerMAC.Sum(nil))
	outerMAC := hmac.New(sm3.New, salt)
	_, _ = outerMAC.Write([]byte(inner))
	return "sm3" + hex.EncodeToString(outerMAC.Sum(nil))
}

// AuthenticateSCRAM plays the server side of SCRAM-SHA-256, verifying the
// client proof derived from password. It returns the client-final message for
// optional round-trip assertions.
func (c *Conn) AuthenticateSCRAM(password string, options SCRAMOptions) (clientFinal string, err error) {
	iterations := options.Iterations
	if iterations == 0 {
		iterations = 4096
	}
	if err := c.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationSASL(scramMechanism)); err != nil {
		return "", err
	}

	typ, payload, err := c.ReadMessage()
	if err != nil {
		return "", err
	}
	if typ != wire.MsgPassword {
		return "", fmt.Errorf("testpeer: expected SASLInitialResponse, got %q", typ)
	}
	mechanism, initial, err := wire.DecodeSASLInitialResponse(payload)
	if err != nil {
		return "", err
	}
	if mechanism != scramMechanism {
		return "", fmt.Errorf("testpeer: mechanism = %q, want %q", mechanism, scramMechanism)
	}
	clientFirst := string(initial)
	const gs2Header = "n,,"
	if !strings.HasPrefix(clientFirst, gs2Header) {
		return "", fmt.Errorf("testpeer: unsupported SCRAM GS2 header in %q", clientFirst)
	}
	clientFirstBare := clientFirst[len(gs2Header):]
	clientNonce, err := scramAttribute(clientFirstBare, "r")
	if err != nil || clientNonce == "" {
		return "", fmt.Errorf("testpeer: client nonce missing: %v", err)
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("testpeer: generate SCRAM salt: %w", err)
	}
	nonceSuffix := make([]byte, 18)
	if _, err := rand.Read(nonceSuffix); err != nil {
		return "", fmt.Errorf("testpeer: generate SCRAM nonce: %w", err)
	}
	serverNonce := clientNonce + base64.RawStdEncoding.EncodeToString(nonceSuffix)
	serverFirst := "r=" + serverNonce +
		",s=" + base64.StdEncoding.EncodeToString(salt) +
		",i=" + strconv.Itoa(iterations)
	if err := c.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationSASLContinue([]byte(serverFirst))); err != nil {
		return "", err
	}

	typ, payload, err = c.ReadMessage()
	if err != nil {
		return "", err
	}
	if typ != wire.MsgPassword {
		return "", fmt.Errorf("testpeer: expected SASLResponse, got %q", typ)
	}
	clientFinal = string(payload)
	nonce, err := scramAttribute(clientFinal, "r")
	if err != nil || nonce != serverNonce {
		return clientFinal, fmt.Errorf("testpeer: client nonce %q does not match server nonce", nonce)
	}
	channelBinding, err := scramAttribute(clientFinal, "c")
	if err != nil || channelBinding != "biws" {
		return clientFinal, fmt.Errorf("testpeer: unsupported channel binding %q", channelBinding)
	}
	proof, err := scramAttribute(clientFinal, "p")
	if err != nil {
		return clientFinal, err
	}
	proofIndex := strings.LastIndex(clientFinal, ",p=")
	if proofIndex < 0 {
		return clientFinal, fmt.Errorf("testpeer: client-final message lacks a proof")
	}
	clientFinalWithoutProof := clientFinal[:proofIndex]

	saltedPassword, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return clientFinal, fmt.Errorf("testpeer: derive salted password: %w", err)
	}
	clientKey := hmacSHA256(saltedPassword, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	authMessage := clientFirstBare + "," + serverFirst + "," + clientFinalWithoutProof
	clientSignature := hmacSHA256(storedKey[:], authMessage)
	expectedProof := make([]byte, len(clientKey))
	for i := range clientKey {
		expectedProof[i] = clientKey[i] ^ clientSignature[i]
	}
	receivedProof, err := base64.StdEncoding.DecodeString(proof)
	if err != nil {
		return clientFinal, fmt.Errorf("testpeer: proof is not base64: %w", err)
	}
	if subtle.ConstantTimeCompare(expectedProof, receivedProof) != 1 {
		return clientFinal, errInvalidProof
	}

	serverKey := hmacSHA256(saltedPassword, "Server Key")
	serverSignature := hmacSHA256(serverKey, authMessage)
	if options.CorruptServerSignature {
		serverSignature[0] ^= 0xff
	}
	serverFinal := "v=" + base64.StdEncoding.EncodeToString(serverSignature)
	if err := c.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationSASLFinal([]byte(serverFinal))); err != nil {
		return clientFinal, err
	}
	return clientFinal, nil
}

// errInvalidProof marks a client proof mismatch so handlers can answer with an
// authentication error.
var errInvalidProof = fmt.Errorf("testpeer: SCRAM client proof mismatch")

// IsInvalidProof reports whether err is a client proof mismatch.
func IsInvalidProof(err error) bool {
	return err == errInvalidProof
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return mac.Sum(nil)
}

// scramAttribute returns the value of a single-letter attribute.
func scramAttribute(message, name string) (string, error) {
	value := ""
	found := false
	for _, part := range strings.Split(message, ",") {
		key, attrValue, ok := strings.Cut(part, "=")
		if !ok || key != name {
			continue
		}
		if found {
			return "", fmt.Errorf("testpeer: duplicate SCRAM attribute %q", name)
		}
		value, found = attrValue, true
	}
	return value, nil
}
