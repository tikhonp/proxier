package routeros

import (
	"fmt"
	"strconv"
	"strings"
)

// Part is a block, or one part of a split update, in a file.
type Part struct {
	Tag       string
	Remove    bool
	Part      int // 1.. of a split update; 1 of 1 otherwise
	Parts     int
	Continued bool // a later part: no tag-removal lines
	Domains   int
	Entries   []Entry // this part's names
}

// Last reports whether the part completes its block.
func (p Part) Last() bool { return p.Part == p.Parts }

// Describe is "update openai (+31)", "update big (part 2/3, +2000)" or "remove mine".
func (p Part) Describe() string {
	if p.Remove {
		return "remove " + p.Tag
	}
	if p.Parts > 1 {
		return fmt.Sprintf("update %s (part %d/%d, +%d)", p.Tag, p.Part, p.Parts, p.Domains)
	}
	return fmt.Sprintf("update %s (+%d)", p.Tag, p.Domains)
}

// File is one script pushed and imported.
type File struct {
	Name    string // proxier-sync-<job>-<n>.rsc
	Parts   []Part // the blocks (or block parts) in it, in order
	Domains int    // ≤ MaxDomains
	Body    []byte
}

// FileName is the n-th (from 1) file of a job ("2207" or "preview").
func FileName(job string, n int) string {
	return "proxier-sync-" + job + "-" + strconv.Itoa(n) + ".rsc"
}

// Files packs the blocks, in order, into files of at most MaxDomains names.
// A block that doesn't fit in the current file starts the next one; a block
// over MaxDomains is split over several files, every part after the first
// continued, so each file stays idempotent. Removals count as no names. Each
// file starts with "# <header> · file i/n: …" and each block with a comment.
func Files(n Names, job, header string, blocks []Block) []File {
	var files [][]Part
	var cur []Part
	used := 0
	flush := func() {
		if len(cur) > 0 {
			files = append(files, cur)
		}
		cur, used = nil, 0
	}
	for _, b := range blocks {
		if b.Remove {
			cur = append(cur, Part{Tag: b.Tag, Remove: true, Part: 1, Parts: 1})
			continue
		}
		e := append([]Entry(nil), b.Entries...)
		Sort(e)
		k := len(e)
		if k <= MaxDomains-used {
			cur = append(cur, Part{Tag: b.Tag, Part: 1, Parts: 1, Domains: k, Entries: e})
			used += k
			continue
		}
		flush()
		if k <= MaxDomains {
			cur = append(cur, Part{Tag: b.Tag, Part: 1, Parts: 1, Domains: k, Entries: e})
			used = k
			continue
		}
		parts := (k + MaxDomains - 1) / MaxDomains
		for i := 0; i < parts; i++ {
			chunk := e[i*MaxDomains : min((i+1)*MaxDomains, k)]
			cur = append(cur, Part{Tag: b.Tag, Part: i + 1, Parts: parts, Continued: i > 0, Domains: len(chunk), Entries: chunk})
			used = len(chunk)
			if i < parts-1 {
				flush()
			}
		}
	}
	flush()

	out := make([]File, 0, len(files))
	for i, parts := range files {
		f := File{Name: FileName(job, i+1), Parts: parts}
		desc := make([]string, 0, len(parts))
		for _, p := range parts {
			f.Domains += p.Domains
			desc = append(desc, p.Describe())
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# %s · file %d/%d: %s\n", header, i+1, len(files), strings.Join(desc, ", "))
		for _, p := range parts {
			b.WriteString("# " + p.Describe() + "\n")
			var lines []string
			if p.Remove {
				lines = RemovalBlock(n, p.Tag)
			} else {
				lines = ServiceBlock(n, p.Tag, p.Entries, p.Continued)
			}
			for _, l := range lines {
				b.WriteString(l + "\n")
			}
		}
		f.Body = []byte(b.String())
		out = append(out, f)
	}
	return out
}
