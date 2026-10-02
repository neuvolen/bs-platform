package content

import (
	_ "embed"
	"regexp"
)

// The Apps Script code of the club's sheet, latest version. The sheet asks
// for it every hour (GET /api/v1/script/latest) and installs it itself, so
// nobody pastes code by hand. The bot token is not here: the file has
// "__BS_BOT_TOKEN__" in its place and the script puts its own token back.
//
//go:embed script/Code.js
var scriptCode string

//go:embed script/appsscript.json
var scriptManifest string

// ScriptTokenMark stands in the embedded code where the bot token was.
const ScriptTokenMark = "__BS_BOT_TOKEN__"

// ScriptFile is one file of an Apps Script project, as the Apps Script API
// has it (type SERVER_JS or JSON; the manifest is "appsscript").
type ScriptFile struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source"`
}

var scriptVersionRe = regexp.MustCompile(`var BS_VERSION = "([^"]+)"`)

// ScriptVersion: BS_VERSION of the embedded code.
func ScriptVersion() string {
	m := scriptVersionRe.FindStringSubmatch(scriptCode)
	if m == nil {
		return ""
	}
	return m[1]
}

// ScriptFiles: the code and the manifest, ready for projects.updateContent.
func ScriptFiles() []ScriptFile {
	return []ScriptFile{
		{Name: "Code", Type: "SERVER_JS", Source: scriptCode},
		{Name: "appsscript", Type: "JSON", Source: scriptManifest},
	}
}
