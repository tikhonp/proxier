package ui_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/ui"
)

func appCSS(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(ui.StaticFS, "css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Zoomed in (or in a narrow window) a table keeps its columns: it scrolls
// sideways in its own box, and a cell keeps to one line ending in "…".
func TestTablesScrollInsteadOfSqueezing(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		`.tbl { overflow-x: auto;`,
		`.tbl .row, .tbl .colhead { min-width: min-content; }`,
		`text-overflow: ellipsis; white-space: nowrap; }`,
		`.tbl .row .wrap { min-width: 0; white-space: normal; overflow-wrap: break-word; }`,
		`.filter-form { display: flex; flex-wrap: wrap;`,
		`.bulk { display: flex; flex-wrap: wrap;`,
		`.crumbs { display: flex; flex-wrap: wrap;`,
		`.tabs { display: flex; gap: 0; border-bottom: 1px solid var(--overlay); padding: 0 16px; overflow-x: auto; }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// A column that may squeeze to nothing is what made labels collide: every
	// table's grid gives each column a real minimum.
	grid := regexp.MustCompile(`\.colhead\.([a-z-]+)[^{]*\{ grid-template-columns: ([^;]+);`)
	found := grid.FindAllStringSubmatch(css, -1)
	if len(found) < 15 {
		t.Fatalf("only %d table grids found", len(found))
	}
	for _, m := range found {
		if strings.Contains(m[2], "minmax(0,") {
			t.Errorf("table %s has a column without a minimum: %s", m[1], m[2])
		}
	}
	// Side-by-side page columns stack below 1100 px instead of squeezing their tables.
	if !regexp.MustCompile(`(?s)@media \(max-width: 1100px\) \{[^@]*\.tgrid, \.subcols, [^}]*grid-template-columns: minmax\(0, 1fr\)`).MatchString(css) {
		t.Error("the two-column layouts do not stack under 1100px")
	}
	// and the rule comes after the layouts it overrides
	if strings.LastIndex(css, "@media (max-width: 1100px)") < strings.Index(css, ".subcols {") {
		t.Error("the 1100px rule comes before the layouts it overrides")
	}
}

// Every table in the templates (a .colhead and its rows) sits in a .tbl box,
// so the stylesheet's scrolling and minimum widths apply to it.
func TestEveryTableIsInATableBox(t *testing.T) {
	root := filepath.Join("..", "..")
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".templ") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if !strings.Contains(l, `class="colhead `) {
				continue
			}
			n++
			if !inTableBox(lines, i) {
				t.Errorf("%s:%d: a table outside a .tbl box", p, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 15 {
		t.Fatalf("only %d tables found", n)
	}
}

var tblClass = regexp.MustCompile(`class="(?:[^"]* )?tbl(?: [^"]*)?"`)

// inTableBox reports whether an element opened with class "tbl" on a line less
// indented than line i is still open there.
func inTableBox(lines []string, i int) bool {
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, "\t")) }
	for j := i - 1; j >= 0; j-- {
		l := lines[j]
		if strings.HasPrefix(l, "templ ") {
			return false
		}
		if tblClass.MatchString(l) && indent(l) < indent(lines[i]) {
			for k := j + 1; k < i; k++ {
				if indent(lines[k]) == indent(l) && strings.TrimSpace(lines[k]) == "</div>" {
					return false
				}
			}
			return true
		}
	}
	return false
}
