package services

import (
	"errors"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
)

// Custom is a new custom service.
type Custom struct{ Name, Tag, Description, Origin string }

// DomainRow is one row of the domain editor.
type DomainRow struct {
	Domain string // as typed or stored
	Exact  bool
	Note   string
}

// Edit is a save of the domain editor.
type Edit struct {
	Name, Tag, Description string
	Rows                   []DomainRow
}

// Report is what Paste many and Save say about the rows.
type Report struct {
	Added    []domain.Name
	Merged   []string // names given more than once, kept once
	Refused  []Refuse
	Punycode []domain.Name
}

// Refuse is a pasted line that isn't a domain; Reason is an i18n key.
type Refuse struct{ Line, Reason string }

// Saved is the result of SaveCustom.
type Saved struct {
	Changed bool
	Report  Report
}

// Limits of a custom service.
const (
	MaxRows        = 5000
	MaxNote        = 200
	MaxName        = 60
	MaxDescription = 1000
)

// reason is the i18n key of a Normalise error.
func reason(err error) string {
	switch {
	case errors.Is(err, domain.ErrIP):
		return "services.paste.ip"
	case errors.Is(err, domain.ErrUnsupported):
		return "services.paste.unsupported"
	}
	return "services.paste.invalid"
}

// Paste adds pasted lines to the table: each is normalised, the ones that
// aren't domains are refused, and the table is merged again. It is pure:
// the editor's Preview and Add to the table.
func (s *Service) Paste(text string, current []DomainRow) ([]DomainRow, Report) {
	var rep Report
	rows := normaliseRows(current)
	have := map[string]bool{}
	for _, r := range rows {
		have[r.Domain] = true
	}
	var pasted []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n, err := domain.Normalise(line)
		if err != nil {
			rep.Refused = append(rep.Refused, Refuse{Line: line, Reason: reason(err)})
			continue
		}
		if n.Unicode != "" {
			rep.Punycode = append(rep.Punycode, n)
		}
		rows = append(rows, DomainRow{Domain: n.Domain, Exact: n.Exact})
		pasted = append(pasted, n.Domain)
	}
	rows, rep.Merged = merge(rows)
	kept := map[string]bool{}
	for _, r := range rows {
		kept[r.Domain] = true
	}
	seen := map[string]bool{}
	for _, d := range pasted {
		if kept[d] && !have[d] && !seen[d] {
			seen[d] = true
			rep.Added = append(rep.Added, display(d))
		}
	}
	return rows, rep
}

// normaliseRows normalises the rows it can; the others stay as typed (Save
// refuses them).
func normaliseRows(in []DomainRow) []DomainRow {
	out := make([]DomainRow, 0, len(in))
	for _, r := range in {
		if strings.TrimSpace(r.Domain) == "" {
			continue
		}
		if n, err := domain.Normalise(r.Domain); err == nil {
			r.Domain, r.Exact = n.Domain, r.Exact || n.Exact
		}
		out = append(out, r)
	}
	return out
}

// merge keeps one row per domain (suffix wins over exact, the first note
// wins), in table order. A row under a suffix row stays: the router resolves
// only the names it is given (ADR 0016).
func merge(rows []DomainRow) ([]DomainRow, []string) {
	var merged []string
	idx := map[string]int{}
	var out []DomainRow
	for _, r := range rows {
		if i, ok := idx[r.Domain]; ok {
			merged = append(merged, r.Domain)
			out[i].Exact = out[i].Exact && r.Exact
			if out[i].Note == "" {
				out[i].Note = r.Note
			}
			continue
		}
		idx[r.Domain] = len(out)
		out = append(out, r)
	}
	return out, merged
}
