// Package auth implements the client-side password authentication exchanges
// used by KingbaseES PG-wire profiles: SCRAM-SHA-256 (RFC 5802/7677) and the
// KingbaseES SM3 challenge. Cryptographic work is bounded and every server
// message is validated before use.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SCRAMMechanism is the SASL mechanism implemented by this package.
const SCRAMMechanism = "SCRAM-SHA-256"

// SCRAM work-factor and message bounds. A server proposing values outside
// these bounds is a protocol error rather than an invitation to burn CPU.
const (
	MinSCRAMIterations = 4096
	MaxSCRAMIterations = 1000000
	maxSCRAMNonceLen   = 2048
	maxSCRAMSaltLen    = 1024
	maxSCRAMMessageLen = 16 << 10
)

// SCRAMClient is a SCRAM-SHA-256 client without channel binding, which is the
// mechanism KingbaseES offers for password authentication.
type SCRAMClient struct {
	username string
	password string
	nonce    string

	clientFirstBare string
	serverSignature []byte
}

// NewSCRAMClient creates a client with a fresh random nonce. KingbaseES
// PG-wire clients send an empty user name because the server already knows it.
func NewSCRAMClient(username, password string) (*SCRAMClient, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("auth: generate SCRAM nonce: %w", err)
	}
	return &SCRAMClient{
		username: username,
		password: password,
		nonce:    base64.RawStdEncoding.EncodeToString(raw),
	}, nil
}

// ClientFirst returns the SASL initial response.
func (c *SCRAMClient) ClientFirst() []byte {
	c.clientFirstBare = "n=" + escapeSCRAMUsername(c.username) + ",r=" + c.nonce
	return []byte("n,," + c.clientFirstBare)
}

// HandleServerFirst validates the server-first-message and returns the
// client-final-message.
func (c *SCRAMClient) HandleServerFirst(serverFirst []byte) ([]byte, error) {
	if c.clientFirstBare == "" {
		return nil, errors.New("auth: SCRAM client-first message was not generated")
	}
	if len(serverFirst) > maxSCRAMMessageLen {
		return nil, errors.New("auth: SCRAM server-first message is too large")
	}
	attributes, err := parseSCRAMAttributes(string(serverFirst))
	if err != nil {
		return nil, err
	}
	serverNonce := attributes["r"]
	saltBase64 := attributes["s"]
	iterationsText := attributes["i"]
	if serverNonce == "" || saltBase64 == "" || iterationsText == "" {
		return nil, errors.New("auth: SCRAM server-first message lacks r, s or i")
	}
	if len(serverNonce) > maxSCRAMNonceLen {
		return nil, errors.New("auth: SCRAM nonce is too long")
	}
	if !strings.HasPrefix(serverNonce, c.nonce) || len(serverNonce) == len(c.nonce) {
		return nil, errors.New("auth: SCRAM server nonce does not extend the client nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(saltBase64)
	if err != nil {
		return nil, fmt.Errorf("auth: SCRAM salt is not valid base64: %w", err)
	}
	if len(salt) == 0 || len(salt) > maxSCRAMSaltLen {
		return nil, errors.New("auth: SCRAM salt length is out of bounds")
	}
	iterations, err := strconv.Atoi(iterationsText)
	if err != nil || iterations < MinSCRAMIterations || iterations > MaxSCRAMIterations {
		return nil, fmt.Errorf("auth: SCRAM iteration count %q is out of bounds", iterationsText)
	}

	saltedPassword, err := pbkdf2.Key(sha256.New, c.password, salt, iterations, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("auth: derive SCRAM salted password: %w", err)
	}
	clientKey := hmacSum(saltedPassword, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	clientFinalWithoutProof := "c=biws,r=" + serverNonce
	authMessage := c.clientFirstBare + "," + string(serverFirst) + "," + clientFinalWithoutProof
	clientSignature := hmacSum(storedKey[:], authMessage)
	clientProof := make([]byte, len(clientKey))
	for i := range clientKey {
		clientProof[i] = clientKey[i] ^ clientSignature[i]
	}
	serverKey := hmacSum(saltedPassword, "Server Key")
	c.serverSignature = hmacSum(serverKey, authMessage)

	clientFinal := clientFinalWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(clientProof)
	return []byte(clientFinal), nil
}

// VerifyServerFinal checks the server-final-message, including the server
// signature, before the connection is trusted.
func (c *SCRAMClient) VerifyServerFinal(serverFinal []byte) error {
	if len(serverFinal) > maxSCRAMMessageLen {
		return errors.New("auth: SCRAM server-final message is too large")
	}
	attributes, err := parseSCRAMAttributes(string(serverFinal))
	if err != nil {
		return err
	}
	if serverError := attributes["e"]; serverError != "" {
		return fmt.Errorf("auth: server rejected SCRAM authentication: %s", serverError)
	}
	signatureBase64 := attributes["v"]
	if signatureBase64 == "" {
		return errors.New("auth: SCRAM server-final message lacks a signature")
	}
	signature, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil {
		return fmt.Errorf("auth: SCRAM server signature is not valid base64: %w", err)
	}
	if len(c.serverSignature) == 0 {
		return errors.New("auth: no SCRAM server signature was expected")
	}
	if subtle.ConstantTimeCompare(signature, c.serverSignature) != 1 {
		return errors.New("auth: SCRAM server signature mismatch")
	}
	return nil
}

func hmacSum(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return mac.Sum(nil)
}

func escapeSCRAMUsername(username string) string {
	return strings.NewReplacer("=", "=3D", ",", "=2C").Replace(username)
}

// parseSCRAMAttributes parses comma-separated one-letter attributes. Unknown
// attributes are ignored; duplicates and malformed entries are rejected.
func parseSCRAMAttributes(message string) (map[string]string, error) {
	attributes := make(map[string]string, 4)
	for _, part := range strings.Split(message, ",") {
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("auth: malformed SCRAM attribute %q", part)
		}
		if _, duplicate := attributes[name]; duplicate {
			return nil, fmt.Errorf("auth: duplicate SCRAM attribute %q", name)
		}
		if name == "m" {
			return nil, errors.New("auth: SCRAM mandatory extension is not supported")
		}
		attributes[name] = value
	}
	return attributes, nil
}
