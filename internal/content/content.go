// Package content holds the club's own materials shipped with the server:
// the 99 checklists for clients. On start the server puts them into the
// platform storage (club document bs_checklists) when the shipped version is newer.
package content

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed checklists.json
var Checklists []byte

// ChecklistsVersion changes whenever the shipped file changes.
func ChecklistsVersion() string {
	s := sha256.Sum256(Checklists)
	return hex.EncodeToString(s[:6])
}
