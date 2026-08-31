package server

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed frontend
var frontendFS embed.FS

func readEmbedded(path string) []byte {
	b, err := fs.ReadFile(frontendFS, path)
	if err != nil {
		panic("quickdrop: missing embedded asset " + path + ": " + err.Error())
	}
	return b
}

var (
	uploadHTML  = string(readEmbedded("frontend/upload.html"))
	uploadCSS   = readEmbedded("frontend/upload.css")
	uploadJS    = readEmbedded("frontend/upload.js")
	controlHTML = string(readEmbedded("frontend/control.html"))
)

// phonePageHTML returns the phone-facing page with the current session's token
// baked in as a small inline config that upload.js reads.
func phonePageHTML(token string) string {
	return strings.ReplaceAll(uploadHTML, "{{TOKEN}}", token)
}