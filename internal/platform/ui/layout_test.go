package ui_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/i18n"
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
	if !regexp.MustCompile(`(?s)@media \(max-width: 1100px\) \{[^@]*\.subcols, \.newcols, [^}]*grid-template-columns: minmax\(0, 1fr\)`).MatchString(css) {
		t.Error("the two-column layouts do not stack under 1100px")
	}
	// and the rule comes after the layouts it overrides
	if strings.LastIndex(css, "@media (max-width: 1100px)") < strings.Index(css, ".subcols {") {
		t.Error("the 1100px rule comes before the layouts it overrides")
	}
}

// The template page holds up zoomed in: the versions table and the draft panel
// stack before the table would scroll in a squeezed column, a version's notes wrap
// in their own column, on a phone the version's buttons go under its facts instead
// of taking a column that squeezed the version and its notes to nothing, and the
// editor's columns are a grid, so the results never wrap onto a line of their own.
func TestTemplatePagesHoldUpWhenZoomed(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		"@media (max-width: 1270px) {\n  .tgrid { grid-template-columns: minmax(0, 1fr); }\n}",
		`.tbl .row .clamp { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3;`,
		`.ed { flex: 1; display: grid; grid-template-columns: 230px minmax(0, 1fr) 330px;`,
		"@media (max-width: 1100px) {\n  .ed { grid-template-columns: 230px minmax(0, 1fr); }\n  .ed-side { grid-column: 1 / -1; border-left: 0; border-top: 1px solid var(--overlay); max-height: 40vh; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	if strings.Index(css, "@media (max-width: 1270px)") < strings.Index(css, ".tgrid {") {
		t.Error("the 1270px rule comes before the layout it overrides")
	}
	// the phone rules come after the narrower ones they override
	phone := regexp.MustCompile(`(?s)@media \(max-width: 760px\) \{[^@]*\.row\.ver \.actions \{ grid-column: 1 / -1; \}[^@]*\.ed \{ grid-template-columns: minmax\(0, 1fr\); \}`).FindStringIndex(css)
	if phone == nil {
		t.Fatal("on a phone the version's buttons or the editor keep their columns")
	}
	if phone[0] < strings.Index(css, "@media (max-width: 1100px) {\n  .ed {") || phone[0] < strings.Index(css, ".row.ver .actions { grid-column: 2 / -1;") {
		t.Error("the phone rules come before the rules they override")
	}
	if regexp.MustCompile(`\.ed-(left|main|side) \{[^}]*flex: [0-9]`).MatchString(css) {
		t.Error("an editor column still sizes itself as a flex item")
	}
}

// Validate's results come into view: under 1100 px the template editor puts
// them under the code, below the window, so pressing Validate seemed to do
// nothing. The answer to a [data-reveal] request is scrolled to (smoothly,
// unless the reader asked for less motion) when its start is out of view, and
// focused; on a phone its divider stays above it, as under 1100 px.
func TestValidationResultsComeIntoView(t *testing.T) {
	b, err := fs.ReadFile(ui.StaticFS, "js/proxier.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		`document.addEventListener('htmx:afterSettle'`,
		`cfg.elt.hasAttribute('data-reveal')`,
		`e.target !== t`, // settling fires once per swapped element, out-of-band ones too
		`(prefers-reduced-motion: reduce)`,
		`t.scrollIntoView({ block: 'start', behavior: still ? 'auto' : 'smooth' })`,
		`t.focus({ preventScroll: true })`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("proxier.js lacks %q", want)
		}
	}
	css := appCSS(t)
	if !strings.Contains(css, `.ed-side:focus-visible { outline-offset: -1px; }`) {
		t.Error("the focused results draw their ring outside the panel")
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 760px\) \{[^@]*\.ed \{ grid-template-columns: minmax\(0, 1fr\); \}[^@]*\}`).FindString(css)
	if !strings.Contains(phone, `.ed-side { border: 0; border-top: 1px solid var(--overlay); }`) {
		t.Errorf("on a phone the results lose the divider above them: %s", phone)
	}
}

// A job's log scrolls in its own box. It grew with its content, so on a
// server being provisioned the areas below the log were thousands of pixels
// down, and following new lines scrolled the whole page to the log's end.
func TestLogScrollsOnItsOwn(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		`.log { flex: 1; max-height: max(240px, 60vh); overflow-y: auto;`,
		`.jobmain { display: flex; flex-direction: column; min-width: 0; background: var(--base); }`,
		`.jobmain.log-full { height: calc(100vh - var(--keyline-h)); }`,
		`.jobmain.log-full .log { flex: 1 1 0; min-height: 0; max-height: none; }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// a wheel over the log goes on to the page at its ends
	if m := regexp.MustCompile(`(?m)^\.log \{[^}]*\}`).FindString(css); strings.Contains(m, "overscroll-behavior") {
		t.Errorf("the log traps the page's scrolling: %s", m)
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 760px\) \{[^@]*\.jobcols \{ grid-template-columns: minmax\(0, 1fr\); \}[^@]*\}`).FindString(css)
	if !strings.Contains(phone, `.jobmain.log-full { height: 100vh; }`) {
		t.Errorf("on a phone (no key line) the expanded log stops short of the window's bottom: %s", phone)
	}

	b, err := fs.ReadFile(ui.StaticFS, "js/proxier.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		`function logAtEnd(l) { return l.scrollHeight - l.scrollTop - l.clientHeight < 4; }`,
		`log.addEventListener('scroll'`, // scrolling up stops following, back at the end starts it
		`if (follow) log.scrollTop = log.scrollHeight;`,
		`e.target.matches('[data-log-follow]')`,
		`e.target.closest('[data-log-expand]')`,
		`sec.classList.toggle('log-full')`,
		`if (full) sec.scrollIntoView({ block: 'start' });`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("proxier.js lacks %q", want)
		}
	}
	// only the box moves: scrolling a line into view scrolled the page too
	if regexp.MustCompile(`(lg|l2|log)\.lastElementChild\.scrollIntoView`).MatchString(js) {
		t.Error("following the log scrolls the page")
	}

	// every log box has its tools in its head
	n := 0
	err = filepath.WalkDir(filepath.Join("..", ".."), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".templ") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, sec := range strings.Split(string(b), `<section class="jobmain"`)[1:] {
			sec = sec[:strings.Index(sec, "</section>")]
			if !strings.Contains(sec, "@ui.JobLog(") {
				continue
			}
			n++
			if !strings.Contains(sec, "@ui.LogTools(") {
				t.Errorf("%s: a log without Follow and Expand", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("only %d logs found", n)
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

// The page column has no padding of its own: tables, areas and forms bring
// theirs. A line of text placed straight in it (a note under a table, a stack
// diff's back line) sat flush against the sidebar, so text there
// carries a class that pads it like the rows (.foot, .pager, .area-body…).
func TestPageTextSitsInsideThePadding(t *testing.T) {
	css := appCSS(t)
	if !strings.Contains(css, `.foot { padding: 12px 16px; font-size: var(--fs-sm); line-height: 16px; color: var(--subtle); }`) {
		t.Error("app.css lacks the .foot note")
	}
	padded := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\.([a-z][a-z0-9-]*) \{[^}]*\bpadding: \d+px (\d+)px`).FindAllStringSubmatch(css, -1) {
		if m[2] != "0" {
			padded[m[1]] = true
		}
	}
	text := regexp.MustCompile(`^(<(p|span|a|small|strong|em|code|label)[ >]|\{ )`)
	class := regexp.MustCompile(`class="([^"]*)"`)
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
		for _, i := range pageColumnLines(lines) {
			n++
			l := strings.TrimSpace(lines[i])
			if !text.MatchString(l) {
				continue
			}
			ok := false
			if m := class.FindStringSubmatch(l); m != nil {
				for _, c := range strings.Fields(m[1]) {
					ok = ok || padded[c]
				}
			}
			if !ok {
				t.Errorf("%s:%d: text straight in the page column, without padding: %s", p, i+1, l)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Fatalf("only %d page-column lines found", n)
	}
}

// pageColumnLines returns the lines inside a ui.Layout or ui.SettingsLayout
// block that are its direct children, with nothing but if, for and switch
// blocks between.
func pageColumnLines(lines []string) []int {
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, "\t")) }
	control := regexp.MustCompile(`^(if |for |switch |case |default:|\} else|else)`)
	var out []int
	for i := 0; i < len(lines); i++ {
		if l := strings.TrimLeft(lines[i], "\t"); !strings.HasPrefix(l, "@ui.Layout(") && !strings.HasPrefix(l, "@ui.SettingsLayout(") {
			continue
		}
		base := indent(lines[i])
		type open struct {
			indent  int
			control bool
		}
		var stack []open
		j := i + 1
		for ; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "" {
				continue
			}
			in := indent(l)
			if in <= base {
				break
			}
			for len(stack) > 0 && stack[len(stack)-1].indent >= in {
				stack = stack[:len(stack)-1]
			}
			direct := true
			for _, o := range stack {
				direct = direct && o.control
			}
			if direct {
				out = append(out, j)
			}
			for k := j + 1; k < len(lines); k++ {
				if strings.TrimSpace(lines[k]) != "" {
					if indent(lines[k]) > in {
						stack = append(stack, open{in, control.MatchString(strings.TrimSpace(l))})
					}
					break
				}
			}
		}
		i = j - 1
	}
	return out
}

// The header ends with the admin's name and a small Sign out, not a menu. A
// long name (an email) gives way and ends in "…", sharing what is left with
// the search, and Sign out, a cell that never shrinks, stays on the screen at
// any width, also on a phone, where buttons may otherwise wrap.
func TestSignOutStaysInTheHeader(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		`.hdr-search { flex: 1 1 160px; min-width: 0;`,
		`.hdr-admin { min-width: 0; padding-right: 12px; }`,
		`.admin-name, .hdr-ph { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }`,
		`.admin-name { max-width: 32ch; color: var(--subtle); }`,
		`.hdr-out { flex: none; display: flex; align-items: center; padding-right: 16px; }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// on a phone a button's label may wrap; Sign out's must not, and the rule
	// comes after the one it overrides
	wrap, keep := strings.Index(css, `white-space: normal; text-align: center; }`), strings.Index(css, `.hdr-out .pc-btn { padding: 0 10px; white-space: nowrap; }`)
	if keep < 0 || keep < wrap {
		t.Error("on a phone Sign out may wrap")
	}
	b, err := fs.ReadFile(ui.StaticFS, "js/proxier.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"admin-pop", "admin-menu", "data-menu-toggle", "closeMenu"} {
		if strings.Contains(css, gone) || strings.Contains(string(b), gone) {
			t.Errorf("the account menu's %s is left over", gone)
		}
	}
}

// A row's grid has a cell for every column. The caret is placed absolutely,
// so it takes no cell: a column drawn for it was left empty and every cell
// moved one column to the left. The 16 px column of Discover's recent runs
// took the site and its line, which broke after every word and ran under the
// status.
func TestRowGridsHaveACellPerColumn(t *testing.T) {
	css := appCSS(t)
	// the grids outside @media (rules there are indented)
	tracks := map[string]int{}
	rule := regexp.MustCompile(`(?m)^([^\s@}][^{]*)\{([^}]*)\}`)
	cols := regexp.MustCompile(`grid-template-columns: ([^;]+);`)
	rowSel := regexp.MustCompile(`(?:^|[\s,])(?:[a-z]+)?(?:\.row)?\.([a-z0-9-]+)(?:[\s,]|$)`) // .row.jobrow or .jobrow
	for _, m := range rule.FindAllStringSubmatch(css, -1) {
		c := cols.FindStringSubmatch(m[2])
		if c == nil {
			continue
		}
		for _, s := range rowSel.FindAllStringSubmatch(strings.TrimSpace(m[1]), -1) {
			if _, ok := tracks[s[1]]; !ok {
				tracks[s[1]] = countTracks(c[1])
			}
		}
	}
	if tracks["disc-recent"] != 3 {
		t.Fatalf("Discover's recent runs are not the site, its state and its line: %d columns", tracks["disc-recent"])
	}
	// narrower, the line goes under the site instead of being cut
	if !regexp.MustCompile(`(?s)@media \(max-width: 1100px\) \{\n  \.row\.disc-recent \{ grid-template-columns: minmax\(0, 1fr\) 110px; \}\n  \.row\.disc-recent \.run-line \{ grid-column: 1 / -1; \}`).MatchString(css) {
		t.Error("under 1100px a recent run's line stays beside the site")
	}
	placedCaret := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.row\.([a-z0-9-]+) \.caret \{ position: static; \}`).FindAllStringSubmatch(css, -1) {
		placedCaret[m[1]] = true
	}

	open := regexp.MustCompile(`^\t*<(div|a|form|summary|li|label) [^>]*class="row ([a-z0-9-]+)[ "]`)
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
		for i, l := range lines {
			m := open.FindStringSubmatch(l)
			if m == nil || tracks[m[2]] == 0 {
				continue
			}
			cells, ok := rowCells(lines, i, placedCaret[m[2]])
			if !ok || !strings.Contains(strings.Join(lines[i:min(i+3, len(lines))], "\n"), `<span class="caret"`) {
				continue // a for loop makes the cells, or no caret: a dashboard row may leave a column of a shared grid empty
			}
			n++
			if cells < tracks[m[2]] {
				t.Errorf("%s:%d: .row.%s has %d columns for %d cells", p, i+1, m[2], tracks[m[2]], cells)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 20 {
		t.Fatalf("only %d rows with a caret found", n)
	}
}

// countTracks counts the columns of a grid-template-columns value.
func countTracks(v string) int {
	n, depth, in := 0, 0, false
	for _, r := range v {
		switch {
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ' ' && depth == 0:
			in = false
			continue
		}
		if !in {
			n++
			in = true
		}
	}
	return n
}

// rowCells counts the grid cells of the row opened on line i: its direct
// children, an absolutely placed caret left out, an if (and its else) counted
// as its larger branch. ok is false when a for loop makes them.
func rowCells(lines []string, i int, placedCaret bool) (cells int, ok bool) {
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, "\t")) }
	base := indent(lines[i])
	for !strings.HasSuffix(strings.TrimSpace(lines[i]), ">") { // the end of the opening tag
		i++
	}
	cell := func(l string) bool {
		switch {
		case strings.HasPrefix(l, `<span class="caret"`):
			return placedCaret
		case strings.HasPrefix(l, "</"), strings.HasPrefix(l, "//"), l == `{ " " }`:
			return false
		}
		return strings.HasPrefix(l, "<") || strings.HasPrefix(l, "@") || strings.HasPrefix(l, "{ ")
	}
	branch, best := 0, 0
	inIf := false
	for j := i + 1; j < len(lines); j++ {
		l := strings.TrimSpace(lines[j])
		in := indent(lines[j])
		if l == "" {
			continue
		}
		if in <= base {
			break
		}
		switch {
		case in == base+1 && strings.HasPrefix(l, "for "):
			return 0, false
		case in == base+1 && strings.HasPrefix(l, "if "):
			inIf, branch, best = true, 0, 0
		case in == base+1 && strings.HasPrefix(l, "} else"):
			best, branch = max(best, branch), 0
		case in == base+1 && l == "}" && inIf:
			cells += max(best, branch)
			inIf = false
		case in == base+1 && cell(l):
			cells++
		case in == base+2 && inIf && cell(l):
			branch++
		}
	}
	return cells, true
}

// A code editor spans the page column and, on a page whose job is the editor,
// fills the window under the title: the Shadowrocket base config was a 560 px
// column (.form) whose code clipped its long lines, with a 78vh box of its own
// inside a page that scrolled too. The editors have Wrap lines and Full
// screen; esc leaves the code first and full screen with the next one.
func TestCodeEditorsFillTheWindow(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		`.code-host .cm-editor, .ed-host .cm-editor { flex: 1 1 0; min-height: 0; }`,
		`.app:has(.code-fill) .side { contain: size; overflow-y: auto; }`, // a long nav doesn't stretch the editor
		`.code-fill { flex: 1 0 auto; display: flex; flex-direction: column; min-width: 0; }`,
		`.code-frame { flex: 1 1 0; min-height: 360px; display: flex; flex-direction: column; min-width: 0; }`,
		`.code-frame > .code-host, .code-frame > textarea { flex: 1 1 0; min-height: 0; }`,
		`.ed.code-fill { display: grid; flex: 1 1 0; min-height: 480px; grid-template-rows: minmax(0, 1fr); }`,
		`.ed-files { flex: 1; min-height: 0; overflow-y: auto; padding: 6px 0; }`,
		`.ed-host { flex: 1 1 0; min-height: 0;`,
		`.code-full .hdr, .code-full .side { display: none !important; }`,
		`.code-full .rs-area ~ :not(dialog) { display: none; }`,
		`.rs-draft > .code-host { flex: 1 1 auto; min-height: max(480px, 78vh); }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// the code runs flush, as the canvas draws the editors: no box inside the page's padding
	if regexp.MustCompile(`\.code-host \{[^}]*border`).MatchString(css) || regexp.MustCompile(`\.rs-draft \{[^}]*padding`).MatchString(css) {
		t.Error("the code sits in a bordered box inside padding")
	}
	stacked := regexp.MustCompile(`(?s)@media \(max-width: 1100px\) \{\s*\.ed \{ grid-template-columns: 230px minmax\(0, 1fr\); \}[^@]*\}`).FindString(css)
	if !strings.Contains(stacked, `.ed.code-fill { grid-template-rows: minmax(0, 1fr) auto; min-height: 560px; }`) {
		t.Errorf("under 1100 px the results push the code out of the window: %s", stacked)
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 760px\) \{[^@]*\.ed \{ grid-template-columns: minmax\(0, 1fr\); \}[^@]*\}`).FindString(css)
	if !strings.Contains(phone, `.ed.code-fill { flex: none; grid-template-rows: auto; min-height: 0; }`) || !strings.Contains(phone, `.ed-host { flex: none; height: max(320px, 70vh); }`) {
		t.Errorf("a phone squeezes the stacked editor into the window: %s", phone)
	}

	b, err := fs.ReadFile(ui.StaticFS, "js/proxier.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		`a.classList.toggle('code-full', on);`,
		`if (t.hasAttribute('data-code-full')) { setCodeFull(!codeFull()); return; }`,
		// a field (the code's own esc only leaves it) and a dialog keep esc
		`if (typing(e.target)) { e.target.blur(); setPending(false); return; }`,
		`if (codeFull() && !(e.target.closest && e.target.closest('dialog'))) { setCodeFull(false); e.preventDefault(); }`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("proxier.js lacks %q", want)
		}
	}
	// pop-ups close before full screen does
	if i, j := strings.Index(js, `if (closeKeys())`), strings.Index(js, `setCodeFull(false)`); i < 0 || j < i {
		t.Error("esc leaves full screen before closing the keys sheet")
	}
	b, err = fs.ReadFile(ui.StaticFS, "js/editor.bundle.js")
	if err != nil {
		t.Fatal(err)
	}
	bundle := string(b)
	for _, want := range []string{"proxier.code.wrap", "[data-code-wrap]", "[data-code-tools]"} {
		if !strings.Contains(bundle, want) {
			t.Errorf("the editor bundle lacks %q (make editor)", want)
		}
	}
	if strings.Contains(bundle, `maxHeight:"78vh"`) {
		t.Error("the editor bundle caps CodeMirror's height itself (make editor)")
	}

	// every code editor in the templates has the tools in its band
	n := 0
	err = filepath.WalkDir(filepath.Join("..", ".."), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".templ") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		src := string(b)
		eds := strings.Count(src, `data-code="`) + strings.Count(src, `id="ed-host"`)
		n += eds
		if eds > 0 && strings.Count(src, "@ui.CodeTools()") < eds {
			t.Errorf("%s: a code editor without Wrap lines and Full screen", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("only %d code editors found", n)
	}
}

// The Shadowrocket config page (issue 016): the URL breaks only after a
// slash; what the phone gets grows with the page instead of scrolling in a
// box of its own (two scrollbars, its end under the key line) and indents a
// wrapped line's continuation; a version's buttons keep 8 px; a fetch is one
// line; an action's button column fits its longest button.
func TestShadowrocketPageHoldsUp(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		`.urlfield code { font-family: inherit; overflow-wrap: anywhere; }`,
		`.subcols.sr-cols { grid-template-columns: minmax(0, 1.3fr) minmax(540px, 1fr); }`,
		`.sr-preview { background: var(--black); padding: 8px 0; font-size: var(--fs-sm); }`,
		`.sr-line .tx { white-space: pre-wrap; word-break: break-all; padding-left: 4ch; text-indent: -4ch; color: var(--subtle); }`,
		`.row.srver { grid-template-columns: 110px minmax(0, 1fr) auto;`,
		`.sr-acts { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }`,
		`.row.srfetch { grid-template-columns: 120px 130px minmax(0, 1fr); gap: 12px; font-size: var(--fs-sm); }`,
		`.row.act { grid-template-columns: minmax(190px, max-content) minmax(0, 1fr); }`,
		`.act-rows > .row.act { grid-column: 1 / -1; grid-template-columns: subgrid; }`,
		`.pc-title > .pc-btn:last-child { margin-left: auto; }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	for what, bad := range map[string]*regexp.Regexp{
		"the URL breaks inside a word":         regexp.MustCompile(`\.urlfield code \{[^}]*break-all`),
		"the preview scrolls in its own box":   regexp.MustCompile(`\.sr-preview \{[^}]*(max-height|overflow)`),
		"an action's button column is fixed":   regexp.MustCompile(`\.row\.act \{ grid-template-columns: \d+px`),
		"a fetch's cells wrap":                 regexp.MustCompile(`\.row\.srfetch > span \{[^}]*overflow-wrap`),
		"a version's buttons touch each other": regexp.MustCompile(`\.sr-acts \{[^}]*gap: [0-4]px`),
	} {
		if bad.MatchString(css) {
			t.Error(what)
		}
	}
	// stacked below 1270 px, versions, fetches and actions come before a
	// preview that may run thousands of lines; a phone puts a fetch's user
	// agent under its time and address, and its action rows in one column
	stacked := regexp.MustCompile(`(?s)@media \(max-width: 1270px\) \{\s*\.subcols\.sr-cols \{ grid-template-columns: minmax\(0, 1fr\); \}.*?\n\}`).FindString(css)
	if !strings.Contains(stacked, `.sr-cols > .col-r { order: -1; }`) {
		t.Errorf("stacked, the preview comes first: %q", stacked)
	}
	if strings.Index(css, `@media (max-width: 1270px) {
  .subcols.sr-cols`) < strings.Index(css, `.subcols.sr-cols { grid-template-columns: minmax(0, 1.3fr)`) {
		t.Error("the 1270 px block comes before the rules it overrides")
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 760px\) \{\s*\.colhead\.srcfg.*?\n\}`).FindString(css)
	if !strings.Contains(phone, `.row.srfetch { grid-template-columns: 120px minmax(0, 1fr); gap: 4px 12px; }`) || !strings.Contains(phone, `.row.srfetch .ua { grid-column: 1 / -1; }`) {
		t.Errorf("a phone's fetch: %q", phone)
	}
	if !regexp.MustCompile(`(?s)@media \(max-width: 760px\) \{[^@]*\.row\.act, \.row\.sublink \{[^}]*\}\s*\.act-rows \{ display: block; \}\s*\.act-rows > \.row\.act \{ grid-template-columns: minmax\(0, 1fr\); \}`).MatchString(css) {
		t.Error("on a phone the shared button column stays")
	}
}

// URLText lets a URL break only after a slash, never inside the scheme's
// "//", and escapes its parts.
func TestURLTextBreaksOnlyAfterSlashes(t *testing.T) {
	for in, want := range map[string]string{
		"https://proxier.example/r/tok-en_1/iphone.conf": "https://<wbr>proxier.example/<wbr>r/<wbr>tok-en_1/<wbr>iphone.conf",
		"http://proxier.test/s/••••":                     "http://<wbr>proxier.test/<wbr>s/<wbr>••••",
		"https://h/a&b<c>/":                              "https://<wbr>h/<wbr>a&amp;b&lt;c&gt;/",
		"":                                               "",
	} {
		var b strings.Builder
		if err := ui.URLText(in).Render(t.Context(), &b); err != nil {
			t.Fatal(err)
		}
		if b.String() != want {
			t.Errorf("%q: %q, want %q", in, b.String(), want)
		}
	}
}

// Content never hides behind the key line: it is sticky at the bottom and in
// the flow after the page column, so at the end of every page it sits under
// the content, never over it (a fixed key line would cover the page's end).
func TestKeyLineNeverCoversThePageEnd(t *testing.T) {
	b, err := fs.ReadFile(ui.StaticFS, "css/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	tokens := string(b)
	if !strings.Contains(tokens, `.pc-keys { position: sticky; bottom: 0;`) {
		t.Error("the key line isn't sticky at the bottom")
	}
	all := tokens + appCSS(t)
	if regexp.MustCompile(`(\.pc-keys|#keyline|\.keyline)[^{]*\{[^}]*position: fixed`).MatchString(all) {
		t.Error("the key line is fixed over the page")
	}
	var page strings.Builder
	cat := i18n.NewCatalog()
	ctx := i18n.WithLocalizer(t.Context(), cat.Localizer(i18n.EN, nil))
	if err := ui.Layout(ui.Shell{Title: "t", Lang: i18n.EN}).Render(ctx, &page); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)<div class="app" id="app">.*<div class="body">.*<main class="main" id="main">.*</main>\s*</div>\s*<footer class="pc-keys" id="keyline">`).MatchString(page.String()) {
		t.Error("the key line isn't in the flow right after the page column")
	}
}

// Every action row in the templates sits in .act-rows, so the rows of an
// Actions area share one button column as wide as their longest button.
func TestActionRowsShareAButtonColumn(t *testing.T) {
	actRows := regexp.MustCompile(`class="act-rows"`)
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
		for i, l := range lines {
			// an action row: a call of a page's actionRow, or one written out
			// (actionRow's own body, right under its templ line, is neither)
			call := strings.HasPrefix(strings.TrimSpace(l), "@actionRow(")
			literal := strings.Contains(l, `<div class="row act">`) && !strings.HasPrefix(lines[max(i-1, 0)], "templ actionRow(")
			if !call && !literal {
				continue
			}
			n++
			indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, "\t")) }
			inside := false
			for j := i - 1; j >= 0 && !strings.HasPrefix(lines[j], "templ "); j-- {
				if actRows.MatchString(lines[j]) && indent(lines[j]) < indent(l) {
					inside = true
					break
				}
				if strings.Contains(lines[j], "@ui.Area(") {
					break
				}
			}
			if !inside {
				t.Errorf("%s:%d: an action row outside .act-rows", p, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 {
		t.Fatalf("only %d action rows found", n)
	}
}
