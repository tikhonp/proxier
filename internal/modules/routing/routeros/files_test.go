package routeros

import (
	"fmt"
	"strings"
	"testing"
)

func names(prefix string, n int) []Entry {
	out := make([]Entry, n)
	for i := range out {
		out[i] = Entry{Name: fmt.Sprintf("%s%05d.com", prefix, i)}
	}
	return out
}

func TestFilesPackAndSplit(t *testing.T) {
	blocks := []Block{
		{Tag: "a", Entries: names("a", 1500)},
		{Tag: "b", Entries: names("b", 400)},
		{Tag: "c", Entries: names("c", 300)}, // doesn't fit beside a and b: next file
		{Tag: "big", Entries: names("g", 5995)},
		{Tag: "small", Entries: names("s", 5)},
		{Tag: "old", Remove: true},
	}
	fs := Files(Defaults, "2207", "proxier sync · router Home · job #2207", blocks)
	type want struct {
		parts   string
		domains int
	}
	wants := []want{
		{"a b", 1900},
		{"c", 300},
		{"big", 2000},
		{"big", 2000},
		{"big small old", 2000},
	}
	if len(fs) != len(wants) {
		for _, f := range fs {
			t.Logf("%s %d %d parts", f.Name, f.Domains, len(f.Parts))
		}
		t.Fatalf("files: %d", len(fs))
	}
	for i, f := range fs {
		var tags []string
		for _, p := range f.Parts {
			tags = append(tags, p.Tag)
		}
		if strings.Join(tags, " ") != wants[i].parts || f.Domains != wants[i].domains || f.Domains > MaxDomains {
			t.Fatalf("file %d: %v %d", i+1, tags, f.Domains)
		}
		if f.Name != fmt.Sprintf("proxier-sync-2207-%d.rsc", i+1) {
			t.Fatalf("name %s", f.Name)
		}
		if !strings.HasPrefix(string(f.Body), fmt.Sprintf("# proxier sync · router Home · job #2207 · file %d/5: ", i+1)) {
			t.Fatalf("header: %.120s", f.Body)
		}
		for _, p := range f.Parts {
			if !strings.Contains(string(f.Body), "\n# "+p.Describe()+"\n") {
				t.Fatalf("block comment %q missing", p.Describe())
			}
		}
	}
	// the 5 995 names: three parts, the second and third continued
	if p := fs[2].Parts[0]; p.Part != 1 || p.Parts != 3 || p.Continued {
		t.Fatalf("part 1: %+v", p)
	}
	for i, f := range []File{fs[3], fs[4]} {
		if p := f.Parts[0]; p.Part != i+2 || !p.Continued || strings.Contains(string(f.Body), `remove [find comment="big"`) {
			t.Fatalf("part %d: %+v", i+2, p)
		}
	}
	if !strings.HasPrefix(string(fs[0].Body), "# proxier sync · router Home · job #2207 · file 1/5: update a (+1500), update b (+400)\n") {
		t.Fatalf("header 1: %.200s", fs[0].Body)
	}
	if !strings.Contains(string(fs[4].Body), "file 5/5: update big (part 3/3, +1995), update small (+5), remove old\n") {
		t.Fatalf("header 5: %.200s", fs[4].Body)
	}
	// every file reads back
	for _, f := range fs {
		if _, err := ParseScript(f.Body); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
	}
	// exactly 6 000 names: three full files
	if fs := Files(Defaults, "1", "h", []Block{{Tag: "big", Entries: names("g", 6000)}}); len(fs) != 3 || fs[2].Domains != 2000 || !fs[2].Parts[0].Continued {
		t.Fatalf("6 000: %d files", len(fs))
	}
	// removals alone make one file
	if fs := Files(Defaults, "1", "h", []Block{{Tag: "x", Remove: true}, {Tag: "y", Remove: true}}); len(fs) != 1 || fs[0].Domains != 0 || len(fs[0].Parts) != 2 {
		t.Fatalf("removals: %+v", fs)
	}
}
