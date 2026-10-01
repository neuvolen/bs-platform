// Package bsfiles carries the club's books and stickers inside the server,
// encrypted. The key is derived from the bot token, which only the server
// (and its owners) hold, so the files in the public repository are unreadable.
// At start the server decrypts them and puts them into the platform's storage.
package bsfiles

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
)

//go:embed data/*.enc
var data embed.FS

// Item is one embedded file.
type Item struct {
	Kind string `json:"kind"` // book | sticker
	Name string `json:"name"`
	Mime string `json:"mime"`
	File string `json:"file"`
}

func open(botToken, name string) ([]byte, error) {
	raw, err := data.ReadFile("data/" + name)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12+16 {
		return nil, errors.New("bsfiles: short file")
	}
	key := sha256.Sum256([]byte("bs-files-v1|" + botToken))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, raw[:12], raw[12:], nil)
}

// Manifest lists the files; it fails if the token is not the one they were made for.
func Manifest(botToken string) ([]Item, error) {
	b, err := open(botToken, "manifest.enc")
	if err != nil {
		return nil, err
	}
	var out []Item
	return out, json.Unmarshal(b, &out)
}

// Read returns the decrypted content of an item.
func Read(botToken string, it Item) ([]byte, error) { return open(botToken, it.File) }
