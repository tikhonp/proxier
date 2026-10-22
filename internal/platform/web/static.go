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
	".woff2": "font/woff2", ".svg": "image/svg+xml", ".ico": "image/x-icon", ".png": "image/png", ".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
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
		return serveFile(c, fsys, name, "public, max-age=31536000, immutable")
	}
}

// RootIcon serves one static file at a fixed root path (/favicon.ico,
// /apple-touch-icon.png): browsers and iOS ask for these without reading the
// page's links, for a bookmark or a home-screen icon, often signed out. The
// URL never changes with a deploy, so it is cached for a day only.
func RootIcon(fsys fs.FS, name string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		return serveFile(c, fsys, name, "public, max-age=86400")
	}
}

func serveFile(c *echo.Context, fsys fs.FS, name, cacheControl string) error {
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
	c.Response().Header().Set("Cache-Control", cacheControl)
	return c.Blob(http.StatusOK, ct, b)
}
