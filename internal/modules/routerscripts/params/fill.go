package params

import "fmt"

// Fill rewrites the literals of the parameters whose value differs from the
// default; every other byte, line terminators included, is copied. A name
// that isn't a parameter is ErrUnknown; a missing one keeps its default.
func Fill(body []byte, values map[string]string) ([]byte, error) {
	s := Parse(body)
	for name := range values {
		if _, ok := s.Param(name); !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknown, name)
		}
	}
	out := make([]byte, 0, len(body)+64)
	prev := 0
	// Params are in line order, so their offsets only grow.
	for _, p := range s.Params {
		v, ok := values[p.Name]
		if !ok || v == p.Default {
			continue
		}
		lit, err := Literal(p, v)
		if err != nil {
			return nil, fmt.Errorf("params: %s: %w", p.Name, err)
		}
		out = append(out, body[prev:p.at]...)
		out = append(out, lit...)
		prev = p.at + len(p.Literal)
	}
	return append(out, body[prev:]...), nil
}
