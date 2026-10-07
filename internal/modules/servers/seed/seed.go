// Package seed holds the template every Proxier starts with: "VLESS XHTTP
// behind nginx", converted from servers-templates/proxy
// (docs/integrations/vless-xhttp.md). The module publishes it as version 1 on
// the first start; from then on it is an ordinary template.
package seed

import (
	"embed"
	"io/fs"
)

// all: keeps .env, which a plain embed would skip as a dot file.
//
//go:embed all:files
var embedded embed.FS

const (
	Slug        = "vless-xhttp"
	Name        = "VLESS XHTTP behind nginx"
	Description = "VLESS over XHTTP, hidden behind nginx with a Let's Encrypt certificate and a decoy site."
	Notes       = "Converted from servers-templates/proxy"
)

// Files returns the seed's tree: path → content, manifest.yaml included.
func Files() map[string][]byte {
	out := map[string][]byte{}
	err := fs.WalkDir(embedded, "files", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := embedded.ReadFile(p)
		if err != nil {
			return err
		}
		out[p[len("files/"):]] = b
		return nil
	})
	if err != nil {
		panic("seed: " + err.Error()) // the embedded tree cannot fail to read
	}
	return out
}
