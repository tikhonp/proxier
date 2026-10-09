package params

import "strings"

// Eval gives the computed values it can: a bare $name, or a parenthesised
// concatenation (.) of quoted strings and $names set earlier in the block,
// from values (defaults for the names missing there). Anything else, such
// as [/system resource get version], has no value.
func Eval(s Script, values map[string]string) map[string]string {
	env := map[string]string{}
	out := map[string]string{}
	for _, g := range s.Groups {
		for _, it := range g.Items {
			if p := it.Param; p != nil {
				v, ok := values[p.Name]
				if !ok {
					v = p.Default
				}
				env[p.Name] = v
				continue
			}
			c := it.Computed
			if v, ok := evalExpr(c.Expr, env); ok {
				env[c.Name] = v
				out[c.Name] = v
			}
		}
	}
	return out
}

func evalExpr(e string, env map[string]string) (string, bool) {
	e = strings.TrimSpace(e)
	if strings.HasPrefix(e, "$") {
		v, rest, ok := variable(e, env)
		return v, ok && strings.TrimSpace(rest) == ""
	}
	if !strings.HasPrefix(e, "(") || !strings.HasSuffix(e, ")") {
		return "", false
	}
	e = e[1 : len(e)-1]
	var b strings.Builder
	for {
		e = strings.TrimLeft(e, " \t")
		var v string
		var ok bool
		switch {
		case strings.HasPrefix(e, `"`):
			var n int
			v, n, ok = unquote(e)
			e = e[min(n, len(e)):]
		case strings.HasPrefix(e, "$"):
			v, e, ok = variable(e, env)
		}
		if !ok {
			return "", false
		}
		b.WriteString(v)
		e = strings.TrimLeft(e, " \t")
		if e == "" {
			return b.String(), true
		}
		if e[0] != '.' {
			return "", false
		}
		e = e[1:]
	}
}

// variable reads $name or $"name" at the start of e: its value and the rest.
func variable(e string, env map[string]string) (string, string, bool) {
	e = e[1:]
	var name string
	if strings.HasPrefix(e, `"`) {
		v, n, ok := unquote(e)
		if !ok {
			return "", "", false
		}
		name, e = v, e[n:]
	} else {
		name = nameShape.FindString(e)
		e = e[len(name):]
	}
	v, ok := env[name]
	return v, e, ok && name != ""
}
