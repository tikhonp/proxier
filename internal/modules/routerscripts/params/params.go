// Package params reads a router script's parameters from the script itself
// (ADR 0011): the literal :local values of its PARAMETERS block, their
// descriptions and annotations, the computed values, and the problems a
// publish has to show. It writes values back as RouterOS literals, changing
// nothing else in the file. Pure: no database, no clock.
package params

import (
	"errors"
	"slices"
)

// Severities of a finding.
const (
	Error   = "error"
	Warning = "warning"
)

// The @fill kinds.
const (
	FillLink      = "subscription-link"
	FillList      = "routing-address-list"
	FillForwarder = "routing-doh-forwarder"
	FillKey       = "proxier-ssh-key"
)

// FillKinds are the known @fill kinds, in the order the docs list them.
var FillKinds = []string{FillLink, FillList, FillForwarder, FillKey}

// ErrUnknown: Fill was given a name that isn't a parameter of the script.
var ErrUnknown = errors.New("params: not a parameter of the script")

// Errors of Literal; their texts are the i18n keys.
var (
	ErrControl = errors.New("params.err.control")
	ErrBare    = errors.New("params.err.bare")
)

// Annotations refine a parameter's form field.
type Annotations struct {
	Secret   bool
	Required bool
	Choices  []string
	Pattern  string // as written
	Fill     string // a Fill kind or ""
}

// Param is a script parameter: a :local line of the block with one literal.
type Param struct {
	Name        string
	Line        int    // 1-based
	Default     string // the literal's value, escapes resolved
	Literal     string // as written: `"10.230.1"`, `7024005`
	Bare        bool
	Description string
	Annotations Annotations

	at int // the literal's byte offset in the body
}

// Computed is a :local line of the block whose value isn't one literal.
type Computed struct {
	Name        string
	Line        int
	Expr        string // as written after the name; "" for none
	Description string
}

// Item is one parameter or computed value, in script order.
type Item struct {
	Param    *Param
	Computed *Computed
}

// Name is the item's name.
func (it Item) Name() string {
	if it.Param != nil {
		return it.Param.Name
	}
	return it.Computed.Name
}

// Description is the item's description.
func (it Item) Description() string {
	if it.Param != nil {
		return it.Param.Description
	}
	return it.Computed.Description
}

// Group is a run of items between blank lines.
type Group struct {
	Heading string // the group's only description, when it belongs to its first item
	Items   []Item
}

// Finding is a problem of a script. Key is an i18n key under "params.".
type Finding struct {
	Line     int            `json:"line"`
	Severity string         `json:"severity"`
	Key      string         `json:"key"`
	Args     map[string]any `json:"args,omitempty"`
}

// Script is what Parse reads from a body.
type Script struct {
	Block    bool // a PARAMETERS line exists
	Start    int  // its line; 0 without one
	End      int  // the end marker's line, or the last line read without one
	Ended    bool // closed by # END PARAMETERS
	Params   []Param
	Computed []Computed
	Groups   []Group
	Findings []Finding // what the body alone shows, by line
}

// Param finds a parameter by name.
func (s Script) Param(name string) (Param, bool) {
	for _, p := range s.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Errors are the findings that block publishing.
func (s Script) Errors() []Finding { return bySeverity(s.Findings, Error) }

// Warnings are the findings a publish must confirm.
func (s Script) Warnings() []Finding { return bySeverity(s.Findings, Warning) }

func bySeverity(fs []Finding, sev string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == sev {
			out = append(out, f)
		}
	}
	return out
}

// Values is every parameter's default.
func (s Script) Values() map[string]string {
	out := make(map[string]string, len(s.Params))
	for _, p := range s.Params {
		out[p.Name] = p.Default
	}
	return out
}

// Changed lists, in script order, the parameters whose value differs from
// the default.
func Changed(s Script, values map[string]string) []string {
	var out []string
	for _, p := range s.Params {
		if v, ok := values[p.Name]; ok && v != p.Default {
			out = append(out, p.Name)
		}
	}
	return out
}

// Gone is a warning per parameter of base (version n) that s doesn't have.
func Gone(base Script, n int, s Script) []Finding {
	var out []Finding
	for _, p := range base.Params {
		if _, ok := s.Param(p.Name); !ok {
			out = append(out, Finding{Severity: Warning, Key: "params.warn.gone", Args: map[string]any{"name": p.Name, "version": n}})
		}
	}
	return out
}

// sortFindings orders findings by line, keeping the order of equal lines.
func sortFindings(fs []Finding) {
	slices.SortStableFunc(fs, func(a, b Finding) int { return a.Line - b.Line })
}
