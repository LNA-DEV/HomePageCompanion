package metatoken

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// keyEnv names the key that encrypts stored tokens: 32 bytes, base64
// (openssl rand -base64 32). Without it refresh stays off (D5b).
const keyEnv = "TOKEN_ENCRYPTION_KEY"

// sealer is the AES-256-GCM AEAD for stored tokens. nil while refresh is
// disabled, and disabledReason says why.
var (
	sealer         cipher.AEAD
	disabledReason = keyEnv + " is not set"
)

// setKey installs the key from its base64 form. An empty value disables
// refresh without an error; a malformed one disables it with one.
func setKey(b64 string) error {
	sealer = nil
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		disabledReason = keyEnv + " is not set"
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		disabledReason = keyEnv + " is not valid base64"
		return errors.New(disabledReason)
	}
	if len(key) != 32 {
		disabledReason = fmt.Sprintf("%s must be 32 bytes, has %d", keyEnv, len(key))
		return errors.New(disabledReason)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		disabledReason = keyEnv + ": " + err.Error()
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		disabledReason = keyEnv + ": " + err.Error()
		return err
	}
	sealer = gcm
	disabledReason = ""
	return nil
}

// seal encrypts token for target as base64(nonce ‖ ciphertext). The target
// name is the additional data, so a value copied into another row does not
// decrypt there.
func seal(target, token string) (string, error) {
	nonce := make([]byte, sealer.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := sealer.Seal(nonce, nonce, []byte(token), []byte(target))
	return base64.StdEncoding.EncodeToString(out), nil
}

// unseal reverses seal. It fails for another key, another target, or a value
// that was tampered with.
func unseal(target, stored string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", err
	}
	n := sealer.NonceSize()
	if len(raw) < n {
		return "", errors.New("stored token too short")
	}
	plain, err := sealer.Open(nil, raw[:n], raw[n:], []byte(target))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
