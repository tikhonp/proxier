package templates

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

// Import limits.
const (
	MaxUpload         = 12 << 20 // bytes of an uploaded zip
	MaxImportFiles    = 500      // entries of a zip, files of a git path
	MaxImportBytes    = 10 << 20 // uncompressed, in total
	maxImportBytesMsg = "10 MiB"
)

// Import refusals. Pages translate them; the text is for logs and tests.
var (
	ErrNotZip           = errors.New("not a zip file")
	ErrManifestNotFound = errors.New("manifest.yaml not found at the root")
	ErrUnsafePath       = errors.New("the archive has an unsafe path or a symlink")
	ErrTooLarge         = errors.New("the files are over " + maxImportBytesMsg + " uncompressed")
	ErrTooManyFiles     = errors.New("too many files")
)

// Export writes a version as a zip: manifest.yaml and the files at the root,
// in path order, with the modes the manifest gives and the publish time as
// every entry's timestamp, so the same version always exports the same
// bytes. Importing it again gives the same files.
func Export(v Version) ([]byte, error) {
	modes := map[string]fs.FileMode{}
	if m, _ := manifest.Parse(v.Files[manifest.Name]); m != nil {
		for _, f := range m.Files {
			if f.Mode != 0 {
				modes[f.Path] = f.Mode
			}
		}
	}
	paths := make([]string, 0, len(v.Files))
	for p := range v.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range paths {
		h := &zip.FileHeader{Name: p, Method: zip.Deflate, Modified: v.PublishedAt.Time}
		mode := fs.FileMode(0o644)
		if m, ok := modes[p]; ok {
			mode = m
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(v.Files[p]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// skipped entries are what archivers add on their own.
func skipped(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		switch seg {
		case "__MACOSX", ".DS_Store", ".git":
			return true
		}
	}
	return false
}

// cleanName checks an archive path and returns it without a leading "./".
func cleanName(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
		return "", ErrUnsafePath
	}
	name = strings.TrimPrefix(name, "./")
	for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if seg == ".." || seg == "" || seg == "." {
			return "", ErrUnsafePath
		}
	}
	return strings.TrimSuffix(name, "/"), nil
}

// ReadZip reads a template zip into a file set. It refuses paths that leave
// the tree, symlinks, more than MaxImportFiles entries and more than
// MaxImportBytes uncompressed (counted while reading, so a zip bomb is
// stopped early). manifest.yaml must be at the root, or inside the one
// top-level folder every file lives in; that folder is stripped.
func ReadZip(r io.ReaderAt, size int64) (map[string][]byte, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, ErrNotZip
	}
	if len(zr.File) > MaxImportFiles {
		return nil, ErrTooManyFiles
	}
	var declared uint64
	for _, f := range zr.File {
		declared += f.UncompressedSize64
		if declared > MaxImportBytes {
			return nil, ErrTooLarge
		}
	}
	files := map[string][]byte{}
	left := int64(MaxImportBytes)
	for _, f := range zr.File {
		if f.Mode()&fs.ModeSymlink != 0 {
			return nil, ErrUnsafePath
		}
		name, err := cleanName(f.Name)
		if err != nil {
			return nil, err
		}
		if skipped(name) || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, ErrNotZip
		}
		b, err := io.ReadAll(io.LimitReader(rc, left+1))
		_ = rc.Close()
		if err != nil {
			return nil, ErrNotZip
		}
		if left -= int64(len(b)); left < 0 {
			return nil, ErrTooLarge
		}
		files[name] = b
	}
	return StripRoot(files)
}

// StripRoot applies the manifest rule to a file set: manifest.yaml at the
// root, or every file inside one top-level folder that holds it (which is
// removed from the paths). Anything else is ErrManifestNotFound.
func StripRoot(files map[string][]byte) (map[string][]byte, error) {
	if _, ok := files[manifest.Name]; ok {
		return files, nil
	}
	top := ""
	for p := range files {
		first, rest, found := strings.Cut(p, "/")
		if !found || rest == "" {
			return nil, ErrManifestNotFound // a loose file beside the folder
		}
		if top != "" && first != top {
			return nil, ErrManifestNotFound
		}
		top = first
	}
	if top == "" {
		return nil, ErrManifestNotFound
	}
	if _, ok := files[path.Join(top, manifest.Name)]; !ok {
		return nil, ErrManifestNotFound
	}
	out := make(map[string][]byte, len(files))
	for p, c := range files {
		out[strings.TrimPrefix(p, top+"/")] = c
	}
	return out, nil
}

// checkImportSize applies the count and size limits to a file set that did not
// come through a zip.
func checkImportSize(files map[string][]byte) error {
	if len(files) > MaxImportFiles {
		return ErrTooManyFiles
	}
	total := 0
	for _, c := range files {
		total += len(c)
	}
	if total > MaxImportBytes {
		return ErrTooLarge
	}
	return nil
}
