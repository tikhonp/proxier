package shadowrocket_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
)

var at = time.Date(2026, 10, 5, 4, 12, 33, 0, time.UTC)

var blocks = []shadowrocket.Block{
	{Tag: "anthropic", Rules: []shadowrocket.Rule{{Name: "anthropic.com"}, {Name: "claude.ai"}}},
	{Tag: "youtube", Rules: []shadowrocket.Rule{{Name: "youtube.com"}, {Name: "youtubei.googleapis.com", Exact: true}}},
}

const ours = "\n" +
	"# Services from routing list \"Main\", by Proxier (2026-10-05 04:12 UTC)\n" +
	"\n# anthropic\nDOMAIN-SUFFIX,anthropic.com,PROXY\nDOMAIN-SUFFIX,claude.ai,PROXY\n" +
	"\n# youtube\nDOMAIN-SUFFIX,youtube.com,PROXY\nDOMAIN,youtubei.googleapis.com,PROXY\n"

func render(t *testing.T, base string) string {
	t.Helper()
	out, err := shadowrocket.Render([]byte(base), "Main", blocks, "PROXY", at)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestInsertBeforeFinal(t *testing.T) {
	head := "[General]\nbypass-system = true\ndns-server = system\n\n[Rule]\nRULE-SET,https://example.com/ads.list,REJECT\nRULE-SET,https://example.com/ru.list,DIRECT\n"
	tail := "FINAL,DIRECT\n\n[Host]\nlocalhost = 127.0.0.1\n"
	if got, want := render(t, head+tail), head+ours+tail; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	// a lower-case header and final are found too; only the first FINAL counts
	base := "[rule]\nDOMAIN,a.example,DIRECT\nfinal,proxy\nFINAL,DIRECT\n"
	want := "[rule]\nDOMAIN,a.example,DIRECT\n" + ours + "final,proxy\nFINAL,DIRECT\n"
	if got := render(t, base); got != want {
		t.Errorf("lower case:\n%s", got)
	}
}

func TestNoFinalAddsOne(t *testing.T) {
	base := "[General]\nloglevel = notify\n[Rule]\nRULE-SET,https://example.com/ads.list,REJECT\n[Host]\nlocalhost = 127.0.0.1\n"
	want := "[General]\nloglevel = notify\n[Rule]\nRULE-SET,https://example.com/ads.list,REJECT\n" + ours + "FINAL,DIRECT\n\n[Host]\nlocalhost = 127.0.0.1\n"
	if got := render(t, base); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	// [Rule] last in the file, its last line without a terminator
	base = "[Rule]\nDOMAIN,a.example,DIRECT"
	want = "[Rule]\nDOMAIN,a.example,DIRECT\n" + ours + "FINAL,DIRECT\n"
	if got := render(t, base); got != want {
		t.Errorf("at the end:\n%q", got)
	}
	// an empty [Rule]
	if got := render(t, "[Rule]\n"); got != "[Rule]\n"+ours+"FINAL,DIRECT\n" {
		t.Errorf("empty:\n%q", got)
	}
}

func TestBaseCopiedByteForByte(t *testing.T) {
	// trailing blank lines of [Rule] stay below the block
	base := "# my config ; v3\n[General]\n  ipv6 = false  \n\n[Rule]\n# ads first\nRULE-SET,https://example.com/ads.list,REJECT\n\n\n[Host]\n"
	want := "# my config ; v3\n[General]\n  ipv6 = false  \n\n[Rule]\n# ads first\nRULE-SET,https://example.com/ads.list,REJECT\n" + ours + "FINAL,DIRECT\n\n\n[Host]\n"
	if got := render(t, base); got != want {
		t.Errorf("blank lines:\n%q\nwant\n%q", got, want)
	}
	// a CRLF base gets CRLF lines and keeps its own
	crlf := strings.ReplaceAll("[General]\r\nx = 1\r\n\r\n[Rule]\r\nRULE-SET,https://example.com/a.list,REJECT\r\nFINAL,DIRECT\r\n", "\r\n", "\r\n")
	got := render(t, crlf)
	wantCRLF := "[General]\r\nx = 1\r\n\r\n[Rule]\r\nRULE-SET,https://example.com/a.list,REJECT\r\n" + strings.ReplaceAll(ours, "\n", "\r\n") + "FINAL,DIRECT\r\n"
	if got != wantCRLF {
		t.Errorf("crlf:\n%q\nwant\n%q", got, wantCRLF)
	}
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Error("a bare LF in a CRLF output")
	}
	// removing the block gives the base back
	if strings.Replace(render(t, base), ours+"FINAL,DIRECT\n", "", 1) != base {
		t.Error("the base is not copied byte for byte")
	}
	if err := shadowrocket.Validate([]byte(base)); err != nil {
		t.Errorf("valid base: %v", err)
	}
}

func TestBaseNeedsRuleSection(t *testing.T) {
	for _, base := range []string{"", "[General]\nx = 1\n", "[Rules]\n", "# [Rule]\n", "RULE-SET,x,REJECT\n"} {
		if _, err := shadowrocket.Render([]byte(base), "Main", blocks, "PROXY", at); !errors.Is(err, shadowrocket.ErrNoRule) {
			t.Errorf("%q: render %v", base, err)
		}
		if err := shadowrocket.Validate([]byte(base)); !errors.Is(err, shadowrocket.ErrNoRule) {
			t.Errorf("%q: validate %v", base, err)
		}
	}
	if err := shadowrocket.Validate([]byte("  [RULE]  \n")); err != nil {
		t.Errorf("[RULE] with spaces: %v", err)
	}
	big := "[Rule]\n" + strings.Repeat("#", shadowrocket.MaxBase)
	if err := shadowrocket.Validate([]byte(big)); !errors.Is(err, shadowrocket.ErrTooBig) {
		t.Errorf("too big: %v", err)
	}
	if err := shadowrocket.Validate([]byte("[Rule]\n\xff\n")); !errors.Is(err, shadowrocket.ErrNotUTF8) {
		t.Errorf("not UTF-8: %v", err)
	}
}

func TestPolicyAndHeader(t *testing.T) {
	out, err := shadowrocket.Render([]byte("[Rule]\nFINAL,DIRECT\n"), "Parents", blocks, "Proxy-Group-A", at.In(time.FixedZone("MSK", 3*3600)))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "# Services from routing list \"Parents\", by Proxier (2026-10-05 04:12 UTC)\n") {
		t.Errorf("header:\n%s", s)
	}
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "DOMAIN") {
			n++
			if !strings.HasSuffix(l, ",Proxy-Group-A") {
				t.Errorf("rule %q", l)
			}
		}
	}
	if n != 4 {
		t.Errorf("%d rules", n)
	}
}
