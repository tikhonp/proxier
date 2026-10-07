package validate

import (
	"sort"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
)

// Limits on the raw (unrendered) files.
const (
	MaxFile    = 1 << 20  // 1 MiB
	MaxVersion = 10 << 20 // 10 MiB
)

func sizeFindings(files map[string][]byte) []finding.Finding {
	var out []finding.Finding
	var total int
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		n := len(files[p])
		total += n
		if n > MaxFile {
			out = append(out, finding.Errorf("size", p, 0, "the file is %.1f MiB; a file may be at most 1 MiB", float64(n)/(1<<20)))
		}
	}
	if total > MaxVersion {
		out = append(out, finding.Errorf("size", "", 0, "the template is %.1f MiB; a version may be at most 10 MiB", float64(total)/(1<<20)))
	}
	return out
}
