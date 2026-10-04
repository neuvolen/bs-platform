package ai

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
)

// R34a: the Claude key the owner pastes in the platform settings. It lives
// in the database sealed with AES-256-GCM (the key derived from
// AI_KEYS_SECRET, or JWT_SECRET when that is not set) and in memory here.
// Neither the page nor the logs ever get it: the settings show its last
// four characters only.

// KeyBox holds the key saved in the settings. FromEnv clients share
// SharedKeys, so the platform and the sheet bridge see one key.
type KeyBox struct {
	mu  sync.RWMutex
	key string
}

// SharedKeys: the box FromEnv gives every client.
var SharedKeys = &KeyBox{}

func (k *KeyBox) Get() string {
	if k == nil {
		return ""
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.key
}

func (k *KeyBox) Set(key string) {
	k.mu.Lock()
	k.key = strings.TrimSpace(key)
	k.mu.Unlock()
}

// Last4: «…AbCd» for the settings, "" when there is no key.
func Last4(key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 8 {
		if key == "" {
			return ""
		}
		return "…"
	}
	return "…" + key[len(key)-4:]
}

// ValidKeyShape: a pasted Anthropic key: one line of printable ASCII, no
// spaces, 20 to 300 characters (sk-ant-… today; the prefix is not required
// so a new format still works).
func ValidKeyShape(key string) bool {
	if len(key) < 20 || len(key) > 300 {
		return false
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

const sealPrefix = "v1:"

func sealKey(secret []byte) []byte {
	h := sha256.New()
	h.Write([]byte("bs-platform/ai-key/v1\x00"))
	h.Write(secret)
	return h.Sum(nil)
}

// Seal encrypts plain with a key derived from secret (AES-256-GCM, random nonce).
func Seal(secret []byte, plain string) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("нет секрета для шифрования ключа")
	}
	blk, err := aes.NewCipher(sealKey(secret))
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := g.Seal(nonce, nonce, []byte(plain), []byte("anthropic"))
	return sealPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts what Seal made with the same secret.
func Open(secret []byte, sealed string) (string, error) {
	if !strings.HasPrefix(sealed, sealPrefix) {
		return "", errors.New("ключ сохранён в неизвестном формате")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil {
		return "", err
	}
	blk, err := aes.NewCipher(sealKey(secret))
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", errors.New("ключ повреждён")
	}
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte("anthropic"))
	if err != nil {
		return "", errors.New("ключ не расшифровывается: сменился секрет сервера, сохраните ключ заново")
	}
	return string(plain), nil
}
