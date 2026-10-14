// Package web es la interfaz web de eirl-encoderd: HTML + CSS + JS sin build ni dependencias,
// embebidos en el binario. Todo se sirve desde el propio aparato (CSP default-src 'self'): funciona
// en una LAN sin internet.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
)

//go:embed static
var files embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(files, "static")
	return sub
}

// Hash: huella de la interfaz embebida. La página la compara al reconectar para recargarse tras
// una actualización (la versión no basta: dos builds "-dirty" del mismo commit dan la misma).
func Hash() string {
	h := sha256.New()
	fs.WalkDir(files, "static", func(p string, d fs.DirEntry, err error) error { // orden léxico
		if err == nil && !d.IsDir() {
			b, _ := files.ReadFile(p)
			h.Write([]byte(p + "\x00"))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)[:6])
}
