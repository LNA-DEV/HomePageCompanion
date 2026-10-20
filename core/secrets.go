package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// secretRow is one secret, encrypted with AES-256-GCM under
// COMPANION_SECRET_KEY. The ciphertext is bound to module and name, so a
// value copied to another row does not decrypt.
type secretRow struct {
	ID        uint   `gorm:"primaryKey"`
	Module    string `gorm:"uniqueIndex:idx_secret"`
	Name      string `gorm:"uniqueIndex:idx_secret"`
	Value     string // base64(nonce ‖ ciphertext)
	UpdatedAt time.Time
}

func (secretRow) TableName() string { return "core_secrets" }

// SecretKeyEnv holds the key: 32 bytes, base64 (`openssl rand -base64 32`).
const SecretKeyEnv = "COMPANION_SECRET_KEY"

// secretKeyFile is where the core keeps a generated key when the
// environment has none, so a fresh installation works; the operator is
// told to move it into .env.
const secretKeyFile = "secret.key"

// ErrNoSecretStore is returned without a database.
var ErrNoSecretStore = errors.New("secret store unavailable")

type secretStore struct {
	c    *Core
	once sync.Once
	aead cipher.AEAD
	err  error
}

func newSecretStore(c *Core) *secretStore { return &secretStore{c: c} }

// parseKey decodes a base64 key of exactly 32 bytes.
func parseKey(b64 string) (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("%s is not base64: %w", SecretKeyEnv, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes, is %d", SecretKeyEnv, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *secretStore) cipher() (cipher.AEAD, error) {
	s.once.Do(func() {
		if k := os.Getenv(SecretKeyEnv); k != "" {
			s.aead, s.err = parseKey(k)
			return
		}
		path := filepath.Join(s.c.opts.DataDir, secretKeyFile)
		if b, err := os.ReadFile(path); err == nil {
			s.aead, s.err = parseKey(string(b))
			return
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			s.err = err
			return
		}
		enc := base64.StdEncoding.EncodeToString(key)
		if err := os.WriteFile(path, []byte(enc+"\n"), 0o600); err != nil {
			s.err = err
			return
		}
		s.c.log.Printf("secrets: %s is not set; generated a key in %s. Move it into .env as %s=…", SecretKeyEnv, path, SecretKeyEnv)
		s.aead, s.err = parseKey(enc)
	})
	return s.aead, s.err
}

func aad(module, name string) []byte { return []byte(module + "\x00" + name) }

func seal(a cipher.AEAD, module, name, value string) (string, error) {
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := a.Seal(nonce, nonce, []byte(value), aad(module, name))
	return base64.StdEncoding.EncodeToString(out), nil
}

func unseal(a cipher.AEAD, module, name, stored string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", err
	}
	if len(raw) < a.NonceSize() {
		return "", errors.New("secret too short")
	}
	plain, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], aad(module, name))
	if err != nil {
		return "", errors.New("secret does not decrypt with this key")
	}
	return string(plain), nil
}

func (s *secretStore) get(module, name string) (string, bool, error) {
	if s.c.db == nil {
		return "", false, ErrNoSecretStore
	}
	var row secretRow
	if s.c.db.Where("module = ? AND name = ?", module, name).Limit(1).Find(&row).RowsAffected == 0 {
		return "", false, nil
	}
	a, err := s.cipher()
	if err != nil {
		return "", false, err
	}
	v, err := unseal(a, module, name, row.Value)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *secretStore) put(module, name, value string) error {
	if s.c.db == nil {
		return ErrNoSecretStore
	}
	a, err := s.cipher()
	if err != nil {
		return err
	}
	enc, err := seal(a, module, name, value)
	if err != nil {
		return err
	}
	var row secretRow
	if s.c.db.Where("module = ? AND name = ?", module, name).Limit(1).Find(&row).RowsAffected == 0 {
		return s.c.db.Create(&secretRow{Module: module, Name: name, Value: enc}).Error
	}
	return s.c.db.Model(&row).Updates(map[string]any{"value": enc, "updated_at": time.Now()}).Error
}

func (s *secretStore) delete(module, name string) error {
	if s.c.db == nil {
		return ErrNoSecretStore
	}
	return s.c.db.Where("module = ? AND name = ?", module, name).Delete(&secretRow{}).Error
}

// rotate re-encrypts every secret from the old key to the new one, in one
// transaction (hpc secrets rotate).
func (s *secretStore) rotate(oldKey, newKey string) (int, error) {
	from, err := parseKey(oldKey)
	if err != nil {
		return 0, fmt.Errorf("old key: %w", err)
	}
	to, err := parseKey(newKey)
	if err != nil {
		return 0, fmt.Errorf("new key: %w", err)
	}
	var rows []secretRow
	if err := s.c.db.Find(&rows).Error; err != nil {
		return 0, err
	}
	tx := s.c.db.Begin()
	for _, r := range rows {
		v, err := unseal(from, r.Module, r.Name, r.Value)
		if err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("%s/%s: %w", r.Module, r.Name, err)
		}
		enc, err := seal(to, r.Module, r.Name, v)
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		if err := tx.Model(&secretRow{}).Where("id = ?", r.ID).Update("value", enc).Error; err != nil {
			tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, err
	}
	s.once = sync.Once{}
	return len(rows), nil
}

// scoped is one building block's view.
type scoped struct {
	s      *secretStore
	module string
}

func (s *secretStore) scope(module string) scoped { return scoped{s: s, module: module} }

func (s scoped) Get(name string) (string, bool, error) { return s.s.get(s.module, name) }
func (s scoped) Put(name, value string) error          { return s.s.put(s.module, name, value) }
func (s scoped) Delete(name string) error              { return s.s.delete(s.module, name) }
