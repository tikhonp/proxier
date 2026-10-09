package ui_test

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

func TestStaticHashIsTwelveHexDigits(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(ui.StaticHash) {
		t.Errorf("hash %q", ui.StaticHash)
	}
	if got := ui.Asset("css/app.css"); got != "/static/"+ui.StaticHash+"/css/app.css" {
		t.Errorf("asset %q", got)
	}
}

func TestActionButtonCarriesDataActionAndCSRF(t *testing.T) {
	cat := i18n.NewCatalog()
	_ = cat.Add("t", i18n.Messages{"t.go": {EN: "Go", RU: "Вперёд"}, "t.drop": {EN: "Drop", RU: "Удалить"}})
	ctx := ui.WithCSRF(i18n.WithLocalizer(t.Context(), cat.Localizer(i18n.EN, nil)), "tok123")

	var b bytes.Buffer
	if err := ui.ActionButton(ui.Action{ID: "t.go", Label: "t.go", Method: "POST", Href: "/go", Key: "r", Fields: map[string]string{"x": "1"}}).Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`data-action="t.go"`, `data-key="r"`, `name="_csrf" value="tok123"`, `name="x" value="1"`, `action="/go"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}

	b.Reset()
	err := ui.ActionButton(ui.Action{ID: "t.drop", Label: "t.drop", Method: "POST", Href: "/drop", Kind: ui.Danger,
		Confirm: &ui.Confirm{Strength: 3, Title: "Drop it", Body: "Gone for good.", Name: "de-1"}}).Render(ctx, &b)
	if err != nil {
		t.Fatal(err)
	}
	out = b.String()
	if !strings.Contains(out, `data-dialog-open="dlg-t.drop"`) || !strings.Contains(out, `data-confirm-name="de-1"`) || !strings.Contains(out, "data-confirm-submit") || !strings.Contains(out, "disabled") {
		t.Errorf("strength 3 dialog:\n%s", out)
	}
}

func TestHighlightEscapesAndNumbersLines(t *testing.T) {
	lines := ui.Highlight("x.yaml", []byte("name: \"<b>&\"\nlist:\n  - 1\n"))
	if len(lines) != 3 || lines[0].N != 1 || lines[2].N != 3 {
		t.Fatalf("lines: %+v", lines)
	}
	if !strings.Contains(lines[0].HTML, "&lt;b&gt;&amp;") || strings.Contains(lines[0].HTML, "<b>") || !strings.Contains(lines[0].HTML, `<span class="`) {
		t.Errorf("line 1: %s", lines[0].HTML)
	}
	// an unknown language and a file with markup stay plain and escaped
	plain := ui.Highlight("notes.txt", []byte("<script>alert(1)</script>\r\nsecond"))
	if len(plain) != 2 || plain[0].HTML != "&lt;script&gt;alert(1)&lt;/script&gt;" || plain[1].HTML != "second" {
		t.Errorf("plain: %+v", plain)
	}
	if got := ui.Highlight("empty.json", nil); got != nil {
		t.Errorf("an empty file: %+v", got)
	}
	for name, want := range map[string]string{
		"compose.yaml": "yaml", "a.YML": "yaml", "x.json": "json", "issue-cert.sh": "bash", "nginx/default.conf.template": "nginx",
		"nginx.conf": "nginx", ".env": "", "index.html": "", "manifest.yaml": "yaml",
	} {
		if got := ui.LangFor(name); got != want {
			t.Errorf("LangFor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseUnifiedAndSplitRows(t *testing.T) {
	diff := "--- a/f\n+++ b/f\n@@ -3,5 +3,6 @@\n keep\n-old one\n-old two\n+new one\n+new two\n+new three\n tail\n\\ No newline at end of file\n"
	hunks := ui.ParseUnified(diff)
	if len(hunks) != 1 || len(hunks[0].Lines) != 8 {
		t.Fatalf("hunks: %+v", hunks)
	}
	l := hunks[0].Lines
	if l[0].Kind != ' ' || l[0].Old != 3 || l[0].New != 3 ||
		l[1].Kind != '-' || l[1].Old != 4 || l[1].Text != "old one" ||
		l[3].Kind != '+' || l[3].New != 4 || l[5].New != 6 ||
		l[6].Kind != ' ' || l[6].Old != 6 || l[6].New != 7 || l[7].Kind != '\\' {
		t.Errorf("lines: %+v", l)
	}
	rows := hunks[0].Rows()
	// context, then two removed against two of three added, one added alone, context, note
	if len(rows) != 6 {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[1].Left.Text != "old one" || rows[1].Right.Text != "new one" || rows[3].Left.Kind != 0 || rows[3].Right.Text != "new three" {
		t.Errorf("pairing: %+v", rows)
	}
	var buf bytes.Buffer
	cat := i18n.NewCatalog()
	_ = cat.Add("t", i18n.Messages{"ui.diff.changed": {EN: "changed", RU: "изменён"}})
	ctx := i18n.WithLocalizer(t.Context(), cat.Localizer(i18n.EN, nil))
	for _, split := range []bool{false, true} {
		buf.Reset()
		if err := ui.Diff(ui.DiffView{Split: split, Files: []ui.DiffFile{{Path: "f", Change: "changed", Hunks: hunks}}}).Render(ctx, &buf); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		for _, want := range []string{"old one", "new three", "−", "+", "@@ -3,5 +3,6 @@"} {
			if !strings.Contains(out, want) {
				t.Errorf("split=%v lacks %q:\n%s", split, want, out)
			}
		}
		if split != strings.Contains(out, `class="drow"`) {
			t.Errorf("split=%v drow mismatch", split)
		}
	}
}

func TestQRIsAnSVGWithAQuietZone(t *testing.T) {
	var b bytes.Buffer
	if err := ui.QR("vless://uuid@host:443?type=xhttp#🇳🇱 Netherlands 1", "Connection QR code").Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"<svg", `class="qr"`, `aria-label="Connection QR code"`, `class="qr-fg"`, `class="qr-bg"`, "shape-rendering"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	// The quiet zone: no module is drawn in the first 4 rows or columns.
	m := regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`).FindStringSubmatch(out)
	if m == nil || m[1] != m[2] {
		t.Fatalf("viewBox: %v", m)
	}
	cells := regexp.MustCompile(`M(\d+) (\d+)h`).FindAllStringSubmatch(out, -1)
	if len(cells) == 0 {
		t.Fatal("no modules drawn")
	}
	for _, cell := range cells {
		x, _ := strconv.Atoi(cell[1])
		y, _ := strconv.Atoi(cell[2])
		if x < 4 || y < 4 {
			t.Fatalf("a module inside the quiet zone: %v", cell)
		}
	}
	// Nothing script-like or styled inline: the CSP forbids it.
	if strings.Contains(out, "style=") || strings.Contains(out, "<script") {
		t.Error("inline style or script in the QR")
	}
}

func TestRouterOSHighlight(t *testing.T) {
	if ui.LangFor("fresh-router.rsc") != "routeros" || ui.LangFor("X.RSC") != "routeros" {
		t.Fatal(".rsc is not routeros")
	}
	src := "# PARAMETERS\n# @fill subscription-link\n:local subUrl \"a\\\"b\\41\"\n:local n 3\n/ip address add address=($lanNet . \".1/24\") interface=$\"lan-if\"\n:local v [/system resource get version]\n"
	lines := ui.Highlight("fresh-router.rsc", []byte(src))
	if len(lines) != 6 {
		t.Fatalf("%d lines", len(lines))
	}
	for i, want := range []string{
		`<span class="c1"># PARAMETERS</span>`,
		`<span class="cs"># @fill subscription-link</span>`,
		`<span class="na">:local</span>`,
		`<span class="se">\&#34;</span>`,
		`<span class="se">\41</span>`,
		`<span class="m">3</span>`,
		`<span class="nb">/ip</span>`,
		`<span class="nv">$lanNet</span>`,
		`<span class="nv">$&#34;lan-if&#34;</span>`,
		`<span class="nb">/system</span>`,
	} {
		joined := ""
		for _, l := range lines {
			joined += l.HTML + "\n"
		}
		if !strings.Contains(joined, want) {
			t.Errorf("%d: no %s in\n%s", i, want, joined)
		}
	}
	if !strings.Contains(lines[2].HTML, `<span class="s">`) {
		t.Errorf("string: %s", lines[2].HTML)
	}
	if !strings.Contains(lines[4].HTML, `<span class="p">=</span>`) {
		t.Errorf("punctuation: %s", lines[4].HTML)
	}
}
