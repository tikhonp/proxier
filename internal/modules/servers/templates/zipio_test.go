package templates_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/db"
)

func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func read(t *testing.T, b []byte) (map[string][]byte, error) {
	t.Helper()
	return templates.ReadZip(bytes.NewReader(b), int64(len(b)))
}

func TestExportImportRoundTrip(t *testing.T) {
	files := seed.Files()
	v := templates.Version{Number: 3, Files: files, PublishedAt: db.At(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))}
	zipped, err := templates.Export(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := read(t, zipped)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, files) {
		t.Errorf("the imported files differ: %d files in, %d out", len(files), len(got))
	}

	// modes come from the manifest, the order is by path, the time is the publish time
	zr, _ := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	modes := map[string]fs.FileMode{}
	var names []string
	for _, f := range zr.File {
		modes[f.Name] = f.Mode().Perm()
		names = append(names, f.Name)
		if !f.Modified.Equal(v.PublishedAt.Time) {
			t.Errorf("%s modified %v", f.Name, f.Modified)
		}
	}
	if modes["issue-cert.sh"] != 0o755 || modes[".env"] != 0o600 || modes["compose.yaml"] != 0o644 {
		t.Errorf("modes: %v", modes)
	}
	if names[0] != ".env" || names[len(names)-1] != "xray-config.json" {
		t.Errorf("order: %v", names)
	}
	again, _ := templates.Export(v)
	if !bytes.Equal(zipped, again) {
		t.Error("the same version exported twice differs")
	}
}

func TestZipManifestTooDeep(t *testing.T) {
	_, err := read(t, makeZip(t, map[string]string{"a/b/manifest.yaml": "name: x\n", "a/b/compose.yaml": "services: {}\n"}))
	if !errors.Is(err, templates.ErrManifestNotFound) || err.Error() != "manifest.yaml not found at the root" {
		t.Errorf("two folders deep: %v", err)
	}
	for name, entries := range map[string]map[string]string{
		"two top-level folders": {"a/manifest.yaml": "x", "b/other": "x"},
		"a loose file":          {"a/manifest.yaml": "x", "README.md": "x"},
		"no manifest":           {"compose.yaml": "x"},
		"empty":                 {},
	} {
		if _, err := read(t, makeZip(t, entries)); !errors.Is(err, templates.ErrManifestNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestZipSingleFolderIsStripped(t *testing.T) {
	got, err := read(t, makeZip(t, map[string]string{
		"my-stack/manifest.yaml":     "name: x\n",
		"my-stack/nginx/site.conf":   "server {}\n",
		"__MACOSX/my-stack/._whatev": "junk",
		"my-stack/.DS_Store":         "junk",
		"my-stack/.git/config":       "junk",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{"manifest.yaml": []byte("name: x\n"), "nginx/site.conf": []byte("server {}\n")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestZipIsSafe(t *testing.T) {
	for name, entries := range map[string]map[string]string{
		"parent":    {"manifest.yaml": "x", "../evil": "x"},
		"nested":    {"manifest.yaml": "x", "a/../../evil": "x"},
		"absolute":  {"manifest.yaml": "x", "/etc/passwd": "x"},
		"backslash": {"manifest.yaml": "x", `a\..\evil`: "x"},
		"drive":     {"manifest.yaml": "x", `C:/evil`: "x"},
	} {
		if _, err := read(t, makeZip(t, entries)); !errors.Is(err, templates.ErrUnsafePath) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// a symlink
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("manifest.yaml")
	_, _ = w.Write([]byte("x"))
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(fs.ModeSymlink | 0o777)
	w, _ = zw.CreateHeader(h)
	_, _ = w.Write([]byte("/etc/passwd"))
	_ = zw.Close()
	if _, err := read(t, buf.Bytes()); !errors.Is(err, templates.ErrUnsafePath) {
		t.Errorf("symlink: %v", err)
	}

	// over 10 MiB uncompressed: refused, and without inflating all of it
	big := bytes.Repeat([]byte{'a'}, 4<<20)
	buf.Reset()
	zw = zip.NewWriter(&buf)
	for _, n := range []string{"manifest.yaml", "a", "b"} {
		w, _ := zw.Create(n)
		_, _ = w.Write(big)
	}
	_ = zw.Close()
	if buf.Len() > 1<<20 {
		t.Fatalf("the test zip is %d bytes, it should be small", buf.Len())
	}
	if _, err := read(t, buf.Bytes()); !errors.Is(err, templates.ErrTooLarge) {
		t.Errorf("a zip bomb: %v", err)
	}

	// the same, with sizes in the headers that lie: Go's reader notices the
	// mismatch itself, either way the zip is refused
	if _, err := read(t, lieAboutSize(t, big)); err == nil {
		t.Error("a zip with lying sizes was accepted")
	}

	// too many entries
	many := map[string]string{"manifest.yaml": "x"}
	for i := 0; i < templates.MaxImportFiles; i++ {
		many["f/"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))] = "x"
	}
	if _, err := read(t, makeZip(t, many)); !errors.Is(err, templates.ErrTooManyFiles) {
		t.Errorf("501 entries: %v", err)
	}

	// not a zip
	if _, err := read(t, []byte("hello")); !errors.Is(err, templates.ErrNotZip) {
		t.Errorf("not a zip: %v", err)
	}
}

// lieAboutSize builds a zip of three 4 MiB files whose central directory claims
// 1 byte each, so only counting while reading catches it.
func lieAboutSize(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"manifest.yaml", "a", "b"} {
		w, _ := zw.Create(n)
		_, _ = w.Write(body)
	}
	_ = zw.Close()
	b := buf.Bytes()
	// central directory entries start with PK\x01\x02; the uncompressed size is at offset 24
	sig := []byte{'P', 'K', 1, 2}
	for i := 0; i+28 < len(b); i++ {
		if bytes.Equal(b[i:i+4], sig) {
			b[i+24], b[i+25], b[i+26], b[i+27] = 1, 0, 0, 0
		}
	}
	return b
}
