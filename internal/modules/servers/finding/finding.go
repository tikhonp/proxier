// Package finding is the result type of every template checker: the manifest
// parser, the renderer and the validators. It is a leaf package (it imports
// nothing of the module) because manifest and render return findings while
// validate imports both.
package finding

import "fmt"

// Severity is how bad a finding is.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Finding is one problem, tied to a file (and a line when known).
type Finding struct {
	Severity Severity `json:"severity"`
	Check    string   `json:"check"` // "manifest" "parameters" "render" "endpoints" "xray" "compose" "nginx" "json" "yaml" "shell" "size"
	Path     string   `json:"path"`  // "" for global
	Line     int      `json:"line"`  // 0 when unknown
	Message  string   `json:"message"`
}

func (f Finding) String() string {
	loc := f.Path
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	if loc == "" {
		return fmt.Sprintf("%s: %s: %s", f.Severity, f.Check, f.Message)
	}
	return fmt.Sprintf("%s: %s: %s: %s", f.Severity, f.Check, loc, f.Message)
}

// Errorf makes an error finding.
func Errorf(check, path string, line int, format string, args ...any) Finding {
	return Finding{Error, check, path, line, fmt.Sprintf(format, args...)}
}

// Warnf makes a warning finding.
func Warnf(check, path string, line int, format string, args ...any) Finding {
	return Finding{Warning, check, path, line, fmt.Sprintf(format, args...)}
}

// Report is everything validation found.
type Report struct{ Findings []Finding }

// OK reports whether there is no error (warnings are allowed).
func (r Report) OK() bool {
	for _, f := range r.Findings {
		if f.Severity == Error {
			return false
		}
	}
	return true
}

// Errors returns the errors.
func (r Report) Errors() []Finding { return r.of(Error) }

// Warnings returns the warnings.
func (r Report) Warnings() []Finding { return r.of(Warning) }

func (r Report) of(s Severity) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// ByPath groups findings by file; "" holds the global ones.
func (r Report) ByPath() map[string][]Finding {
	out := map[string][]Finding{}
	for _, f := range r.Findings {
		out[f.Path] = append(out[f.Path], f)
	}
	return out
}
