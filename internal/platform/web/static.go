package web

import (
	"io/fs"
	"mime"
	"net/http"
	"path"

	"github.com/labstack/echo/v5"
)

var contentTypes = map[string]string{
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".woff2": "font/woff2", ".svg": "image/svg+xml", ".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
}

// Static serves /static/<hash>/<path> from fsys. Only the current hash
// exists, so everything is immutable.
func Static(fsys fs.FS, hash string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if c.Param("hash") != hash {
			return echo.ErrNotFound
		}
		name := path.Clean(c.Param("*"))
		if name == "." || name == "/" || path.IsAbs(name) || !fs.ValidPath(name) {
			return echo.ErrNotFound
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return echo.ErrNotFound
		}
		ct, ok := contentTypes[path.Ext(name)]
		if !ok {
			if ct = mime.TypeByExtension(path.Ext(name)); ct == "" {
				ct = "application/octet-stream"
			}
		}
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return c.Blob(http.StatusOK, ct, b)
	}
}
