package validate

import (
	"bytes"
	"errors"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"mvdan.cc/sh/v3/syntax"
)

// shellFindings is `bash -n` in Go: the file must parse as bash.
func shellFindings(f render.RenderedFile) []finding.Finding {
	p := syntax.NewParser(syntax.Variant(syntax.LangBash))
	if _, err := p.Parse(bytes.NewReader(f.Content), f.Path); err != nil {
		var pe syntax.ParseError
		if errors.As(err, &pe) {
			return []finding.Finding{errorf("shell", f, int(pe.Pos.Line()), "%s (column %d)", pe.Text, pe.Pos.Col())}
		}
		var le syntax.LangError
		if errors.As(err, &le) {
			return []finding.Finding{errorf("shell", f, int(le.Pos.Line()), "%s", le.Error())}
		}
		return []finding.Finding{errorf("shell", f, 0, "%v", err)}
	}
	return nil
}
