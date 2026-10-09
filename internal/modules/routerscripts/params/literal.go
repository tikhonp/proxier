package params

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// unquote reads the quoted literal at the start of s with RouterOS's
// escapes: its value and the length of the literal. ok is false for an
// unknown escape or a missing closing quote.
func unquote(s string) (string, int, bool) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), i + 1, true
		case '\\':
			if i+1 >= len(s) {
				return "", 0, false
			}
			e := s[i+1]
			if isHex(e) && i+2 < len(s) && isHex(s[i+2]) {
				b.WriteByte(hexVal(e)<<4 | hexVal(s[i+2]))
				i += 2
				continue
			}
			r, ok := escapes[e]
			if !ok {
				return "", 0, false
			}
			b.WriteByte(r)
			i++
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, false
}

// escapes are RouterOS's named string escapes.
var escapes = map[byte]byte{
	'"': '"', '\\': '\\', '$': '$', '?': '?',
	'n': '\n', 'r': '\r', 't': '\t', '_': ' ', 'a': 0x07, 'b': 0x08, 'f': 0x0c, 'v': 0x0b,
}

// isHex: RouterOS's \XX escape takes capital hex digits only, so \a and \b
// stay the bell and the backspace.
func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' }

func hexVal(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'A' + 10
}

var quoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, `?`, `\?`)

// Literal writes value as p's RouterOS literal. "?" is escaped too: "\?" is
// always a valid escape and a pasted line then never prompts for help.
func Literal(p Param, value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", ErrControl
	}
	if p.Bare {
		if !bareShape.MatchString(value) {
			return "", ErrBare
		}
		return value, nil
	}
	return `"` + quoteEscaper.Replace(value) + `"`, nil
}

// Check validates a value (trimmed) for p: the i18n key of the problem, or "".
func Check(p Param, value string) string {
	value = strings.TrimSpace(value)
	a := p.Annotations
	if value == "" && a.Required {
		return "params.err.required"
	}
	if value != "" && len(a.Choices) > 0 && !slices.Contains(a.Choices, value) {
		return "params.err.choice"
	}
	if value != "" && a.Pattern != "" {
		re, err := regexp.Compile(anchored(a.Pattern))
		if err != nil || !re.MatchString(value) {
			return "params.err.pattern"
		}
	}
	if _, err := Literal(p, value); err != nil {
		return err.Error()
	}
	return ""
}
