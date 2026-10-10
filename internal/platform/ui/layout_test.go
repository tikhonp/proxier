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

// A side panel beside a long form or list (New server's "Will be created", the
// generate form's summary, a script's parameters, a catalog preview) stays in
// view while the page scrolls: its column runs the row's height, so the divider
// does too, and its content sticks to the top of the window. The header scrolls
// away, so an offset of its height left a gap above the panel.
func TestSidePanelsStickToTheTop(t *testing.T) {
	css := appCSS(t)
	stick := `.stick { position: sticky; top: 0; max-height: calc(100vh - var(--keyline-h)); overflow-y: auto; }`
	if !strings.Contains(css, stick) {
		t.Errorf("app.css lacks %q", stick)
	}
	if regexp.MustCompile(`position: sticky; top: var\(--header-h\)`).MatchString(css) {
		t.Error("something sticks below the header, which is not fixed: a gap above it")
	}
	// the panel's column is as tall as the row: no align-items: start on the grid
	for _, sel := range []string{".newcols", ".search-cols", ".rs-editor"} {
		m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sel) + ` \{[^}]*\}`).FindString(css)
		if m == "" {
			t.Errorf("no %s rule", sel)
		} else if strings.Contains(m, "align-items: start") {
			t.Errorf("%s keeps its side panel only as tall as its content: %s", sel, m)
		}
	}
	for _, want := range []string{`.newsum { border-left: 1px solid var(--overlay); }`, `.preview { border-left: 1px solid var(--overlay); }`, `.rs-panel { min-width: 0; border-left: 1px solid var(--overlay); }`} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// stacked under 1100 px, a panel is part of the page again
	i, j := strings.Index(css, stick), strings.LastIndex(css, "@media (max-width: 1100px)")
	if i < 0 || j < i || !strings.Contains(css[j:], `.stick { position: static; max-height: none; overflow: visible; }`) {
		t.Error("the panels still stick once the columns stack under 1100px")
	}

	// every side panel in the templates holds its content in a .stick box
	panel := regexp.MustCompile(`<aside class="newsum"|class="rs-panel"|class="area drawer-panel`)
	n := 0
	err := filepath.WalkDir(filepath.Join("..", ".."), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".templ") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		lines := strings.Split(string(b), "\n")
		for k, l := range lines {
			if !panel.MatchString(l) {
				continue
			}
			n++
			if !hasStick(lines, k) {
				t.Errorf("%s:%d: a side panel without a .stick box", p, k+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 4 {
		t.Fatalf("only %d side panels found", n)
	}
}

// hasStick reports whether the element opened on line i is itself the .stick
// box or holds one as its first child.
func hasStick(lines []string, i int) bool {
	stick := regexp.MustCompile(`class="(?:[^"]* )?stick(?: [^"]*)?"`)
	for k := i; k < len(lines)-1; k++ {
		if stick.MatchString(lines[k]) {
			return true
		}
		if strings.HasSuffix(strings.TrimSpace(lines[k]), ">") { // the end of the opening tag
			return stick.MatchString(lines[k+1])
		}
	}
	return false
}
