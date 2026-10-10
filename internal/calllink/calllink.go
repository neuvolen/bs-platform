// Package calllink: R75, the platform's own call link of a meeting.
//
// Every online meeting of a resident (or a lead's разбор) is held on the
// platform: https://app.bxclub.kz/call/<id>. The id is the same for all the
// meetings of one person (one link to remember, it never goes stale), and is
// an HMAC of the person's name with the server's secret: it cannot be guessed
// from the name, and the server finds the person back by computing it for the
// names it knows (boards, residents, the schedule).
//
// Google Meet stays only a fallback: the meeting keeps its Meet link in the
// data, the call shows it as «Meet» inside the call.
package calllink

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"os"
	"regexp"
	"strings"
)

// DefaultBase: where the platform lives.
const DefaultBase = "https://app.bxclub.kz"

// Path: the link's path before the id.
const Path = "/call/"

// IDRe: what an id looks like (16 base32 letters, lower case).
var IDRe = regexp.MustCompile(`^[a-z2-7]{16}$`)

// Norm: "Пётр  Иванов", "петр иванов" and "Пётр Иванов" are one person.
func Norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	return strings.Join(strings.Fields(s), " ")
}

func secret() []byte {
	if s := os.Getenv("JWT_SECRET"); s != "" {
		return []byte("bs-call-r75|" + s)
	}
	return []byte("bs-call-r75")
}

// ID: the person's call id ("" for an empty name).
func ID(name string) string {
	n := Norm(name)
	if n == "" {
		return ""
	}
	m := hmac.New(sha256.New, secret())
	m.Write([]byte(n))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(m.Sum(nil))[:16])
}

// Base: the platform's address (BS_CALL_BASE, then PUBLIC_URL, then app.bxclub.kz).
func Base() string {
	for _, k := range []string{"BS_CALL_BASE", "PUBLIC_URL"} {
		if v := strings.TrimRight(strings.TrimSpace(os.Getenv(k)), "/"); strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://localhost") || strings.HasPrefix(v, "http://127.") {
			return v
		}
	}
	return DefaultBase
}

// URL: the person's call link ("" for an empty name).
func URL(name string) string {
	id := ID(name)
	if id == "" {
		return ""
	}
	return Base() + Path + id
}

// IsOnline: the meeting is held online (a link in it, or the online format).
func IsOnline(link, format string) bool {
	f := strings.ToLower(strings.TrimSpace(format))
	return strings.Contains(link, "http") || f == "online" || f == "онлайн"
}
